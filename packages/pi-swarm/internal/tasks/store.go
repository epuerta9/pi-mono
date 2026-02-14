package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/oklog/ulid/v2"
)

// Store provides CRUD operations on the shared task KV bucket.
// All mutations use Compare-And-Swap (CAS) to prevent lost updates
// when multiple agents race on the same task.
type Store struct {
	kv      jetstream.KeyValue
	nc      *nats.Conn
	agentID string
}

// NewStore creates a task store backed by the SWARM_TASKS KV bucket.
func NewStore(js jetstream.JetStream, nc *nats.Conn, agentID string) (*Store, error) {
	kv, err := js.KeyValue(context.Background(), "SWARM_TASKS")
	if err != nil {
		return nil, fmt.Errorf("open SWARM_TASKS: %w", err)
	}
	return &Store{kv: kv, nc: nc, agentID: agentID}, nil
}

// Create adds a new task to the swarm. Returns the created task with
// its generated ID and initial revision.
func (s *Store) Create(ctx context.Context, title, description string, opts ...TaskOption) (*Task, error) {
	task := &Task{
		ID:          ulid.Make().String(),
		Title:       title,
		Description: description,
		Status:      TaskPending,
		Priority:    1,
		CreatedBy:   s.agentID,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	for _, opt := range opts {
		opt(task)
	}

	data, err := json.Marshal(task)
	if err != nil {
		return nil, fmt.Errorf("marshal task: %w", err)
	}

	rev, err := s.kv.Create(ctx, taskKey(task.ID), data)
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	task.Revision = rev

	s.publishEvent(task.ID, "created", task)
	return task, nil
}

// Get retrieves a task by ID.
func (s *Store) Get(ctx context.Context, taskID string) (*Task, error) {
	entry, err := s.kv.Get(ctx, taskKey(taskID))
	if err != nil {
		return nil, fmt.Errorf("get task %s: %w", taskID, err)
	}
	var task Task
	if err := json.Unmarshal(entry.Value(), &task); err != nil {
		return nil, fmt.Errorf("unmarshal task: %w", err)
	}
	task.Revision = entry.Revision()
	return &task, nil
}

// List returns all tasks in the swarm.
func (s *Store) List(ctx context.Context) ([]*Task, error) {
	keys, err := s.kv.Keys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list task keys: %w", err)
	}

	tasks := make([]*Task, 0, len(keys))
	for _, key := range keys {
		entry, err := s.kv.Get(ctx, key)
		if err != nil {
			continue
		}
		var task Task
		if err := json.Unmarshal(entry.Value(), &task); err != nil {
			continue
		}
		task.Revision = entry.Revision()
		tasks = append(tasks, &task)
	}
	return tasks, nil
}

// Claim atomically claims an unclaimed task for this agent.
// Uses CAS to ensure only one agent wins the race.
func (s *Store) Claim(ctx context.Context, taskID string) (*Task, error) {
	task, err := s.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != TaskPending {
		return nil, fmt.Errorf("task %s is %s, not claimable", taskID, task.Status)
	}

	task.Status = TaskClaimed
	task.ClaimedBy = s.agentID
	task.UpdatedAt = time.Now().UTC()

	rev, err := s.casUpdate(ctx, task)
	if err != nil {
		return nil, fmt.Errorf("claim task %s (CAS conflict — another agent claimed it first?): %w", taskID, err)
	}
	task.Revision = rev

	s.publishEvent(taskID, "claimed", map[string]string{
		"agent_id": s.agentID,
		"task_id":  taskID,
	})
	return task, nil
}

// Activate marks a claimed task as actively being worked on.
func (s *Store) Activate(ctx context.Context, taskID string) (*Task, error) {
	task, err := s.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.ClaimedBy != s.agentID {
		return nil, fmt.Errorf("task %s claimed by %s, not us (%s)", taskID, task.ClaimedBy, s.agentID)
	}
	if task.Status != TaskClaimed {
		return nil, fmt.Errorf("task %s is %s, expected claimed", taskID, task.Status)
	}

	task.Status = TaskActive
	task.UpdatedAt = time.Now().UTC()

	rev, err := s.casUpdate(ctx, task)
	if err != nil {
		return nil, err
	}
	task.Revision = rev
	return task, nil
}

// Complete marks a task as done with a result summary.
func (s *Store) Complete(ctx context.Context, taskID, result string) error {
	task, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if task.ClaimedBy != s.agentID {
		return fmt.Errorf("task %s claimed by %s, not us (%s)", taskID, task.ClaimedBy, s.agentID)
	}

	task.Status = TaskDone
	task.Result = result
	task.UpdatedAt = time.Now().UTC()

	if _, err := s.casUpdate(ctx, task); err != nil {
		return fmt.Errorf("complete task %s: %w", taskID, err)
	}

	s.publishEvent(taskID, "completed", map[string]string{
		"agent_id": s.agentID,
		"task_id":  taskID,
		"result":   result,
	})
	return nil
}

// Fail marks a task as failed with an error description.
func (s *Store) Fail(ctx context.Context, taskID, reason string) error {
	task, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if task.ClaimedBy != s.agentID {
		return fmt.Errorf("task %s claimed by %s, not us (%s)", taskID, task.ClaimedBy, s.agentID)
	}

	task.Status = TaskFailed
	task.Result = reason
	task.UpdatedAt = time.Now().UTC()

	if _, err := s.casUpdate(ctx, task); err != nil {
		return fmt.Errorf("fail task %s: %w", taskID, err)
	}

	s.publishEvent(taskID, "failed", map[string]string{
		"agent_id": s.agentID,
		"task_id":  taskID,
		"error":    reason,
	})
	return nil
}

// Release gives up a claimed task so another agent can pick it up.
func (s *Store) Release(ctx context.Context, taskID, reason string) error {
	task, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if task.ClaimedBy != s.agentID {
		return fmt.Errorf("task %s claimed by %s, not us (%s)", taskID, task.ClaimedBy, s.agentID)
	}

	task.Status = TaskPending
	task.ClaimedBy = ""
	task.UpdatedAt = time.Now().UTC()

	if _, err := s.casUpdate(ctx, task); err != nil {
		return fmt.Errorf("release task %s: %w", taskID, err)
	}

	s.publishEvent(taskID, "released", map[string]string{
		"agent_id": s.agentID,
		"task_id":  taskID,
		"reason":   reason,
	})
	return nil
}

// Progress publishes a progress update for a task without modifying
// the KV entry. Other agents watching the subject see the update.
func (s *Store) Progress(ctx context.Context, taskID, message string) {
	s.publishEvent(taskID, "progress", map[string]string{
		"agent_id": s.agentID,
		"task_id":  taskID,
		"message":  message,
	})
}

// Watch returns a channel of TaskEvents for all task mutations.
// The channel closes when the context is cancelled.
func (s *Store) Watch(ctx context.Context) (<-chan TaskEvent, error) {
	watcher, err := s.kv.WatchAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("watch tasks: %w", err)
	}

	ch := make(chan TaskEvent, 64)
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case entry := <-watcher.Updates():
				if entry == nil {
					continue // initial values sentinel
				}
				var task Task
				if err := json.Unmarshal(entry.Value(), &task); err != nil {
					continue
				}
				task.Revision = entry.Revision()
				ch <- TaskEvent{
					Operation: entry.Operation().String(),
					Task:      task,
				}
			}
		}
	}()
	return ch, nil
}

// casUpdate performs a Compare-And-Swap update on the task's KV entry.
func (s *Store) casUpdate(ctx context.Context, task *Task) (uint64, error) {
	data, err := json.Marshal(task)
	if err != nil {
		return 0, fmt.Errorf("marshal task: %w", err)
	}
	return s.kv.Update(ctx, taskKey(task.ID), data, task.Revision)
}

// publishEvent publishes a lifecycle event on the task's NATS subject.
func (s *Store) publishEvent(taskID, event string, payload any) {
	data, _ := json.Marshal(payload)
	subject := fmt.Sprintf("swarm.tasks.%s.%s", taskID, event)
	_ = s.nc.Publish(subject, data)
}

func taskKey(id string) string {
	return "tasks." + id
}

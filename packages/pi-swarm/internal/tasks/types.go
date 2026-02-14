package tasks

import "time"

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskPending TaskStatus = "pending"  // available for claiming
	TaskClaimed TaskStatus = "claimed"  // agent intends to work on it
	TaskActive  TaskStatus = "active"   // agent is actively executing
	TaskDone    TaskStatus = "done"     // completed successfully
	TaskFailed  TaskStatus = "failed"   // completed with failure
)

// Task is the core unit of work in the swarm.
type Task struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      TaskStatus `json:"status"`
	Priority    int        `json:"priority"`    // 0=low, 1=normal, 2=high, 3=critical
	CreatedBy   string     `json:"created_by"`  // agent ID
	ClaimedBy   string     `json:"claimed_by"`  // agent ID (empty if unclaimed)
	ParentID    string     `json:"parent_id"`   // for sub-tasks
	Tags        []string   `json:"tags"`
	Result      string     `json:"result"`      // outcome summary when done/failed
	DependsOn   []string   `json:"depends_on"`  // task IDs that must complete first
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Revision    uint64     `json:"-"` // NATS KV revision for CAS
}

// TaskEvent represents a change to a task, emitted by the KV watcher.
type TaskEvent struct {
	Operation string // "PUT" or "DEL"
	Task      Task
}

// TaskOption is a functional option for task creation.
type TaskOption func(*Task)

// WithPriority sets the task priority.
func WithPriority(p int) TaskOption {
	return func(t *Task) { t.Priority = p }
}

// WithParent sets the parent task ID (for sub-tasks).
func WithParent(parentID string) TaskOption {
	return func(t *Task) { t.ParentID = parentID }
}

// WithTags sets the task tags.
func WithTags(tags ...string) TaskOption {
	return func(t *Task) { t.Tags = tags }
}

// WithDependencies sets the task dependency chain.
func WithDependencies(deps ...string) TaskOption {
	return func(t *Task) { t.DependsOn = deps }
}

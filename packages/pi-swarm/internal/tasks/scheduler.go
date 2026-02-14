package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mariozechner/pi-mono/packages/pi-swarm/internal/presence"
)

// Scheduler watches for pending tasks and assigns them to available
// agents based on capabilities and dependency resolution.
//
// Only one scheduler runs at a time across the swarm — leadership is
// determined by a distributed lock in SWARM_LOCKS.
type Scheduler struct {
	tasks    *Store
	presence *presence.Tracker
	locks    jetstream.KeyValue
	nc       *nats.Conn
	agentID  string
	log      *slog.Logger
}

// NewScheduler creates a scheduler. Call Run() to start it.
func NewScheduler(tasks *Store, pres *presence.Tracker, js jetstream.JetStream, nc *nats.Conn, agentID string) (*Scheduler, error) {
	locks, err := js.KeyValue(context.Background(), "SWARM_LOCKS")
	if err != nil {
		return nil, fmt.Errorf("open SWARM_LOCKS: %w", err)
	}
	return &Scheduler{
		tasks:    tasks,
		presence: pres,
		locks:    locks,
		nc:       nc,
		agentID:  agentID,
		log:      slog.Default().With("component", "scheduler"),
	}, nil
}

// Run attempts to acquire the leader lock, then runs the scheduling
// loop. If another node is already leader, this blocks until it can
// acquire the lock (leader failover).
func (s *Scheduler) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Try to acquire leader lock
		_, err := s.locks.Create(ctx, "scheduler.leader", []byte(s.agentID))
		if err != nil {
			// Another agent is leader — wait and retry
			s.log.Debug("another agent is scheduler leader, waiting")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Second):
				continue
			}
		}

		s.log.Info("acquired scheduler leadership", "agent", s.agentID)

		// We are leader — run the scheduling loop
		if err := s.runLoop(ctx); err != nil {
			s.log.Error("scheduler loop exited", "error", err)
		}

		// Leadership lost or context cancelled — loop back to try again
	}
}

func (s *Scheduler) runLoop(ctx context.Context) error {
	taskEvents, err := s.tasks.Watch(ctx)
	if err != nil {
		return err
	}

	// Renew leader lock periodically
	renewTicker := time.NewTicker(20 * time.Second)
	defer renewTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-renewTicker.C:
			// Renew the lock by putting our agent ID
			if _, err := s.locks.Put(ctx, "scheduler.leader", []byte(s.agentID)); err != nil {
				return fmt.Errorf("renew leader lock: %w", err)
			}

		case te, ok := <-taskEvents:
			if !ok {
				return fmt.Errorf("task watcher closed")
			}
			if te.Task.Status == TaskPending {
				s.tryAssign(ctx, te.Task)
			}
		}
	}
}

// tryAssign attempts to assign a pending task to an idle agent.
func (s *Scheduler) tryAssign(ctx context.Context, task Task) {
	// Check that all dependencies are satisfied
	for _, depID := range task.DependsOn {
		dep, err := s.tasks.Get(ctx, depID)
		if err != nil || dep.Status != TaskDone {
			s.log.Debug("task dependency not met",
				"task", task.ID,
				"dependency", depID,
			)
			return
		}
	}

	// Find idle agents
	agents := s.presence.GetByStatus("idle")
	if len(agents) == 0 {
		s.log.Debug("no idle agents for task", "task", task.ID)
		return
	}

	// Pick the best match (for now: first idle agent)
	for _, agent := range agents {
		if agent.AgentID == task.CreatedBy {
			// Prefer not assigning back to creator (they may be busy with user)
			continue
		}
		s.requestClaim(ctx, agent.AgentID, task.ID)
		return
	}

	// Fallback: assign to creator if they're the only one idle
	if agents[0].Status == "idle" {
		s.requestClaim(ctx, agents[0].AgentID, task.ID)
	}
}

// requestClaim sends an RPC request to a specific agent asking it to
// claim a task. Uses NATS request/reply for reliable delivery.
func (s *Scheduler) requestClaim(ctx context.Context, targetAgentID, taskID string) {
	req := ClaimRequest{TaskID: taskID}
	data, _ := json.Marshal(req)

	subject := fmt.Sprintf("swarm.rpc.%s.claim", targetAgentID)
	resp, err := s.nc.RequestWithContext(ctx, subject, data, 5*time.Second)
	if err != nil {
		s.log.Warn("claim request failed",
			"target", targetAgentID,
			"task", taskID,
			"error", err,
		)
		return
	}

	var result ClaimResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return
	}
	if result.Claimed {
		s.log.Info("task assigned",
			"task", taskID,
			"agent", targetAgentID,
		)
	}
}

// ClaimRequest is the RPC payload for asking an agent to claim a task.
type ClaimRequest struct {
	TaskID string `json:"task_id"`
}

// ClaimResponse is the RPC reply from an agent after a claim request.
type ClaimResponse struct {
	Claimed bool   `json:"claimed"`
	Error   string `json:"error,omitempty"`
}

package presence

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const heartbeatInterval = 10 * time.Second

// Tracker manages this agent's presence in the swarm and tracks
// the presence of all other agents via KV watch.
type Tracker struct {
	kv      jetstream.KeyValue
	agentID string
	self    AgentPresence
	log     *slog.Logger

	mu    sync.RWMutex
	peers map[string]AgentPresence // agentID -> presence
}

// NewTracker creates a presence tracker for the given agent.
func NewTracker(js jetstream.JetStream, agentID, name string, capabilities []string) (*Tracker, error) {
	kv, err := js.KeyValue(context.Background(), "SWARM_PRESENCE")
	if err != nil {
		return nil, fmt.Errorf("open SWARM_PRESENCE: %w", err)
	}

	return &Tracker{
		kv:      kv,
		agentID: agentID,
		self: AgentPresence{
			AgentID:      agentID,
			Name:         name,
			Status:       "idle",
			Capabilities: capabilities,
			JoinedAt:     time.Now().UTC(),
		},
		log:   slog.Default().With("component", "presence"),
		peers: make(map[string]AgentPresence),
	}, nil
}

// Start begins the heartbeat loop and peer watcher. Blocks until
// the context is cancelled.
func (t *Tracker) Start(ctx context.Context) error {
	// Initial announcement
	if err := t.heartbeat(ctx); err != nil {
		return fmt.Errorf("initial heartbeat: %w", err)
	}

	// Watch all presence entries for peer tracking
	go t.watchPeers(ctx)

	// Periodic heartbeat
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Clean departure — delete our presence key
			_ = t.kv.Delete(context.Background(), presenceKey(t.agentID))
			return ctx.Err()
		case <-ticker.C:
			if err := t.heartbeat(ctx); err != nil {
				t.log.Warn("heartbeat failed", "error", err)
			}
		}
	}
}

// SetStatus updates this agent's status (idle, working, busy).
func (t *Tracker) SetStatus(status string) {
	t.mu.Lock()
	t.self.Status = status
	t.mu.Unlock()
}

// SetCurrentTask updates the task this agent is working on.
func (t *Tracker) SetCurrentTask(taskID string) {
	t.mu.Lock()
	t.self.CurrentTask = taskID
	if taskID != "" {
		t.self.Status = "working"
	} else {
		t.self.Status = "idle"
	}
	t.mu.Unlock()
}

// SetModel updates the LLM model this agent is using.
func (t *Tracker) SetModel(model string) {
	t.mu.Lock()
	t.self.Model = model
	t.mu.Unlock()
}

// GetPeers returns a snapshot of all known agents (including self).
func (t *Tracker) GetPeers() []AgentPresence {
	t.mu.RLock()
	defer t.mu.RUnlock()

	peers := make([]AgentPresence, 0, len(t.peers)+1)
	peers = append(peers, t.self)
	for _, p := range t.peers {
		peers = append(peers, p)
	}
	return peers
}

// GetByStatus returns all agents with the given status.
func (t *Tracker) GetByStatus(status string) []AgentPresence {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var result []AgentPresence
	if t.self.Status == status {
		result = append(result, t.self)
	}
	for _, p := range t.peers {
		if p.Status == status {
			result = append(result, p)
		}
	}
	return result
}

// PeerCount returns the number of known peers (excluding self).
func (t *Tracker) PeerCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.peers)
}

// heartbeat publishes this agent's current presence to the KV store.
func (t *Tracker) heartbeat(ctx context.Context) error {
	t.mu.RLock()
	presence := t.self
	t.mu.RUnlock()

	presence.LastSeen = time.Now().UTC()

	data, err := json.Marshal(presence)
	if err != nil {
		return err
	}

	_, err = t.kv.Put(ctx, presenceKey(t.agentID), data)
	return err
}

// watchPeers subscribes to all presence KV changes and maintains
// the local peer map.
func (t *Tracker) watchPeers(ctx context.Context) {
	watcher, err := t.kv.WatchAll(ctx)
	if err != nil {
		t.log.Error("failed to watch presence", "error", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case entry := <-watcher.Updates():
			if entry == nil {
				continue
			}

			var agent AgentPresence
			if err := json.Unmarshal(entry.Value(), &agent); err != nil {
				continue
			}

			// Skip our own presence
			if agent.AgentID == t.agentID {
				continue
			}

			t.mu.Lock()
			switch entry.Operation() {
			case jetstream.KeyValuePut:
				t.peers[agent.AgentID] = agent
				t.log.Info("peer updated",
					"peer", agent.AgentID,
					"status", agent.Status,
				)
			case jetstream.KeyValueDelete, jetstream.KeyValuePurge:
				delete(t.peers, agent.AgentID)
				t.log.Info("peer departed", "peer", agent.AgentID)
			}
			t.mu.Unlock()
		}
	}
}

func presenceKey(agentID string) string {
	return "presence." + agentID
}

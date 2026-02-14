package swarmctx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/oklog/ulid/v2"
)

// Store provides shared context read/write backed by SWARM_CONTEXT KV
// and SWARM_SESSIONS Object Store for full session transcripts.
type Store struct {
	kv      jetstream.KeyValue
	obj     jetstream.ObjectStore
	agentID string
}

// NewStore opens the SWARM_CONTEXT KV bucket and SWARM_SESSIONS Object Store.
func NewStore(js jetstream.JetStream, agentID string) (*Store, error) {
	kv, err := js.KeyValue(context.Background(), "SWARM_CONTEXT")
	if err != nil {
		return nil, fmt.Errorf("open SWARM_CONTEXT: %w", err)
	}
	obj, err := js.ObjectStore(context.Background(), "SWARM_SESSIONS")
	if err != nil {
		return nil, fmt.Errorf("open SWARM_SESSIONS: %w", err)
	}
	return &Store{kv: kv, obj: obj, agentID: agentID}, nil
}

// Append adds a context entry visible to all agents in the swarm.
func (s *Store) Append(ctx context.Context, sessionID string, ctype ContextType, role, content string, metadata map[string]any) (*ContextEntry, error) {
	entry := &ContextEntry{
		ID:        ulid.Make().String(),
		SessionID: sessionID,
		AgentID:   s.agentID,
		Type:      ctype,
		Role:      role,
		Content:   content,
		Metadata:  metadata,
		CreatedAt: time.Now().UTC(),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("marshal context entry: %w", err)
	}

	key := fmt.Sprintf("context.%s.%s", sessionID, entry.ID)
	if _, err := s.kv.Put(ctx, key, data); err != nil {
		return nil, fmt.Errorf("put context entry: %w", err)
	}

	return entry, nil
}

// GetSession returns all context entries for a given session, ordered by ID (ULID = time-ordered).
func (s *Store) GetSession(ctx context.Context, sessionID string) ([]*ContextEntry, error) {
	prefix := fmt.Sprintf("context.%s.", sessionID)
	keys, err := s.kv.Keys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list context keys: %w", err)
	}

	var entries []*ContextEntry
	for _, key := range keys {
		if len(key) < len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		kvEntry, err := s.kv.Get(ctx, key)
		if err != nil {
			continue
		}
		var entry ContextEntry
		if err := json.Unmarshal(kvEntry.Value(), &entry); err != nil {
			continue
		}
		entries = append(entries, &entry)
	}
	return entries, nil
}

// SyncSession uploads a full session transcript (JSONL) to the Object Store.
func (s *Store) SyncSession(ctx context.Context, sessionID string, jsonlData []byte) error {
	objKey := fmt.Sprintf("sessions/%s.jsonl", sessionID)
	reader := bytes.NewReader(jsonlData)
	if _, err := s.obj.Put(ctx, jetstream.ObjectMeta{Name: objKey}, reader); err != nil {
		return fmt.Errorf("sync session %s: %w", sessionID, err)
	}
	return nil
}

// LoadSession downloads a full session transcript from the Object Store.
func (s *Store) LoadSession(ctx context.Context, sessionID string) ([]byte, error) {
	objKey := fmt.Sprintf("sessions/%s.jsonl", sessionID)
	data, err := s.obj.GetBytes(ctx, objKey)
	if err != nil {
		return nil, fmt.Errorf("load session %s: %w", sessionID, err)
	}
	return data, nil
}

// Watch subscribes to context changes for a specific session or all sessions.
// Use "context.>" for all, or "context.<session-id>.>" for one session.
func (s *Store) Watch(ctx context.Context, keyPattern string) (<-chan ContextEvent, error) {
	watcher, err := s.kv.Watch(ctx, keyPattern)
	if err != nil {
		return nil, fmt.Errorf("watch context %s: %w", keyPattern, err)
	}

	ch := make(chan ContextEvent, 64)
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case kvEntry := <-watcher.Updates():
				if kvEntry == nil {
					continue
				}
				var entry ContextEntry
				if err := json.Unmarshal(kvEntry.Value(), &entry); err != nil {
					continue
				}
				ch <- ContextEvent{
					Operation: kvEntry.Operation().String(),
					Entry:     entry,
				}
			}
		}
	}()
	return ch, nil
}

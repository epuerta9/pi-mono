package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const largeContentThreshold = 1 << 20 // 1MB

// Store provides read/write access to the shared memory KV bucket,
// with automatic overflow to the Object Store for large entries.
type Store struct {
	kv      jetstream.KeyValue
	obj     jetstream.ObjectStore
	agentID string
}

// NewStore opens the SWARM_MEMORY KV bucket and SWARM_ARTIFACTS Object Store.
func NewStore(js jetstream.JetStream, agentID string) (*Store, error) {
	kv, err := js.KeyValue(context.Background(), "SWARM_MEMORY")
	if err != nil {
		return nil, fmt.Errorf("open SWARM_MEMORY: %w", err)
	}
	obj, err := js.ObjectStore(context.Background(), "SWARM_ARTIFACTS")
	if err != nil {
		return nil, fmt.Errorf("open SWARM_ARTIFACTS: %w", err)
	}
	return &Store{kv: kv, obj: obj, agentID: agentID}, nil
}

// Put stores or updates a memory entry. Content larger than 1MB is
// automatically stored in the Object Store with a reference in KV.
func (s *Store) Put(ctx context.Context, key, content string) (*MemoryEntry, error) {
	entry := &MemoryEntry{
		Key:       key,
		Content:   content,
		MimeType:  "text/markdown",
		Author:    s.agentID,
		UpdatedAt: time.Now().UTC(),
	}

	// Overflow large content to Object Store
	if len(content) > largeContentThreshold {
		objKey := "memory/" + strings.ReplaceAll(key, ".", "/")
		if _, err := s.obj.PutBytes(ctx, objKey, []byte(content)); err != nil {
			return nil, fmt.Errorf("store large memory in object store: %w", err)
		}
		entry.Content = "objstore://SWARM_ARTIFACTS/" + objKey
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("marshal memory entry: %w", err)
	}

	rev, err := s.kv.Put(ctx, key, data)
	if err != nil {
		return nil, fmt.Errorf("put memory %s: %w", key, err)
	}
	entry.Revision = rev
	return entry, nil
}

// Get retrieves a single memory entry by key. If the content is an
// Object Store reference, it automatically fetches the full content.
func (s *Store) Get(ctx context.Context, key string) (*MemoryEntry, error) {
	kvEntry, err := s.kv.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("get memory %s: %w", key, err)
	}

	var entry MemoryEntry
	if err := json.Unmarshal(kvEntry.Value(), &entry); err != nil {
		return nil, fmt.Errorf("unmarshal memory: %w", err)
	}
	entry.Revision = kvEntry.Revision()

	// Dereference Object Store pointer
	if strings.HasPrefix(entry.Content, "objstore://") {
		content, err := s.resolveObjRef(ctx, entry.Content)
		if err != nil {
			return nil, err
		}
		entry.Content = content
	}

	return &entry, nil
}

// Delete removes a memory entry.
func (s *Store) Delete(ctx context.Context, key string) error {
	// Check if content is in Object Store and clean it up
	entry, err := s.Get(ctx, key)
	if err == nil && strings.HasPrefix(entry.Content, "objstore://") {
		objKey := strings.TrimPrefix(entry.Content, "objstore://SWARM_ARTIFACTS/")
		_ = s.obj.Delete(ctx, objKey)
	}

	return s.kv.Delete(ctx, key)
}

// GetGlobal returns all global memory entries.
func (s *Store) GetGlobal(ctx context.Context) ([]*MemoryEntry, error) {
	return s.getByPrefix(ctx, "memory.global.")
}

// GetForAgent returns global memory plus agent-specific memory.
func (s *Store) GetForAgent(ctx context.Context, agentID string) ([]*MemoryEntry, error) {
	global, err := s.GetGlobal(ctx)
	if err != nil {
		return nil, err
	}
	agentMem, err := s.getByPrefix(ctx, "memory.agent."+agentID+".")
	if err != nil {
		return nil, err
	}
	return append(global, agentMem...), nil
}

// GetForTeam returns global memory plus team-specific memory.
func (s *Store) GetForTeam(ctx context.Context, teamID string) ([]*MemoryEntry, error) {
	global, err := s.GetGlobal(ctx)
	if err != nil {
		return nil, err
	}
	teamMem, err := s.getByPrefix(ctx, "memory.team."+teamID+".")
	if err != nil {
		return nil, err
	}
	return append(global, teamMem...), nil
}

// Watch subscribes to memory changes matching a key pattern.
// Use "memory.global.>" for all global changes, or "memory.>" for everything.
func (s *Store) Watch(ctx context.Context, keyPattern string) (<-chan MemoryEvent, error) {
	watcher, err := s.kv.Watch(ctx, keyPattern)
	if err != nil {
		return nil, fmt.Errorf("watch memory %s: %w", keyPattern, err)
	}

	ch := make(chan MemoryEvent, 32)
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
				var entry MemoryEntry
				if err := json.Unmarshal(kvEntry.Value(), &entry); err != nil {
					continue
				}
				entry.Revision = kvEntry.Revision()
				ch <- MemoryEvent{
					Operation: kvEntry.Operation().String(),
					Entry:     entry,
				}
			}
		}
	}()
	return ch, nil
}

// getByPrefix returns all entries whose key starts with the given prefix.
func (s *Store) getByPrefix(ctx context.Context, prefix string) ([]*MemoryEntry, error) {
	keys, err := s.kv.Keys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list memory keys: %w", err)
	}

	var entries []*MemoryEntry
	for _, key := range keys {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		entry, err := s.Get(ctx, key)
		if err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// resolveObjRef fetches content from an objstore:// reference.
func (s *Store) resolveObjRef(ctx context.Context, ref string) (string, error) {
	// ref format: "objstore://SWARM_ARTIFACTS/memory/global/conventions"
	objKey := strings.TrimPrefix(ref, "objstore://SWARM_ARTIFACTS/")
	data, err := s.obj.GetBytes(ctx, objKey)
	if err != nil {
		return "", fmt.Errorf("fetch object %s: %w", objKey, err)
	}
	return string(data), nil
}

package memory

import "time"

// MemoryEntry is a shared knowledge fragment visible to agents in the swarm.
//
// Scoping:
//   - "memory.global.<name>"        — visible to all agents
//   - "memory.team.<team>.<name>"   — visible to team members
//   - "memory.agent.<agent>.<name>" — private but replicated
type MemoryEntry struct {
	Key       string    `json:"key"`
	Content   string    `json:"content"`     // markdown text or objstore:// reference
	MimeType  string    `json:"mime_type"`   // default "text/markdown"
	Author    string    `json:"author"`      // agent ID that last wrote this
	UpdatedAt time.Time `json:"updated_at"`
	Revision  uint64    `json:"-"`           // NATS KV revision for CAS
}

// MemoryEvent is emitted when a memory entry changes.
type MemoryEvent struct {
	Operation string      // "PUT" or "DEL"
	Entry     MemoryEntry
}

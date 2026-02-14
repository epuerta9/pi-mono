package swarmctx

import "time"

// ContextType categorizes what kind of context fragment this is.
type ContextType string

const (
	ContextMessage    ContextType = "message"     // regular conversation message
	ContextToolResult ContextType = "tool_result"  // tool execution result
	ContextSummary    ContextType = "summary"      // compacted/summarized context
	ContextInsight    ContextType = "insight"       // agent-generated observation
)

// ContextEntry is a shared context fragment that any agent in the
// swarm can read. This enables agents to learn from each other's
// work without duplicating effort.
type ContextEntry struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	AgentID   string         `json:"agent_id"`
	Type      ContextType    `json:"type"`
	Role      string         `json:"role"`     // user | assistant | system
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata"` // tool name, model, token count, etc.
	ParentID  string         `json:"parent_id"` // for threading context
	CreatedAt time.Time      `json:"created_at"`
}

// ContextEvent is emitted when context changes.
type ContextEvent struct {
	Operation string
	Entry     ContextEntry
}

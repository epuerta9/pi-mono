package presence

import "time"

// AgentPresence represents a live agent in the swarm.
// Stored in SWARM_PRESENCE KV with 30s TTL — if an agent stops
// sending heartbeats, its entry expires and it's considered offline.
type AgentPresence struct {
	AgentID      string   `json:"agent_id"`
	Name         string   `json:"name"`
	Model        string   `json:"model"`        // current LLM model
	Status       string   `json:"status"`       // idle | working | busy
	CurrentTask  string   `json:"current_task"`  // task ID if working
	Capabilities []string `json:"capabilities"`  // ["code","search","test","review",...]
	JoinedAt     time.Time `json:"joined_at"`
	LastSeen     time.Time `json:"last_seen"`
}

// PresenceEvent is emitted when an agent's presence changes.
type PresenceEvent struct {
	Operation string        // "PUT" (join/update) or "DEL" (left/expired)
	Agent     AgentPresence
}

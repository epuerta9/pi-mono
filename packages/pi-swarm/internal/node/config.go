package node

// Config holds all configuration for a swarm node.
type Config struct {
	// AgentID is the unique identifier for this agent in the swarm.
	AgentID string

	// Name is a human-readable label for this node.
	Name string

	// DataDir is the directory for JetStream storage.
	// Defaults to ~/.pi-swarm/data/<AgentID>
	DataDir string

	// Port is the NATS client port. Default 4222.
	Port int

	// ClusterPort is the NATS cluster (route) port. Default 6222.
	ClusterPort int

	// ClusterName is the shared cluster name. All nodes in the same
	// swarm must use the same cluster name. Default "pi-swarm".
	ClusterName string

	// Seeds are addresses of existing cluster nodes to connect to.
	// Format: "nats://host:cluster-port"
	// When empty, this node starts as a standalone seed.
	Seeds []string

	// Advertise is the external address other nodes should use to
	// reach this node's cluster port. Useful for NAT/Docker setups.
	Advertise string

	// PiCommand is the command to launch a Pi agent process.
	// Default "pi".
	PiCommand string

	// PiArgs are extra arguments passed to the Pi process.
	PiArgs []string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig(agentID string) Config {
	return Config{
		AgentID:     agentID,
		Name:        agentID,
		Port:        4222,
		ClusterPort: 6222,
		ClusterName: "pi-swarm",
		PiCommand:   "pi",
	}
}

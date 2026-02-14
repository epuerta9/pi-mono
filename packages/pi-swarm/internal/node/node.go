package node

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// SwarmNode is a single node in the Pi Swarm. It embeds a NATS server
// with JetStream and exposes typed access to all shared state buckets.
type SwarmNode struct {
	server  *natsserver.Server
	conn    *nats.Conn
	js      jetstream.JetStream
	config  Config
	AgentID string
}

// New creates and starts an embedded NATS server, connects to it as a
// client, initializes JetStream, and provisions all KV/Object Store
// buckets required by the swarm.
func New(cfg Config) (*SwarmNode, error) {
	dataDir := cfg.DataDir
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".pi-swarm", "data", cfg.AgentID)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	opts := &natsserver.Options{
		ServerName: cfg.AgentID,
		Host:       "0.0.0.0",
		Port:       cfg.Port,
		JetStream:  true,
		StoreDir:   dataDir,
		NoLog:      true,
		NoSigs:     true,
	}

	// Cluster configuration
	if cfg.ClusterName != "" {
		opts.Cluster = natsserver.ClusterOpts{
			Name: cfg.ClusterName,
			Host: "0.0.0.0",
			Port: cfg.ClusterPort,
		}
		if cfg.Advertise != "" {
			opts.Cluster.Advertise = cfg.Advertise
		}
	}

	// Seed routes for joining an existing cluster
	if len(cfg.Seeds) > 0 {
		routes, err := parseRoutes(cfg.Seeds)
		if err != nil {
			return nil, fmt.Errorf("parse seed routes: %w", err)
		}
		opts.Routes = routes
	}

	ns, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("create nats server: %w", err)
	}
	ns.Start()

	// Wait for the server to be ready
	if !ns.ReadyForConnections(10 * time.Second) {
		ns.Shutdown()
		return nil, fmt.Errorf("nats server not ready after 10s")
	}

	// Connect as a client to our own embedded server
	nc, err := nats.Connect(ns.ClientURL(),
		nats.Name(cfg.AgentID),
		nats.ReconnectWait(time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		ns.Shutdown()
		return nil, fmt.Errorf("connect to embedded nats: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, fmt.Errorf("init jetstream: %w", err)
	}

	node := &SwarmNode{
		server:  ns,
		conn:    nc,
		js:      js,
		config:  cfg,
		AgentID: cfg.AgentID,
	}

	return node, nil
}

// EnsureBuckets creates or updates all KV buckets and Object Stores
// needed by the swarm. Safe to call multiple times (idempotent).
func (n *SwarmNode) EnsureBuckets(ctx context.Context) error {
	replicas := n.replicaCount()

	kvConfigs := []jetstream.KeyValueConfig{
		{
			Bucket:      "SWARM_TASKS",
			Description: "Shared task state with CAS-protected mutations",
			Replicas:    replicas,
			History:     10,
		},
		{
			Bucket:      "SWARM_MEMORY",
			Description: "Shared memory entries (conventions, knowledge, preferences)",
			Replicas:    replicas,
			History:     5,
		},
		{
			Bucket:      "SWARM_CONTEXT",
			Description: "Shared context fragments between agents",
			Replicas:    replicas,
			History:     1,
		},
		{
			Bucket:      "SWARM_PRESENCE",
			Description: "Agent presence and heartbeat (ephemeral)",
			Replicas:    1,
			TTL:         30 * time.Second,
		},
		{
			Bucket:      "SWARM_LOCKS",
			Description: "Distributed locks for leader election and task coordination",
			Replicas:    replicas,
			TTL:         60 * time.Second,
		},
	}

	for _, cfg := range kvConfigs {
		if _, err := n.js.CreateOrUpdateKeyValue(ctx, cfg); err != nil {
			return fmt.Errorf("ensure KV bucket %s: %w", cfg.Bucket, err)
		}
	}

	objConfigs := []jetstream.ObjectStoreConfig{
		{
			Bucket:      "SWARM_SESSIONS",
			Description: "Full session transcripts (JSONL)",
			Replicas:    replicas,
		},
		{
			Bucket:      "SWARM_ARTIFACTS",
			Description: "Large files, attachments, and memory documents >1MB",
			Replicas:    replicas,
		},
	}

	for _, cfg := range objConfigs {
		if _, err := n.js.CreateOrUpdateObjectStore(ctx, cfg); err != nil {
			return fmt.Errorf("ensure Object Store %s: %w", cfg.Bucket, err)
		}
	}

	return nil
}

// Conn returns the NATS client connection.
func (n *SwarmNode) Conn() *nats.Conn { return n.conn }

// JetStream returns the JetStream context.
func (n *SwarmNode) JetStream() jetstream.JetStream { return n.js }

// Shutdown gracefully stops the node.
func (n *SwarmNode) Shutdown() {
	n.conn.Drain()
	n.server.Shutdown()
	n.server.WaitForShutdown()
}

// ClusterInfo returns information about the current cluster state.
func (n *SwarmNode) ClusterInfo() *natsserver.ClusterInfo {
	return n.server.ClusterInfo()
}

// replicaCount returns the appropriate replica count based on cluster size.
// For 1-2 nodes, uses 1 replica. For 3+, uses 3 replicas.
func (n *SwarmNode) replicaCount() int {
	info := n.server.ClusterInfo()
	if info == nil || len(info.URLs) < 3 {
		return 1
	}
	return 3
}

// parseRoutes converts seed address strings to NATS route URLs.
func parseRoutes(seeds []string) ([]*url.URL, error) {
	routes := make([]*url.URL, 0, len(seeds))
	for _, seed := range seeds {
		u, err := url.Parse(seed)
		if err != nil {
			return nil, fmt.Errorf("invalid seed %q: %w", seed, err)
		}
		routes = append(routes, u)
	}
	return routes, nil
}

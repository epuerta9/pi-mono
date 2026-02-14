package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mariozechner/pi-mono/packages/pi-swarm/internal/bridge"
	swarmctx "github.com/mariozechner/pi-mono/packages/pi-swarm/internal/context"
	"github.com/mariozechner/pi-mono/packages/pi-swarm/internal/memory"
	"github.com/mariozechner/pi-mono/packages/pi-swarm/internal/node"
	"github.com/mariozechner/pi-mono/packages/pi-swarm/internal/presence"
	"github.com/mariozechner/pi-mono/packages/pi-swarm/internal/tasks"
)

func main() {
	// Flags
	agentID := flag.String("agent-id", "", "Unique agent identifier (required)")
	name := flag.String("name", "", "Human-readable agent name (defaults to agent-id)")
	dataDir := flag.String("data-dir", "", "JetStream data directory")
	port := flag.Int("port", 4222, "NATS client port")
	clusterPort := flag.Int("cluster-port", 6222, "NATS cluster port")
	clusterName := flag.String("cluster-name", "pi-swarm", "Cluster name")
	seeds := flag.String("seeds", "", "Comma-separated seed node addresses (nats://host:port)")
	advertise := flag.String("advertise", "", "External address for cluster routing")
	piCmd := flag.String("pi-cmd", "pi", "Command to launch Pi agent")
	capabilities := flag.String("capabilities", "code,search,test", "Comma-separated agent capabilities")
	flag.Parse()

	if *agentID == "" {
		fmt.Fprintln(os.Stderr, "error: --agent-id is required")
		flag.Usage()
		os.Exit(1)
	}
	if *name == "" {
		*name = *agentID
	}

	// Structured logging
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	log := slog.Default().With("agent", *agentID)

	// Parse seeds
	var seedList []string
	if *seeds != "" {
		seedList = strings.Split(*seeds, ",")
	}

	// Parse capabilities
	capList := strings.Split(*capabilities, ",")

	// Build node config
	cfg := node.Config{
		AgentID:     *agentID,
		Name:        *name,
		DataDir:     *dataDir,
		Port:        *port,
		ClusterPort: *clusterPort,
		ClusterName: *clusterName,
		Seeds:       seedList,
		Advertise:   *advertise,
		PiCommand:   *piCmd,
	}

	// Start
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Info("starting pi-swarm node",
		"port", cfg.Port,
		"cluster_port", cfg.ClusterPort,
		"seeds", seedList,
	)

	// 1. Boot embedded NATS + JetStream
	n, err := node.New(cfg)
	if err != nil {
		log.Error("failed to start node", "error", err)
		os.Exit(1)
	}
	defer n.Shutdown()

	// 2. Create KV buckets and Object Stores
	if err := n.EnsureBuckets(ctx); err != nil {
		log.Error("failed to ensure buckets", "error", err)
		os.Exit(1)
	}
	log.Info("jetstream buckets ready")

	// 3. Initialize stores
	taskStore, err := tasks.NewStore(n.JetStream(), n.Conn(), *agentID)
	if err != nil {
		log.Error("failed to init task store", "error", err)
		os.Exit(1)
	}

	memStore, err := memory.NewStore(n.JetStream(), *agentID)
	if err != nil {
		log.Error("failed to init memory store", "error", err)
		os.Exit(1)
	}

	ctxStore, err := swarmctx.NewStore(n.JetStream(), *agentID)
	if err != nil {
		log.Error("failed to init context store", "error", err)
		os.Exit(1)
	}

	presTracker, err := presence.NewTracker(n.JetStream(), *agentID, *name, capList)
	if err != nil {
		log.Error("failed to init presence tracker", "error", err)
		os.Exit(1)
	}

	// 4. Start presence heartbeats
	go func() {
		if err := presTracker.Start(ctx); err != nil {
			log.Error("presence tracker stopped", "error", err)
		}
	}()

	// 5. Start task scheduler (all nodes compete for leadership)
	scheduler, err := tasks.NewScheduler(taskStore, presTracker, n.JetStream(), n.Conn(), *agentID)
	if err != nil {
		log.Error("failed to init scheduler", "error", err)
		os.Exit(1)
	}
	go func() {
		if err := scheduler.Run(ctx); err != nil {
			log.Error("scheduler stopped", "error", err)
		}
	}()

	// 6. Bridge to Pi TypeScript agent
	piBridge := bridge.NewPiBridge(
		*agentID, *piCmd, flag.Args(),
		taskStore, memStore, ctxStore, presTracker, n.Conn(),
	)
	if err := piBridge.Start(ctx); err != nil {
		log.Error("failed to start pi bridge", "error", err)
		os.Exit(1)
	}

	log.Info("pi-swarm node ready",
		"agent_id", *agentID,
		"peers", presTracker.PeerCount(),
	)

	// Wait for shutdown signal
	<-ctx.Done()
	log.Info("shutting down")

	if err := piBridge.Stop(); err != nil {
		log.Warn("pi agent stop error", "error", err)
	}
}

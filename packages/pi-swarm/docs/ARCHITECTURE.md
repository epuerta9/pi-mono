# Pi Swarm: Distributed Agent Memory & Tasks

## Overview

Pi Swarm turns isolated Pi agents into a collaborative swarm by introducing a
shared memory and task layer built on **Go** with **embedded NATS JetStream**.
Every Pi node runs an embedded NATS server that automatically clusters with
peers, giving the swarm:

- **Shared Todos/Tasks** — any Pi can create, claim, update, or complete tasks
- **Shared Context** — conversation fragments, tool results, and reasoning
  visible to all peers
- **Shared Memory** — long-lived knowledge (MEMORY.md equivalents) replicated
  across the cluster

```
┌─────────────────────────────────────────────────────────────┐
│                      NATS Cluster                           │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐              │
│  │ Pi Node A│◄──►│ Pi Node B│◄──►│ Pi Node C│   ...        │
│  │ (embed)  │    │ (embed)  │    │ (embed)  │              │
│  └────┬─────┘    └────┬─────┘    └────┬─────┘              │
│       │               │               │                    │
│  ┌────┴───────────────┴───────────────┴────┐               │
│  │          JetStream (R3 replicated)      │               │
│  │                                         │               │
│  │  ┌─────────┐ ┌──────────┐ ┌──────────┐ │               │
│  │  │ KV:     │ │ KV:      │ │ Object   │ │               │
│  │  │ todos   │ │ memory   │ │ Store:   │ │               │
│  │  │ agents  │ │ context  │ │ sessions │ │               │
│  │  │ locks   │ │ presence │ │ artifacts│ │               │
│  │  └─────────┘ └──────────┘ └──────────┘ │               │
│  └─────────────────────────────────────────┘               │
│                                                             │
│  Subjects:                                                  │
│    swarm.tasks.>        (task lifecycle events)             │
│    swarm.memory.>       (memory updates)                   │
│    swarm.context.>      (context sharing)                  │
│    swarm.presence.>     (heartbeats & discovery)           │
│    swarm.rpc.>          (request/reply between agents)     │
└─────────────────────────────────────────────────────────────┘
```

---

## Design Principles

1. **Embedded, not external** — Each Pi binary embeds a NATS server. No
   separate infrastructure to deploy. Start a Pi, join the swarm.
2. **Offline-first** — A lone Pi works fine with a single-node JetStream.
   When peers appear, state syncs automatically via NATS clustering.
3. **CAS for correctness** — All KV mutations use Compare-And-Swap
   (revision-based) to prevent lost updates when multiple Pi's race.
4. **Subjects for reactivity** — Real-time NATS pub/sub pushes events to
   all interested agents. No polling.
5. **Object Store for bulk** — Session transcripts, file attachments, and
   large memory documents go in the Object Store (chunked, replicated).

---

## Data Model

### 1. Tasks / Todos

Stored in **KV bucket: `SWARM_TASKS`**

Key format: `tasks.<task-id>` (ULID)

```go
type Task struct {
    ID          string        `json:"id"`          // ULID
    Title       string        `json:"title"`
    Description string        `json:"description"`
    Status      TaskStatus    `json:"status"`      // pending | claimed | active | done | failed
    Priority    int           `json:"priority"`    // 0=low, 1=normal, 2=high, 3=critical
    CreatedBy   string        `json:"created_by"`  // agent ID
    ClaimedBy   string        `json:"claimed_by"`  // agent ID (empty if unclaimed)
    ParentID    string        `json:"parent_id"`   // for sub-tasks
    Tags        []string      `json:"tags"`
    Result      string        `json:"result"`      // outcome when done/failed
    DependsOn   []string      `json:"depends_on"`  // task IDs that must complete first
    CreatedAt   time.Time     `json:"created_at"`
    UpdatedAt   time.Time     `json:"updated_at"`
    Revision    uint64        `json:"-"`           // NATS KV revision (for CAS)
}

type TaskStatus string
const (
    TaskPending  TaskStatus = "pending"
    TaskClaimed  TaskStatus = "claimed"   // agent intends to work on it
    TaskActive   TaskStatus = "active"    // agent is actively executing
    TaskDone     TaskStatus = "done"
    TaskFailed   TaskStatus = "failed"
)
```

**Lifecycle events** published to `swarm.tasks.<task-id>.<event>`:

| Event       | Subject                           | Payload            |
|-------------|-----------------------------------|--------------------|
| created     | `swarm.tasks.<id>.created`        | Full Task          |
| claimed     | `swarm.tasks.<id>.claimed`        | `{agent, task_id}` |
| progress    | `swarm.tasks.<id>.progress`       | `{agent, message}` |
| completed   | `swarm.tasks.<id>.completed`      | `{agent, result}`  |
| failed      | `swarm.tasks.<id>.failed`         | `{agent, error}`   |
| released    | `swarm.tasks.<id>.released`       | `{agent, reason}`  |

### 2. Shared Memory

Stored in **KV bucket: `SWARM_MEMORY`**

Key format: `memory.<scope>.<name>`

Scopes:
- `global` — visible to all agents (`memory.global.project-conventions`)
- `team.<team-id>` — visible to a subset (`memory.team.backend.api-patterns`)
- `agent.<agent-id>` — private but replicated (`memory.agent.pi-a.preferences`)

```go
type MemoryEntry struct {
    Key       string    `json:"key"`
    Content   string    `json:"content"`    // markdown text
    MimeType  string    `json:"mime_type"`  // "text/markdown" default
    Author    string    `json:"author"`     // agent ID
    UpdatedAt time.Time `json:"updated_at"`
    Revision  uint64    `json:"-"`
}
```

For large memory documents (>1MB), the `Content` field contains a reference
`objstore://<bucket>/<key>` and the actual content lives in the Object Store.

### 3. Shared Context

Stored in **KV bucket: `SWARM_CONTEXT`**

Key format: `context.<session-id>.<entry-id>` (ULID)

```go
type ContextEntry struct {
    ID        string          `json:"id"`
    SessionID string          `json:"session_id"`
    AgentID   string          `json:"agent_id"`
    Type      ContextType     `json:"type"`      // message | tool_result | summary | insight
    Role      string          `json:"role"`      // user | assistant | system
    Content   string          `json:"content"`
    Metadata  map[string]any  `json:"metadata"`  // tool name, model, tokens, etc.
    ParentID  string          `json:"parent_id"` // for threading
    CreatedAt time.Time       `json:"created_at"`
}

type ContextType string
const (
    ContextMessage    ContextType = "message"
    ContextToolResult ContextType = "tool_result"
    ContextSummary    ContextType = "summary"     // compacted context
    ContextInsight    ContextType = "insight"      // agent-generated observations
)
```

Full session transcripts are stored in **Object Store: `SWARM_SESSIONS`** as
JSONL files (same format as existing pi sessions), keyed by session ID.

### 4. Agent Presence

Stored in **KV bucket: `SWARM_PRESENCE`** with TTL=30s

Key format: `presence.<agent-id>`

```go
type AgentPresence struct {
    AgentID     string    `json:"agent_id"`
    Name        string    `json:"name"`
    Model       string    `json:"model"`       // current LLM model
    Status      string    `json:"status"`      // idle | working | busy
    CurrentTask string    `json:"current_task"` // task ID if working
    Capabilities []string `json:"capabilities"` // ["code","search","test",...]
    JoinedAt    time.Time `json:"joined_at"`
    LastSeen    time.Time `json:"last_seen"`
}
```

Heartbeats published to `swarm.presence.<agent-id>.heartbeat` every 10s.

---

## Go Package Structure

```
packages/pi-swarm/
├── docs/
│   └── ARCHITECTURE.md          # this file
├── cmd/
│   └── pi-swarm/
│       └── main.go              # binary entrypoint
├── internal/
│   ├── node/
│   │   ├── node.go              # embedded NATS + JetStream bootstrap
│   │   ├── cluster.go           # peer discovery & route configuration
│   │   └── config.go            # node configuration
│   ├── tasks/
│   │   ├── store.go             # KV-backed task CRUD with CAS
│   │   ├── scheduler.go         # task assignment & dependency resolution
│   │   ├── watcher.go           # KV watch for real-time task updates
│   │   └── types.go             # Task, TaskStatus, TaskEvent
│   ├── memory/
│   │   ├── store.go             # KV + ObjectStore memory operations
│   │   ├── watcher.go           # watch for memory changes
│   │   ├── merge.go             # conflict resolution strategies
│   │   └── types.go             # MemoryEntry
│   ├── context/
│   │   ├── store.go             # shared context read/write
│   │   ├── session.go           # full session sync to Object Store
│   │   ├── compactor.go         # context summarization triggers
│   │   └── types.go             # ContextEntry
│   ├── presence/
│   │   ├── tracker.go           # heartbeat + TTL-based presence
│   │   ├── discovery.go         # agent capability matching
│   │   └── types.go             # AgentPresence
│   ├── rpc/
│   │   ├── server.go            # NATS request/reply handlers
│   │   ├── client.go            # typed RPC client for agent-to-agent
│   │   └── types.go             # RPC request/response envelopes
│   └── bridge/
│       ├── pi_bridge.go         # bridge to existing pi TypeScript agent
│       ├── stdio.go             # JSON-over-stdin/stdout (matches pi RPC mode)
│       └── http.go              # optional HTTP/WebSocket bridge
├── pkg/
│   └── swarmapi/
│       ├── client.go            # public Go client for embedding
│       ├── tasks.go             # task operations interface
│       ├── memory.go            # memory operations interface
│       └── context.go           # context operations interface
├── go.mod
├── go.sum
└── Makefile
```

---

## Component Deep Dives

### A. Embedded NATS Node (`internal/node/`)

Each Pi Swarm process embeds a full NATS server with JetStream enabled.

```go
package node

import (
    "github.com/nats-io/nats-server/v2/server"
    "github.com/nats-io/nats.go"
    "github.com/nats-io/nats.go/jetstream"
)

type SwarmNode struct {
    server   *server.Server
    conn     *nats.Conn
    js       jetstream.JetStream
    config   Config
    agentID  string
}

type Config struct {
    AgentID     string   // unique agent identifier
    Name        string   // human-readable name
    DataDir     string   // JetStream storage directory
    Port        int      // NATS client port (default 4222)
    ClusterPort int      // NATS cluster port (default 6222)
    ClusterName string   // shared cluster name (default "pi-swarm")
    Seeds       []string // seed node addresses for joining
    // When empty, starts as standalone — other nodes connect to this one
}

func New(cfg Config) (*SwarmNode, error) {
    opts := &server.Options{
        ServerName: cfg.AgentID,
        Port:       cfg.Port,
        JetStream:  true,
        StoreDir:   cfg.DataDir,
        Cluster: server.ClusterOpts{
            Name: cfg.ClusterName,
            Port: cfg.ClusterPort,
        },
    }

    // Add seed routes for cluster formation
    if len(cfg.Seeds) > 0 {
        opts.Routes = parseRoutes(cfg.Seeds)
    }

    ns, err := server.NewServer(opts)
    if err != nil {
        return nil, fmt.Errorf("start embedded nats: %w", err)
    }
    ns.Start()

    // Connect as a client to our own embedded server
    nc, err := nats.Connect(ns.ClientURL())
    if err != nil {
        return nil, fmt.Errorf("connect to embedded nats: %w", err)
    }

    js, err := jetstream.New(nc)
    if err != nil {
        return nil, fmt.Errorf("init jetstream: %w", err)
    }

    return &SwarmNode{
        server:  ns,
        conn:    nc,
        js:      js,
        config:  cfg,
        agentID: cfg.AgentID,
    }, nil
}

// EnsureBuckets creates KV buckets and Object Stores if they don't exist.
func (n *SwarmNode) EnsureBuckets(ctx context.Context) error {
    // KV Buckets
    buckets := []jetstream.KeyValueConfig{
        {Bucket: "SWARM_TASKS", Description: "Shared task state", Replicas: 3},
        {Bucket: "SWARM_MEMORY", Description: "Shared memory entries", Replicas: 3},
        {Bucket: "SWARM_CONTEXT", Description: "Shared context fragments", Replicas: 3},
        {Bucket: "SWARM_PRESENCE", Description: "Agent presence", TTL: 30 * time.Second, Replicas: 1},
        {Bucket: "SWARM_LOCKS", Description: "Distributed locks", TTL: 60 * time.Second, Replicas: 3},
    }
    for _, cfg := range buckets {
        if _, err := n.js.CreateOrUpdateKeyValue(ctx, cfg); err != nil {
            return fmt.Errorf("ensure bucket %s: %w", cfg.Bucket, err)
        }
    }

    // Object Stores
    objStores := []jetstream.ObjectStoreConfig{
        {Bucket: "SWARM_SESSIONS", Description: "Full session transcripts", Replicas: 3},
        {Bucket: "SWARM_ARTIFACTS", Description: "Large files and attachments", Replicas: 3},
    }
    for _, cfg := range objStores {
        if _, err := n.js.CreateOrUpdateObjectStore(ctx, cfg); err != nil {
            return fmt.Errorf("ensure object store %s: %w", cfg.Bucket, err)
        }
    }
    return nil
}
```

**Clustering:** Nodes discover each other via seed addresses. The first node
starts standalone. Subsequent nodes pass the first node's address as a seed.
NATS handles route gossip — once two nodes connect, they share knowledge of all
other nodes automatically.

### B. Task Store (`internal/tasks/store.go`)

```go
package tasks

type Store struct {
    kv      jetstream.KeyValue
    nc      *nats.Conn
    agentID string
}

func NewStore(js jetstream.JetStream, nc *nats.Conn, agentID string) (*Store, error) {
    kv, err := js.KeyValue(context.Background(), "SWARM_TASKS")
    if err != nil {
        return nil, err
    }
    return &Store{kv: kv, nc: nc, agentID: agentID}, nil
}

// Create adds a new task. Returns the created task with its ID.
func (s *Store) Create(ctx context.Context, title, description string, opts ...TaskOption) (*Task, error) {
    task := &Task{
        ID:          ulid.Make().String(),
        Title:       title,
        Description: description,
        Status:      TaskPending,
        CreatedBy:   s.agentID,
        CreatedAt:   time.Now(),
        UpdatedAt:   time.Now(),
    }
    for _, opt := range opts {
        opt(task)
    }

    data, _ := json.Marshal(task)
    rev, err := s.kv.Create(ctx, "tasks."+task.ID, data)
    if err != nil {
        return nil, fmt.Errorf("create task: %w", err)
    }
    task.Revision = rev

    // Publish lifecycle event
    s.publish("swarm.tasks."+task.ID+".created", task)
    return task, nil
}

// Claim atomically claims an unclaimed task using CAS.
func (s *Store) Claim(ctx context.Context, taskID string) (*Task, error) {
    task, err := s.Get(ctx, taskID)
    if err != nil {
        return nil, err
    }
    if task.Status != TaskPending {
        return nil, fmt.Errorf("task %s is %s, not claimable", taskID, task.Status)
    }

    task.Status = TaskClaimed
    task.ClaimedBy = s.agentID
    task.UpdatedAt = time.Now()

    data, _ := json.Marshal(task)
    // CAS: only succeeds if no one else modified the task since we read it
    rev, err := s.kv.Update(ctx, "tasks."+taskID, data, task.Revision)
    if err != nil {
        return nil, fmt.Errorf("claim task (CAS conflict?): %w", err)
    }
    task.Revision = rev

    s.publish("swarm.tasks."+taskID+".claimed", map[string]string{
        "agent":   s.agentID,
        "task_id": taskID,
    })
    return task, nil
}

// Complete marks a task as done with a result.
func (s *Store) Complete(ctx context.Context, taskID, result string) error {
    task, err := s.Get(ctx, taskID)
    if err != nil {
        return err
    }
    if task.ClaimedBy != s.agentID {
        return fmt.Errorf("task %s claimed by %s, not us (%s)", taskID, task.ClaimedBy, s.agentID)
    }

    task.Status = TaskDone
    task.Result = result
    task.UpdatedAt = time.Now()

    data, _ := json.Marshal(task)
    _, err = s.kv.Update(ctx, "tasks."+taskID, data, task.Revision)
    if err != nil {
        return fmt.Errorf("complete task: %w", err)
    }

    s.publish("swarm.tasks."+taskID+".completed", map[string]string{
        "agent":   s.agentID,
        "result":  result,
    })
    return nil
}

// Watch returns a channel of task events for real-time updates.
func (s *Store) Watch(ctx context.Context) (<-chan TaskEvent, error) {
    watcher, err := s.kv.WatchAll(ctx)
    if err != nil {
        return nil, err
    }

    ch := make(chan TaskEvent, 64)
    go func() {
        defer close(ch)
        for entry := range watcher.Updates() {
            if entry == nil {
                continue // initial values done
            }
            var task Task
            json.Unmarshal(entry.Value(), &task)
            task.Revision = entry.Revision()
            ch <- TaskEvent{
                Operation: entry.Operation().String(),
                Task:      task,
            }
        }
    }()
    return ch, nil
}
```

### C. Task Scheduler (`internal/tasks/scheduler.go`)

The scheduler assigns pending tasks to idle agents based on capabilities and
dependencies.

```go
package tasks

// Scheduler watches for pending tasks and assigns them to available agents.
type Scheduler struct {
    tasks    *Store
    presence *presence.Tracker
    agentID  string
}

// Run starts the scheduling loop. Only the leader agent runs this.
// Leadership is determined by a distributed lock in SWARM_LOCKS.
func (s *Scheduler) Run(ctx context.Context) error {
    // Acquire leader lock (auto-renewed)
    lock, err := s.acquireLeaderLock(ctx)
    if err != nil {
        return err // another agent is leader, we just watch
    }
    defer lock.Release()

    taskEvents, _ := s.tasks.Watch(ctx)
    presenceEvents, _ := s.presence.Watch(ctx)

    for {
        select {
        case <-ctx.Done():
            return nil
        case te := <-taskEvents:
            if te.Task.Status == TaskPending {
                s.tryAssign(ctx, te.Task)
            }
        case pe := <-presenceEvents:
            if pe.Status == "idle" {
                s.assignPendingTo(ctx, pe.AgentID)
            }
        }
    }
}

func (s *Scheduler) tryAssign(ctx context.Context, task Task) {
    // Check dependencies are met
    for _, depID := range task.DependsOn {
        dep, _ := s.tasks.Get(ctx, depID)
        if dep == nil || dep.Status != TaskDone {
            return // dependency not met, skip
        }
    }

    // Find idle agent with matching capabilities
    agents := s.presence.GetByStatus("idle")
    for _, agent := range agents {
        if s.matchesCapabilities(agent, task) {
            // Notify agent via request/reply
            s.requestClaim(ctx, agent.AgentID, task.ID)
            return
        }
    }
}
```

### D. Memory Store (`internal/memory/store.go`)

```go
package memory

type Store struct {
    kv  jetstream.KeyValue
    obj jetstream.ObjectStore
}

// Put stores or updates a memory entry. Uses CAS for safe concurrent updates.
func (s *Store) Put(ctx context.Context, key, content, author string) (*MemoryEntry, error) {
    entry := &MemoryEntry{
        Key:       key,
        Content:   content,
        MimeType:  "text/markdown",
        Author:    author,
        UpdatedAt: time.Now(),
    }

    // Large content goes to Object Store
    if len(content) > 1<<20 { // >1MB
        objKey := "memory/" + strings.ReplaceAll(key, ".", "/")
        _, err := s.obj.PutBytes(ctx, objKey, []byte(content))
        if err != nil {
            return nil, fmt.Errorf("store large memory: %w", err)
        }
        entry.Content = "objstore://SWARM_ARTIFACTS/" + objKey
    }

    data, _ := json.Marshal(entry)
    rev, err := s.kv.Put(ctx, key, data)
    if err != nil {
        return nil, err
    }
    entry.Revision = rev
    return entry, nil
}

// GetGlobal retrieves all global memory entries.
func (s *Store) GetGlobal(ctx context.Context) ([]*MemoryEntry, error) {
    return s.getByPrefix(ctx, "memory.global.")
}

// GetForAgent retrieves global + agent-specific memory.
func (s *Store) GetForAgent(ctx context.Context, agentID string) ([]*MemoryEntry, error) {
    global, err := s.GetGlobal(ctx)
    if err != nil {
        return nil, err
    }
    agent, err := s.getByPrefix(ctx, "memory.agent."+agentID+".")
    if err != nil {
        return nil, err
    }
    return append(global, agent...), nil
}

// Watch subscribes to memory changes in real-time.
func (s *Store) Watch(ctx context.Context, keyPattern string) (<-chan MemoryEvent, error) {
    // KV watch with key filter
    watcher, err := s.kv.Watch(ctx, keyPattern)
    // ... similar to task watcher
}
```

### E. Pi Bridge (`internal/bridge/`)

The bridge connects the Go swarm layer to existing TypeScript Pi agents.

```go
package bridge

// PiBridge connects a TypeScript Pi agent process to the swarm.
// It wraps Pi's existing RPC mode (JSON over stdin/stdout) and maps
// swarm events to Pi commands and vice versa.
type PiBridge struct {
    cmd     *exec.Cmd
    stdin   io.WriteCloser
    stdout  *bufio.Scanner
    swarm   *swarmapi.Client
    agentID string
}

// Start launches a Pi agent in RPC mode and bridges it to the swarm.
func (b *PiBridge) Start(ctx context.Context) error {
    b.cmd = exec.CommandContext(ctx, "pi", "--mode", "rpc")
    b.stdin, _ = b.cmd.StdinPipe()
    stdout, _ := b.cmd.StdoutPipe()
    b.stdout = bufio.NewScanner(stdout)
    b.cmd.Start()

    // Inject shared memory as system context
    memories, _ := b.swarm.Memory().GetForAgent(ctx, b.agentID)
    for _, mem := range memories {
        b.sendCommand(RpcCommand{
            Command: "steer",
            Data: map[string]any{
                "message": fmt.Sprintf("[Shared Memory: %s]\n%s", mem.Key, mem.Content),
            },
        })
    }

    go b.readLoop(ctx)  // read Pi events, publish to swarm
    go b.watchLoop(ctx) // watch swarm events, send to Pi

    return nil
}

// readLoop reads Pi RPC events and maps them to swarm actions.
func (b *PiBridge) readLoop(ctx context.Context) {
    for b.stdout.Scan() {
        var event RpcEvent
        json.Unmarshal(b.stdout.Bytes(), &event)

        switch event.Type {
        case "tool_result_end":
            // If the tool was "todo", sync result to shared tasks
            if event.Data.ToolName == "todo" {
                b.syncTodoToSwarm(ctx, event.Data)
            }
        case "message_end":
            // Share context fragment with swarm
            b.swarm.Context().Append(ctx, ContextEntry{
                AgentID: b.agentID,
                Type:    ContextMessage,
                Role:    "assistant",
                Content: event.Data.Content,
            })
        }
    }
}

// watchLoop watches swarm events and injects them into the Pi agent.
func (b *PiBridge) watchLoop(ctx context.Context) {
    taskEvents, _ := b.swarm.Tasks().Watch(ctx)
    memEvents, _ := b.swarm.Memory().Watch(ctx, "memory.global.>")

    for {
        select {
        case te := <-taskEvents:
            if te.Task.ClaimedBy == b.agentID && te.Operation == "PUT" {
                // New task assigned to us — send as follow-up prompt
                b.sendCommand(RpcCommand{
                    Command: "follow_up",
                    Data: map[string]any{
                        "message": fmt.Sprintf("New task assigned: %s\n\n%s",
                            te.Task.Title, te.Task.Description),
                    },
                })
            }
        case me := <-memEvents:
            // Shared memory updated — steer agent with new context
            b.sendCommand(RpcCommand{
                Command: "steer",
                Data: map[string]any{
                    "message": fmt.Sprintf("[Memory Updated: %s]\n%s",
                        me.Entry.Key, me.Entry.Content),
                },
            })
        case <-ctx.Done():
            return
        }
    }
}
```

---

## Interaction Flows

### Flow 1: Collaborative Task Execution

```
  Pi-A (user facing)              Swarm (NATS)              Pi-B (background)
  ─────────────────              ────────────              ─────────────────
        │                              │                          │
  User: "Build the API              │                          │
    and write tests"                   │                          │
        │                              │                          │
  Creates 2 tasks ──────────► KV.Put(task-1: "Build API")       │
                    ──────────► KV.Put(task-2: "Write tests")   │
                    ──────────►   depends_on: [task-1]          │
        │                              │                          │
  Claims task-1 ────────────► KV.Update(task-1, claimed=Pi-A)  │
        │                              │                          │
  Working on API...                    │                          │
        │                              │                          │
  Publishes progress ───────► swarm.tasks.1.progress ─────────► │
                                       │                   (watching)
        │                              │                          │
  Completes task-1 ─────────► KV.Update(task-1, done) ────────► │
                                       │                          │
                                 Scheduler detects               │
                                 task-2 deps met ───────────────► │
                                       │                   Claims task-2
                                       │                          │
                                       │                   Writes tests
                                       │                   using Pi-A's
                                       │                   API code as
                                       │                   context
                                       │                          │
                               ◄──────────────────────── Completes task-2
        │                              │                          │
  User sees: "All tasks done"          │                          │
```

### Flow 2: Shared Memory Update

```
  Pi-A                         Swarm (NATS)                    Pi-B
  ────                         ────────────                    ────
    │                              │                             │
  Discovers project uses           │                             │
  specific conventions             │                             │
    │                              │                             │
  memory.Put(                      │                             │
    "memory.global.conventions",   │                             │
    "Use snake_case for..."  ─────►│                             │
  )                                │                             │
                                   │──── KV Watch fires ───────►│
                                   │                             │
                                   │                  Pi-B injects into
                                   │                  its system prompt:
                                   │                  "Use snake_case..."
                                   │                             │
                                   │                  All future code
                                   │                  from Pi-B follows
                                   │                  the convention
```

### Flow 3: Context Sharing (Real-time Collaboration)

```
  Pi-A                         Swarm                          Pi-B
  ────                         ─────                          ────
    │                            │                              │
  User asks Pi-A about           │                              │
  auth implementation            │                              │
    │                            │                              │
  Pi-A reads auth code,          │                              │
  publishes insight ────────►    │                              │
    "auth uses JWT with          │                              │
     RS256, tokens in            │                              │
     httpOnly cookies"           │──── context.append ────────►│
                                 │                              │
                                 │                   Pi-B now knows
                                 │                   about auth without
                                 │                   re-reading the code
                                 │                              │
  User asks Pi-B to              │                              │
  "add rate limiting             │                              │
   to the auth endpoints"        │                              │
                                 │                              │
                                 │                   Pi-B already has
                                 │                   context about JWT
                                 │                   and cookies — no
                                 │                   duplicate work
```

---

## Startup & Clustering

### Single Agent (Development)

```bash
pi-swarm --agent-id pi-alpha --data-dir ~/.pi-swarm/data
# Starts embedded NATS on :4222, cluster port :6222
# JetStream with single-replica buckets
# Bridges to local `pi` process
```

### Multi-Agent Cluster

```bash
# Terminal 1: First node (becomes initial seed)
pi-swarm --agent-id pi-alpha --port 4222 --cluster-port 6222

# Terminal 2: Second node (joins via seed)
pi-swarm --agent-id pi-beta --port 4223 --cluster-port 6223 \
         --seeds "nats://localhost:6222"

# Terminal 3: Third node (joins via either seed)
pi-swarm --agent-id pi-gamma --port 4224 --cluster-port 6224 \
         --seeds "nats://localhost:6222,nats://localhost:6223"
```

Once 3+ nodes form, JetStream uses R3 replication. Any node can go down
without data loss.

### Remote Clustering

```bash
# Machine A (10.0.1.1)
pi-swarm --agent-id pi-alpha --port 4222 --cluster-port 6222 \
         --advertise 10.0.1.1

# Machine B (10.0.1.2)
pi-swarm --agent-id pi-beta --port 4222 --cluster-port 6222 \
         --advertise 10.0.1.2 \
         --seeds "nats://10.0.1.1:6222"
```

---

## KV Buckets & Object Stores Summary

| Store              | Type          | Replicas | TTL  | Purpose                              |
|--------------------|---------------|----------|------|--------------------------------------|
| `SWARM_TASKS`      | KV            | 3        | —    | Task state (CRUD + CAS)             |
| `SWARM_MEMORY`     | KV            | 3        | —    | Shared memory entries                |
| `SWARM_CONTEXT`    | KV            | 3        | —    | Context fragments                    |
| `SWARM_PRESENCE`   | KV            | 1        | 30s  | Agent heartbeats (ephemeral)         |
| `SWARM_LOCKS`      | KV            | 3        | 60s  | Distributed locks (leader election)  |
| `SWARM_SESSIONS`   | Object Store  | 3        | —    | Full session JSONL transcripts       |
| `SWARM_ARTIFACTS`  | Object Store  | 3        | —    | Large files, memory docs >1MB       |

---

## NATS Subject Hierarchy

```
swarm.
├── tasks.
│   └── <task-id>.
│       ├── created       # new task
│       ├── claimed       # agent claimed task
│       ├── progress      # work-in-progress update
│       ├── completed     # task finished
│       ├── failed        # task failed
│       └── released      # agent released claim
├── memory.
│   ├── global.>          # global memory changes
│   ├── team.<id>.>       # team memory changes
│   └── agent.<id>.>      # agent memory changes
├── context.
│   └── <session-id>.>    # context entries for a session
├── presence.
│   └── <agent-id>.
│       ├── heartbeat     # periodic heartbeat
│       ├── joined        # agent came online
│       └── left          # agent went offline
└── rpc.
    └── <agent-id>.
        ├── request       # request/reply to specific agent
        └── broadcast     # broadcast to all agents
```

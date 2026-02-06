# Distributed Pi: Architecture Comparison

## The Two Approaches

### Approach A: Shared Tool Server via NATS

```
┌───────────┐   ┌───────────┐   ┌───────────┐
│   Pi 1    │   │   Pi 2    │   │   Pi 3    │
│ (no tools)│   │ (no tools)│   │ (no tools)│
└─────┬─────┘   └─────┬─────┘   └─────┬─────┘
      │               │               │
      │    tool calls via NATS        │
      └───────────────┼───────────────┘
                      ▼
            ┌─────────────────┐
            │   Tool Server   │
            │  (one sandbox)  │
            │                 │
            │  ┌───────────┐  │
            │  │   Repo    │  │
            │  │  (state)  │  │
            │  └───────────┘  │
            └─────────────────┘

Pros:
  ✓ Single source of truth
  ✓ Real-time shared state
  ✓ Lower total resource usage

Cons:
  ✗ Complex coordination (who's editing what?)
  ✗ Single point of failure
  ✗ Latency for every tool call
  ✗ Serialization bottleneck
```

### Approach B: Per-Agent Sandbox (E2B Style)

```
┌───────────────────────────────────────────────────────────────────┐
│                                                                   │
│  ┌───────────┐       ┌───────────┐       ┌───────────┐           │
│  │   Pi 1    │       │   Pi 2    │       │   Pi 3    │           │
│  │           │       │           │       │           │           │
│  │ ┌───────┐ │       │ ┌───────┐ │       │ ┌───────┐ │           │
│  │ │Sandbox│ │       │ │Sandbox│ │       │ │Sandbox│ │           │
│  │ │ repo  │ │       │ │ repo  │ │       │ │ repo  │ │           │
│  │ │clone 1│ │       │ │clone 2│ │       │ │clone 3│ │           │
│  │ └───────┘ │       │ └───────┘ │       │ └───────┘ │           │
│  └───────────┘       └───────────┘       └───────────┘           │
│                                                                   │
│                         NATS (coordination only)                  │
│                              │                                    │
│                              ▼                                    │
│                    ┌─────────────────┐                           │
│                    │   Coordinator   │                           │
│                    │  (git merging)  │                           │
│                    └─────────────────┘                           │
│                              │                                    │
│                              ▼                                    │
│                         Final Repo                                │
│                                                                   │
└───────────────────────────────────────────────────────────────────┘

Pros:
  ✓ Full isolation (security)
  ✓ No coordination during work
  ✓ Parallel execution (truly parallel)
  ✓ Simpler model (each agent is independent)
  ✓ Natural failure isolation

Cons:
  ✗ More resources (N clones)
  ✗ Merge conflicts at the end
  ✗ Agents can't see each other's in-progress work
```

---

## Honest Assessment: E2B Model is Usually Better

For most coding tasks, **Approach B (per-agent sandbox) wins**:

| Factor | Shared Tool Server | Per-Agent Sandbox |
|--------|-------------------|-------------------|
| Complexity | High (locking, queuing) | Low (isolated) |
| Parallelism | Limited (serialized tools) | Full |
| Security | Shared = more risk | Isolated = safe |
| Failure handling | One failure affects all | Isolated failures |
| State management | Complex (who owns what?) | Simple (each owns sandbox) |
| Merge strategy | Continuous (complex) | End-of-task (git) |

**When Shared Tool Server makes sense:**
- Interactive pair programming (need real-time sync)
- Stateful long-running processes (databases, servers)
- Very large repos (can't clone N times)
- Sequential pipeline (one agent feeds next)

**When Per-Agent Sandbox makes sense:**
- Parallel independent tasks (most common)
- Security isolation required
- Different agents, different skills
- Tasks that produce mergeable artifacts (code, docs)

---

## The Right Architecture: Hybrid

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          Pi Platform                                     │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                     NATS (Coordination Layer)                    │   │
│  │                                                                  │   │
│  │  • Task assignment                                               │   │
│  │  • Progress updates                                              │   │
│  │  • Result collection                                             │   │
│  │  • Agent discovery                                               │   │
│  │  • Shared memory (KV store)                                      │   │
│  │                                                                  │   │
│  │  NOT for tool execution                                          │   │
│  │                                                                  │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│                                                                         │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐        │
│  │ Sandbox Pool    │  │ Sandbox Pool    │  │ Sandbox Pool    │        │
│  │ (E2B/Firecracker│  │ (E2B/Firecracker│  │ (E2B/Firecracker│        │
│  │                 │  │                 │  │                 │        │
│  │ ┌─────────────┐ │  │ ┌─────────────┐ │  │ ┌─────────────┐ │        │
│  │ │ Pi Agent    │ │  │ │ Pi Agent    │ │  │ │ Pi Agent    │ │        │
│  │ │ + tools     │ │  │ │ + tools     │ │  │ │ + tools     │ │        │
│  │ │ + repo      │ │  │ │ + repo      │ │  │ │ + repo      │ │        │
│  │ └─────────────┘ │  │ └─────────────┘ │  │ └─────────────┘ │        │
│  │                 │  │                 │  │                 │        │
│  │ Local bash,     │  │ Local bash,     │  │ Local bash,     │        │
│  │ local files,    │  │ local files,    │  │ local files,    │        │
│  │ local git       │  │ local git       │  │ local git       │        │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘        │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                     Go Coordinator                               │   │
│  │                                                                  │   │
│  │  • Provision sandboxes (E2B API / Firecracker)                  │   │
│  │  • Clone repos into sandboxes                                    │   │
│  │  • Assign tasks to sandboxes                                     │   │
│  │  • Collect results                                               │   │
│  │  • Merge git branches                                            │   │
│  │  • Handle conflicts                                              │   │
│  │                                                                  │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## What NATS is For (and Not For)

### NATS IS for:
```go
// Task distribution
nc.Publish("tasks.high.coder", task)

// Progress updates
nc.Publish("progress.task-123", update)

// Agent heartbeats
nc.Publish("agents.pi-1.heartbeat", status)

// Shared context (KV)
kv.Put("project.style-guide", styleGuide)
kv.Put("project.architecture", archDoc)

// Results
nc.Publish("results.task-123", result)

// Events
nc.Publish("events.task-123.file-written", event)
```

### NATS is NOT for:
```go
// ❌ DON'T: Tool execution over NATS
nc.Request("tools.bash", "npm test")  // High latency, complex

// ❌ DON'T: File operations over NATS
nc.Request("tools.read", "/src/auth.ts")  // Unnecessary overhead

// ❌ DON'T: Real-time file sync
nc.Publish("files.changed", fileContent)  // Too much traffic
```

**Tools run locally in the sandbox. NATS coordinates work between sandboxes.**

---

## Conflict Prevention: Git Worktree Model

Instead of locking paths, use Git's natural isolation:

```
Main Repo (origin)
     │
     ├── Pi 1 works on branch: feature/oauth-provider
     │   └── Sandbox 1 has full clone
     │
     ├── Pi 2 works on branch: feature/oauth-tests
     │   └── Sandbox 2 has full clone
     │
     └── Pi 3 works on branch: feature/oauth-docs
         └── Sandbox 3 has full clone

At completion:
  Coordinator merges all branches → main
  If conflict: Coordinator agent resolves OR human review
```

**Why this works:**
- Each agent has full freedom in their branch
- No runtime coordination needed
- Git handles the hard merge logic
- Conflicts are rare if tasks are well-decomposed

---

## When Shared Tool Server DOES Make Sense

### Scenario: Long-Running Stateful Service

```
┌─────────────────────────────────────────────────────────────────────────┐
│                      Shared Database/Server                              │
│                                                                         │
│   ┌───────────────────────────────────────────────────────────────┐    │
│   │               Persistent Sandbox                               │    │
│   │                                                                │    │
│   │    ┌─────────────┐  ┌─────────────┐  ┌─────────────┐          │    │
│   │    │ PostgreSQL  │  │   Redis     │  │  App Server │          │    │
│   │    │ (stateful)  │  │ (stateful)  │  │ (running)   │          │    │
│   │    └─────────────┘  └─────────────┘  └─────────────┘          │    │
│   │                                                                │    │
│   └───────────────────────────────────────────────────────────────┘    │
│                              ▲                                          │
│                              │ NATS tool calls                          │
│         ┌────────────────────┼────────────────────┐                    │
│         ▼                    ▼                    ▼                    │
│   ┌───────────┐        ┌───────────┐        ┌───────────┐             │
│   │   Pi 1    │        │   Pi 2    │        │   Pi 3    │             │
│   │ (migrate) │        │ (test)    │        │ (debug)   │             │
│   └───────────┘        └───────────┘        └───────────┘             │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘

Use case: Database migration, integration testing, debugging production
- Agents need to interact with same running services
- State is too expensive to replicate
- Sequential operations on shared state
```

### Scenario: Interactive Development

```
Human (laptop) ←──── watching ────→ Shared Sandbox
      │                                    ▲
      │ "fix the bug I'm seeing"          │
      ▼                                    │
    Pi Agent ────── tool calls ────────────┘

Use case: Human + AI pair programming
- Human needs to see changes in real-time
- Shared filesystem is the point
- Single sandbox is simpler
```

---

## The E2B/Firecracker Integration

### Option 1: Use E2B Directly

```go
// Go coordinator using E2B SDK
import "github.com/e2b-dev/sdk-go"

func provisionSandbox(task Task) (*e2b.Sandbox, error) {
    sandbox, err := e2b.NewSandbox(ctx, e2b.SandboxOpts{
        Template: "pi-agent",  // Custom template with Pi installed
    })
    if err != nil {
        return nil, err
    }

    // Clone repo
    sandbox.Process.Start("git", "clone", task.Repo, "/workspace")

    // Start Pi in RPC mode
    sandbox.Process.Start("pi", "--rpc", "--cwd", "/workspace")

    return sandbox, nil
}

// E2B pricing: ~$0.05/hour for 1 vCPU sandbox
// 10 agents for 1 hour = $0.50
```

### Option 2: Self-Hosted Firecracker

```go
// Firecracker microVM management
import "github.com/firecracker-microvm/firecracker-go-sdk"

func provisionMicroVM(task Task) (*MicroVM, error) {
    cfg := firecracker.Config{
        SocketPath:      "/tmp/firecracker.sock",
        KernelImagePath: "/path/to/vmlinux",
        RootDrive: firecracker.RootDrive{
            Path:     "/path/to/rootfs.ext4",  // Has Pi pre-installed
            ReadOnly: false,
        },
        MachineCfg: firecracker.MachineCfg{
            VcpuCount:  2,
            MemSizeMib: 2048,
        },
    }

    vm, err := firecracker.NewMachine(ctx, cfg)
    if err != nil {
        return nil, err
    }

    vm.Start(ctx)
    // SSH in, clone repo, start Pi
    return vm, nil
}

// Self-hosted: Just compute costs
// More control, more ops work
```

### Option 3: Kubernetes + gVisor/Kata

```yaml
# Kubernetes pod with Pi agent
apiVersion: v1
kind: Pod
metadata:
  name: pi-agent-1
spec:
  runtimeClassName: gvisor  # Or kata for VM isolation
  containers:
    - name: pi
      image: pi-platform/pi-agent:latest
      command: ["pi", "--rpc"]
      volumeMounts:
        - name: workspace
          mountPath: /workspace
  initContainers:
    - name: git-clone
      image: alpine/git
      command: ["git", "clone", "$(REPO_URL)", "/workspace"]
      volumeMounts:
        - name: workspace
          mountPath: /workspace
  volumes:
    - name: workspace
      emptyDir: {}
```

---

## Recommended Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Pi Platform                                      │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                      Go API Gateway                              │   │
│  │  POST /tasks, GET /status, WS /stream                           │   │
│  └───────────────────────────────┬─────────────────────────────────┘   │
│                                  │                                      │
│  ┌───────────────────────────────▼─────────────────────────────────┐   │
│  │                         NATS JetStream                           │   │
│  │  Coordination, messaging, shared KV (NOT tool execution)        │   │
│  └───────────────────────────────┬─────────────────────────────────┘   │
│                                  │                                      │
│  ┌───────────────────────────────▼─────────────────────────────────┐   │
│  │                      Go Coordinator                              │   │
│  │                                                                  │   │
│  │  1. Receive task                                                 │   │
│  │  2. Provision sandbox (E2B/Firecracker/K8s)                     │   │
│  │  3. Clone repo into sandbox                                      │   │
│  │  4. Start Pi agent in sandbox (RPC mode)                        │   │
│  │  5. Send task to Pi via RPC                                      │   │
│  │  6. Stream progress to NATS                                      │   │
│  │  7. Collect result                                               │   │
│  │  8. Push branch / merge                                          │   │
│  │  9. Destroy sandbox                                              │   │
│  │                                                                  │   │
│  └──────────────┬────────────────┬────────────────┬────────────────┘   │
│                 │                │                │                     │
│    ┌────────────▼───┐ ┌─────────▼────┐ ┌────────▼─────┐               │
│    │ E2B Sandbox    │ │ E2B Sandbox  │ │ E2B Sandbox  │               │
│    │                │ │              │ │              │               │
│    │ Pi + tools     │ │ Pi + tools   │ │ Pi + tools   │               │
│    │ repo clone     │ │ repo clone   │ │ repo clone   │               │
│    │ branch: task-1 │ │ branch: task2│ │ branch: task3│               │
│    │                │ │              │ │              │               │
│    │ LOCAL tools    │ │ LOCAL tools  │ │ LOCAL tools  │               │
│    └────────────────┘ └──────────────┘ └──────────────┘               │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘

Tools (bash, file, git) run LOCAL to each sandbox.
NATS handles coordination, NOT tool execution.
Each sandbox is fully isolated.
Git merges bring it all together.
```

---

## Summary: Why Not Shared Tool Server?

| Shared Tool Server via NATS | Per-Agent Sandbox (E2B) |
|-----------------------------|-------------------------|
| Every tool call goes over network | Tools run locally (~0 latency) |
| Complex locking for file access | No locking needed |
| Single point of failure | Isolated failures |
| Hard to parallelize | Embarrassingly parallel |
| Complex state management | Simple (each owns state) |
| Security: shared = risky | Security: isolated = safe |

**Bottom line:** Using NATS for tool execution adds complexity without benefit.
NATS should coordinate work; sandboxes should execute work.

The E2B model is simpler AND more powerful for most use cases.

---

## When to Use What

```
Need real-time shared state?
  → Shared sandbox (one Pi, one sandbox)

Need parallel independent work?
  → Per-agent sandboxes (E2B model)

Need both?
  → Hybrid: per-agent sandboxes + shared services (DB) via NATS
```

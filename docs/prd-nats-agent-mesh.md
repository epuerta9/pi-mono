# PRD: NATS Agent Mesh

**Tagline:** *Every agent is the infrastructure.*

**Status:** Draft
**Date:** 2026-02-14
**Author:** Claude / epuerta9

---

## 1. Problem Statement

Multi-agent coding collaboration is the defining challenge of 2026. The industry has converged on the primitives needed — communication, persistence, compute, orchestration — but every solution builds them as **separate cloud services** that agents run on top of:

- **Warp Oz**: Cloud sandboxes + Warp Drive storage + proprietary orchestration
- **Claude Code Agent Teams**: tmux/iTerm2 panes + file locking + in-memory coordination
- **Gas Town**: Git as the persistence layer + ephemeral worker processes
- **OpenHands**: Event-stream architecture + centralized server
- **OpenClaw**: Local gateway + multi-provider routing

These approaches share a common pattern: **infrastructure is external to the agent**. The agent is a client. Someone else runs the message bus, the state store, the coordination layer.

This creates friction:
- You need infrastructure *before* you can collaborate
- Local development requires simulating cloud services
- Cross-tool collaboration (Claude Code + OpenHands + custom agents) requires a shared platform neither controls
- Scaling from 1 agent to 50 requires ops work, not just launching more agents

### What if the agent *was* the infrastructure?

---

## 2. Vision

A single Go binary that is simultaneously a **coding agent**, a **message broker**, a **KV store**, an **object store**, and a **cluster node**. Launch one and it works alone. Launch two and they auto-discover and collaborate. Launch fifty across ten machines and they form a mesh.

No central server. No cloud dependency. No configuration beyond "here's a seed address."

**This is not a framework.** It's a runtime that makes multi-agent collaboration an emergent property of the architecture, not a feature you build on top.

---

## 3. Why NATS Embedded in Go

### 3.1 Why NATS

NATS is the only technology that provides **all five of Zach Lloyd's primitives** in a single embeddable library:

| Primitive | NATS Feature | Alternative Requires |
|-----------|-------------|---------------------|
| Agent communication | Core pub/sub + request/reply | Kafka, RabbitMQ, Redis, custom protocol |
| Reliable task distribution | JetStream consumers (work queues) | Celery, Bull, SQS, custom queuing |
| Persistent state | JetStream KV with watches | Redis, etcd, Consul, PostgreSQL |
| Artifact storage | JetStream Object Store | S3, MinIO, shared filesystem |
| Cluster formation | Embedded server with gossip | Consul, etcd, manual configuration |

NATS is a CNCF incubating project with clients in 30+ languages, sub-millisecond latency, and a 10MB binary footprint. It was designed for exactly this: **lightweight, embeddable, zero-config distributed systems**.

### 3.2 Why Go

- **Single static binary**: No runtime (Node, Python, JVM). Ship one file.
- **Native NATS embedding**: `github.com/nats-io/nats-server/v2/server` — import and start. First-class support, same codebase as production NATS.
- **Goroutines + channels**: The agent loop (stream LLM → execute tool → stream result → loop) maps naturally. Steering messages are channel sends. Concurrent tool execution is `go func()`.
- **Cross-compilation**: `GOOS=linux GOARCH=arm64 go build` — done. Every platform, every architecture.
- **Memory efficiency**: A Go agent with embedded NATS uses ~30MB. A Node.js Pi instance uses ~150MB+. At 50 agents, this matters.

### 3.3 Why Embedded (Not Sidecar)

The agent could connect to an external NATS cluster. But embedding changes the operational model fundamentally:

```
External NATS:   Deploy NATS → Configure → Deploy agents → Connect → Collaborate
Embedded NATS:   Deploy agents → Collaborate
```

Each agent binary carries its own NATS server. When agents discover each other, their embedded servers form a cluster automatically. The "infrastructure" scales exactly with the number of agents — no over-provisioning, no under-provisioning, no provisioning at all.

---

## 4. Architecture

### 4.1 Single Binary Structure

```
┌─────────────────────────────────────────────────────────┐
│  nam (NATS Agent Mesh) - Single Go Binary               │
│                                                         │
│  ┌───────────────┐  ┌───────────────┐  ┌─────────────┐ │
│  │  Agent Core   │  │  Embedded     │  │  Protocol    │ │
│  │               │  │  NATS Server  │  │  Adapters    │ │
│  │  - LLM calls  │  │               │  │             │ │
│  │  - Tool exec  │  │  - Pub/Sub    │  │  - A2A      │ │
│  │  - Session    │  │  - JetStream  │  │  - MCP      │ │
│  │  - Compact    │  │  - KV Store   │  │  - RPC      │ │
│  │  - TUI/CLI    │  │  - Obj Store  │  │  - Custom   │ │
│  └───────┬───────┘  └───────┬───────┘  └──────┬──────┘ │
│          │                  │                  │        │
│          └──────────────────┼──────────────────┘        │
│                             │                           │
│                      NATS Client                        │
│                     (in-process)                         │
└─────────────────────┬───────────────────────────────────┘
                      │
            Cluster gossip / routes
                      │
┌─────────────────────┴───────────────────────────────────┐
│  Another nam instance (auto-discovered peer)            │
└─────────────────────────────────────────────────────────┘
```

### 4.2 Operational Modes

```
# Solo mode — just a coding agent, NATS runs locally only
nam

# Mesh mode — auto-cluster with peers
nam --seed agent-2.local:6222

# Join mode — connect to existing mesh, no embedded server
nam --connect nats://cluster.internal:4222

# Headless mode — no TUI, controllable via NATS messages
nam --headless --seed agent-2.local:6222

# Human mode — TUI that observes and steers a mesh of agents
nam --observe --connect nats://cluster.internal:4222
```

### 4.3 Subject Namespace

```
mesh.                              # Top-level namespace
├── agents.                        # Agent lifecycle
│   ├── {id}.announce              # Agent joins mesh (pub)
│   ├── {id}.heartbeat             # Periodic liveness (pub)
│   ├── {id}.status                # KV: current state
│   └── {id}.capabilities          # KV: tools, models, skills
│
├── tasks.                         # Work distribution (JetStream)
│   ├── {project}.unassigned       # Work queue — agents pull tasks
│   ├── {project}.assigned.{id}    # Claimed tasks
│   └── {project}.completed        # Done tasks (audit log)
│
├── comms.                         # Agent-to-agent messaging
│   ├── broadcast                  # All agents (pub/sub)
│   ├── direct.{id}               # Point-to-point (request/reply)
│   └── topic.{name}              # Topic channels (pub/sub)
│
├── artifacts.                     # Shared files (Object Store)
│   ├── {project}.patches          # Diffs, PRs
│   ├── {project}.context          # Shared context summaries
│   └── {project}.results          # Test results, reviews
│
├── coordination.                  # Distributed coordination
│   ├── locks.{path}               # File/resource locking (KV)
│   ├── conflicts                  # Conflict detection signals
│   └── merge-queue                # Ordered merge requests
│
├── human.                         # Human-agent interface
│   ├── steer.{id}                 # Redirect specific agent
│   ├── steer.all                  # Redirect all agents
│   ├── approve.{id}               # Approval gates
│   └── observe                    # Full event stream for UI
│
└── tools.                         # Remote tool registry
    ├── builtin.{name}             # Built-in tools (read/write/bash)
    └── extension.{name}           # External tools (any language)
```

---

## 5. How Other Coding Agents Join the Mesh

This is the critical design decision. The mesh is **not** a closed system. Any coding agent — Claude Code, OpenHands, OpenClaw, Codex, a custom script — can participate by implementing a thin NATS adapter.

### 5.1 The Bridge Pattern

```
┌──────────────┐     ┌──────────────┐     ┌───────────────┐
│ Claude Code  │────▶│ NATS Bridge  │────▶│               │
│ (TypeScript) │◀────│ (sidecar)    │◀────│               │
└──────────────┘     └──────────────┘     │               │
                                          │  NATS Mesh    │
┌──────────────┐     ┌──────────────┐     │               │
│ OpenHands    │────▶│ NATS Bridge  │────▶│               │
│ (Python)     │◀────│ (sidecar)    │◀────│               │
└──────────────┘     └──────────────┘     │               │
                                          │               │
┌──────────────┐                          │               │
│ nam          │══════════════════════════▶│               │
│ (Go,embedded)│◀═════════════════════════│               │
└──────────────┘                          └───────────────┘
```

A **bridge** is a lightweight process (~5MB Go binary) that:
1. Speaks the agent's native protocol (Claude Code RPC over stdin/stdout, OpenHands event stream, MCP, etc.)
2. Speaks NATS
3. Translates between them

### 5.2 Bridge Implementations

**Claude Code Bridge:**
```
claude-code-bridge
├── Spawns Claude Code in RPC mode (--rpc)
├── Subscribes to mesh.tasks.{project}.unassigned
├── Translates task → Claude Code RPC "prompt" command
├── Publishes tool executions to mesh.human.observe
├── Publishes results to mesh.tasks.{project}.completed
└── Responds to mesh.human.steer.{id} → RPC "steer" command
```

**OpenHands Bridge:**
```
openhands-bridge
├── Connects to OpenHands server API
├── Subscribes to mesh.tasks.{project}.unassigned
├── Translates task → OpenHands AgentDelegateAction
├── Streams events to mesh.human.observe
├── Publishes results to mesh.tasks.{project}.completed
└── Responds to mesh.human.steer.{id} → OpenHands API
```

**MCP Bridge (universal):**
```
mcp-bridge
├── Exposes mesh tools as MCP tools (any MCP-compatible agent can use)
├── mesh.artifacts.read → MCP resource
├── mesh.comms.direct → MCP tool
├── mesh.tasks.claim → MCP tool
└── Any MCP host (Claude Desktop, Cursor, etc.) becomes mesh-aware
```

**A2A Bridge:**
```
a2a-bridge
├── Exposes each mesh agent as an A2A Agent Card
├── Maps A2A tasks to mesh.tasks subjects
├── Translates A2A message/artifact parts ↔ NATS messages/objects
└── Any A2A-compatible platform can discover and use mesh agents
```

### 5.3 Minimal Integration Contract

An agent only needs to handle **4 NATS interactions** to participate in the mesh:

```
1. ANNOUNCE    → Publish to mesh.agents.{id}.announce
               { id, name, capabilities, model }

2. CLAIM TASK  → Pull from mesh.tasks.{project}.unassigned
               → Publish to mesh.tasks.{project}.assigned.{id}

3. COMMUNICATE → Publish/subscribe to mesh.comms.*
               { from, to, type, content }

4. COMPLETE    → Publish to mesh.tasks.{project}.completed
               { task_id, result, artifacts[] }
```

Everything else — state tracking, heartbeats, observation, locking — is optional and progressive. An agent that only speaks these 4 messages is a full mesh participant.

---

## 6. Agent-to-Agent Communication

### 6.1 Message Format

```json
{
  "id": "msg_abc123",
  "from": "agent-1",
  "to": "agent-2",          // or "*" for broadcast
  "type": "request",        // request | response | signal | artifact
  "subject": "review",      // semantic intent
  "content": "I've finished the auth middleware. Can you review src/auth.go?",
  "context": {
    "task_id": "task_42",
    "branch": "feature/auth",
    "files_touched": ["src/auth.go", "src/auth_test.go"]
  },
  "timestamp": "2026-02-14T10:30:00Z"
}
```

### 6.2 Communication Patterns

**Pattern 1: Task Delegation (JetStream Work Queue)**
```
Orchestrator → mesh.tasks.myapp.unassigned
  "Implement OAuth login flow"
  "Write integration tests for user API"
  "Review and fix failing CI on main"

Agents pull tasks based on capability matching.
JetStream handles: load balancing, redelivery on timeout, exactly-once.
```

**Pattern 2: Peer Consultation (Request/Reply)**
```
Agent-1 → mesh.comms.direct.agent-2 (request)
  "I'm about to modify src/db/schema.go. Are you touching that file?"

Agent-2 → reply
  "Yes, I'm adding a migration. Hold off — I'll signal when done."

Agent-2 → mesh.comms.topic.file-releases (signal)
  "src/db/schema.go is free"
```

**Pattern 3: Context Sharing (KV + Object Store)**
```
// Agent finishes exploring a codebase area
Agent-1 → KV: mesh.agents.agent-1.context
  { summary: "Auth system uses JWT with refresh tokens, see src/auth/*",
    files_understood: ["src/auth/jwt.go", "src/auth/middleware.go"] }

// Other agents read this before duplicating work
Agent-3 → KV Watch: mesh.agents.*.context
  "Agent-1 already understands auth. I'll ask them instead of re-reading."
```

**Pattern 4: Conflict Resolution (Pub/Sub Signals)**
```
// Lock before editing
Agent-1 → KV: mesh.coordination.locks.src/main.go
  { owner: "agent-1", task: "task_42", since: "..." }

// Detect conflict
Agent-2 → KV Watch: mesh.coordination.locks.src/main.go
  "Locked by agent-1. I'll work on something else."

// Or negotiate
Agent-2 → mesh.comms.direct.agent-1
  "I also need main.go for task_43. Can we coordinate?"
```

**Pattern 5: Human Steering (Pub/Sub)**
```
// Human sends global redirect
Human → mesh.human.steer.all
  "Stop all auth work. We're switching to OAuth2 with PKCE, not basic JWT."

// All agents receive, acknowledge, adjust
Agent-* → mesh.comms.broadcast
  "Acknowledged. Dropping current auth approach."
```

**Pattern 6: Approval Gates**
```
// Agent needs human approval before destructive action
Agent-1 → mesh.human.approve.agent-1
  { action: "delete_migration", file: "db/migrations/003.sql", reason: "..." }

// Human approves or denies
Human → mesh.human.approve.agent-1.response
  { approved: true, comment: "Go ahead, that migration was a mistake" }
```

### 6.3 Why This Beats Current Approaches

| Approach | Communication | Limitation |
|----------|--------------|------------|
| **Claude Code Agent Teams** | File locking + tmux panes | Single machine, same tool only, no persistent state between teammates |
| **Gas Town** | Git commits + structured bead files | Slow (git roundtrip), no real-time coordination, context dies with agent |
| **OpenHands** | Event stream + delegation actions | Centralized server, same framework only |
| **OpenClaw** | Internal routing between configured agents | Single gateway process, not distributed |
| **NATS Mesh** | Pub/sub + KV + Object Store + Work Queues | Real-time, distributed, persistent, any agent, any language, any machine |

---

## 7. Who's Getting Closest (February 2026)

### 7.1 Protocol Layer — A2A + MCP

The industry has standardized on **A2A for agent-to-agent** and **MCP for agent-to-tool**. Both are now under the Linux Foundation. This is the right layering:

```
MCP  = "What tools can you use?"     (vertical: agent ↔ tool)
A2A  = "What can you do for me?"     (horizontal: agent ↔ agent)
NATS = "How do we actually talk?"    (transport: the wire protocol)
```

A2A defines agent cards, task lifecycle, and message formats — but it's **transport-agnostic**. It currently assumes HTTP/SSE. NATS would be a natural A2A transport binding that adds: pub/sub fan-out, persistent queues, KV state, and clustering — things HTTP/SSE can't do natively.

### 7.2 Orchestration Layer — Nobody's Got It Right Yet

- **Claude Code Agent Teams**: Best DX but limited to single machine, same tool, requires specific terminal emulators. No persistent coordination state.
- **Warp Oz**: Right vision (cloud-native, orchestrated) but centralized platform dependency. Lock-in risk.
- **Gas Town**: Most philosophically honest ("embrace chaos, git is truth") but $100/hr burn rate and no real-time coordination proves the point — you need a communication layer beyond git.
- **claude-flow**: Impressive community effort (54+ agent types) but TypeScript + WASM, not embeddable infrastructure.
- **VS Code 1.109**: Multi-agent *within VS Code* — right idea, wrong scope. Agents shouldn't need an IDE to collaborate.

### 7.3 The Gap

Everyone is building orchestration **on top of** infrastructure they don't control or that doesn't exist yet. The missing piece is a **self-contained, embeddable coordination runtime** that any agent can carry with it.

That's what NATS embedded in Go is. Not an orchestration framework. Not a platform. A **runtime primitive** that makes distributed coordination as natural as reading a file.

---

## 8. Extension Model

The self-extending problem from the Pi-to-Go port discussion is solved by NATS naturally.

### 8.1 Extensions as NATS Services

```
# A tool extension (any language)
→ Subscribes to: mesh.tools.extension.jira-search
→ Receives: { query: "auth bugs", project: "MYAPP" }
→ Returns: { results: [...] }

# A hook extension
→ Subscribes to: mesh.agents.*.status (KV watch)
→ Reacts to agent state changes
→ Publishes to: mesh.comms.broadcast or mesh.human.observe

# A skill extension
→ Subscribes to: mesh.tools.extension.deploy-staging
→ Executes multi-step workflow
→ Publishes progress to: mesh.comms.topic.deploys
```

### 8.2 Extension Discovery

```
# Extensions announce themselves like agents
→ Publish to: mesh.tools.extension.{name}.announce
  { name, description, input_schema, output_schema }

# Agents discover available tools via KV
→ KV Bucket: tool-registry
→ Key: mesh.tools.extension.{name}
→ Value: { schema, endpoint_subject, provider_agent }
```

### 8.3 Advantages Over In-Process Extensions

| | Pi (TypeScript in-process) | nam (NATS services) |
|-|---------------------------|---------------------|
| Language | TypeScript only | Any language with NATS client (30+) |
| Isolation | Shares process, can crash agent | Separate process, fault-isolated |
| Distribution | Local only | Anywhere on the mesh |
| Discovery | File-system scan at startup | Dynamic registration, hot-reload |
| Scaling | One instance per agent | Shared across all mesh agents |
| Security | Full process access | Scoped to NATS subjects |

---

## 9. Implementation Phases

### Phase 1: Solo Agent (Weeks 1-4)
- Port Pi's core agent loop to Go (LLM streaming, tool execution, session management)
- Built-in tools: read, write, edit, bash
- Anthropic + OpenAI provider support
- Basic TUI (Bubble Tea)
- JSONL session persistence
- **No NATS yet** — prove the agent works standalone

### Phase 2: Embedded NATS (Weeks 5-8)
- Embed NATS server in the binary
- Implement subject namespace (Section 4.3)
- KV-backed agent state
- Object Store for artifact sharing
- Auto-clustering between instances
- `--seed` flag for mesh formation

### Phase 3: Multi-Agent Coordination (Weeks 9-12)
- JetStream work queue for task distribution
- File locking via KV
- Peer consultation (request/reply)
- Context sharing between agents
- Human steering via pub/sub
- `--observe` mode for human oversight TUI

### Phase 4: Bridges & Protocols (Weeks 13-16)
- Claude Code RPC bridge
- MCP bridge (mesh tools as MCP resources)
- A2A bridge (mesh agents as A2A agent cards)
- OpenHands bridge
- Extension-as-NATS-service model
- Dynamic tool discovery

### Phase 5: Production Hardening (Weeks 17-20)
- Conflict resolution strategies
- Merge queue coordination
- Cost tracking and token budgets per agent
- Approval gates for destructive actions
- Metrics and observability (NATS built-in)
- Security: NATS auth, TLS, subject permissions

---

## 10. Success Metrics

1. **Solo parity**: nam in solo mode matches Pi's coding capabilities
2. **Zero-config mesh**: Two `nam` instances on the same network auto-discover and collaborate without configuration
3. **Heterogeneous mesh**: A mesh containing nam + Claude Code (via bridge) + a Python script successfully coordinates on a shared task
4. **Scale**: 20 agents across 5 machines complete a multi-file refactoring with zero merge conflicts
5. **Extension ecosystem**: A tool written in Python runs as a NATS service and is discoverable by all mesh agents

---

## 11. Open Questions

1. **Consensus on merge order**: When multiple agents finish simultaneously, who merges first? NATS ordering guarantees + JetStream sequence numbers give us total order, but the merge *strategy* is a policy question.
2. **Context deduplication**: If Agent A and Agent B both summarize the same file, how do we avoid redundant context? KV watches help detect this, but the compaction strategy needs design.
3. **Token budgets**: How do we allocate LLM spending across agents? Per-task budgets? Global pool? This is an orchestration policy, not a runtime concern.
4. **A2A alignment**: A2A is the emerging standard. Should NATS be an A2A transport binding, or should the mesh be A2A-native with NATS as an implementation detail?
5. **Git strategy**: Worktree-per-agent (like ccswarm), branch-per-agent (like Gas Town), or something else? NATS solves communication but git merge strategy is orthogonal.
6. **Trust model**: In a heterogeneous mesh, how much do agents trust each other's output? Peer review as a first-class primitive?

---

## 12. Competitive Position

```
                        Cloud-Dependent
                             ▲
                             │
                   Warp Oz ● │ ● OpenHands Cloud
                             │
                             │
         ● VS Code 1.109     │
  Single-Tool ◄──────────────┼──────────────► Multi-Tool
                             │
              Claude Code  ● │
              Agent Teams    │
                             │  ● Gas Town
                     nam ◉   │
                             │
                             ▼
                       Self-Contained
```

nam occupies a unique position: **self-contained AND multi-tool**. It doesn't require cloud infrastructure (unlike Oz) and it doesn't require all agents to be the same tool (unlike Claude Code Agent Teams). The embedded NATS approach means the infrastructure *is* the agent, and the bridge pattern means any agent can join.

---

*Every agent is the infrastructure.*

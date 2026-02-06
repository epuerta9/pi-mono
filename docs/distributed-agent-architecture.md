# Distributed Agent Architecture

## The Core Insight

A single agent (Claude Code, Pi) is great for focused work, but hits limits:
- Context window fills up on large codebases
- Sequential execution (can only do one thing at a time)
- No parallelization of independent tasks
- No specialization (one agent does everything)
- No persistent memory across sessions

A distributed system addresses all of these.

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         NATS JetStream Cluster                          │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌────────────┐           │
│  │ tasks.*    │ │ results.*  │ │ memory.*   │ │ health.*   │           │
│  │ (work q)   │ │ (outcomes) │ │ (shared)   │ │ (heartbeat)│           │
│  └────────────┘ └────────────┘ └────────────┘ └────────────┘           │
└─────────────────────────────────────────────────────────────────────────┘
        │                │                │                │
        │    ┌───────────┴────────────────┴───────────┐    │
        │    │                                        │    │
        ▼    ▼                                        ▼    ▼
┌──────────────────┐                          ┌──────────────────┐
│   Coordinator    │                          │    Specialist    │
│   (Go binary)    │                          │     Agents       │
│                  │                          │                  │
│ - Task routing   │                          │ - Test Runner    │
│ - Agent spawning │                          │ - Code Reviewer  │
│ - Git management │                          │ - Doc Writer     │
│ - Cost tracking  │                          │ - Security Audit │
│ - Conflict res.  │                          │ - Refactorer     │
└──────────────────┘                          └──────────────────┘
        │
        │ manages
        ▼
┌──────────────────────────────────────────────────────────────────────┐
│                        Agent Pool                                     │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌────────────┐        │
│  │ Claude Code│ │ Claude Code│ │    Pi      │ │  Go Agent  │        │
│  │ Worker 1   │ │ Worker 2   │ │  Worker    │ │  (fast)    │        │
│  │            │ │            │ │            │ │            │        │
│  │ [worktree] │ │ [worktree] │ │ [worktree] │ │ [worktree] │        │
│  └────────────┘ └────────────┘ └────────────┘ └────────────┘        │
└──────────────────────────────────────────────────────────────────────┘
        │
        │ all work on
        ▼
┌──────────────────────────────────────────────────────────────────────┐
│                     Shared Git Repository                             │
│  main ─────●─────●─────●─────●─────●                                 │
│             \   /       \   /                                         │
│              ● ●         ● ●   ← agent branches auto-merged          │
└──────────────────────────────────────────────────────────────────────┘
```

---

## How You Connect: Local Claude Code → Cluster

### Option 1: MCP Tool (Cleanest)

Your local Claude Code gets a `cluster` tool via MCP:

```json
// ~/.claude/settings.json
{
  "mcpServers": {
    "swarm": {
      "command": "swarm-mcp",
      "args": ["--nats", "nats://cluster.example.com:4222"]
    }
  }
}
```

Now in Claude Code you can say:
```
"Delegate the test fixing to the cluster while I work on the feature"
```

And Claude Code calls:
```
cluster.delegate({
  task: "Fix all failing tests in src/api/",
  priority: "high",
  wait: false  // async - I'll keep working
})
```

### Option 2: CLI Command

```bash
# From your terminal, while Claude Code runs locally
swarm delegate "Run security audit on the authentication module"
swarm delegate "Write tests for the new user service" --wait
swarm status  # See what's running
swarm results # Get completed work
```

### Option 3: Pi/Claude Code Extension

```typescript
// ~/.pi/agent/extensions/swarm.ts
import { ExtensionFactory } from "@anthropic/pi-coding-agent";

export default ((pi) => {
  pi.registerTool({
    name: "swarm_delegate",
    description: "Delegate a task to the distributed agent cluster",
    parameters: Type.Object({
      task: Type.String({ description: "What to do" }),
      specialist: Type.Optional(Type.String({
        description: "Specific agent type: test-runner, reviewer, docs, security"
      })),
      priority: Type.Optional(Type.Union([
        Type.Literal("low"),
        Type.Literal("normal"),
        Type.Literal("high")
      ])),
      wait: Type.Optional(Type.Boolean({ description: "Wait for result" }))
    }),
    async execute(id, params, signal, onUpdate, ctx) {
      const nc = await connect({ servers: process.env.NATS_URL });
      const js = nc.jetstream();

      // Publish task
      await js.publish("tasks.new", JSON.encode({
        id: crypto.randomUUID(),
        task: params.task,
        specialist: params.specialist,
        priority: params.priority,
        repo: await getGitRemote(),
        branch: await getCurrentBranch(),
        requestedBy: os.hostname(),
      }));

      if (params.wait) {
        // Subscribe to result
        const result = await waitForResult(id);
        return { content: [{ type: "text", text: result }] };
      }

      return { content: [{ type: "text", text: "Task delegated to cluster" }] };
    }
  });

  // Also register a tool to check status
  pi.registerTool({
    name: "swarm_status",
    description: "Check status of delegated tasks",
    // ...
  });
}) satisfies ExtensionFactory;
```

---

## Part 2: Distributed Agent Roles

### The Specialist Model

Instead of one generalist agent, have specialists:

| Role | What It Does | Why Separate? |
|------|--------------|---------------|
| **Architect** | High-level design, task decomposition | Needs broad context, low frequency |
| **Coder** | Implements features | Needs deep focus, parallel instances |
| **Test Runner** | Writes/runs tests, fixes failures | Can run continuously in background |
| **Reviewer** | Code review, suggestions | Fresh eyes, different context |
| **Doc Writer** | Documentation, comments | Different skill, can parallelize |
| **Security** | Audit, vulnerability scanning | Specialized knowledge |
| **Refactorer** | Tech debt, cleanup | Low priority, background work |
| **Researcher** | Explores solutions, reads docs | Web access, long-running |

### Why This Beats Single Agent

**Single Agent (Claude Code):**
```
You: "Add auth, write tests, update docs, review for security"

Claude Code:
  1. Adds auth (15 min)
  2. Writes tests (10 min)
  3. Updates docs (5 min)
  4. Security review (5 min)

Total: 35 min sequential
```

**Distributed:**
```
You: "Add auth, write tests, update docs, review for security"

Coordinator decomposes:
  → Coder Agent: Add auth
  → (waits for auth done)
  → Test Agent: Write tests     ┐
  → Doc Agent: Update docs      ├─ parallel
  → Security Agent: Review      ┘

Total: 15 min + 10 min = 25 min (and you kept working locally)
```

---

## Part 3: A Real Workflow Where Distributed Excels

### Scenario: "Refactor authentication to use OAuth2"

**Single Agent Approach (painful):**
```
Session 1: Understand current auth system
Session 2: Design OAuth2 integration
Session 3: Implement OAuth2 provider
Session 4: Update all endpoints
Session 5: Fix tests (they're all broken)
Session 6: Update documentation
Session 7: Security review

Problems:
- Context lost between sessions
- Can't parallelize
- One failure blocks everything
- Takes 2-3 days
```

**Distributed Approach:**

```
┌─────────────────────────────────────────────────────────────────────┐
│  You (local Claude Code)                                            │
│  "Refactor auth to OAuth2"                                          │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│  Coordinator receives task, decomposes:                             │
│                                                                     │
│  Phase 1 (Research - parallel):                                     │
│    → Researcher: "Analyze current auth in src/auth/"                │
│    → Researcher: "Find OAuth2 best practices for Go/Node"           │
│    → Security: "Audit current auth vulnerabilities"                 │
│                                                                     │
│  Phase 2 (Design):                                                  │
│    → Architect: Design OAuth2 integration (uses Phase 1 results)    │
│    → Output: Design doc + task breakdown                            │
│                                                                     │
│  Phase 3 (Implementation - parallel):                               │
│    → Coder 1: Implement OAuth2 provider (src/auth/oauth2.go)        │
│    → Coder 2: Update user service (src/services/user.go)            │
│    → Coder 3: Update API handlers (src/api/handlers/)               │
│                                                                     │
│  Phase 4 (Validation - parallel):                                   │
│    → Test Agent: Run existing tests, note failures                  │
│    → Test Agent: Write new OAuth2 tests                             │
│    → Security: Audit new implementation                             │
│    → Doc Agent: Update API docs and README                          │
│                                                                     │
│  Phase 5 (Fix):                                                     │
│    → Coder: Fix any test failures (loop until green)                │
│                                                                     │
│  Phase 6 (Merge):                                                   │
│    → Coordinator: Merge all branches, resolve conflicts             │
│    → Reviewer: Final review of complete changeset                   │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│  Result: PR ready for human review                                  │
│  Time: 2-4 hours (vs 2-3 days)                                      │
│  Cost: ~$20-50 (vs same $ spread over days)                         │
└─────────────────────────────────────────────────────────────────────┘
```

### What You Did During This Time

```
While cluster worked on OAuth2:

You (local Claude Code):
  - Built the new dashboard UI
  - Fixed that urgent bug in production
  - Reviewed a teammate's PR

Periodically:
  "swarm status" → See OAuth2 at Phase 3
  "swarm results security" → Read security audit findings

End of day:
  "swarm results" → OAuth2 PR ready for review
```

---

## Part 4: Team Collaboration Patterns

### Pattern 1: Shared Cluster, Personal Agents

```
┌──────────────────────────────────────────────────────────────────────┐
│                        Team Cluster (always running)                 │
│                                                                      │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐                  │
│  │ Test Runner │  │ Reviewer    │  │ Security    │                  │
│  │ (24/7)      │  │ (on-demand) │  │ (scheduled) │                  │
│  └─────────────┘  └─────────────┘  └─────────────┘                  │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
         ▲                 ▲                 ▲
         │                 │                 │
    ┌────┴────┐       ┌────┴────┐       ┌────┴────┐
    │  Alice  │       │   Bob   │       │ Charlie │
    │ (local) │       │ (local) │       │ (local) │
    │         │       │         │       │         │
    │ Claude  │       │   Pi    │       │ Claude  │
    │ Code    │       │         │       │ Code    │
    └─────────┘       └─────────┘       └─────────┘

Alice: "swarm delegate 'Review Bob's PR #123'"
Bob: "swarm delegate 'Run full test suite'"
Charlie: "swarm delegate 'Security audit before release'"
```

### Pattern 2: The Async Handoff

```
Monday 9am - Alice (SF):
  "Implement user profile feature"
  → Starts work locally
  → Gets 60% done
  → "swarm continue 'Finish user profile, I'll be back tomorrow'"
  → Goes home

Monday 6pm - Cluster:
  → Picks up Alice's branch
  → Completes remaining 40%
  → Writes tests
  → Opens draft PR

Tuesday 9am - Alice:
  → "swarm status"
  → Sees completed PR
  → Reviews, makes small tweaks
  → Merges
```

### Pattern 3: The Code Review Pipeline

Every PR automatically gets:
```
┌────────────┐     ┌────────────┐     ┌────────────┐     ┌────────────┐
│ PR Created │ ──▶ │ Test Agent │ ──▶ │  Reviewer  │ ──▶ │  Security  │
│            │     │ runs tests │     │  comments  │     │   audit    │
└────────────┘     └────────────┘     └────────────┘     └────────────┘
                          │                  │                  │
                          ▼                  ▼                  ▼
                   ┌──────────────────────────────────────────────────┐
                   │ PR has: test results, review comments, security │
                   │ findings BEFORE human looks at it               │
                   └──────────────────────────────────────────────────┘
```

### Pattern 4: The Swarm Attack

For massive tasks (migrations, major refactors):

```
"Migrate all 200 API endpoints from REST to GraphQL"

Coordinator:
  → Analyzes codebase, finds 200 endpoints
  → Groups into 20 batches of 10
  → Spins up 20 parallel Coder agents
  → Each agent:
      - Gets isolated worktree
      - Migrates 10 endpoints
      - Writes tests
      - Commits to feature branch
  → Coordinator merges all 20 branches
  → Final agent runs full test suite
  → Human reviews one comprehensive PR

Time: 2-3 hours (vs 2-3 weeks manually)
```

---

## Part 5: The Economics

### When Distributed Makes Sense

| Task Type | Single Agent | Distributed | Winner |
|-----------|--------------|-------------|--------|
| Quick bug fix | $0.50, 5 min | $2 overhead | Single |
| Feature (small) | $3, 30 min | $5, 15 min | Depends |
| Feature (large) | $15, 3 hours | $20, 1 hour | Distributed |
| Major refactor | $50, 2 days | $100, 4 hours | Distributed |
| Migration | $200, 2 weeks | $300, 1 day | Distributed |

**The crossover point:** Tasks taking >1 hour sequentially benefit from distribution.

### Cost Control Strategies

```go
type CostPolicy struct {
    MaxDailySpend     float64  // $100/day cap
    MaxParallelAgents int      // 10 agents max
    PreferCheapModel  bool     // Haiku for simple tasks
    QueueLowPriority  bool     // Batch low-pri overnight
}
```

---

## Why This Isn't Obvious (Yet)

1. **Tooling doesn't exist** - Gas Town is closest, but 17 days old
2. **Coordination is hard** - Git conflicts, context sharing, coherent output
3. **Cost visibility** - Easy to burn $100/hour accidentally
4. **Trust** - Do you trust 20 agents modifying your codebase?
5. **Debugging** - When something breaks, which agent did it?

The value is real, but the UX and tooling need to catch up.

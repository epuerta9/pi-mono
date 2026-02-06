# Solving the Hard Problems in Distributed Agents

## Problem 1: How Does a Remote Agent Access Your Local Repo?

### The Reality

Your local machine has:
- The git repository
- Your uncommitted changes
- Your context (what you're working on)
- Claude Code's todo list

A Pi agent on AWS has:
- Nothing. It's a fresh container.

### Solution: Git + Context Sync Protocol

```
┌─────────────────────────────────────────────────────────────────────────┐
│ Your Local Machine                                                      │
│                                                                         │
│  ┌───────────────┐    ┌───────────────┐    ┌───────────────┐           │
│  │ Your Repo     │    │ Claude Code   │    │ Swarm MCP     │           │
│  │ (with changes)│    │ (with todos)  │    │ Server        │           │
│  └───────┬───────┘    └───────┬───────┘    └───────┬───────┘           │
│          │                    │                    │                    │
│          │   "delegate tests" │                    │                    │
│          │◄───────────────────┤                    │                    │
│          │                    │                    │                    │
└──────────┼────────────────────┼────────────────────┼────────────────────┘
           │                    │                    │
           │ Step 1: Push WIP   │ Step 2: Publish    │
           │ branch             │ task + context     │
           ▼                    ▼                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                          NATS + Git Server                              │
│                                                                         │
│  ┌───────────────────────┐    ┌───────────────────────────────────────┐│
│  │ Git Remote            │    │ NATS                                  ││
│  │ (GitHub/GitLab)       │    │                                       ││
│  │                       │    │ tasks.delegate:                       ││
│  │ branches:             │    │ {                                     ││
│  │   main                │    │   task_id: "abc123",                  ││
│  │   feature/oauth       │    │   repo: "github.com/you/app",         ││
│  │   swarm/abc123-tests ◄┼────┼── branch: "swarm/abc123-tests",       ││
│  │   (WIP auto-pushed)   │    │   context: {                          ││
│  │                       │    │     files_touched: ["src/auth/*"],    ││
│  │                       │    │     recent_changes: "Added OAuth...", ││
│  │                       │    │     todos: ["Write tests", ...],      ││
│  │                       │    │     focus_areas: ["src/auth/"]        ││
│  │                       │    │   },                                  ││
│  │                       │    │   task: "Write tests for OAuth"       ││
│  │                       │    │ }                                     ││
│  └───────────────────────┘    └───────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────────────┘
           │                                         │
           │ Step 3: Clone branch                    │ Step 4: Receive task
           ▼                                         ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Remote Agent (Pi on AWS)                                                │
│                                                                         │
│  1. Receives task from NATS                                             │
│  2. git clone --branch swarm/abc123-tests                               │
│  3. Reads context: knows what changed, what to focus on                 │
│  4. Works on task                                                       │
│  5. Commits to swarm/abc123-tests                                       │
│  6. Publishes result to NATS                                            │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
           │
           │ Step 5: Result + branch ready
           ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Back to Your Local Machine                                              │
│                                                                         │
│  Claude Code receives result via NATS:                                  │
│  "Tests written. 47 new tests, all passing."                            │
│                                                                         │
│  You: "Great, merge those in"                                           │
│  Claude Code: git fetch && git merge swarm/abc123-tests                 │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### The Context Package

What gets sent with each delegated task:

```go
type TaskContext struct {
    // Git context
    Repo       string   `json:"repo"`        // github.com/you/app
    Branch     string   `json:"branch"`      // swarm/task-123
    BaseBranch string   `json:"base_branch"` // feature/oauth (what to diff against)

    // What changed (so agent knows what's relevant)
    FilesTouched   []string `json:"files_touched"`    // ["src/auth/*"]
    RecentChanges  string   `json:"recent_changes"`   // Compacted summary

    // What you're doing (from Claude Code's state)
    CurrentTodos   []string `json:"current_todos"`    // Your todo list
    FocusAreas     []string `json:"focus_areas"`      // Directories you're in

    // Explicit instructions
    Task           string   `json:"task"`             // "Write tests for OAuth"
    Constraints    []string `json:"constraints"`      // "Don't modify src/api/"

    // Shared memory (from NATS KV)
    ProjectContext string   `json:"project_context"`  // From memory.project
    StyleGuide     string   `json:"style_guide"`      // From memory.style
}
```

### Implementation: Auto-Push WIP Branch

```go
// Before delegating, push your current state
func (s *SwarmMCP) prepareTaskBranch(taskID, task string) (*TaskContext, error) {
    // Create a WIP branch for this task
    branchName := fmt.Sprintf("swarm/%s", taskID)

    // Stash any uncommitted changes, create branch, apply stash
    exec.Command("git", "stash").Run()
    exec.Command("git", "checkout", "-b", branchName).Run()
    exec.Command("git", "stash", "pop").Run()

    // Commit WIP state (agent will work from here)
    exec.Command("git", "add", "-A").Run()
    exec.Command("git", "commit", "-m", fmt.Sprintf("WIP: %s", task)).Run()

    // Push to remote
    exec.Command("git", "push", "-u", "origin", branchName).Run()

    // Switch back to original branch
    exec.Command("git", "checkout", "-").Run()

    // Build context
    return &TaskContext{
        Repo:          getRemoteURL(),
        Branch:        branchName,
        FilesTouched:  getRecentlyTouchedFiles(),
        RecentChanges: summarizeRecentChanges(),
        CurrentTodos:  getClaudeCodeTodos(),
        Task:          task,
    }, nil
}
```

---

## Problem 2: Task Dependencies & Todo Integration

### The Problem

Claude Code has todos:
```
☐ Implement OAuth provider
☐ Write tests for OAuth
☐ Update API docs
☐ Security review
```

If we delegate "Write tests" but OAuth isn't done, the tests have nothing to test.

### Solution: Dependency DAG in NATS

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Task Dependency Graph                           │
│                                                                         │
│                    ┌──────────────────┐                                 │
│                    │ Implement OAuth  │                                 │
│                    │ (local - you)    │                                 │
│                    └────────┬─────────┘                                 │
│                             │                                           │
│              ┌──────────────┼──────────────┐                            │
│              │              │              │                            │
│              ▼              ▼              ▼                            │
│     ┌────────────┐  ┌────────────┐  ┌────────────┐                     │
│     │Write Tests │  │Update Docs │  │ Security   │                     │
│     │(cluster)   │  │(cluster)   │  │ (cluster)  │                     │
│     │            │  │            │  │            │                     │
│     │depends_on: │  │depends_on: │  │depends_on: │                     │
│     │[oauth]     │  │[oauth]     │  │[oauth,     │                     │
│     │            │  │            │  │ tests]     │                     │
│     └────────────┘  └────────────┘  └────────────┘                     │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### Task Schema with Dependencies

```go
type Task struct {
    ID          string    `json:"id"`
    Description string    `json:"description"`
    Status      string    `json:"status"`  // pending, blocked, running, done, failed

    // Dependencies
    DependsOn   []string  `json:"depends_on"`   // Task IDs that must complete first
    Blocks      []string  `json:"blocks"`       // Task IDs waiting on this

    // Where to run
    RunLocal    bool      `json:"run_local"`    // If true, stays on your machine
    Specialist  string    `json:"specialist"`   // "test-runner", "security", etc.

    // Completion signal
    Signal      string    `json:"signal"`       // NATS subject to publish when done
}
```

### Coordinator Handles Dependencies

```go
func (c *Coordinator) onTaskComplete(taskID string, result TaskResult) {
    task := c.tasks[taskID]
    task.Status = "done"

    // Find tasks blocked by this one
    for _, blockedID := range task.Blocks {
        blocked := c.tasks[blockedID]

        // Check if all dependencies are now satisfied
        allDone := true
        for _, depID := range blocked.DependsOn {
            if c.tasks[depID].Status != "done" {
                allDone = false
                break
            }
        }

        if allDone {
            // Unblock and dispatch
            blocked.Status = "pending"
            c.dispatch(blocked)
        }
    }

    // Notify NATS
    c.nc.Publish(task.Signal, result)
}
```

### Claude Code Todo Sync

Your local Claude Code's todos sync with the cluster:

```typescript
// Extension: sync-todos-to-cluster.ts
pi.on("todo_updated", async (event, ctx) => {
    const todos = event.todos;

    // Convert to cluster tasks
    const tasks = todos.map(todo => ({
        id: todo.id,
        description: todo.content,
        status: mapStatus(todo.status),
        run_local: !todo.delegated,
        depends_on: todo.dependsOn || [],
    }));

    // Publish to NATS
    await nc.publish("tasks.sync", JSON.stringify({
        source: "claude-code",
        user: os.hostname(),
        tasks
    }));
});

// When cluster completes a task, update local todo
nc.subscribe("results.*", (msg) => {
    const result = JSON.parse(msg.data);
    pi.updateTodo(result.task_id, { status: "completed" });
});
```

---

## Problem 3: Multiple Devs, One Cluster - Avoiding Conflicts

### The Problem

```
Alice: "Delegate: refactor auth module"
Bob:   "Delegate: add new auth endpoint"

Both modify src/auth/ → CONFLICT
```

### Solution: Work Isolation + Smart Routing

```
┌─────────────────────────────────────────────────────────────────────────┐
│                    Coordinator: Work Isolation                          │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                    Active Work Registry                         │   │
│  │                                                                 │   │
│  │  Locked Paths:                                                  │   │
│  │    src/auth/*     → Alice (task: abc123, branch: swarm/abc123) │   │
│  │    src/api/users  → Bob   (task: def456, branch: swarm/def456) │   │
│  │    src/utils/*    → [available]                                 │   │
│  │                                                                 │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│                                                                         │
│  When new task arrives:                                                 │
│    1. Analyze task → what files will it touch?                          │
│    2. Check against locked paths                                        │
│    3. If conflict:                                                      │
│       a. Queue task (wait for lock release)                             │
│       b. OR notify user of conflict                                     │
│       c. OR route to same worker (serialize)                            │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### Path Lock Implementation

```go
type WorkRegistry struct {
    locks map[string]*WorkLock  // path prefix → lock
    mu    sync.RWMutex
}

type WorkLock struct {
    Owner    string    // "alice@laptop"
    TaskID   string
    Branch   string
    Paths    []string  // ["src/auth/*", "tests/auth/*"]
    AcquiredAt time.Time
    ExpiresAt  time.Time  // Auto-release if agent dies
}

func (r *WorkRegistry) TryAcquire(owner string, paths []string) (*WorkLock, []Conflict, error) {
    r.mu.Lock()
    defer r.mu.Unlock()

    conflicts := []Conflict{}

    for _, path := range paths {
        for lockedPath, lock := range r.locks {
            if pathsOverlap(path, lockedPath) && lock.Owner != owner {
                conflicts = append(conflicts, Conflict{
                    Path:        path,
                    LockedBy:    lock.Owner,
                    LockedPath:  lockedPath,
                    TaskID:      lock.TaskID,
                })
            }
        }
    }

    if len(conflicts) > 0 {
        return nil, conflicts, nil  // Can't acquire, return conflicts
    }

    // Acquire locks
    lock := &WorkLock{
        Owner:      owner,
        Paths:      paths,
        AcquiredAt: time.Now(),
        ExpiresAt:  time.Now().Add(30 * time.Minute),
    }

    for _, path := range paths {
        r.locks[path] = lock
    }

    return lock, nil, nil
}
```

### Conflict Resolution Strategies

```go
type ConflictStrategy int

const (
    // Wait for the lock to be released
    StrategyQueue ConflictStrategy = iota

    // Tell the user about the conflict
    StrategyNotify

    // Route to the same worker (serialize the work)
    StrategySerialize

    // Fork: let both proceed, merge later (risky)
    StrategyFork
)

func (c *Coordinator) handleConflict(task Task, conflicts []Conflict, strategy ConflictStrategy) {
    switch strategy {
    case StrategyQueue:
        // Add to waiting queue, will dispatch when lock released
        c.waitingTasks[conflicts[0].TaskID] = append(
            c.waitingTasks[conflicts[0].TaskID],
            task,
        )
        c.notifyUser(task.Owner, fmt.Sprintf(
            "Task queued. Waiting for %s to finish with %s",
            conflicts[0].LockedBy,
            conflicts[0].LockedPath,
        ))

    case StrategyNotify:
        c.notifyUser(task.Owner, fmt.Sprintf(
            "⚠️ Conflict: %s is working on %s. Options:\n"+
            "1. Wait (swarm queue %s)\n"+
            "2. Work on something else\n"+
            "3. Coordinate with %s",
            conflicts[0].LockedBy,
            conflicts[0].LockedPath,
            task.ID,
            conflicts[0].LockedBy,
        ))

    case StrategySerialize:
        // Route this task to the same agent that has the lock
        existingTask := c.tasks[conflicts[0].TaskID]
        task.DependsOn = append(task.DependsOn, conflicts[0].TaskID)
        task.PreferWorker = existingTask.AssignedWorker
        c.dispatch(task)
    }
}
```

### Team Visibility: Who's Working On What

```
$ swarm status --team

Active Work:
┌──────────────┬────────────────────────────┬─────────────────┬──────────┐
│ Developer    │ Task                       │ Paths           │ Status   │
├──────────────┼────────────────────────────┼─────────────────┼──────────┤
│ alice@laptop │ Refactor auth module       │ src/auth/*      │ running  │
│ bob@desktop  │ Add user endpoint          │ src/api/users/* │ running  │
│ charlie@mac  │ Update docs                │ docs/*          │ running  │
│ [cluster]    │ Security audit             │ (read-only)     │ running  │
│ [cluster]    │ Run full test suite        │ (read-only)     │ running  │
└──────────────┴────────────────────────────┴─────────────────┴──────────┘

Queued:
  - bob: "Refactor auth tests" (waiting on alice's auth work)
```

---

## Problem 4: Beyond Code - Other Use Cases

### What Pi + NATS Enables

From exploring Pi's architecture, here are compelling non-code use cases:

### 1. **Incident Response Pipeline**

```
┌────────────┐    ┌────────────┐    ┌────────────┐    ┌────────────┐
│ Alert      │───▶│ Triage     │───▶│ Root Cause │───▶│ Remediate  │
│ Detector   │    │ Agent      │    │ Analyzer   │    │ Agent      │
└────────────┘    └────────────┘    └────────────┘    └────────────┘
     │                  │                  │                  │
     │   NATS: incident.detected    incident.triaged   incident.analyzed
     │                  │                  │                  │
     └──────────────────┴──────────────────┴──────────────────┘
                                │
                    ┌───────────┴───────────┐
                    │  Shared Context       │
                    │  - System topology    │
                    │  - Recent deployments │
                    │  - Historical issues  │
                    │  - Runbooks           │
                    └───────────────────────┘

Real example:
  1. Alert: "API latency > 500ms"
  2. Triage: "Affects /api/users, started 10min ago"
  3. Root Cause: "Database connection pool exhausted after deploy abc123"
  4. Remediate: "Rolled back to deploy xyz789, latency normalized"

Total time: 3 minutes (vs 30+ minutes with humans paging each other)
```

### 2. **Data Pipeline Orchestration**

```
Your Data Team's Workflow:
  "Process yesterday's sales data, load to warehouse, update dashboards"

┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
│ Extract  │───▶│ Validate │───▶│ Transform│───▶│ Load     │
│ Agent    │    │ Agent    │    │ Agent    │    │ Agent    │
└──────────┘    └──────────┘    └──────────┘    └──────────┘
     │                │               │               │
     │  NATS: etl.extract.done   etl.validate.done  etl.load.done
     │                │               │               │
     ▼                ▼               ▼               ▼
  S3 bucket      Quality report    Cleaned data    Warehouse
  (raw data)     (NATS KV)         (S3)            (BigQuery)

Benefits:
  - Each agent is specialized (SQL, Python, dbt)
  - Automatic retry on failure
  - Parallel processing of independent tables
  - Audit trail in NATS
```

### 3. **Research & Analysis Swarm**

```
"Analyze competitors' pricing strategies"

┌─────────────────────────────────────────────────────────────────────────┐
│                        Research Coordinator                              │
└───────────────────────────────────┬─────────────────────────────────────┘
                                    │
          ┌─────────────────────────┼─────────────────────────┐
          ▼                         ▼                         ▼
┌──────────────────┐    ┌──────────────────┐    ┌──────────────────┐
│ Web Researcher   │    │ Web Researcher   │    │ Web Researcher   │
│ (Competitor A)   │    │ (Competitor B)   │    │ (Competitor C)   │
└────────┬─────────┘    └────────┬─────────┘    └────────┬─────────┘
         │                       │                       │
         └───────────────────────┼───────────────────────┘
                                 ▼
                    ┌──────────────────────┐
                    │   Synthesis Agent    │
                    │   (combines findings)│
                    └──────────────────────┘
                                 │
                                 ▼
                    ┌──────────────────────┐
                    │   Report Generator   │
                    │   (creates deck)     │
                    └──────────────────────┘

Output: "Competitor A is 15% cheaper on enterprise, B has usage-based model..."
```

### 4. **Content Production Pipeline**

```
"Write a blog post about our new feature"

┌─────────────┐    ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│ Researcher  │───▶│ Outliner    │───▶│ Writer      │───▶│ Editor      │
│ (gathers    │    │ (structures │    │ (drafts     │    │ (polishes   │
│  context)   │    │  content)   │    │  content)   │    │  final)     │
└─────────────┘    └─────────────┘    └─────────────┘    └─────────────┘
       │                  │                  │                  │
       ▼                  ▼                  ▼                  ▼
  Feature docs       3-section          1500 word         Final post
  User feedback      outline            draft             + images
  Competitor posts

Parallel:
  → Image Generator: creates hero image, diagrams
  → SEO Optimizer: suggests title, meta, keywords
  → Social Writer: creates tweet thread, LinkedIn post
```

### 5. **Customer Support Escalation**

```
Customer: "I can't log in and I'm locked out of my account"

┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│ First Line  │───▶│ Specialist  │───▶│ Escalation  │
│ (common     │    │ (account    │    │ (human      │
│  issues)    │    │  recovery)  │    │  handoff)   │
└─────────────┘    └─────────────┘    └─────────────┘
       │
       ├── 80% resolved (password reset, cache clear)
       │
       └── 20% need specialist
                │
                ├── 90% resolved (account unlock, 2FA reset)
                │
                └── 10% need human (fraud, legal, exceptions)

Context flows through:
  - Customer history
  - Recent actions (from logs)
  - Account status
  - Previous tickets
```

### 6. **Continuous Learning System**

```
"Help me learn Kubernetes"

┌─────────────────────────────────────────────────────────────────────────┐
│                         Learning Coordinator                             │
│                                                                         │
│  Student Progress (NATS KV):                                            │
│    completed: [pods, deployments]                                       │
│    struggling: [services]                                               │
│    next: [ingress, configmaps]                                          │
│                                                                         │
└───────────────────────────────────┬─────────────────────────────────────┘
                                    │
          ┌─────────────────────────┼─────────────────────────┐
          ▼                         ▼                         ▼
┌──────────────────┐    ┌──────────────────┐    ┌──────────────────┐
│ Concept Teacher  │    │ Exercise Creator │    │ Progress Tracker │
│ (explains topics)│    │ (generates labs) │    │ (assesses work)  │
└──────────────────┘    └──────────────────┘    └──────────────────┘

Adaptive:
  - If struggling → simpler explanations, more examples
  - If fast → skip ahead, harder exercises
  - Memory: what analogies worked, what confused them
```

### 7. **Autonomous DevOps**

```
"Keep production healthy"

┌─────────────────────────────────────────────────────────────────────────┐
│                    24/7 Production Guardian                              │
│                                                                         │
│  Agents:                                                                │
│    ├── Metric Watcher (Prometheus/Datadog)                              │
│    ├── Log Analyzer (searches for errors)                               │
│    ├── Cost Monitor (cloud spend)                                       │
│    ├── Security Scanner (CVE alerts)                                    │
│    └── Capacity Planner (predicts scaling needs)                        │
│                                                                         │
│  On alert:                                                              │
│    1. Diagnose (what's wrong?)                                          │
│    2. Assess (how bad? who's affected?)                                 │
│    3. Remediate (can we auto-fix?)                                      │
│    4. Notify (page human if needed)                                     │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘

Example night shift:
  2:00 AM - Memory leak detected in service-x
  2:01 AM - Agent restarts service-x pod
  2:02 AM - Memory stable, no pages sent
  2:03 AM - Creates ticket: "Investigate memory leak in service-x"

  Morning: Dev reviews agent's analysis + proposed fix
```

---

## The Key Insight

**The value isn't "more agents" - it's specialization + parallelization + persistence.**

| Single Agent | Distributed Cluster |
|--------------|---------------------|
| One context window | Specialized contexts per agent |
| Sequential work | Parallel execution |
| Dies when you close laptop | Runs 24/7 |
| Your expertise only | Specialist knowledge |
| Manual handoffs | Automatic pipelines |

The cluster is most valuable when:
1. **Work is parallelizable** (tests, docs, review can happen simultaneously)
2. **Work is specialized** (security audit needs different skills than coding)
3. **Work is continuous** (monitoring, maintenance runs forever)
4. **Team needs coordination** (shared understanding, no conflicts)

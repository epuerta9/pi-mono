# Pi Platform: Distributed Agent Infrastructure

## The Vision

Turn Pi from a local coding agent into a **distributed agent platform**:

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        Pi Platform (API-First)                          │
│                                                                         │
│  "Deploy agents. Share tools. Scale infinitely. Everything is an API." │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              API Gateway                                 │
│                           (Go + REST/gRPC)                              │
│                                                                         │
│   POST /agents          - Deploy an agent                               │
│   POST /tasks           - Submit a task                                 │
│   GET  /tasks/{id}      - Get task status/result                        │
│   POST /tools           - Register a tool                               │
│   GET  /tools           - Discover available tools                      │
│   WS   /stream/{id}     - Stream agent output                           │
│                                                                         │
└───────────────────────────────────┬─────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                            NATS JetStream                                │
│                                                                         │
│  Streams:                                                               │
│    TASKS      - Durable task queue                                      │
│    RESULTS    - Task outcomes                                           │
│    TOOLS      - Tool registry (KV)                                      │
│    AGENTS     - Agent registry (KV)                                     │
│    EVENTS     - Real-time events                                        │
│                                                                         │
│  Subjects:                                                              │
│    tasks.{priority}.{specialist}  - Task routing                        │
│    agents.{id}.heartbeat          - Health monitoring                   │
│    tools.{name}.invoke            - Tool invocation                     │
│    events.{agent_id}.*            - Agent events                        │
│                                                                         │
└───────────────────────────────────┬─────────────────────────────────────┘
                                    │
          ┌─────────────────────────┼─────────────────────────┐
          ▼                         ▼                         ▼
┌──────────────────┐    ┌──────────────────┐    ┌──────────────────┐
│   Agent Node 1   │    │   Agent Node 2   │    │   Agent Node N   │
│                  │    │                  │    │                  │
│ ┌──────────────┐ │    │ ┌──────────────┐ │    │ ┌──────────────┐ │
│ │   Pi Agent   │ │    │   Pi Agent   │ │    │ │  Go Agent    │ │
│ │ (coding)     │ │    │ (testing)    │ │    │ │ (custom)     │ │
│ └──────────────┘ │    │ └──────────────┘ │    │ └──────────────┘ │
│                  │    │                  │    │                  │
│ ┌──────────────┐ │    │ ┌──────────────┐ │    │ ┌──────────────┐ │
│ │ Tool: git    │ │    │ │ Tool: test   │ │    │ │ Tool: deploy │ │
│ │ Tool: grep   │ │    │ │ Tool: lint   │ │    │ │ Tool: k8s    │ │
│ └──────────────┘ │    │ └──────────────┘ │    │ └──────────────┘ │
└──────────────────┘    └──────────────────┘    └──────────────────┘
```

---

## Core Concepts

### 1. Everything is an API

```yaml
# No CLI commands. No manual setup. Just HTTP/gRPC.

# Deploy an agent
POST /api/v1/agents
{
  "name": "my-coder",
  "type": "pi",
  "config": {
    "model": "claude-sonnet-4-20250514",
    "extensions": ["git", "test-runner"],
    "memory": "2Gi"
  }
}

# Submit a task
POST /api/v1/tasks
{
  "description": "Add OAuth2 to the auth module",
  "repo": "github.com/myorg/myapp",
  "branch": "feature/oauth",
  "agent": "my-coder",  # or omit for auto-routing
  "priority": "high"
}

# Get result
GET /api/v1/tasks/task-123
{
  "id": "task-123",
  "status": "completed",
  "result": {
    "summary": "Added OAuth2 provider with Google and GitHub support",
    "files_changed": ["src/auth/oauth.ts", "src/auth/providers/*"],
    "branch": "feature/oauth",
    "pr_url": "https://github.com/myorg/myapp/pull/456"
  }
}
```

### 2. Tools are Network Services

```yaml
# Register a tool (makes it available to ALL agents)
POST /api/v1/tools
{
  "name": "semantic-search",
  "description": "Search code semantically using embeddings",
  "endpoint": "nats://tools.semantic-search.invoke",
  "schema": {
    "input": {
      "query": "string",
      "repo": "string",
      "limit": "number?"
    },
    "output": {
      "results": [{ "file": "string", "line": "number", "score": "number" }]
    }
  }
}

# Now ANY agent can use it:
# Agent calls tool → Platform routes to tool service via NATS
```

### 3. Agents Discover Each Other

```yaml
# List available agents
GET /api/v1/agents
[
  { "name": "coder-1", "type": "pi", "status": "idle", "skills": ["typescript", "go"] },
  { "name": "tester-1", "type": "pi", "status": "busy", "skills": ["jest", "pytest"] },
  { "name": "security-1", "type": "custom", "status": "idle", "skills": ["audit", "scan"] }
]

# Agents can delegate to each other:
# coder-1: "I need tests written"
# → Platform routes to tester-1
# → tester-1 works
# → Result flows back to coder-1
```

---

## Go Platform Components

### 1. API Gateway (`pi-gateway`)

```go
// pi-gateway/main.go
package main

import (
    "github.com/gofiber/fiber/v2"
    "github.com/nats-io/nats.go"
)

type Gateway struct {
    nc     *nats.Conn
    js     nats.JetStreamContext
    agents *AgentRegistry
    tools  *ToolRegistry
    tasks  *TaskManager
}

func main() {
    gw := NewGateway()

    app := fiber.New()

    // Agent management
    app.Post("/api/v1/agents", gw.DeployAgent)
    app.Get("/api/v1/agents", gw.ListAgents)
    app.Delete("/api/v1/agents/:id", gw.StopAgent)

    // Task management
    app.Post("/api/v1/tasks", gw.SubmitTask)
    app.Get("/api/v1/tasks/:id", gw.GetTask)
    app.Get("/api/v1/tasks/:id/stream", gw.StreamTask)  // WebSocket

    // Tool management
    app.Post("/api/v1/tools", gw.RegisterTool)
    app.Get("/api/v1/tools", gw.ListTools)
    app.Post("/api/v1/tools/:name/invoke", gw.InvokeTool)

    // Webhooks
    app.Post("/api/v1/webhooks", gw.RegisterWebhook)

    app.Listen(":8080")
}

// Submit a task
func (gw *Gateway) SubmitTask(c *fiber.Ctx) error {
    var req TaskRequest
    c.BodyParser(&req)

    task := &Task{
        ID:          uuid.New().String(),
        Description: req.Description,
        Repo:        req.Repo,
        Branch:      req.Branch,
        Status:      "pending",
        CreatedAt:   time.Now(),
    }

    // Store task
    gw.tasks.Store(task)

    // Publish to NATS for routing
    subject := fmt.Sprintf("tasks.%s.%s", req.Priority, req.Specialist)
    gw.js.Publish(subject, task.ToJSON())

    return c.JSON(task)
}

// Stream task output via WebSocket
func (gw *Gateway) StreamTask(c *fiber.Ctx) error {
    taskID := c.Params("id")

    return websocket.New(func(ws *websocket.Conn) {
        // Subscribe to task events
        sub, _ := gw.nc.Subscribe(fmt.Sprintf("events.task.%s", taskID), func(msg *nats.Msg) {
            ws.WriteMessage(websocket.TextMessage, msg.Data)
        })
        defer sub.Unsubscribe()

        // Keep connection alive until task completes
        for {
            task := gw.tasks.Get(taskID)
            if task.Status == "completed" || task.Status == "failed" {
                break
            }
            time.Sleep(100 * time.Millisecond)
        }
    })(c)
}
```

### 2. Agent Runner (`pi-runner`)

```go
// pi-runner/main.go
// Runs on each node, manages Pi agent lifecycle

package main

import (
    "os/exec"
    "github.com/nats-io/nats.go"
)

type AgentRunner struct {
    nc       *nats.Conn
    nodeID   string
    agents   map[string]*ManagedAgent
}

type ManagedAgent struct {
    ID       string
    Type     string  // "pi", "custom"
    Process  *exec.Cmd
    Stdin    io.WriteCloser
    Stdout   io.ReadCloser
}

func (ar *AgentRunner) Start() {
    // Subscribe to agent deploy requests
    ar.nc.Subscribe("agents.deploy", func(msg *nats.Msg) {
        var req DeployRequest
        json.Unmarshal(msg.Data, &req)
        ar.deployAgent(req)
    })

    // Subscribe to tasks for our agents
    ar.nc.Subscribe("tasks.>", func(msg *nats.Msg) {
        var task Task
        json.Unmarshal(msg.Data, &task)
        ar.routeTask(task)
    })

    // Heartbeat
    go ar.heartbeatLoop()
}

func (ar *AgentRunner) deployAgent(req DeployRequest) *ManagedAgent {
    var cmd *exec.Cmd

    switch req.Type {
    case "pi":
        // Start Pi in RPC mode
        cmd = exec.Command("pi", "--rpc")
        cmd.Env = append(os.Environ(),
            fmt.Sprintf("ANTHROPIC_API_KEY=%s", req.APIKey),
        )
    case "custom":
        // Start custom Go agent
        cmd = exec.Command(req.Binary, req.Args...)
    }

    stdin, _ := cmd.StdinPipe()
    stdout, _ := cmd.StdoutPipe()
    cmd.Start()

    agent := &ManagedAgent{
        ID:      req.ID,
        Type:    req.Type,
        Process: cmd,
        Stdin:   stdin,
        Stdout:  stdout,
    }

    // Forward stdout to NATS
    go ar.forwardOutput(agent)

    ar.agents[req.ID] = agent
    return agent
}

func (ar *AgentRunner) routeTask(task Task) {
    // Find appropriate agent
    agent := ar.findAgent(task.Specialist)
    if agent == nil {
        return  // No agent available on this node
    }

    // Send task to agent via RPC
    rpcCmd := RPCCommand{
        ID:      task.ID,
        Type:    "prompt",
        Message: task.Description,
    }
    json.NewEncoder(agent.Stdin).Encode(rpcCmd)

    // Mark task as running
    ar.nc.Publish("tasks.status", StatusUpdate{
        TaskID: task.ID,
        Status: "running",
        Agent:  agent.ID,
    })
}
```

### 3. Tool Registry (`pi-tools`)

```go
// pi-tools/main.go
// Network-accessible tool services

package main

type ToolServer struct {
    nc    *nats.Conn
    tools map[string]ToolHandler
}

type ToolHandler func(ctx context.Context, input json.RawMessage) (json.RawMessage, error)

func (ts *ToolServer) RegisterTool(name string, handler ToolHandler) {
    ts.tools[name] = handler

    // Subscribe to invocations
    ts.nc.Subscribe(fmt.Sprintf("tools.%s.invoke", name), func(msg *nats.Msg) {
        result, err := handler(context.Background(), msg.Data)
        if err != nil {
            msg.Respond(errorResponse(err))
            return
        }
        msg.Respond(result)
    })

    // Register in tool registry
    ts.nc.Publish("tools.register", ToolRegistration{
        Name:        name,
        Endpoint:    fmt.Sprintf("tools.%s.invoke", name),
        Description: getDescription(handler),
    })
}

// Example tools
func main() {
    ts := NewToolServer()

    // Semantic search tool
    ts.RegisterTool("semantic-search", func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
        var req SemanticSearchRequest
        json.Unmarshal(input, &req)

        results := semanticSearch(req.Query, req.Repo, req.Limit)
        return json.Marshal(results)
    })

    // Git operations tool
    ts.RegisterTool("git", func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
        var req GitRequest
        json.Unmarshal(input, &req)

        switch req.Operation {
        case "status":
            return json.Marshal(gitStatus(req.Repo))
        case "diff":
            return json.Marshal(gitDiff(req.Repo, req.Base, req.Head))
        case "commit":
            return json.Marshal(gitCommit(req.Repo, req.Message, req.Files))
        }
        return nil, fmt.Errorf("unknown operation: %s", req.Operation)
    })

    // Test runner tool
    ts.RegisterTool("test", func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
        var req TestRequest
        json.Unmarshal(input, &req)

        results := runTestsParallel(req.Repo, req.Pattern, req.Workers)
        return json.Marshal(results)
    })

    // Deploy tool
    ts.RegisterTool("deploy", func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
        var req DeployRequest
        json.Unmarshal(input, &req)

        switch req.Target {
        case "k8s":
            return json.Marshal(deployToK8s(req))
        case "fly":
            return json.Marshal(deployToFly(req))
        case "vercel":
            return json.Marshal(deployToVercel(req))
        }
        return nil, fmt.Errorf("unknown target: %s", req.Target)
    })

    select {}  // Run forever
}
```

### 4. Pi Extension for Platform Integration

```typescript
// packages/coding-agent/src/extensions/platform.ts
// Extension that connects Pi to the platform

import { ExtensionFactory } from "../core/extensions/types.js";
import { connect, StringCodec } from "nats";

const extension: ExtensionFactory = async (pi) => {
    const nc = await connect({ servers: process.env.NATS_URL });
    const sc = StringCodec();

    // Register this agent with platform
    const agentID = process.env.AGENT_ID || crypto.randomUUID();
    await nc.publish("agents.register", sc.encode(JSON.stringify({
        id: agentID,
        type: "pi",
        node: process.env.HOSTNAME,
        status: "ready",
        skills: await detectSkills()
    })));

    // Heartbeat
    setInterval(() => {
        nc.publish(`agents.${agentID}.heartbeat`, sc.encode(JSON.stringify({
            status: pi.isIdle() ? "idle" : "busy",
            memory: process.memoryUsage(),
        })));
    }, 5000);

    // Forward events to platform
    pi.subscribe((event) => {
        nc.publish(`events.agent.${agentID}`, sc.encode(JSON.stringify(event)));
    });

    // Discover and register network tools
    const toolsKV = await nc.jetstream().views.kv("TOOLS");
    const tools = await toolsKV.keys();

    for await (const toolName of tools) {
        const entry = await toolsKV.get(toolName);
        const toolDef = JSON.parse(sc.decode(entry!.value));

        // Register network tool as local tool
        pi.registerTool({
            name: toolDef.name,
            description: toolDef.description + " (network tool)",
            parameters: toolDef.schema.input,
            async execute(id, params) {
                // Call tool via NATS request-reply
                const response = await nc.request(
                    toolDef.endpoint,
                    sc.encode(JSON.stringify(params)),
                    { timeout: 60000 }
                );
                const result = JSON.parse(sc.decode(response.data));
                return { content: [{ type: "text", text: JSON.stringify(result, null, 2) }] };
            }
        });
    }

    // Delegate tool for sending work to other agents
    pi.registerTool({
        name: "delegate",
        description: "Delegate a task to another agent in the cluster",
        parameters: Type.Object({
            task: Type.String({ description: "Task description" }),
            specialist: Type.Optional(Type.String({ description: "Agent type" })),
            wait: Type.Optional(Type.Boolean({ description: "Wait for result" }))
        }),
        async execute(id, params) {
            const taskID = crypto.randomUUID();

            await nc.publish("tasks.delegate", sc.encode(JSON.stringify({
                id: taskID,
                description: params.task,
                specialist: params.specialist,
                from_agent: agentID,
            })));

            if (params.wait) {
                // Wait for result
                const sub = nc.subscribe(`results.${taskID}`);
                for await (const msg of sub) {
                    const result = JSON.parse(sc.decode(msg.data));
                    return { content: [{ type: "text", text: result.summary }] };
                }
            }

            return { content: [{ type: "text", text: `Task ${taskID} delegated` }] };
        }
    });
};

export default extension;
```

---

## API Reference

### Agents

```yaml
# Deploy agent
POST /api/v1/agents
Request:
  name: string           # Unique name
  type: "pi" | "custom"  # Agent runtime
  config:
    model?: string       # LLM model
    extensions?: string[] # Pi extensions to load
    memory?: string      # Memory limit
    cpu?: string         # CPU limit
Response:
  id: string
  status: "deploying" | "ready" | "error"
  endpoint: string       # Direct endpoint if needed

# List agents
GET /api/v1/agents
Response:
  agents:
    - id: string
      name: string
      type: string
      status: "idle" | "busy" | "offline"
      skills: string[]
      current_task?: string

# Stop agent
DELETE /api/v1/agents/{id}
```

### Tasks

```yaml
# Submit task
POST /api/v1/tasks
Request:
  description: string    # What to do
  repo?: string          # Git repo
  branch?: string        # Branch to work on
  context?: object       # Additional context
  agent?: string         # Specific agent (optional)
  specialist?: string    # Agent type (optional)
  priority?: "low" | "normal" | "high"
  webhook?: string       # Callback URL
Response:
  id: string
  status: "pending"
  estimated_wait?: string

# Get task
GET /api/v1/tasks/{id}
Response:
  id: string
  status: "pending" | "running" | "completed" | "failed"
  agent?: string
  started_at?: timestamp
  completed_at?: timestamp
  result?:
    summary: string
    files_changed: string[]
    artifacts: object[]
  error?: string

# Stream task (WebSocket)
WS /api/v1/tasks/{id}/stream
Messages:
  { type: "status", status: "running", agent: "coder-1" }
  { type: "output", text: "Reading file src/auth.ts..." }
  { type: "tool_call", tool: "edit", args: {...} }
  { type: "complete", result: {...} }
```

### Tools

```yaml
# Register tool
POST /api/v1/tools
Request:
  name: string
  description: string
  endpoint: string       # NATS subject
  schema:
    input: JSONSchema
    output: JSONSchema
Response:
  id: string
  status: "registered"

# List tools
GET /api/v1/tools
Response:
  tools:
    - name: string
      description: string
      schema: object

# Invoke tool (for testing)
POST /api/v1/tools/{name}/invoke
Request:
  input: object
Response:
  output: object
```

### Webhooks

```yaml
# Register webhook
POST /api/v1/webhooks
Request:
  url: string
  events: ["task.completed", "task.failed", "agent.error"]
  secret?: string        # For signature verification
Response:
  id: string

# Webhook payload
POST {your-webhook-url}
Headers:
  X-Signature: sha256=...
Body:
  event: string
  timestamp: string
  data: object
```

---

## Deployment Options

### 1. Single Node (Development)

```yaml
# docker-compose.yml
version: '3.8'
services:
  nats:
    image: nats:latest
    command: -js
    ports:
      - "4222:4222"

  gateway:
    image: pi-platform/gateway
    environment:
      - NATS_URL=nats://nats:4222
    ports:
      - "8080:8080"

  runner:
    image: pi-platform/runner
    environment:
      - NATS_URL=nats://nats:4222
      - ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock  # For container tools

  tools:
    image: pi-platform/tools
    environment:
      - NATS_URL=nats://nats:4222
```

### 2. Kubernetes (Production)

```yaml
# Helm values
gateway:
  replicas: 3
  resources:
    memory: 512Mi
    cpu: 500m

runners:
  replicas: 10
  resources:
    memory: 4Gi
    cpu: 2

nats:
  cluster:
    enabled: true
    replicas: 3
  jetstream:
    enabled: true
    storage: 100Gi
```

### 3. Edge/Hybrid

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│   Your Laptop   │     │   AWS Region    │     │   GCP Region    │
│                 │     │                 │     │                 │
│ ┌─────────────┐ │     │ ┌─────────────┐ │     │ ┌─────────────┐ │
│ │ Pi Runner   │ │     │ │ Pi Runner   │ │     │ │ Pi Runner   │ │
│ │ (local dev) │ │     │ │ (scale)     │ │     │ │ (scale)     │ │
│ └──────┬──────┘ │     │ └──────┬──────┘ │     │ └──────┬──────┘ │
│        │        │     │        │        │     │        │        │
└────────┼────────┘     └────────┼────────┘     └────────┼────────┘
         │                       │                       │
         └───────────────────────┼───────────────────────┘
                                 │
                    ┌────────────▼────────────┐
                    │    NATS Super-Cluster   │
                    │    (geo-distributed)    │
                    └─────────────────────────┘
```

---

## SDK for Platform Users

```typescript
// @pi-platform/sdk
import { PiPlatform } from "@pi-platform/sdk";

const platform = new PiPlatform({
    apiUrl: "https://api.pi-platform.io",
    apiKey: process.env.PI_API_KEY
});

// Deploy an agent
const agent = await platform.agents.deploy({
    name: "my-coder",
    type: "pi",
    config: { model: "claude-sonnet-4-20250514" }
});

// Submit a task and wait
const result = await platform.tasks.submit({
    description: "Add user authentication with OAuth2",
    repo: "github.com/myorg/myapp",
    agent: agent.id
}).wait();

console.log(result.summary);
console.log(result.filesChanged);

// Stream task output
const task = await platform.tasks.submit({ ... });
for await (const event of task.stream()) {
    console.log(event);
}

// Register a custom tool
await platform.tools.register({
    name: "my-custom-tool",
    endpoint: "nats://my-tools.custom.invoke",
    schema: { ... }
});
```

---

## Pricing Model (If Productized)

```yaml
# Usage-based pricing
compute:
  - $0.01 per agent-minute (Pi agents)
  - $0.005 per agent-minute (Go agents)

tasks:
  - $0.10 per task (includes routing, storage)
  - $0.01 per MB of artifacts stored

tools:
  - Free for built-in tools
  - $0.001 per invocation for custom tools

# Plans
free:
  - 100 agent-minutes/month
  - 10 concurrent agents
  - Community support

team:
  - $99/month
  - 10,000 agent-minutes/month
  - 50 concurrent agents
  - Priority support

enterprise:
  - Custom pricing
  - Unlimited
  - Self-hosted option
  - SLA
```

---

## What This Enables

| Use Case | How It Works |
|----------|--------------|
| **CI/CD Agent** | GitHub webhook → Task → Agent fixes/reviews → PR comment |
| **Support Bot** | Slack message → Task → Agent investigates → Response |
| **Code Review** | PR opened → Agent reviews → Comments + suggestions |
| **Migration Service** | API call → Agent migrates → Report + PR |
| **Documentation** | Code pushed → Agent updates docs → Commits |
| **Security Audit** | Scheduled → Agent scans → Report + tickets |

---

## Summary

**The Platform =**
- Go API Gateway (stateless, scalable)
- NATS JetStream (durable messaging, KV store)
- Pi Agents (with platform extension)
- Go Tools (network-accessible services)
- SDK for developers

**API-First means:**
- Everything via REST/gRPC/WebSocket
- No CLI required (but CLI wraps API)
- Webhooks for async workflows
- SDKs for every language
- Self-service provisioning

This turns Pi from "a coding agent you run locally" into "a platform for deploying and orchestrating AI agents at scale."

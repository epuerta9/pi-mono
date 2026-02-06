// Package main provides an MCP server for Claude Code / Pi integration
// This allows local agents to delegate tasks to the swarm cluster
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// MCP Server that exposes swarm tools to Claude Code
// Claude Code config (~/.claude/settings.json):
// {
//   "mcpServers": {
//     "swarm": {
//       "command": "swarm-mcp",
//       "args": ["--nats", "nats://cluster.example.com:4222"]
//     }
//   }
// }

type MCPServer struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// Tool definitions for MCP
var tools = []map[string]any{
	{
		"name":        "swarm_delegate",
		"description": "Delegate a task to the distributed agent cluster. Use this for parallelizable work, background tasks, or specialist work (testing, security audit, documentation).",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "Description of what needs to be done",
				},
				"specialist": map[string]any{
					"type":        "string",
					"enum":        []string{"", "test-runner", "reviewer", "security", "docs", "refactor"},
					"description": "Type of specialist agent to use (empty for general)",
				},
				"priority": map[string]any{
					"type":        "string",
					"enum":        []string{"low", "normal", "high"},
					"default":     "normal",
					"description": "Task priority",
				},
				"wait": map[string]any{
					"type":        "boolean",
					"default":     false,
					"description": "Wait for the task to complete before returning",
				},
				"context": map[string]any{
					"type":        "object",
					"description": "Additional context to pass to the agent",
				},
			},
			"required": []string{"task"},
		},
	},
	{
		"name":        "swarm_status",
		"description": "Check the status of the agent cluster and running tasks",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "Specific task ID to check (optional)",
				},
			},
		},
	},
	{
		"name":        "swarm_results",
		"description": "Get results from completed tasks",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "Specific task ID (optional, returns recent if not specified)",
				},
				"limit": map[string]any{
					"type":        "number",
					"default":     5,
					"description": "Maximum number of results to return",
				},
			},
		},
	},
	{
		"name":        "swarm_memory",
		"description": "Access shared memory across the agent cluster. Store or retrieve context that should be available to all agents.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"get", "set", "list"},
					"description": "Action to perform",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Memory key",
				},
				"value": map[string]any{
					"type":        "string",
					"description": "Value to store (for set action)",
				},
			},
			"required": []string{"action"},
		},
	},
}

func NewMCPServer(natsURL string) (*MCPServer, error) {
	nc, err := nats.Connect(natsURL)
	if err != nil {
		return nil, err
	}

	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}

	return &MCPServer{nc: nc, js: js}, nil
}

// MCP Protocol handlers
func (s *MCPServer) HandleListTools() map[string]any {
	return map[string]any{
		"tools": tools,
	}
}

func (s *MCPServer) HandleCallTool(name string, args map[string]any) map[string]any {
	switch name {
	case "swarm_delegate":
		return s.handleDelegate(args)
	case "swarm_status":
		return s.handleStatus(args)
	case "swarm_results":
		return s.handleResults(args)
	case "swarm_memory":
		return s.handleMemory(args)
	default:
		return map[string]any{
			"error": fmt.Sprintf("unknown tool: %s", name),
		}
	}
}

func (s *MCPServer) handleDelegate(args map[string]any) map[string]any {
	task := args["task"].(string)
	specialist := ""
	if s, ok := args["specialist"].(string); ok {
		specialist = s
	}
	priority := "normal"
	if p, ok := args["priority"].(string); ok {
		priority = p
	}
	wait := false
	if w, ok := args["wait"].(bool); ok {
		wait = w
	}

	taskID := fmt.Sprintf("%d", time.Now().UnixNano())[:8]

	taskData := map[string]any{
		"id":           taskID,
		"description":  task,
		"specialist":   specialist,
		"priority":     priority,
		"repo":         os.Getenv("SWARM_REPO"),
		"branch":       os.Getenv("SWARM_BRANCH"),
		"requested_by": os.Getenv("HOSTNAME"),
		"created_at":   time.Now(),
		"status":       "pending",
	}

	if ctx, ok := args["context"].(map[string]any); ok {
		taskData["context"] = ctx
	}

	data, _ := json.Marshal(taskData)

	subject := fmt.Sprintf("tasks.%s.%s", priority, specialist)
	if specialist == "" {
		subject = fmt.Sprintf("tasks.%s.general", priority)
	}

	ctx := context.Background()
	s.js.Publish(ctx, subject, data)

	if wait {
		// Wait for result
		sub, _ := s.nc.SubscribeSync(fmt.Sprintf("results.%s", taskID))
		msg, err := sub.NextMsg(10 * time.Minute)
		if err != nil {
			return map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": fmt.Sprintf("Task %s submitted but timed out waiting for result", taskID)},
				},
			}
		}

		var result map[string]any
		json.Unmarshal(msg.Data, &result)
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": formatResult(result)},
			},
		}
	}

	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": fmt.Sprintf("Task %s delegated to cluster.\n\nDescription: %s\nSpecialist: %s\nPriority: %s\n\nUse swarm_status to check progress or swarm_results to get the outcome.", taskID, task, specialist, priority)},
		},
	}
}

func (s *MCPServer) handleStatus(args map[string]any) map[string]any {
	ctx := context.Background()

	// Get stream stats
	tasksStream, _ := s.js.Stream(ctx, "TASKS")
	resultsStream, _ := s.js.Stream(ctx, "RESULTS")

	tasksInfo, _ := tasksStream.Info(ctx)
	resultsInfo, _ := resultsStream.Info(ctx)

	status := fmt.Sprintf(`Cluster Status:
- Pending tasks: %d
- Completed tasks: %d
`, tasksInfo.State.Msgs, resultsInfo.State.Msgs)

	if taskID, ok := args["task_id"].(string); ok && taskID != "" {
		// Get specific task status
		// Would query the task from stream
		status += fmt.Sprintf("\nTask %s: [would show status here]", taskID)
	}

	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": status},
		},
	}
}

func (s *MCPServer) handleResults(args map[string]any) map[string]any {
	ctx := context.Background()

	limit := 5
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	results := []string{}

	// Get recent results from stream
	stream, _ := s.js.Stream(ctx, "RESULTS")
	consumer, _ := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		DeliverPolicy: jetstream.DeliverLastPerSubjectPolicy,
	})

	for i := 0; i < limit; i++ {
		msg, err := consumer.Next()
		if err != nil {
			break
		}
		var task map[string]any
		json.Unmarshal(msg.Data(), &task)
		results = append(results, formatResult(task))
	}

	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": fmt.Sprintf("Recent Results:\n\n%s", results)},
		},
	}
}

func (s *MCPServer) handleMemory(args map[string]any) map[string]any {
	action := args["action"].(string)
	ctx := context.Background()

	switch action {
	case "get":
		key := args["key"].(string)
		// Would fetch from memory stream
		kv, _ := s.js.KeyValue(ctx, "MEMORY")
		entry, err := kv.Get(ctx, key)
		if err != nil {
			return map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": fmt.Sprintf("Key '%s' not found", key)},
				},
			}
		}
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": string(entry.Value())},
			},
		}

	case "set":
		key := args["key"].(string)
		value := args["value"].(string)
		kv, _ := s.js.KeyValue(ctx, "MEMORY")
		kv.Put(ctx, key, []byte(value))
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": fmt.Sprintf("Stored '%s' in shared memory", key)},
			},
		}

	case "list":
		kv, _ := s.js.KeyValue(ctx, "MEMORY")
		keys, _ := kv.Keys(ctx)
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": fmt.Sprintf("Memory keys: %v", keys)},
			},
		}
	}

	return nil
}

func formatResult(task map[string]any) string {
	result := task["result"].(map[string]any)
	status := "✓"
	if !result["success"].(bool) {
		status = "✗"
	}

	return fmt.Sprintf(`
Task: %s %s
Description: %s
Duration: %s
Cost: $%.4f

Summary:
%s
`, status, task["id"], task["description"], result["duration"], result["cost_usd"], result["summary"])
}

// Main entry point would handle MCP protocol over stdio
func main() {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	server, err := NewMCPServer(natsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect: %v\n", err)
		os.Exit(1)
	}

	// Would implement MCP protocol over stdio here
	// Reading JSON-RPC messages, dispatching to handlers
	_ = server
	fmt.Println("MCP server would run here...")
}

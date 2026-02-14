package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"

	"github.com/nats-io/nats.go"

	swarmctx "github.com/mariozechner/pi-mono/packages/pi-swarm/internal/context"
	"github.com/mariozechner/pi-mono/packages/pi-swarm/internal/memory"
	"github.com/mariozechner/pi-mono/packages/pi-swarm/internal/presence"
	"github.com/mariozechner/pi-mono/packages/pi-swarm/internal/tasks"
)

// PiBridge connects a TypeScript Pi agent process (running in RPC mode)
// to the Go swarm layer. It translates swarm events into Pi RPC commands
// and Pi RPC events into swarm state mutations.
type PiBridge struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Scanner
	tasks     *tasks.Store
	memory    *memory.Store
	context   *swarmctx.Store
	presence  *presence.Tracker
	nc        *nats.Conn
	agentID   string
	sessionID string
	piCmd     string
	piArgs    []string
	log       *slog.Logger
}

// RpcCommand is the JSON structure sent to Pi's stdin in RPC mode.
type RpcCommand struct {
	ID      string         `json:"id,omitempty"`
	Command string         `json:"command"`
	Data    map[string]any `json:"data,omitempty"`
}

// RpcEvent is the JSON structure read from Pi's stdout in RPC mode.
type RpcEvent struct {
	ID   string         `json:"id,omitempty"`
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// NewPiBridge creates a bridge that will spawn and connect to a Pi agent.
func NewPiBridge(
	agentID string,
	piCmd string,
	piArgs []string,
	taskStore *tasks.Store,
	memStore *memory.Store,
	ctxStore *swarmctx.Store,
	presTracker *presence.Tracker,
	nc *nats.Conn,
) *PiBridge {
	return &PiBridge{
		agentID:  agentID,
		piCmd:    piCmd,
		piArgs:   piArgs,
		tasks:    taskStore,
		memory:   memStore,
		context:  ctxStore,
		presence: presTracker,
		nc:       nc,
		log:      slog.Default().With("component", "bridge", "agent", agentID),
	}
}

// Start launches the Pi agent in RPC mode, injects shared memory
// as initial context, and begins the bidirectional event loop.
func (b *PiBridge) Start(ctx context.Context) error {
	args := append([]string{"--mode", "rpc"}, b.piArgs...)
	b.cmd = exec.CommandContext(ctx, b.piCmd, args...)

	var err error
	b.stdin, err = b.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := b.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	b.stdout = bufio.NewScanner(stdout)
	b.stdout.Buffer(make([]byte, 0, 1<<20), 1<<20) // 1MB line buffer

	if err := b.cmd.Start(); err != nil {
		return fmt.Errorf("start pi process: %w", err)
	}

	b.log.Info("pi agent started", "pid", b.cmd.Process.Pid)

	// Inject shared memory as initial context
	if err := b.injectSharedMemory(ctx); err != nil {
		b.log.Warn("failed to inject shared memory", "error", err)
	}

	// Subscribe to RPC claim requests from the scheduler
	b.subscribeClaimRequests(ctx)

	// Start bidirectional event loops
	go b.readLoop(ctx)
	go b.watchTaskAssignments(ctx)
	go b.watchMemoryUpdates(ctx)

	return nil
}

// SendPrompt sends a user prompt to the Pi agent.
func (b *PiBridge) SendPrompt(message string) error {
	return b.sendCommand(RpcCommand{
		Command: "prompt",
		Data:    map[string]any{"message": message},
	})
}

// Stop gracefully shuts down the Pi agent process.
func (b *PiBridge) Stop() error {
	b.stdin.Close()
	return b.cmd.Wait()
}

// injectSharedMemory loads all relevant memory and sends it as
// context to the Pi agent at startup.
func (b *PiBridge) injectSharedMemory(ctx context.Context) error {
	memories, err := b.memory.GetForAgent(ctx, b.agentID)
	if err != nil {
		return err
	}

	if len(memories) == 0 {
		return nil
	}

	// Build a combined memory context string
	var combined string
	for _, mem := range memories {
		combined += fmt.Sprintf("## [Shared Memory: %s]\n%s\n\n", mem.Key, mem.Content)
	}

	return b.sendCommand(RpcCommand{
		Command: "steer",
		Data: map[string]any{
			"message": combined,
		},
	})
}

// subscribeClaimRequests listens for task claim requests from the
// scheduler via NATS request/reply.
func (b *PiBridge) subscribeClaimRequests(ctx context.Context) {
	subject := fmt.Sprintf("swarm.rpc.%s.claim", b.agentID)
	_, err := b.nc.Subscribe(subject, func(msg *nats.Msg) {
		var req tasks.ClaimRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			return
		}

		// Try to claim the task
		task, err := b.tasks.Claim(ctx, req.TaskID)
		resp := tasks.ClaimResponse{}
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Claimed = true
			b.presence.SetCurrentTask(task.ID)

			// Send the task to the Pi agent as a follow-up prompt
			go func() {
				_ = b.sendCommand(RpcCommand{
					Command: "follow_up",
					Data: map[string]any{
						"message": fmt.Sprintf(
							"[Swarm Task %s] %s\n\n%s",
							task.ID, task.Title, task.Description,
						),
					},
				})
			}()
		}

		data, _ := json.Marshal(resp)
		_ = msg.Respond(data)
	})
	if err != nil {
		b.log.Error("failed to subscribe to claim requests", "error", err)
	}
}

// readLoop reads Pi RPC events from stdout and maps them to swarm actions.
func (b *PiBridge) readLoop(ctx context.Context) {
	for b.stdout.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		var event RpcEvent
		if err := json.Unmarshal(b.stdout.Bytes(), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_end":
			// Share assistant response as context in the swarm
			content, _ := event.Data["content"].(string)
			if content != "" {
				_, _ = b.context.Append(ctx, b.sessionID,
					swarmctx.ContextMessage, "assistant", content, nil)
			}

		case "tool_result_end":
			toolName, _ := event.Data["tool_name"].(string)
			resultContent, _ := event.Data["content"].(string)

			// Share tool results as context
			_, _ = b.context.Append(ctx, b.sessionID,
				swarmctx.ContextToolResult, "assistant", resultContent,
				map[string]any{"tool_name": toolName})

			// Special handling: if the tool was "todo", sync to shared tasks
			if toolName == "todo" {
				b.syncTodoToSwarm(ctx, event.Data)
			}

		case "agent_end":
			// Agent finished — mark current task as idle
			b.presence.SetCurrentTask("")
		}
	}
}

// syncTodoToSwarm converts a Pi todo tool result into a shared swarm task.
func (b *PiBridge) syncTodoToSwarm(ctx context.Context, data map[string]any) {
	details, ok := data["details"].(map[string]any)
	if !ok {
		return
	}
	action, _ := details["action"].(string)
	if action != "add" {
		return
	}

	// Extract the todo text from the details
	todosRaw, _ := details["todos"].([]any)
	if len(todosRaw) == 0 {
		return
	}

	// Create a shared task for the last added todo
	lastTodo, ok := todosRaw[len(todosRaw)-1].(map[string]any)
	if !ok {
		return
	}
	text, _ := lastTodo["text"].(string)
	if text != "" {
		_, _ = b.tasks.Create(ctx, text, "Synced from Pi todo tool")
	}
}

// watchTaskAssignments watches for tasks assigned to this agent.
func (b *PiBridge) watchTaskAssignments(ctx context.Context) {
	events, err := b.tasks.Watch(ctx)
	if err != nil {
		b.log.Error("failed to watch tasks", "error", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			// React to tasks claimed by us but not yet sent to Pi
			if event.Task.ClaimedBy == b.agentID && event.Task.Status == tasks.TaskActive {
				b.log.Info("task activated", "task", event.Task.ID)
			}
		}
	}
}

// watchMemoryUpdates injects memory changes into the Pi agent in real-time.
func (b *PiBridge) watchMemoryUpdates(ctx context.Context) {
	events, err := b.memory.Watch(ctx, "memory.global.>")
	if err != nil {
		b.log.Error("failed to watch memory", "error", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.Entry.Author == b.agentID {
				continue // skip our own updates
			}

			_ = b.sendCommand(RpcCommand{
				Command: "steer",
				Data: map[string]any{
					"message": fmt.Sprintf(
						"[Shared Memory Updated: %s] (by %s)\n%s",
						event.Entry.Key, event.Entry.Author, event.Entry.Content,
					),
				},
			})
		}
	}
}

// sendCommand writes an RPC command to the Pi agent's stdin.
func (b *PiBridge) sendCommand(cmd RpcCommand) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = b.stdin.Write(data)
	return err
}

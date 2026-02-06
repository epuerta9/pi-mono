// Package swarm provides a distributed agent coordinator built on NATS
package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Task represents work to be done by an agent
type Task struct {
	ID          string            `json:"id"`
	Description string            `json:"description"`
	Specialist  string            `json:"specialist,omitempty"` // "", "test-runner", "reviewer", "security", "docs"
	Priority    string            `json:"priority"`             // "low", "normal", "high"
	Repo        string            `json:"repo"`
	Branch      string            `json:"branch"`
	Worktree    string            `json:"worktree,omitempty"`
	RequestedBy string            `json:"requested_by"`
	ParentTask  string            `json:"parent_task,omitempty"`
	Context     map[string]string `json:"context,omitempty"` // Shared context from previous tasks
	CreatedAt   time.Time         `json:"created_at"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	Status      string            `json:"status"` // "pending", "running", "completed", "failed"
	Result      *TaskResult       `json:"result,omitempty"`
}

type TaskResult struct {
	Success  bool     `json:"success"`
	Summary  string   `json:"summary"`
	Branch   string   `json:"branch"`
	Commits  []string `json:"commits"`
	Files    []string `json:"files_changed"`
	Errors   []string `json:"errors,omitempty"`
	Duration string   `json:"duration"`
	Cost     float64  `json:"cost_usd"`
}

// Agent represents a running agent instance
type Agent struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"` // "claude-code", "pi", "go-fast"
	Specialist string    `json:"specialist"`
	Worktree   string    `json:"worktree"`
	CurrentTask string   `json:"current_task,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	LastSeen   time.Time `json:"last_seen"`
	Process    *os.Process `json:"-"`
}

// Coordinator manages the distributed agent system
type Coordinator struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	ctx    context.Context
	cancel context.CancelFunc

	repoPath    string
	worktreeDir string

	agents   map[string]*Agent
	agentsMu sync.RWMutex

	tasks   map[string]*Task
	tasksMu sync.RWMutex

	maxAgents int
	logger    *slog.Logger
}

// NewCoordinator creates a new distributed agent coordinator
func NewCoordinator(natsURL, repoPath string) (*Coordinator, error) {
	nc, err := nats.Connect(natsURL)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("jetstream: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	c := &Coordinator{
		nc:          nc,
		js:          js,
		ctx:         ctx,
		cancel:      cancel,
		repoPath:    repoPath,
		worktreeDir: filepath.Join(repoPath, ".swarm-worktrees"),
		agents:      make(map[string]*Agent),
		tasks:       make(map[string]*Task),
		maxAgents:   10, // Configurable
		logger:      slog.Default(),
	}

	if err := c.setupStreams(); err != nil {
		return nil, fmt.Errorf("setup streams: %w", err)
	}

	return c, nil
}

func (c *Coordinator) setupStreams() error {
	ctx := c.ctx

	// Tasks stream - work items
	_, err := c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        "TASKS",
		Description: "Agent task queue",
		Subjects:    []string{"tasks.>"},
		Retention:   jetstream.WorkQueuePolicy,
		MaxAge:      24 * time.Hour,
	})
	if err != nil {
		return err
	}

	// Results stream - completed work
	_, err = c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        "RESULTS",
		Description: "Task results",
		Subjects:    []string{"results.>"},
		Retention:   jetstream.LimitsPolicy,
		MaxAge:      7 * 24 * time.Hour,
	})
	if err != nil {
		return err
	}

	// Memory stream - shared context
	_, err = c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        "MEMORY",
		Description: "Shared agent memory",
		Subjects:    []string{"memory.>"},
		Retention:   jetstream.LimitsPolicy,
		MaxAge:      30 * 24 * time.Hour,
	})
	if err != nil {
		return err
	}

	return nil
}

// SubmitTask adds a new task to the queue
func (c *Coordinator) SubmitTask(description string, opts ...TaskOption) (*Task, error) {
	task := &Task{
		ID:          uuid.New().String()[:8],
		Description: description,
		Priority:    "normal",
		CreatedAt:   time.Now(),
		Status:      "pending",
	}

	for _, opt := range opts {
		opt(task)
	}

	data, _ := json.Marshal(task)

	// Determine subject based on priority and specialist
	subject := fmt.Sprintf("tasks.%s.%s", task.Priority, task.Specialist)
	if task.Specialist == "" {
		subject = fmt.Sprintf("tasks.%s.general", task.Priority)
	}

	_, err := c.js.Publish(c.ctx, subject, data)
	if err != nil {
		return nil, err
	}

	c.tasksMu.Lock()
	c.tasks[task.ID] = task
	c.tasksMu.Unlock()

	c.logger.Info("task submitted", "id", task.ID, "description", description)
	return task, nil
}

type TaskOption func(*Task)

func WithSpecialist(s string) TaskOption { return func(t *Task) { t.Specialist = s } }
func WithPriority(p string) TaskOption   { return func(t *Task) { t.Priority = p } }
func WithRepo(r string) TaskOption       { return func(t *Task) { t.Repo = r } }
func WithBranch(b string) TaskOption     { return func(t *Task) { t.Branch = b } }
func WithContext(c map[string]string) TaskOption { return func(t *Task) { t.Context = c } }

// Run starts the coordinator's main loop
func (c *Coordinator) Run() error {
	c.logger.Info("coordinator starting", "repo", c.repoPath, "max_agents", c.maxAgents)

	// Start task consumer
	go c.consumeTasks()

	// Start health monitor
	go c.monitorHealth()

	// Start result handler
	go c.handleResults()

	<-c.ctx.Done()
	return nil
}

func (c *Coordinator) consumeTasks() {
	consumer, err := c.js.CreateOrUpdateConsumer(c.ctx, "TASKS", jetstream.ConsumerConfig{
		Name:          "coordinator",
		Durable:       "coordinator",
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: "tasks.>",
	})
	if err != nil {
		c.logger.Error("create consumer failed", "error", err)
		return
	}

	msgs, _ := consumer.Messages()
	for {
		select {
		case <-c.ctx.Done():
			return
		case msg := <-msgs:
			var task Task
			if err := json.Unmarshal(msg.Data(), &task); err != nil {
				msg.Nak()
				continue
			}

			// Check if we can spawn an agent
			c.agentsMu.RLock()
			activeCount := len(c.agents)
			c.agentsMu.RUnlock()

			if activeCount >= c.maxAgents {
				// Re-queue with delay
				msg.NakWithDelay(10 * time.Second)
				continue
			}

			// Spawn agent for this task
			go c.spawnAgent(&task)
			msg.Ack()
		}
	}
}

func (c *Coordinator) spawnAgent(task *Task) {
	agent := &Agent{
		ID:          uuid.New().String()[:8],
		Type:        c.selectAgentType(task),
		Specialist:  task.Specialist,
		CurrentTask: task.ID,
		StartedAt:   time.Now(),
		LastSeen:    time.Now(),
	}

	// Create git worktree for isolation
	worktreePath := filepath.Join(c.worktreeDir, agent.ID)
	branchName := fmt.Sprintf("swarm/%s/%s", task.ID, agent.ID)

	cmd := exec.Command("git", "worktree", "add", "-b", branchName, worktreePath)
	cmd.Dir = c.repoPath
	if err := cmd.Run(); err != nil {
		c.logger.Error("worktree creation failed", "error", err)
		return
	}
	agent.Worktree = worktreePath
	task.Worktree = worktreePath

	c.agentsMu.Lock()
	c.agents[agent.ID] = agent
	c.agentsMu.Unlock()

	c.logger.Info("agent spawned", "agent", agent.ID, "type", agent.Type, "task", task.ID)

	// Update task status
	now := time.Now()
	task.StartedAt = &now
	task.Status = "running"

	// Run the agent
	var proc *exec.Cmd
	switch agent.Type {
	case "claude-code":
		proc = c.runClaudeCode(agent, task)
	case "pi":
		proc = c.runPi(agent, task)
	case "go-fast":
		proc = c.runGoFast(agent, task)
	}

	if proc != nil {
		agent.Process = proc.Process
		proc.Wait()
	}

	// Cleanup
	c.cleanup(agent, task)
}

func (c *Coordinator) selectAgentType(task *Task) string {
	// Fast tasks use Go agent
	switch task.Specialist {
	case "test-runner":
		return "go-fast" // Just runs commands
	case "security":
		return "claude-code" // Needs deep analysis
	case "reviewer":
		return "claude-code" // Needs reasoning
	case "docs":
		return "pi" // Good for focused writing
	default:
		return "claude-code" // General purpose
	}
}

func (c *Coordinator) runClaudeCode(agent *Agent, task *Task) *exec.Cmd {
	prompt := c.buildPrompt(task)

	cmd := exec.Command("claude",
		"--print",       // Non-interactive
		"--dangerously-skip-permissions",
		"--output-format", "json",
		prompt,
	)
	cmd.Dir = agent.Worktree
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("SWARM_TASK_ID=%s", task.ID),
		fmt.Sprintf("SWARM_AGENT_ID=%s", agent.ID),
		fmt.Sprintf("NATS_URL=%s", c.nc.ConnectedUrl()),
	)

	output, err := cmd.Output()
	if err != nil {
		c.publishResult(task, &TaskResult{
			Success: false,
			Errors:  []string{err.Error()},
		})
		return cmd
	}

	// Parse result and publish
	result := c.parseAgentOutput(output, agent.Worktree)
	c.publishResult(task, result)

	return cmd
}

func (c *Coordinator) runPi(agent *Agent, task *Task) *exec.Cmd {
	prompt := c.buildPrompt(task)

	cmd := exec.Command("pi",
		"--mode", "print",
		"--json",
		prompt,
	)
	cmd.Dir = agent.Worktree

	output, _ := cmd.Output()
	result := c.parseAgentOutput(output, agent.Worktree)
	c.publishResult(task, result)

	return cmd
}

func (c *Coordinator) runGoFast(agent *Agent, task *Task) *exec.Cmd {
	// For simple tasks, use the built-in Go agent (faster, cheaper)
	// This would be a simple LLM call + tool execution loop
	// Implementation would be similar to what we discussed earlier

	c.logger.Info("go-fast agent not yet implemented, falling back to claude-code")
	return c.runClaudeCode(agent, task)
}

func (c *Coordinator) buildPrompt(task *Task) string {
	// Build a focused prompt with context
	prompt := fmt.Sprintf(`You are a specialist agent working on a specific task.

TASK: %s

CONSTRAINTS:
- Work only in this worktree (already checked out)
- Commit your changes with clear messages
- Do not push (coordinator handles merging)
- Signal completion by creating a file: .swarm-done

CONTEXT FROM PREVIOUS AGENTS:
%v

When done, summarize what you did in .swarm-done
`, task.Description, task.Context)

	// Add specialist instructions
	switch task.Specialist {
	case "test-runner":
		prompt += "\nFOCUS: Run tests, fix failures, ensure all pass before marking done."
	case "reviewer":
		prompt += "\nFOCUS: Review code changes, add comments, suggest improvements."
	case "security":
		prompt += "\nFOCUS: Audit for security vulnerabilities, check OWASP top 10."
	case "docs":
		prompt += "\nFOCUS: Update documentation to reflect code changes."
	}

	return prompt
}

func (c *Coordinator) parseAgentOutput(output []byte, worktree string) *TaskResult {
	result := &TaskResult{
		Success: true,
	}

	// Check for .swarm-done file
	donePath := filepath.Join(worktree, ".swarm-done")
	if summary, err := os.ReadFile(donePath); err == nil {
		result.Summary = string(summary)
	}

	// Get commits made
	cmd := exec.Command("git", "log", "--oneline", "HEAD...HEAD~10", "--")
	cmd.Dir = worktree
	if out, err := cmd.Output(); err == nil {
		result.Commits = parseCommits(string(out))
	}

	// Get files changed
	cmd = exec.Command("git", "diff", "--name-only", "HEAD~1")
	cmd.Dir = worktree
	if out, err := cmd.Output(); err == nil {
		result.Files = parseFiles(string(out))
	}

	return result
}

func (c *Coordinator) publishResult(task *Task, result *TaskResult) {
	now := time.Now()
	task.CompletedAt = &now
	task.Result = result
	task.Status = "completed"
	if !result.Success {
		task.Status = "failed"
	}

	if task.StartedAt != nil {
		result.Duration = now.Sub(*task.StartedAt).String()
	}

	data, _ := json.Marshal(task)
	subject := fmt.Sprintf("results.%s", task.ID)
	c.js.Publish(c.ctx, subject, data)

	c.logger.Info("task completed",
		"id", task.ID,
		"success", result.Success,
		"duration", result.Duration,
	)
}

func (c *Coordinator) cleanup(agent *Agent, task *Task) {
	// Remove worktree
	exec.Command("git", "worktree", "remove", agent.Worktree).Run()

	// Remove agent from active list
	c.agentsMu.Lock()
	delete(c.agents, agent.ID)
	c.agentsMu.Unlock()
}

func (c *Coordinator) monitorHealth() {
	ticker := time.NewTicker(30 * time.Second)
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.agentsMu.Lock()
			for id, agent := range c.agents {
				// Check if process is still alive
				if agent.Process != nil {
					// Process.Signal(0) checks if process exists
					if err := agent.Process.Signal(os.Signal(nil)); err != nil {
						c.logger.Warn("agent died", "id", id, "task", agent.CurrentTask)
						// Re-queue the task
						if task, ok := c.tasks[agent.CurrentTask]; ok {
							task.Status = "pending"
							c.SubmitTask(task.Description,
								WithSpecialist(task.Specialist),
								WithPriority("high"), // Retry with high priority
							)
						}
						delete(c.agents, id)
					}
				}
			}
			c.agentsMu.Unlock()
		}
	}
}

func (c *Coordinator) handleResults() {
	// Subscribe to results for logging/notification
	consumer, _ := c.js.CreateOrUpdateConsumer(c.ctx, "RESULTS", jetstream.ConsumerConfig{
		Name:          "result-handler",
		FilterSubject: "results.>",
	})

	msgs, _ := consumer.Messages()
	for msg := range msgs {
		var task Task
		json.Unmarshal(msg.Data(), &task)

		// Notify requestor
		c.nc.Publish(fmt.Sprintf("notify.%s", task.RequestedBy), msg.Data())

		// Store in memory for future context
		if task.Result != nil && task.Result.Success {
			memKey := fmt.Sprintf("memory.completed.%s", task.ID)
			c.js.Publish(c.ctx, memKey, msg.Data())
		}

		msg.Ack()
	}
}

func (c *Coordinator) Stop() {
	c.cancel()
	c.nc.Close()
}

// Helper functions
func parseCommits(output string) []string {
	// Parse git log output
	return nil // Implementation
}

func parseFiles(output string) []string {
	// Parse git diff output
	return nil // Implementation
}

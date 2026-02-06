// Package main provides the swarm CLI for interacting with the distributed agent cluster
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"
)

var natsURL string

func main() {
	rootCmd := &cobra.Command{
		Use:   "swarm",
		Short: "Distributed agent cluster CLI",
	}

	rootCmd.PersistentFlags().StringVar(&natsURL, "nats", "nats://localhost:4222", "NATS server URL")

	rootCmd.AddCommand(delegateCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(resultsCmd())
	rootCmd.AddCommand(watchCmd())
	rootCmd.AddCommand(serveCmd())

	rootCmd.Execute()
}

func delegateCmd() *cobra.Command {
	var specialist string
	var priority string
	var wait bool

	cmd := &cobra.Command{
		Use:   "delegate [task description]",
		Short: "Delegate a task to the cluster",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			nc, _ := nats.Connect(natsURL)
			js, _ := jetstream.New(nc)
			defer nc.Close()

			task := map[string]any{
				"id":          generateID(),
				"description": args[0],
				"specialist":  specialist,
				"priority":    priority,
				"repo":        getCurrentRepo(),
				"branch":      getCurrentBranch(),
				"requested_by": getHostname(),
				"created_at":  time.Now(),
				"status":      "pending",
			}

			data, _ := json.Marshal(task)
			subject := fmt.Sprintf("tasks.%s.%s", priority, specialist)
			if specialist == "" {
				subject = fmt.Sprintf("tasks.%s.general", priority)
			}

			js.Publish(cmd.Context(), subject, data)

			fmt.Printf("✓ Task %s delegated\n", task["id"])
			fmt.Printf("  Description: %s\n", args[0])
			fmt.Printf("  Specialist: %s\n", specialist)
			fmt.Printf("  Priority: %s\n", priority)

			if wait {
				fmt.Println("\nWaiting for result...")
				waitForResult(nc, task["id"].(string))
			}
		},
	}

	cmd.Flags().StringVarP(&specialist, "specialist", "s", "", "Agent type: test-runner, reviewer, security, docs")
	cmd.Flags().StringVarP(&priority, "priority", "p", "normal", "Priority: low, normal, high")
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "Wait for task completion")

	return cmd
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show cluster status",
		Run: func(cmd *cobra.Command, args []string) {
			nc, _ := nats.Connect(natsURL)
			js, _ := jetstream.New(nc)
			defer nc.Close()

			// Get stream info
			tasks, _ := js.Stream(cmd.Context(), "TASKS")
			results, _ := js.Stream(cmd.Context(), "RESULTS")

			tasksInfo, _ := tasks.Info(cmd.Context())
			resultsInfo, _ := results.Info(cmd.Context())

			fmt.Println("=== Cluster Status ===\n")
			fmt.Printf("Pending tasks:   %d\n", tasksInfo.State.Msgs)
			fmt.Printf("Completed tasks: %d\n", resultsInfo.State.Msgs)

			// List recent tasks
			fmt.Println("\n=== Recent Tasks ===\n")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tStatus\tSpecialist\tDescription")
			fmt.Fprintln(w, "--\t------\t----------\t-----------")

			// Would iterate through tasks stream here
			// Simplified for example

			w.Flush()
		},
	}
}

func resultsCmd() *cobra.Command {
	var taskID string

	cmd := &cobra.Command{
		Use:   "results",
		Short: "Show task results",
		Run: func(cmd *cobra.Command, args []string) {
			nc, _ := nats.Connect(natsURL)
			js, _ := jetstream.New(nc)
			defer nc.Close()

			// Get results stream
			stream, _ := js.Stream(cmd.Context(), "RESULTS")

			if taskID != "" {
				// Get specific result
				consumer, _ := stream.CreateOrUpdateConsumer(cmd.Context(), jetstream.ConsumerConfig{
					FilterSubject: fmt.Sprintf("results.%s", taskID),
				})
				msg, _ := consumer.Next()
				if msg != nil {
					var task map[string]any
					json.Unmarshal(msg.Data(), &task)
					printResult(task)
				}
			} else {
				// List all recent results
				fmt.Println("=== Recent Results ===\n")
				// Would iterate through results stream
			}
		},
	}

	cmd.Flags().StringVarP(&taskID, "task", "t", "", "Specific task ID")
	return cmd
}

func watchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Watch cluster activity in real-time",
		Run: func(cmd *cobra.Command, args []string) {
			nc, _ := nats.Connect(natsURL)
			defer nc.Close()

			fmt.Println("Watching cluster activity (Ctrl+C to stop)...\n")

			// Subscribe to all relevant subjects
			nc.Subscribe("tasks.>", func(msg *nats.Msg) {
				var task map[string]any
				json.Unmarshal(msg.Data, &task)
				fmt.Printf("[TASK] %s: %s\n", task["id"], task["description"])
			})

			nc.Subscribe("results.>", func(msg *nats.Msg) {
				var task map[string]any
				json.Unmarshal(msg.Data, &task)
				result := task["result"].(map[string]any)
				status := "✓"
				if !result["success"].(bool) {
					status = "✗"
				}
				fmt.Printf("[RESULT] %s %s: %s\n", status, task["id"], result["summary"])
			})

			// Block forever
			select {}
		},
	}
}

func serveCmd() *cobra.Command {
	var repoPath string
	var maxAgents int

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the coordinator server",
		Run: func(cmd *cobra.Command, args []string) {
			if repoPath == "" {
				repoPath, _ = os.Getwd()
			}

			fmt.Printf("Starting coordinator...\n")
			fmt.Printf("  Repo: %s\n", repoPath)
			fmt.Printf("  NATS: %s\n", natsURL)
			fmt.Printf("  Max agents: %d\n", maxAgents)

			// coordinator, _ := swarm.NewCoordinator(natsURL, repoPath)
			// coordinator.Run()
			fmt.Println("\n[Coordinator would start here]")
			select {} // Block
		},
	}

	cmd.Flags().StringVarP(&repoPath, "repo", "r", "", "Repository path")
	cmd.Flags().IntVarP(&maxAgents, "max-agents", "m", 10, "Maximum concurrent agents")

	return cmd
}

// Helper functions
func generateID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())[:8]
}

func getCurrentRepo() string {
	// Would run: git remote get-url origin
	return "github.com/user/repo"
}

func getCurrentBranch() string {
	// Would run: git branch --show-current
	return "main"
}

func getHostname() string {
	h, _ := os.Hostname()
	return h
}

func waitForResult(nc *nats.Conn, taskID string) {
	sub, _ := nc.SubscribeSync(fmt.Sprintf("results.%s", taskID))
	msg, _ := sub.NextMsg(10 * time.Minute)
	if msg != nil {
		var task map[string]any
		json.Unmarshal(msg.Data, &task)
		printResult(task)
	}
}

func printResult(task map[string]any) {
	result := task["result"].(map[string]any)

	fmt.Println("=== Task Result ===\n")
	fmt.Printf("ID:       %s\n", task["id"])
	fmt.Printf("Status:   %s\n", task["status"])

	if result["success"].(bool) {
		fmt.Println("Success:  ✓")
	} else {
		fmt.Println("Success:  ✗")
	}

	fmt.Printf("Duration: %s\n", result["duration"])
	fmt.Printf("Cost:     $%.4f\n", result["cost_usd"])
	fmt.Printf("\nSummary:\n%s\n", result["summary"])

	if files, ok := result["files_changed"].([]any); ok && len(files) > 0 {
		fmt.Println("\nFiles changed:")
		for _, f := range files {
			fmt.Printf("  - %s\n", f)
		}
	}

	if commits, ok := result["commits"].([]any); ok && len(commits) > 0 {
		fmt.Println("\nCommits:")
		for _, c := range commits {
			fmt.Printf("  - %s\n", c)
		}
	}
}

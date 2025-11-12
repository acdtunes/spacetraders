package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// NewContainerCommand creates the container command with subcommands
func NewContainerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "container",
		Short: "Manage background containers",
		Long:  `Manage background containers running operations like navigation, mining, scouting, etc.`,
	}

	cmd.AddCommand(newContainerListCommand())
	cmd.AddCommand(newContainerLogsCommand())
	cmd.AddCommand(newContainerGetCommand())
	cmd.AddCommand(newContainerStopCommand())

	return cmd
}

// newContainerListCommand lists all containers
func newContainerListCommand() *cobra.Command {
	var status string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all containers",
		Long:  `List all background containers with their status.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			var playerIDPtr *int
			if playerID > 0 {
				playerIDPtr = &playerID
			}

			var statusPtr *string
			if status != "" {
				statusPtr = &status
			}

			containers, err := client.ListContainers(ctx, playerIDPtr, statusPtr)
			if err != nil {
				return fmt.Errorf("failed to list containers: %w", err)
			}

			if len(containers) == 0 {
				fmt.Println("No containers found")
				return nil
			}

			// Display containers in table format
			fmt.Printf("%-30s %-15s %-12s %-10s %s\n",
				"CONTAINER ID", "TYPE", "STATUS", "ITERATION", "CREATED")
			fmt.Println("─────────────────────────────────────────────────────────────────────────────────")

			for _, c := range containers {
				createdAt := formatTimestamp(c.CreatedAt)
				iteration := fmt.Sprintf("%d/%d", c.CurrentIteration, c.MaxIterations)
				if c.MaxIterations == -1 {
					iteration = fmt.Sprintf("%d/∞", c.CurrentIteration)
				}

				fmt.Printf("%-30s %-15s %-12s %-10s %s\n",
					truncate(c.ContainerID, 30),
					c.ContainerType,
					c.Status,
					iteration,
					createdAt,
				)
			}

			fmt.Printf("\nTotal: %d containers\n", len(containers))

			return nil
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Filter by status (RUNNING, COMPLETED, FAILED, etc.)")

	return cmd
}

// newContainerGetCommand gets detailed container info
func newContainerGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <container-id>",
		Short: "Get detailed container information",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			containerID := args[0]

			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			container, err := client.GetContainer(ctx, containerID)
			if err != nil {
				return fmt.Errorf("failed to get container: %w", err)
			}

			// Display detailed info
			fmt.Printf("Container: %s\n", container.ContainerID)
			fmt.Println("══════════════════════════════════════════════")
			fmt.Printf("  Type:             %s\n", container.ContainerType)
			fmt.Printf("  Status:           %s\n", container.Status)
			fmt.Printf("  Player ID:        %d\n", container.PlayerID)
			fmt.Printf("  Current Iteration: %d\n", container.CurrentIteration)
			fmt.Printf("  Max Iterations:   %d\n", container.MaxIterations)
			fmt.Printf("  Restart Count:    %d\n", container.RestartCount)
			fmt.Printf("  Created At:       %s\n", container.CreatedAt)
			fmt.Printf("  Updated At:       %s\n", container.UpdatedAt)

			if container.Metadata != "" {
				fmt.Printf("\nMetadata:\n%s\n", container.Metadata)
			}

			return nil
		},
	}

	return cmd
}

// newContainerStopCommand stops a container
func newContainerStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <container-id>",
		Short: "Stop a running container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			containerID := args[0]

			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.StopContainer(ctx, containerID)
			if err != nil {
				return fmt.Errorf("failed to stop container: %w", err)
			}

			fmt.Printf("✓ Container stopped: %s\n", result.ContainerID)
			fmt.Printf("  Status:  %s\n", result.Status)
			fmt.Printf("  Message: %s\n", result.Message)

			return nil
		},
	}

	return cmd
}

// newContainerLogsCommand retrieves container logs from database
func newContainerLogsCommand() *cobra.Command {
	var (
		limit int
		level string
	)

	cmd := &cobra.Command{
		Use:   "logs <container-id>",
		Short: "Get logs from a container",
		Long: `Retrieve logs for a specific container from the database.

Examples:
  spacetraders container logs navigate-SCOUT-1-1234567890
  spacetraders container logs navigate-SCOUT-1-1234567890 --limit 50
  spacetraders container logs navigate-SCOUT-1-1234567890 --level ERROR`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			containerID := args[0]

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Load config and connect to database
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			// Create log repository
			logRepo := persistence.NewGormContainerLogRepository(db)

			// Query logs
			ctx := context.Background()

			var levelPtr *string
			if level != "" {
				levelPtr = &level
			}

			logs, err := logRepo.GetLogs(ctx, containerID, playerIdent.PlayerID, limit, levelPtr, nil)
			if err != nil {
				return fmt.Errorf("failed to get logs: %w", err)
			}

			if len(logs) == 0 {
				fmt.Println("No logs found for container:", containerID)
				return nil
			}

			// Display logs in reverse order (oldest first)
			for i := len(logs) - 1; i >= 0; i-- {
				log := logs[i]
				fmt.Printf("[%s] [%s] %s\n",
					log.Timestamp.Format("2006-01-02 15:04:05"),
					log.Level,
					log.Message,
				)
			}

			fmt.Printf("\nTotal: %d log entries\n", len(logs))

			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum number of log entries")
	cmd.Flags().StringVar(&level, "level", "", "Filter by log level (INFO, WARNING, ERROR, DEBUG)")

	return cmd
}

// Helper functions

func formatTimestamp(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Format("2006-01-02 15:04:05")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

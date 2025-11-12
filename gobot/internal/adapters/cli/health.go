package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// NewHealthCommand creates the health command
func NewHealthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check daemon health status",
		Long:  `Verify that the daemon is running and responsive.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			health, err := client.HealthCheck(ctx)
			if err != nil {
				return fmt.Errorf("health check failed: %w", err)
			}

			fmt.Println("✓ Daemon is healthy")
			fmt.Printf("  Status:            %s\n", health.Status)
			fmt.Printf("  Version:           %s\n", health.Version)
			fmt.Printf("  Active Containers: %d\n", health.ActiveContainers)

			return nil
		},
	}

	return cmd
}

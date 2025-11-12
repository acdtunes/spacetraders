package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// NewNavigateCommand creates the navigate command
func NewNavigateCommand() *cobra.Command {
	var (
		shipSymbol  string
		destination string
	)

	cmd := &cobra.Command{
		Use:   "navigate",
		Short: "Navigate a ship to a destination waypoint",
		Long: `Navigate a ship to a destination waypoint within the same system.

The daemon will automatically:
- Orbit the ship if docked
- Plan the optimal route (including refuel stops if needed)
- Navigate to the destination
- Return a container ID for tracking progress

Examples:
  spacetraders navigate --ship AGENT-1 --destination X1-GZ7-B1 --player-id 1
  spacetraders navigate --ship SCOUT-2 --destination X1-GZ7-A1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if destination == "" {
				return fmt.Errorf("--destination flag is required")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Create gRPC client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Execute navigate command
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.NavigateShip(ctx, shipSymbol, destination, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("navigation failed: %w", err)
			}

			// Display result
			fmt.Println("✓ Navigation started successfully")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  Ship:             %s\n", result.ShipSymbol)
			fmt.Printf("  Destination:      %s\n", result.Destination)
			fmt.Printf("  Status:           %s\n", result.Status)
			fmt.Printf("  Estimated Time:   %d seconds\n", result.EstimatedTime)
			fmt.Printf("\nTrack progress with: spacetraders container logs %s\n", result.ContainerID)

			return nil
		},
	}

	// Command-specific flags
	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to navigate (required)")
	cmd.Flags().StringVar(&destination, "destination", "", "Destination waypoint symbol (required)")

	return cmd
}

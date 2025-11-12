package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// NewRefuelCommand creates the refuel command
func NewRefuelCommand() *cobra.Command {
	var (
		shipSymbol string
		units      int
	)

	cmd := &cobra.Command{
		Use:   "refuel",
		Short: "Refuel a ship at its current location",
		Long: `Refuel a ship at its current location.
Ship must be docked at a waypoint with fuel available.

Examples:
  spacetraders refuel --ship AGENT-1 --player-id 1
  spacetraders refuel --ship AGENT-1 --units 100 --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			var unitsPtr *int
			if units > 0 {
				unitsPtr = &units
			}

			result, err := client.RefuelShip(ctx, shipSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol, unitsPtr)
			if err != nil {
				return fmt.Errorf("refuel failed: %w", err)
			}

			fmt.Println("✓ Refuel operation started")
			fmt.Printf("  Container ID:  %s\n", result.ContainerID)
			fmt.Printf("  Ship:          %s\n", result.ShipSymbol)
			fmt.Printf("  Fuel Added:    %d\n", result.FuelAdded)
			fmt.Printf("  Credits Cost:  %d\n", result.CreditsCost)
			fmt.Printf("  Status:        %s\n", result.Status)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to refuel (required)")
	cmd.Flags().IntVar(&units, "units", 0, "Specific fuel units to purchase (omit for full tank)")

	return cmd
}

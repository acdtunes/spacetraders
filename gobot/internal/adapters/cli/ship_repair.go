package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newShipRepairUnreadableCommand is the manual door onto the repair the daemon already
// runs on its own. It exists for the hull an operator has in front of them, not because
// the automatic path needs help.
func newShipRepairUnreadableCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "repair-unreadable",
		Short: "Repair a hull the API will not serialise",
		Long: `Repair a hull whose composite record the SpaceTraders API refuses to serve.

The signature is a GET /my/ships/<SYMBOL> that returns a server error while every
sub-resource (/nav, /cargo, /cooldown, /mounts, /modules) still returns 200. That means one
field the sub-resources do not cover will not render, and fuel is the only such field a
client can write. Docking and refuelling overwrites it and the record serves again.

The daemon confirms that signature before it writes anything: it re-reads the composite
record, bisects against the sub-resources, and refuses when they refuse too (that is the API
being down, not this hull). The refuel is a real spend and passes the working-capital guard
unchanged, so an unreadable treasury or an unpriceable waypoint refuses rather than spends.

This runs the same sequence the standing sweep runs. It ignores only the backoff and any
prior escalation; the attempt bound still applies.

Examples:
  spacetraders ship repair-unreadable --ship ENDURANCE-1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			// The bisect and the fuel write are several rate-limited calls deep, and a
			// refused read spends its own retry ladder first.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()

			result, err := client.RepairUnreadableShip(ctx, shipSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("repair failed: %w", err)
			}
			if result.Error != "" {
				return fmt.Errorf("repair refused: %s", result.Error)
			}

			if result.Repaired {
				fmt.Printf("✓ %s repaired\n", result.ShipSymbol)
			} else {
				fmt.Printf("✗ %s not repaired\n", result.ShipSymbol)
			}
			fmt.Printf("  Outcome:  %s\n", result.Outcome)
			fmt.Printf("  Reason:   %s\n", result.Reason)
			if result.Attempts > 0 {
				fmt.Printf("  Attempts: %d\n", result.Attempts)
			}
			if result.Escalated {
				fmt.Printf("  ESCALATED: the automatic repair has given up on this hull; it will not be retried\n")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to repair (required)")

	return cmd
}

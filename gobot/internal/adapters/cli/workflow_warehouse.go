package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowWarehouseCommand creates the workflow warehouse subcommand.
//
// It is a THIN CLIENT: it asks the daemon to start a warehouse CONTAINER on an
// idle, dedicated storage hull parked at a home waypoint. The warehouse is a
// PASSIVE inventory buffer — it holds the whitelisted goods that tour/trade
// deposit legs drop into it, and contract workers withdraw from it in-system.
// It runs under the daemon's proven container lifecycle:
//
//   - single-writer safety + claim-release-on-death: the container claims the
//     hull under the "warehouse" fleet dedication (RULINGS #7) and releases it on
//     completion, crash, or daemon shutdown;
//   - restart recovery (RULINGS #2): the container is RUNNING and rebuildable
//     from its launch config, and the hull's live cargo is reconstructed by the
//     storage recovery service — no inventory is lost across a daemon restart.
//
// Run this on a genuinely idle hull; the daemon refuses a hull it is actively
// flying.
func newWorkflowWarehouseCommand() *cobra.Command {
	var (
		shipSymbol     string
		waypointSymbol string
		goodsCsv       string
	)

	cmd := &cobra.Command{
		Use:   "warehouse",
		Short: "Park an idle hull as a passive inventory warehouse at a home waypoint (as a daemon container)",
		Long: `Ask the daemon to dedicate one idle hull as a passive inventory warehouse
parked at a home waypoint, as a recovery-safe daemon container (sp-dchv Lane B).

The warehouse buffers a whitelist of contract goods: tour/trade deposit legs drop
cheap cross-system goods into it, and contract workers source those goods from it
in-system at zero market ask (inventory-first sourcing). The warehouse does no
work of its own — it is a standing buffer hull.

Execution model: the warehouse runs INSIDE the daemon as a container
(single-writer, claim-release-on-death, restart-safe). The hull is dedicated to
the "warehouse" fleet, so no other coordinator can poach it. This command only
starts it and returns the container id; follow it with 'container logs'. The
daemon must be running. Run this only on a genuinely idle hull.

Examples:
  spacetraders workflow warehouse --ship ENDURANCE-9 --waypoint X1-GZ7-H1 --goods IRON_ORE,ALUMINUM --agent ENDURANCE
  spacetraders workflow warehouse --ship ENDURANCE-9 --waypoint X1-GZ7-H1 --goods COPPER --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if waypointSymbol == "" {
				return fmt.Errorf("--waypoint flag is required")
			}
			goods := parseCsvList(goodsCsv)
			if len(goods) == 0 {
				return fmt.Errorf("--goods flag is required (at least one good)")
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

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.StartWarehouse(ctx, shipSymbol, waypointSymbol, goods, playerIdent.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to start warehouse: %w", err)
			}

			fmt.Println("✓ Warehouse started")
			fmt.Printf("  Container ID:  %s\n", result.ContainerID)
			fmt.Printf("  Ship:          %s\n", result.ShipSymbol)
			fmt.Printf("  Waypoint:      %s\n", result.WaypointSymbol)
			fmt.Printf("  Status:        %s\n", result.Status)
			if result.Message != "" {
				fmt.Printf("  Message:       %s\n", result.Message)
			}
			fmt.Println("\n  The warehouse runs as a daemon container (dedicated hull, claim-release-on-death, restart-safe).")
			fmt.Printf("  Use 'spacetraders container logs %s' to follow it.\n", result.ContainerID)
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Idle hull to dedicate as the warehouse (required)")
	cmd.Flags().StringVar(&waypointSymbol, "waypoint", "", "Home waypoint to park the warehouse hull at (required)")
	cmd.Flags().StringVar(&goodsCsv, "goods", "", "Comma-separated whitelist of goods the warehouse buffers (required)")

	return cmd
}

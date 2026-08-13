package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newShipScrapCommand builds `ship scrap`: sell a hull for credits and retire it.
// The sale is irreversible and the daemon performs it (RULING #3).
func newShipScrapCommand() *cobra.Command {
	var (
		shipSymbol string
		confirmed  bool
	)

	cmd := &cobra.Command{
		Use:   "scrap",
		Short: "Sell a ship back to a shipyard for credits and retire it",
		Long: `Sell a ship back to a shipyard for credits, removing it from the fleet.

The hull must be at a waypoint with the SHIPYARD trait. The daemon claims the
hull, docks it, sells it, records the proceeds in the ledger, and deletes its
fleet row.

CARGO IS DESTROYED, NOT PAID FOR. A full hold fetches the same price as an empty
one, so the daemon REFUSES to scrap any hull still carrying cargo. Drain the hold
first (spacetraders ship sell / jettison / transfer). The refusal cannot be
overridden.

The sale cannot be undone, so --yes is required.

Examples:
  spacetraders ship scrap --ship TORWIND-446 --yes --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if !confirmed {
				return fmt.Errorf("scrapping %s is irreversible: pass --yes to confirm", shipSymbol)
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

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			result, err := client.ScrapShip(ctx, shipSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("scrap failed: %w", err)
			}
			if result.Error != "" {
				return fmt.Errorf("scrap failed: %s", result.Error)
			}

			fmt.Printf("✓ Scrapped %s\n", result.ShipSymbol)
			fmt.Printf("  Shipyard: %s\n", result.WaypointSymbol)
			fmt.Printf("  Proceeds: %d credits\n", result.TotalPrice)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to scrap (required)")
	cmd.Flags().BoolVar(&confirmed, "yes", false, "Confirm the irreversible sale (required)")

	return cmd
}

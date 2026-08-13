package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// retireOutcomeLines renders what the operator is told after marking a hull. The next step
// after a retirement is scrapping, which destroys whatever is still in the hold — so a hull
// is called ready only when the hold is actually empty, and otherwise the line names what
// is still aboard.
func retireOutcomeLines(response *pb.RetireShipResponse) []string {
	if !response.Retiring {
		return []string{
			fmt.Sprintf("✓ %s retirement cancelled — it returns to normal service", response.ShipSymbol),
		}
	}
	if response.Drained {
		return []string{
			fmt.Sprintf("✓ %s marked retiring — its hold is empty, so it is ready to scrap", response.ShipSymbol),
		}
	}
	return []string{
		fmt.Sprintf("✓ %s marked retiring — still carrying %d unit(s)", response.ShipSymbol, response.CargoUnits),
		"  It keeps flying tours until it has sold that load, then stops being planned any.",
	}
}

// newShipRetireCommand creates the ship retire subcommand.
func newShipRetireCommand() *cobra.Command {
	var (
		shipSymbol string
		cancel     bool
	)

	cmd := &cobra.Command{
		Use:   "retire",
		Short: "Withdraw a hull from service once it has sold its load",
		Long: `Mark a hull for retirement. The tour it is flying finishes and sells normally;
once its hold is empty the trade coordinator plans it no further tours, and it
is then ready to scrap. A retiring hull that still carries cargo keeps flying —
that is how it sells — so retirement drains rather than strands.

This is not 'fleet unassign'. Unassign breaks the live claim and stops the
container mid-tour, parking the hull wherever it stood still holding its load,
and hands it to the general pool where another coordinator picks it up. Retire
touches neither the claim nor the dedication; it governs only the next tour.

Read progress with 'ship list': a retiring hull shows its mark in the FLEET
column and reads "drained" once the CARGO column reaches zero.

Examples:
  spacetraders ship retire --ship TORWIND-1E
  spacetraders ship retire --ship TORWIND-1E --cancel`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx, cancelCtx := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancelCtx()

			response, err := client.RetireShip(ctx, shipSymbol, cancel, playerIdent)
			if err != nil {
				return fmt.Errorf("failed to retire ship: %w", err)
			}

			for _, line := range retireOutcomeLines(response) {
				fmt.Println(line)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")
	cmd.Flags().BoolVar(&cancel, "cancel", false, "Clear the retirement, returning the hull to normal service")

	return cmd
}

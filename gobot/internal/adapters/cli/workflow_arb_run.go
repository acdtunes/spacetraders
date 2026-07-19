package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowArbRunCommand creates the workflow arb-run subcommand: a ONE-SHOT,
// captain-directed, guarded arbitrage run — the deliberate middle between
// hand-flying an arb leg and the autonomous trade-route circuit.
//
// Like trade-route this is a THIN CLIENT: it asks the daemon to start a
// recovery-safe arb-run CONTAINER rather than flying the trade in-process. The container
// buys the named good ONCE at the source, routes (cross-gate: jump + cooldown + refuel,
// same RouteExecutor-backed travel trade-route uses) to the destination, sells, and
// stops — no loop, no lane auto-selection.
//
// Every cap and floor is a guard that fails CLOSED (refuses the buy) rather than risk an
// overspend: the hull must actually be at --buy-at; the live source ask vs the
// destination bid must clear --min-margin; the tranche is bounded by --max-units, hold
// space, and --max-spend; and the buy must not drop live treasury below the
// working-capital reserve. Run this only on a genuinely idle hull — the daemon refuses a
// hull it is actively flying.
func newWorkflowArbRunCommand() *cobra.Command {
	var (
		shipSymbol string
		good       string
		buyAt      string
		sellAt     string
		maxUnits   int
		maxSpend   int
		minMargin  int
		reserve    int
	)

	cmd := &cobra.Command{
		Use:   "arb-run",
		Short: "Fly one idle hull through a single captain-directed, guarded arbitrage leg (as a daemon container)",
		Long: `Ask the daemon to fly ONE idle hull through a single captain-specified arbitrage
leg: buy a good at a source waypoint, route (cross-gate if needed) to a destination
waypoint, sell once, and stop. This is the safe middle between hand-flying an arb leg
and the autonomous 'workflow trade-route' circuit — you name the lane, it flies it
once, guarded.

Guards (each fails CLOSED, refusing the buy, rather than risk an overspend):
  - the hull must actually be at --buy-at before anything is bought;
  - the live source ask vs the destination bid must clear --min-margin;
  - the tranche is capped by --max-units, the hull's hold, and --max-spend;
  - the buy must not drop live treasury below the working-capital reserve.

Execution model: the run executes INSIDE the daemon as a container (single-writer,
claim-release-on-death, RouteExecutor-backed travel, restart-safe). This command only
starts it and returns the container id; follow it with 'container logs'. The daemon
must be running. Run this only on a genuinely idle hull.

Examples:
  spacetraders workflow arb-run --ship ENDURANCE-7 --good IRON_ORE --buy-at X1-GZ7-A1 --sell-at X1-GZ7-B2 --agent ENDURANCE
  spacetraders workflow arb-run --ship ENDURANCE-7 --good FUEL --buy-at X1-GZ7-H1 --sell-at X1-AB3-C4 --max-units 40 --max-spend 200000 --min-margin 500 --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if good == "" {
				return fmt.Errorf("--good flag is required")
			}
			if buyAt == "" {
				return fmt.Errorf("--buy-at flag is required")
			}
			if sellAt == "" {
				return fmt.Errorf("--sell-at flag is required")
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

			// Optional guards: 0 (flag unset) → nil, letting the coordinator apply its own
			// default/disabled semantics per guard (full hold, no spend cap, only reject a
			// non-positive margin, default working-capital reserve).
			maxUnitsArg := optionalInt32(maxUnits)
			maxSpendArg := optionalInt32(maxSpend)
			minMarginArg := optionalInt32(minMargin)
			reserveArg := optionalInt32(reserve)

			result, err := client.StartArbRun(ctx, shipSymbol, good, buyAt, sellAt, playerIdent.PlayerID, &playerIdent.AgentSymbol, maxUnitsArg, maxSpendArg, minMarginArg, reserveArg)
			if err != nil {
				return fmt.Errorf("failed to start arb-run: %w", err)
			}

			fmt.Println("✓ Arb-run started")
			fmt.Printf("  Container ID:  %s\n", result.ContainerID)
			fmt.Printf("  Ship:          %s\n", result.ShipSymbol)
			fmt.Printf("  Good:          %s\n", result.Good)
			fmt.Printf("  Buy at:        %s\n", result.BuyAt)
			fmt.Printf("  Sell at:       %s\n", result.SellAt)
			fmt.Printf("  Status:        %s\n", result.Status)
			if result.Message != "" {
				fmt.Printf("  Message:       %s\n", result.Message)
			}
			fmt.Println("\n  The run executes as a daemon container (single-writer, claim-release-on-death, one-shot).")
			fmt.Printf("  Use 'spacetraders container logs %s' to follow it.\n", result.ContainerID)
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Idle hull to fly the arb leg (required; must already be at --buy-at)")
	cmd.Flags().StringVar(&good, "good", "", "Good to buy at the source and sell at the destination (required)")
	cmd.Flags().StringVar(&buyAt, "buy-at", "", "Source waypoint to buy at (required)")
	cmd.Flags().StringVar(&sellAt, "sell-at", "", "Destination waypoint to sell at, may be cross-system (required)")
	cmd.Flags().IntVar(&maxUnits, "max-units", 0, "Cap the tranche to this many units (0 = the hull's full available cargo)")
	cmd.Flags().IntVar(&maxSpend, "max-spend", 0, "Working-capital cap on the buy in credits (0 = no explicit cap)")
	cmd.Flags().IntVar(&minMargin, "min-margin", 0, "Per-unit margin floor: abort before buying if (dest bid − source ask) < this (0 = only reject a non-positive margin)")
	cmd.Flags().IntVar(&reserve, "working-capital-reserve", 0, "Hard spend floor: never drop live treasury below this (0 = coordinator default)")

	return cmd
}

// optionalInt32 maps a CLI int guard flag to the proto's optional int32: 0 (unset) → nil,
// so the daemon coordinator applies its own default/disabled semantics for that guard.
func optionalInt32(v int) *int32 {
	if v == 0 {
		return nil
	}
	i := int32(v)
	return &i
}

package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowStockerCommand creates the workflow stocker subcommand: the STOCKER LOOP
// (sp-zdwg) — a dedicated hull that fills a home warehouse the tours rationally won't
// (sp-dchv proved deposit legs lose to direct sells at every re-plan; the stocker
// dedicates capacity instead of distorting tour objectives).
//
// Like tour-run this is a THIN CLIENT: it asks the daemon to start a recovery-safe
// stocker CONTAINER rather than flying in-process. Each round-trip the container
// need-ranks the most-needed supported stock good (highest savings/u × units-short), buys
// it at the cheapest foreign market (live-verified at the dock, fail-closed), hauls home,
// and deposits into the warehouse — repeating until nothing is left to stock.
//
// Guards fail CLOSED (RULINGS #4): every buy is live-checked against the capital ceiling
// (10% of live treasury), the per-leg budget, and the working-capital reserve; a run that
// ends holding cargo it bought but never deposited reports FAILED (never a false success),
// and the next run's first move is to deposit it. Run this only on a genuinely idle,
// dedicated hull — the daemon refuses one it is actively flying. A running 'workflow
// warehouse' at --warehouse-waypoint is a precondition (the deposit sink).
func newWorkflowStockerCommand() *cobra.Command {
	var (
		shipSymbol        string
		warehouseWaypoint string
		budgetPerLeg      int
		reserve           int64
		iterations        int
		maxMarketAge      int
		targetPerGood     int
		standing          bool
		tickSeconds       int
		refillHysteresis  int
	)

	cmd := &cobra.Command{
		Use:   "stocker",
		Short: "Fly one dedicated hull as a warehouse-filling stocker loop (as a daemon container)",
		Long: `Ask the daemon to fly ONE dedicated idle hull as a STOCKER LOOP: each round-trip it
need-ranks the most-needed supported stock good (highest savings-per-unit × units-short),
buys it at the cheapest foreign market (live-verified at the dock), hauls it home to the
warehouse, and deposits it — filling the home warehouse that the trade tours rationally
won't (they correctly prefer direct sells; the stocker dedicates capacity instead).

Continuous mode (--iterations -1): the container fills until nothing is left to stock (the
warehouse is at target, or nothing eligible is affordable/fresh), then completes honestly.
Relaunch it when contracts drain the warehouse. --iterations N runs exactly N productive
round-trips; 0 (default) runs one.

Guards (each fails CLOSED):
  - every buy is live-verified at the dock and capped by the capital ceiling (10% of live
    treasury), the per-leg budget (--budget-per-leg), and the working-capital reserve;
  - the foreign price must be fresh (--max-market-age-minutes, default 75);
  - a run that ends holding cargo it bought but never deposited reports FAILED, never a
    false success (the next run deposits it first).

Execution model: the stocker runs INSIDE the daemon as a container (single-writer,
claim-release-on-death, RouteExecutor-backed travel, restart-safe — a laden hull resumes
deposit-first). This command only starts it and returns the container id; follow it with
'container logs'. The daemon must be running, and a 'workflow warehouse' must be running at
--warehouse-waypoint. Run only on an idle, dedicated hull.

Examples:
  spacetraders workflow stocker --ship STOCKER-1 --warehouse-waypoint X1-GZ7-H1 --iterations -1 --agent ENDURANCE
  spacetraders workflow stocker --ship STOCKER-1 --warehouse-waypoint X1-GZ7-H1 --budget-per-leg 200000 --iterations -1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if warehouseWaypoint == "" {
				return fmt.Errorf("--warehouse-waypoint flag is required")
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

			// Optional knobs: 0 (flag unset) → nil, letting the coordinator apply its own
			// default per knob (budget_per_leg → no cap, working_capital_reserve → 50k,
			// max_market_age_minutes → 75, target_per_good → the miner's measured demand,
			// iterations → one round-trip). --iterations -1 (continuous) is non-zero, so it
			// maps through to &(-1) and is honored.
			// --standing (sp-k1ka): a standing refill that never completes at target — it
			// parks and auto-re-stages when contracts drain the warehouse below target, and
			// survives daemon restart (re-adopted standing). Launch it ONCE; no manual
			// relaunch. &standing is passed always (false = the historical finite behavior).
			result, err := client.StartStocker(ctx, shipSymbol, warehouseWaypoint, playerIdent.PlayerID, &playerIdent.AgentSymbol,
				optionalInt32(budgetPerLeg), optionalInt64(reserve), optionalInt32(iterations), optionalInt32(maxMarketAge), optionalInt32(targetPerGood),
				&standing, optionalInt32(tickSeconds), optionalInt32(refillHysteresis))
			if err != nil {
				return fmt.Errorf("failed to start stocker: %w", err)
			}

			fmt.Println("✓ Stocker started")
			fmt.Printf("  Container ID:       %s\n", result.ContainerID)
			fmt.Printf("  Ship:               %s\n", result.ShipSymbol)
			fmt.Printf("  Warehouse waypoint: %s\n", result.WarehouseWaypoint)
			fmt.Printf("  Status:             %s\n", result.Status)
			if result.Message != "" {
				fmt.Printf("  Message:            %s\n", result.Message)
			}
			fmt.Println("\n  The stocker executes as a daemon container (single-writer, claim-release-on-death, restart-safe).")
			fmt.Printf("  Use 'spacetraders container logs %s' to follow it.\n", result.ContainerID)
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Dedicated idle hull to fly the stocker loop (required)")
	cmd.Flags().StringVar(&warehouseWaypoint, "warehouse-waypoint", "", "Home warehouse waypoint to deposit into; its system is the demand anchor (required)")
	cmd.Flags().IntVar(&budgetPerLeg, "budget-per-leg", 0, "Per-buy-leg spend cap in credits (0 = no explicit per-leg cap)")
	cmd.Flags().Int64Var(&reserve, "working-capital-reserve", 0, "Hard spend floor: never drop live treasury below this (0 = coordinator default, 50k)")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Round-trip count: -1 = CONTINUOUS (fill until nothing left to stock), N>0 = N round-trips, 0 = one")
	cmd.Flags().IntVar(&maxMarketAge, "max-market-age-minutes", 0, "Freshness cap on the foreign ask at pick (0 = coordinator default, 75)")
	cmd.Flags().IntVar(&targetPerGood, "target-per-good", 0, "Fill-target override per good (0 = the miner's measured demand units)")
	cmd.Flags().BoolVar(&standing, "standing", false, "STANDING refill: never complete at target — park and auto-re-stage when contracts drain the warehouse below target; survives daemon restart. Launch once; no manual relaunch (sp-k1ka)")
	cmd.Flags().IntVar(&tickSeconds, "tick-seconds", 0, "STANDING park cadence between at-target re-checks in seconds (0 = default 30s); only used with --standing")
	cmd.Flags().IntVar(&refillHysteresis, "refill-hysteresis", 0, "STANDING target-hysteresis: minimum units-short before re-staging a good (0 = default 1), so a tiny gap does not thrash a refill; only used with --standing")

	return cmd
}

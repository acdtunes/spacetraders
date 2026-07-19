package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowAutoOutfitCommand creates the workflow auto-outfit subcommand: it
// launches the STANDING guarded auto-outfit coordinator — the module analogue of hull
// acquisition. Each tick it measures per-hull cargo saturation from tour_leg_telemetry,
// catalogs the modules available to buy off the market cache, and installs the
// highest-marginal-value (hull, module) upgrade behind a fail-closed money/ceiling/cap
// guard stack.
//
// Like the fleet-autosizer / capacity-reconciler it is a THIN CLIENT: it asks the daemon
// to start one recovery-safe coordinator container and returns its id. The coordinator
// survives daemon restarts (it re-adopts from its persisted launch config). THIS is the
// engine's ONLY start path — it is deliberately never boot-standing-armed (deploy-inert),
// so a fresh deploy changes nothing until an operator runs this command.
func newWorkflowAutoOutfitCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "auto-outfit",
		Short: "Start the standing guarded auto-outfit coordinator (installs the highest-marginal-value module upgrade on the most saturated hull, guarded)",
		Long: `Start the STANDING guarded auto-outfit coordinator for a player (sp-buyd) — the module
analogue of the fleet autosizer's hull-buying. Instead of buying a new hull to add capacity,
it closes a capability gap by installing a MODULE on an existing hull.

Each tick (default 5min):
  MEASURE   per-hull cargo saturation from tour_leg_telemetry (realized_units / capacity over
            the hull's load legs) — the SATURATED hull, not the busiest, is the upgrade target
            (a cargo module sits idle on a half-empty hull).
  CATALOG   the modules available to buy off the market cache (watchlist: FUEL_TANK,
            CARGO_HOLD_II/III), ranked by marginal value; announces one the first time it
            enters reach.
  SELECT    the highest-marginal-value (hull, module) pair: benefit = capacity gained x
            measured saturation x throughput, minus cost; hard-filtered on a free module
            slot + role match, FAIL-CLOSED on thin telemetry (a hull with too few legs is
            never upgraded), payback-gated vs the new-hull alternative (cheaper per unit wins).
  INSTALL   behind the guard stack: treasury reserve floor, 25%-of-treasury price ceiling,
            absolute price ceiling, per-tick install cap. Any unreadable input installs
            nothing this tick (fail-closed no-op).

The loop is idempotent, restart-safe, and self-healing (every decision re-derived from
persisted telemetry/market/fleet each tick).

Pass --dry-run to launch OBSERVE-ONLY: it evaluates + logs every WOULD-install each tick but
installs nothing (recommended first-start posture: watch a live cycle before arming). A
dry-run launch stays dry-run across daemon restarts until stopped and relaunched.

Calibration is live-tunable with no restart:
  spacetraders tune --operation autooutfit --show
  spacetraders tune --operation autooutfit min_telemetry_samples 12
  (knobs: min_telemetry_samples, price_ceiling, max_installs_per_tick, payback_horizon_hours,
   treasury_reserve, max_treasury_fraction_pct)

Examples:
  spacetraders workflow auto-outfit --agent TORWIND --dry-run
  spacetraders workflow auto-outfit --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
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

			containerID, err := client.AutoOutfitCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, dryRun)
			if err != nil {
				return fmt.Errorf("failed to start auto-outfit coordinator: %w", err)
			}

			fmt.Println("✓ Auto-outfit coordinator started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			if dryRun {
				fmt.Println("\n  DRY-RUN: it measures/catalogs/selects and logs every WOULD-install each tick but")
				fmt.Println("  installs NOTHING — watch a cycle, then relaunch without --dry-run to arm it.")
			} else {
				fmt.Println("\n  It installs the highest-marginal-value module upgrade on the most saturated hull each")
				fmt.Println("  tick, behind the treasury/ceiling/cap guard stack (unreadable input = no-op).")
			}
			fmt.Println("  Tune it live: spacetraders tune --operation autooutfit --show")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Measure + log every WOULD-install but install nothing (observe-only; recommended first start)")

	return cmd
}

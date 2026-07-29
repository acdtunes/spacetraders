package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowFleetAutosizerCommand creates the workflow fleet-autosizer subcommand: it
// launches the STANDING fleet capacity autosizer — the coordinator that sizes the hull pool to
// demand and AUTO-BUYS hulls (lights to factory-chain demand, heavies to unserved trade demand)
// behind the full fail-closed money-guard stack.
//
// Like the siting / trade-fleet coordinators it is a THIN CLIENT: it asks the daemon to start one
// recovery-safe coordinator container and returns its id. The coordinator survives daemon restarts
// (it re-adopts from its persisted launch config). It is LIVE BY DEFAULT: launched here it is ACTIVE
// immediately (no enablement flip) — every purchase runs the full guard stack, so a buy fires only
// when live treasury (net of the reserve floor), the price ceilings, the per-tick cap, and the
// class ceilings all clear.
//
// All tuning lives in config.yaml's [fleet_autosizer] section (the per-class ceilings, the price
// knobs, the purchase margin/cap), resolved LIVE on every build — so a retune is `edit config.yaml
// + restart daemon`, no code redeploy. This command only names the player/agent.
func newWorkflowFleetAutosizerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet-autosizer",
		Short: "Start the standing fleet capacity autosizer (sizes the hull pool to demand and auto-buys behind the money-guard stack)",
		Long: `Start the STANDING fleet capacity autosizer for a player (sp-1txd) — the buy-side twin of
'siting-coordinator'. It sizes the hull pool to demand and AUTO-BUYS hulls when funds clear the
full guard stack, retiring the captain's manual "watch the fleet, buy when short" loop.

Each slow tick (default 15min) it, per enabled hull class:
  DEMAND   LIGHTS  = ceil(desired_chains x light_rotation_slots) + rebalancer_vacancies vs the
                    HAULER worker pool (inverts the siting C3 rotation math).
           HEAVIES = one hull per profitable-but-unflown solver lane beyond the trade-hull count
                    (fail-closed until that lane-count read path lands).
  GUARD    a candidate buy passes ONLY if EVERY guard clears (fail-closed — any unreadable input
           blocks): demand>current, the class's own ceiling, per-tick cap, price
           readable, per-class price ceiling, treasury-% rule, and treasury net of the reserve
           floor >= price + margin. API-utilization fails CLOSED (an unreadable utilization holds
           growth). Every decision logs its arithmetic.
  BUY      on approval it buys ONE hull and DEDICATES it to its class fleet in the same breath
           (dedicate-at-purchase, so no coordinator poaches a heavy/warehouse hull), emits the
           purchase counter + a captain notice, and stops at the per-tick cap.

It is LIVE BY DEFAULT: launched here it is ACTIVE immediately, and it buys whenever the guard
stack clears. Watch it through the per-decision arithmetic in the log.

Tuning is config-driven (config.yaml [fleet_autosizer], live on daemon restart):
  tick_interval_secs / purchase_cap_per_tick                          pacing
  fleet_ceiling_{lights,heavies}                                      per-class API-budget ceilings
  purchase_margin_over_floor                                          treasury guard (margin over the
                                                                       immutable reserve floor)
  heavy_unserved_lanes_min                                            heavy anti-thrash streak
  max_price_{lights,heavies} / max_premium_over_cheapest_pct          price ceilings

Examples:
  spacetraders workflow fleet-autosizer --agent TORWIND
  spacetraders workflow fleet-autosizer --player-id 1`,
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

			containerID, err := client.FleetAutosizerCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to start fleet autosizer: %w", err)
			}

			fmt.Println("✓ Fleet capacity autosizer started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Println("\n  It sizes the hull pool to demand and auto-buys behind the money-guard stack (LIVE by default).")
			fmt.Println("  Tune it in config.yaml [fleet_autosizer] (live on daemon restart); every decision logs its arithmetic.")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	return cmd
}

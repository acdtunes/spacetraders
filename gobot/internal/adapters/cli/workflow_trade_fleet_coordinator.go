package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowTradeFleetCoordinatorCommand creates the workflow trade-fleet-coordinator
// subcommand: it launches the STANDING trade-fleet coordinator, the tour twin
// of `contract start` / `scout start`. The coordinator watches every 'trade'-dedicated
// hull and, whenever one is parked by an honest tour exit (margins died in both systems,
// or a completion), relaunches a fresh CONTINUOUS tour on it after a cooldown — retiring
// the captain's hand-relaunch loop.
//
// Like the scout/contract coordinators it is a THIN CLIENT: it asks the daemon to start
// one recovery-safe coordinator container and returns its id. The coordinator survives
// daemon restarts (it re-adopts from its persisted launch config) and each tour it spawns
// claims its own hull under operation="trade" — the coordinator claims nothing itself.
//
// All tuning lives in config.yaml's [trade_fleet] section (enabled, cooldown_seconds,
// max_concurrent_tours, tick_seconds, and the per-tour caps), resolved LIVE on every
// build — so a retune is `edit config.yaml + restart daemon`, no code redeploy and no
// coordinator recreate. This command only names the player/agent.
func newWorkflowTradeFleetCoordinatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trade-fleet-coordinator",
		Short: "Start the standing trade-fleet coordinator (keeps continuous tours alive on 'trade' hulls)",
		Long: `Start the STANDING trade-fleet coordinator for a player (sp-1278) — the tour twin of
'contract start'. It reconciles the 'trade'-dedicated fleet every tick: any hull parked
by an honest tour exit (margins died in both systems, or a sold-out completion) is
relaunched into a fresh CONTINUOUS tour after a per-hull cooldown that lets the local
ground breathe (the rich->tapped->rich cycle). A hull mid-tour is never disturbed.

This retires the captain's hand-relaunch loop: launch it once and every trade hull keeps
touring on its own, re-adopted across daemon restarts.

Ownership: each tour claims its own hull under operation="trade" (the coordinator claims
nothing). Captain off-switches are respected for free — a captain-reserved hull is
skipped, and unpinning a hull from the 'trade' fleet removes it from the coordinator's
view with no restart.

Tuning is config-driven (config.yaml [trade_fleet], live on daemon restart):
  enabled                on/off (default on)
  cooldown_seconds       per-hull relaunch cooldown (default 180)
  max_concurrent_tours   cap on simultaneous tours (0 = unlimited, bounded by fleet size)
  tick_seconds           reconcile cadence (default 30)
  max_hops / max_spend / min_margin / replan_limit / working_capital_reserve
                         per-tour caps (0 = the tour's own default)

Examples:
  spacetraders workflow trade-fleet-coordinator --agent TORWIND
  spacetraders workflow trade-fleet-coordinator --player-id 1`,
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

			containerID, err := client.TradeFleetCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to start trade fleet coordinator: %w", err)
			}

			fmt.Println("✓ Trade fleet coordinator started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Println("\n  It keeps continuous tours alive on every 'trade'-dedicated hull.")
			fmt.Println("  Tune it in config.yaml [trade_fleet] (live on daemon restart).")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	return cmd
}

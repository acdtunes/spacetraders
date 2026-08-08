package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowFleetGrowthCommand creates the workflow fleet-growth subcommand: it launches the
// STANDING fleet-growth coordinator, the fleet's ONLY heavy buyer. Like the autosizer / trade-fleet
// coordinators it is a THIN CLIENT — it asks the daemon to start one recovery-safe container and
// returns its id, and the coordinator survives restarts by re-adopting its persisted launch config.
// It is LIVE once launched; every purchase runs the full fail-closed guard stack. Its three levers
// are live-tunable, so this names nothing but the player/agent: no config section, no launch flag.
func newWorkflowFleetGrowthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet-growth",
		Short: "Start the standing fleet-growth coordinator (the fleet's only heavy buyer, behind the money-guard stack)",
		Long: `Start the STANDING fleet-growth coordinator for a player — the fleet's ONLY heavy buyer.

Each slow tick it derives the fleet's WAVE from durable facts and acts on it:
  WAVE     HEAVY when the fleet is capacity-short on trade lanes, a heavy target is priced, the
           heavy cap has room, and the ask is REACHABLE by this fleet (judged on the ledger's
           peak-over-window treasury, never a live balance that swings with the trade cycle).
           PROBE otherwise — and the reason is published, because a stalled coordinator and a
           deliberate PROBE wave otherwise look identical.
  GUARD    a candidate buy passes ONLY if EVERY guard clears (fail-closed — any unreadable input
           blocks): the heavy cap, the anti-thrash streak, a readable price under its ceiling and
           premium bound, API utilization, the 25%-of-treasury rule, and treasury net of the
           immutable reserve floor PLUS the working-capital term the trading fleet needs to keep
           flying. Every decision logs its arithmetic.
  BUY      on approval it buys ONE heavy and DEDICATES it in the same breath, emits the purchase
           counter + a captain notice, and stops.

The wave is also read by the probe drain, so the two spenders alternate off ONE definition rather
than bidding against each other for the same treasury.

It is LIVE BY DEFAULT: launched here it is ACTIVE immediately. Three live-tunable levers, applied on
the next tick with no restart (` + "`spacetraders tune --operation growth --show`" + `):
  growth_enabled              master switch: 1=on, 2=off (off also forces the wave to PROBE, so
                              probe buying resumes rather than pausing for a buyer that cannot buy)
  heavy_cap                   ceiling on owned HEAVY HULLS, counted fleet-wide
  growth_runway_milli_hours   milli-hours of the fleet's UNRECOVERED cargo position (bought minus
                              sold back) held back above the reserve floor

Examples:
  spacetraders workflow fleet-growth --agent TORWIND
  spacetraders workflow fleet-growth --player-id 1`,
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

			containerID, err := client.FleetGrowthCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to start fleet growth coordinator: %w", err)
			}

			fmt.Println("✓ Fleet-growth coordinator started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Println("\n  It owns the fleet's heavy buying and publishes the wave the probe drain reads (LIVE by default).")
			fmt.Println("  Tune it with 'spacetraders tune --operation growth' (applies next tick); every decision logs its arithmetic.")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	return cmd
}

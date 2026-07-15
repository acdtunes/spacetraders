package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowShipyardBackfillCommand creates the workflow shipyard-backfill subcommand (sp-s1ek):
// the LAUNCH VERB for the standing shipyard-backfill sweep engine (sp-rhju). Before this the
// engine — enumerator, deeper-first ranking, rate/idle bound, tune knob, restart-recovery — was
// fully built but INERT, with no start path. This verb closes that gap.
//
// Like the frontier / capacity coordinators it is a THIN CLIENT: it asks the daemon to start one
// recovery-safe coordinator container and returns its id. The coordinator survives daemon restarts
// (it re-adopts from its persisted launch config). THIS is the engine's ONLY start path — it is
// deliberately never boot-standing-armed, so a fresh deploy changes nothing until an operator runs
// this command.
func newWorkflowShipyardBackfillCommand() *cobra.Command {
	var (
		tickInterval  time.Duration
		maxDispatches int
	)

	cmd := &cobra.Command{
		Use:   "shipyard-backfill",
		Short: "Start the standing shipyard-backfill sweep (backfills the charted-but-unscanned shipyard systems, deeper-first)",
		Long: `Start the STANDING shipyard-backfill sweep for a player (sp-s1ek — the launch verb for the
sp-rhju engine). It is the highest-leverage heavy-hunt catch-up: the charted-but-unscanned
shipyard systems are blind spots (charted, so their existence is known, but never market/
shipyard-scanned), and each tick this coordinator backfills them.

Each tick it:
  ENUMERATE  the charted-but-unscanned shipyard systems (minus those already posted/scanned).
  RANK       deeper-first (the far frontier systems no standing post reaches).
  BOUND      by --max-dispatches (per-cycle sweep-once declaration cap) AND idle probe supply —
             it never declares more sweep-once posts than there are idle probes to man them.
  DECLARE    a sweep-once scout post for the top targets; the scout-post reconciler and its jump
             relays claim and move the probes. This coordinator moves and claims nothing.

It re-derives every decision from persisted state, so it survives daemon restarts (re-adopting
its container from the persisted launch config). The per-cycle rate cap is live-tunable without a
restart via 'spacetraders tune --operation shipyardbackfill'.

Examples:
  spacetraders workflow shipyard-backfill --agent TORWIND
  spacetraders workflow shipyard-backfill --player-id 1 --tick 60s --max-dispatches 3`,
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

			containerID, err := client.ShipyardBackfillCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, ShipyardBackfillCoordinatorParams{
				TickIntervalSecs:      int(tickInterval.Seconds()),
				MaxDispatchesPerCycle: maxDispatches,
			})
			if err != nil {
				return fmt.Errorf("shipyard backfill coordinator failed: %w", err)
			}

			fmt.Println("✓ Shipyard backfill coordinator started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Println("\n  It backfills the charted-but-unscanned shipyard systems each tick, deeper-first,")
			fmt.Println("  bounded by --max-dispatches and idle probe supply; the scout-post reconciler moves/mans.")
			fmt.Println("  Tune the rate cap live with 'spacetraders tune --operation shipyardbackfill'.")
			fmt.Println("  Watch decisions with 'spacetraders container logs " + containerID + "'.")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	cmd.Flags().DurationVar(&tickInterval, "tick", 0, "Reconcile cadence (e.g. 60s); 0 uses the coordinator default")
	cmd.Flags().IntVar(&maxDispatches, "max-dispatches", 0, "Per-cycle sweep-once declaration cap; 0 uses the coordinator default")
	return cmd
}

package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowBootstrapCommand creates the workflow bootstrap subcommand: it launches the
// STANDING captain bootstrap coordinator — the reconciler that encodes the known-good cold-start
// playbook and drives a fresh agent toward the jump gate, so the captain launches it once and
// monitors rather than re-deriving the cold-start sequence every era.
//
// Like the siting / fleet-autosizer coordinators it is a THIN CLIENT: it asks the daemon to start
// one recovery-safe coordinator container and returns its id. The coordinator survives daemon
// restarts (it re-observes and resumes at real state — no persisted cursor). It is LIVE BY DEFAULT:
// launched here it is ACTIVE immediately (no enablement flip).
//
// The cold-start SHAPE is fixed in the coordinator; config.yaml's [bootstrap] section carries only
// the boot-gate and the reconcile cadence, resolved LIVE on every build — so a change is `edit
// config.yaml + restart daemon`, no code redeploy. The money guards every buy respects are hard
// constants, not config. This command only names the player/agent.
func newWorkflowBootstrapCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "bootstrap",
		Short: "Start the standing captain bootstrap coordinator (drives a cold agent through the cold-start arc to the jump gate)",
		Long: `Start the STANDING captain bootstrap coordinator for a player (sp-3nbe) — the reconciler
that encodes the known-good cold-start playbook so the captain launches it once and monitors,
never babysits. It OBSERVES the live world each tick, DERIVES the current phase from that
observation (never a stored cursor), and ACTS on the delta behind guards, so a restart re-observes
and resumes at real state with no double-acting.

COLDSTART runs scanning and contracts as PARALLEL workstreams (contracts are the funding floor and
run from hour 0, never waiting on scanning); GATE follows, and EXPANSION is terminal:
  BUY     probes → 3, STAGED and capital-gated — and only when the treasury left after the buy
          still clears the immutable working-capital floor (cushion=(treasury−price) ≥
          common.ImmutableReserveFloor, 50k). Each decision logs its full arithmetic (price,
          treasury, cushion, floor, what would have blocked).
  SCOUT   declare the home coverage post so the scout-post coordinator mans an idle probe and
          market data flows.
  EARN    retire the frigate, place one light hauler per viable contract hub (up to 4), seed the
          trade hull, and run batch-contract — alongside the probe ramp, not after it.
  GATE    once the contract op is genuinely SCALED AND FUNDED (the full fleet has reached the
          auto-scaler's live target and the treasury holds a surplus), drive jump-gate
          construction and size gate workers.
  EXIT    at EXPANSION — the gate is BUILT, so it hands off to the standing coordinators and exits.

It is LIVE BY DEFAULT: launched here it is ACTIVE immediately. Set [bootstrap] bootstrap_disabled=
true to stand it down.

Tuning is two controls only — the cold-start shape is fixed in code:
  bootstrap_disabled   the escape (config.yaml, live on daemon restart)
  tick_seconds         reconcile cadence; also live-tunable as tick_secs with no restart

The working-capital money-guard itself (every buy leaves the treasury ≥ this floor) is the
immutable common.ImmutableReserveFloor (50k, sp-05glh) — a hard constant, not a config.yaml knob.

Examples:
  spacetraders workflow bootstrap --agent ENDURANCE
  spacetraders workflow bootstrap --player-id 1`,
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

			containerID, err := client.BootstrapCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to start bootstrap coordinator: %w", err)
			}

			fmt.Println("✓ Captain bootstrap coordinator started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Println("\n  It drives the cold start (probes → target, contracts from hour 0) LIVE by default.")
			fmt.Println("  Tune the cadence in config.yaml [bootstrap] (live on daemon restart).")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}
}

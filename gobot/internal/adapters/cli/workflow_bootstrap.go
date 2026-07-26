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
// launched here it is ACTIVE immediately (no enablement flip). Slice 1 runs the DATA phase — it buys
// probes to target (staged, capital-gated) and assigns every probe to scout-all-markets, then holds
// at DATA-complete once market coverage clears the bar (INCOME/GATE are later slices).
//
// All tuning lives in config.yaml's [bootstrap] section (the disable, probe target, coverage bar,
// tick cadence, probe ship type), resolved LIVE on every build — so a retune is `edit config.yaml +
// restart daemon`, no code redeploy. The money-guard itself (the working-capital floor every buy
// respects) is the immutable common.ImmutableReserveFloor (sp-05glh) — not a config.yaml knob. This
// command only names the player/agent and an optional --dry-run watch mode.
func newWorkflowBootstrapCommand() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Start the standing captain bootstrap coordinator (drives a cold agent through the cold-start arc to the jump gate)",
		Long: `Start the STANDING captain bootstrap coordinator for a player (sp-3nbe) — the reconciler
that encodes the known-good cold-start playbook so the captain launches it once and monitors,
never babysits. It OBSERVES the live world each tick, DERIVES the current phase from that
observation (never a stored cursor), and ACTS on the delta behind guards, so a restart re-observes
and resumes at real state with no double-acting.

DATA and INCOME are PARALLEL workstreams (contracts are the funding floor and run from hour 0, never
waiting on scanning); GATE follows, and EXPANSION is terminal:
  BUY     probes → probe_target (default 3), STAGED and capital-gated — at most one buy per tick,
          and only when the treasury left after the buy still clears the immutable working-capital
          floor (cushion=(treasury−price) ≥ common.ImmutableReserveFloor, 50k). Each decision logs
          its full arithmetic (price, treasury, cushion, floor, what would have blocked).
  SCOUT   assign every probe to scout-all-markets (idempotent VRP assignment) so market data flows.
  EARN    retire the frigate, place one light hauler per viable contract hub (up to hauler_target),
          and run batch-contract — alongside the probe ramp, not after it.
  GATE    once the contract op is genuinely SCALED AND FUNDED (the full fleet has reached the
          auto-scaler's live target and the treasury holds a surplus), drive jump-gate
          construction and size gate workers to the active material chains.
  EXIT    at EXPANSION — the gate is BUILT, so it hands off to the standing coordinators and exits.

It is LIVE BY DEFAULT: launched here it is ACTIVE immediately. Set [bootstrap] bootstrap_disabled=
true to stand it down. Pass --dry-run (or set [bootstrap] dry_run=true) to evaluate + log every
decision loudly while acting on nothing.

Tuning is config-driven (config.yaml [bootstrap], live on daemon restart):
  bootstrap_disabled / dry_run                        escapes
  probe_target                                        DATA probe target
  hauler_target / income_bar                          INCOME ramp + the earning bar
  gate_worker_target                                  GATE worker cap
  tick_seconds / probe_ship_type / hauler_ship_type   cadence + the assets bought

The working-capital money-guard itself (every buy leaves the treasury ≥ this floor) is the
immutable common.ImmutableReserveFloor (50k, sp-05glh) — a hard constant, not a config.yaml knob.

Examples:
  spacetraders workflow bootstrap --agent ENDURANCE
  spacetraders workflow bootstrap --player-id 1 --dry-run`,
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

			containerID, err := client.BootstrapCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, dryRun)
			if err != nil {
				return fmt.Errorf("failed to start bootstrap coordinator: %w", err)
			}

			fmt.Println("✓ Captain bootstrap coordinator started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			if dryRun {
				fmt.Println("\n  DRY-RUN: it evaluates + logs every decision but buys/assigns nothing.")
			} else {
				fmt.Println("\n  It drives the DATA phase (probes → target, scout every market) LIVE by default.")
			}
			fmt.Println("  Tune it in config.yaml [bootstrap] (live on daemon restart).")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Evaluate + log every decision but buy/assign nothing (watch mode)")

	return cmd
}

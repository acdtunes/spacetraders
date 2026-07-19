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
// reserve margin, tick cadence, probe ship type), resolved LIVE on every build — so a retune is
// `edit config.yaml + restart daemon`, no code redeploy. This command only names the player/agent
// and an optional --dry-run watch mode.
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

Slice 1 runs the DATA phase (INCOME/GATE are later slices):
  BUY     probes → probe_target (default 3), STAGED and capital-gated — at most one buy per tick,
          and only when the price clears the money-guard (spend ≤ reserve_margin × treasury). Each
          decision logs its full arithmetic (price, treasury, the cap, what would have blocked).
  SCOUT   assign every probe to scout-all-markets (idempotent VRP assignment) so market data flows.
  EXIT    hold at DATA-complete once market coverage ≥ coverage_bar (the later phases are stubs).

It is LIVE BY DEFAULT: launched here it is ACTIVE immediately. Set [bootstrap] bootstrap_disabled=
true to stand it down. Pass --dry-run (or set [bootstrap] dry_run=true) to evaluate + log every
decision loudly while acting on nothing.

Tuning is config-driven (config.yaml [bootstrap], live on daemon restart):
  bootstrap_disabled / dry_run                        escapes
  probe_target / coverage_bar                         DATA target + exit
  reserve_margin                                      the ≤-fraction-of-treasury money-guard + pacer
  tick_seconds / probe_ship_type                      cadence + the asset bought

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

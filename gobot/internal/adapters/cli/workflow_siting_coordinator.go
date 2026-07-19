package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowSitingCoordinatorCommand creates the workflow siting-coordinator subcommand: it
// launches the STANDING factory-siting coordinator — the "brain" that automates
// factory discovery, placement, and capacity planning, the factory twin of the trade-fleet /
// scout-post coordinators.
//
// Like those it is a THIN CLIENT: it asks the daemon to start one recovery-safe coordinator
// container and returns its id. The coordinator survives daemon restarts (it re-adopts from its
// persisted launch config), and every goods_factory chain it launches runs the full guard stack
// (2dv4 + a5j7 + C2 + r5a6) on its own passes — the siting coordinator drives PORTFOLIO
// membership, never bypassing a per-chain guard.
//
// Each slow tick it SCANs candidate (good, system) sites (export-site hard gate + in-system
// input eligibility + freshness), SCOREs them by branchPL × tour-alignment − competition −
// staleness, MAINTAINs the top-K portfolio (K = floor(haulers / workers_per_chain)), ACTs by
// launching the missing top-K and retiring chains that fall out (with hysteresis), and EMITs
// scout-demand for stale-but-promising sites. It is LIVE BY DEFAULT (Admiral: no dark-shipping).
//
// All tuning lives in config.yaml's [manufacturing.siting] section (siting_disabled, the tick
// cadence, top_k / workers_per_chain, the score weights, the concentration caps, and the
// hysteresis / self-check / scout-demand knobs), resolved LIVE on every build — so a retune is
// `edit config.yaml + restart daemon`, no code redeploy and no coordinator recreate. This
// command only names the player/agent.
func newWorkflowSitingCoordinatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "siting-coordinator",
		Short: "Start the standing factory-siting coordinator (automates factory discovery, placement, and capacity planning)",
		Long: `Start the STANDING factory-siting coordinator for a player (sp-vdld) — the factory twin
of 'trade-fleet-coordinator'. It is the standing "brain" that automates factory discovery,
placement, and capacity planning, retiring the captain's manual expansion sweeps.

Each slow tick (default 15min) it:
  SCAN     enumerate candidate (good, system) factory sites that pass the export-site hard gate
           (a factory is map-fixed — you cannot manufacture where a good only IMPORTs/EXCHANGEs),
           in-system input eligibility (supply-first), and market-data freshness.
  SCORE    branchPL projection x tour-alignment - input-competition - staleness.
  MAINTAIN pick the top-K portfolio (K = floor(haulers / workers_per_chain)), subject to
           per-system and per-input-market concentration caps.
  ACT      launch missing top-K chains THROUGH the guard stack (each launched goods_factory
           coordinator runs 2dv4 + a5j7 + C2 + r5a6 on its own passes — guards veto at zero
           cost, never bypassed); retire chains that fall out of top-K via a clean stop, with
           hysteresis to prevent thrash.
  EMIT     post scout-demand for stale-but-promising sites so coverage refreshes them.

It is LIVE BY DEFAULT: launched here it is ACTIVE immediately (no enablement flip). Set
[manufacturing.siting] siting_disabled=true in config.yaml to stand the whole brain down.

Ownership: it claims nothing itself — each goods_factory chain it launches claims its own
hulls through the existing factory path. It composes with (never duplicates) the per-chain
guards: it drives portfolio membership; each chain keeps its own safety.

Tuning is config-driven (config.yaml [manufacturing.siting], live on daemon restart):
  siting_disabled            emergency off-switch (default off = ACTIVE)
  dry_run                    evaluate + log decisions but take no action (watch mode)
  tick_interval_secs         reconcile cadence (default 900)
  top_k / workers_per_chain  portfolio size (top_k pins it; else derived, default /3.5)
  weight_* / max_chains_*    score weights and concentration caps
  freshness_max_secs / emit_staleness_secs / retire_hysteresis_ticks / ...

Examples:
  spacetraders workflow siting-coordinator --agent TORWIND
  spacetraders workflow siting-coordinator --player-id 1`,
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

			containerID, err := client.SitingCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to start siting coordinator: %w", err)
			}

			fmt.Println("✓ Factory-siting coordinator started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Println("\n  It automates factory discovery, placement, and capacity planning (LIVE by default).")
			fmt.Println("  Tune it in config.yaml [manufacturing.siting] (live on daemon restart).")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	return cmd
}

package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowWorkerRebalancerCoordinatorCommand creates the workflow
// worker-rebalancer-coordinator subcommand (sp-f5pr): it launches the STANDING
// worker-rebalancer coordinator, which ferries idle undedicated light-haulers cross-system
// to worker-starved factory systems so a factory posting "No in-system worker" self-heals
// without captain hand-holding.
//
// Like the trade-fleet / scout coordinators it is a THIN CLIENT: it asks the daemon to
// start one recovery-safe coordinator container and returns its id. The coordinator
// survives daemon restarts (it re-adopts from its persisted launch config, and ALL its
// operational state is derived from ship + container rows — zero new persisted state), and
// each ferry it spawns claims its own hull under operation="worker_ferry" (occupancy, not a
// dedication) — the coordinator poaches nothing pinned or captain-reserved.
//
// A vacancy is a factory system that has run past a warm-up window with no in-system idle
// light and fewer lights than factory chains; the coordinator jump-routes the nearest idle
// light from a source system with a spare, then reclaims it on arrival so the destination
// factory mans it in-system.
//
// All tuning lives in config.yaml's [worker_rebalancer] section (enabled, vacancy_min_minutes,
// source_min_idle, ferry_cooldown_seconds, max_concurrent_ferries, max_lights_per_system,
// tick_seconds), resolved LIVE on every build — so a retune is `edit config.yaml + restart
// daemon`, no code redeploy. This command only names the player/agent; --dry-run makes the
// coordinator decide + log without ferrying anything (run it first to preview).
func newWorkflowWorkerRebalancerCoordinatorCommand() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "worker-rebalancer-coordinator",
		Short: "Start the standing worker-rebalancer coordinator (ferries idle lights to worker-starved factory systems)",
		Long: `Start the STANDING worker-rebalancer coordinator for a player (sp-f5pr). It reconciles
every tick: it finds worker-starved factory systems (a factory past its warm-up window with
no in-system idle light and fewer lights than factory chains) and ferries the nearest idle
undedicated light-hauler from a source system that can spare one — then reclaims the hull on
arrival so the destination factory mans it in-system.

Launch it once and worker starvation self-heals across daemon restarts; the coordinator
holds no in-memory state (every clock/cap is derived from ship + container rows).

Ownership: each ferry claims its own hull under operation="worker_ferry" (occupancy, not a
dedication — the coordinator claims nothing directly, and never poaches a pinned or
captain-reserved hull). Guards fail closed: any unreadable state ⇒ no ferry that tick.

Tuning is config-driven (config.yaml [worker_rebalancer], live on daemon restart):
  enabled                  on/off (default on)
  tick_seconds             reconcile cadence (default 60)
  vacancy_min_minutes      factory warm-up before a system counts as starved (default 15)
  source_min_idle          idle lights a source must hold to donate one (default 2)
  ferry_cooldown_seconds   per-system suppress window after a ferry (default 600)
  max_concurrent_ferries   cap on simultaneous ferries (default 2)
  max_lights_per_system    per-system light cap incl. in-flight (default 0 = uncapped)

Examples:
  spacetraders workflow worker-rebalancer-coordinator --agent TORWIND --dry-run
  spacetraders workflow worker-rebalancer-coordinator --agent TORWIND
  spacetraders workflow worker-rebalancer-coordinator --player-id 1`,
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

			containerID, err := client.WorkerRebalancerCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, dryRun)
			if err != nil {
				return fmt.Errorf("failed to start worker rebalancer coordinator: %w", err)
			}

			fmt.Println("✓ Worker rebalancer coordinator started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			if dryRun {
				fmt.Println("  Mode:         DRY-RUN (decides + logs, ferries nothing)")
			}
			fmt.Println("\n  It ferries idle light-haulers to worker-starved factory systems.")
			fmt.Println("  Tune it in config.yaml [worker_rebalancer] (live on daemon restart).")
			fmt.Println("  Watch it with 'spacetraders container logs " + containerID + "'.")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Decide and log the ferry it would dispatch, but ferry nothing")

	return cmd
}

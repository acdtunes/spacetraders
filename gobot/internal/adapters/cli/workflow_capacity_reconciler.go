package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newWorkflowCapacityReconcilerCommand creates the workflow capacity-reconciler subcommand
// (epic st-7zk, foundation st-fyr): it launches the STANDING capacity reconciler — the
// declarative engine that continuously drives the contract-delivery machine's actual capacity
// topology (clusters/hubs/warehouses/stockers/workers) toward a computed desired topology,
// maximizing per-hull-sustained $/hr with cycle-time as the lever, paced by the treasury capex
// governor.
//
// Like the fleet-autosizer / siting coordinators it is a THIN CLIENT: it asks the daemon to
// start one recovery-safe coordinator container and returns its id. The coordinator survives
// daemon restarts (it re-adopts from its persisted launch config). THIS is the engine's ONLY
// start path — it is deliberately never boot-standing-armed, so a fresh deploy changes nothing
// until an operator runs this command.
func newWorkflowCapacityReconcilerCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "capacity-reconciler",
		Short: "Start the standing capacity reconciler (drives actual contract-delivery topology toward the computed desired topology, capex-paced)",
		Long: `Start the STANDING capacity reconciler for a player (epic st-7zk) — the declarative
actual → desired → diff → converge engine for the contract-delivery machine.

Each tick (default 5min) it runs SENSE → PLAN → DIFF → GOVERN → CONVERGE:
  SENSE     read-only signals: per-hub contract demand, accept→fulfill cycle-times, current
            topology, per-hull utilization, treasury/economics.
  PLAN      desired topology: covered hubs, buffered goods + caps per hub, warehouse/stocker/
            worker counts + positions — every add ROI-gated on per-hull-$/hr.
  DIFF      gap → ordered actions, cheapest-lever-first: 1 reuse idle hulls, 2 rebalance/
            reposition, 3 buffer whitelist/caps, 4 add cluster/autobuy (capital).
  GOVERN    capex pacing: reserve floor, surplus-fraction drain, 25%-per-decision cap, ROI gate.
  CONVERGE  cheap tiers execute autonomously via the existing primitives; capital actions file
            a PROPOSAL for approval (tiered autonomy v1 — nothing is auto-bought).

The loop is stateless per tick (idempotent, restart-safe, self-healing) and honors the
captain/DISABLED kill switch at the top of EVERY tick.

Pass --dry-run (or set [capacity_reconciler] dry_run=true) to launch OBSERVE-ONLY: SENSE/PLAN/
DIFF/GOVERN run as normal but CONVERGE actuates nothing and files no proposal — it logs what it
WOULD do each tick (recommended first-start posture: watch a live cycle before arming). A
dry-run launch stays dry-run across daemon restarts until stopped and relaunched.

FOUNDATION STATE: the intelligence lanes land incrementally — with the no-op planner wired the
engine provably emits ZERO actions, so starting it is safe and changes nothing yet.

Calibration is config-driven (config.yaml [capacity_reconciler], live on daemon restart):
  reserve_floor / surplus_fraction / per_decision_cap_pct (default 25)   capex governor
  roi_payback_horizon_hours / add_threshold_per_hull_cr_hr               ROI gates
  stocker_capacity_budget                                                buffer selection
  tick_interval_secs (default 300) / approval_threshold (default 0 =     pacing + autonomy
  every capital action needs approval)

Examples:
  spacetraders workflow capacity-reconciler --agent TORWIND
  spacetraders workflow capacity-reconciler --player-id 1`,
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

			containerID, err := client.CapacityReconcilerCoordinator(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, dryRun)
			if err != nil {
				return fmt.Errorf("failed to start capacity reconciler: %w", err)
			}

			fmt.Println("✓ Capacity reconciler started")
			fmt.Printf("  Container ID: %s\n", containerID)
			fmt.Printf("  Agent:        %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			if dryRun {
				fmt.Println("\n  DRY-RUN: it evaluates + logs every decision each tick but actuates NOTHING (no reuse/")
				fmt.Println("  rebalance/buffer change) and files NO proposal — watch a cycle, then relaunch to arm it.")
			} else {
				fmt.Println("\n  It reconciles actual contract-delivery topology toward the desired topology every tick,")
				fmt.Println("  honoring captain/DISABLED each tick; capital spends file proposals (nothing auto-buys in v1).")
			}
			fmt.Println("  Tune it in config.yaml [capacity_reconciler] (live on daemon restart).")
			fmt.Println("  Stop with 'spacetraders container stop " + containerID + "'.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Evaluate + log every decision but actuate nothing and file no proposal (observe-only)")

	return cmd
}

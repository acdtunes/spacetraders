package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	capacityCmd "github.com/andrescamacho/spacetraders-go/internal/application/capacity/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// This file wires the capacity reconciler's launch path + live-config resolution + recovery
// build (epic st-7zk foundation, st-fyr). The launch trigger mirrors FleetAutosizerCoordinator:
// identity-only launch config → buildCommandForType (the single builder shared by creation and
// recovery) → NewContainer with iterations=-1 for the infinite reconcile loop → Add → runner →
// registerContainer → go Start. All calibration ([capacity_reconciler]) resolves LIVE from
// config.yaml inside buildCommandForType, so a config edit + restart retunes even a recovered
// coordinator.
//
// BOOT-STANDING (sp-ov8z, epic sp-difa): this coordinator IS now a member of
// bootStandingCoordinatorTypes — daemon boot launches it unconditionally for a zero-intervention
// cold start (it is the only standing brain the bootstrap GATE hand-off does not launch). This
// DELIBERATELY reverses the earlier "st-fyr deploy-inert hard requirement"; the reversal is safe
// ONLY because sp-2jrz (stop-is-complete-retire) landed. It can also still be started explicitly:
//
//	spacetraders workflow capacity-reconciler --agent <AGENT>   (CLI → gRPC CapacityReconcilerCoordinator)
//
// It is restart-safe: the container persists as RUNNING, and a daemon restart re-adopts it through
// RecoverRunningContainers → buildCommandForType, the same recovery idiom every standing coordinator
// uses (RULINGS #2). FLAGGED (sp-ov8z): now that it is boot-standing, a bare `container stop` no
// longer decommissions it across a restart — the next boot re-launches it — so a durable decommission
// additionally needs config dry_run/disable (cf. the sp-udgc demand-driven-boot-guard pattern).

// CapacityReconcilerCoordinator starts the standing capacity reconciler for a player: a
// recovery-safe container that each tick drives the contract-delivery machine's actual capacity
// topology toward the computed desired topology (SENSE → PLAN → DIFF → GOVERN → CONVERGE),
// capex-paced and kill-switch-gated. The production wiring (main.go) is the FULLY ARMED engine —
// real SENSE/PLAN/DIFF/GOVERN plus a cheap-tier actuator that reassigns/repositions hulls and
// writes depot buffers; only tier-4 capital stays gated behind the human-approved proposal path.
// "Deploy-inert" here means only that it is never boot-standing (below), NOT that it is observe-only.
func (s *DaemonServer) CapacityReconcilerCoordinator(ctx context.Context, playerID int, dryRun bool) (string, error) {
	// Double-launch guard: ONE standing reconciler per player. A twin loop
	// would double-execute tier-1..3 actions and double-file proposals once
	// the actuation lanes land (Proposal.ID's "stable across re-files" dedupe
	// assumes a single filer) — refuse loudly, matching the guarded launches
	// elsewhere (container_ops_contract.go).
	existingID, err := firstContainerIDOfType(ctx, s.containerRepo, playerID, container.ContainerTypeCapacityReconciler)
	if err != nil {
		return "", fmt.Errorf("failed to check for a running capacity reconciler: %w", err)
	}
	if existingID != "" {
		return "", fmt.Errorf("capacity reconciler already running for player %d (container %s) — stop it first: spacetraders container stop %s",
			playerID, existingID, existingID)
	}

	containerID := utils.GenerateContainerID("capacity_reconciler", fmt.Sprintf("player-%d", playerID))

	// Identity only — the [capacity_reconciler] knobs are injected by
	// resolveCapacityReconcilerConfig inside buildCommandForType, the single injection point
	// shared by creation and recovery. capacity_launch_dry_run is an IDENTITY flag: the
	// launch-time --dry-run decision, persisted so a recovered container stays observe-only
	// until it is stopped and relaunched (deliberately NOT a live-config key — mirrors
	// bootstrap_launch_dry_run). config.yaml [capacity_reconciler] dry_run provides the
	// default when the flag is absent; either source arms observe-only.
	config := map[string]interface{}{
		"container_id": containerID,
	}
	if dryRun {
		config["capacity_launch_dry_run"] = true
	}

	cmd, err := s.buildCommandForType("capacity_reconciler_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create capacity reconciler command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeCapacityReconciler,
		playerID,
		-1,  // Infinite iterations (reconcile loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "capacity_reconciler_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist capacity reconciler container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Capacity reconciler container")

	return containerID, nil
}

// capacityReconcilerConfigKeys enumerates every launch-config key the [capacity_reconciler]
// knobs occupy. resolveCapacityReconcilerConfig clears these before re-injecting the live
// values, so a stale persisted copy from a prior boot can never shadow the current config.yaml
// (the sp-ts82 live-config discipline). Keep in lockstep with
// injectCapacityReconcilerConfig and buildCapacityReconcilerCoordinatorCommand's reads.
// container_id and capacity_launch_dry_run are IDENTITY (set once at creation) and are
// deliberately NOT in this list — they must SURVIVE a rebuild (the CLI --dry-run decision
// stays sticky across a daemon restart, mirroring bootstrap_launch_dry_run).
var capacityReconcilerConfigKeys = []string{
	"capacity_dry_run",
	"capacity_reserve_floor",
	"capacity_surplus_fraction",
	"capacity_per_decision_cap_pct",
	"capacity_roi_payback_horizon_hours",
	"capacity_add_threshold_per_hull_cr_hr",
	"capacity_stocker_capacity_budget",
	"capacity_tick_interval_secs",
	"capacity_approval_threshold",
}

// resolveCapacityReconcilerConfig makes config.yaml the single LIVE source of truth for the
// reconciler's calibration (mirroring resolveFleetAutosizerConfig). It clears any capacity_*
// keys already in the launch config (stale copies persisted at a prior boot) and re-injects the
// daemon's boot-loaded values, so the rebuilt command reflects the CURRENT config.yaml on every
// build — creation and restart recovery alike. The clear is what lets dropping a knob from
// config.yaml fall back to the coordinator's own documented default rather than being shadowed
// by the now-absent live value.
func (s *DaemonServer) resolveCapacityReconcilerConfig(config map[string]interface{}) {
	for _, key := range capacityReconcilerConfigKeys {
		delete(config, key)
	}
	s.injectCapacityReconcilerConfig(config)
}

// injectCapacityReconcilerConfig writes the [capacity_reconciler] knobs from config.yaml
// (s.capacityReconcilerConfig) into the coordinator container's launch config. Only keys the
// captain actually set (non-zero) are written, so an unset knob defers to the coordinator's own
// documented default (RULINGS #5 — the daemon never hardcodes the operational values).
func (s *DaemonServer) injectCapacityReconcilerConfig(config map[string]interface{}) {
	cr := s.capacityReconcilerConfig
	if cr.DryRun {
		config["capacity_dry_run"] = true
	}
	if cr.ReserveFloor != 0 {
		config["capacity_reserve_floor"] = int(cr.ReserveFloor)
	}
	if cr.SurplusFraction != 0 {
		config["capacity_surplus_fraction"] = cr.SurplusFraction
	}
	if cr.PerDecisionCapPct != 0 {
		config["capacity_per_decision_cap_pct"] = cr.PerDecisionCapPct
	}
	if cr.ROIPaybackHorizonHours != 0 {
		config["capacity_roi_payback_horizon_hours"] = cr.ROIPaybackHorizonHours
	}
	if cr.AddThresholdPerHullCrHr != 0 {
		config["capacity_add_threshold_per_hull_cr_hr"] = cr.AddThresholdPerHullCrHr
	}
	if cr.StockerCapacityBudget != 0 {
		config["capacity_stocker_capacity_budget"] = cr.StockerCapacityBudget
	}
	if cr.TickIntervalSecs != 0 {
		config["capacity_tick_interval_secs"] = cr.TickIntervalSecs
	}
	if cr.ApprovalThreshold != 0 {
		config["capacity_approval_threshold"] = int(cr.ApprovalThreshold)
	}
}

// buildCapacityReconcilerCoordinatorCommand rebuilds the standing reconciler command from a
// persisted launch config so a daemon restart re-adopts it (RULINGS #2). The
// [capacity_reconciler] knobs are resolved LIVE from config.yaml just before this runs
// (resolveCapacityReconcilerConfig in buildCommandForType), so the persisted capacity_* keys are
// transient — the reads below see the current config.yaml. Every knob is optional (0 → the
// coordinator's documented default, RULINGS #5), so the creation op and recovery share one
// construction and can never drift.
func buildCapacityReconcilerCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &capacityCmd.RunCapacityReconcilerCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(playerID),
		ContainerID: cfg.RequiredNonEmptyString("container_id"),

		// DryRun ORs the live config.yaml knob (capacity_dry_run, re-injected each
		// build) with the persisted launch-time --dry-run flag (capacity_launch_dry_run,
		// preserved across restart) — either source arms observe-only, and a dry-run
		// launch stays dry-run through recovery (mirrors buildBootstrapCommand). The
		// predicate is factored into capacityReconcilerDryRun so the depot buffer-authority
		// handoff (armedCapacityReconcilerOwnsBuffers) reads "armed" the SAME way.
		DryRun:           capacityReconcilerDryRun(cfg),
		TickIntervalSecs: cfg.OptionalInt("capacity_tick_interval_secs", 0),
		// Money floor reads with PresentOrFail semantics (sp-ggk2 doctrine):
		// a PRESENT-but-unparseable reserve floor must fail the build, never
		// silently collapse to the 50k default and under-protect the runway.
		ReserveFloorCredits:      int64(cfg.PresentOrFailInt("capacity_reserve_floor", 0)),
		SurplusFraction:          cfg.OptionalFloat("capacity_surplus_fraction", 0),
		PerDecisionCapPct:        cfg.OptionalInt("capacity_per_decision_cap_pct", 0),
		ROIPaybackHorizonHours:   cfg.OptionalFloat("capacity_roi_payback_horizon_hours", 0),
		AddThresholdPerHullCrHr:  cfg.OptionalFloat("capacity_add_threshold_per_hull_cr_hr", 0),
		StockerCapacityBudget:    cfg.OptionalInt("capacity_stocker_capacity_budget", 0),
		ApprovalThresholdCredits: int64(cfg.OptionalInt("capacity_approval_threshold", 0)),
	}
}

// capacityReconcilerDryRun is the SINGLE arming predicate for the reconciler: DryRun is TRUE
// (observe-only, NOT armed) when EITHER the live config knob (capacity_dry_run) OR the persisted
// launch-time flag (capacity_launch_dry_run) is set — either source arms observe-only. It is
// SHARED by buildCapacityReconcilerCoordinatorCommand (what the reconciler itself runs as) and by
// armedCapacityReconcilerOwnsBuffers (the depot buffer-authority handoff, sp-j4mc), so the two can
// never drift on what "armed" means — a divergence would either strand the depot buffer or let it
// fight a live armed reconciler.
func capacityReconcilerDryRun(cfg *configReader) bool {
	return cfg.OptionalBool("capacity_dry_run") || cfg.OptionalBool("capacity_launch_dry_run")
}

// armedCapacityReconcilerOwnsBuffers reports whether an ARMED capacity reconciler (epic st-7zk)
// owns this player's depot buffers: a RUNNING CAPACITY_RECONCILER container for the player whose
// DryRun is FALSE (capacityReconcilerDryRun — capacity_dry_run AND capacity_launch_dry_run both
// false). When TRUE, the depot buffer re-solve stands down (container_ops_depot_launch.go) so the
// depot and the reconciler never both write supported_goods.
//
// FAIL-SAFE toward depot-owns — a query failure must NEVER strand buffers. Every uncertainty
// yields FALSE (the depot keeps its current buffer authority): a nil repo (degraded/test), an
// erroring/unreadable repo, a corrupt config, no reconciler, a DryRun reconciler (the current live
// state), or a not-RUNNING reconciler. The check is scoped to the player via ListByStatus.
func (s *DaemonServer) armedCapacityReconcilerOwnsBuffers(ctx context.Context, playerID int) bool {
	if s.containerRepo == nil {
		return false // repo unreadable (degraded/test) → depot owns
	}
	running, err := s.containerRepo.ListByStatus(ctx, container.ContainerStatusRunning, &playerID)
	if err != nil {
		return false // container query failed → never strand buffers, depot owns
	}
	for _, model := range running {
		if model == nil || model.ContainerType != string(container.ContainerTypeCapacityReconciler) {
			continue
		}
		if capacityReconcilerConfigArmed(model.Config) {
			return true
		}
	}
	return false
}

// capacityReconcilerConfigArmed decodes a persisted reconciler container config (JSON) and reports
// whether it is ARMED — DryRun FALSE per capacityReconcilerDryRun. A config that will not parse
// fails toward NOT-armed (depot owns), so a corrupt row can never stand the depot down.
func capacityReconcilerConfigArmed(configJSON string) bool {
	var values map[string]interface{}
	if err := json.Unmarshal([]byte(configJSON), &values); err != nil {
		return false // unparseable config → treat as not-armed (fail-safe toward depot-owns)
	}
	return !capacityReconcilerDryRun(newConfigReader(values))
}

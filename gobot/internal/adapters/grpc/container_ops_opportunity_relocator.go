package grpc

import (
	"context"
	"fmt"

	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// This file wires the opportunity relocator's launch path + recovery build (sp-zvywu Part 2),
// mirroring container_ops_contract_scaler.go: identity-only launch config → buildCommandForType (the
// single builder shared by creation and recovery) → NewContainer with iterations=-1 for the infinite
// reconcile loop → Add → runner.
//
// LAUNCH CONFIG IS IDENTITY ONLY, by design. Every knob the relocator takes is an OPERATIONAL
// threshold or cap with a documented default (RULINGS #5), and the reconciler resolves 0/absent to
// that default itself — so a bare launch carries no knobs and behaves exactly as the constants
// document. An operator who wants a different cadence, a higher uplift bar, or a stand-down writes the
// key into this container's config column and the rebuild reads it, which is also what makes the
// values survive a restart (RULINGS #2).
//
// THERE IS NO ENABLE FLAG AND NO ARMING SEAM. The relocator is ARMED the moment its container is
// launched; the only stop is reposition_disabled, the SHARED operator kill-switch that halts ALL
// auto-relocation (the margins-death rescue, the rate-floor rescue, and this) so one stand-down is one
// decision rather than three.

// OpportunityRelocatorCoordinator starts the standing opportunity relocator: a recovery-safe container
// that ranks every (trade hull, reachable region) pair by relocation NPV and moves the best-valued
// hulls onto better ground. It NEVER SPENDS — hulls move through the shared travel machinery, no money
// guard is read or relaxed (RULINGS #4) — and never touches the command frigate or a pinned hull
// (RULINGS #7). Idempotent at the caller: launch only when none is already RUNNING/PENDING.
func (s *DaemonServer) OpportunityRelocatorCoordinator(ctx context.Context, playerID int) (string, error) {
	containerID := utils.GenerateContainerID("opportunity_relocator", fmt.Sprintf("player-%d", playerID))

	// Identity only — every threshold and cap resolves to its documented default inside the
	// reconciler, and an operator override lands as a key in this same config column.
	config := map[string]interface{}{
		"container_id": containerID,
	}

	cmd, err := s.buildCommandForType("opportunity_relocator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create opportunity relocator command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeOpportunityRelocator,
		playerID,
		-1,  // Infinite iterations (the reconcile loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "opportunity_relocator"); err != nil {
		return "", fmt.Errorf("failed to persist opportunity relocator container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Opportunity relocator container")

	return containerID, nil
}

// buildOpportunityRelocatorCommand rebuilds the standing relocator command from a persisted launch
// config so a daemon restart re-adopts it (RULINGS #2). container_id is IDENTITY (set once at
// creation, survives the rebuild).
//
// Every other key is OPTIONAL and ABSENT-READS-AS-DEFAULT, which is the whole RULINGS #5 contract
// here: the reconciler's own resolve* helpers turn 0 into the documented constant, so a container
// launched (or recovered) with no knobs runs the calibrated defaults rather than a cap of zero. That is
// why these are OptionalInt with a 0 fallback and NOT PresentOrFailInt — none of them is a money guard
// (the relocator spends nothing), so a 0 is a legitimate "use the default", not a dangerous silent
// floor.
//
// reposition_disabled is the SHARED kill-switch key — the same name the tour coordinator's own
// relocation triggers read (buildTourCoordinatorCommand), so an operator stand-down is expressed one
// way everywhere. Absent reads as false: the relocator is LIVE once launched, and a recovery from a
// config that predates the key stays live (the autosizer/scaler absent-key-is-enabled discipline).
func buildOpportunityRelocatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.RunOpportunityRelocatorCommand{
		PlayerID:    playerID,
		ContainerID: cfg.RequiredNonEmptyString("container_id"),

		// Cadence + economics. 0/absent → the reconciler's documented defaults (900s tick, 500k NPV
		// floor, 1.5x uplift bar — itself clamped so it can only ever be RAISED, 24h horizon, one
		// 60-minute tour of risk margin).
		TickSeconds:           cfg.OptionalInt("relocator_tick_secs", 0),
		NPVThresholdCredits:   int64(cfg.OptionalInt("relocator_npv_threshold_credits", 0)),
		UpliftBarPct:          cfg.OptionalInt("relocator_uplift_bar_pct", 0),
		HorizonHours:          cfg.OptionalInt("relocator_horizon_hours", 0),
		RiskMarginTourMinutes: cfg.OptionalInt("relocator_risk_margin_tour_minutes", 0),

		// Fleet-wide and per-hull rate limiters. 0/absent → 2 concurrent relocations, a 90-minute
		// per-hull cooldown (restart-durable, read off the persisted intents rather than this config).
		MaxConcurrentRelocations: cfg.OptionalInt("relocator_max_concurrent_relocations", 0),
		CooldownMinutes:          cfg.OptionalInt("relocator_cooldown_minutes", 0),

		// Observation shape. 0/absent → the bead's system + 2-hop region radius and a 4-hour EWMA
		// window (roughly 4-8 completed tours per hull).
		RegionHopRadius:   cfg.OptionalInt("relocator_region_hop_radius", 0),
		RateWindowMinutes: cfg.OptionalInt("relocator_rate_window_minutes", 0),

		// SHARED with the reposition paths, deliberately: the anti-herd per-system cap and the
		// stored-adjacency jump bound have ONE definition across all three relocation triggers, so the
		// fleet-spread policy and the routing relaxation cannot drift between them. Same key names the
		// tour container uses.
		RepositionReachMaxHullsPerSystem: cfg.OptionalInt("reposition_reach_max_hulls_per_system", 0),
		JumpBound:                        cfg.OptionalInt("reposition_jump_bound", 0),

		// The one stop. Shared with the tour coordinator's own relocation triggers.
		RepositionDisabled: cfg.OptionalBool("reposition_disabled"),
	}
}

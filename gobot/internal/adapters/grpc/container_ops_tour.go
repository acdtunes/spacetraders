package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// TourRunOperationResult reports the container started for a one-shot guarded trade tour.
type TourRunOperationResult struct {
	ContainerID string
	ShipSymbol  string
}

// StartTourRun launches a captain-directed, guarded multi-hop trade tour (sp-1ek0) as
// a recovery-safe daemon container — arb-run's twin. Unlike arb-run it does not name a
// lane: it asks the depth-aware planner for a tour, flies it leg by leg with prices
// re-verified live at every dock, and re-plans on drift.
//
// iterations (sp-m5kv) makes it CONTINUOUS: -1 = tour, re-plan from the new position,
// tour again until margins die/starvation/stop (engine-cadence capital velocity);
// N>0 = exactly N tours; 0/unset = one tour (the original one-shot). The coordinator
// owns this loop (CoordinatorOwnsIterations), so the container still runs one iteration.
//
// It reuses arb-run's exact start machinery so it inherits the same safety properties:
//
//   - Idle-gap discipline: it refuses any hull that is not genuinely idle BEFORE
//     persisting anything, so a refused start has no side effects and never steals a
//     hull the daemon is actively flying.
//   - Single-writer + release-on-death: the ContainerRunner claims the hull through the
//     normal lifecycle (ship_symbol metadata) and force-releases it on every terminal
//     path, so the hull is never stranded.
//   - Recovery-safe: the row is created RUNNING and "tour_run" is registered in the
//     command factory (sp-7yej invariant 4), so a daemon restart rebuilds the run from
//     its launch config (a cargo-aware re-plan from current state — a persisted -1
//     resumes continuous) or cleanly releases the hull.
//
// max_spend=0 is persisted as-is; the coordinator resolves the 25%-of-treasury default
// at launch (RULINGS #6) with the working-capital floor guarding every buy regardless.
func (s *DaemonServer) StartTourRun(
	ctx context.Context,
	shipSymbol string,
	maxHops int,
	maxSpend int64,
	minMargin int,
	replanLimit int,
	workingCapitalReserve int64,
	workingCapitalReserveTreasuryPct int,
	agentSymbol string,
	iterations int,
	playerID int,
) (*TourRunOperationResult, error) {
	if shipSymbol == "" {
		return nil, fmt.Errorf("ship symbol is required")
	}

	// Idle-gap discipline: only fly a genuinely idle hull, never steal one mid-task.
	ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return nil, fmt.Errorf("ship %s not found", shipSymbol)
	}
	if !ship.IsIdle() {
		return nil, fmt.Errorf("ship %s is not idle (assigned to %q) - tour-run only takes idle-gap hulls", shipSymbol, ship.ContainerID())
	}

	containerID := utils.GenerateContainerID("tour-run", shipSymbol)
	config := map[string]interface{}{
		"ship_symbol":             shipSymbol,
		"container_id":            containerID,
		"agent_symbol":            agentSymbol,
		"max_hops":                maxHops,
		"max_spend":               maxSpend,
		"min_margin":              minMargin,
		"replan_limit":            replanLimit,
		"working_capital_reserve": workingCapitalReserve,
		// sp-yqx4: the counter-cyclical floor percent. Persisted as-is (0 too, so an absent
		// override survives a recovery rebuild unchanged); buildTourCoordinatorCommand's
		// resolveReserveTreasuryPct resolves 0/absent → the 40% default BEFORE the command
		// reaches the handler, so every tour — daemon relaunch, CLI, or recovery — carries a
		// resolved (never-zero) pct by the time Handle()'s `WorkingCapitalReserveTreasuryPct
		// > 0` ctx-stamp gate runs, which is therefore always true in production. It reads
		// false only for a command built directly (bypassing this registry), which is how
		// the sp-agzj/sp-ggk2 absolute-floor-only test suites keep asserting pre-yqx4 behavior.
		"working_capital_reserve_treasury_pct": workingCapitalReserveTreasuryPct,
		// sp-686e: the stranded-hull detector threshold, sourced from the daemon's live
		// [trade_fleet] config (not a per-call param — it is a daemon-global tuning, the same
		// for every tour a captain or the trade-fleet coordinator launches). Persisted as-is
		// (0 too, so an absent knob survives a recovery rebuild unchanged); buildTourCoordinatorCommand
		// passes it to the coordinator, which resolves 0/absent → its default 3.
		"stranded_consecutive_threshold": s.tradeFleetConfig.StrandedConsecutiveThreshold,
		// sp-kl16: the tour-reposition jump bound, sourced from the daemon's live [trade_fleet]
		// config (a daemon-global tuning, same for every tour). Persisted as-is (0 too, so an
		// absent knob survives a recovery rebuild unchanged); buildTourCoordinatorCommand passes
		// it to the coordinator, which resolves 0/absent → its default 12. This is the o34q WRITE
		// side — the scout bug (sp-o34q) was a persist path that DROPPED the bound; writing it into
		// the launch config here, where PersistRepositionState's read-modify-write preserves it and
		// buildTourCoordinatorCommand reads it back, is what makes the bound survive the round-trip.
		"reposition_jump_bound": s.tradeFleetConfig.RepositionJumpBound,
		// sp-syaz: the per-tour distinct-system cap, sourced from the daemon's live
		// [trade_fleet] config (a daemon-global tour tuning, same for every tour — the
		// mirror of reposition_jump_bound/stranded_consecutive_threshold above). Persisted
		// as-is (0 too, so an absent knob survives a recovery rebuild unchanged);
		// buildTourCoordinatorCommand reads it back into cmd.MaxTourSystems, which rides
		// TourConstraints to the solver, resolving 0/absent → the MAX_TOUR_SYSTEMS default
		// (2). This WRITE is what makes the request-driven cap take effect in production —
		// without it the knob is inert and every tour silently clamps to 2.
		"max_tour_systems": s.tradeFleetConfig.MaxTourSystems,
		// sp-im74 config plumbing: the closed-circuit (return-to-anchor) arming flag, sourced
		// from the daemon's live [trade_fleet] config (a daemon-global tour tuning, same for
		// every tour — the mirror of max_tour_systems above). Persisted as-is (false too, so an
		// absent/unarmed knob survives a recovery rebuild unchanged and OPEN mode stays stable);
		// buildTourCoordinatorCommand reads it back into cmd.ClosedTours, which im74 already
		// threads to TourConstraints.Closed and the solver's closed-circuit path, resolving
		// false → OPEN tours (byte-identical to today). This WRITE is what lets the deferred
		// arming knob take effect — without it cmd.ClosedTours is inert and always false.
		"closed_tours": s.tradeFleetConfig.ClosedTours,
		// sp-z7ng: the placement/relocation scoring loop knobs, sourced from the daemon's live
		// [trade_fleet] config (daemon-global tour tuning, same for every tour — the mirror of
		// max_tour_systems/reposition_jump_bound above). Persisted as-is (zeros/false too, so an
		// absent knob survives a recovery rebuild unchanged and the default-OFF dormancy is stable
		// in BOTH directions); buildTourCoordinatorCommand reads them back onto the command via
		// OptionalBool/OptionalInt, which yield the zero values for absent keys — the dormancy the
		// Reposition* knobs already rely on. placement_score_enabled=false keeps the legacy engine.
		"placement_score_enabled":       s.tradeFleetConfig.PlacementScoreEnabled,
		"placement_beta_window_minutes": s.tradeFleetConfig.PlacementBetaWindowMinutes,
		"placement_park_floor_pct":      s.tradeFleetConfig.PlacementParkFloorPct,
		"placement_shortlist_top_n":     s.tradeFleetConfig.PlacementShortlistTopN,
		// sp-uf64: the reposition-reach knobs (always-broaden discovery + deadhead-decay ranking +
		// anti-herd cap), sourced from the daemon's live [trade_fleet] config (daemon-global tour
		// tuning, same for every tour — the mirror of closed_tours/placement_* above). Persisted
		// as-is (false/0 too, so an absent knob survives a recovery rebuild unchanged and the
		// default-OFF dormancy is stable in BOTH directions); buildTourCoordinatorCommand reads them
		// back onto the command via OptionalBool/OptionalInt, which yield the zero values for absent
		// keys. reposition_reach_enabled=false keeps the legacy 1-hop-first reposition. This WRITE is
		// what lets the knob take effect — without it cmd.RepositionReachEnabled is inert and false.
		"reposition_reach_enabled":              s.tradeFleetConfig.RepositionReachEnabled,
		"reposition_reach_hop_decay_pct":        s.tradeFleetConfig.RepositionReachHopDecayPct,
		"reposition_reach_max_hulls_per_system": s.tradeFleetConfig.RepositionReachMaxHullsPerSystem,
		// epic sp-fguo Part 2: the rate-floor early-reposition knobs, sourced from the daemon's live
		// [trade_fleet] config (daemon-global tour tuning, the mirror of reposition_reach_* above).
		// Persisted as-is (false/0 too, so an absent knob survives a recovery rebuild unchanged and
		// the default-OFF dormancy is stable in BOTH directions); buildTourCoordinatorCommand reads
		// them back via OptionalBool/OptionalInt, which yield the zero values for absent keys.
		// reposition_rate_floor_enabled=false keeps the trigger dormant. This WRITE is what lets the
		// knob take effect — without it cmd.RepositionRateFloorEnabled is inert and false.
		"reposition_rate_floor_enabled":         s.tradeFleetConfig.RepositionRateFloorEnabled,
		"reposition_rate_floor_pct":             s.tradeFleetConfig.RepositionRateFloorPct,
		"reposition_rate_floor_improvement_pct": s.tradeFleetConfig.RepositionRateFloorImprovementPct,
		"reposition_rate_floor_dwell_minutes":   s.tradeFleetConfig.RepositionRateFloorDwellMinutes,
		// sp-jsng: the candidate-widening knobs (the #1 fleet-$/hr lever, sp-7q5t), sourced from the
		// daemon's live [trade_fleet] config (daemon-global tour tuning, the mirror of max_tour_systems/
		// closed_tours above). Persisted as-is (0 too, so an absent knob survives a recovery rebuild
		// unchanged and the 1-hop default is stable); buildTourCoordinatorCommand reads them back onto
		// cmd.CandidateHopDepth / cmd.CandidateShortlistTopN, which the widenedTourSystems producer reads
		// (arming-gated by max_tour_systems > 2). candidate_hop_depth 0/absent → the coordinator floors it
		// to 1 (the exact 1-hop set, byte-identical to today). This WRITE is what lets the deferred knob
		// take effect — without it cmd.CandidateHopDepth is inert and every tour stays 1-hop.
		"candidate_hop_depth":       s.tradeFleetConfig.CandidateHopDepth,
		"candidate_shortlist_top_n": s.tradeFleetConfig.CandidateShortlistTopN,
		"iterations":                iterations,
		// sp-sg35: the tour heavies are dedicated to the "trade" fleet
		// (ships.dedicated_fleet == "trade"), so tour_run MUST claim under that
		// same 'trade' identity — otherwise the dedication guard (atomic ClaimShip
		// AND the legacy-path guard) would reject a tour from claiming its OWN
		// hull, killing the entire trade-fleet on the next restart. Same constant
		// and stamping pattern as container_ops_trade.go:95 / container_ops_idle_arb.go:69,
		// persisted in the launch config so BOTH a fresh start and a recovery
		// rebuild claim under operationTrade: a 'trade'-dedicated hull is permitted
		// (operation == dedication) while a foreign-fleet hull is still rejected.
		"operation": operationTrade,
	}

	// Build the tour command through the same factory recovery uses, so the launch
	// config and the recovery rebuild can never drift.
	cmd, err := s.buildCommandForType("tour_run", config, playerID, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create tour-run command: %w", err)
	}

	// The coordinator owns the tour loop (CoordinatorOwnsIterations, sp-m5kv): whether
	// this is one tour or a continuous --iterations -1 run, the container runs Handle()
	// exactly ONCE and the coordinator loops internally, so the container's own
	// iteration budget stays 1 (re-entering it would double-loop the run). The
	// persisted "iterations" config drives the coordinator's loop and survives a
	// recovery rebuild, so a -1 run resumes continuous after a restart.
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTrading,
		playerID,
		1,   // coordinator owns iterations; the runner invokes Handle() once
		nil, // no parent — top-level, recovered independently
		config,
		nil, // default RealClock
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "tour_run"); err != nil {
		return nil, fmt.Errorf("failed to persist tour-run container: %w", err)
	}

	// The runner claims the hull (ship_symbol metadata), flips the row to RUNNING, and
	// owns release-on-death.
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Tour-run container %s failed: %v\n", containerID, err)
		}
	}()

	return &TourRunOperationResult{
		ContainerID: containerID,
		ShipSymbol:  shipSymbol,
	}, nil
}

// TourRepositionConfigPersister backs the tour coordinator's
// tradingCmd.RepositionStatePersister with the container config (sp-zhii). When a
// continuous tour commits a margins-death reposition it merges the in-flight destination
// (reposition_in_progress + reposition_target_system/waypoint) into the SAME persisted
// config the recovery rebuild reads (buildTourCoordinatorCommand), and clears it once the
// jump lands — so a daemon restart mid-jump resumes toward the same ground instead of
// re-planning at whatever intermediate hop it was re-adopted on (RULINGS #2). Like
// ArbCostConfigPersister it is a read-modify-write of the config map guarded to those
// keys, and the config has no other writer during a run, so it never clobbers the
// status/heartbeat columns the runner updates concurrently.
type TourRepositionConfigPersister struct {
	containerRepo *persistence.ContainerRepositoryGORM
}

// NewTourRepositionConfigPersister wires the config-backed reposition-state store for the
// tour coordinator (sp-zhii).
func NewTourRepositionConfigPersister(containerRepo *persistence.ContainerRepositoryGORM) *TourRepositionConfigPersister {
	return &TourRepositionConfigPersister{containerRepo: containerRepo}
}

// PersistRepositionState merges the reposition episode into the container's persisted
// config, preserving every launch knob the rebuild also needs. On InProgress=false it
// writes the cleared state (empty target) so a restart after the jump landed does NOT
// re-resume a completed reposition. A missing container row (already terminalized) is an
// error the caller logs and swallows: this is resume durability, never a movement guard.
func (p *TourRepositionConfigPersister) PersistRepositionState(ctx context.Context, containerID string, playerID int, ep tradingCmd.RepositionEpisode) error {
	model, err := p.containerRepo.Get(ctx, containerID, playerID)
	if err != nil {
		return fmt.Errorf("load container %s to persist reposition state: %w", containerID, err)
	}
	if model == nil {
		return fmt.Errorf("container %s not found - cannot persist reposition state", containerID)
	}

	config := map[string]interface{}{}
	if model.Config != "" {
		if uerr := json.Unmarshal([]byte(model.Config), &config); uerr != nil {
			return fmt.Errorf("deserialize container %s config to persist reposition state: %w", containerID, uerr)
		}
	}
	config["reposition_in_progress"] = ep.InProgress
	config["reposition_target_system"] = ep.TargetSystem
	config["reposition_target_waypoint"] = ep.TargetWaypoint

	merged, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("serialize container %s config after merging reposition state: %w", containerID, err)
	}
	return p.containerRepo.UpdateContainerConfig(ctx, containerID, playerID, string(merged))
}

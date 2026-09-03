package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// TourRunOperationResult reports the container started for a one-shot guarded trade tour.
type TourRunOperationResult struct {
	ContainerID string
	ShipSymbol  string
}

// TourRunOverrides carries per-launch overrides the trade-fleet coordinator applies to a
// SPECIFIC relaunch that the daemon-global [trade_fleet] config does not (sp-nxrt). nil =
// no override (byte-identical to a config-only launch), so the captain CLI / gRPC path and
// every recovery rebuild are unchanged.
type TourRunOverrides struct {
	// RepositionReachEnabled arms reposition-reach for THIS launch even when the global
	// reposition_reach_enabled is off — the sp-nxrt fast-fail escalation: a hull that
	// fast-failed twice at its current ground is relaunched to REACH a fresh system it
	// could not see over the default 1-hop scan, instead of the coordinator sleeping
	// longer. It only ever ARMS reach (never disarms), so it cannot fight a captain who
	// globally enabled it.
	RepositionReachEnabled bool

	// MVTLoop selects the MVT trade loop for THIS launch, set by the fleet coordinator from
	// the hull's trade-mvt tag; false is the legacy tour path, so rollback is one tag change.
	MVTLoop bool
}

// applyTourRunOverrides layers a launch's per-launch overrides onto the freshly-built tour
// config map (sp-nxrt), just before the command is built — so the persisted launch config
// (and therefore a recovery rebuild) carries the override, and the registry's OptionalBool
// read of reposition_reach_enabled picks it up with no daemon-global flip. nil is a no-op;
// an override only ever UPGRADES reposition_reach_enabled to true (never downgrades), so a
// non-escalated launch on a reach-on fleet is untouched.
func applyTourRunOverrides(config map[string]interface{}, overrides *TourRunOverrides) {
	if overrides == nil {
		return
	}
	if overrides.RepositionReachEnabled {
		config["reposition_reach_enabled"] = true
	}
	// Written only when selected: an absent key reads as false, the legacy tour path.
	if overrides.MVTLoop {
		config["mvt_loop"] = true
	}
}

// StartTourRun launches a captain-directed, guarded multi-hop trade tour (sp-1ek0) as
// a recovery-safe daemon container — arb-run's twin. Unlike arb-run it does not name a
// lane: it asks the depth-aware planner for a tour, flies it leg by leg with prices
// re-verified live at every dock, and re-plans on drift.
//
// iterations makes it CONTINUOUS: -1 = tour, re-plan from the new position,
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
	agentSymbol string,
	iterations int,
	playerID int,
	overrides *TourRunOverrides,
) (*TourRunOperationResult, error) {
	if shipSymbol == "" {
		return nil, fmt.Errorf("ship symbol is required")
	}

	if err := s.requireIdleHull(ctx, shipSymbol, playerID, "tour-run"); err != nil {
		return nil, err
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
		"iterations":              iterations,
		// Tour heavies are dedicated_fleet=="trade", so tour_run MUST claim under that same
		// identity or the dedication guard rejects a tour claiming its OWN hull.
		"operation": operationTrade,
	}
	s.addTradeFleetTourKnobs(config)

	// Overrides must land BEFORE the command is built so they ride the same persisted
	// launch config the recovery rebuild reads.
	applyTourRunOverrides(config, overrides)

	// Build the tour command through the same factory recovery uses, so the launch
	// config and the recovery rebuild can never drift.
	cmd, err := s.buildCommandForType("tour_run", config, playerID, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create tour-run command: %w", err)
	}

	// The coordinator owns the tour loop, so the container's own iteration budget stays 1
	// — re-entering it would double-loop the run. The persisted "iterations" drives the loop.
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
	s.startContainerRunner(containerEntity, cmd, containerID, "Tour-run container")

	return &TourRunOperationResult{
		ContainerID: containerID,
		ShipSymbol:  shipSymbol,
	}, nil
}

// addTradeFleetTourKnobs stamps the daemon-global [trade_fleet] tunings into a launch config.
// Persisted as-is including zero/false, so an absent knob survives a recovery rebuild.
func (s *DaemonServer) addTradeFleetTourKnobs(config map[string]interface{}) {
	config["stranded_consecutive_threshold"] = s.tradeFleetConfig.StrandedConsecutiveThreshold
	config["reposition_jump_bound"] = s.tradeFleetConfig.RepositionJumpBound
	config["max_tour_systems"] = s.tradeFleetConfig.MaxTourSystems
	config["closed_tours"] = s.tradeFleetConfig.ClosedTours

	config["placement_disabled"] = s.tradeFleetConfig.PlacementDisabled
	config["placement_beta_window_minutes"] = s.tradeFleetConfig.PlacementBetaWindowMinutes
	config["placement_park_floor_pct"] = s.tradeFleetConfig.PlacementParkFloorPct
	config["placement_shortlist_top_n"] = s.tradeFleetConfig.PlacementShortlistTopN
	config["placement_horizon_minutes"] = s.tradeFleetConfig.PlacementHorizonMinutes

	config["reposition_reach_enabled"] = s.tradeFleetConfig.RepositionReachEnabled
	config["reposition_reach_hop_decay_pct"] = s.tradeFleetConfig.RepositionReachHopDecayPct
	config["reposition_reach_max_hulls_per_system"] = s.tradeFleetConfig.RepositionReachMaxHullsPerSystem

	config["reposition_own_trade_penalty_pct"] = s.tradeFleetConfig.OwnTradePenaltyPct
	config["reposition_own_trade_cold_minutes"] = s.tradeFleetConfig.OwnTradeColdMinutes
	config["reposition_own_trade_penalty_disabled"] = s.tradeFleetConfig.OwnTradePenaltyDisabled

	config["reposition_rate_floor_enabled"] = s.tradeFleetConfig.RepositionRateFloorEnabled
	config["reposition_rate_floor_pct"] = s.tradeFleetConfig.RepositionRateFloorPct
	config["reposition_rate_floor_improvement_pct"] = s.tradeFleetConfig.RepositionRateFloorImprovementPct
	config["reposition_rate_floor_dwell_minutes"] = s.tradeFleetConfig.RepositionRateFloorDwellMinutes

	// candidate_hop_depth is arming-gated by max_tour_systems > 2; 0/absent floors to the
	// exact 1-hop set in the coordinator.
	config["candidate_hop_depth"] = s.tradeFleetConfig.CandidateHopDepth
	config["candidate_shortlist_top_n"] = s.tradeFleetConfig.CandidateShortlistTopN

	config["externality_weight"] = s.tradeFleetConfig.ExternalityWeight
	config["tour_neighbors_durable_first"] = s.tradeFleetConfig.TourNeighborsDurableFirst

	// MVT trade loop knobs, written only when set so an absent key defers to the coordinator's
	// spec default rather than pinning a 0. The loop is armed per hull by mvt_loop, never by these.
	if v := s.tradeFleetConfig.YieldWindowSells; v > 0 {
		config["yield_window_sells"] = v
	}
	if v := s.tradeFleetConfig.YieldMinSells; v > 0 {
		config["yield_min_sells"] = v
	}
	if v := s.tradeFleetConfig.ClaimReachHops; v > 0 {
		config["claim_reach_hops"] = v
	}
	if v := s.tradeFleetConfig.ClaimReachMaxHops; v > 0 {
		config["claim_reach_max_hops"] = v
	}
	if v := s.tradeFleetConfig.RankerMinSpreadPerUnit; v > 0 {
		config["ranker_min_spread_per_unit"] = v
	}
	if v := s.tradeFleetConfig.SpecialistCadenceMinutes; v > 0 {
		config["specialist_cadence_minutes"] = v
	}
	if v := s.tradeFleetConfig.YieldRateSpanFloorMinutes; v > 0 {
		config["yield_rate_span_floor_minutes"] = v
	}
	if v := s.tradeFleetConfig.MVTRescueJumpsPerEpisode; v > 0 {
		config["mvt_rescue_jumps_per_episode"] = v
	}
	if v := s.tradeFleetConfig.MVTJumpFeeMaxSharePct; v > 0 {
		config["mvt_jump_fee_max_share_pct"] = v
	}
	if v := s.tradeFleetConfig.MVTRecentlyLeftMinutes; v > 0 {
		config["mvt_recently_left_minutes"] = v
	}
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
// PersistRelocationOffer merges the sp-e8d92 relocation OFFER into the tour container's own config —
// the SAME map the relocator already reads to decide whether a hull is on tour, so the read side costs
// no extra query and makes no API call.
//
// Absolute RFC 3339 instants, never durations: a restart must not silently extend a hold, and a relative
// value would do exactly that. A ZERO OfferedUntil writes the empty string, which is how an offer is
// CLEARED — the waiter treats an absent deadline as no offer, so clearing degrades to today's behaviour
// rather than to a hull held forever.
//
// Guarded to its two keys, like its sibling above, so it never clobbers a launch knob the recovery
// rebuild reads. A returned error is advisory: the offer is an optimisation, never a movement guard, so
// the caller logs it and keeps touring.
func (p *TourRepositionConfigPersister) PersistRelocationOffer(ctx context.Context, containerID string, playerID int, offer tradingCmd.RelocationOffer) error {
	model, err := p.containerRepo.Get(ctx, containerID, playerID)
	if err != nil {
		return fmt.Errorf("load container %s to persist the relocation offer: %w", containerID, err)
	}
	if model == nil {
		return fmt.Errorf("container %s not found - cannot persist the relocation offer", containerID)
	}
	config := map[string]interface{}{}
	if model.Config != "" {
		if uerr := json.Unmarshal([]byte(model.Config), &config); uerr != nil {
			return fmt.Errorf("deserialize container %s config to persist the relocation offer: %w", containerID, uerr)
		}
	}
	config[relocationOfferUntilConfigKey] = formatRelocationOfferInstant(offer.OfferedUntil)
	config[relocationOfferBackoffConfigKey] = formatRelocationOfferInstant(offer.BackoffUntil)

	merged, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("serialize container %s config after merging the relocation offer: %w", containerID, err)
	}
	return p.containerRepo.UpdateContainerConfig(ctx, containerID, playerID, string(merged))
}

// formatRelocationOfferInstant renders a deadline for the config, or "" for a zero time so an absent
// offer reads as absent rather than as the year 1.
func formatRelocationOfferInstant(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

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

// Named once so this WRITE side and buildTourCoordinatorCommand's READ side cannot drift apart.
const (
	tourLegWaypointConfigKey = "tour_leg_waypoint"
	tourLegGoodsConfigKey    = "tour_leg_goods"
)

// PersistTourLegState merges the SELL leg a hull is currently flying — its sink waypoint and
// the goods carried there — into the container's persisted config, so a restart mid-leg
// finishes that discharge instead of making a laden hull wait out a re-plan first (RULINGS #2).
//
// A ZERO state writes empty strings, which is how the leg is CLEARED: an absent waypoint reads
// as "no leg in flight", so clearing degrades to today's behaviour rather than to a hull
// re-flying a leg it already flew. Guarded to its two keys like its siblings above; a returned
// error is advisory — resume durability, never a spend or movement guard.
func (p *TourRepositionConfigPersister) PersistTourLegState(ctx context.Context, containerID string, playerID int, state tradingCmd.TourLegState) error {
	model, err := p.containerRepo.Get(ctx, containerID, playerID)
	if err != nil {
		return fmt.Errorf("load container %s to persist the in-flight tour leg: %w", containerID, err)
	}
	if model == nil {
		return fmt.Errorf("container %s not found - cannot persist the in-flight tour leg", containerID)
	}

	config := map[string]interface{}{}
	if model.Config != "" {
		if uerr := json.Unmarshal([]byte(model.Config), &config); uerr != nil {
			return fmt.Errorf("deserialize container %s config to persist the in-flight tour leg: %w", containerID, uerr)
		}
	}
	config[tourLegWaypointConfigKey] = state.Waypoint
	config[tourLegGoodsConfigKey] = state.Goods

	merged, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("serialize container %s config after merging the in-flight tour leg: %w", containerID, err)
	}
	return p.containerRepo.UpdateContainerConfig(ctx, containerID, playerID, string(merged))
}

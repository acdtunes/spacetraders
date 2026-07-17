package grpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// dedicationGuardShipRepo enforces ClaimShip's real fleet-dedication rule (permit
// iff operation == dedicated_fleet, or the hull is undedicated) so a test can
// prove an operation-keyed claim under the matching fleet identity is PERMITTED on
// a same-fleet-dedicated hull and REJECTED on a foreign one. FindBySymbol + Save
// back the legacy branch; ClaimShip backs the operation-keyed branch the tour now
// takes (sp-sg35).
type dedicationGuardShipRepo struct {
	navigation.ShipRepository
	ship *navigation.Ship
}

func (r *dedicationGuardShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

func (r *dedicationGuardShipRepo) ClaimShip(_ context.Context, symbol, containerID string, _ shared.PlayerID, operation string) error {
	if r.ship.DedicatedFleet() != "" && r.ship.DedicatedFleet() != operation {
		return shared.NewShipDedicatedToOtherFleetError(symbol, r.ship.DedicatedFleet(), operation)
	}
	return r.ship.AssignToContainer(containerID, shared.NewRealClock())
}

func (r *dedicationGuardShipRepo) Save(_ context.Context, _ *navigation.Ship) error { return nil }

func (r *dedicationGuardShipRepo) FindByContainer(_ context.Context, _ string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return nil, nil
}

// sp-sg35 THE PREREQUISITE — this is the test that would have caught the
// fleet-killer. The tour heavies (TORWIND-19/2B/2C/2D/2E/37/39/42) are dedicated
// to the "trade" fleet, and tour_run claims through the ContainerRunner. If the
// launch config does not stamp the "trade" operation, the dedication guard (both
// the atomic ClaimShip and the sp-sg35 legacy-path guard) rejects a tour from
// claiming its OWN hull. StartTourRun must persist "operation":"trade" so both a
// fresh start and a recovery rebuild claim under operation == dedication.
func TestStartTourRun_StampsTradeOperationInLaunchConfig(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	// TORWIND-19 is a real tour heavy, dedicated to the "trade" fleet.
	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	result, err := s.StartTourRun(context.Background(), "TORWIND-19", 5, int64(100000), 10, 3, int64(0), 0 /* reserve treasury pct */, "AGENT", 1, playerID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "TORWIND-19", result.ShipSymbol)

	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner, "a live runner must own the tour (release-on-death)")
	defer runner.cancelFunc()

	// The persisted row must be rebuildable by recovery under the trade identity.
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Equal(t, "tour_run", model.CommandType)
	// The fleet identity MUST ride the launch config as 'trade' (== the DB
	// dedication value) so a 'trade'-dedicated tour heavy is permitted, not rejected.
	require.Contains(t, model.Config, `"operation":"trade"`)
}

// sp-kl16 THE O34Q WRITE-SIDE PIN. The tour-reposition jump bound is daemon-global tuning
// ([trade_fleet].reposition_jump_bound), so StartTourRun must STAMP it into the launch config —
// the write half of the persist boundary the scout relay's bug (sp-o34q) dropped. Without this
// write, buildTourCoordinatorCommand would read 0 on every rebuild and the reposition would
// silently degrade to the strict resolver, exactly the C1-blocking failure sp-kl16 fixes. Paired
// with TestTourRepositionJumpBound_RoundTripsThroughRebuildAcrossPersist (the read+merge half).
func TestStartTourRun_StampsRepositionJumpBoundFromTradeFleetConfig(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.tradeFleetConfig.RepositionJumpBound = 9 // the captain's [trade_fleet] override

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	result, err := s.StartTourRun(context.Background(), "TORWIND-19", 5, int64(100000), 10, 3, int64(0), 0, "AGENT", 1, playerID)
	require.NoError(t, err)
	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner)
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Contains(t, model.Config, `"reposition_jump_bound":9`, "StartTourRun must stamp the [trade_fleet] reposition jump bound so the recovery rebuild reads it back (the o34q write side)")

	// And the whole boundary round-trips: the rebuild reloads the stamped bound.
	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))
	rebuilt, err := s.buildCommandForType("tour_run", cfg, playerID, result.ContainerID)
	require.NoError(t, err)
	require.Equal(t, 9, rebuilt.(*tradingCmd.RunTourCoordinatorCommand).RepositionJumpBound)
}

// sp-syaz THE CONFIG-KNOB PROPAGATION PIN (review major). max_tour_systems is a daemon-global
// tour tuning ([trade_fleet].max_tour_systems, the per-tour distinct-system cap), so — exactly
// like the sp-kl16 reposition jump bound and the sp-686e stranded threshold above — StartTourRun
// must STAMP it into the launch config from tradeFleetConfig. Without this write,
// buildTourCoordinatorCommand reads 0 on every launch AND rebuild and the request-driven cap is
// INERT in production (the review finding): the operator's config.yaml value never reaches the
// solver, which silently stays at its MAX_TOUR_SYSTEMS default (2).
func TestStartTourRun_StampsMaxTourSystemsFromTradeFleetConfig(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.tradeFleetConfig.MaxTourSystems = 4 // the captain's [trade_fleet] override

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	result, err := s.StartTourRun(context.Background(), "TORWIND-19", 5, int64(100000), 10, 3, int64(0), 0, "AGENT", 1, playerID)
	require.NoError(t, err)
	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner)
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Contains(t, model.Config, `"max_tour_systems":4`, "StartTourRun must stamp the [trade_fleet] max_tour_systems so the launch/rebuild reads it back — otherwise the request-driven cap is inert in production")

	// The whole boundary round-trips: the rebuild reloads the stamped cap into cmd.MaxTourSystems.
	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))
	rebuilt, err := s.buildCommandForType("tour_run", cfg, playerID, result.ContainerID)
	require.NoError(t, err)
	require.Equal(t, 4, rebuilt.(*tradingCmd.RunTourCoordinatorCommand).MaxTourSystems)
}

// sp-syaz default-safety companion: an UNSET [trade_fleet].max_tour_systems (0) rebuilds to
// cmd.MaxTourSystems 0 — which the solver reads as its MAX_TOUR_SYSTEMS default (2), so a daemon
// that never sets the knob is byte-identical to today (the launch path stays default-safe).
func TestStartTourRun_MaxTourSystemsDefaultsZeroWhenUnset(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	// tradeFleetConfig.MaxTourSystems left at its zero value (the operator never set it).

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	result, err := s.StartTourRun(context.Background(), "TORWIND-19", 5, int64(100000), 10, 3, int64(0), 0, "AGENT", 1, playerID)
	require.NoError(t, err)
	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner)
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))
	rebuilt, err := s.buildCommandForType("tour_run", cfg, playerID, result.ContainerID)
	require.NoError(t, err)
	require.Equal(t, 0, rebuilt.(*tradingCmd.RunTourCoordinatorCommand).MaxTourSystems)
}

// sp-z7ng RED#18 — the placement-knob threading pin. The four placement/relocation loop knobs are
// daemon-global [trade_fleet] tunings (exactly like max_tour_systems / reposition_jump_bound), so
// StartTourRun must STAMP them into the launch config and buildTourCoordinatorCommand must READ them
// back onto the command. Set knobs round-trip onto the command; an ABSENT knob rebuilds to the
// zero/false value (dormant) — the default-OFF safety the whole child rides on.
func TestTourContainerConfig_ThreadsPlacementKnobs(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.tradeFleetConfig.PlacementScoreEnabled = true // the captain's [trade_fleet] arming
	s.tradeFleetConfig.PlacementBetaWindowMinutes = 45
	s.tradeFleetConfig.PlacementParkFloorPct = 25
	s.tradeFleetConfig.PlacementShortlistTopN = 5

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	result, err := s.StartTourRun(context.Background(), "TORWIND-19", 5, int64(100000), 10, 3, int64(0), 0, "AGENT", 1, playerID)
	require.NoError(t, err)
	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner)
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Contains(t, model.Config, `"placement_score_enabled":true`, "StartTourRun must stamp placement_score_enabled so the rebuild reads it back (the arming write side)")
	require.Contains(t, model.Config, `"placement_beta_window_minutes":45`)
	require.Contains(t, model.Config, `"placement_park_floor_pct":25`)
	require.Contains(t, model.Config, `"placement_shortlist_top_n":5`)

	// The whole boundary round-trips onto the command.
	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))
	rebuilt, err := s.buildCommandForType("tour_run", cfg, playerID, result.ContainerID)
	require.NoError(t, err)
	cmd := rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	require.True(t, cmd.PlacementScoreEnabled)
	require.Equal(t, 45, cmd.PlacementBetaWindowMinutes)
	require.Equal(t, 25, cmd.PlacementParkFloorPct)
	require.Equal(t, 5, cmd.PlacementShortlistTopN)
}

// sp-z7ng default-safety companion: an UNSET [trade_fleet] placement block rebuilds to a DORMANT
// command — placement_score_enabled false, every int 0 — so every existing container and recovery
// rebuild takes the legacy reposition branch. This is the launch-path half of the default-off proof.
func TestTourContainerConfig_PlacementKnobsDefaultDormantWhenUnset(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	// tradeFleetConfig placement block left at its zero values (the operator never armed it).

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	result, err := s.StartTourRun(context.Background(), "TORWIND-19", 5, int64(100000), 10, 3, int64(0), 0, "AGENT", 1, playerID)
	require.NoError(t, err)
	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner)
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))
	rebuilt, err := s.buildCommandForType("tour_run", cfg, playerID, result.ContainerID)
	require.NoError(t, err)
	cmd := rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	require.False(t, cmd.PlacementScoreEnabled, "an unset placement block must rebuild DORMANT (legacy reposition runs)")
	require.Equal(t, 0, cmd.PlacementBetaWindowMinutes)
	require.Equal(t, 0, cmd.PlacementParkFloorPct)
	require.Equal(t, 0, cmd.PlacementShortlistTopN)
}

// closed_tours THE CONFIG-KNOB PROPAGATION PIN. im74 wired cmd.ClosedTours -> cons.Closed ->
// the OR-Tools closed-circuit solver but deferred the config knob that SETS it, so
// cmd.ClosedTours was always false and closed mode could never be armed. closed_tours is a
// daemon-global [trade_fleet] tuning (exactly like max_tour_systems / reposition_jump_bound),
// so — mirroring TestStartTourRun_StampsMaxTourSystemsFromTradeFleetConfig — StartTourRun must
// STAMP it into the launch config from tradeFleetConfig and buildTourCoordinatorCommand must
// READ it back onto cmd.ClosedTours. Without both writes the knob is INERT: the operator's
// config.yaml value never reaches the solver and every tour stays OPEN.
func TestStartTourRun_StampsClosedToursFromTradeFleetConfig(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.tradeFleetConfig.ClosedTours = true // the captain's [trade_fleet] arming

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	result, err := s.StartTourRun(context.Background(), "TORWIND-19", 5, int64(100000), 10, 3, int64(0), 0, "AGENT", 1, playerID)
	require.NoError(t, err)
	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner)
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Contains(t, model.Config, `"closed_tours":true`, "StartTourRun must stamp the [trade_fleet] closed_tours so the launch/rebuild reads it back — otherwise the arming knob is inert and closed mode can never be turned on")

	// The whole boundary round-trips: the rebuild reloads the stamped flag into cmd.ClosedTours,
	// which im74 already threads to cons.Closed and the closed-circuit solver.
	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))
	rebuilt, err := s.buildCommandForType("tour_run", cfg, playerID, result.ContainerID)
	require.NoError(t, err)
	require.True(t, rebuilt.(*tradingCmd.RunTourCoordinatorCommand).ClosedTours)
}

// closed_tours default-safety companion: an UNSET [trade_fleet].closed_tours (false) rebuilds
// to cmd.ClosedTours false — which im74's cons.Closed path reads as an OPEN tour, so a daemon
// that never arms the knob is byte-identical to today (the launch path stays default-safe/OPEN).
func TestStartTourRun_ClosedToursDefaultsFalseWhenUnset(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	// tradeFleetConfig.ClosedTours left at its zero value (the operator never armed it).

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	result, err := s.StartTourRun(context.Background(), "TORWIND-19", 5, int64(100000), 10, 3, int64(0), 0, "AGENT", 1, playerID)
	require.NoError(t, err)
	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner)
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))
	rebuilt, err := s.buildCommandForType("tour_run", cfg, playerID, result.ContainerID)
	require.NoError(t, err)
	require.False(t, rebuilt.(*tradingCmd.RunTourCoordinatorCommand).ClosedTours, "an unset closed_tours must rebuild to cmd.ClosedTours=false (OPEN tours, byte-identical to today)")
}

// sp-sg35 — the permit half: a tour_run-shaped container (operation "trade", the
// value StartTourRun now stamps) claiming a hull dedicated to the SAME "trade"
// fleet is permitted (operation == dedication), so the trade-fleet tours keep
// flying their own hulls.
func TestTourClaimPermittedOnMatchingTradeDedication(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &dedicationGuardShipRepo{ship: hull}

	const containerID = "tour-run-TORWIND-19"
	entity := container.NewContainer(containerID, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-19", "operation": operationTrade}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "tour_run"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	defer runner.cancelFunc()

	err := runner.Start()

	require.NoError(t, err, "a tour claiming its OWN 'trade'-dedicated hull under operation=trade must be permitted")
	requireContainerState(t, db, containerID, "RUNNING", "")
	require.True(t, hull.IsAssigned())
	require.Equal(t, containerID, hull.ContainerID())
}

// sp-sg35 — the reject half: even with the trade identity, a tour must NOT claim a
// hull dedicated to a FOREIGN fleet (here "contract"); the guard still rejects it,
// so the stamp is a scoped permission for the tour's own fleet, not a skeleton key.
func TestTourClaimRejectedOnForeignFleetDedication(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	hull := newIdleTradeShip(t, "SHIP-CONTRACT", playerID)
	hull.SetDedicatedFleet("contract")
	s.shipRepo = &dedicationGuardShipRepo{ship: hull}

	const containerID = "tour-run-SHIP-CONTRACT"
	entity := container.NewContainer(containerID, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-CONTRACT", "operation": operationTrade}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "tour_run"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, s.clock)

	err := runner.Start()

	require.Error(t, err, "a trade tour must not claim a foreign 'contract'-dedicated hull")
	var dedErr *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, err, &dedErr)
	requireContainerState(t, db, containerID, "FAILED", "claim_failed")
	require.False(t, hull.IsAssigned())
}

// reposition_reach THE CONFIG-KNOB PROPAGATION PIN (sp-uf64). The reach discovery/ranking/anti-herd
// path is a daemon-global [trade_fleet] tuning (exactly like closed_tours / placement_*), so —
// mirroring TestStartTourRun_StampsClosedToursFromTradeFleetConfig — StartTourRun must STAMP the
// flag + the two int knobs into the launch config from tradeFleetConfig and buildTourCoordinatorCommand
// must READ them back onto the command. Without both writes the knob is INERT: the operator's
// config.yaml value never reaches buildRepositionCandidates and every reposition stays 1-hop-first.
func TestStartTourRun_StampsRepositionReachFromTradeFleetConfig(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.tradeFleetConfig.RepositionReachEnabled = true // the captain's [trade_fleet] arming
	s.tradeFleetConfig.RepositionReachHopDecayPct = 70
	s.tradeFleetConfig.RepositionReachMaxHullsPerSystem = 3

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	result, err := s.StartTourRun(context.Background(), "TORWIND-19", 5, int64(100000), 10, 3, int64(0), 0, "AGENT", 1, playerID)
	require.NoError(t, err)
	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner)
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Contains(t, model.Config, `"reposition_reach_enabled":true`, "StartTourRun must stamp the [trade_fleet] reposition_reach_enabled so the launch/rebuild reads it back — otherwise the reach path can never be armed")

	// The whole boundary round-trips: the rebuild reloads the stamped knobs onto the command, which
	// buildRepositionCandidates reads to always-broaden + deadhead-decay + anti-herd.
	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))
	rebuilt, err := s.buildCommandForType("tour_run", cfg, playerID, result.ContainerID)
	require.NoError(t, err)
	cmd := rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	require.True(t, cmd.RepositionReachEnabled)
	require.Equal(t, 70, cmd.RepositionReachHopDecayPct)
	require.Equal(t, 3, cmd.RepositionReachMaxHullsPerSystem)
}

// reposition_reach default-safety companion (sp-uf64): an UNSET [trade_fleet] reposition_reach block
// rebuilds to RepositionReachEnabled=false + the two int knobs at 0 — which buildRepositionCandidates
// reads as the legacy 1-hop-first reposition, so a daemon that never arms the knobs is byte-identical
// to today (the launch path stays default-safe).
func TestStartTourRun_RepositionReachDefaultsWhenUnset(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	// tradeFleetConfig reposition-reach fields left at their zero values (the operator never armed them).

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	result, err := s.StartTourRun(context.Background(), "TORWIND-19", 5, int64(100000), 10, 3, int64(0), 0, "AGENT", 1, playerID)
	require.NoError(t, err)
	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner)
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))
	rebuilt, err := s.buildCommandForType("tour_run", cfg, playerID, result.ContainerID)
	require.NoError(t, err)
	cmd := rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	require.False(t, cmd.RepositionReachEnabled, "an unset reposition_reach_enabled must rebuild to false (legacy 1-hop reposition, byte-identical to today)")
	require.Equal(t, 0, cmd.RepositionReachHopDecayPct, "an unset hop_decay_pct must rebuild to the sentinel 0 (consumer resolves -> 85)")
	require.Equal(t, 0, cmd.RepositionReachMaxHullsPerSystem, "an unset max_hulls_per_system must rebuild to the sentinel 0 (consumer resolves -> 5)")
}

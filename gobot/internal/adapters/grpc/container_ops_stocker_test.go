package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Idle-gap discipline (RULINGS #7): the stocker dedicates a hull, so it must refuse a hull
// the daemon is already flying BEFORE persisting anything — a refused start has no side
// effects and never poaches another coordinator's ship. Mirrors StartArbRun/StartWarehouse.
func TestStartStocker_RefusesNonIdleShip(t *testing.T) {
	ship := newIdleTradeShip(t, "STK-BUSY", 1)
	require.NoError(t, ship.AssignToContainer("tour_run-OTHER", shared.NewRealClock()))

	s := &DaemonServer{
		shipRepo:       &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"STK-BUSY": ship}},
		containers:     make(map[string]*ContainerRunner),
		containerSpecs: make(map[string]ContainerSpec),
	}
	s.registerContainerSpecs()

	result, err := s.StartStocker(context.Background(), "STK-BUSY", "X1-HOME-A1", 0, 0, -1, 0, 0, false, 0, 0, false, "ENDURANCE", 1)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "not idle")
	require.Empty(t, s.containers, "a refused start registers no container")
	require.Equal(t, "tour_run-OTHER", ship.ContainerID(), "the other coordinator's claim is untouched")
}

// An idle hull must produce a recovery-visible stocker container: persisted with the
// "stocker" command_type, "TRADING" container_type, and a config carrying
// ship_symbol/warehouse_waypoint/iterations so restart recovery can rebuild it (RULINGS #2),
// plus the operation="stocker" fleet identity so both the fresh start and a recovery rebuild
// claim the hull under the durable stocker dedication (sp-m92a) — the claim that lets the
// stocker (and only the stocker) take its own 'stocker'-dedicated hull.
func TestStartStocker_IdleShip_PersistsRecoveryVisibleContainer(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ship := newIdleTradeShip(t, "STK-1", playerID)
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"STK-1": ship}}

	result, err := s.StartStocker(context.Background(), "STK-1", "X1-HOME-A1", 200000, 60000, -1, 60, 120, false, 0, 0, false, "ENDURANCE", playerID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.ContainerID)
	require.Equal(t, "STK-1", result.ShipSymbol)
	require.Equal(t, "X1-HOME-A1", result.WarehouseWaypoint)

	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner, "a live runner must own the stocker (release-on-death)")
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Equal(t, "stocker", model.CommandType)
	require.Equal(t, "TRADING", model.ContainerType)
	require.Contains(t, model.Config, "STK-1")
	require.Contains(t, model.Config, "X1-HOME-A1")
	require.Contains(t, model.Config, "warehouse_waypoint")
	require.Contains(t, model.Config, "iterations")
	require.Contains(t, model.Config, `"operation":"stocker"`)
}

// sp-k1ka: a STANDING launch must PERSIST the standing intent (+ cadence/hysteresis) in the
// container config so a daemon restart RE-ADOPTS the stocker STANDING (RULINGS #2) — recovery
// rebuilds the command from this config, resuming the park-and-re-stage loop with no manual
// relaunch. This pins the persistence half; the rebuild half is the command-factory pin test.
func TestStartStocker_Standing_PersistsStandingIntentForRestart(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ship := newIdleTradeShip(t, "STK-STD", playerID)
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"STK-STD": ship}}

	result, err := s.StartStocker(context.Background(), "STK-STD", "X1-HOME-A1", 0, 0, -1, 0, 0, true, 45, 8, false, "ENDURANCE", playerID)
	require.NoError(t, err)
	require.NotNil(t, result)

	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner)
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Contains(t, model.Config, `"standing":true`, "the standing intent must be persisted for restart re-adoption")
	require.Contains(t, model.Config, `"tick_seconds":45`)
	require.Contains(t, model.Config, `"refill_hysteresis":8`)
}

// sp-k2xav: the intra-system sourcing intent (home_system_only) must PERSIST in the container
// config so a daemon restart RE-ADOPTS the depot stocker home-system-scoped (RULINGS #2/#14) —
// recovery rebuilds the command from this config; without the persisted flag a restart would
// silently revert the depot to cross-system sourcing. Pairs with the command-factory rebuild pin.
func TestStartStocker_HomeSystemOnly_PersistsForRestart(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ship := newIdleTradeShip(t, "STK-HSO", playerID)
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"STK-HSO": ship}}

	result, err := s.StartStocker(context.Background(), "STK-HSO", "X1-HOME-A1", 0, 0, -1, 0, 0, true, 0, 0, true, "ENDURANCE", playerID)
	require.NoError(t, err)
	require.NotNil(t, result)

	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner)
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Contains(t, model.Config, `"home_system_only":true`, "the intra-system sourcing intent must be persisted for restart re-adoption")
}

// sp-k2xav: the command-factory REBUILD reads home_system_only back off the persisted config into
// the command (the restart half of RULINGS #2) — so a recovered depot stocker resumes home-system
// sourcing. An absent key rebuilds HomeSystemOnly=false (the generic cross-system default,
// byte-identical for every pre-existing stocker container).
func TestBuildStockerCommand_ReadsHomeSystemOnly(t *testing.T) {
	on := buildStockerCoordinatorCommand(newConfigReader(map[string]interface{}{
		"ship_symbol": "STK-1", "warehouse_waypoint": "X1-HOME-A1", "home_system_only": true,
	}), 1, "ctr-on").(*tradingCmd.RunStockerCoordinatorCommand)
	require.True(t, on.HomeSystemOnly, "home_system_only:true must rebuild into the command for restart re-adoption")

	off := buildStockerCoordinatorCommand(newConfigReader(map[string]interface{}{
		"ship_symbol": "STK-1", "warehouse_waypoint": "X1-HOME-A1",
	}), 1, "ctr-off").(*tradingCmd.RunStockerCoordinatorCommand)
	require.False(t, off.HomeSystemOnly, "an absent home_system_only must default false (generic cross-system, byte-identical)")
}

// Recovery must ADOPT a RUNNING stocker container as a top-level coordinator (not skip it
// as a worker, not orphan it): rebuild the command from the launch config, re-claim the
// idle hull, and start a live runner. The hull-claim half of RULINGS #2 (a laden hull's
// cargo is rebuilt from live ship state on the coordinator's own resume-deposit-first pass).
func TestRecoveryAdoptsRunningStockerContainer(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ship := newIdleTradeShip(t, "STK-2", playerID)
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"STK-2": ship}}

	insertRunningContainer(t, db, "stk-rec-1", "stocker", "TRADING",
		`{"ship_symbol":"STK-2","warehouse_waypoint":"X1-HOME-A1","iterations":-1,"container_id":"stk-rec-1","operation":"stocker"}`,
		playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	runner := s.registeredRunner("stk-rec-1")
	require.NotNil(t, runner, "a RUNNING stocker container must be adopted by recovery, not skipped")
	defer runner.cancelFunc()
	requireContainerState(t, db, "stk-rec-1", "RUNNING", "")
	require.True(t, ship.IsAssigned(), "the stocker hull must be re-claimed on recovery, not left stranded")
	require.Equal(t, "stk-rec-1", ship.ContainerID())
}

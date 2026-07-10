package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
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

	result, err := s.StartStocker(context.Background(), "STK-BUSY", "X1-HOME-A1", 0, 0, -1, 0, 0, "ENDURANCE", 1)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "not idle")
	require.Empty(t, s.containers, "a refused start registers no container")
	require.Equal(t, "tour_run-OTHER", ship.ContainerID(), "the other coordinator's claim is untouched")
}

// An idle hull must produce a recovery-visible stocker container: persisted with the
// "stocker" command_type, "TRADING" container_type, and a config carrying
// ship_symbol/warehouse_waypoint/iterations so restart recovery can rebuild it (RULINGS #2).
func TestStartStocker_IdleShip_PersistsRecoveryVisibleContainer(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ship := newIdleTradeShip(t, "STK-1", playerID)
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"STK-1": ship}}

	result, err := s.StartStocker(context.Background(), "STK-1", "X1-HOME-A1", 200000, 60000, -1, 60, 120, "ENDURANCE", playerID)
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
		`{"ship_symbol":"STK-2","warehouse_waypoint":"X1-HOME-A1","iterations":-1,"container_id":"stk-rec-1"}`,
		playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	runner := s.registeredRunner("stk-rec-1")
	require.NotNil(t, runner, "a RUNNING stocker container must be adopted by recovery, not skipped")
	defer runner.cancelFunc()
	requireContainerState(t, db, "stk-rec-1", "RUNNING", "")
	require.True(t, ship.IsAssigned(), "the stocker hull must be re-claimed on recovery, not left stranded")
	require.Equal(t, "stk-rec-1", ship.ContainerID())
}

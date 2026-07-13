package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Idle-gap discipline (RULINGS #7): the warehouse dedicates a hull, so it must
// refuse a hull the daemon is already flying BEFORE persisting anything — a
// refused start has no side effects and never poaches another coordinator's
// ship. Mirrors StartTradeRoute's boundary check.
func TestStartWarehouse_RefusesNonIdleShip(t *testing.T) {
	ship := newIdleTradeShip(t, "WH-BUSY", 1)
	require.NoError(t, ship.AssignToContainer("gas_coordinator-OTHER", shared.NewRealClock()))

	s := &DaemonServer{
		shipRepo:       &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"WH-BUSY": ship}},
		containers:     make(map[string]*ContainerRunner),
		containerSpecs: make(map[string]ContainerSpec),
	}
	s.registerContainerSpecs()

	result, err := s.StartWarehouse(context.Background(), "WH-BUSY", "X1-HOME-A1", []string{"IRON_ORE"}, 1)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "not idle")
	require.Empty(t, s.containers, "a refused start registers no container")
	require.Equal(t, "gas_coordinator-OTHER", ship.ContainerID(), "the other coordinator's claim is untouched")
}

// An idle hull must produce a recovery-visible warehouse container: persisted
// with the "warehouse" command_type, "WAREHOUSE" container_type, and a config
// carrying ship_symbol/waypoint_symbol/supported_goods so restart recovery can
// rebuild it, plus the operation="warehouse" fleet identity so both the fresh
// start and a recovery rebuild claim the hull under the warehouse dedication
// (RULINGS #7). This is the recovery-visibility RULINGS #2 requires — the row
// the StorageRecoveryService and container recovery both key off.
func TestStartWarehouse_IdleShip_PersistsRecoveryVisibleContainer(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ship := newIdleTradeShip(t, "WH-1", playerID)
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"WH-1": ship}}

	result, err := s.StartWarehouse(context.Background(), "WH-1", "X1-HOME-A1", []string{"IRON_ORE", "ALUMINUM"}, playerID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.ContainerID)
	require.Equal(t, "WH-1", result.ShipSymbol)
	require.Equal(t, "X1-HOME-A1", result.WaypointSymbol)

	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner, "a live runner must own the warehouse (release-on-death)")
	defer runner.cancelFunc()

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Equal(t, "warehouse", model.CommandType)
	require.Equal(t, "WAREHOUSE", model.ContainerType)
	require.Contains(t, model.Config, "WH-1")
	require.Contains(t, model.Config, "X1-HOME-A1")
	require.Contains(t, model.Config, "supported_goods")
	require.Contains(t, model.Config, "IRON_ORE")
	require.Contains(t, model.Config, `"operation":"warehouse"`)
}

// Recovery must ADOPT a RUNNING warehouse container as a top-level coordinator
// (not skip it as a worker, not orphan it): rebuild the command from the launch
// config, re-claim the idle hull under the warehouse dedication, and start a
// live runner. This is the hull-claim half of RULINGS #2 — the cargo half is
// rebuilt separately by the StorageRecoveryService (proven in the storage
// package). Together they make a warehouse fully restart-resilient.
func TestRecoveryAdoptsRunningWarehouseContainer(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ship := newIdleTradeShip(t, "WH-2", playerID)
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"WH-2": ship}}

	insertRunningContainer(t, db, "wh-rec-1", "warehouse", "WAREHOUSE",
		`{"ship_symbol":"WH-2","waypoint_symbol":"X1-HOME-A1","supported_goods":["IRON_ORE"],"container_id":"wh-rec-1","operation":"warehouse"}`,
		playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	runner := s.registeredRunner("wh-rec-1")
	require.NotNil(t, runner, "a RUNNING warehouse container must be adopted by recovery, not skipped")
	defer runner.cancelFunc()
	requireContainerState(t, db, "wh-rec-1", "RUNNING", "")
	require.True(t, ship.IsAssigned(), "the warehouse hull must be re-claimed on recovery, not left stranded")
	require.Equal(t, "wh-rec-1", ship.ContainerID())
}

// stubWarehouseMiner is a canned demand miner for the target-unit computation test.
type stubWarehouseMiner struct {
	rows []persistence.DemandCandidate
	err  error
}

func (m *stubWarehouseMiner) Mine(ctx context.Context, homeSystem string, playerID int, eraID *int, opts persistence.DemandMinerOptions) ([]persistence.DemandCandidate, error) {
	return m.rows, m.err
}

// warehouseTargetUnits computes the per-good caps StartWarehouse persists into the config
// (sp-5n7v ENGINE CHANGE #1): auto-computed from live demand over the REAL hull capacity.
func TestWarehouseTargetUnits_ComputedFromLiveDemandAndRealCapacity(t *testing.T) {
	miner := &stubWarehouseMiner{rows: []persistence.DemandCandidate{
		{Good: "DRUGS", ContractCount: 3, DemandUnits: 72, MaxContractUnits: 24, ForeignSystem: "X1-J58", HomeAsk: 700, HomeAskKnown: true},
		{Good: "ANTIMATTER", ContractCount: 2, DemandUnits: 16, MaxContractUnits: 8, ForeignSystem: "X1-I56", HomeAsk: 900, HomeAskKnown: true},
	}}

	// Real hull capacity 80 (read from the ship, never assumed). Nil coords lookup → the
	// distance-aware residual FAILS OPEN to the coarse in/cross-system constant (RULINGS #1).
	targets := warehouseTargetUnits(context.Background(), miner, 80, "X1-VB74", "X1-VB74-A1", nil, 1, nil)

	require.Equal(t, 24, targets["DRUGS"], "buffered at its single-contract size")
	require.Equal(t, 8, targets["ANTIMATTER"])
}

// With no demand miner (or a mining error) the caps fall back to the static cold-start set,
// clipped to the REAL hull capacity — never assume-80.
func TestWarehouseTargetUnits_ColdStartClippedToRealCapacity(t *testing.T) {
	// A small 50-cargo hull: DRUGS(24)+MEDICINE(20)=44 fit; EQUIPMENT(20) overflows.
	targets := warehouseTargetUnits(context.Background(), nil, 50, "X1-VB74", "X1-VB74-A1", nil, 1, nil)

	require.Equal(t, 24, targets["DRUGS"])
	require.Equal(t, 20, targets["MEDICINE"])
	require.Zero(t, targets["EQUIPMENT"], "the cold-start set is clipped to the real capacity, not assume-80")
	total := 0
	for _, u := range targets {
		total += u
	}
	require.LessOrEqual(t, total, 50)
}

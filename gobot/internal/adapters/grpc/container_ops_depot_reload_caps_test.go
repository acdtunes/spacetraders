package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// sp-94du: on registry reload, a non-idle hull's warehouse operation row must have its
// supported_goods whitelist re-solved and PERSISTED — never skipped — so a cap-selector
// redeploy reaches an already-running warehouse.
//
// The stocker re-reads each warehouse's supported_goods from the STORE every pass
// (warehousesAt -> FindRunning), so persisting the freshly-recomputed whitelist to the running
// operation's row is what makes a redeploy live. These tests drive launchDepotWarehouse against
// a non-idle hull whose warehouse operation carries a STALE whitelist and assert the row is
// re-solved + persisted (a, b) WITHOUT double-launching the coordinator (c).

// nonIdleDepotWarehouseFixture seeds a RUNNING warehouse storage_operations row on a NON-IDLE
// hull (its coordinator already re-adopted by recovery) with a STALE supported_goods whitelist,
// and injects a fake receipt miner so the re-solve runs off a fixed candidate set. It returns
// the operation repo + id so a test can read the persisted whitelist back after the reload.
func nonIdleDepotWarehouseFixture(
	t *testing.T,
	s *DaemonServer,
	db *gorm.DB,
	playerID int,
	shipSymbol, warehouseWaypoint string,
	staleGoods []string,
	minerRows []persistence.DemandCandidate,
) (*persistence.StorageOperationRepository, string) {
	t.Helper()
	ctx := context.Background()

	// A hull whose warehouse coordinator was already re-adopted by recovery is NOT idle — the
	// exact condition the re-solve must not skip.
	ship := newIdleTradeShip(t, shipSymbol, playerID)
	require.NoError(t, ship.AssignToContainer("warehouse-RUNNING-"+shipSymbol, shared.NewRealClock()))
	require.False(t, ship.IsIdle(), "the recovered warehouse hull must be non-idle for this regression")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{shipSymbol: ship}}

	// The re-solve runs off this fixed candidate set instead of a live demand history.
	s.depotReceiptMinerOverride = &fakeReceiptMiner{rows: minerRows}

	operationID := "warehouse-" + shipSymbol + "-stale"
	opRepo := persistence.NewStorageOperationRepository(db, nil)
	op, err := storage.NewWarehouseOperation(operationID, playerID, warehouseWaypoint, []string{shipSymbol}, staleGoods, nil)
	require.NoError(t, err)
	require.NoError(t, op.Start())
	require.NoError(t, opRepo.Create(ctx, op))
	return opRepo, operationID
}

// (a) On registry reload, a depot warehouse whose hull is NON-IDLE still gets its receipt
// caps recomputed and PERSISTED — proving the re-solve is not skipped for a non-idle hull. The
// stale generic whitelist is REPLACED by the recomputed selector's goods on the running
// operation's row (fresh good present, stale good gone), so the stocker re-reads the new set.
func TestLaunchDepotWarehouse_NonIdleReload_RecomputesAndPersistsCaps(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	const shipSymbol = "WH-RELOAD-A"
	const warehouseWaypoint = "X1-J58-WH"

	// Stale generic set stamped by the OLD selector (mirrors the live ELECTRONICS/EQUIPMENT/
	// MEDICINE bug). The re-solve yields the high-value CLOTHING the new selector surfaces.
	staleGoods := []string{"ELECTRONICS", "EQUIPMENT", "MEDICINE"}
	minerRows := []persistence.DemandCandidate{
		{Good: "CLOTHING", ContractCount: 5, MaxContractUnits: 40,
			ForeignMarket: "X2-SRC", ForeignSystem: "X2", ForeignAsk: 500,
			HomeAsk: 500, HomeAskKnown: true, ContractRewardPerUnit: 5000},
	}
	opRepo, operationID := nonIdleDepotWarehouseFixture(t, s, db, playerID, shipSymbol, warehouseWaypoint, staleGoods, minerRows)

	require.NoError(t, s.launchDepotWarehouse(context.Background(), shipSymbol, warehouseWaypoint, nil, playerID))

	reloaded, err := opRepo.FindByID(context.Background(), operationID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	require.True(t, reloaded.SupportsGood("CLOTHING"),
		"the recomputed high-value good must reach the running warehouse's persisted whitelist")
	require.False(t, reloaded.SupportsGood("ELECTRONICS"),
		"the stale whitelist must be REPLACED by the re-solve, not left standing")
}

// (b) The recomputed caps reflect the reward-ranked selector, end to end through the persist
// path: given two received goods identical in every knapsack input EXCEPT that market ask and
// contract reward are INVERTED, under a capacity that fits one the PERSISTED whitelist keeps the
// high-contract-reward good and drops the high-market-ask/low-reward one. This proves the
// re-solve routes through the sp-64se reward-ranked selector (depotColocatedWarehouseTargets),
// not a generic ask-ranked fallback.
func TestLaunchDepotWarehouse_NonIdleReload_PersistsRewardRankedCaps(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	const shipSymbol = "WH-RELOAD-B"
	const warehouseWaypoint = "X1-J58-WH"

	staleGoods := []string{"ELECTRONICS"}
	// Cross-system sources so the haul-leg residual is equal for both goods — contract reward is
	// the ONLY differentiator. Capacity 40 (the hull's cargo) fits exactly one 40-unit good.
	minerRows := []persistence.DemandCandidate{
		{Good: "HIGH_ASK_LOW_REWARD", ContractCount: 5, MaxContractUnits: 40,
			ForeignMarket: "X2-SRC", ForeignSystem: "X2", ForeignAsk: 8000,
			HomeAsk: 8000, HomeAskKnown: true, ContractRewardPerUnit: 1000},
		{Good: "LOW_ASK_HIGH_REWARD", ContractCount: 5, MaxContractUnits: 40,
			ForeignMarket: "X2-SRC", ForeignSystem: "X2", ForeignAsk: 500,
			HomeAsk: 500, HomeAskKnown: true, ContractRewardPerUnit: 5000},
	}
	opRepo, operationID := nonIdleDepotWarehouseFixture(t, s, db, playerID, shipSymbol, warehouseWaypoint, staleGoods, minerRows)

	require.NoError(t, s.launchDepotWarehouse(context.Background(), shipSymbol, warehouseWaypoint, nil, playerID))

	reloaded, err := opRepo.FindByID(context.Background(), operationID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	require.True(t, reloaded.SupportsGood("LOW_ASK_HIGH_REWARD"),
		"the re-solve ranks by CONTRACT REWARD, so the high-reward good reaches the running whitelist")
	require.False(t, reloaded.SupportsGood("HIGH_ASK_LOW_REWARD"),
		"a high market ask must NOT win a buffer slot when the good's contract reward is low")
}

// (c) The non-idle reload REFRESHES the running warehouse's caps in place — it must NOT
// double-launch the coordinator. It re-solves and persists the new whitelist (the work happened)
// while creating NO new warehouse container and registering NO new runner (the IsIdle gate still
// governs the LAUNCH, only not the re-solve).
func TestLaunchDepotWarehouse_NonIdleReload_DoesNotDoubleLaunchCoordinator(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	const shipSymbol = "WH-RELOAD-C"
	const warehouseWaypoint = "X1-J58-WH"

	staleGoods := []string{"ELECTRONICS"}
	minerRows := []persistence.DemandCandidate{
		{Good: "CLOTHING", ContractCount: 5, MaxContractUnits: 40,
			ForeignMarket: "X2-SRC", ForeignSystem: "X2", ForeignAsk: 500,
			HomeAsk: 500, HomeAskKnown: true, ContractRewardPerUnit: 5000},
	}
	opRepo, operationID := nonIdleDepotWarehouseFixture(t, s, db, playerID, shipSymbol, warehouseWaypoint, staleGoods, minerRows)

	require.NoError(t, s.launchDepotWarehouse(context.Background(), shipSymbol, warehouseWaypoint, nil, playerID))

	// The re-solve happened (so we are genuinely on the refresh path, not a silent no-op)...
	reloaded, err := opRepo.FindByID(context.Background(), operationID)
	require.NoError(t, err)
	require.True(t, reloaded.SupportsGood("CLOTHING"), "the non-idle reload must have refreshed the caps")

	// ...but NO second coordinator was launched: no live runner and no new WAREHOUSE container row.
	require.Empty(t, s.containers, "a non-idle reload must not register a second warehouse runner")
	var warehouseContainers int64
	require.NoError(t, db.Model(&persistence.ContainerModel{}).
		Where("player_id = ? AND container_type = ?", playerID, "WAREHOUSE").
		Count(&warehouseContainers).Error)
	require.Zero(t, warehouseContainers, "a non-idle reload must not persist a second warehouse container")
}

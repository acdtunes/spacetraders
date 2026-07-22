package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// On registry reload, a depot warehouse whose hull is NON-IDLE (its coordinator already re-adopted by
// recovery) must have its supported_goods re-applied to the running storage_operations row — now the
// FIXED far-source whitelist (launchDepotWarehouse pins it; the on-demand receipt miner is gone). This is
// the RESTART-SAFETY guarantee (RULINGS #2): a reboot re-pins the SAME set, so an armed warehouse's goods
// survive every restart. The old receipt-miner re-solve OVERWROTE the pin on the first reboot (the bug
// closed here). The stocker re-reads each warehouse's supported_goods from the store every pass
// (warehousesAt -> FindRunning), so persisting the fixed whitelist onto the running row makes the re-pin
// live on the next stocker tick, no container restart needed.

// nonIdleDepotWarehouseFixture seeds a RUNNING warehouse storage_operations row on a NON-IDLE hull (its
// coordinator already re-adopted by recovery) with a STALE supported_goods whitelist, and returns the
// operation repo + id so a test can read the persisted whitelist back after the reload.
func nonIdleDepotWarehouseFixture(
	t *testing.T,
	s *DaemonServer,
	db *gorm.DB,
	playerID int,
	shipSymbol, warehouseWaypoint string,
	staleGoods []string,
) (*persistence.StorageOperationRepository, string) {
	t.Helper()
	ctx := context.Background()

	// A hull whose warehouse coordinator was already re-adopted by recovery is NOT idle — the exact
	// condition the re-pin must not skip.
	ship := newIdleTradeShip(t, shipSymbol, playerID)
	require.NoError(t, ship.AssignToContainer("warehouse-RUNNING-"+shipSymbol, shared.NewRealClock()))
	require.False(t, ship.IsIdle(), "the recovered warehouse hull must be non-idle for this regression")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{shipSymbol: ship}}

	operationID := "warehouse-" + shipSymbol + "-stale"
	opRepo := persistence.NewStorageOperationRepository(db, nil)
	op, err := storage.NewWarehouseOperation(operationID, playerID, warehouseWaypoint, []string{shipSymbol}, staleGoods, nil)
	require.NoError(t, err)
	require.NoError(t, op.Start())
	require.NoError(t, opRepo.Create(ctx, op))
	return opRepo, operationID
}

// (a) On registry reload, a non-idle depot warehouse's stale whitelist is REPLACED by the FIXED
// far-source whitelist on the running operation's row — proving the reload RE-PINS (not skips) and that
// the pinned set survives restart (the fixed good present, the stale good gone).
func TestLaunchDepotWarehouse_NonIdleReload_PinsFixedWhitelist(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	const shipSymbol = "WH-RELOAD-A"
	const warehouseWaypoint = "X1-J58-WH"

	// A stale generic set stamped by the OLD receipt selector (mirrors the live ELECTRONICS/EQUIPMENT/
	// MEDICINE bug). The reload re-pins the fixed far-source whitelist over it.
	staleGoods := []string{"ELECTRONICS", "EQUIPMENT", "MEDICINE"}
	opRepo, operationID := nonIdleDepotWarehouseFixture(t, s, db, playerID, shipSymbol, warehouseWaypoint, staleGoods)

	require.NoError(t, s.launchDepotWarehouse(context.Background(), shipSymbol, warehouseWaypoint, playerID))

	reloaded, err := opRepo.FindByID(context.Background(), operationID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	require.True(t, reloaded.SupportsGood(contractscaler.FarSourceGoods[0]),
		"the fixed far-source whitelist must reach the running warehouse's persisted row on reload")
	require.False(t, reloaded.SupportsGood("ELECTRONICS"),
		"the stale whitelist must be REPLACED by the fixed pin, not left standing")
}

// (b) The non-idle reload RE-PINS the caps in place — it must NOT double-launch the coordinator. It
// re-applies and persists the fixed whitelist (the work happened) while creating NO new warehouse
// container and registering NO new runner (the IsIdle gate still governs the LAUNCH, only not the re-pin).
func TestLaunchDepotWarehouse_NonIdleReload_DoesNotDoubleLaunchCoordinator(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	const shipSymbol = "WH-RELOAD-C"
	const warehouseWaypoint = "X1-J58-WH"

	opRepo, operationID := nonIdleDepotWarehouseFixture(t, s, db, playerID, shipSymbol, warehouseWaypoint, []string{"ELECTRONICS"})

	require.NoError(t, s.launchDepotWarehouse(context.Background(), shipSymbol, warehouseWaypoint, playerID))

	// The re-pin happened (so we are genuinely on the refresh path, not a silent no-op)...
	reloaded, err := opRepo.FindByID(context.Background(), operationID)
	require.NoError(t, err)
	require.True(t, reloaded.SupportsGood(contractscaler.FarSourceGoods[0]), "the non-idle reload must have re-pinned the caps")

	// ...but NO second coordinator was launched: no live runner and no new WAREHOUSE container row.
	require.Empty(t, s.containers, "a non-idle reload must not register a second warehouse runner")
	var warehouseContainers int64
	require.NoError(t, db.Model(&persistence.ContainerModel{}).
		Where("player_id = ? AND container_type = ?", playerID, "WAREHOUSE").
		Count(&warehouseContainers).Error)
	require.Zero(t, warehouseContainers, "a non-idle reload must not persist a second warehouse container")
}

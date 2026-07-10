package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// sp-86yb: `operations stop` (e.g. a gas coordinator `--gas` stop) killed the
// coordinator CONTAINER but left its storage_operations ROW at status=RUNNING.
// Every manufacturing coordinator then saw an "active" storage source and spawned
// STORAGE_ACQUIRE_DELIVER tasks against the now-empty, agentless ship forever - a
// recurring wedge. StopContainer must terminalize the storage_operations row for
// the coordinator it just stopped, not merely update the containers table.
func TestStopContainerTerminalizesGasCoordinatorStorageOperationRow(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const coordinatorID = "gas_coordinator-TORWIND-9-c1def23d"
	seedStorageOperation(t, db, coordinatorID, playerID, "RUNNING")
	startTestGasCoordinator(t, s, coordinatorID, playerID)

	require.NoError(t, s.StopContainer(coordinatorID))

	requireStorageOperationStatus(t, db, coordinatorID, playerID, "STOPPED")
}

// sp-3lj5: a warehouse container needs the identical terminalization gas
// coordinators received under sp-86yb. Left un-terminalized, a stopped
// warehouse's storage_operations row stays RUNNING forever - a stale "zombie"
// row that the stocker/tour warehouse lookup can resolve to instead of a live
// replacement at the same waypoint, always reading back zero free space and
// making a warehouse with real free space look full.
func TestStopContainerTerminalizesWarehouseStorageOperationRow(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const warehouseID = "warehouse-TORWIND-12-bad719ff"
	seedWarehouseStorageOperation(t, db, warehouseID, playerID, "RUNNING")
	startTestWarehouse(t, s, warehouseID, playerID)

	require.NoError(t, s.StopContainer(warehouseID))

	requireStorageOperationStatus(t, db, warehouseID, playerID, "STOPPED")
}

// The terminalization must be scoped to gas coordinators specifically: stopping
// some other container type must never reach into storage_operations, even if a
// row happens to share its ID.
func TestStopContainerOnNonGasCoordinatorLeavesStorageOperationRowAlone(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const workerID = "manufacturing-task-worker-TORWIND-9-abc123"
	seedStorageOperation(t, db, workerID, playerID, "RUNNING")
	startTestContainer(t, s, workerID, playerID, container.ContainerTypeManufacturingTaskWorker)

	require.NoError(t, s.StopContainer(workerID))

	requireStorageOperationStatus(t, db, workerID, playerID, "RUNNING")
}

// Idempotency guard: if the storage_operations row already reached a terminal
// status by some other path (e.g. the coordinator completed normally right before
// the stop request landed), StopContainer must not error out or clobber it back to
// STOPPED - COMPLETED is the better outcome and must be preserved.
func TestStopContainerDoesNotClobberAlreadyCompletedStorageOperationRow(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const coordinatorID = "gas_coordinator-TORWIND-9-already-done"
	seedStorageOperation(t, db, coordinatorID, playerID, "COMPLETED")
	startTestGasCoordinator(t, s, coordinatorID, playerID)

	require.NoError(t, s.StopContainer(coordinatorID))

	requireStorageOperationStatus(t, db, coordinatorID, playerID, "COMPLETED")
}

// Same idempotency guard, mirrored for warehouses (sp-3lj5).
func TestStopContainerDoesNotClobberAlreadyCompletedWarehouseStorageOperationRow(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	const warehouseID = "warehouse-TORWIND-12-already-done"
	seedWarehouseStorageOperation(t, db, warehouseID, playerID, "COMPLETED")
	startTestWarehouse(t, s, warehouseID, playerID)

	require.NoError(t, s.StopContainer(warehouseID))

	requireStorageOperationStatus(t, db, warehouseID, playerID, "COMPLETED")
}

func startTestGasCoordinator(t *testing.T, s *DaemonServer, id string, playerID int) {
	t.Helper()
	startTestContainer(t, s, id, playerID, container.ContainerTypeGasCoordinator)
}

func startTestWarehouse(t *testing.T, s *DaemonServer, id string, playerID int) {
	t.Helper()
	startTestContainer(t, s, id, playerID, container.ContainerTypeWarehouse)
}

// startTestContainer registers a real, running ContainerRunner (Start()ed for
// real, not just entity.Start()) so its execute() goroutine exists and reacts to
// context cancellation near-instantly, keeping Stop() off its 10s fallback path.
func startTestContainer(t *testing.T, s *DaemonServer, id string, playerID int, containerType container.ContainerType) {
	t.Helper()
	entity := container.NewContainer(id, containerType, playerID, -1, nil, nil, nil)
	runner := NewContainerRunner(entity, nil, nil, noopLogRepo{}, nil, nil, nil)
	require.NoError(t, runner.Start())
	s.registerContainer(id, runner)
}

func seedStorageOperation(t *testing.T, db *gorm.DB, id string, playerID int, status string) {
	t.Helper()
	model := &persistence.StorageOperationModel{
		ID:             id,
		PlayerID:       playerID,
		WaypointSymbol: "X1-PIN-GAS",
		OperationType:  "GAS_SIPHON",
		Status:         status,
		ExtractorShips: `["SHIP-EXT-1"]`,
		StorageShips:   `["SHIP-STORE-1"]`,
		SupportedGoods: `["LIQUID_HYDROGEN"]`,
	}
	require.NoError(t, db.Create(model).Error)
}

// seedWarehouseStorageOperation mirrors seedStorageOperation for the WAREHOUSE
// operation type. ExtractorShips is deliberately empty, matching
// storage.NewWarehouseOperation's shape: a warehouse is fed by haulers, not
// extractors.
func seedWarehouseStorageOperation(t *testing.T, db *gorm.DB, id string, playerID int, status string) {
	t.Helper()
	model := &persistence.StorageOperationModel{
		ID:             id,
		PlayerID:       playerID,
		WaypointSymbol: "X1-TORWIND-12",
		OperationType:  "WAREHOUSE",
		Status:         status,
		ExtractorShips: `[]`,
		StorageShips:   `["SHIP-STORE-1"]`,
		SupportedGoods: `["FUEL"]`,
	}
	require.NoError(t, db.Create(model).Error)
}

func requireStorageOperationStatus(t *testing.T, db *gorm.DB, id string, playerID int, want string) {
	t.Helper()
	var model persistence.StorageOperationModel
	require.NoError(t, db.First(&model, "id = ? AND player_id = ?", id, playerID).Error)
	require.Equal(t, want, model.Status)
}

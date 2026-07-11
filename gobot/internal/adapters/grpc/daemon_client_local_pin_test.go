package grpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func newLocalClientHarness(t *testing.T) (*DaemonClientLocal, *DaemonServer, *gorm.DB, int) {
	t.Helper()
	server, db, playerID := newRecoveryTestServer(t)
	return NewDaemonClientLocal(server), server, db, playerID
}

func loadContainerRow(t *testing.T, db *gorm.DB, id string) persistence.ContainerModel {
	t.Helper()
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", id).Error)
	return model
}

func containerConfig(t *testing.T, model persistence.ContainerModel) map[string]interface{} {
	t.Helper()
	var config map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &config))
	return config
}

func siphonWorkerCommand(playerID int) *gasCmd.RunSiphonWorkerCommand {
	return &gasCmd.RunSiphonWorkerCommand{
		ShipSymbol:         "SHIP-SIPHON",
		PlayerID:           shared.MustNewPlayerID(playerID),
		GasGiant:           "X1-GG-1",
		CoordinatorID:      "gas-coord-1",
		StorageOperationID: "gas-op-1",
	}
}

func storageShipCommand(playerID int) *gasCmd.RunStorageShipWorkerCommand {
	return &gasCmd.RunStorageShipWorkerCommand{
		ShipSymbol:         "SHIP-STORAGE",
		PlayerID:           shared.MustNewPlayerID(playerID),
		GasGiant:           "X1-GG-1",
		CoordinatorID:      "gas-coord-1",
		StorageOperationID: "gas-op-1",
	}
}

func TestPersistContractWorkflow_StoresConfigAndLinksParent(t *testing.T) {
	client, _, db, playerID := newLocalClientHarness(t)
	cmd := &contractCmd.RunWorkflowCommand{
		ShipSymbol:    "SHIP-CW",
		PlayerID:      shared.MustNewPlayerID(playerID),
		ContainerID:   "cw-1",
		CoordinatorID: "fleet-coord-1",
	}

	require.NoError(t, client.PersistContainer(context.Background(), daemon.ContainerKindContractWorkflow, "cw-1", uint(playerID), cmd))

	model := loadContainerRow(t, db, "cw-1")
	require.Equal(t, "CONTRACT_WORKFLOW", model.ContainerType)
	require.Equal(t, "contract_workflow", model.CommandType)
	require.Equal(t, "PENDING", model.Status)
	require.NotNil(t, model.ParentContainerID)
	require.Equal(t, "fleet-coord-1", *model.ParentContainerID)
	require.Equal(t, map[string]interface{}{
		"ship_symbol":    "SHIP-CW",
		"coordinator_id": "fleet-coord-1",
	}, containerConfig(t, model))
}

func TestLocalClientPersistContractWorkflow_RejectsWrongCommandType(t *testing.T) {
	client, _, _, playerID := newLocalClientHarness(t)

	err := client.PersistContainer(context.Background(), daemon.ContainerKindContractWorkflow, "cw-bad", uint(playerID), "not-a-command")

	require.ErrorIs(t, err, daemon.ErrInvalidCommandType)
}

func TestPersistGasSiphonWorker_PersistsAsGasSiphonWorkerType(t *testing.T) {
	client, _, db, playerID := newLocalClientHarness(t)

	require.NoError(t, client.PersistContainer(context.Background(), daemon.ContainerKindGasSiphonWorker, "siphon-1", uint(playerID), siphonWorkerCommand(playerID)))

	model := loadContainerRow(t, db, "siphon-1")
	require.Equal(t, "GAS_SIPHON_WORKER", model.ContainerType)
	require.Equal(t, "gas_siphon_worker", model.CommandType)
	require.Equal(t, "PENDING", model.Status)
	require.NotNil(t, model.ParentContainerID)
	require.Equal(t, "gas-coord-1", *model.ParentContainerID)
	require.Equal(t, map[string]interface{}{
		"ship_symbol":          "SHIP-SIPHON",
		"gas_giant":            "X1-GG-1",
		"coordinator_id":       "gas-coord-1",
		"storage_operation_id": "gas-op-1",
	}, containerConfig(t, model))
}

func TestLocalClientPersistGasSiphonWorker_RejectsWrongCommandType(t *testing.T) {
	client, _, _, playerID := newLocalClientHarness(t)

	err := client.PersistContainer(context.Background(), daemon.ContainerKindGasSiphonWorker, "siphon-bad", uint(playerID), "not-a-command")

	require.Error(t, err)
}

func TestPersistStorageShip_StoresTypeStatusParentAndConfig(t *testing.T) {
	client, _, db, playerID := newLocalClientHarness(t)

	require.NoError(t, client.PersistContainer(context.Background(), daemon.ContainerKindStorageShip, "storage-1", uint(playerID), storageShipCommand(playerID)))

	model := loadContainerRow(t, db, "storage-1")
	require.Equal(t, "STORAGE_SHIP", model.ContainerType)
	require.Equal(t, "storage_ship", model.CommandType)
	require.Equal(t, "PENDING", model.Status)
	require.NotNil(t, model.ParentContainerID)
	require.Equal(t, "gas-coord-1", *model.ParentContainerID)
	require.Equal(t, map[string]interface{}{
		"ship_symbol":          "SHIP-STORAGE",
		"gas_giant":            "X1-GG-1",
		"coordinator_id":       "gas-coord-1",
		"storage_operation_id": "gas-op-1",
	}, containerConfig(t, model))
}

func TestLocalClientStartGasSiphonWorker_RegistersRunner(t *testing.T) {
	client, server, _, playerID := newLocalClientHarness(t)
	require.NoError(t, client.PersistContainer(context.Background(), daemon.ContainerKindGasSiphonWorker, "siphon-run", uint(playerID), siphonWorkerCommand(playerID)))

	require.NoError(t, client.StartContainer(context.Background(), daemon.ContainerKindGasSiphonWorker, "siphon-run"))

	runner := server.registeredRunner("siphon-run")
	require.NotNil(t, runner)
	runner.cancelFunc()
}

func TestLocalClientStartStorageShip_RegistersRunner(t *testing.T) {
	client, server, _, playerID := newLocalClientHarness(t)
	require.NoError(t, client.PersistContainer(context.Background(), daemon.ContainerKindStorageShip, "storage-run", uint(playerID), storageShipCommand(playerID)))

	require.NoError(t, client.StartContainer(context.Background(), daemon.ContainerKindStorageShip, "storage-run"))

	runner := server.registeredRunner("storage-run")
	require.NotNil(t, runner)
	runner.cancelFunc()
}

func TestLocalClientStartContractWorkflow_RegistersRunner(t *testing.T) {
	client, server, _, playerID := newLocalClientHarness(t)
	cmd := &contractCmd.RunWorkflowCommand{
		ShipSymbol:    "SHIP-CW",
		PlayerID:      shared.MustNewPlayerID(playerID),
		ContainerID:   "cw-run",
		CoordinatorID: "fleet-coord-1",
	}
	require.NoError(t, client.PersistContainer(context.Background(), daemon.ContainerKindContractWorkflow, "cw-run", uint(playerID), cmd))

	require.NoError(t, client.StartContainer(context.Background(), daemon.ContainerKindContractWorkflow, "cw-run"))

	runner := server.registeredRunner("cw-run")
	require.NotNil(t, runner)
	runner.cancelFunc()
}

func TestLocalClientPersistContainer_UnknownKindFails(t *testing.T) {
	client, _, _, playerID := newLocalClientHarness(t)

	err := client.PersistContainer(context.Background(), "mystery", "x-1", uint(playerID), nil)

	require.ErrorIs(t, err, daemon.ErrUnknownContainerKind)
}

func TestLocalClientStartContainer_UnknownKindFails(t *testing.T) {
	client, _, _, _ := newLocalClientHarness(t)

	err := client.StartContainer(context.Background(), "mystery", "x-1")

	require.ErrorIs(t, err, daemon.ErrUnknownContainerKind)
}

func TestGRPCClientGenericContainerMethods_ReportNotImplemented(t *testing.T) {
	client := &DaemonClientGRPC{}

	require.Error(t, client.PersistContainer(context.Background(), daemon.ContainerKindContractWorkflow, "x-1", 1, nil))
	require.Error(t, client.StartContainer(context.Background(), daemon.ContainerKindContractWorkflow, "x-1"))
}

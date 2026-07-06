package grpc

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

type recoveryStubShipRepo struct {
	navigation.ShipRepository
}

func (r *recoveryStubShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	return nil, fmt.Errorf("ship %s unavailable in recovery test", symbol)
}

func (r *recoveryStubShipRepo) FindByContainer(ctx context.Context, containerID string, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	return nil, nil
}

type blockingMediator struct{}

func (m *blockingMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *blockingMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}

func (m *blockingMediator) RegisterMiddleware(middleware common.Middleware) {}

func newRecoveryTestServer(t *testing.T) (*DaemonServer, *gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "REC-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	s := &DaemonServer{
		db:                    db,
		logRepo:               persistence.NewGormContainerLogRepository(db, nil),
		containerRepo:         persistence.NewContainerRepository(db),
		shipRepo:              &recoveryStubShipRepo{},
		mediator:              &blockingMediator{},
		clock:                 shared.NewRealClock(),
		containers:            make(map[string]*ContainerRunner),
		containerSpecs:        make(map[string]ContainerSpec),
		pendingWorkerCommands: make(map[string]interface{}),
	}
	s.registerContainerSpecs()
	return s, db, player.ID
}

func insertRunningContainer(t *testing.T, db *gorm.DB, id, commandType, containerType, config string, playerID int, parentID *string) {
	t.Helper()
	now := time.Now()
	model := &persistence.ContainerModel{
		ID:                id,
		PlayerID:          playerID,
		ContainerType:     containerType,
		CommandType:       commandType,
		Status:            "RUNNING",
		ParentContainerID: parentID,
		Config:            config,
		StartedAt:         &now,
		HeartbeatAt:       &now,
	}
	require.NoError(t, db.Create(model).Error)
}

func requireContainerState(t *testing.T, db *gorm.DB, id, wantStatus, wantReasonPrefix string) {
	t.Helper()
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", id).Error)
	require.Equal(t, wantStatus, model.Status)
	require.True(t, strings.HasPrefix(model.ExitReason, wantReasonPrefix),
		"exit_reason %q does not start with %q", model.ExitReason, wantReasonPrefix)
}

func (s *DaemonServer) registeredRunner(containerID string) *ContainerRunner {
	s.containersMu.RLock()
	defer s.containersMu.RUnlock()
	return s.containers[containerID]
}

func TestRecoverySkipsContractWorkerWithCoordinatorAndMarksInterrupted(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	parent := "coord-1"
	insertRunningContainer(t, db, "worker-1", "contract_workflow", "CONTRACT_WORKFLOW",
		`{"ship_symbol":"SHIP-A","coordinator_id":"coord-1"}`, playerID, &parent)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	requireContainerState(t, db, "worker-1", "FAILED", "worker_interrupted")
	require.Nil(t, s.registeredRunner("worker-1"))
}

func TestRecoverySkipsWorkerIdentifiedByParentContainerID(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	parent := "coord-9"
	insertRunningContainer(t, db, "worker-2", "contract_workflow", "CONTRACT_WORKFLOW",
		`{"ship_symbol":"SHIP-B"}`, playerID, &parent)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	requireContainerState(t, db, "worker-2", "FAILED", "worker_interrupted")
	require.Nil(t, s.registeredRunner("worker-2"))
}

func TestRecoveryMarksKnownWorkerCommandTypesInterrupted(t *testing.T) {
	workerTypes := []string{"manufacturing_task_worker", "gas_siphon_worker", "gas_transport_worker", "storage_ship"}
	for _, workerType := range workerTypes {
		t.Run(workerType, func(t *testing.T) {
			s, db, playerID := newRecoveryTestServer(t)
			id := "w-" + workerType
			insertRunningContainer(t, db, id, workerType, "WORKER", `{}`, playerID, nil)

			require.NoError(t, s.RecoverRunningContainers(context.Background()))

			requireContainerState(t, db, id, "FAILED", "worker_interrupted")
			require.Nil(t, s.registeredRunner(id))
		})
	}
}

func TestRecoveryMarksOrphanedGasSiphonWorkerInterruptedViaSpec(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "orphan-siphon-1", "gas_siphon_worker", "GAS_SIPHON_WORKER",
		`{"ship_symbol":"SHIP-G","gas_giant":"X1-GG1","storage_operation_id":"op-1"}`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	requireContainerState(t, db, "orphan-siphon-1", "FAILED", "worker_interrupted")
	require.Nil(t, s.registeredRunner("orphan-siphon-1"))
}

func TestRecoveryAttemptsStandaloneContractWorkflowWithEmptyCoordinator(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	emptyParent := ""
	insertRunningContainer(t, db, "solo-1", "contract_workflow", "CONTRACT_WORKFLOW",
		`{"ship_symbol":"SHIP-A","coordinator_id":""}`, playerID, &emptyParent)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	requireContainerState(t, db, "solo-1", "FAILED", "recovery_failed")
	require.Nil(t, s.registeredRunner("solo-1"))
}

func TestRecoveryRestartsTopLevelCoordinator(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "fleet-1", "contract_fleet_coordinator", "CONTRACT_FLEET_COORDINATOR",
		`{"ship_symbols":[],"container_id":"fleet-1"}`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	runner := s.registeredRunner("fleet-1")
	require.NotNil(t, runner)
	requireContainerState(t, db, "fleet-1", "RUNNING", "")
	runner.cancelFunc()
}

func TestRecoveryFailsUnknownCommandType(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "mystery-1", "mystery_op", "MYSTERY", `{}`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	requireContainerState(t, db, "mystery-1", "FAILED", "recovery_failed")
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", "mystery-1").Error)
	require.Contains(t, model.ExitReason, "unknown command type")
	require.Nil(t, s.registeredRunner("mystery-1"))
}

func TestRecoveryFailsInvalidConfigJSON(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "broken-1", "scout_tour", "SCOUT", `{not json`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	requireContainerState(t, db, "broken-1", "FAILED", "invalid_config")
	require.Nil(t, s.registeredRunner("broken-1"))
}

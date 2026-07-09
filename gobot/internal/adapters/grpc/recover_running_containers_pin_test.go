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
	// sp-njpu: recovery is scoped to the open era's player, so the harness needs an
	// open era owned by this player. Containers inserted with this player's ID are
	// therefore "live" and recover as before; foreign-player containers are dead-era.
	era := persistence.EraModel{Name: "REC-ERA", AgentSymbol: "REC-AGENT", PlayerID: player.ID}
	require.NoError(t, db.Create(&era).Error)
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
	workerTypes := []string{"manufacturing_task_worker", "gas_siphon_worker", "storage_ship"}
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

// TestRecoveryRestoresFactoryIterationBudgetFromMaxIterationsKey is the sp-perx
// regression: StartGoodsFactory persists a factory's iteration budget under the
// "max_iterations" config key (see container_ops_goods.go), but recoverContainer
// only ever read "iterations" when reconstructing the container entity on restart.
// Every recovered factory silently collapsed to the hardcoded default of 1, ran one
// more production cycle, then self-completed (Container.ShouldContinue is false once
// currentIteration reaches 1) — indistinguishable from "didn't survive restart" to
// the captain, even though the persisted budget said run forever (-1) or for N more
// cycles. Unlike goods_factory_coordinator, contract_fleet_coordinator and
// manufacturing_coordinator loop forever inside a single Handle() call, so the same
// stale-budget bug is latent but harmless for them; this only bites handlers that
// return after one cycle and rely on the container-level budget to keep going.
func TestRecoveryRestoresFactoryIterationBudgetFromMaxIterationsKey(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		config string
		want   int
	}{
		{
			name:   "infinite budget survives restart",
			id:     "goods-inf",
			config: `{"target_good":"MICROPROCESSORS","system_symbol":"X1-TEST","container_id":"goods-inf","max_iterations":-1}`,
			want:   -1,
		},
		{
			name:   "finite budget survives restart",
			id:     "goods-20",
			config: `{"target_good":"MICROPROCESSORS","system_symbol":"X1-TEST","container_id":"goods-20","max_iterations":20}`,
			want:   20,
		},
		{
			name:   "absent max_iterations still defaults to 1",
			id:     "goods-def",
			config: `{"target_good":"MICROPROCESSORS","system_symbol":"X1-TEST","container_id":"goods-def"}`,
			want:   1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, db, playerID := newRecoveryTestServer(t)
			insertRunningContainer(t, db, tc.id, "goods_factory_coordinator", "goods_factory_coordinator",
				tc.config, playerID, nil)

			require.NoError(t, s.RecoverRunningContainers(context.Background()))

			runner := s.registeredRunner(tc.id)
			require.NotNil(t, runner)
			require.Equal(t, tc.want, runner.Container().MaxIterations(),
				"recovered factory's iteration budget must match what was persisted")
			runner.cancelFunc()
		})
	}
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

// TestRecoverySkipsDeadEraCoordinator is the sp-njpu regression: a top-level
// coordinator belonging to a player whose era is closed / universe was reset must
// NOT be re-instantiated on daemon restart (cross-era zombie). The open era is
// owned by the harness player; this container belongs to a different, dead-era
// player, so recovery must skip it and mark it terminally as dead_era.
func TestRecoverySkipsDeadEraCoordinator(t *testing.T) {
	s, db, _ := newRecoveryTestServer(t)
	// A player from a prior, now-closed era (universe reset 2026-07-05 in the bug).
	deadPlayer := persistence.PlayerModel{AgentSymbol: "DEAD-ERA-AGENT", Token: "dead-tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&deadPlayer).Error)
	insertRunningContainer(t, db, "zombie-fleet-1", "contract_fleet_coordinator", "CONTRACT_FLEET_COORDINATOR",
		`{"ship_symbols":[],"container_id":"zombie-fleet-1"}`, deadPlayer.ID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	// Must NOT be re-instantiated (no live runner) and must be marked terminal.
	require.Nil(t, s.registeredRunner("zombie-fleet-1"))
	requireContainerState(t, db, "zombie-fleet-1", "FAILED", "dead_era")
}

// TestRecoverySkipsDeadEraWorkerBeforeCoordinatorCheck proves the era guard fires
// ahead of the worker-adoption path: an era-1 worker (parent coordinator) from a
// dead-era player is marked dead_era, not worker_interrupted, so the whole dead-era
// subtree stays down (era-1 workers were resurrecting in the bug report).
func TestRecoverySkipsDeadEraWorkerBeforeCoordinatorCheck(t *testing.T) {
	s, db, _ := newRecoveryTestServer(t)
	deadPlayer := persistence.PlayerModel{AgentSymbol: "DEAD-ERA-WORKER-AGENT", Token: "dead-tok-2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&deadPlayer).Error)
	parent := "dead-coord-1"
	insertRunningContainer(t, db, "zombie-worker-1", "manufacturing_task_worker", "WORKER",
		`{"ship_symbol":"SHIP-Z","coordinator_id":"dead-coord-1"}`, deadPlayer.ID, &parent)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	require.Nil(t, s.registeredRunner("zombie-worker-1"))
	requireContainerState(t, db, "zombie-worker-1", "FAILED", "dead_era")
}

// TestRecoverySkipsAllWhenNoOpenEra proves that with every era closed (between eras
// after a universe reset), even a container owned by the most recent player is not
// recovered: no open era means no live player.
func TestRecoverySkipsAllWhenNoOpenEra(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	require.NoError(t, db.Model(&persistence.EraModel{}).
		Where("player_id = ?", playerID).
		Update("closed_at", time.Now()).Error)
	insertRunningContainer(t, db, "post-reset-1", "contract_fleet_coordinator", "CONTRACT_FLEET_COORDINATOR",
		`{"ship_symbols":[],"container_id":"post-reset-1"}`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	require.Nil(t, s.registeredRunner("post-reset-1"))
	requireContainerState(t, db, "post-reset-1", "FAILED", "dead_era")
}

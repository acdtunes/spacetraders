package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type reclaimFakeShipRepo struct {
	navigation.ShipRepository

	ship   *navigation.Ship
	onSave func()
	saves  []contractShipSnapshot
}

func (r *reclaimFakeShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return []*navigation.Ship{r.ship}, nil
}

func (r *reclaimFakeShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	if r.ship.ContainerID() == containerID {
		return []*navigation.Ship{r.ship}, nil
	}
	return nil, nil
}

func (r *reclaimFakeShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	r.saves = append(r.saves, contractShipSnapshot{containerID: ship.ContainerID(), assigned: ship.IsAssigned()})
	if r.onSave != nil {
		r.onSave()
	}
	return nil
}

type reclaimFakeContainerRepo struct {
	byStatus map[string][]persistence.ContainerSummary
}

func (r *reclaimFakeContainerRepo) ListByStatusSimple(_ context.Context, status string, _ *int) ([]persistence.ContainerSummary, error) {
	return r.byStatus[status], nil
}

type reclaimFakeSubscriber struct {
	navigation.ShipEventSubscriber

	workerCompleted chan navigation.WorkerCompletedEvent
}

func (s *reclaimFakeSubscriber) SubscribeWorkerCompleted(_ string) <-chan navigation.WorkerCompletedEvent {
	return s.workerCompleted
}

func (s *reclaimFakeSubscriber) UnsubscribeWorkerCompleted(_ string, _ <-chan navigation.WorkerCompletedEvent) {
}

func newReclaimHandler(repo *reclaimFakeShipRepo, containerRepo *reclaimFakeContainerRepo) *RunFleetCoordinatorHandler {
	return &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(&spawnContractFakeDaemonClient{}, containerRepo, repo),
		shipRepo:               repo,
		clock:                  shared.NewRealClock(),
		eventSubscriber:        &reclaimFakeSubscriber{workerCompleted: make(chan navigation.WorkerCompletedEvent)},
	}
}

func runCoordinatorUntilIdleOrTimeout(t *testing.T, handler *RunFleetCoordinatorHandler, repo *reclaimFakeShipRepo) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	repo.onSave = cancel

	_, _ = handler.Handle(ctx, contractSpawnCommand())
}

func TestFleetCoordinator_ReclaimsShipHeldByInterruptedWorker(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	if err := ship.AssignToContainer("contract-work-dead", shared.NewRealClock()); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}
	repo := &reclaimFakeShipRepo{ship: ship}
	containerRepo := &reclaimFakeContainerRepo{byStatus: map[string][]persistence.ContainerSummary{
		"FAILED": {{ID: "contract-work-dead", ContainerType: "CONTRACT_WORKFLOW", Status: "FAILED"}},
	}}
	handler := newReclaimHandler(repo, containerRepo)

	runCoordinatorUntilIdleOrTimeout(t, handler, repo)

	if len(repo.saves) == 0 {
		t.Fatalf("expected ship held by interrupted worker to be reclaimed (released + saved), got no saves")
	}
	if last := repo.saves[len(repo.saves)-1]; last.assigned {
		t.Fatalf("expected ship released from dead worker, got %+v", last)
	}
	if !ship.IsIdle() {
		t.Fatalf("expected ship back in idle pool after reclaim")
	}
}

func TestFleetCoordinator_DoesNotReclaimShipsFromForeignFailedContainers(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	if err := ship.AssignToContainer("mfg-work-dead", shared.NewRealClock()); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}
	repo := &reclaimFakeShipRepo{ship: ship}
	containerRepo := &reclaimFakeContainerRepo{byStatus: map[string][]persistence.ContainerSummary{
		"FAILED": {{ID: "mfg-work-dead", ContainerType: "MANUFACTURING_COORDINATOR", Status: "FAILED"}},
	}}
	handler := newReclaimHandler(repo, containerRepo)

	runCoordinatorUntilIdleOrTimeout(t, handler, repo)

	if len(repo.saves) != 0 {
		t.Fatalf("expected no reclaim for non-contract FAILED containers, got saves %v", repo.saves)
	}
	if !ship.IsAssigned() {
		t.Fatalf("expected foreign container's ship assignment untouched")
	}
}

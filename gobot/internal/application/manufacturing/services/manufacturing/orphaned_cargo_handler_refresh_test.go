package manufacturing

import (
	"context"
	"testing"

	domainManufacturing "github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// phantomStubShipRepo returns a server-true (empty-hold) ship from
// SyncShipFromAPI, modelling a phantom foreign-cargo desync where the daemon
// cache shows a partially loaded hold the ship does not actually hold.
type phantomStubShipRepo struct {
	navigation.ShipRepository

	serverShip *navigation.Ship
	syncCalled int
}

func (s *phantomStubShipRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	s.syncCalled++
	return s.serverShip, nil
}

// phantomTaskRepo records whether the orphaned-cargo machinery interrogated it
// for a cargo-matching task. Post-fix a phantom hauler is filtered out before
// any task lookup, so FindAvailableByGood must never be called for it.
type phantomTaskRepo struct {
	domainManufacturing.TaskRepository

	findAvailableCalled int
}

func (r *phantomTaskRepo) FindAvailableByGood(_ context.Context, _ int, _ string) ([]*domainManufacturing.ManufacturingTask, error) {
	r.findAvailableCalled++
	return nil, nil
}

// phantomWorkerManager records ship benching (worker-container assignment).
type phantomWorkerManager struct {
	assignCalled int
}

func (m *phantomWorkerManager) AssignTaskToShip(_ context.Context, _ AssignTaskParams) error {
	m.assignCalled++
	return nil
}

func (m *phantomWorkerManager) HandleWorkerCompletion(_ context.Context, _ string) (*TaskCompletion, error) {
	return nil, nil
}

func (m *phantomWorkerManager) HandleTaskFailure(_ context.Context, _ TaskCompletion) error {
	return nil
}

func newCargoHauler(t *testing.T, symbol string, units int) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	var inventory []*shared.CargoItem
	if units > 0 {
		item, itemErr := shared.NewCargoItem("IRON_ORE", "Iron Ore", "ore", units)
		if itemErr != nil {
			t.Fatalf("NewCargoItem: %v", itemErr)
		}
		inventory = append(inventory, item)
	}
	cargo, err := shared.NewCargo(80, units, inventory)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		location,
		fuel,
		0,
		80,
		cargo,
		9,
		"FRAME_HAULER",
		"HAULER",
		nil,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// A phantom FOREIGN-CARGO hold (cluster lesson L47, e.g. TORWIND-3 cached
// 44/80 IRON_ORE but server 0/80) silently benches an idle hauler from the
// coordinator's eligible pool: the stale cache makes it "look half-full", so it
// is diverted into orphaned-cargo/liquidate handling for cargo it does not
// hold. The handler must reconcile each cargo-bearing idle ship against the
// server (force GET /my/ships) BEFORE diverting it; a server-empty hold keeps
// the healed hauler in the eligible pool with exactly one refresh and no bench.
func TestHandleShipsWithExistingCargo_PhantomCargoStaysInEligiblePool(t *testing.T) {
	phantom := newCargoHauler(t, "TORWIND-3", 44) // stale cache: looks half-full
	serverTrue := newCargoHauler(t, "TORWIND-3", 0) // server truth: empty hold

	shipRepo := &phantomStubShipRepo{serverShip: serverTrue}
	taskRepo := &phantomTaskRepo{}
	workerManager := &phantomWorkerManager{}

	handler := NewOrphanedCargoHandler(taskRepo, nil, shipRepo, workerManager, nil, nil)

	idle := map[string]*navigation.Ship{"TORWIND-3": phantom}
	result, err := handler.HandleShipsWithExistingCargo(context.Background(), OrphanedCargoParams{
		IdleShips:          idle,
		PlayerID:           1,
		MaxConcurrentTasks: 5,
	})
	if err != nil {
		t.Fatalf("HandleShipsWithExistingCargo: %v", err)
	}

	if shipRepo.syncCalled != 1 {
		t.Fatalf("expected exactly one server refresh of the cargo-bearing hauler, got %d", shipRepo.syncCalled)
	}
	if _, stillEligible := result["TORWIND-3"]; !stillEligible {
		t.Fatalf("expected phantom-cargo hauler to remain in the eligible pool, but it was benched")
	}
	if taskRepo.findAvailableCalled != 0 {
		t.Fatalf("expected phantom hauler to be filtered out before any task lookup, got %d lookups", taskRepo.findAvailableCalled)
	}
	if workerManager.assignCalled != 0 {
		t.Fatalf("expected phantom hauler NOT to be benched onto a task, got %d assignments", workerManager.assignCalled)
	}
}

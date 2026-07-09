package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// multiOrphanFakeShipRepo is reclaimFakeShipRepo's multi-ship sibling: the
// starvation-only reclaim test only ever needs one ship (the fleet has
// nothing else, so it is trivially "starved"). This scenario needs a fleet
// where a genuinely idle ship coexists with one stuck on a dead worker, so
// FindAllByPlayer/FindByContainer must fan out over a slice instead of a
// single *navigation.Ship.
type multiOrphanFakeShipRepo struct {
	navigation.ShipRepository

	ships  []*navigation.Ship
	onSave func()
	saves  []contractShipSnapshot
}

func (r *multiOrphanFakeShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, nil
}

func (r *multiOrphanFakeShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	var matched []*navigation.Ship
	for _, ship := range r.ships {
		if ship.ContainerID() == containerID {
			matched = append(matched, ship)
		}
	}
	return matched, nil
}

func (r *multiOrphanFakeShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	r.saves = append(r.saves, contractShipSnapshot{containerID: ship.ContainerID(), assigned: ship.IsAssigned()})
	if r.onSave != nil {
		r.onSave()
	}
	return nil
}

// alwaysErrContractRepo makes ContractMarketService.NegotiateContract fail
// immediately on FindActiveContracts, before it ever touches the mediator.
// This scenario's handler is built without a fleetPoolManager/mediator (like
// the other coordinator-loop fakes in this package) - the test cares only
// about whether the interrupted-worker reclaim pass runs when the fleet is
// NOT fully starved, not about what happens once a contract is negotiated.
// A nil mediator dereferenced past this point would panic instead of failing
// the assertion cleanly, so negotiation must be stopped here, one call early.
type alwaysErrContractRepo struct {
	domainContract.ContractRepository
}

func (alwaysErrContractRepo) FindActiveContracts(_ context.Context, _ int) ([]*domainContract.Contract, error) {
	return nil, errors.New("stub: negotiation not exercised by this test")
}

// newOrphanReclaimTestShip builds a second haul-capable ship with its own
// symbol, mirroring newNegotiateTestShip (which hardcodes "TORWIND-3") so a
// scenario can hold two distinct ships at once.
func newOrphanReclaimTestShip(t *testing.T, symbol string, status navigation.NavStatus) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(80, 0, nil)
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
		status,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// TestFleetCoordinator_ReclaimsOrphanedShipEvenWhenOtherShipsAreIdle covers
// the sp-tgp5 gap left after st-anu (commit 6bded3c): reclaim only ran
// reactively inside the "len(availableShips) == 0" starvation branch, so a
// ship orphaned by a daemon restart (assigned to a now-FAILED
// CONTRACT_WORKFLOW container - see markWorkerInterrupted, which
// deliberately preserves the assignment) stayed stuck forever as long as the
// rest of the fleet wasn't fully idle-starved. Two ships: one genuinely idle
// (available), one still "assigned" to a dead worker container. The
// coordinator must reclaim the orphan on its very first pass, not only when
// every other ship is also unavailable.
func TestFleetCoordinator_ReclaimsOrphanedShipEvenWhenOtherShipsAreIdle(t *testing.T) {
	orphan := newNegotiateTestShip(t, navigation.NavStatusInOrbit) // TORWIND-3
	if err := orphan.AssignToContainer("contract-work-dead", shared.NewRealClock()); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}
	idle := newOrphanReclaimTestShip(t, "TORWIND-4", navigation.NavStatusInOrbit) // genuinely idle, unassigned

	repo := &multiOrphanFakeShipRepo{ships: []*navigation.Ship{orphan, idle}}
	containerRepo := &reclaimFakeContainerRepo{byStatus: map[string][]persistence.ContainerSummary{
		"FAILED": {{ID: "contract-work-dead", ContainerType: "CONTRACT_WORKFLOW", Status: "FAILED"}},
	}}

	handler := &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(&spawnContractFakeDaemonClient{}, containerRepo, repo),
		contractMarketService:  contractServices.NewContractMarketService(nil, alwaysErrContractRepo{}),
		shipRepo:               repo,
		clock:                  &shared.MockClock{CurrentTime: time.Now()},
		eventSubscriber:        &reclaimFakeSubscriber{workerCompleted: make(chan navigation.WorkerCompletedEvent)},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	repo.onSave = cancel

	_, _ = handler.Handle(ctx, contractSpawnCommand())

	if len(repo.saves) == 0 {
		t.Fatalf("expected the ship orphaned by a dead worker to be reclaimed even though %s was idle, got no saves", idle.ShipSymbol())
	}
	if last := repo.saves[len(repo.saves)-1]; last.assigned {
		t.Fatalf("expected orphaned ship released from the dead worker, got %+v", last)
	}
	if !orphan.IsIdle() {
		t.Fatalf("expected orphaned ship back in the idle pool after reclaim, even with another ship idle")
	}
}

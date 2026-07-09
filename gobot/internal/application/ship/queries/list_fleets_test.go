package queries

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// listFleetsStubShipRepo embeds the domain interface so only FindAllByPlayer
// needs a concrete implementation; any unexpected call panics on a nil-method
// deref, surfacing accidental cache reads.
type listFleetsStubShipRepo struct {
	navigation.ShipRepository

	ships []*navigation.Ship
}

func (s *listFleetsStubShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return s.ships, nil
}

// newFleetTestShip builds a docked, unclaimed ship; callers layer on the
// dedication tag, a container claim, or an in-transit status per scenario.
func newFleetTestShip(t *testing.T, symbol string, navStatus navigation.NavStatus) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TW-A2", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		location,
		fuel,
		100,
		40,
		cargo,
		30,
		"FRAME_HAULER",
		"HAULER",
		nil,
		navStatus,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// Fleets exist implicitly as the distinct DedicatedFleet tags: the query must
// group members under their fleet, sort fleets by name and members by symbol
// (stable CLI output), and leave untagged ships out entirely.
func TestListFleets_GroupsTaggedShipsAndSortsDeterministically(t *testing.T) {
	contractB := newFleetTestShip(t, "TORWIND-5", navigation.NavStatusDocked)
	contractB.SetDedicatedFleet("contract")
	contractA := newFleetTestShip(t, "TORWIND-4", navigation.NavStatusDocked)
	contractA.SetDedicatedFleet("contract")
	bulk := newFleetTestShip(t, "TORWIND-19", navigation.NavStatusDocked)
	bulk.SetDedicatedFleet("bulk_circuit")
	untagged := newFleetTestShip(t, "TORWIND-1", navigation.NavStatusDocked)
	repo := &listFleetsStubShipRepo{ships: []*navigation.Ship{contractB, contractA, bulk, untagged}}
	handler := NewListFleetsHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &ListFleetsQuery{PlayerID: &pid})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	fleets := resp.(*ListFleetsResponse).Fleets
	if len(fleets) != 2 {
		t.Fatalf("expected 2 fleets (bulk_circuit, contract), got %d: %+v", len(fleets), fleets)
	}
	if fleets[0].Name != "bulk_circuit" || fleets[1].Name != "contract" {
		t.Fatalf("expected fleets sorted by name [bulk_circuit contract], got [%s %s]", fleets[0].Name, fleets[1].Name)
	}
	if len(fleets[1].Ships) != 2 || fleets[1].Ships[0].ShipSymbol != "TORWIND-4" || fleets[1].Ships[1].ShipSymbol != "TORWIND-5" {
		t.Fatalf("expected contract members sorted by symbol [TORWIND-4 TORWIND-5], got %+v", fleets[1].Ships)
	}
	for _, fleet := range fleets {
		for _, member := range fleet.Ships {
			if member.ShipSymbol == "TORWIND-1" {
				t.Fatalf("untagged ship TORWIND-1 must not appear in any fleet: %+v", fleets)
			}
		}
	}
}

// Idle means claimable right now: a free docked member is idle, one claimed
// by a container is not, and one mid-flight is not even without a claim —
// mirroring the coordinator's own dispatch predicate.
func TestListFleets_DerivesIdleFromClaimAndTransitState(t *testing.T) {
	idle := newFleetTestShip(t, "TORWIND-4", navigation.NavStatusDocked)
	idle.SetDedicatedFleet("contract")
	busy := newFleetTestShip(t, "TORWIND-5", navigation.NavStatusDocked)
	busy.SetDedicatedFleet("contract")
	if err := busy.AssignToContainer("contract-worker-TORWIND-5", shared.NewRealClock()); err != nil {
		t.Fatalf("assign busy member: %v", err)
	}
	inTransit := newFleetTestShip(t, "TORWIND-6", navigation.NavStatusInTransit)
	inTransit.SetDedicatedFleet("contract")
	repo := &listFleetsStubShipRepo{ships: []*navigation.Ship{idle, busy, inTransit}}
	handler := NewListFleetsHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &ListFleetsQuery{PlayerID: &pid})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	fleets := resp.(*ListFleetsResponse).Fleets
	if len(fleets) != 1 || len(fleets[0].Ships) != 3 {
		t.Fatalf("expected one fleet with 3 members, got %+v", fleets)
	}
	wantIdle := map[string]bool{"TORWIND-4": true, "TORWIND-5": false, "TORWIND-6": false}
	for _, member := range fleets[0].Ships {
		if member.Idle != wantIdle[member.ShipSymbol] {
			t.Fatalf("expected %s idle=%v, got %v", member.ShipSymbol, wantIdle[member.ShipSymbol], member.Idle)
		}
	}
}

// No dedicated ships means no fleets — an empty list, not an error, so the
// CLI can print its "every ship is in the general pool" message.
func TestListFleets_NoTaggedShips_ReturnsEmpty(t *testing.T) {
	untagged := newFleetTestShip(t, "TORWIND-1", navigation.NavStatusDocked)
	repo := &listFleetsStubShipRepo{ships: []*navigation.Ship{untagged}}
	handler := NewListFleetsHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &ListFleetsQuery{PlayerID: &pid})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if fleets := resp.(*ListFleetsResponse).Fleets; len(fleets) != 0 {
		t.Fatalf("expected no fleets when nothing is dedicated, got %+v", fleets)
	}
}

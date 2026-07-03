package contract

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// poolStubShipRepo embeds the domain interface so only FindAllByPlayer needs a
// concrete implementation; any other call panics on a nil-method deref.
type poolStubShipRepo struct {
	navigation.ShipRepository
	ships []*navigation.Ship
}

func (s *poolStubShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return s.ships, nil
}

func newPoolHauler(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	location, _ := shared.NewWaypoint("X1-PZ28-A1", 0, 0)
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		location,
		fuel,
		0,
		40,
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

func contains(symbols []string, target string) bool {
	for _, s := range symbols {
		if s == target {
			return true
		}
	}
	return false
}

// A reserved hauler must never be discovered by the coordinator idle-hauler pool.
// Before the reservation flag, whichever coordinator ran first auto-claimed the
// sole productive hauler; reserving it holds it out for a dedicated stream.
func TestFindIdleLightHaulers_ExcludesReservedShips(t *testing.T) {
	reserved := newPoolHauler(t, "TORWIND-4")
	reserved.Reserve("gate hauler")
	unreserved := newPoolHauler(t, "TORWIND-3")

	repo := &poolStubShipRepo{ships: []*navigation.Ship{reserved, unreserved}}

	ships, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if contains(symbols, "TORWIND-4") {
		t.Fatalf("reserved ship TORWIND-4 must be excluded from the idle-hauler pool, got %v", symbols)
	}
	if !contains(symbols, "TORWIND-3") {
		t.Fatalf("unreserved ship TORWIND-3 must remain in the idle-hauler pool, got %v", symbols)
	}
	if len(ships) != 1 {
		t.Fatalf("expected exactly one idle hauler (the unreserved one), got %d", len(ships))
	}
}

// Clearing the reservation returns the ship to the discoverable pool — the
// default (unreserved) behavior must be unchanged.
func TestFindIdleLightHaulers_UnreservedShipRemainsDiscoverable(t *testing.T) {
	ship := newPoolHauler(t, "TORWIND-4")
	ship.Reserve("gate hauler")
	ship.ClearReservation()

	repo := &poolStubShipRepo{ships: []*navigation.Ship{ship}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if !contains(symbols, "TORWIND-4") {
		t.Fatalf("after ClearReservation, TORWIND-4 must be discoverable again, got %v", symbols)
	}
}

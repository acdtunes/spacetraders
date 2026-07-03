package reservation

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// stubShipRepo embeds the domain interface so only FindBySymbol/Save need
// concrete implementations; any other call panics on a nil-method deref.
type stubShipRepo struct {
	navigation.ShipRepository
	ship      *navigation.Ship
	saved     *navigation.Ship
	saveCalls int
}

func (s *stubShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return s.ship, nil
}

func (s *stubShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	s.saveCalls++
	s.saved = ship
	return nil
}

func newTestShip(t *testing.T, symbol string) *navigation.Ship {
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
		symbol, shared.MustNewPlayerID(1), location, fuel,
		0, 40, cargo, 9, "FRAME_HAULER", "HAULER", nil, navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

func TestSetShipReservation_ReservesAndPersists(t *testing.T) {
	ship := newTestShip(t, "TORWIND-4")
	repo := &stubShipRepo{ship: ship}
	handler := NewSetShipReservationHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &SetShipReservationCommand{
		ShipSymbol: "TORWIND-4",
		Reserved:   true,
		Reason:     "gate hauler",
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if repo.saveCalls != 1 {
		t.Fatalf("expected reservation to be persisted once, got %d Save calls", repo.saveCalls)
	}
	if !repo.saved.IsReserved() {
		t.Fatalf("persisted ship must be reserved")
	}
	if repo.saved.ReservationReason() != "gate hauler" {
		t.Fatalf("reason not persisted, got %q", repo.saved.ReservationReason())
	}
	res := resp.(*SetShipReservationResponse)
	if !res.Reserved || res.Reason != "gate hauler" || res.ShipSymbol != "TORWIND-4" {
		t.Fatalf("unexpected response: %+v", res)
	}
}

func TestSetShipReservation_ClearsReservation(t *testing.T) {
	ship := newTestShip(t, "TORWIND-4")
	ship.Reserve("gate hauler")
	repo := &stubShipRepo{ship: ship}
	handler := NewSetShipReservationHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &SetShipReservationCommand{
		ShipSymbol: "TORWIND-4",
		Reserved:   false,
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if repo.saved.IsReserved() {
		t.Fatalf("ship must no longer be reserved after clear")
	}
	if repo.saved.ReservationReason() != "" {
		t.Fatalf("reason must be cleared, got %q", repo.saved.ReservationReason())
	}
	if res := resp.(*SetShipReservationResponse); res.Reserved {
		t.Fatalf("response must report unreserved")
	}
}

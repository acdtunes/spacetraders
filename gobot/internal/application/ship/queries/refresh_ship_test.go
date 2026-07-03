package queries

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// refreshStubShipRepo embeds the domain interface so only the methods the
// handler exercises need concrete implementations; any unexpected call panics
// on a nil-method deref, surfacing accidental cache reads.
type refreshStubShipRepo struct {
	navigation.ShipRepository

	syncedShip      *navigation.Ship
	syncCalledCount int
	findCalledCount int
}

func (s *refreshStubShipRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	s.syncCalledCount++
	return s.syncedShip, nil
}

func (s *refreshStubShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	s.findCalledCount++
	return nil, nil
}

func newRefreshTestShip(t *testing.T, symbol string, location *shared.Waypoint, cargoUnits int) *navigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	var inventory []*shared.CargoItem
	if cargoUnits > 0 {
		item, err := shared.NewCargoItem("IRON_ORE", "Iron Ore", "", cargoUnits)
		if err != nil {
			t.Fatalf("NewCargoItem: %v", err)
		}
		inventory = []*shared.CargoItem{item}
	}
	cargo, err := shared.NewCargo(40, cargoUnits, inventory)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
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

// Reproduces the phantom-cargo desync: the daemon cache holds 40/40 IRON_ORE the
// server says is 0. RefreshShip must force a write-through fetch (SyncShipFromAPI)
// and return the server-true state, never serving the stale cache via FindBySymbol.
func TestRefreshShip_ForcesWriteThroughAndReturnsServerState(t *testing.T) {
	location, _ := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	serverTrue := newRefreshTestShip(t, "TORWIND-1", location, 0)

	repo := &refreshStubShipRepo{syncedShip: serverTrue}

	handler := NewRefreshShipHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &RefreshShipQuery{
		ShipSymbol: "TORWIND-1",
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	refreshResp, ok := resp.(*RefreshShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if repo.syncCalledCount != 1 {
		t.Fatalf("expected exactly one write-through sync from API, got %d", repo.syncCalledCount)
	}
	if repo.findCalledCount != 0 {
		t.Fatalf("expected no stale cache read, got %d FindBySymbol calls", repo.findCalledCount)
	}
	if refreshResp.Ship.CargoUnits() != 0 {
		t.Fatalf("expected reconciled cargo of 0 units, got %d", refreshResp.Ship.CargoUnits())
	}
}

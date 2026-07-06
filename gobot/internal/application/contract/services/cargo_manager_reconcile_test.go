package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// reconcileFakeShipRepo embeds the full ShipRepository interface (nil) and only
// overrides FindBySymbol / SyncShipFromAPI. Any other method call panics, which
// keeps the fake honest about what the code under test actually uses.
type reconcileFakeShipRepo struct {
	navigation.ShipRepository
	cached     *navigation.Ship // what the (phantom) DB cache holds
	server     *navigation.Ship // authoritative server truth
	syncCalled bool
	findCalled bool
}

func (f *reconcileFakeShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	f.findCalled = true
	return f.cached, nil
}

func (f *reconcileFakeShipRepo) SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	f.syncCalled = true
	return f.server, nil
}

func buildShipWithIronOre(t *testing.T, units int) *navigation.Ship {
	t.Helper()

	waypoint, err := shared.NewWaypoint("X1-PZ28-H63", 1, 1)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}

	var inventory []*shared.CargoItem
	if units > 0 {
		item, err := shared.NewCargoItem("IRON_ORE", "Iron Ore", "ore", units)
		if err != nil {
			t.Fatalf("cargo item: %v", err)
		}
		inventory = append(inventory, item)
	}
	cargo, err := shared.NewCargo(40, units, inventory)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}

	ship, err := navigation.NewShip(
		"TORWIND-1",
		shared.MustNewPlayerID(1),
		waypoint,
		fuel,
		100,
		40,
		cargo,
		30,
		"FRAME_FRIGATE",
		"HAULER",
		nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

// Regression for the phantom-cargo incident (2026-07-02): the daemon's ship DB
// cache held 40 IRON_ORE the server never actually delivered, so contract
// delivery computed units from the phantom cache and failed server-side with a
// 4219 "Ship has 0 unit(s)" error. ReloadShipState must reconcile against the
// authoritative server record rather than trusting the stale cache.
func TestReloadShipStateReconcilesAgainstServer(t *testing.T) {
	repo := &reconcileFakeShipRepo{
		cached: buildShipWithIronOre(t, 40), // phantom cache
		server: buildShipWithIronOre(t, 0),  // server truth
	}

	mgr := NewCargoManager(nil, repo)

	_, currentUnits, err := mgr.ReloadShipState(context.Background(), "TORWIND-1", shared.MustNewPlayerID(1), "IRON_ORE")
	if err != nil {
		t.Fatalf("ReloadShipState: %v", err)
	}

	if !repo.syncCalled {
		t.Error("expected ReloadShipState to reconcile against the server via SyncShipFromAPI")
	}
	if currentUnits != 0 {
		t.Errorf("expected reconciled cargo of 0 units from the server, got %d (phantom cache)", currentUnits)
	}
}

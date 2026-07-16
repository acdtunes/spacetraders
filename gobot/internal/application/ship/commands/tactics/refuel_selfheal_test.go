package tactics

// Self-heal test for sp-yd84 SAFETY item 2 (refuel path).
//
// CUT 1's idempotent dock skip trusts the in-memory NavStatus. If that state has
// DRIFTED from server reality (a prior wrong skip, a game-side auto-undock, a
// stale DB row), a refuel whose dock was skipped will hit the live API's 4214
// "must be docked". The handler MUST recover — issue the missing dock and retry
// the refuel — so a wrong skip can never break the leg. This is the test that
// proves "does not break trade/nav".

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// driftRefuelRepo models a ship whose in-memory NavStatus disagrees with the
// server-side reality. Refuel rejects with the live 4214 wire form until the
// ship is actually docked (reality==DOCKED); Dock is the corrective API call.
type driftRefuelRepo struct {
	domainNavigation.ShipRepository // embedded: any unused method panics if hit

	reality     domainNavigation.NavStatus
	dockCalls   int
	refuelCalls int
}

func (r *driftRefuelRepo) Dock(_ context.Context, ship *domainNavigation.Ship, _ shared.PlayerID) error {
	r.dockCalls++
	r.reality = domainNavigation.NavStatusDocked
	_, _ = ship.EnsureDocked()
	return nil
}

func (r *driftRefuelRepo) Refuel(_ context.Context, ship *domainNavigation.Ship, _ shared.PlayerID, _ *int) (*domainNavigation.RefuelResult, error) {
	if r.reality != domainNavigation.NavStatusDocked {
		return nil, fmt.Errorf(`API error (status 400): {"error":{"code":4214,"message":"Ship %s must be docked to refuel."}}`, ship.ShipSymbol())
	}
	r.refuelCalls++
	_, _ = ship.RefuelToFull()
	return &domainNavigation.RefuelResult{
		CreditsCost:  0, // keeps the async ledger goroutine a no-op
		FuelCurrent:  ship.Fuel().Current,
		FuelCapacity: ship.Fuel().Capacity,
	}, nil
}

func (r *driftRefuelRepo) Save(_ context.Context, _ *domainNavigation.Ship) error { return nil }

func newShipAtFuelStation(t *testing.T, status domainNavigation.NavStatus) *domainNavigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-FUEL-1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	location.HasFuel = true
	fuel, err := shared.NewFuel(100, 400)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := domainNavigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), location, fuel, 400, 40, cargo,
		9, "FRAME_HAULER", "HAULER", nil, status,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// TestRefuelShipHandler_SelfHealsWrongDockSkip drives the RefuelShipHandler with
// a ship the in-memory state says is DOCKED while server reality is IN_ORBIT
// (drift). The idempotent dock skip therefore skips the dock, and the refuel
// hits 4214. The handler must self-heal: issue a real dock and retry, so the
// refuel still completes.
//
// MUTATION: removing the self-heal makes Handle return the raw 4214 error and
// leaves dockCalls==0 — this test fails, proving the safety net is load-bearing.
func TestRefuelShipHandler_SelfHealsWrongDockSkip(t *testing.T) {
	ship := newShipAtFuelStation(t, domainNavigation.NavStatusDocked)    // in-memory DOCKED (wrong)
	repo := &driftRefuelRepo{reality: domainNavigation.NavStatusInOrbit} // reality IN_ORBIT

	handler := NewRefuelShipHandler(repo, nil, nil, nil)

	resp, err := handler.Handle(context.Background(), &types.RefuelShipCommand{
		Ship:     ship,
		PlayerID: shared.MustNewPlayerID(1),
	})
	if err != nil {
		t.Fatalf("expected self-heal to complete the refuel despite drift, got error: %v", err)
	}
	if resp == nil {
		t.Fatalf("expected a refuel response, got nil")
	}
	if repo.dockCalls != 1 {
		t.Fatalf("expected the self-heal to issue exactly 1 corrective dock, got %d", repo.dockCalls)
	}
	if repo.refuelCalls != 1 {
		t.Fatalf("expected the refuel to succeed after re-docking, got %d successful refuel(s)", repo.refuelCalls)
	}
	if !ship.IsDocked() {
		t.Fatalf("expected ship DOCKED after self-heal, got %s", ship.NavStatus())
	}
}

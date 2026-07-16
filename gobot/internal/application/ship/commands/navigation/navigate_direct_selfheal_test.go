package navigation

// Self-heal test for sp-yd84 SAFETY item 2 (navigate path).
//
// The idempotent orbit skip (CUT 1) trusts the in-memory NavStatus. If that has
// DRIFTED from server reality (a prior wrong skip, a game-side dock, a stale DB
// row), a navigate whose orbit was skipped is rejected with the live API's 4236
// "not currently in orbit". The handler MUST recover — issue the missing orbit
// and retry the navigate — so a wrong skip can never break navigation. Mirrors
// the proven jumpWithOrbitRetry idiom (sp-28n2) in this same package.

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// driftNavigateRepo models a ship whose in-memory NavStatus disagrees with the
// server-side reality. Navigate rejects with the live 4236 wire form until the
// ship is actually in orbit (reality==IN_ORBIT); Orbit is the corrective call.
type driftNavigateRepo struct {
	domainNavigation.ShipRepository // embedded: any unused method panics if hit

	reality       domainNavigation.NavStatus
	orbitCalls    int
	navigateCalls int
}

func (r *driftNavigateRepo) Orbit(_ context.Context, ship *domainNavigation.Ship, _ shared.PlayerID) error {
	r.orbitCalls++
	r.reality = domainNavigation.NavStatusInOrbit
	_, _ = ship.EnsureInOrbit()
	return nil
}

func (r *driftNavigateRepo) Navigate(_ context.Context, ship *domainNavigation.Ship, destination *shared.Waypoint, _ shared.PlayerID) (*domainNavigation.Result, error) {
	if r.reality != domainNavigation.NavStatusInOrbit {
		return nil, fmt.Errorf(`API error (status 400): {"error":{"code":4236,"message":"Ship %s is not currently in orbit."}}`, ship.ShipSymbol())
	}
	r.navigateCalls++
	if err := ship.StartTransit(destination); err == nil {
		_ = ship.Arrive()
	}
	return &domainNavigation.Result{
		ArrivalTimeStr: "", // empty => caller skips the event wait
		FuelCurrent:    ship.Fuel().Current,
		FuelCapacity:   ship.Fuel().Capacity,
	}, nil
}

func (r *driftNavigateRepo) Save(_ context.Context, _ *domainNavigation.Ship) error { return nil }

func newNavShip(t *testing.T, location *shared.Waypoint, status domainNavigation.NavStatus) *domainNavigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(400, 400)
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

// TestNavigateDirectHandler_SelfHealsWrongOrbitSkip drives the handler with a
// ship the in-memory state says is IN_ORBIT while server reality is DOCKED
// (drift). The navigate is rejected with 4236; the handler must self-heal by
// issuing a real orbit and retrying, so the navigate still completes.
//
// MUTATION: removing the self-heal makes Handle return the raw 4236 error and
// leaves orbitCalls==0 — this test fails, proving the safety net is load-bearing.
func TestNavigateDirectHandler_SelfHealsWrongOrbitSkip(t *testing.T) {
	from, err := shared.NewWaypoint("X1-NAV-A", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	to, err := shared.NewWaypoint("X1-NAV-B", 100, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}

	ship := newNavShip(t, from, domainNavigation.NavStatusInOrbit)        // in-memory IN_ORBIT (wrong)
	repo := &driftNavigateRepo{reality: domainNavigation.NavStatusDocked} // reality DOCKED

	handler := NewNavigateDirectHandler(repo, nil)

	resp, err := handler.Handle(context.Background(), &types.NavigateDirectCommand{
		Ship:                ship,
		Destination:         to.Symbol,
		DestinationWaypoint: to,
		PlayerID:            shared.MustNewPlayerID(1),
		FlightMode:          "CRUISE",
	})
	if err != nil {
		t.Fatalf("expected self-heal to complete the navigate despite drift, got error: %v", err)
	}
	if resp == nil {
		t.Fatalf("expected a navigate response, got nil")
	}
	if repo.orbitCalls != 1 {
		t.Fatalf("expected the self-heal to issue exactly 1 corrective orbit, got %d", repo.orbitCalls)
	}
	if repo.navigateCalls != 1 {
		t.Fatalf("expected the navigate to succeed after orbiting, got %d successful navigate(s)", repo.navigateCalls)
	}
}

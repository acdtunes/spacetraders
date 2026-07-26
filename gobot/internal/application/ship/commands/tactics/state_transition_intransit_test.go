package tactics

// Dock/orbit vs an unrecorded server transit (arrival-state desync family):
// when a navigate's HTTP response is lost, the local snapshot still shows the
// hull parked at its old waypoint, and the next dock/orbit at that phantom
// position is rejected with the live 4214 "ship is currently in-transit".
// The tactic must adopt the AUTHORITATIVE server nav (SyncShipFromAPI) and
// return a typed ErrShipInTransit so the route layer can wait the transit out
// — instead of surfacing a raw error the container burns restart budget on.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// phantomPositionRepo rejects dock/orbit with the live 4214 wire form (the
// server knows a transit the local snapshot does not) — or dockErr when set —
// and serves the authoritative post-sync ship from SyncShipFromAPI.
type phantomPositionRepo struct {
	domainNavigation.ShipRepository // embedded: any unused method panics if hit

	fresh      *domainNavigation.Ship
	dockErr    error
	syncErr    error
	syncCalls  int
	dockCalls  int
	orbitCalls int
}

func inTransitRejection(ship *domainNavigation.Ship) error {
	return fmt.Errorf(`API error (status 400): {"error":{"code":4214,"message":"Ship %s is currently in-transit from X1-TAC-A to X1-TAC-B and arrives in 120 seconds.","data":{"destinationSymbol":"X1-TAC-B","secondsToArrival":120}}}`, ship.ShipSymbol())
}

func (r *phantomPositionRepo) Dock(_ context.Context, ship *domainNavigation.Ship, _ shared.PlayerID) error {
	r.dockCalls++
	if r.dockErr != nil {
		return r.dockErr
	}
	return fmt.Errorf("failed to dock ship: %w", inTransitRejection(ship))
}

func (r *phantomPositionRepo) Orbit(_ context.Context, ship *domainNavigation.Ship, _ shared.PlayerID) error {
	r.orbitCalls++
	return fmt.Errorf("failed to orbit ship: %w", inTransitRejection(ship))
}

func (r *phantomPositionRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	r.syncCalls++
	if r.syncErr != nil {
		return nil, r.syncErr
	}
	return r.fresh, nil
}

func newTransitTacticShip(t *testing.T, symbol string, location *shared.Waypoint, status domainNavigation.NavStatus) *domainNavigation.Ship {
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
		symbol, shared.MustNewPlayerID(1), location, fuel, 400, 40, cargo,
		9, "FRAME_HAULER", "HAULER", nil, status,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

func serverTransitShip(t *testing.T, symbol string) *domainNavigation.Ship {
	t.Helper()
	dest, err := shared.NewWaypoint("X1-TAC-B", 100, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	ship := newTransitTacticShip(t, symbol, dest, domainNavigation.NavStatusInTransit)
	ship.SetArrivalTime(time.Now().Add(120 * time.Second).UTC().Truncate(time.Second))
	return ship
}

func TestDockShip_ServerInTransit_AdoptsServerNavAndReturnsTypedError(t *testing.T) {
	origin, _ := shared.NewWaypoint("X1-TAC-A", 0, 0)
	// Local snapshot: parked in orbit at the phantom origin.
	ship := newTransitTacticShip(t, "TAC-1", origin, domainNavigation.NavStatusInOrbit)
	repo := &phantomPositionRepo{fresh: serverTransitShip(t, "TAC-1")}

	_, err := NewDockShipHandler(repo).Handle(context.Background(), &types.DockShipCommand{
		Ship:     ship,
		PlayerID: shared.MustNewPlayerID(1),
	})

	var transitErr *types.ErrShipInTransit
	if !errors.As(err, &transitErr) {
		t.Fatalf("expected typed ErrShipInTransit from a dock at a phantom position, got: %v", err)
	}
	if transitErr.Destination != "X1-TAC-B" {
		t.Fatalf("expected the server transit destination on the typed error, got %q", transitErr.Destination)
	}
	if repo.syncCalls != 1 {
		t.Fatalf("expected exactly 1 authoritative sync, got %d", repo.syncCalls)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInTransit || ship.CurrentLocation().Symbol != "X1-TAC-B" {
		t.Fatalf("expected the caller's ship to adopt the server transit, got %s at %s",
			ship.NavStatus(), ship.CurrentLocation().Symbol)
	}
}

func TestOrbitShip_ServerInTransit_AdoptsServerNavAndReturnsTypedError(t *testing.T) {
	origin, _ := shared.NewWaypoint("X1-TAC-A", 0, 0)
	// Local snapshot: docked at the phantom origin, so the orbit is a real API call.
	ship := newTransitTacticShip(t, "TAC-2", origin, domainNavigation.NavStatusDocked)
	repo := &phantomPositionRepo{fresh: serverTransitShip(t, "TAC-2")}

	_, err := NewOrbitShipHandler(repo).Handle(context.Background(), &types.OrbitShipCommand{
		Ship:     ship,
		PlayerID: shared.MustNewPlayerID(1),
	})

	var transitErr *types.ErrShipInTransit
	if !errors.As(err, &transitErr) {
		t.Fatalf("expected typed ErrShipInTransit from an orbit at a phantom position, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInTransit {
		t.Fatalf("expected the caller's ship to adopt the server transit, still %s", ship.NavStatus())
	}
}

// The adoption is strictly a 4214 path: any other dock failure must propagate
// untouched with NO authoritative resync and NO typed in-transit error — a
// misclassified adoption would overwrite live local state (cargo/fuel
// mutations included) from an unnecessary API read on every routine failure.
func TestDockShip_NonInTransitError_PropagatesWithoutResync(t *testing.T) {
	origin, _ := shared.NewWaypoint("X1-TAC-A", 0, 0)
	ship := newTransitTacticShip(t, "TAC-4", origin, domainNavigation.NavStatusInOrbit)
	repo := &phantomPositionRepo{
		dockErr: errors.New(`API error (status 503): {"error":{"message":"service unavailable"}}`),
	}

	_, err := NewDockShipHandler(repo).Handle(context.Background(), &types.DockShipCommand{
		Ship:     ship,
		PlayerID: shared.MustNewPlayerID(1),
	})

	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected the 503 rejection to propagate, got: %v", err)
	}
	var transitErr *types.ErrShipInTransit
	if errors.As(err, &transitErr) {
		t.Fatalf("a non-4214 failure must not be dressed as an in-transit adoption: %v", err)
	}
	if repo.syncCalls != 0 {
		t.Fatalf("a non-4214 failure must not trigger the authoritative resync, got %d sync call(s)", repo.syncCalls)
	}
}

func TestDockShip_ServerInTransitButSyncFails_PropagatesOriginalRejection(t *testing.T) {
	origin, _ := shared.NewWaypoint("X1-TAC-A", 0, 0)
	ship := newTransitTacticShip(t, "TAC-3", origin, domainNavigation.NavStatusInOrbit)
	repo := &phantomPositionRepo{syncErr: errors.New("api unavailable")}

	_, err := NewDockShipHandler(repo).Handle(context.Background(), &types.DockShipCommand{
		Ship:     ship,
		PlayerID: shared.MustNewPlayerID(1),
	})

	if err == nil {
		t.Fatalf("expected the original rejection to propagate when the authoritative sync is unavailable")
	}
	var transitErr *types.ErrShipInTransit
	if errors.As(err, &transitErr) {
		t.Fatalf("must not fabricate an adopted transit without the authoritative read, got typed error: %v", err)
	}
}

package navigation

// In-transit self-heal for the arrival-state desync family: a navigate whose
// HTTP response was lost client-side (timeout after the server had already
// applied it) gets silently re-sent by the client retry layer and rejected
// with 4214 "ship is in transit" — while the LOCAL state never recorded the
// transit. The handler must reconcile from the authoritative server nav
// (SyncShipFromAPI) instead of surfacing an error the container dies on:
//   - transit already heading to THIS command's destination => report the
//     navigation as in progress (it is), with the server's arrival;
//   - transit heading elsewhere => adopt the server nav locally and return a
//     typed ErrShipInTransit the route executor can wait out;
//   - sync unavailable => the original rejection propagates unchanged.

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

// Wire form captured verbatim from a live daemon.log 4214 rejection.
const inTransitWireBody = `API error (status 400): {"error":{"code":4214,"message":"Ship is currently in-transit from X1-NAV-A to X1-NAV-B and arrives in 201 seconds.","data":{"departureSymbol":"X1-NAV-A","destinationSymbol":"X1-NAV-B","arrival":"2026-07-25T22:08:12.312Z","departureTime":"2026-07-25T22:04:45.312Z","secondsToArrival":201}}}`

// inTransitNavigateRepo rejects every Navigate with the live 4214 wire form
// (the server knows a transit the local snapshot does not) — or navigateErr
// when set — and serves the authoritative post-sync ship from SyncShipFromAPI.
type inTransitNavigateRepo struct {
	domainNavigation.ShipRepository // embedded: any unused method panics if hit

	fresh         *domainNavigation.Ship
	navigateErr   error
	syncErr       error
	syncCalls     int
	navigateCalls int
}

func (r *inTransitNavigateRepo) Navigate(_ context.Context, _ *domainNavigation.Ship, _ *shared.Waypoint, _ shared.PlayerID) (*domainNavigation.Result, error) {
	r.navigateCalls++
	if r.navigateErr != nil {
		return nil, r.navigateErr
	}
	return nil, fmt.Errorf("failed to navigate ship: %w", errors.New(inTransitWireBody))
}

func (r *inTransitNavigateRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	r.syncCalls++
	if r.syncErr != nil {
		return nil, r.syncErr
	}
	return r.fresh, nil
}

// inTransitShip builds the authoritative post-sync snapshot: IN_TRANSIT with
// CurrentLocation()==destination (the domain invariant for a transiting hull)
// and the server's arrival clock.
func inTransitShip(t *testing.T, destination *shared.Waypoint, arrival time.Time) *domainNavigation.Ship {
	t.Helper()
	ship := newNavShip(t, destination, domainNavigation.NavStatusInTransit)
	ship.SetArrivalTime(arrival)
	return ship
}

func TestNavigateDirect_InTransitToSameDestination_ReportsNavigating(t *testing.T) {
	from, _ := shared.NewWaypoint("X1-NAV-A", 0, 0)
	to, _ := shared.NewWaypoint("X1-NAV-B", 100, 0)
	arrival := time.Now().Add(90 * time.Second).UTC().Truncate(time.Second)

	ship := newNavShip(t, from, domainNavigation.NavStatusInOrbit) // stale: knows nothing of the transit
	repo := &inTransitNavigateRepo{fresh: inTransitShip(t, to, arrival)}
	handler := NewNavigateDirectHandler(repo, nil)

	resp, err := handler.Handle(context.Background(), &types.NavigateDirectCommand{
		Ship:                ship,
		Destination:         to.Symbol,
		DestinationWaypoint: to,
		PlayerID:            shared.MustNewPlayerID(1),
		FlightMode:          "CRUISE",
	})
	if err != nil {
		t.Fatalf("expected the duplicate navigate to heal into an in-progress navigation, got error: %v", err)
	}
	navResp, ok := resp.(*types.NavigateDirectResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", resp)
	}
	if navResp.Status != "navigating" {
		t.Fatalf("expected status 'navigating', got %q", navResp.Status)
	}
	if want := arrival.Format(time.RFC3339); navResp.ArrivalTimeStr != want {
		t.Fatalf("expected the SERVER's arrival %q, got %q", want, navResp.ArrivalTimeStr)
	}
	if repo.syncCalls != 1 {
		t.Fatalf("expected exactly 1 authoritative sync, got %d", repo.syncCalls)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInTransit {
		t.Fatalf("expected the caller's ship to adopt the server transit, still %s", ship.NavStatus())
	}
	if got := ship.CurrentLocation().Symbol; got != to.Symbol {
		t.Fatalf("expected adopted transit destination %s, got %s", to.Symbol, got)
	}
}

func TestNavigateDirect_InTransitElsewhere_AdoptsStateAndReturnsTypedError(t *testing.T) {
	from, _ := shared.NewWaypoint("X1-NAV-A", 0, 0)
	to, _ := shared.NewWaypoint("X1-NAV-B", 100, 0)
	elsewhere, _ := shared.NewWaypoint("X1-NAV-C", 0, 100)
	arrival := time.Now().Add(90 * time.Second).UTC().Truncate(time.Second)

	ship := newNavShip(t, from, domainNavigation.NavStatusInOrbit)
	repo := &inTransitNavigateRepo{fresh: inTransitShip(t, elsewhere, arrival)}
	handler := NewNavigateDirectHandler(repo, nil)

	_, err := handler.Handle(context.Background(), &types.NavigateDirectCommand{
		Ship:                ship,
		Destination:         to.Symbol,
		DestinationWaypoint: to,
		PlayerID:            shared.MustNewPlayerID(1),
		FlightMode:          "CRUISE",
	})
	var transitErr *types.ErrShipInTransit
	if !errors.As(err, &transitErr) {
		t.Fatalf("expected a typed ErrShipInTransit for a transit to another waypoint, got: %v", err)
	}
	if transitErr.Destination != elsewhere.Symbol {
		t.Fatalf("expected typed error to carry the server transit destination %s, got %s", elsewhere.Symbol, transitErr.Destination)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInTransit || ship.CurrentLocation().Symbol != elsewhere.Symbol {
		t.Fatalf("expected the caller's ship to adopt the server transit to %s, got %s at %s",
			elsewhere.Symbol, ship.NavStatus(), ship.CurrentLocation().Symbol)
	}
}

func TestNavigateDirect_InTransitButSyncFails_PropagatesOriginalRejection(t *testing.T) {
	from, _ := shared.NewWaypoint("X1-NAV-A", 0, 0)
	to, _ := shared.NewWaypoint("X1-NAV-B", 100, 0)

	ship := newNavShip(t, from, domainNavigation.NavStatusInOrbit)
	repo := &inTransitNavigateRepo{syncErr: errors.New("api unavailable")}
	handler := NewNavigateDirectHandler(repo, nil)

	_, err := handler.Handle(context.Background(), &types.NavigateDirectCommand{
		Ship:                ship,
		Destination:         to.Symbol,
		DestinationWaypoint: to,
		PlayerID:            shared.MustNewPlayerID(1),
		FlightMode:          "CRUISE",
	})
	if err == nil {
		t.Fatalf("expected the original rejection to propagate when the authoritative sync is unavailable")
	}
	var transitErr *types.ErrShipInTransit
	if errors.As(err, &transitErr) {
		t.Fatalf("must not fabricate an adopted transit without the authoritative read, got typed error: %v", err)
	}
	if !strings.Contains(err.Error(), "4214") {
		t.Fatalf("expected the raw 4214 rejection in the chain, got: %v", err)
	}
	if repo.syncCalls != 1 {
		t.Fatalf("expected the sync to have been attempted once, got %d", repo.syncCalls)
	}
}

// The race window's OTHER outcome: the sync shows the hull no longer in
// transit but parked somewhere that is NOT this command's destination. The
// handler must NOT mint a success for a hop that never happened — that would
// re-create the phantom-position desync this reconcile exists to kill. The
// original rejection propagates (the caller re-plans) while the local ship
// keeps the adopted, truthful position.
func TestNavigateDirect_InTransitRaceParkedElsewhere_PropagatesRejectionWithTruthfulState(t *testing.T) {
	from, _ := shared.NewWaypoint("X1-NAV-A", 0, 0)
	to, _ := shared.NewWaypoint("X1-NAV-B", 100, 0)
	elsewhere, _ := shared.NewWaypoint("X1-NAV-C", 0, 100)

	ship := newNavShip(t, from, domainNavigation.NavStatusInOrbit)
	// Authoritative read: the transit ended and left the hull parked at C.
	repo := &inTransitNavigateRepo{fresh: newNavShip(t, elsewhere, domainNavigation.NavStatusInOrbit)}
	handler := NewNavigateDirectHandler(repo, nil)

	resp, err := handler.Handle(context.Background(), &types.NavigateDirectCommand{
		Ship:                ship,
		Destination:         to.Symbol,
		DestinationWaypoint: to,
		PlayerID:            shared.MustNewPlayerID(1),
		FlightMode:          "CRUISE",
	})
	if err == nil {
		t.Fatalf("must not report success for a navigate the server never accepted; got response %#v", resp)
	}
	var transitErr *types.ErrShipInTransit
	if errors.As(err, &transitErr) {
		t.Fatalf("hull is no longer in transit - a typed in-transit error would make callers wait on nothing: %v", err)
	}
	if !strings.Contains(err.Error(), "4214") {
		t.Fatalf("expected the original 4214 rejection in the chain, got: %v", err)
	}
	if ship.NavStatus() != domainNavigation.NavStatusInOrbit || ship.CurrentLocation().Symbol != elsewhere.Symbol {
		t.Fatalf("expected the adopted truthful position %s IN_ORBIT, got %s at %s",
			elsewhere.Symbol, ship.NavStatus(), ship.CurrentLocation().Symbol)
	}
}

// The in-transit reconcile is strictly a 4214 path: any other navigate
// failure must propagate untouched with NO authoritative resync — a
// misclassified reconcile would burn an API read per failure and could mint
// phantom successes for unrelated rejections.
func TestNavigateDirect_NonInTransitError_PropagatesWithoutResync(t *testing.T) {
	from, _ := shared.NewWaypoint("X1-NAV-A", 0, 0)
	to, _ := shared.NewWaypoint("X1-NAV-B", 100, 0)

	ship := newNavShip(t, from, domainNavigation.NavStatusInOrbit)
	repo := &inTransitNavigateRepo{
		navigateErr: errors.New(`API error (status 400): {"error":{"code":4203,"message":"Navigate request failed. Ship SHIP-1 requires 12 more fuel for navigation."}}`),
	}
	handler := NewNavigateDirectHandler(repo, nil)

	_, err := handler.Handle(context.Background(), &types.NavigateDirectCommand{
		Ship:                ship,
		Destination:         to.Symbol,
		DestinationWaypoint: to,
		PlayerID:            shared.MustNewPlayerID(1),
		FlightMode:          "CRUISE",
	})
	if err == nil || !strings.Contains(err.Error(), "4203") {
		t.Fatalf("expected the 4203 rejection to propagate, got: %v", err)
	}
	if repo.syncCalls != 0 {
		t.Fatalf("a non-4214 failure must not trigger the authoritative resync, got %d sync call(s)", repo.syncCalls)
	}
}

func TestNavigateDirect_InTransitRaceAlreadyArrivedAtDestination_ReportsAlreadyThere(t *testing.T) {
	from, _ := shared.NewWaypoint("X1-NAV-A", 0, 0)
	to, _ := shared.NewWaypoint("X1-NAV-B", 100, 0)

	ship := newNavShip(t, from, domainNavigation.NavStatusInOrbit)
	// The transit finished between the 4214 rejection and the sync: the
	// authoritative read shows the hull parked at this command's destination.
	repo := &inTransitNavigateRepo{fresh: newNavShip(t, to, domainNavigation.NavStatusInOrbit)}
	handler := NewNavigateDirectHandler(repo, nil)

	resp, err := handler.Handle(context.Background(), &types.NavigateDirectCommand{
		Ship:                ship,
		Destination:         to.Symbol,
		DestinationWaypoint: to,
		PlayerID:            shared.MustNewPlayerID(1),
		FlightMode:          "CRUISE",
	})
	if err != nil {
		t.Fatalf("expected an arrived-during-race navigate to heal, got error: %v", err)
	}
	navResp, ok := resp.(*types.NavigateDirectResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", resp)
	}
	if navResp.Status != "already_at_destination" {
		t.Fatalf("expected status 'already_at_destination', got %q", navResp.Status)
	}
}

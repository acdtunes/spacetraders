package ship

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// This file pins the write-loss that stranded TORWIND-41: a warp moved the hull from
// X1-GF41 to X1-KC84 and NOTHING wrote the new position to the ships row. Every warp
// mutated only the in-memory *navigation.Ship the caller happened to be holding, so the
// durable row — the one thing the next tick re-derives from (RULINGS #2) — kept naming
// the ORIGIN system forever. The planner then faithfully computed a jump out of a system
// the hull had already left, the API refused it (4255), and the loop never broke.
//
// Warp was the only cross-system mover with no durable write at all. Navigate persists at
// departure (ShipRepository.Navigate -> navigateColumns) and jump persists after the API
// returns (jump_ship.go SaveWithRetry); warp did neither.
//
// WHY THE OLD TESTS MISSED IT: every existing warp test constructs the executor with a
// NIL ship repository and asserts on the in-memory hull. A test that can only see the
// object the code mutates can never notice that nothing was written. The fake below is
// therefore built the other way round: its durable row is a SEPARATE Ship rebuilt from
// stored values on every read, so an in-memory mutation alone proves nothing.

// --- durable-row test double ----------------------------------------------

// durableShipRow models the ships row: the fields a warp is supposed to write, held
// independently of any Ship the executor is mutating.
type durableShipRow struct {
	locationSymbol string
	locationX      float64
	locationY      float64
	navStatus      domainNavigation.NavStatus
	arrivalTime    *time.Time
	fuelCurrent    int
	fuelCapacity   int
}

// durableRowShipRepo is the driven-port double at the persistence boundary. It stores a
// durable ROW and rebuilds a fresh *Ship from it on every FindBySymbol, exactly as the
// production repository does (modelToDomain). SaveWithRetry re-reads that fresh hull,
// applies the caller's mutation to it and folds the result back into the row — so the
// only way the row can change is a genuine write through the port.
//
// saveFailure, when set, is returned by SaveWithRetry to drive the fail-closed path.
type durableRowShipRepo struct {
	domainNavigation.ShipRepository // embedded: any method a test does not stub panics

	mu     sync.Mutex
	symbol string
	row    durableShipRow
	// departureRow is the row as it stood after the FIRST write of the leg. Keeping it
	// separate is what lets a test assert the row passed THROUGH the in-transit state
	// rather than only ending in the right place — the sweeper reads the row mid-flight,
	// not at the end.
	departureRow durableShipRow
	saves        int
	saveFailure  error
	orbitCalls   int
}

func newDurableRowShipRepo(location *shared.Waypoint, fuelCurrent, fuelCapacity int) *durableRowShipRepo {
	return &durableRowShipRepo{
		symbol: "TORWIND-41",
		row: durableShipRow{
			locationSymbol: location.Symbol,
			locationX:      location.X,
			locationY:      location.Y,
			navStatus:      domainNavigation.NavStatusInOrbit,
			fuelCurrent:    fuelCurrent,
			fuelCapacity:   fuelCapacity,
		},
	}
}

// hullFromRow rebuilds the persisted hull. Callers get a NEW object every time, so a
// mutation applied to some other Ship instance can never leak into an assertion.
func (r *durableRowShipRepo) hullFromRow() (*domainNavigation.Ship, error) {
	waypoint, err := shared.NewWaypoint(r.row.locationSymbol, r.row.locationX, r.row.locationY)
	if err != nil {
		return nil, err
	}
	fuel, err := shared.NewFuel(r.row.fuelCurrent, r.row.fuelCapacity)
	if err != nil {
		return nil, err
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		return nil, err
	}
	warpModule := domainNavigation.NewShipModule("MODULE_WARP_DRIVE_I", 0, 0, domainNavigation.ShipRequirements{})
	hull, err := domainNavigation.NewShip(
		r.symbol,
		shared.MustNewPlayerID(1),
		waypoint,
		fuel,
		r.row.fuelCapacity,
		40,
		cargo,
		9,
		"FRAME_EXPLORER",
		"EXPLORER",
		[]*domainNavigation.ShipModule{warpModule},
		r.row.navStatus,
	)
	if err != nil {
		return nil, err
	}
	if r.row.arrivalTime != nil {
		hull.SetArrivalTime(*r.row.arrivalTime)
	}
	return hull, nil
}

func (r *durableRowShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hullFromRow()
}

func (r *durableRowShipRepo) SaveWithRetry(_ context.Context, _ string, _ shared.PlayerID, mutate domainNavigation.ShipMutation) (*domainNavigation.Ship, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveFailure != nil {
		return nil, false, r.saveFailure
	}
	fresh, err := r.hullFromRow()
	if err != nil {
		return nil, false, err
	}
	changed, err := mutate(fresh)
	if err != nil {
		return nil, false, err
	}
	if !changed {
		return fresh, false, nil
	}
	r.saves++
	r.row.locationSymbol = fresh.CurrentLocation().Symbol
	r.row.locationX = fresh.CurrentLocation().X
	r.row.locationY = fresh.CurrentLocation().Y
	r.row.navStatus = fresh.NavStatus()
	r.row.arrivalTime = fresh.ArrivalTime()
	if fresh.Fuel() != nil {
		r.row.fuelCurrent = fresh.Fuel().Current
		r.row.fuelCapacity = fresh.Fuel().Capacity
	}
	if r.saves == 1 {
		r.departureRow = r.row
	}
	return fresh, true, nil
}

// warpRepoFor seeds a durable row from a hull a test has already built. It exists so
// every EXISTING warp test gets a real persistence boundary instead of the nil repository
// that made this whole class of defect invisible: a warp test that cannot see writes
// cannot notice their absence.
func warpRepoFor(hull *domainNavigation.Ship) *durableRowShipRepo {
	repo := newDurableRowShipRepo(hull.CurrentLocation(), hull.Fuel().Current, hull.Fuel().Capacity)
	repo.symbol = hull.ShipSymbol()
	return repo
}

// arrivedImmediatelySubscriber delivers the ARRIVED event the instant the executor
// subscribes, so the in-transit branch runs end to end without a real wait.
type arrivedImmediatelySubscriber struct {
	stubSubscriber
}

func (arrivedImmediatelySubscriber) SubscribeArrived(shipSymbol string) <-chan domainNavigation.ShipArrivedEvent {
	ch := make(chan domainNavigation.ShipArrivedEvent, 1)
	ch <- domainNavigation.ShipArrivedEvent{ShipSymbol: shipSymbol}
	return ch
}

func (arrivedImmediatelySubscriber) UnsubscribeArrived(string, <-chan domainNavigation.ShipArrivedEvent) {
}

// Orbit is reached by the executor's pre-warp ensureShipInOrbit. It updates the row's
// nav status only, mirroring the production scoped write (navStatusColumns).
func (r *durableRowShipRepo) Orbit(_ context.Context, hull *domainNavigation.Ship, _ shared.PlayerID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orbitCalls++
	_, _ = hull.EnsureInOrbit()
	r.row.navStatus = domainNavigation.NavStatusInOrbit
	return nil
}

// persistedSystem is the system the NEXT TICK would plan a route from.
func (r *durableRowShipRepo) persistedSystem() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return shared.ExtractSystemSymbol(r.row.locationSymbol)
}

func (r *durableRowShipRepo) persistedRow() durableShipRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.row
}

// arrivingWarpNavigator returns a warp result that puts the hull IN TRANSIT with a real
// arrival time — the shape the live warp API actually returns for a cross-system leg.
type arrivingWarpNavigator struct {
	arrival    string
	fuelAfter  int
	legs       []string
	failAfter  int // legs to serve before returning an error (0 = never fail)
	legFailure error
}

func (n *arrivingWarpNavigator) Warp(_ context.Context, hull *domainNavigation.Ship, destination *shared.Waypoint, _ shared.PlayerID) (*domainNavigation.Result, error) {
	if n.failAfter > 0 && len(n.legs) >= n.failAfter {
		return nil, n.legFailure
	}
	n.legs = append(n.legs, destination.Symbol)
	return &domainNavigation.Result{
		Destination:    destination.Symbol,
		ArrivalTimeStr: n.arrival,
		FuelCurrent:    n.fuelAfter,
		FuelCapacity:   hull.Fuel().Capacity,
	}, nil
}

// --- the production reproduction ------------------------------------------

// TestExecuteWarpLeg_PersistsTheNewSystem_SoTheNextTickDoesNotPlanFromTheStaleOrigin is
// the TORWIND-41 regression, stated in the incident's own symbols. The hull warps out of
// X1-GF41 into X1-KC84. The assertion is deliberately made against the DURABLE ROW rather
// than the in-memory hull, because "the next tick re-derives from durable state" is the
// property that actually failed: the row said X1-GF41 while the hull stood in X1-KC84, so
// every subsequent route was planned out of a system the hull had already left.
func TestExecuteWarpLeg_PersistsTheNewSystem_SoTheNextTickDoesNotPlanFromTheStaleOrigin(t *testing.T) {
	origin := mustWaypoint(t, "X1-GF41-I57", 0, 0)
	destination := mustWaypoint(t, "X1-KC84-A1", 100, 0)

	repo := newDurableRowShipRepo(origin, 800, 800)
	hull := newWarpExplorerShip(t, 800, 800, origin)
	// A leg that lands immediately (no arrival clock) is the simplest shape that still
	// completes a cross-system move.
	warp := &arrivingWarpNavigator{arrival: "", fuelAfter: 700}

	executor := NewRouteExecutor(repo, &warpRefuelMediator{}, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, &spyCharter{}, escapableSystems("X1-KC84"))

	if err := executor.ExecuteWarpLeg(context.Background(), hull, destination, shared.MustNewPlayerID(1)); err != nil {
		t.Fatalf("warp to a reachable system should succeed: %v", err)
	}

	if got := repo.persistedSystem(); got != "X1-KC84" {
		t.Fatalf("durable row still names %s after a completed warp into X1-KC84 — the next tick will plan a route out of a system the hull has left (the TORWIND-41 4255 loop); row location=%s",
			got, repo.persistedRow().locationSymbol)
	}
	if got := repo.persistedRow().locationSymbol; got != destination.Symbol {
		t.Fatalf("expected the durable row to name the arrival waypoint %s, got %s", destination.Symbol, got)
	}
}

// TestExecuteWarpLeg_PersistsTheDepartureSoTheStuckShipSweeperCanSeeTheHull pins the
// second half of the loss. A warp that leaves the hull genuinely IN TRANSIT must write
// that transit to the row: nav_status=IN_TRANSIT plus the arrival clock. Without it the
// row never mentions the transit, so ShipStateScheduler's sweeper — which selects on
// nav_status='IN_TRANSIT' AND arrival_time<=now — is structurally blind to the hull and
// the whole arrival safety net is inert for warps.
func TestExecuteWarpLeg_PersistsTheDepartureSoTheStuckShipSweeperCanSeeTheHull(t *testing.T) {
	origin := mustWaypoint(t, "X1-GF41-I57", 0, 0)
	destination := mustWaypoint(t, "X1-KC84-A1", 100, 0)
	arrival := time.Now().Add(90 * time.Second).UTC().Format(time.RFC3339)

	repo := newDurableRowShipRepo(origin, 800, 800)
	hull := newWarpExplorerShip(t, 800, 800, origin)
	warp := &arrivingWarpNavigator{arrival: arrival, fuelAfter: 700}

	// A subscriber that reports the ARRIVED event immediately keeps the test fast while
	// still driving the real in-transit branch.
	executor := NewRouteExecutor(repo, &warpRefuelMediator{}, nil, nil, nil, nil, nil, arrivedImmediatelySubscriber{}).
		WithWarpSupport(warp, &spyCharter{}, escapableSystems("X1-KC84"))

	if err := executor.ExecuteWarpLeg(context.Background(), hull, destination, shared.MustNewPlayerID(1)); err != nil {
		t.Fatalf("warp should succeed: %v", err)
	}

	// The departure write must have happened BEFORE the arrival write, so the row passed
	// through IN_TRANSIT-at-destination rather than jumping straight to the final state.
	if repo.departureRow.navStatus != domainNavigation.NavStatusInTransit {
		t.Fatalf("expected the departure write to record IN_TRANSIT, got %q — the sweeper's nav_status='IN_TRANSIT' predicate can never match this hull",
			repo.departureRow.navStatus)
	}
	if repo.departureRow.arrivalTime == nil {
		t.Fatalf("expected the departure write to record an arrival clock — the sweeper also requires arrival_time IS NOT NULL")
	}
	if repo.departureRow.locationSymbol != destination.Symbol {
		t.Fatalf("expected the departure write to name the destination %s, got %s", destination.Symbol, repo.departureRow.locationSymbol)
	}
}

// TestExecuteWarpRoute_PersistsEachCompletedLegBeforeAttemptingTheNext pins the multi-leg
// case. A route whose second leg is refused must still leave the row naming the system
// the first leg actually reached — otherwise one failed leg discards a completed
// inter-system move and the hull is lost two systems away from its row.
func TestExecuteWarpRoute_PersistsEachCompletedLegBeforeAttemptingTheNext(t *testing.T) {
	origin := mustWaypoint(t, "X1-GF41-I57", 0, 0)
	firstLeg := mustWaypoint(t, "X1-KC84-A1", 100, 0)
	secondLeg := mustWaypoint(t, "X1-AJ10-B1", 200, 0)

	repo := newDurableRowShipRepo(origin, 800, 800)
	hull := newWarpExplorerShip(t, 800, 800, origin)
	warp := &arrivingWarpNavigator{
		arrival:    "",
		fuelAfter:  700,
		failAfter:  1,
		legFailure: errors.New("server refused the onward leg"),
	}

	executor := NewRouteExecutor(repo, &warpRefuelMediator{}, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, &spyCharter{}, escapableSystems("X1-KC84", "X1-AJ10"))

	err := executor.ExecuteWarpRoute(context.Background(), hull, []*shared.Waypoint{firstLeg, secondLeg}, shared.MustNewPlayerID(1))
	if err == nil {
		t.Fatalf("expected the refused second leg to fail the route")
	}

	if got := repo.persistedSystem(); got != "X1-KC84" {
		t.Fatalf("a completed first leg was discarded by the failure of the second: durable row names %s, hull is physically in X1-KC84", got)
	}
}

// TestExecuteWarpLeg_FailsClosedWhenTheDeparturePositionCannotBePersisted pins the
// RULINGS #4 direction of the new write. A warp whose position cannot be recorded has
// left the fleet with a row it KNOWS is wrong; continuing to fly the hull from that row
// is what produced the original incident. The leg must fail loudly instead of reporting
// a success whose position was silently dropped.
func TestExecuteWarpLeg_FailsClosedWhenTheDeparturePositionCannotBePersisted(t *testing.T) {
	origin := mustWaypoint(t, "X1-GF41-I57", 0, 0)
	destination := mustWaypoint(t, "X1-KC84-A1", 100, 0)

	repo := newDurableRowShipRepo(origin, 800, 800)
	repo.saveFailure = errors.New("ships row locked")
	hull := newWarpExplorerShip(t, 800, 800, origin)
	warp := &arrivingWarpNavigator{arrival: "", fuelAfter: 700}

	executor := NewRouteExecutor(repo, &warpRefuelMediator{}, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, &spyCharter{}, escapableSystems("X1-KC84"))

	err := executor.ExecuteWarpLeg(context.Background(), hull, destination, shared.MustNewPlayerID(1))
	if err == nil {
		t.Fatalf("a warp whose new position could not be persisted must fail closed, not report success")
	}
}

// TestExecuteWarpLeg_RecordsTheLandingSoTheRowStopsClaimingATransit pins the second
// write. Once the hull is down, the row must say so: a row left claiming a transit that
// has already ended keeps the hull inside the arrival machinery instead of returning it
// to the idle pool, and its arrival clock keeps naming a moment that has passed.
func TestExecuteWarpLeg_RecordsTheLandingSoTheRowStopsClaimingATransit(t *testing.T) {
	origin := mustWaypoint(t, "X1-GF41-I57", 0, 0)
	destination := mustWaypoint(t, "X1-KC84-A1", 100, 0)
	arrival := time.Now().Add(90 * time.Second).UTC().Format(time.RFC3339)

	repo := newDurableRowShipRepo(origin, 800, 800)
	hull := newWarpExplorerShip(t, 800, 800, origin)
	warp := &arrivingWarpNavigator{arrival: arrival, fuelAfter: 700}

	executor := NewRouteExecutor(repo, &warpRefuelMediator{}, nil, nil, nil, nil, nil, arrivedImmediatelySubscriber{}).
		WithWarpSupport(warp, &spyCharter{}, escapableSystems("X1-KC84"))

	if err := executor.ExecuteWarpLeg(context.Background(), hull, destination, shared.MustNewPlayerID(1)); err != nil {
		t.Fatalf("warp should succeed: %v", err)
	}

	final := repo.persistedRow()
	if final.navStatus != domainNavigation.NavStatusInOrbit {
		t.Fatalf("expected the landed hull's row to read IN_ORBIT, got %q — the row still claims a transit that has ended", final.navStatus)
	}
	if final.arrivalTime != nil {
		t.Fatalf("expected the landed hull's row to carry no arrival clock, got %v", final.arrivalTime)
	}
	if final.locationSymbol != destination.Symbol {
		t.Fatalf("expected the landed row to name %s, got %s", destination.Symbol, final.locationSymbol)
	}
}

// TestExecuteWarpLeg_RefusesAWarpItCannotRecord pins the precondition. An executor with
// no persistence boundary can move a hull but cannot write down where it went — which is
// precisely the defect this file exists for. It must refuse BEFORE the warp call, so no
// hull is ever moved by a path that will lose the move.
func TestExecuteWarpLeg_RefusesAWarpItCannotRecord(t *testing.T) {
	origin := mustWaypoint(t, "X1-GF41-I57", 0, 0)
	destination := mustWaypoint(t, "X1-KC84-A1", 100, 0)

	hull := newWarpExplorerShip(t, 800, 800, origin)
	warp := &arrivingWarpNavigator{arrival: "", fuelAfter: 700}

	executor := NewRouteExecutor(nil, &warpRefuelMediator{}, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, &spyCharter{}, escapableSystems("X1-KC84"))

	err := executor.ExecuteWarpLeg(context.Background(), hull, destination, shared.MustNewPlayerID(1))
	if err == nil {
		t.Fatalf("a warp with nowhere to record the move must be refused")
	}
	if len(warp.legs) != 0 {
		t.Fatalf("the hull was moved by a path that cannot record the move: warp legs %v", warp.legs)
	}
}

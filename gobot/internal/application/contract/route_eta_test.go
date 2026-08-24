package contract

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	etaTestSystem = "X1-TW"
	etaTestGoal   = "X1-TW-GOAL"
)

// fakeRoutePlanner records concurrency (peak in-flight calls) and answers
// PlanRoute per candidate, keyed by the request's StartWaypoint.
type fakeRoutePlanner struct {
	mu          sync.Mutex
	inFlight    int32
	maxInFlight int32
	perShip     map[string]fakeAnswer
	delay       time.Duration
}

type fakeAnswer struct {
	seconds int
	err     error
}

func (f *fakeRoutePlanner) PlanRoute(ctx context.Context, req *routing.RouteRequest) (*routing.RouteResponse, error) {
	cur := atomic.AddInt32(&f.inFlight, 1)
	defer atomic.AddInt32(&f.inFlight, -1)
	for {
		prev := atomic.LoadInt32(&f.maxInFlight)
		if cur <= prev || atomic.CompareAndSwapInt32(&f.maxInFlight, prev, cur) {
			break
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	a := f.perShip[req.StartWaypoint]
	if a.err != nil {
		return nil, a.err
	}
	return &routing.RouteResponse{TotalTimeSeconds: a.seconds}, nil
}

func testClock() *shared.MockClock {
	return &shared.MockClock{CurrentTime: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

// newETAShip builds an idle (docked) hull standing at waypointSymbol - a
// distinct location per ship so the fake can key a distinct answer per candidate.
func newETAShip(t *testing.T, symbol, waypointSymbol string) *navigation.Ship {
	t.Helper()
	return newETAShipWithStatus(t, symbol, waypointSymbol, navigation.NavStatusDocked, nil)
}

// newInTransitETAShip builds an IN_TRANSIT hull already standing at its
// destination waypointSymbol (CurrentLocation is the destination mid-transit),
// with the given arrival time - nil reproduces the unpriceable case.
func newInTransitETAShip(t *testing.T, symbol, waypointSymbol string, arrival *time.Time) *navigation.Ship {
	t.Helper()
	return newETAShipWithStatus(t, symbol, waypointSymbol, navigation.NavStatusInTransit, arrival)
}

func newETAShipWithStatus(t *testing.T, symbol, waypointSymbol string, status navigation.NavStatus, arrival *time.Time) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(80, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	wp, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), wp, fuel, 100, 40, cargo, 30, "FRAME_LIGHT_HAULER", "HAULER", nil, status)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	if arrival != nil {
		ship.SetArrivalTime(*arrival)
	}
	return ship
}

func TestRouteETA_HappyPath_AllPriced_OKTrue(t *testing.T) {
	shipA := newETAShip(t, "TORWIND-A", "X1-TW-A1")
	shipB := newETAShip(t, "TORWIND-B", "X1-TW-A2")
	fake := &fakeRoutePlanner{perShip: map[string]fakeAnswer{
		"X1-TW-A1": {seconds: 42},
		"X1-TW-A2": {seconds: 77},
	}}
	estimator := NewRouteETAEstimator(fake, testClock(), 0)

	result := estimator.EstimateAll(context.Background(), []*navigation.Ship{shipA, shipB}, etaTestSystem, etaTestGoal, map[string]*shared.Waypoint{})

	if !result.OK {
		t.Fatalf("expected OK=true, got false (dropped=%v)", result.Dropped)
	}
	if result.ETAs["TORWIND-A"] != 42 {
		t.Fatalf("expected TORWIND-A ETA 42, got %v", result.ETAs["TORWIND-A"])
	}
	if result.ETAs["TORWIND-B"] != 77 {
		t.Fatalf("expected TORWIND-B ETA 77, got %v", result.ETAs["TORWIND-B"])
	}
	if len(result.Dropped) != 0 {
		t.Fatalf("expected no drops, got %v", result.Dropped)
	}
}

func TestRouteETA_InTransitHull_AddsRemainingTransit(t *testing.T) {
	clock := testClock()
	arrival := clock.CurrentTime.Add(90 * time.Second)
	transiting := newInTransitETAShip(t, "TORWIND-C", "X1-TW-C", &arrival)
	unpriceable := newInTransitETAShip(t, "TORWIND-D", "X1-TW-D", nil)
	fake := &fakeRoutePlanner{perShip: map[string]fakeAnswer{
		"X1-TW-C": {seconds: 60},
	}}
	estimator := NewRouteETAEstimator(fake, clock, 0)

	result := estimator.EstimateAll(context.Background(), []*navigation.Ship{transiting, unpriceable}, etaTestSystem, etaTestGoal, map[string]*shared.Waypoint{})

	if !result.OK {
		t.Fatalf("expected OK=true (one hull still priced), got false")
	}
	if result.ETAs["TORWIND-C"] != 150 {
		t.Fatalf("expected TORWIND-C ETA 90s remaining + 60s route = 150, got %v", result.ETAs["TORWIND-C"])
	}
	if !containsSymbol(result.Dropped, "TORWIND-D") {
		t.Fatalf("expected TORWIND-D (nil ArrivalTime) in Dropped, got %v", result.Dropped)
	}
	if _, priced := result.ETAs["TORWIND-D"]; priced {
		t.Fatalf("TORWIND-D must not be priced, it has no arrival time to add")
	}
}

func TestRouteETA_OneUnroutable_DroppedOthersKept(t *testing.T) {
	good := newETAShip(t, "TORWIND-E", "X1-TW-E")
	bad := newETAShip(t, "TORWIND-F", "X1-TW-F")
	fake := &fakeRoutePlanner{perShip: map[string]fakeAnswer{
		"X1-TW-E": {seconds: 55},
		"X1-TW-F": {err: errors.New("no route found")},
	}}
	estimator := NewRouteETAEstimator(fake, testClock(), 0)

	result := estimator.EstimateAll(context.Background(), []*navigation.Ship{good, bad}, etaTestSystem, etaTestGoal, map[string]*shared.Waypoint{})

	if !result.OK {
		t.Fatalf("expected OK=true (one hull unroutable, one priced), got false")
	}
	if result.ETAs["TORWIND-E"] != 55 {
		t.Fatalf("expected TORWIND-E ETA 55, got %v", result.ETAs["TORWIND-E"])
	}
	if !containsSymbol(result.Dropped, "TORWIND-F") {
		t.Fatalf("expected TORWIND-F in Dropped, got %v", result.Dropped)
	}
}

// newETAShipNilLocation builds an idle hull with a nil CurrentLocation() -
// the fail-open guard case Ship.validate() doesn't reject (unlike fuel/cargo).
func newETAShipNilLocation(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(80, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), nil, fuel, 100, 40, cargo, 30, "FRAME_LIGHT_HAULER", "HAULER", nil, navigation.NavStatusDocked)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	return ship
}

func TestRouteETA_NilCurrentLocation_DroppedOthersKept(t *testing.T) {
	good := newETAShip(t, "TORWIND-M", "X1-TW-M")
	noLocation := newETAShipNilLocation(t, "TORWIND-N")
	fake := &fakeRoutePlanner{perShip: map[string]fakeAnswer{
		"X1-TW-M": {seconds: 21},
	}}
	estimator := NewRouteETAEstimator(fake, testClock(), 0)

	result := estimator.EstimateAll(context.Background(), []*navigation.Ship{good, noLocation}, etaTestSystem, etaTestGoal, map[string]*shared.Waypoint{})

	if !result.OK {
		t.Fatalf("expected OK=true (one hull still priced), got false")
	}
	if result.ETAs["TORWIND-M"] != 21 {
		t.Fatalf("expected TORWIND-M ETA 21, got %v", result.ETAs["TORWIND-M"])
	}
	if !containsSymbol(result.Dropped, "TORWIND-N") {
		t.Fatalf("expected TORWIND-N (nil CurrentLocation) in Dropped, got %v", result.Dropped)
	}
	if _, priced := result.ETAs["TORWIND-N"]; priced {
		t.Fatalf("TORWIND-N must not be priced, it has no location to route from")
	}
}

func TestRouteETA_AllUnroutable_OKFalse(t *testing.T) {
	shipG := newETAShip(t, "TORWIND-G", "X1-TW-G")
	shipH := newETAShip(t, "TORWIND-H", "X1-TW-H")
	fake := &fakeRoutePlanner{perShip: map[string]fakeAnswer{
		"X1-TW-G": {err: errors.New("no route found")},
		"X1-TW-H": {err: errors.New("no route found")},
	}}
	estimator := NewRouteETAEstimator(fake, testClock(), 0)

	result := estimator.EstimateAll(context.Background(), []*navigation.Ship{shipG, shipH}, etaTestSystem, etaTestGoal, map[string]*shared.Waypoint{})

	if result.OK {
		t.Fatalf("expected OK=false when every candidate is unroutable")
	}
	if len(result.ETAs) != 0 {
		t.Fatalf("expected no priced candidates, got %v", result.ETAs)
	}
	// The cause must distinguish this from a transport error or a budget overrun - none of the
	// individual per-ship failures here ever touched ctx.
	if result.Cause != "all_candidates_unroutable" {
		t.Fatalf("expected cause=all_candidates_unroutable, got %q", result.Cause)
	}
}

func TestRouteETA_BudgetOverrun_OKFalse(t *testing.T) {
	ship := newETAShip(t, "TORWIND-I", "X1-TW-I")
	fake := &fakeRoutePlanner{delay: 2 * time.Second}
	estimator := NewRouteETAEstimator(fake, testClock(), 50*time.Millisecond)

	start := time.Now()
	result := estimator.EstimateAll(context.Background(), []*navigation.Ship{ship}, etaTestSystem, etaTestGoal, map[string]*shared.Waypoint{})
	elapsed := time.Since(start)

	if result.OK {
		t.Fatalf("expected OK=false on budget overrun")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected EstimateAll to return within budget+slack, took %v", elapsed)
	}
	// OUR OWN budget context expired (ctx.Err() != nil at the point of failure), distinct from a
	// downstream transport-class error surfacing independently.
	if result.Cause != "budget_exceeded" {
		t.Fatalf("expected cause=budget_exceeded, got %q", result.Cause)
	}
}

// A configured budget is the one EstimateAll enforces - the knob is not merely stored.
func TestRouteETA_ConfiguredBudget_IsTheOneEnforced(t *testing.T) {
	ship := newETAShip(t, "TORWIND-P", "X1-TW-P")
	fake := &fakeRoutePlanner{delay: 5 * time.Second}
	estimator := NewRouteETAEstimator(fake, testClock(), 120*time.Millisecond)

	if estimator.Budget() != 120*time.Millisecond {
		t.Fatalf("expected budget 120ms, got %v", estimator.Budget())
	}

	start := time.Now()
	result := estimator.EstimateAll(context.Background(), []*navigation.Ship{ship}, etaTestSystem, etaTestGoal, map[string]*shared.Waypoint{})
	elapsed := time.Since(start)

	if result.OK || result.Cause != "budget_exceeded" {
		t.Fatalf("expected a budget overrun, got OK=%v cause=%q", result.OK, result.Cause)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("expected EstimateAll to hold for the configured budget, returned after %v", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected EstimateAll to stop at the configured budget, not the client's own delay, took %v", elapsed)
	}
}

// A non-positive budget is the "operator set nothing" case. It must resolve to the
// in-code default: a zero budget would expire before the first route answers and demote
// every selection to straight-line ranking.
func TestRouteETA_NonPositiveBudget_TakesTheDefault(t *testing.T) {
	for _, budget := range []time.Duration{0, -1 * time.Second} {
		estimator := NewRouteETAEstimator(&fakeRoutePlanner{}, testClock(), budget)
		if estimator.Budget() != DefaultRouteETABudget {
			t.Fatalf("budget %v: expected the default %v, got %v", budget, DefaultRouteETABudget, estimator.Budget())
		}
	}
}

// Budget is read on the fallback-logging path, where the estimator may be nil.
func TestRouteETA_NilEstimator_BudgetIsZeroNotAPanic(t *testing.T) {
	var estimator *RouteETAEstimator
	if estimator.Budget() != 0 {
		t.Fatalf("expected a nil estimator to report a zero budget, got %v", estimator.Budget())
	}
}

func TestRouteETA_TransportClassError_OKFalse(t *testing.T) {
	timedOut := newETAShip(t, "TORWIND-J", "X1-TW-J")
	fine := newETAShip(t, "TORWIND-K", "X1-TW-K")
	fake := &fakeRoutePlanner{perShip: map[string]fakeAnswer{
		"X1-TW-J": {err: fmt.Errorf("upstream: %w", context.DeadlineExceeded)},
		"X1-TW-K": {seconds: 33},
	}}
	estimator := NewRouteETAEstimator(fake, testClock(), 0)

	result := estimator.EstimateAll(context.Background(), []*navigation.Ship{timedOut, fine}, etaTestSystem, etaTestGoal, map[string]*shared.Waypoint{})

	if result.OK {
		t.Fatalf("expected OK=false: a transport-class error must fail the whole batch open")
	}
	if containsSymbol(result.Dropped, "TORWIND-J") {
		t.Fatalf("a transport-class error must flip OK, not land its ship in Dropped, got %v", result.Dropped)
	}
	if result.ETAs["TORWIND-K"] != 33 {
		t.Fatalf("expected the unaffected candidate TORWIND-K still priced at 33, got %v", result.ETAs["TORWIND-K"])
	}
	// The error surfaced immediately, with OUR OWN budget nowhere near expiry, so this must be
	// distinguished from a budget overrun.
	if result.Cause != "transport_error" {
		t.Fatalf("expected cause=transport_error, got %q", result.Cause)
	}
}

// TestRouteETA_NilClock_OKFalse: a RouteETAEstimator built without its clock (the zero value, or
// a caller that skips NewRouteETAEstimator) must fail open rather than panic on e.clock.Now().
func TestRouteETA_NilClock_OKFalse(t *testing.T) {
	estimator := &RouteETAEstimator{planner: &fakeRoutePlanner{perShip: map[string]fakeAnswer{
		"X1-TW-Z": {seconds: 10},
	}}}
	ship := newETAShip(t, "TORWIND-Z", "X1-TW-Z")

	result := estimator.EstimateAll(context.Background(), []*navigation.Ship{ship}, etaTestSystem, etaTestGoal, map[string]*shared.Waypoint{})

	if result.OK {
		t.Fatalf("expected OK=false with a nil clock, got true")
	}
}

func TestRouteETA_CallsRunInParallel(t *testing.T) {
	ships := []*navigation.Ship{
		newETAShip(t, "TORWIND-L1", "X1-TW-L1"),
		newETAShip(t, "TORWIND-L2", "X1-TW-L2"),
		newETAShip(t, "TORWIND-L3", "X1-TW-L3"),
		newETAShip(t, "TORWIND-L4", "X1-TW-L4"),
	}
	fake := &fakeRoutePlanner{
		delay: 50 * time.Millisecond,
		perShip: map[string]fakeAnswer{
			"X1-TW-L1": {seconds: 10},
			"X1-TW-L2": {seconds: 10},
			"X1-TW-L3": {seconds: 10},
			"X1-TW-L4": {seconds: 10},
		},
	}
	estimator := NewRouteETAEstimator(fake, testClock(), 0)

	start := time.Now()
	result := estimator.EstimateAll(context.Background(), ships, etaTestSystem, etaTestGoal, map[string]*shared.Waypoint{})
	elapsed := time.Since(start)

	if !result.OK {
		t.Fatalf("expected OK=true, got dropped=%v", result.Dropped)
	}
	if got := atomic.LoadInt32(&fake.maxInFlight); got < 2 {
		t.Fatalf("expected calls to overlap (maxInFlight >= 2), got %d", got)
	}
	if elapsed >= 150*time.Millisecond {
		t.Fatalf("expected parallel calls to finish well under 4x50ms sequential, took %v", elapsed)
	}
}

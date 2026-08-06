package ship

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// transientRefuelErr is the shape isRetryableRefuelError classifies as transient, so a
// fixture using it exercises the retry-then-escalate path rather than the fail-fast one.
func transientRefuelErr() error {
	return errors.New("max retries exceeded: server error (500)")
}

// TestExecuteRoute_RefuelFailureAfterFinalSegment_CompletesAndDoesNotReroute is the
// headline defect: the hull has already ARRIVED at the last waypoint on the route,
// carrying the cargo the destination wanted. A refuel there tops off for a LATER trip;
// the route itself has nothing left to fly. A transient failure on that top-up must not
// mark the route FAILED, and must not fly the hull to an alternate fuel stop — the
// escalation is the expensive half, and it carries the cargo away from where it was due.
func TestExecuteRoute_RefuelFailureAfterFinalSegment_CompletesAndDoesNotReroute(t *testing.T) {
	from := mustWaypoint(t, "X1-KP46-I56", 0, 0)
	to := mustWaypoint(t, "X1-KP46-I55", 10, 0)
	from.HasFuel = true
	to.HasFuel = true // the destination sells fuel, so the top-up is attempted rather than skipped

	// Above the conservative top-off threshold so the PRE-DEPARTURE refuel does not fire:
	// this test is about the refuel that runs after the segment lands, and a pre-departure
	// failure would reach the same retry code by a different route and pass for the wrong
	// reason. Still short of a full tank, so the planned refuel is genuinely attempted
	// rather than skipped as a no-op.
	ship := newExecutorTestShip(t, 580, 600, from)
	seg := domainNavigation.NewRouteSegment(from, to, 10, 10, 0, shared.FlightModeCruise, true)

	route, err := domainNavigation.NewRoute(
		"route-final-topup", ship.ShipSymbol(), 1,
		[]*domainNavigation.RouteSegment{seg}, 600, false,
	)
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}

	mockClock := &shared.MockClock{CurrentTime: time.Now()}
	fake := &recordingMediator{
		fuel:            580,
		capacity:        600,
		distByDest:      map[string]float64{to.Symbol: 10},
		refuelAlwaysErr: transientRefuelErr(),
		clock:           mockClock,
	}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, nil, stubSubscriber{})

	execErr := executor.ExecuteRoute(context.Background(), route, ship, shared.MustNewPlayerID(1))

	if execErr != nil {
		t.Fatalf("a top-up failure at the FINAL waypoint must not fail the route - the hull has arrived and has nothing left to fly; got error: %v", execErr)
	}
	if route.Status() != domainNavigation.RouteStatusCompleted {
		t.Fatalf("expected route COMPLETED after the final segment landed, got %s", route.Status())
	}
	if navs := fake.navigateCommands(); len(navs) != 1 {
		t.Fatalf("expected exactly 1 navigate (the route's own single segment) and NO alternate-stop reroute, got %d", len(navs))
	}
	// Calibration: without this the test passes when the refuel is SKIPPED (full tank, or no
	// fuel station), which is the same green for the opposite reason.
	if attempts := fake.refuelAttempts(); attempts == 0 {
		t.Fatalf("fixture never attempted a refuel, so the absorb path was never exercised and this assertion proves nothing")
	}
}

// countingNavMetrics counts the absorbed-failure metric so criterion 6 is asserted on the
// counter itself rather than on a log line.
type countingNavMetrics struct {
	nonEssential map[string]int
}

func (f *countingNavMetrics) RecordRouteCompletion(int, domainNavigation.RouteStatus, float64, int, int) {
}
func (f *countingNavMetrics) RecordSegmentCompletion(int, int, int)             {}
func (f *countingNavMetrics) RecordFuelPurchase(int, string, int)               {}
func (f *countingNavMetrics) RecordFuelConsumption(int, shared.FlightMode, int) {}
func (f *countingNavMetrics) RecordStrandedJumpContainer(int, string)           {}
func (f *countingNavMetrics) RecordNonEssentialRefuelFailure(_ int, kind string) {
	f.nonEssential[kind]++
}

// postArrivalFixture builds the executor/ship/segment trio for a post-arrival refuel that
// always fails transiently, with the hull standing AT the segment's destination.
func postArrivalFixture(t *testing.T, fuel int, requiresRefuel bool) (*RouteExecutor, *recordingMediator, *domainNavigation.Ship, *domainNavigation.RouteSegment) {
	t.Helper()
	from := mustWaypoint(t, "X1-KP46-E42", 0, 0)
	to := mustWaypoint(t, "X1-KP46-I56", 10, 0)
	to.HasFuel = true

	ship := newExecutorTestShip(t, fuel, 600, to) // already arrived: standing at the destination
	seg := domainNavigation.NewRouteSegment(from, to, 10, 10, 0, shared.FlightModeCruise, requiresRefuel)

	mockClock := &shared.MockClock{CurrentTime: time.Now()}
	fake := &recordingMediator{
		fuel:            fuel,
		capacity:        600,
		distByDest:      map[string]float64{to.Symbol: 10},
		refuelAlwaysErr: transientRefuelErr(),
		clock:           mockClock,
	}
	return NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, nil, stubSubscriber{}), fake, ship, seg
}

// TestPostArrivalRefuel_OpportunisticFailure_IsAbsorbedAndCounted is criterion 1. An
// opportunistic top-up is not in the plan at all, so nothing downstream can be waiting on it;
// its failure must never propagate, and must never buy an alternate-stop reroute.
func TestPostArrivalRefuel_OpportunisticFailure_IsAbsorbedAndCounted(t *testing.T) {
	executor, fake, ship, seg := postArrivalFixture(t, 200, false) // low tank -> the strategy tops off

	counter := &countingNavMetrics{nonEssential: map[string]int{}}
	metrics.SetGlobalNavigationCollector(counter)
	defer metrics.SetGlobalNavigationCollector(nil)

	err := executor.handlePostArrivalRefueling(context.Background(), seg, ship, shared.MustNewPlayerID(1), 0)

	if err != nil {
		t.Fatalf("an opportunistic top-up is an optimization - its failure must not propagate, got: %v", err)
	}
	if fake.refuelAttempts() == 0 {
		t.Fatalf("fixture never attempted the opportunistic refuel, so nothing was absorbed and this proves nothing")
	}
	if navs := fake.navigateCommands(); len(navs) != 0 {
		t.Fatalf("an opportunistic top-up must never buy an alternate-stop reroute, got %d navigate(s)", len(navs))
	}
	if counter.nonEssential["opportunistic"] != 1 {
		t.Fatalf("expected exactly 1 absorbed opportunistic failure counted, got %v", counter.nonEssential)
	}
}

// TestPostArrivalRefuel_PlannedFailure_AbsorbedWhenRemainingLegsAffordable is criterion 4 —
// the second observed variant, where the executor's own plan says the last hop needs no
// refuel and the tank already covers it. A stop the remaining route does not need must not
// be able to abandon what the route already achieved.
func TestPostArrivalRefuel_PlannedFailure_AbsorbedWhenRemainingLegsAffordable(t *testing.T) {
	executor, fake, ship, seg := postArrivalFixture(t, 580, true)

	counter := &countingNavMetrics{nonEssential: map[string]int{}}
	metrics.SetGlobalNavigationCollector(counter)
	defer metrics.SetGlobalNavigationCollector(nil)

	// The hop still ahead burns far less than the tank holds.
	err := executor.handlePostArrivalRefueling(context.Background(), seg, ship, shared.MustNewPlayerID(1), 10)

	if err != nil {
		t.Fatalf("the remaining leg is already affordable, so this refuel was optional - it must not fail the route, got: %v", err)
	}
	if fake.refuelAttempts() == 0 {
		t.Fatalf("fixture never attempted the planned refuel, so the absorb path was never exercised")
	}
	if navs := fake.navigateCommands(); len(navs) != 0 {
		t.Fatalf("a refuel the remaining route does not need must not buy a reroute, got %d navigate(s)", len(navs))
	}
	if counter.nonEssential["planned_not_required"] != 1 {
		t.Fatalf("expected exactly 1 absorbed planned-but-unneeded failure counted, got %v", counter.nonEssential)
	}
}

// TestPostArrivalRefuel_PlannedFailure_StillFatalWhenRemainingLegNeedsTheFuel is criterion 5,
// the regression guard. A hull that genuinely cannot reach the next waypoint on what it is
// holding is stranded, and escalating then is the correct, expensive answer. Absorbing here
// would strand hulls silently, which is strictly worse than the bug being fixed.
func TestPostArrivalRefuel_PlannedFailure_StillFatalWhenRemainingLegNeedsTheFuel(t *testing.T) {
	executor, fake, ship, seg := postArrivalFixture(t, 175, true)

	counter := &countingNavMetrics{nonEssential: map[string]int{}}
	metrics.SetGlobalNavigationCollector(counter)
	defer metrics.SetGlobalNavigationCollector(nil)

	// The remaining legs need more fuel than the hull is carrying.
	err := executor.handlePostArrivalRefueling(context.Background(), seg, ship, shared.MustNewPlayerID(1), 400)

	if err == nil {
		t.Fatalf("a refuel the remaining route DEPENDS on must still fail the route when unrecoverable - absorbing it would strand the hull silently")
	}
	// The error TYPE is what separates the two paths, and is the assertion that would catch a
	// regression here: only the escalating path wraps into ErrRefuelUnrecoverable, because only
	// it has attempted the alternate stop. The absorbing path returns the bare refuel error.
	var unrecoverable *ErrRefuelUnrecoverable
	if !errors.As(err, &unrecoverable) {
		t.Fatalf("expected the escalating path's typed unrecoverable-refuel error, got %T: %v - an absorbed failure returns the bare error instead", err, err)
	}
	if len(counter.nonEssential) != 0 {
		t.Fatalf("an essential refuel failure must NOT be counted as absorbed, got %v", counter.nonEssential)
	}
	if fake.refuelAttempts() == 0 {
		t.Fatalf("fixture never attempted the refuel, so neither path was exercised")
	}
}

// TestRefuelWithoutEscalation_DoesNotReachForAnAlternateStop is criterion 2's real
// discriminator. Asserting "no navigate was issued" is NOT one: the reroute needs a waypoint
// repository to find an alternate, and these fixtures wire none, so that assertion passes
// whether or not escalation was attempted. The error TYPE does separate them — only the
// escalating path has an alternate stop to fail at, and only it wraps the result into
// ErrRefuelUnrecoverable.
func TestRefuelWithoutEscalation_DoesNotReachForAnAlternateStop(t *testing.T) {
	for _, tc := range []struct {
		name        string
		escalating  bool
		wantWrapped bool
	}{
		{"non-essential refuel stops at the waypoint", false, false},
		{"essential refuel escalates", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, fake, ship, _ := postArrivalFixture(t, 300, true)

			var err error
			if tc.escalating {
				err = executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1), false)
			} else {
				err = executor.attemptNonEssentialRefuel(context.Background(), ship, shared.MustNewPlayerID(1), false)
			}

			if err == nil {
				t.Fatalf("fixture refuels always fail, so an error was expected on either path")
			}
			if fake.refuelAttempts() == 0 {
				t.Fatalf("fixture never attempted a refuel")
			}
			var unrecoverable *ErrRefuelUnrecoverable
			if got := errors.As(err, &unrecoverable); got != tc.wantWrapped {
				t.Fatalf("escalating=%v: wrapped-in-ErrRefuelUnrecoverable = %v, want %v (got %T: %v)", tc.escalating, got, tc.wantWrapped, err, err)
			}
		})
	}
}

// TestRemainingLegsAffordable_ReservesTheSafetyMargin pins that a hull is excused a refuel
// only when it could finish the plan AND still land holding the standard reserve. Without the
// margin a hull is waved through on fuel that leaves nothing on arrival, which is how a
// skipped refuel becomes a strand.
func TestRemainingLegsAffordable_ReservesTheSafetyMargin(t *testing.T) {
	executor, _, ship, _ := postArrivalFixture(t, 100, true)

	if executor.remainingLegsAffordable(ship, 100) {
		t.Fatalf("fuel covering the remaining legs EXACTLY leaves no reserve on arrival - that refuel is still essential")
	}
	if !executor.remainingLegsAffordable(ship, 100-domainNavigation.DefaultFuelSafetyMargin) {
		t.Fatalf("fuel covering the remaining legs plus the standard reserve makes the refuel optional")
	}
}

// TestFuelForSegmentsAfter_CountsPastFuelSellingWaypoints pins why this is not the existing
// flight-mode reserve. That reserve stops at the next waypoint selling fuel — which is every
// waypoint a refuel is attempted at — so reusing it would read zero exactly where the decision
// is made, and would excuse refuels that genuinely strand a hull.
func TestFuelForSegmentsAfter_CountsPastFuelSellingWaypoints(t *testing.T) {
	a := mustWaypoint(t, "X1-FS-A", 0, 0)
	b := mustWaypoint(t, "X1-FS-B", 10, 0)
	c := mustWaypoint(t, "X1-FS-C", 20, 0)
	b.HasFuel = true // the reserve stops here; the remaining-plan total must not

	seg0 := domainNavigation.NewRouteSegment(a, b, 10, 10, 0, shared.FlightModeCruise, true)
	seg1 := domainNavigation.NewRouteSegment(b, c, 10, 10, 0, shared.FlightModeCruise, false)
	route, err := domainNavigation.NewRoute("route-fs", "FS-1", 1, []*domainNavigation.RouteSegment{seg0, seg1}, 600, false)
	if err != nil {
		t.Fatalf("NewRoute: %v", err)
	}

	if reserve := route.FuelReserveAfterCurrentSegment(); reserve != 0 {
		t.Fatalf("fixture precondition: the reserve should read 0 here because it stops at the fuel-selling waypoint, got %d - this test no longer demonstrates the difference", reserve)
	}
	if got := fuelForSegmentsAfter(route, 0); got <= 0 {
		t.Fatalf("the leg past the fuel-selling waypoint still burns fuel and must be counted, got %d", got)
	}
}

// TestPostArrivalRefuel_NonEssentialFailureDoesNotSpendTheRetryBudget is the dead time the
// absorb left behind. The hull is standing on the waypoint it was sent to, loaded; the refuel
// here tops it up for a LATER trip and the remaining route does not need it. Absorbing that
// failure only AFTER the full elapsed-time budget leaves the hull parked for the length of the
// budget over fuel nothing is waiting on — on the critical path of whatever it is carrying.
//
// The budget exists to rescue a refuel the route DEPENDS on. Spending it on one nothing depends
// on buys nothing and costs the delivery.
//
// Waiting is measured on the mock clock, which advances only when the retry loop sleeps, so this
// asserts the absence of retry waiting itself rather than wall-clock timing that could pass on a
// fast machine for the wrong reason.
func TestPostArrivalRefuel_NonEssentialFailureDoesNotSpendTheRetryBudget(t *testing.T) {
	executor, fake, ship, seg := postArrivalFixture(t, 200, false) // opportunistic: never required
	start := fake.clock.Now()

	err := executor.handlePostArrivalRefueling(context.Background(), seg, ship, shared.MustNewPlayerID(1), 0)
	if err != nil {
		t.Fatalf("an opportunistic top-up must still be absorbed, got: %v", err)
	}

	// Calibration: a refuel that was never attempted would satisfy both assertions below for the
	// opposite reason.
	if fake.refuelAttempts() == 0 {
		t.Fatal("fixture never attempted a refuel, so nothing was retried and this proves nothing")
	}
	if waited := fake.clock.Now().Sub(start); waited >= DefaultRefuelBackoffBase {
		t.Fatalf("the hull waited %s on a refuel the remaining route does not need — it must not sleep even once before carrying on", waited)
	}
	if attempts := fake.refuelAttempts(); attempts != 1 {
		t.Fatalf("a refuel nothing depends on should be tried once and let go, got %d attempts", attempts)
	}
}

// TestPostArrivalRefuel_EssentialFailureStillSpendsTheFullBudget is the RULINGS #4 half. A hull
// that genuinely cannot reach the next waypoint on what it holds is stranded without this
// refuel, and waiting out a transient upstream failure is enormously cheaper than the
// alternative. Narrowing the non-essential branch must not touch this one.
func TestPostArrivalRefuel_EssentialFailureStillSpendsTheFullBudget(t *testing.T) {
	executor, fake, ship, seg := postArrivalFixture(t, 175, true) // planned, and the rest is unaffordable
	start := fake.clock.Now()

	err := executor.handlePostArrivalRefueling(context.Background(), seg, ship, shared.MustNewPlayerID(1), 400)
	if err == nil {
		t.Fatal("a refuel the remaining legs depend on must still fail the segment when unrecoverable")
	}
	if waited := fake.clock.Now().Sub(start); waited < DefaultRefuelRetryBudget {
		t.Fatalf("an essential refuel gave up after %s — it must spend the whole budget waiting out a transient failure before escalating", waited)
	}
	if attempts := fake.refuelAttempts(); attempts < 2 {
		t.Fatalf("an essential refuel must RETRY, got %d attempt(s)", attempts)
	}
}

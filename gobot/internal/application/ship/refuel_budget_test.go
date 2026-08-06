package ship

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-l7zha: a transient upstream 500 on the refuel endpoint cost 41 MINUTES of a
// blocked serial contract pipeline. The retry budget gave up after 3 attempts
// (~5-6 minutes of wall clock, measured from the staging incident: attempt 2 at
// 09:31:16, attempt 3 at 09:34:19) and escalated to an alternate-fuel-stop
// reroute — a 144-unit DRIFT crawl across the system. The escalation is
// enormously more expensive than waiting out a 500, so the budget must be
// bounded by ELAPSED TIME rather than attempt count.
//
// The bar these tests hold the code to is the literal wall-clock requirement
// (10 minutes), not the named constant: asserting against
// DefaultRefuelRetryBudget would keep passing if the constant were later
// lowered to a second, which is exactly the regression that would reopen this
// bug. TestRefuelRetryConstants_MeetTheIncidentBar ties the constant back to
// the bar separately.
const incidentRetryBar = 10 * time.Minute

// refuelIncidentSignature is the error captured verbatim from the sp-l7zha
// staging incident, and is what isRetryableRefuelError must classify as
// transient.
func refuelIncidentSignature() error {
	return fmt.Errorf("failed to refuel: %w", fmt.Errorf("max retries exceeded: server error (500)"))
}

// The named budget/backoff constants must satisfy the incident's requirement.
// Stated as inequalities against the bar rather than as equality with
// themselves, so a future retune that still clears the bar is free while one
// that reopens sp-l7zha fails here.
func TestRefuelRetryConstants_MeetTheIncidentBar(t *testing.T) {
	if DefaultRefuelRetryBudget < incidentRetryBar {
		t.Errorf("DefaultRefuelRetryBudget = %v, must be at least the %v the incident requires",
			DefaultRefuelRetryBudget, incidentRetryBar)
	}
	// A backoff interval cap keeps the tail of the budget from collapsing into
	// one long sleep — without it, doubling reaches multi-minute waits and the
	// 500 is re-probed only a handful of times across the whole window.
	if DefaultRefuelBackoffMax > 90*time.Second {
		t.Errorf("DefaultRefuelBackoffMax = %v, a single backoff interval longer than 90s makes the budget's tail one long sleep",
			DefaultRefuelBackoffMax)
	}
	if DefaultRefuelBackoffMax <= 0 || DefaultRefuelBackoffBase <= 0 {
		t.Errorf("backoff base/cap must both be positive, got base=%v cap=%v",
			DefaultRefuelBackoffBase, DefaultRefuelBackoffMax)
	}
}

// ACCEPTANCE #1 (primary form): a 5xx refuel failure is retried for at least 10
// minutes of wall clock BEFORE any alternate-stop reroute.
//
// The assertion is the mock-clock timestamp of the reroute NavigateDirectCommand
// itself, not an attempt count — attempt count is the bound being replaced, so
// asserting on it would pin the defect rather than the fix. Against the 3-attempt
// implementation the reroute is stamped ~6 seconds in (2s + 4s of backoff) and
// this fails; the fixture fails refuels UNBOUNDEDLY, so no finite budget can
// satisfy it by outlasting the errors.
func TestRefuelShipWithRetry_RetriesTenMinutesBeforeReroutingToAnAlternateStop(t *testing.T) {
	origin := mustWaypoint(t, "X1-KP46-I56", 0, 0)
	origin.HasFuel = true
	origin.Traits = []string{"MARKETPLACE"}

	alt := mustWaypoint(t, "X1-KP46-K84", 30, 0)
	alt.HasFuel = true
	alt.Traits = []string{"MARKETPLACE"}

	ship := newExecutorTestShip(t, 100, 400, origin)

	start := time.Date(2026, 8, 5, 9, 28, 0, 0, time.UTC)
	mockClock := &shared.MockClock{CurrentTime: start}

	fake := &recordingMediator{
		fuel:                    100,
		capacity:                400,
		distByDest:              map[string]float64{alt.Symbol: 30},
		refuelAlwaysErr:         refuelIncidentSignature(),
		refuelFailsUntilReroute: true,
		clock:                   mockClock,
	}
	waypointRepo := &fakeWaypointRepo{
		bySystemTrait: map[string][]*shared.Waypoint{
			origin.SystemSymbol + "|MARKETPLACE": {origin, alt},
		},
	}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, waypointRepo, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1), true)

	if len(fake.navigateTimes) != 1 {
		t.Fatalf("expected exactly 1 alternate-stop reroute navigate, got %d (cannot measure the pre-reroute budget without it)",
			len(fake.navigateTimes))
	}
	if elapsed := fake.navigateTimes[0].Sub(start); elapsed < incidentRetryBar {
		t.Errorf("rerouted to the alternate fuel stop after only %v of retrying; a transient 5xx must be retried for at least %v first (sp-l7zha: the reroute cost 41 minutes)",
			elapsed, incidentRetryBar)
	}
	// Corroborates that the budget is spent RETRYING rather than idled away in
	// one sleep: the old bound was 3 attempts, so anything at or below it means
	// the loop is still attempt-shaped.
	if got := fake.refuelAttempts(); got <= 3 {
		t.Errorf("only %d refuel attempts across the whole budget; the 5xx must be re-probed repeatedly, not slept through", got)
	}
	if err != nil {
		t.Errorf("the alternate stop refuels fine here, so the overall refuel must succeed, got: %v", err)
	}
}

// ACCEPTANCE #1 (total-budget form): with NO fuel-capable alternate to reroute
// to, the whole call is retry time, so the clock delta across it measures the
// budget directly with nothing else mixed in.
func TestRefuelShipWithRetry_TransientFailuresBurnTheFullBudgetNotThreeAttempts(t *testing.T) {
	origin := mustWaypoint(t, "X1-KP46-I56", 0, 0)
	origin.HasFuel = true
	origin.Traits = []string{"MARKETPLACE"}

	noFuelMarket := mustWaypoint(t, "X1-KP46-B8", 10, 0) // only other market, sells no fuel
	noFuelMarket.HasFuel = false
	noFuelMarket.Traits = []string{"MARKETPLACE"}

	ship := newExecutorTestShip(t, 100, 400, origin)

	start := time.Date(2026, 8, 5, 9, 28, 0, 0, time.UTC)
	mockClock := &shared.MockClock{CurrentTime: start}

	fake := &recordingMediator{
		fuel:            100,
		capacity:        400,
		refuelAlwaysErr: refuelIncidentSignature(),
		clock:           mockClock,
	}
	waypointRepo := &fakeWaypointRepo{
		bySystemTrait: map[string][]*shared.Waypoint{
			origin.SystemSymbol + "|MARKETPLACE": {origin, noFuelMarket},
		},
	}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, waypointRepo, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1), true)

	if elapsed := mockClock.Now().Sub(start); elapsed < incidentRetryBar {
		t.Errorf("gave up on a transient 5xx after %v; the budget must run at least %v", elapsed, incidentRetryBar)
	}
	if got := fake.refuelAttempts(); got <= 3 {
		t.Errorf("expected many more than the old 3-attempt bound across a %v budget, got %d attempts", incidentRetryBar, got)
	}
	if len(fake.navigateCommands()) != 0 {
		t.Errorf("no fuel-capable alternate exists, so no reroute may be attempted, got %d navigates", len(fake.navigateCommands()))
	}

	var unrecoverable *ErrRefuelUnrecoverable
	if !errors.As(err, &unrecoverable) {
		t.Fatalf("expected *ErrRefuelUnrecoverable so a goods_factory caller can park, got %T: %v", err, err)
	}
	// The reported attempt count must be the REAL one. A stale constant here
	// would misreport the escalation in exactly the log an operator reads
	// during the next incident.
	if unrecoverable.Attempts != fake.refuelAttempts() {
		t.Errorf("ErrRefuelUnrecoverable.Attempts = %d but %d refuels were actually sent",
			unrecoverable.Attempts, fake.refuelAttempts())
	}
	if unrecoverable.Elapsed < incidentRetryBar {
		t.Errorf("ErrRefuelUnrecoverable.Elapsed = %v, must report the budget actually spent (>= %v)",
			unrecoverable.Elapsed, incidentRetryBar)
	}
}

// The budget must be spent on REPEATED probes of the failing endpoint, not on a
// handful of ever-longer sleeps. Unbounded doubling from a 2s base reaches
// ~4 minutes by the ninth interval, which would re-probe a 500 only a few times
// across ten minutes and leave the recovery to luck.
//
// Discriminating in both directions: no interval may exceed the cap (rejects
// unbounded doubling), and the cap must actually be REACHED (rejects a
// fixed-tiny-backoff implementation that would satisfy the ceiling vacuously
// while hammering the endpoint).
func TestRefuelShipWithRetry_BackoffIntervalIsCappedAndTheCapIsReached(t *testing.T) {
	origin := mustWaypoint(t, "X1-KP46-I56", 0, 0)
	origin.HasFuel = true

	ship := newExecutorTestShip(t, 100, 400, origin)

	start := time.Date(2026, 8, 5, 9, 28, 0, 0, time.UTC)
	mockClock := &shared.MockClock{CurrentTime: start}

	fake := &recordingMediator{
		fuel:            100,
		capacity:        400,
		refuelAlwaysErr: refuelIncidentSignature(),
		clock:           mockClock,
	}
	// No waypoint repo: the reroute fails immediately, so every stamped gap
	// between refuels is a backoff interval and nothing else.
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, nil, stubSubscriber{})

	_ = executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1), true)

	if len(fake.refuelTimes) < 2 {
		t.Fatalf("need at least 2 refuel attempts to measure a backoff interval, got %d", len(fake.refuelTimes))
	}

	var longest time.Duration
	for i := 1; i < len(fake.refuelTimes); i++ {
		gap := fake.refuelTimes[i].Sub(fake.refuelTimes[i-1])
		if gap > DefaultRefuelBackoffMax {
			t.Errorf("backoff interval %d was %v, longer than the %v cap - the budget's tail became one long sleep",
				i, gap, DefaultRefuelBackoffMax)
		}
		if gap > longest {
			longest = gap
		}
	}
	if longest < DefaultRefuelBackoffMax {
		t.Errorf("longest backoff interval was %v, never reaching the %v cap - backoff must still GROW toward the cap, not stay tiny",
			longest, DefaultRefuelBackoffMax)
	}
}

// ACCEPTANCE #2: a non-transient (4xx-class) failure must still fail fast. The
// discriminating assertion is that it consumes NO wall clock at all — an
// implementation that let a permanent error fall into the retry loop would burn
// ten minutes per hull on an error that will never clear, which is strictly
// worse than the bug being fixed.
func TestRefuelShipWithRetry_NonTransientFailureDoesNotBurnTheRetryBudget(t *testing.T) {
	origin := mustWaypoint(t, "X1-KP46-I56", 0, 0)
	origin.HasFuel = true
	origin.Traits = []string{"MARKETPLACE"}

	alt := mustWaypoint(t, "X1-KP46-K84", 30, 0)
	alt.HasFuel = true
	alt.Traits = []string{"MARKETPLACE"}

	ship := newExecutorTestShip(t, 100, 400, origin)

	start := time.Date(2026, 8, 5, 9, 28, 0, 0, time.UTC)
	mockClock := &shared.MockClock{CurrentTime: start}

	fake := &recordingMediator{
		fuel:     100,
		capacity: 400,
		refuelAlwaysErr: fmt.Errorf("failed to refuel: %w", fmt.Errorf(
			`API error (status 400): {"error":{"message":"Purchase failed. Agent has insufficient funds.","code":4600}}`)),
		clock: mockClock,
	}
	waypointRepo := &fakeWaypointRepo{
		bySystemTrait: map[string][]*shared.Waypoint{
			origin.SystemSymbol + "|MARKETPLACE": {origin, alt},
		},
	}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, waypointRepo, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1), true)

	if elapsed := mockClock.Now().Sub(start); elapsed != 0 {
		t.Errorf("a permanent 4600 burned %v of the retry budget; it must fail fast with no backoff at all", elapsed)
	}
	if got := fake.refuelAttempts(); got != 1 {
		t.Errorf("expected exactly 1 attempt for a non-transient error, got %d", got)
	}
	if len(fake.navigateCommands()) != 0 {
		t.Errorf("a permanent error must not trigger the alternate-stop reroute, got %d navigates", len(fake.navigateCommands()))
	}
	if err == nil || !strings.Contains(err.Error(), "4600") {
		t.Errorf("expected the original 4600 error to surface unchanged, got: %v", err)
	}
}

// ACCEPTANCE #3: a hull at or above capacity must SKIP the refuel entirely.
//
// The incident hull read 729/600 from the live API and 600/600 locally — full
// either way — and still docked, called refuel, failed, retried, and rerouted
// 144 units across the system for fuel it did not need. This fix alone would
// have prevented the whole incident.
//
// The assertion is on the total command count, which rejects the two
// implementations an outcome-only check would let through: one that calls
// refuel and discards the result (>= 1 command), and one that skips only the
// refuel but still pays the dock (>= 1 command). refuelAlwaysErr is armed so a
// call would also fail loudly rather than pass quietly.
func TestRefuelShipWithRetry_FullTankMakesNoAPICallAtAll(t *testing.T) {
	origin := mustWaypoint(t, "X1-KP46-I56", 0, 0)
	origin.HasFuel = true

	ship := newExecutorTestShip(t, 600, 600, origin) // the incident hull's tank

	start := time.Date(2026, 8, 5, 9, 28, 0, 0, time.UTC)
	mockClock := &shared.MockClock{CurrentTime: start}

	fake := &recordingMediator{
		fuel:            600,
		capacity:        600,
		refuelAlwaysErr: refuelIncidentSignature(),
		clock:           mockClock,
	}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, nil, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1), true)

	if len(fake.commands) != 0 {
		t.Errorf("a full tank must reach the API zero times; got %d commands (%s)",
			len(fake.commands), commandTypeNames(fake.commands))
	}
	if elapsed := mockClock.Now().Sub(start); elapsed != 0 {
		t.Errorf("a full tank must consume no wall clock at all, burned %v", elapsed)
	}
	if err != nil {
		t.Errorf("skipping a refuel a full hull does not need is success, got: %v", err)
	}
}

// ACCEPTANCE #6 (regression) and the boundary of the fullness guard: one unit
// short of full must still refuel. A guard written as `>` rather than `>=`, or
// one keyed on a percentage threshold, changes behaviour here.
func TestRefuelShipWithRetry_OneUnitShortOfFullStillRefuels(t *testing.T) {
	origin := mustWaypoint(t, "X1-KP46-I56", 0, 0)
	origin.HasFuel = true

	ship := newExecutorTestShip(t, 599, 600, origin)

	mockClock := &shared.MockClock{CurrentTime: time.Date(2026, 8, 5, 9, 28, 0, 0, time.UTC)}
	fake := &recordingMediator{fuel: 599, capacity: 600, clock: mockClock}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, nil, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1), true)

	if got := fake.refuelAttempts(); got != 1 {
		t.Errorf("a tank one unit short of full must still be topped up: expected 1 refuel, got %d", got)
	}
	if fake.fuel != fake.capacity {
		t.Errorf("expected the ship refuelled to capacity, got fuel=%d capacity=%d", fake.fuel, fake.capacity)
	}
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// A zero-capacity hull (a probe) can hold no fuel at all, so 0/0 satisfies the
// fullness guard and it must skip rather than dock-and-refuel a tank that does
// not exist. Pinned because probes are ~half the fleet and a guard written
// against a positive capacity would treat them as permanently empty.
func TestRefuelShipWithRetry_ZeroCapacityProbeSkipsTheRefuel(t *testing.T) {
	origin := mustWaypoint(t, "X1-KP46-I56", 0, 0)
	origin.HasFuel = true

	ship := newExecutorTestShip(t, 0, 0, origin)

	mockClock := &shared.MockClock{CurrentTime: time.Date(2026, 8, 5, 9, 28, 0, 0, time.UTC)}
	fake := &recordingMediator{fuel: 0, capacity: 0, refuelAlwaysErr: refuelIncidentSignature(), clock: mockClock}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, nil, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1), true)

	if len(fake.commands) != 0 {
		t.Errorf("a zero-capacity probe must reach the API zero times; got %d commands (%s)",
			len(fake.commands), commandTypeNames(fake.commands))
	}
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// A cancelled context must abort the retry loop rather than sleep out the full
// budget. Extending the loop from ~6 seconds to ten minutes makes an
// uninterruptible sleep a shutdown hazard: a container being torn down would
// hold its hull for ten more minutes.
func TestRefuelShipWithRetry_CancelledContextAbortsTheBudget(t *testing.T) {
	origin := mustWaypoint(t, "X1-KP46-I56", 0, 0)
	origin.HasFuel = true

	ship := newExecutorTestShip(t, 100, 400, origin)

	start := time.Date(2026, 8, 5, 9, 28, 0, 0, time.UTC)
	mockClock := &shared.MockClock{CurrentTime: start}

	fake := &recordingMediator{
		fuel:            100,
		capacity:        400,
		refuelAlwaysErr: refuelIncidentSignature(),
		clock:           mockClock,
	}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, nil, stubSubscriber{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := executor.refuelShipWithRetry(ctx, ship, shared.MustNewPlayerID(1), true)

	if elapsed := mockClock.Now().Sub(start); elapsed >= incidentRetryBar {
		t.Errorf("a cancelled context still burned the whole %v budget (%v elapsed)", incidentRetryBar, elapsed)
	}
	if got := fake.refuelAttempts(); got != 1 {
		t.Errorf("expected the loop to stop after the first attempt on a cancelled context, got %d attempts", got)
	}
	if err == nil {
		t.Errorf("expected an error when the retry loop is cancelled mid-failure, got nil")
	}
}

// commandTypeNames renders the command types a fake recorded, so a failing
// count assertion says WHICH calls leaked through rather than only how many.
func commandTypeNames(commands []mediator.Request) string {
	names := make([]string, 0, len(commands))
	for _, c := range commands {
		names = append(names, fmt.Sprintf("%T", c))
	}
	return strings.Join(names, ", ")
}

// TestRefuelRetry_OriginRecoveredBeforeTheDetour_NoReroute pins the guarantee that makes the
// detour a last resort rather than a reflex.
//
// The failure that spends the budget is transient by construction — the loop only reaches its
// end for errors it classified retryable — and what it is about to commit to is the expensive
// branch: a crawl across the system in DRIFT, which in the incident cost far more than the fuel
// was worth. The loop therefore probes the original waypoint ONCE MORE after the budget has
// fully elapsed, so a stop that came back while the hull was waiting is used instead of flown
// away from.
//
// That behaviour already existed and nothing pinned it: shortening the loop so it stops without
// that final probe leaves every other refuel test green.
func TestRefuelRetry_OriginRecoveredBeforeTheDetour_NoReroute(t *testing.T) {
	origin := mustWaypoint(t, "X1-KP46-I56", 0, 0)
	origin.HasFuel = true
	origin.Traits = []string{"MARKETPLACE"}
	alt := mustWaypoint(t, "X1-KP46-K84", 30, 0)
	alt.HasFuel = true
	alt.Traits = []string{"MARKETPLACE"}

	ship := newExecutorTestShip(t, 100, 400, origin)
	start := time.Date(2026, 8, 5, 9, 28, 0, 0, time.UTC)
	mockClock := &shared.MockClock{CurrentTime: start}

	fake := &recordingMediator{
		fuel:       100,
		capacity:   400,
		distByDest: map[string]float64{alt.Symbol: 30},
		clock:      mockClock,
		// The outage clears as the budget runs out — the shape the incident actually had.
		refuelFailsUntilTime: start.Add(DefaultRefuelRetryBudget),
	}
	waypointRepo := &fakeWaypointRepo{
		bySystemTrait: map[string][]*shared.Waypoint{
			origin.SystemSymbol + "|MARKETPLACE": {origin, alt},
		},
	}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, waypointRepo, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1), true)

	if err != nil {
		t.Fatalf("the original stop was serving again by the end of the budget, so the refuel must succeed there: %v", err)
	}
	// Calibration: a fixture whose refuels never failed would satisfy the no-reroute assertion
	// below without the budget loop ever having run.
	if got := fake.refuelAttempts(); got <= 3 {
		t.Fatalf("only %d refuel attempt(s) — the fixture never spent the budget, so this proves nothing about what happens at the end of it", got)
	}
	if navs := len(fake.navigateTimes); navs != 0 {
		t.Fatalf("the hull flew to an alternate fuel stop (%d navigate(s)) although the original waypoint was serving again — the detour is the expensive branch and must not be taken without one last look", navs)
	}
}

// TestRefuelRetry_OriginStillDead_StillEscalates is the other half. The final probe is an extra
// attempt before the detour, never a replacement for it: a hull that genuinely cannot refuel
// where it stands must still be taken somewhere it can.
func TestRefuelRetry_OriginStillDead_StillEscalates(t *testing.T) {
	origin := mustWaypoint(t, "X1-KP46-I56", 0, 0)
	origin.HasFuel = true
	origin.Traits = []string{"MARKETPLACE"}
	alt := mustWaypoint(t, "X1-KP46-K84", 30, 0)
	alt.HasFuel = true
	alt.Traits = []string{"MARKETPLACE"}

	ship := newExecutorTestShip(t, 100, 400, origin)
	start := time.Date(2026, 8, 5, 9, 28, 0, 0, time.UTC)
	mockClock := &shared.MockClock{CurrentTime: start}

	fake := &recordingMediator{
		fuel:                    100,
		capacity:                400,
		distByDest:              map[string]float64{alt.Symbol: 30},
		refuelAlwaysErr:         refuelIncidentSignature(),
		refuelFailsUntilReroute: true, // the station is broken, not merely busy
		clock:                   mockClock,
	}
	waypointRepo := &fakeWaypointRepo{
		bySystemTrait: map[string][]*shared.Waypoint{
			origin.SystemSymbol + "|MARKETPLACE": {origin, alt},
		},
	}
	executor := NewRouteExecutor(nil, fake, mockClock, nil, nil, nil, waypointRepo, stubSubscriber{})

	err := executor.refuelShipWithRetry(context.Background(), ship, shared.MustNewPlayerID(1), true)

	if navs := len(fake.navigateTimes); navs != 1 {
		t.Fatalf("a waypoint that stays dead must still send the hull to an alternate, got %d navigate(s)", navs)
	}
	if err != nil {
		t.Fatalf("the alternate serves fine here, so the refuel must ultimately succeed: %v", err)
	}
}

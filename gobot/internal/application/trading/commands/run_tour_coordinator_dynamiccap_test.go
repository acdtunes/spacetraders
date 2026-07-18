package commands

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// tourStubAPIClient is a scripted live-treasury fake for the dynamic (--max-spend 0)
// re-resolution path (sp-7z7j). It embeds domainPorts.APIClient (nil) so only GetAgent
// is overridden. failFirst makes the first N GetAgent calls ERROR (a transient treasury
// read failure — the exact global blip that fails every hull's read at once), after
// which it returns credits. It counts calls so a test can prove the 25%-of-treasury cap
// is RE-RESOLVED per iteration and that an unreadable read is RETRIED rather than
// swallowed into a loop-ending "tour unavailable".
type tourStubAPIClient struct {
	domainPorts.APIClient
	mu        sync.Mutex
	credits   int
	failFirst int
	calls     int
}

func (c *tourStubAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls <= c.failFirst {
		return nil, errors.New("agent API unavailable (simulated transient treasury read failure)")
	}
	return &player.AgentData{Credits: c.credits}, nil
}

func (c *tourStubAPIClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// newTourHandlerWithAPI mirrors newTourHandler but wires a REAL (fake) apiClient so the
// dynamic-cap re-resolution path (defaultMaxSpend → GetAgent) is exercised, unlike the
// base helper which passes nil (leaving the 25%-of-treasury resolution disabled).
func newTourHandlerWithAPI(t *testing.T, fx *tourFixture, planner routing.RoutingClient, tel trading.TourTelemetryRepository, apiClient domainPorts.APIClient) *RunTourCoordinatorHandler {
	return NewRunTourCoordinatorHandler(
		&tourFakeMediator{fx: fx},
		&tourFakeShipRepo{fx: fx, t: t},
		&tourFakeMarketRepo{fx: fx, t: t},
		&tourFakeWaypointRepo{fx: fx},
		tel,
		planner,
		nil,
		&trFakeClock{},
		apiClient,
	)
}

func dynamicCapFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
}

func roundTripPlan() *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
		leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
		leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
	}}
}

// sp-7z7j regression (the bead's named assertion): a dynamic-cap (--max-spend 0)
// continuous (--iterations -1) tour RE-RESOLVES 25% of LIVE treasury at each iteration
// and REACHES ITERATION 2. Treasury reads healthy at 8,000,000 → each tour is sized to
// 2,000,000 (25%), the SAME live re-resolution the fixed-cap control never needs. The
// budget must be re-read per iteration (not resolved once and cached), and both tours
// must fly before margins die.
func TestTour_ContinuousDynamicCapReResolvesEachIterationAndReachesIteration2(t *testing.T) {
	fx := dynamicCapFixture()
	api := &tourStubAPIClient{credits: 8_000_000}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		roundTripPlan(), // tour 1 (iteration 1)
		roundTripPlan(), // tour 2 (iteration 2) — the loop MUST reach this
		{Feasible: false, InfeasibleReason: "no_profitable_tour"}, // then margins die
	}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-DYN-TOKEN")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DYN", PlayerID: 1, ContainerID: "ctr-dyn", Iterations: -1,
		MaxSpend:          0, // dynamic: 25% of live treasury, re-resolved each tour
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("dynamic-cap continuous tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.ToursCompleted != 2 {
		t.Fatalf("dynamic-cap -1 tour must REACH ITERATION 2 (two productive tours), got ToursCompleted=%d (%+v)", r.ToursCompleted, r)
	}
	if r.TourUnavailable {
		t.Fatalf("a dynamic-cap tour with a readable treasury is never tour-unavailable: %+v", r)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q (margins died after two productive tours)", r.ExitReason, tourExitStarvation)
	}
	// The budget was RE-RESOLVED from live treasury on each of the two productive
	// tours: 25% of 8,000,000 = 2,000,000, a fresh positive value each pass.
	if len(planner.maxSpends) < 2 || planner.maxSpends[0] != 2_000_000 || planner.maxSpends[1] != 2_000_000 {
		t.Fatalf("planner max-spends = %v, want the first two = 2,000,000 (25%% of live treasury, re-resolved per iteration)", planner.maxSpends)
	}
	// Re-resolution means one live treasury read per loop pass, not a single cached read.
	if api.callCount() < 2 {
		t.Fatalf("expected the treasury to be re-read per iteration (>=2 GetAgent calls), got %d", api.callCount())
	}
}

// sp-7z7j core bug: a TRANSIENT treasury read failure at re-resolution time must NOT
// silently end the -1 loop. Before the fix, an unreadable treasury resolved to a 0
// budget, the planner refused it (spend_cap = max(0, 0 - reserve) = 0 → infeasible),
// and — nothing earned yet on a relaunch — the coordinator reported the whole run
// "tour unavailable" and COMPLETED the container after exactly one iteration (the 5/5
// field repro). The fix fails CLOSED (never spends on an unreadable budget, never falls
// back to unlimited) but PAUSES and RETRIES, so the very next pass — with the treasury
// readable again — flies a real tour. Here GetAgent fails once then recovers.
func TestTour_ContinuousDynamicCapUnreadableTreasuryPausesRetriesInsteadOfEndingLoop(t *testing.T) {
	fx := dynamicCapFixture()
	api := &tourStubAPIClient{credits: 8_000_000, failFirst: 1} // first read fails, then recovers
	planner := &tourFakeRoutingClient{
		infeasibleOnZeroSpend: true, // mirror the real solver: a 0 budget is infeasible
		plans: []*routing.TourPlan{
			roundTripPlan(), // the productive tour, once the treasury is readable again
			{Feasible: false, InfeasibleReason: "no_profitable_tour"}, // then margins die
		},
	}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-BLIP-TOKEN")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-BLIP", PlayerID: 1, ContainerID: "ctr-blip", Iterations: -1,
		MaxSpend: 0, WorkingCapitalReserve: 1_000_000, // the field-repro reserve
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a transient treasury blip must not error the run: %v", err)
	}
	r := tourResponse(t, resp)

	// The bug's signature: a transient blip on the first (relaunch) tour was reported
	// as a fail-open no-op and the -1 container completed after one iteration.
	if r.TourUnavailable {
		t.Fatalf("a transient treasury read failure must NOT end the -1 loop as tour-unavailable (this is the sp-7z7j single-iteration exit): %+v", r)
	}
	if r.ToursCompleted < 1 {
		t.Fatalf("after the treasury recovered the loop must fly a real tour, got ToursCompleted=%d (%+v)", r.ToursCompleted, r)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q (the run earned, then margins died — never tour_unavailable)", r.ExitReason, tourExitStarvation)
	}
	// Fail CLOSED: the planner was NEVER handed an unreadable-derived 0 budget. The
	// unreadable iteration was skipped BEFORE planning, never planned-then-spent.
	for i, ms := range planner.maxSpends {
		if ms <= 0 {
			t.Fatalf("planner call %d received max-spend %d — the coordinator must never plan/spend on an unreadable (0) budget (RULINGS #4), max-spends=%v", i, ms, planner.maxSpends)
		}
	}
	// The unreadable read was RETRIED, not swallowed: >=2 GetAgent calls (the failed
	// one plus at least one recovery).
	if api.callCount() < 2 {
		t.Fatalf("expected the unreadable treasury read to be retried (>=2 GetAgent calls), got %d", api.callCount())
	}
}

// sp-7z7j control 1: a FIXED --max-spend with --iterations -1 is unchanged — the cap is
// a CONSTANT per tour and is never re-resolved from treasury, so a treasury blip cannot
// starve the planner's budget the way a dynamic cap could (why the field control ran all
// day). The per-buy working-capital floor still reads treasury (that guard is orthogonal
// to the cap), but the CAP handed to the planner is the fixed value on every tour.
func TestTour_FixedCapContinuousUsesConstantCapNeverReResolves(t *testing.T) {
	fx := dynamicCapFixture()
	api := &tourStubAPIClient{credits: 8_000_000}
	planner := &tourFakeRoutingClient{
		infeasibleOnZeroSpend: true, // a 0 budget would be refused — the fixed cap must never be 0
		plans: []*routing.TourPlan{
			roundTripPlan(),
			{Feasible: false, InfeasibleReason: "no_profitable_tour"},
		},
	}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-FIX-TOKEN")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-FIX", PlayerID: 1, ContainerID: "ctr-fix", Iterations: -1,
		MaxSpend:          500_000, // FIXED cap — constant per tour, no re-resolution
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("fixed-cap continuous tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.ToursCompleted != 1 {
		t.Fatalf("fixed-cap tour flew its productive tour then margins died; want ToursCompleted=1, got %d", r.ToursCompleted)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitStarvation)
	}
	// Every plan saw the constant fixed cap, never a re-resolved (25%-of-treasury) value.
	if len(planner.maxSpends) == 0 {
		t.Fatalf("expected the planner to be called at least once")
	}
	for i, ms := range planner.maxSpends {
		if ms != 500_000 {
			t.Fatalf("planner call %d received max-spend %d, want the constant fixed 500,000 (fixed cap never re-resolves)", i, ms)
		}
	}
}

// sp-7z7j control 2: iterations=1 with a dynamic cap still exits after exactly one tour
// (single-tour mode preserved). The dynamic re-resolution does not turn a one-shot into
// a loop.
func TestTour_DynamicCapSingleTourStillExitsAfterOne(t *testing.T) {
	fx := dynamicCapFixture()
	api := &tourStubAPIClient{credits: 8_000_000}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{roundTripPlan()}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-ONE-TOKEN")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-ONE", PlayerID: 1, ContainerID: "ctr-one", Iterations: 1,
		MaxSpend: 0, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("one-shot dynamic-cap tour returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.ToursCompleted != 1 {
		t.Fatalf("iterations=1 must fly exactly one tour, got %d", r.ToursCompleted)
	}
	if planner.calls != 1 {
		t.Fatalf("iterations=1 must call the planner exactly once, got %d", planner.calls)
	}
	if !r.Completed {
		t.Fatalf("a one-shot dynamic-cap tour must complete, got %+v", r)
	}
	if r.ExitReason != tourExitIterations {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitIterations)
	}
	// The single tour was still sized to 25% of live treasury (2,000,000), not 0.
	if len(planner.maxSpends) != 1 || planner.maxSpends[0] != 2_000_000 {
		t.Fatalf("planner max-spends = %v, want [2000000] (25%% of live treasury)", planner.maxSpends)
	}
}

package commands

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Serializing every planner in the fleet behind ONE token buys the dispersal property at
// the price of the whole fleet's planning throughput, because the solve — the slow part by
// three orders of magnitude — sits inside the token. These tests pin the split: planners
// whose sink universes can INTERSECT still take turns (dispersal is only obtainable by
// taking turns), and planners whose universes are DISJOINT plan at the same time, because
// there is no sink for them to converge on.

const (
	// domainBatchWait bounds an arrival barrier so a serialized run still finishes: every
	// planner that arrives alone pays it once. Generous enough that a loaded box cannot
	// mistake a slow goroutine for a serialized one.
	domainBatchWait = 3 * time.Second
	// domainQuickWait is the same bound for the tests that EXPECT serialization, where the
	// wait is paid per planner and nothing is proven by making it long.
	domainQuickWait = 60 * time.Millisecond
)

func domainSource(system string) string      { return system + "-A" }
func domainSink(system string, i int) string { return fmt.Sprintf("%s-B%d", system, i) }
func domainSinks(system string, n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, domainSink(system, i))
	}
	return out
}

// domainFixture builds a world of independent trading systems: every system in
// hullSystems carries a deep source market, and every system in sinkSystems carries
// `sinksPer` sinks each sized so ONE hull's tranche pair exactly fills the fleet-wide cap.
// neighbors is the gate topology the tour graph is derived from — nil leaves every system
// an island, which is what makes two hulls' sink universes provably disjoint.
func domainFixture(hullSystems, sinkSystems []string, sinksPer int, neighbors map[string][]string) *tourFixture {
	fx := &tourFixture{
		cargo: map[string]int{}, location: domainSource(hullSystems[0]), cargoCap: 100,
		markets:   map[string][]string{},
		bid:       map[string]map[string]int{},
		ask:       map[string]map[string]int{},
		tv:        map[string]map[string]int{},
		neighbors: neighbors,
	}
	for _, sys := range hullSystems {
		src := domainSource(sys)
		fx.markets[sys] = append(fx.markets[sys], src)
		fx.ask[src] = map[string]int{"G1": 100}
		fx.tv[src] = map[string]int{"G1": dispersalSourceTV}
	}
	for _, sys := range sinkSystems {
		for _, wp := range domainSinks(sys, sinksPer) {
			fx.markets[sys] = append(fx.markets[sys], wp)
			fx.bid[wp] = map[string]int{"G1": 200}
			fx.ask[wp] = map[string]int{"G1": 200}
			fx.tv[wp] = map[string]int{"G1": dispersalSinkTV}
		}
	}
	return fx
}

func domainPlan(system, sink string) *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, ProjectedProfit: 4000, Legs: []routing.TourLeg{
		leg(domainSource(system), system, buy("G1", dispersalUnits, 100)),
		leg(sink, shared.ExtractSystemSymbol(sink), sell("G1", dispersalUnits, 200)),
	}}
}

// domainPlanner is dispersalPlanner generalized over a multi-system world, plus the one
// measurement these tests exist for: how many solves were IN FLIGHT at once. It applies the
// same availability rule the solver's net_absorption does and always prefers the first sink
// still deep enough, so every planner reading the same depth picks the same sink.
type domainPlanner struct {
	routing.RoutingClient
	// sinksFor ranks the sinks a hull in this system will try, best first.
	sinksFor func(system string) []string
	barrier  *arrivalBarrier

	mu          sync.Mutex
	calls       int
	inFlight    int
	maxInFlight int
}

func (p *domainPlanner) plannerCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *domainPlanner) peakConcurrentSolves() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxInFlight
}

func (p *domainPlanner) OptimizeTradeTour(
	_ context.Context,
	_ []routing.TourGoodSnapshot,
	_ []routing.TourWaypoint,
	ship routing.TourShipState,
	_ routing.TourConstraints,
	_ []routing.TourDepositCandidate,
	absorbed []routing.TourMarketAbsorption,
) (*routing.TourPlan, error) {
	p.mu.Lock()
	p.calls++
	p.inFlight++
	if p.inFlight > p.maxInFlight {
		p.maxInFlight = p.inFlight
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}()

	planned := map[string]int{}
	for _, a := range absorbed {
		if a.Side == absorption.SideSell {
			planned[a.Waypoint] += a.PlannedUnits
		}
	}
	if p.barrier != nil {
		p.barrier.arrive()
	}

	for _, wp := range p.sinksFor(ship.CurrentSystem) {
		occupied := int(math.Ceil(float64(planned[wp]) / float64(dispersalSinkTV)))
		if (tourACapTranches-occupied)*dispersalSinkTV >= dispersalUnits {
			return domainPlan(ship.CurrentSystem, wp), nil
		}
	}
	return &routing.TourPlan{Feasible: false, InfeasibleReason: "every sink is already occupied"}, nil
}

// domainShips builds one hull per system, each standing on its own system's source market.
func domainShips(t *testing.T, fx *tourFixture, systems []string) []*navigation.Ship {
	t.Helper()
	ships := make([]*navigation.Ship, 0, len(systems))
	for _, sys := range systems {
		fx.location = domainSource(sys)
		ships = append(ships, fx.buildShip(t, "TOUR-"+sys))
	}
	return ships
}

// planConcurrently runs one planAndReserve per ship, all released together.
func planConcurrently(t *testing.T, h *RunTourCoordinatorHandler, ships []*navigation.Ship) []planOutcome {
	t.Helper()
	outcomes := make([]planOutcome, len(ships))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, ship := range ships {
		wg.Add(1)
		go func(i int, ship *navigation.Ship) {
			defer wg.Done()
			cmd := &RunTourCoordinatorCommand{
				ShipSymbol: ship.ShipSymbol(), PlayerID: 1, ContainerID: fmt.Sprintf("ctr-%d", i),
			}
			<-start
			_, _, reason, feasible, err := h.planAndReserve(context.Background(), cmd, ship, tourPlanBudget{maxHops: 3, maxSpend: 1_000_000})
			outcomes[i] = planOutcome{feasible: feasible, reason: reason, err: err}
		}(i, ship)
	}
	close(start)
	wg.Wait()
	return outcomes
}

func requireAllFeasible(t *testing.T, outcomes []planOutcome) {
	t.Helper()
	for i, o := range outcomes {
		require.NoErrorf(t, o.err, "hull %d", i)
		require.Truef(t, o.feasible, "hull %d secured no tour (%q)", i, o.reason)
	}
}

// requireNoSinkOverCap asserts the ledger's own money invariant directly: no
// (waypoint, good, side) pool holds more PLANNED units than the fleet-wide depth cap that
// sized it. Two planners that both reserved the same sink show up here as an over-cap
// pool, which is the failure the plan gate and the ledger transaction jointly exist to
// make impossible.
func requireNoSinkOverCap(t *testing.T, db *gorm.DB, capUnits int) {
	t.Helper()
	var rows []persistence.MarketAbsorptionLedgerModel
	require.NoError(t, db.Where("side = ? AND state = ?", absorption.SideSell, "PLANNED").Find(&rows).Error)
	units := map[string]int{}
	for _, r := range rows {
		units[r.Waypoint+"/"+r.Good] += r.Units
	}
	for lane, n := range units {
		require.LessOrEqualf(t, n, capUnits,
			"sink %s holds %d planned units over a %d-unit cap: two planners reserved the same depth", lane, n, capUnits)
	}
}

// THE THROUGHPUT DEFECT. Six hulls, six systems, no gate edges between them: no sink any
// one of them can reach is reachable by another, so nothing they choose can collide. They
// must plan AT THE SAME TIME. Serializing them costs the fleet one whole solve of latency
// per hull for a dispersal guarantee that is vacuous here.
func TestTourPlanGate_DisjointTourGraphsPlanConcurrently(t *testing.T) {
	systems := []string{"X1-D1", "X1-D2", "X1-D3", "X1-D4", "X1-D5", "X1-D6"}
	fx := domainFixture(systems, systems, 1, nil)
	planner := &domainPlanner{
		sinksFor: func(system string) []string { return domainSinks(system, 1) },
		barrier:  newArrivalBarrier(len(systems), domainBatchWait),
	}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, 0)

	outcomes := planConcurrently(t, h, domainShips(t, fx, systems))

	requireAllFeasible(t, outcomes)
	require.Equalf(t, len(systems), planner.peakConcurrentSolves(),
		"only %d of %d solves ever overlapped: hulls whose tour graphs share no system cannot "+
			"contend for depth, so serializing them buys nothing and costs the fleet a solve of "+
			"latency per hull", planner.peakConcurrentSolves(), len(systems))
	require.Equal(t, len(systems), planner.plannerCalls(), "one solve each")
	require.Len(t, reservedSinks(t, db), len(systems))
	requireNoSinkOverCap(t, db, dispersalUnits)
}

// THE INVARIANT THAT SURVIVES. Two hulls in different home systems that BOTH gate onto one
// shared system share that system's sinks — a finer shard key (the hull's own system) would
// let them plan blind to each other and converge. They must take turns, and the turn must
// buy what turns are for: each is steered off the other's sink WHILE planning, in one solve.
func TestTourPlanGate_OverlappingTourGraphsStillTakeTurns(t *testing.T) {
	const hub = "X1-SH"
	hulls := []string{"X1-N1", "X1-N2"}
	fx := domainFixture(hulls, []string{hub}, 2, map[string][]string{
		"X1-N1": {hub}, "X1-N2": {hub},
	})
	planner := &domainPlanner{
		sinksFor: func(string) []string { return domainSinks(hub, 2) },
		barrier:  newArrivalBarrier(len(hulls), domainQuickWait),
	}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, 0)

	outcomes := planConcurrently(t, h, domainShips(t, fx, hulls))

	requireAllFeasible(t, outcomes)
	require.Equalf(t, 1, planner.peakConcurrentSolves(),
		"two hulls sharing the gate neighbour that holds every sink solved at once: they net the "+
			"same pre-reservation depth, rank the same sink best and haul as a pack into depth that "+
			"fits one of them")
	require.Equal(t, len(hulls), planner.plannerCalls(),
		"one solve each: a sink taken between the netting and the reserve shows up as a breach and a re-plan")
	require.Len(t, reservedSinks(t, db), len(hulls), "the two hulls hold the two distinct sinks")
	requireNoSinkOverCap(t, db, dispersalUnits)
}

// The order two planners take overlapping domains in is what decides whether they can
// hold-and-wait on each other, so it must be a property of the DOMAINS and never of the
// order a caller happened to resolve its tour graph in — a tour graph is listed home-first,
// so two hulls gated onto each other resolve the same pair in opposite orders.
func TestPlanDomainKeys_AreCanonicallyOrderedAndDeduped(t *testing.T) {
	homeP1 := planDomainKeys(1, []string{"X1-P1", "X1-P2", "X1-P1"})
	homeP2 := planDomainKeys(1, []string{"X1-P2", "X1-P1"})

	require.Equal(t, homeP2, homeP1, "two planners over the same systems must queue in the same order")
	require.Equal(t, []planDomain{{playerID: 1, system: "X1-P1"}, {playerID: 1, system: "X1-P2"}}, homeP1)
	require.Len(t, planDomainKeys(1, nil), 1, "a plan whose graph resolved to nothing still takes a domain")
}

// MUTUAL OVERLAP, end to end. Two hulls gated onto each other hold the SAME pair of domains
// resolved in opposite orders — the AB/BA hold-and-wait shape. Both must still fly.
func TestTourPlanGate_MutuallyOverlappingGraphsDoNotHoldAndWait(t *testing.T) {
	hulls := []string{"X1-P1", "X1-P2"}
	fx := domainFixture(hulls, hulls, 1, map[string][]string{
		"X1-P1": {"X1-P2"}, "X1-P2": {"X1-P1"},
	})
	planner := &domainPlanner{
		// Both rank the same sink first, so the pair contends for domains AND for depth.
		sinksFor: func(string) []string { return []string{domainSink("X1-P1", 1), domainSink("X1-P2", 1)} },
		barrier:  newArrivalBarrier(len(hulls), domainQuickWait),
	}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, 0)
	h.planGateWait = 2 * time.Second

	done := make(chan []planOutcome, 1)
	go func() { done <- planConcurrently(t, h, domainShips(t, fx, hulls)) }()
	var outcomes []planOutcome
	select {
	case outcomes = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("planners never finished: two hulls holding the same pair of domains deadlocked on each other")
	}

	requireAllFeasible(t, outcomes)
	require.Equal(t, len(hulls), planner.plannerCalls(), "one solve each")
	require.Len(t, reservedSinks(t, db), len(hulls))
}

// THE KILL SWITCH. A concurrency bound of one is the pre-existing fleet-wide token: every
// planner takes its turn regardless of how disjoint its sink universe is.
func TestTourPlanGate_ConcurrencyBoundOneSerializesTheWholeFleet(t *testing.T) {
	systems := []string{"X1-K1", "X1-K2", "X1-K3", "X1-K4"}
	fx := domainFixture(systems, systems, 1, nil)
	planner := &domainPlanner{
		sinksFor: func(system string) []string { return domainSinks(system, 1) },
		barrier:  newArrivalBarrier(len(systems), domainQuickWait),
	}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, 0)
	h.SetPlanConcurrency(1)

	outcomes := planConcurrently(t, h, domainShips(t, fx, systems))

	requireAllFeasible(t, outcomes)
	require.Equal(t, 1, planner.peakConcurrentSolves(), "bound 1 must reproduce the fleet-wide token exactly")
	require.Equal(t, len(systems), planner.plannerCalls())
	require.Len(t, reservedSinks(t, db), len(systems))
}

// THE BOUND ITSELF. More disjoint domains than slots must not stampede the routing service
// past what its worker pool can search at full budget — the bound caps solves in flight
// while still letting every hull through.
func TestTourPlanGate_ConcurrencyBoundCapsSolvesInFlight(t *testing.T) {
	systems := []string{"X1-C1", "X1-C2", "X1-C3", "X1-C4", "X1-C5", "X1-C6"}
	const bound = 2
	fx := domainFixture(systems, systems, 1, nil)
	planner := &domainPlanner{
		sinksFor: func(system string) []string { return domainSinks(system, 1) },
		barrier:  newArrivalBarrier(bound, domainQuickWait),
	}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, 0)
	h.SetPlanConcurrency(bound)

	outcomes := planConcurrently(t, h, domainShips(t, fx, systems))

	requireAllFeasible(t, outcomes)
	require.LessOrEqual(t, planner.peakConcurrentSolves(), bound, "the bound caps solves in flight")
	require.Equal(t, bound, planner.peakConcurrentSolves(), "and the bound is actually reached")
	require.Len(t, reservedSinks(t, db), len(systems))
}

// THE STALE SNAPSHOT. A planner outside the gate's reach — another engine's container, which
// records its own occupancy through the ledger and never through this gate — can take the
// sink a tour is mid-solve on. The Reserve stays the arbiter: the plan breaches, the tour
// re-plans against the now-occupied sink, and nothing ends up over its cap.
func TestTourPlanGate_ForeignReservationDuringSolveBreachesAndReplans(t *testing.T) {
	const sys = "X1-ST"
	fx := domainFixture([]string{sys}, []string{sys}, 2, nil)
	inSolve := make(chan struct{}, 1)
	holdSolve := make(chan struct{})
	planner := &domainPlanner{sinksFor: func(string) []string { return domainSinks(sys, 2) }}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	h.SetAbsorptionLedger(&solveWatchLedger{Ledger: ledger, entered: inSolve, hold: holdSolve}, 0)

	ships := domainShips(t, fx, []string{sys})
	cmd := &RunTourCoordinatorCommand{ShipSymbol: ships[0].ShipSymbol(), PlayerID: 1, ContainerID: "ctr-tour"}
	done := make(chan planOutcome, 1)
	go func() {
		_, _, reason, feasible, err := h.planAndReserve(context.Background(), cmd, ships[0], tourPlanBudget{maxHops: 3, maxSpend: 1_000_000})
		done <- planOutcome{feasible: feasible, reason: reason, err: err}
	}()

	// The tour has read depth and chosen sink 1; a foreign engine takes it before the reserve.
	<-inSolve
	_, ok, err := ledger.Reserve(context.Background(), 1, "ctr-foreign", "arb", []absorption.ReserveEntry{{
		Waypoint: domainSink(sys, 1), Good: "G1", Side: absorption.SideSell,
		Units: dispersalUnits, CapUnits: dispersalUnits, TTL: time.Hour,
	}})
	require.NoError(t, err)
	require.True(t, ok)
	close(holdSolve)

	outcome := <-done
	require.NoError(t, outcome.err)
	require.Truef(t, outcome.feasible, "the tour never re-planned onto the free sink: %s", outcome.reason)
	require.Equal(t, map[string]string{
		domainSink(sys, 1): "ctr-foreign",
		domainSink(sys, 2): "ctr-tour",
	}, reservedSinks(t, db), "the breached plan is rolled back whole and the re-plan takes the sink still free")
	requireNoSinkOverCap(t, db, dispersalUnits)
}

// solveWatchLedger signals once, at the START of the reservation that follows the first
// solve, and parks there — the window a test needs to move the world under a plan that has
// already been chosen. Everything else is the real DB-backed ledger.
type solveWatchLedger struct {
	absorption.Ledger
	entered chan struct{}
	hold    chan struct{}
	once    sync.Once
}

func (s *solveWatchLedger) Reserve(ctx context.Context, playerID int, containerID, engine string, entries []absorption.ReserveEntry) ([]string, bool, error) {
	first := false
	s.once.Do(func() { first = true })
	if first {
		s.entered <- struct{}{}
		<-s.hold
	}
	return s.Ledger.Reserve(ctx, playerID, containerID, engine, entries)
}

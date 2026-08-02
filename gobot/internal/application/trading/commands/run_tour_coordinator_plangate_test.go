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
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// Plan-time absorption reservations must be visible to CONCURRENT planners, not only to
// the next one to start. These tests drive N planners through ONE shared handler against
// the REAL DB-backed ledger, exactly as the daemon does: the trade-fleet coordinator
// launches a tour container per idle hull and each container plans on its own goroutine,
// so a batch dispatch puts N planners in the netting read at the same instant.

const (
	// dispersalSinkTV sizes each sink so ONE hull's tranche pair exactly fills the
	// fleet-wide cap (tourACapTranches × trade_volume): a second hull on the same sink
	// breaches. That makes "did they disperse?" a binary, unambiguous outcome.
	dispersalSinkTV    = 20
	dispersalUnits     = dispersalSinkTV * tourACapTranches
	dispersalSourceTV  = 1000 // the buy side is deep, so only sink contention is ever tested
	dispersalBatchWait = 60 * time.Millisecond
	dispersalHold      = 100 * time.Millisecond
)

func dispersalSink(i int) string { return fmt.Sprintf("X1-S1-B%d", i) }

// dispersalFixture is one deep source market plus `sinks` shallow sinks of the same good,
// all in the home system.
func dispersalFixture(sinks int) *tourFixture {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A"}},
		bid:     map[string]map[string]int{},
		ask:     map[string]map[string]int{"X1-S1-A": {"G1": 100}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G1": dispersalSourceTV}},
	}
	for i := 1; i <= sinks; i++ {
		wp := dispersalSink(i)
		fx.markets["X1-S1"] = append(fx.markets["X1-S1"], wp)
		fx.bid[wp] = map[string]int{"G1": 200}
		fx.ask[wp] = map[string]int{"G1": 200}
		fx.tv[wp] = map[string]int{"G1": dispersalSinkTV}
	}
	return fx
}

func dispersalPlan(sink string) *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, ProjectedProfit: 4000, Legs: []routing.TourLeg{
		leg("X1-S1-A", "X1-S1", buy("G1", dispersalUnits, 100)),
		leg(sink, "X1-S1", sell("G1", dispersalUnits, 200)),
	}}
}

// arrivalBarrier releases a batch of goroutines together once `want` of them have arrived,
// or after `wait` when fewer ever do, and re-arms for the next batch. It is what makes a
// concurrency test deterministic instead of a coin flip on the scheduler: every planner
// parked here has already done its ledger read, so releasing them together reproduces the
// live batch-dispatch race exactly — N reads, then N reserves.
type arrivalBarrier struct {
	mu      sync.Mutex
	want    int
	wait    time.Duration
	arrived int
	batch   chan struct{}
}

func newArrivalBarrier(want int, wait time.Duration) *arrivalBarrier {
	return &arrivalBarrier{want: want, wait: wait, batch: make(chan struct{})}
}

func (b *arrivalBarrier) arrive() {
	b.mu.Lock()
	b.arrived++
	batch := b.batch
	if b.arrived >= b.want {
		b.arrived = 0
		b.batch = make(chan struct{})
		b.mu.Unlock()
		close(batch)
		return
	}
	b.mu.Unlock()

	timer := time.NewTimer(b.wait)
	defer timer.Stop()
	select {
	case <-batch:
	case <-timer.C:
		b.mu.Lock()
		if b.batch == batch && b.arrived > 0 {
			b.arrived--
		}
		b.mu.Unlock()
	}
}

// dispersalPlanner stands in for the Python solver's sink choice and applies the SAME
// availability rule the solver's net_absorption does: outstanding PLANNED depth drops
// ceil(planned / trade_volume) tranches off a pool's head, and this plan needs the full
// tranche pair. It always prefers the lowest-numbered sink still deep enough — so every
// planner reading the same depth picks the SAME sink, which is precisely the convergence
// the ledger is supposed to break.
type dispersalPlanner struct {
	routing.RoutingClient
	sinks   int
	barrier *arrivalBarrier
	// onPlan, when set, runs before the barrier — the seam a test uses to observe that a
	// planner has entered the solve (and therefore finished its ledger read).
	onPlan func()

	mu    sync.Mutex
	calls int
}

func (p *dispersalPlanner) plannerCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *dispersalPlanner) OptimizeTradeTour(
	_ context.Context,
	_ []routing.TourGoodSnapshot,
	_ []routing.TourWaypoint,
	_ routing.TourShipState,
	_ routing.TourConstraints,
	_ []routing.TourDepositCandidate,
	absorbed []routing.TourMarketAbsorption,
) (*routing.TourPlan, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()

	planned := map[string]int{}
	for _, a := range absorbed {
		if a.Side == absorption.SideSell {
			planned[a.Waypoint] += a.PlannedUnits
		}
	}
	if p.onPlan != nil {
		p.onPlan()
	}
	if p.barrier != nil {
		p.barrier.arrive()
	}

	for i := 1; i <= p.sinks; i++ {
		wp := dispersalSink(i)
		occupied := int(math.Ceil(float64(planned[wp]) / float64(dispersalSinkTV)))
		if (tourACapTranches-occupied)*dispersalSinkTV >= dispersalUnits {
			return dispersalPlan(wp), nil
		}
	}
	return &routing.TourPlan{Feasible: false, InfeasibleReason: "every sink is already occupied"}, nil
}

// reservedSinks maps sink waypoint -> owning container for every PLANNED sell row.
func reservedSinks(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	var rows []persistence.MarketAbsorptionLedgerModel
	require.NoError(t, db.Where("side = ? AND state = ?", absorption.SideSell, "PLANNED").Find(&rows).Error)
	out := map[string]string{}
	for _, r := range rows {
		out[r.Waypoint] = r.ContainerID
	}
	return out
}

// planOutcome is one hull's planAndReserve result, collected off its goroutine.
type planOutcome struct {
	feasible bool
	reason   string
	err      error
}

// THE CORE RACE. Six hulls bought in one batch are discovered on the same coordinator tick
// and plan simultaneously. Each nets the ledger, each ranks the same sink best, and nothing
// in the plan itself knows what the other five are choosing right now. They must still end
// up on six different sinks — and must get there at PLAN time, in one solve each, not by
// colliding at reserve time and re-planning (the live "reserve breached the fleet-wide sink
// cap - re-planning" churn).
//
// The two windows a planner can be invisible in are both forced open here: the barrier holds
// every planner in the solve until the whole batch has read, and the spy parks the first
// reservation until a second planner reaches a solve. Both stay shut only if the read and
// the write that follows it are one critical section.
func TestTourPlanGate_ConcurrentPlannersDoNotAllTakeTheSameSink(t *testing.T) {
	const hulls = 6
	fx := dispersalFixture(hulls)
	planner := &dispersalPlanner{sinks: hulls, barrier: newArrivalBarrier(hulls, dispersalBatchWait)}
	secondSolve := make(chan struct{})
	var secondOnce sync.Once
	planner.onPlan = func() {
		if planner.plannerCalls() >= 2 {
			secondOnce.Do(func() { close(secondSolve) })
		}
	}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	h.SetAbsorptionLedger(&gateSpyLedger{
		Ledger: ledger, holdFirstReserve: true, holdUntil: secondSolve, holdFor: dispersalHold,
	}, 0)

	ship := fx.buildShip(t, "TOUR-BATCH")
	outcomes := make([]planOutcome, hulls)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < hulls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := &RunTourCoordinatorCommand{
				ShipSymbol: fmt.Sprintf("TOUR-%d", i), PlayerID: 1, ContainerID: fmt.Sprintf("ctr-%d", i),
			}
			<-start
			_, _, reason, feasible, err := h.planAndReserve(context.Background(), cmd, ship, tourPlanBudget{maxHops: 3, maxSpend: 1_000_000})
			outcomes[i] = planOutcome{feasible: feasible, reason: reason, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, o := range outcomes {
		require.NoErrorf(t, o.err, "hull %d", i)
		require.Truef(t, o.feasible, "hull %d secured no tour (%q): a batch-dispatched hull that "+
			"loses the sink race to its own packmates earns nothing", i, o.reason)
	}
	sinks := reservedSinks(t, db)
	require.Lenf(t, sinks, hulls, "the %d hulls hold %d distinct sinks (%v): concurrent planners "+
		"that cannot see each other's plan-time reservations all rank the same sink best and "+
		"haul as a pack into depth that fits one of them", hulls, len(sinks), sinks)
	require.Equalf(t, hulls, planner.plannerCalls(), "each hull must be STEERED off an occupied "+
		"sink while planning, in one solve; extra solves are the breach-and-re-plan churn the "+
		"ledger is meant to make unnecessary")
}

// The RE-PLAN window. An incumbent hull releases its own stale PLANNED rows before it
// re-plans (so the fresh plan nets against OTHERS' depth, not its own). For the length of
// that release its own held sink looks free to everyone. A planner reading right then takes
// the sink out from under the incumbent — which is how a hull loses the sink it was already
// committed to and the fleet churns instead of settling. The release therefore has to sit
// INSIDE the same critical section as the netting read and the reservation.
//
// The rival is held at the gate for the whole window here (the spy parks the incumbent
// mid-release until the rival plans, or 100ms), so if that window were reachable at all the
// rival would take sink 1 every time.
func TestTourPlanGate_IncumbentKeepsItsSinkAcrossTheReplanReleaseWindow(t *testing.T) {
	fx := dispersalFixture(2)
	rivalPlanned := make(chan struct{})
	var plannedOnce sync.Once
	planner := &dispersalPlanner{sinks: 2, onPlan: func() { plannedOnce.Do(func() { close(rivalPlanned) }) }}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)

	// The incumbent already holds sink 1 from its previous plan.
	incumbentReleased := make(chan struct{})
	spy := &gateSpyLedger{
		Ledger: ledger, releaseWatch: "ctr-incumbent", released: incumbentReleased,
		holdUntil: rivalPlanned, holdFor: dispersalHold,
	}
	h.SetAbsorptionLedger(spy, 0)
	_, ok, err := ledger.Reserve(context.Background(), 1, "ctr-incumbent", "tour", []absorption.ReserveEntry{{
		Waypoint: dispersalSink(1), Good: "G1", Side: absorption.SideSell,
		Units: dispersalUnits, CapUnits: dispersalUnits, TTL: time.Hour,
	}})
	require.NoError(t, err)
	require.True(t, ok)

	ship := fx.buildShip(t, "TOUR-REPLAN")
	var incumbent, rival planOutcome
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-INC", PlayerID: 1, ContainerID: "ctr-incumbent"}
		_, _, reason, feasible, e := h.planAndReserve(context.Background(), cmd, ship, tourPlanBudget{maxHops: 3, maxSpend: 1_000_000})
		incumbent = planOutcome{feasible: feasible, reason: reason, err: e}
	}()

	// Start the rival the instant the incumbent's rows are gone — the exact window an
	// unserialized planner reads a free sink in.
	<-incumbentReleased
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-RIV", PlayerID: 1, ContainerID: "ctr-rival"}
		_, _, reason, feasible, e := h.planAndReserve(context.Background(), cmd, ship, tourPlanBudget{maxHops: 3, maxSpend: 1_000_000})
		rival = planOutcome{feasible: feasible, reason: reason, err: e}
	}()
	wg.Wait()

	require.NoError(t, incumbent.err)
	require.NoError(t, rival.err)
	require.Truef(t, incumbent.feasible, "the incumbent lost its own sink during its re-plan release window: %s", incumbent.reason)
	require.Truef(t, rival.feasible, "the rival secured no tour: %s", rival.reason)
	require.Equal(t, map[string]string{dispersalSink(1): "ctr-incumbent", dispersalSink(2): "ctr-rival"},
		reservedSinks(t, db), "the incumbent keeps the sink it was already committed to; the rival is steered to the free one")
	require.Equal(t, 2, planner.plannerCalls(), "one solve each: a stolen sink shows up as a breach and a re-plan")
}

// FAIL CLOSED. A planner that cannot be serialized against its peers has no safe way to
// read depth: planning anyway is planning blind into a sink another hull is mid-way through
// taking. It refuses the tour and reserves nothing, rather than co-dumping on spec.
func TestTourPlanGate_ContendedGateRefusesTheTourAndReservesNothing(t *testing.T) {
	fx := dispersalFixture(2)
	inSolve := make(chan struct{})
	holdSolve := make(chan struct{})
	planner := &dispersalPlanner{sinks: 2, onPlan: func() {
		select {
		case inSolve <- struct{}{}:
			<-holdSolve
		default: // later solves run straight through
		}
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, 0)
	h.planGateWait = 20 * time.Millisecond

	ship := fx.buildShip(t, "TOUR-CONTEND")
	var holder, blocked planOutcome
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-HOLD", PlayerID: 1, ContainerID: "ctr-holder"}
		_, _, reason, feasible, e := h.planAndReserve(context.Background(), cmd, ship, tourPlanBudget{maxHops: 3, maxSpend: 1_000_000})
		holder = planOutcome{feasible: feasible, reason: reason, err: e}
	}()

	<-inSolve // the holder is inside the solve, holding the gate
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-BLOCK", PlayerID: 1, ContainerID: "ctr-blocked"}
	_, _, reason, feasible, err := h.planAndReserve(context.Background(), cmd, ship, tourPlanBudget{maxHops: 3, maxSpend: 1_000_000})
	blocked = planOutcome{feasible: feasible, reason: reason, err: err}
	close(holdSolve)
	wg.Wait()

	require.NoError(t, blocked.err)
	require.Falsef(t, blocked.feasible, "a planner that could not be serialized planned anyway (%q)", blocked.reason)
	require.Contains(t, blocked.reason, "tour unavailable")
	var blockedRows int64
	require.NoError(t, db.Model(&persistence.MarketAbsorptionLedgerModel{}).
		Where("container_id = ?", "ctr-blocked").Count(&blockedRows).Error)
	require.Zerof(t, blockedRows, "the refused planner must hold no depth at all")
	require.NoError(t, holder.err)
	require.True(t, holder.feasible, "the holder's own tour is unaffected")
}

// NON-INTERFERENCE. One planner with nobody to contend against plans and reserves exactly
// as before: its first-choice sink, one solve, one buy-side and one sell-side row.
func TestTourPlanGate_UncontendedPlannerIsUnchanged(t *testing.T) {
	fx := dispersalFixture(3)
	planner := &dispersalPlanner{sinks: 3}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, 0)

	ship := fx.buildShip(t, "TOUR-SOLO")
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-SOLO", PlayerID: 1, ContainerID: "ctr-solo"}
	plan, _, reason, feasible, err := h.planAndReserve(context.Background(), cmd, ship, tourPlanBudget{maxHops: 3, maxSpend: 1_000_000})

	require.NoError(t, err)
	require.Truef(t, feasible, "uncontended planner refused: %s", reason)
	require.Equal(t, dispersalSink(1), plan.Legs[1].Waypoint, "it takes its first-choice sink")
	require.Equal(t, 1, planner.plannerCalls())
	require.Equal(t, map[string]string{dispersalSink(1): "ctr-solo"}, reservedSinks(t, db))
	require.Len(t, tourLedgerRows(t, db, "ctr-solo"), 2, "one buy-side and one sell-side hold")
}

// gateSpyLedger parks one of the two ledger WRITES that bracket the netting read — a
// container's re-plan release, or the reservation itself — holding that window wide open
// until a rival planner has read (holdUntil) or holdFor elapses. Without it, "can a
// concurrent planner read inside this window?" is a scheduler coin flip and a passing test
// proves nothing about it. Everything else is the real DB-backed ledger.
type gateSpyLedger struct {
	absorption.Ledger
	// releaseWatch names the container whose re-plan release is signalled and then parked.
	releaseWatch string
	released     chan struct{}
	// holdFirstReserve parks the first reservation of any container.
	holdFirstReserve bool

	holdUntil chan struct{}
	holdFor   time.Duration

	releaseOnce sync.Once
	reserveOnce sync.Once
}

func (s *gateSpyLedger) park() {
	timer := time.NewTimer(s.holdFor)
	defer timer.Stop()
	select {
	case <-s.holdUntil:
	case <-timer.C:
	}
}

func (s *gateSpyLedger) Reserve(ctx context.Context, playerID int, containerID, engine string, entries []absorption.ReserveEntry) ([]string, bool, error) {
	if s.holdFirstReserve {
		first := false
		s.reserveOnce.Do(func() { first = true })
		if first {
			s.park()
		}
	}
	return s.Ledger.Reserve(ctx, playerID, containerID, engine, entries)
}

func (s *gateSpyLedger) ReleaseByContainerExcept(ctx context.Context, containerID string, playerID int, keep []absorption.LaneKey) (int, error) {
	n, err := s.Ledger.ReleaseByContainerExcept(ctx, containerID, playerID, keep)
	if containerID != s.releaseWatch {
		return n, err
	}
	first := false
	s.releaseOnce.Do(func() {
		first = true
		close(s.released)
	})
	if first {
		s.park()
	}
	return n, err
}

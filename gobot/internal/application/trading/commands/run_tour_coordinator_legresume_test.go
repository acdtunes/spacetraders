package commands

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// The restart-resume of an in-flight SELL leg. A TRADING container persists knobs and
// reposition state but nothing about the leg the hull is flying, so a bounce made every
// laden hull wait out a full planner round trip before it could discharge cargo it had
// already bought — measured at a median 5.3 minutes to first plan against a 0.7-minute
// plan-to-trade gap, i.e. the wait, not the trade, is the cost. These pin the resume: what
// it flies, what it refuses to fly, that it touches no reservation, and that no decline
// path can leave cargo aboard a released hull.

// legResumeFixture is a one-system world with a sink: the hull sits at X1-R1-A holding 40
// G1, and X1-R1-B IMPORTS G1 at a rich bid. Nothing here is tourable (the fake planner
// returns infeasible), so the ONLY way G1 reaches X1-R1-B before the exit sweep is the
// resume.
func legResumeFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{"G1": 40}, location: "X1-R1-A", cargoCap: 100,
		markets:   map[string][]string{"X1-R1": {"X1-R1-A", "X1-R1-B"}},
		ask:       map[string]map[string]int{"X1-R1-A": {"G1": 100}, "X1-R1-B": {"G1": 260}},
		bid:       map[string]map[string]int{"X1-R1-B": {"G1": 200}},
		tv:        map[string]map[string]int{"X1-R1-A": {"G1": 1000}, "X1-R1-B": {"G1": 1000}},
		tradeType: map[string]map[string]string{"X1-R1-B": {"G1": "IMPORT"}},
	}
}

// timelineLen reads the buy/sell timeline under the fixture lock, so a planner double can
// sample it mid-run to prove ordering (did anything trade BEFORE the first plan?).
func (f *tourFixture) timelineLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timeline)
}

// firstPlanProbe is a planner double that records how much trading had already happened
// when it was first consulted. That number IS the mechanism under test: a resume trades
// before any plan exists, a re-plan cannot.
type firstPlanProbe struct {
	mu       sync.Mutex
	fx       *tourFixture
	tradesAt int
}

func (p *firstPlanProbe) plan(_ routing.TourShipState) *routing.TourPlan {
	p.mu.Lock()
	if p.tradesAt < 0 {
		p.tradesAt = p.fx.timelineLen()
	}
	p.mu.Unlock()
	return infeasibleTour()
}

func (p *firstPlanProbe) tradesBeforeFirstPlan() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tradesAt
}

func newFirstPlanProbe(fx *tourFixture) (*firstPlanProbe, *tourFakeRoutingClient) {
	probe := &firstPlanProbe{fx: fx, tradesAt: -1}
	return probe, &tourFakeRoutingClient{planFn: probe.plan}
}

// fakeOutstandingPurchases is the ledger-backed rebuild of what a hull bought before the
// bounce and has not discharged — the input the stranded veto fails closed on.
type fakeOutstandingPurchases struct{ byHull map[string]map[string]int }

func (f *fakeOutstandingPurchases) OutstandingPurchases(_ context.Context, _ int, _ string) (map[string]map[string]int, error) {
	return f.byHull, nil
}

var _ ledger.OutstandingPurchaseReader = (*fakeOutstandingPurchases)(nil)

// THE UNLOCK. A hull re-adopted mid-leg — holding G1 it already paid for, with the sink it
// was flying to persisted — finishes that sell BEFORE any planner is consulted. Without
// this the hull idles through a full plan round trip and the sale lands on whatever the
// re-plan picks instead.
func TestTourLegResume_LadenHullFinishesTheSellLegBeforeAnyPlan(t *testing.T) {
	fx := legResumeFixture()
	probe, planner := newFirstPlanProbe(fx)
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RESUME", PlayerID: 1, ContainerID: "ctr-resume", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)
	r := tourResponse(t, resp)

	require.Equal(t, 1, probe.tradesBeforeFirstPlan(),
		"the resumed sell must land BEFORE the first planner call - that wait is the whole cost")
	require.Equal(t, []string{"SELL:G1"}, fx.timeline)
	require.Equal(t, "X1-R1-B", fx.location, "the hull flies to the persisted sink")
	require.Empty(t, fx.cargo["G1"], "the hold is discharged by the resume")
	require.Zero(t, r.ExitHoldLiquidations,
		"the exit sweep found nothing left to sell, so the resume - not the sweep - discharged the hold")
	require.False(t, r.CargoStranded)
}

// A hull that came up EMPTY has nothing to resume: a stale leg key must never fly it
// anywhere. Re-planning from where it stands is correct here and stays the behaviour.
func TestTourLegResume_UnladenHullPlansInsteadOfFlying(t *testing.T) {
	fx := legResumeFixture()
	fx.cargo = map[string]int{}
	probe, planner := newFirstPlanProbe(fx)
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-EMPTY", PlayerID: 1, ContainerID: "ctr-empty", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)

	require.Equal(t, 0, probe.tradesBeforeFirstPlan(), "nothing may trade before the plan")
	require.Equal(t, "X1-R1-A", fx.location, "an empty hull is never flown to a persisted sink")
	require.NotContains(t, fx.navDests, "X1-R1-B")
}

// STALENESS is resolved by re-reading, not by storing. The persisted waypoint is durable;
// whether it is still worth flying to is recomputed from the live snapshot every time. A
// sink that no longer bids for the held good declines the resume and the run re-plans -
// which is what makes an age knob unnecessary.
func TestTourLegResume_DeadSinkDeclinesAndReplans(t *testing.T) {
	fx := legResumeFixture()
	fx.bid = map[string]map[string]int{} // the bid that justified the leg is gone
	probe, planner := newFirstPlanProbe(fx)
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DEADSINK", PlayerID: 1, ContainerID: "ctr-deadsink", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)

	require.Equal(t, 0, probe.tradesBeforeFirstPlan())
	require.Equal(t, "X1-R1-A", fx.location, "no bid, no flight - the re-plan decides from where the hull stands")
}

// A STALE market read is the same refusal: freshness is the gate the resume shares with
// every other cash-recovery path, so a sink priced from an old observation is not flown to.
func TestTourLegResume_StaleSinkDeclines(t *testing.T) {
	fx := legResumeFixture()
	fx.staleMarkets = map[string]bool{"X1-R1-B": true}
	probe, planner := newFirstPlanProbe(fx)
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-STALE", PlayerID: 1, ContainerID: "ctr-stale", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)

	require.Equal(t, 0, probe.tradesBeforeFirstPlan())
	require.Equal(t, "X1-R1-A", fx.location)
}

// The persisted goods BOUND the resume: it finishes the leg that was in flight, it does not
// improvise a general liquidation. A good the leg never meant to sell here is carried, and
// the ordinary path decides where it goes.
func TestTourLegResume_SellsOnlyThePersistedGoods(t *testing.T) {
	fx := legResumeFixture()
	fx.cargo = map[string]int{"G1": 40, "G2": 20}
	fx.ask["X1-R1-B"]["G2"] = 300
	fx.bid["X1-R1-B"]["G2"] = 250
	fx.tv["X1-R1-B"]["G2"] = 1000
	fx.tradeType["X1-R1-B"]["G2"] = "IMPORT"
	probe, planner := newFirstPlanProbe(fx)
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-BOUND", PlayerID: 1, ContainerID: "ctr-bound", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)

	require.Equal(t, 1, probe.tradesBeforeFirstPlan(), "exactly the persisted good sells before the plan")
	require.Equal(t, "SELL:G1", fx.timeline[0])
}

// A leg carrying TWO goods into a sink that has since stopped bidding for one of them
// discharges the one it still can and carries the other into the re-plan. The good with no
// bid must not be dispatched anywhere - a sink that does not exist is not a destination.
func TestTourLegResume_SkipsAPersistedGoodTheSinkNoLongerBids(t *testing.T) {
	fx := legResumeFixture()
	fx.cargo = map[string]int{"G1": 40, "G2": 20} // G2 has no bid at X1-R1-B at all
	fx.ask["X1-R1-B"]["G2"] = 300
	fx.tv["X1-R1-B"]["G2"] = 1000
	probe, planner := newFirstPlanProbe(fx)
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-HALFBID", PlayerID: 1, ContainerID: "ctr-halfbid", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1,G2",
	})
	require.NoError(t, err)

	require.Equal(t, 1, probe.tradesBeforeFirstPlan(), "only the good the sink still bids for is discharged")
	require.Equal(t, "SELL:G1", fx.timeline[0])
	require.NotContains(t, fx.navDests, "", "a good with no sink must never be dispatched to an empty waypoint")
}

// The resume NEVER buys. It is a discharge of cargo already paid for, so it cannot open
// exposure against a price nobody re-planned - which is why buy legs are deliberately not
// persisted at all.
func TestTourLegResume_NeverBuys(t *testing.T) {
	fx := legResumeFixture()
	probe, planner := newFirstPlanProbe(fx)
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-NOBUY", PlayerID: 1, ContainerID: "ctr-nobuy", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		// X1-R1-A sells G1 at an ask of 100 - a resume that improvised would buy here.
		TourLegWaypoint: "X1-R1-A", TourLegGoods: "G1",
	})
	require.NoError(t, err)

	require.Zero(t, probe.tradesBeforeFirstPlan())
	require.Zero(t, fx.buys, "the resume path has no buy seam at all")
}

// NO-STRAND, part 1: a resume that discharges the hold SETTLES the obligation the hull
// carried across the bounce, so the run completes clean instead of being vetoed for cargo
// it has in fact delivered.
func TestTourLegResume_FullDischargeSettlesThePreRestartObligation(t *testing.T) {
	fx := legResumeFixture()
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetPurchaseObligationReader(&fakeOutstandingPurchases{
		byHull: map[string]map[string]int{"TOUR-SETTLE": {"G1": 40}},
	})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-SETTLE", PlayerID: 1, ContainerID: "ctr-settle", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)
	r := tourResponse(t, resp)

	require.Equal(t, 1, r.ResumedLegs)
	require.Zero(t, fx.cargo["G1"], "the hull is released empty")
	require.False(t, r.CargoStranded, "the pre-restart obligation is discharged by the resumed sale")
	require.True(t, r.Completed)
}

// NO-STRAND, part 2: a resume that can only discharge PART of the hold (the sink's traded
// volume caps one sale) discharges EXACTLY what it sold and not a unit more. The residual
// stays owed and the stranded veto still reports it — the resume can neither settle an
// obligation it did not meet nor hide cargo it left aboard.
func TestTourLegResume_PartialDischargeIsAccountedNotForgotten(t *testing.T) {
	fx := legResumeFixture()
	fx.tv["X1-R1-B"]["G1"] = 10 // one sale absorbs 10 of the 40 aboard
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetPurchaseObligationReader(&fakeOutstandingPurchases{
		byHull: map[string]map[string]int{"TOUR-PARTIAL": {"G1": 40}},
	})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PARTIAL", PlayerID: 1, ContainerID: "ctr-partial", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)
	r := tourResponse(t, resp)

	require.Equal(t, 1, r.ResumedLegs)
	require.Positive(t, fx.cargo["G1"], "the shallow sink could not take the whole hold")
	require.True(t, r.CargoStranded, "what the resume could not discharge is still owed and still reported")
	require.False(t, r.Completed)
}

// NO-STRAND, part 2: when the resume DECLINES, every strand guarantee still binds. Nothing
// in this world will buy G1, so the hull is reported stranded exactly as it is today - the
// resume can neither swallow the veto nor invent an exit.
func TestTourLegResume_DeclineLeavesTheStrandedVetoIntact(t *testing.T) {
	fx := legResumeFixture()
	fx.bid = map[string]map[string]int{} // no buyer anywhere
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetPurchaseObligationReader(&fakeOutstandingPurchases{
		byHull: map[string]map[string]int{"TOUR-STRAND": {"G1": 40}},
	})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-STRAND", PlayerID: 1, ContainerID: "ctr-strand", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)
	r := tourResponse(t, resp)

	require.True(t, r.CargoStranded, "a declined resume must not mask an undischargeable hold")
	require.False(t, r.Completed)
}

// A hull that was re-adopted MID-JUMP has already left the ground its leg was flown on.
// The reposition resume wins and the leg is dropped, so the hull never flies back across a
// gate to a sink it is no longer near.
func TestTourLegResume_SkippedWhenARepositionResumed(t *testing.T) {
	fx := legResumeFixture()
	fx.markets["X1-R2"] = []string{"X1-R2-A"}
	fx.neighbors = map[string][]string{"X1-R1": {"X1-R2"}}
	probe, planner := newFirstPlanProbe(fx)
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetRepositionPersister(&fakeRepositionPersister{})

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-MIDJUMP", PlayerID: 1, ContainerID: "ctr-midjump", Iterations: -1,
		ModelArtifactPath:    writeTourArtifact(t),
		RepositionInProgress: true, RepositionTargetSystem: "X1-R2", RepositionTargetWaypoint: "X1-R2-A",
		TourLegWaypoint: "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)

	require.Zero(t, probe.tradesBeforeFirstPlan(), "the jump destination is the ground, not the abandoned leg")
	require.NotContains(t, fx.navDests, "X1-R1-B")
}

// A ONE-SHOT run is not a restart-recovered engine; only a CONTINUOUS (-1) container is
// re-adopted mid-leg, so only it resumes.
func TestTourLegResume_OneShotRunDoesNotResume(t *testing.T) {
	fx := legResumeFixture()
	probe, planner := newFirstPlanProbe(fx)
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-ONESHOT", PlayerID: 1, ContainerID: "ctr-oneshot",
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)

	require.Zero(t, probe.tradesBeforeFirstPlan(), "the plan comes first on a one-shot run")
	require.Zero(t, tourResponse(t, resp).ResumedLegs)
}

// The WRITE side: the leg in flight is persisted the instant it is chosen (before the hull
// moves), carrying only the sink and the goods it is going to discharge there. A leg that
// sells nothing the hull holds writes the CLEAR, so a later restart cannot resume a leg
// that has already been flown.
func TestTourLegPersist_WritesTheSellLegAndClearsOnABuyOnlyLeg(t *testing.T) {
	fx := arbFixture(1000)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	persister := &fakeTourLegPersister{}
	h.SetTourLegPersister(persister)

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PERSIST", PlayerID: 1, ContainerID: "ctr-persist",
		ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	// arbPlan is buy G1 at X1-S1-A then sell it at X1-S1-B: leg 0 holds nothing yet (clear),
	// leg 1 departs laden toward the sink (the resumable state).
	require.Equal(t, []TourLegState{
		{},
		{Waypoint: "X1-S1-B", Goods: "G1"},
	}, persister.recorded())
}

// RESERVATION INTEGRITY. The resume consumes sink depth this container ALREADY holds - the
// release path preserves sell-side rows under cargo in the hold - so it reserves nothing
// and releases nothing itself. What it must never do is stack a second row on the sink it
// is selling into, or leave the first one behind: after the run the container holds exactly
// its converted recovery shadow and no PLANNED row at all.
func TestTourLegResume_ReusesTheHeldSinkReservationWithoutStackingOrLeaking(t *testing.T) {
	fx := legResumeFixture()
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	al, db := setupTourLedger(t)
	h.SetAbsorptionLedger(al, 0)

	// The pre-restart plan's sink hold, exactly as releaseTourReservations preserves it for
	// cargo still aboard.
	ctx := context.Background()
	_, ok, err := al.Reserve(ctx, 1, "ctr-resv", absorptionEngineTour, []absorption.ReserveEntry{{
		Waypoint: "X1-R1-B", Good: "G1", Side: absorption.SideSell,
		Units: 40, CapUnits: 4000, TTL: 0,
	}})
	require.NoError(t, err)
	require.True(t, ok)

	_, err = h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RESV", PlayerID: 1, ContainerID: "ctr-resv", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)

	rows := tourLedgerRows(t, db, "ctr-resv")
	planned := 0
	shadows := 0
	for _, row := range rows {
		switch row.State {
		case "PLANNED":
			planned++
		case "EXECUTED":
			shadows++
			require.Equal(t, "X1-R1-B", row.Waypoint)
			require.Equal(t, absorption.SideSell, row.Side)
			require.Equal(t, 40, row.Units, "the shadow carries the units the resume actually sold")
		}
	}
	require.Zero(t, planned, "no PLANNED row may survive the run - a stacked or leaked hold is the corruption")
	require.Equal(t, 1, shadows, "the held sink reservation converts once, on the sale that consumed it")
}

// The resumed sale is a real trade and must be visible as one - but it carries NO solver
// plan basis (the basis is deliberately not persisted), so it declares its own engine
// rather than posing as a plan leg whose projection failed to save.
func TestTourLegResume_RecordsTelemetryWithNoInventedPlanBasis(t *testing.T) {
	fx := legResumeFixture()
	tel := &tourFakeTelemetry{}
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, tel)

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-TEL", PlayerID: 1, ContainerID: "ctr-tel", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
		TourLegWaypoint:   "X1-R1-B", TourLegGoods: "G1",
	})
	require.NoError(t, err)

	var resumeLegs []trading.TourLegTelemetry
	for _, l := range tel.rows {
		if l.Engine == trading.LegEngineResume {
			resumeLegs = append(resumeLegs, l)
		}
	}
	require.Len(t, resumeLegs, 1)
	require.Equal(t, "X1-R1-B", resumeLegs[0].Waypoint)
	require.Equal(t, "G1", resumeLegs[0].Good)
	require.False(t, resumeLegs[0].IsBuy)
	require.Zero(t, resumeLegs[0].PlannedUnitPrice, "a resumed leg never invents the basis it did not persist")
	require.Equal(t, trading.ResumeLegIndex, resumeLegs[0].LegIndex)
}

// fakeTourLegPersister records every in-flight-leg write so the write side can be pinned
// without a database.
type fakeTourLegPersister struct {
	mu     sync.Mutex
	states []TourLegState
}

func (p *fakeTourLegPersister) PersistTourLegState(_ context.Context, _ string, _ int, s TourLegState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.states = append(p.states, s)
	return nil
}

func (p *fakeTourLegPersister) recorded() []TourLegState {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]TourLegState, len(p.states))
	copy(out, p.states)
	return out
}

var _ TourLegStatePersister = (*fakeTourLegPersister)(nil)

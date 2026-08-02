package commands

// run_tour_coordinator_relocation_regions_test.go — the region-observer seam (sp-zvywu Part 2).
//
// EVERY test here exists for one reason: the projected rate is the single most load-bearing input to
// the relocation NPV, and the two ways this seam can be wrong are both silent.
//
//   - A FABRICATED rate sends a hull to ground that does not exist. So the readable case pins the
//     exact number, and every unreadable case pins that NOTHING is reported instead of a fallback.
//   - A rate netted of the deadhead would double-charge the crossing (the relocator's valuation
//     already charges TravelHours and CurrentRate×TravelHours separately), quietly refusing moves
//     that genuinely pay. The readable case is built so a deadhead-netted rate produces a DIFFERENT
//     number and fails.
//
// Assertions are on the RelocatorRegion values the port returns — the boundary the relocator actually
// consumes.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// FindAllByPlayer serves the fixture's declared hulls as the WHOLE fleet — the seam the region
// observer resolves its representative origin hull through (relocationOriginHull). Backed by the same
// activeHulls declaration FindActiveByPlayer reads, so a test names a hull once.
//
// It is deliberately the FULL fleet and not the assigned subset: a trade hull at honest tour release
// holds no claim, so an origin-hull lookup over the assigned-only set would find nothing exactly when
// the relocator needs it and report every ground unobservable.
func (r *tourFakeShipRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	r.fx.mu.Lock()
	hulls := append([]activeHull(nil), r.fx.activeHulls...)
	r.fx.mu.Unlock()
	out := make([]*navigation.Ship, 0, len(hulls))
	for i, ah := range hulls {
		out = append(out, r.fx.buildHullAt(r.t, fmt.Sprintf("FLEET-%d", i), ah.system, ah.fleet))
	}
	return out, nil
}

// errPreflightUnavailable is the "the pre-flight CALL failed" class — a gRPC/snapshot failure, which is
// categorically different from a solver that returned an infeasible verdict.
var errPreflightUnavailable = errors.New("planner unavailable")

// relocRegionFixture models the relocator's question: the hull stands in the dead ground X1-ORIG, and
// a richer ground X1-RICH sits TWO gate hops out behind the barren interior hop X1-H1. The live gate
// scan is barren (the uncharted-origin shape); the DURABLE gate graph exposes the route, exactly as
// production wires it.
//
// X1-RICH carries one in-system lane: buy G at X1-RICH-A (ask 100), sell into the X1-RICH-B IMPORT
// sink (bid 200), volume 10 — a capped spread of 1000, so the system pre-ranks as a real candidate
// rather than the score-0 fallback.
func relocRegionFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-ORIG-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-RICH": {"X1-RICH-A", "X1-RICH-B"},
		},
		ask: map[string]map[string]int{
			"X1-RICH-A": {"G": 100}, "X1-RICH-B": {"G": 200},
		},
		bid: map[string]map[string]int{
			"X1-RICH-B": {"G": 200},
		},
		tv: map[string]map[string]int{
			"X1-RICH-A": {"G": 10}, "X1-RICH-B": {"G": 10},
		},
		tradeType: map[string]map[string]string{
			"X1-RICH-B": {"G": "IMPORT"},
		},
		neighbors: map[string][]string{}, // live scan barren — the durable graph drives discovery
		// The hull the relocator is evaluating: trade-dedicated, standing in the origin system. It is
		// what the region observer prices a candidate tour FOR.
		activeHulls: []activeHull{{system: "X1-ORIG", fleet: tradeFleet}},
	}
}

// relocRegionGateGraph: X1-ORIG gates to the barren X1-H1 (1 hop), which gates on to X1-RICH (2 hops).
// So a radius-2 walk surfaces exactly {H1@1, RICH@2}; H1 carries no market and is rejected, leaving
// X1-RICH as the one candidate at TWO gate hops.
func relocRegionGateGraph() *fakeGateGraph {
	return &fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-ORIG": {{ConnectedSystem: "X1-H1", GateWaypoint: "X1-H1-GATE"}},
		"X1-H1":   {{ConnectedSystem: "X1-RICH", GateWaypoint: "X1-RICH-GATE"}},
	}}
}

// relocRegionHandler wires the observer with a real model artifact on disk (ObserveRegions binds the
// model version exactly as Handle does, so an absent artifact is an honest "unobservable" rather than
// a guessed version) and a planner whose verdict the test controls per candidate system.
func relocRegionHandler(t *testing.T, fx *tourFixture, planFn func(routing.TourShipState) *routing.TourPlan) *RunTourCoordinatorHandler {
	t.Helper()
	artifact := filepath.Join(t.TempDir(), "market_model.json")
	if err := os.WriteFile(artifact, []byte(`{"fit_version": 7, "era": "era-5"}`), 0o600); err != nil {
		t.Fatalf("write model artifact: %v", err)
	}
	h := newTourHandler(t, fx, &tourFakeRoutingClient{planFn: planFn}, &tourFakeTelemetry{})
	h.SetGateGraph(relocRegionGateGraph())
	h.SetModelArtifactPath(artifact)
	return h
}

// relocRegionPlanAt returns plan only when the pre-flight is priced AT system, and an infeasible
// verdict everywhere else — the shape planAtCandidate drives (a SYNTHETIC ship state positioned at the
// candidate).
func relocRegionPlanAt(system string, plan *routing.TourPlan) func(routing.TourShipState) *routing.TourPlan {
	return func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == system {
			return plan
		}
		return &routing.TourPlan{Feasible: false, InfeasibleReason: "no_profitable_tour"}
	}
}

func relocRegionObserve(t *testing.T, h *RunTourCoordinatorHandler) []RelocatorRegion {
	t.Helper()
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	regions, err := h.ObserveRegions(ctx, 1, "X1-ORIG", defaultRelocatorRegionHopRadius)
	if err != nil {
		t.Fatalf("ObserveRegions returned an error: %v", err)
	}
	return regions
}

func relocRegionOne(t *testing.T, regions []RelocatorRegion) RelocatorRegion {
	t.Helper()
	if len(regions) != 1 {
		t.Fatalf("observed %d regions, want exactly 1 (X1-RICH); got %+v", len(regions), regions)
	}
	return regions[0]
}

// ── the projected rate ───────────────────────────────────────────────────────────────────────────

// THE CENTRAL TEST. A feasible pre-flight yields a GENUINE, planner-derived rate — and the numbers are
// chosen so the two wrong answers are both distinguishable from the right one:
//
//	ProjectedProfit 600,000 at 300,000 cr/hr => the plan's own wall-clock is 2.0 h.
//	DepositValue 150,000 is SYNTHETIC savings value, not cash, so fresh profit is 450,000.
//	=> the honest rate is 450,000 / 2.0 h = 225,000 cr/hr.
//
// A rate that simply echoed the solver's cph would report 300,000 (booking pre-positioning value as
// cash earning). A rate netted of the deadhead — 450,000 / ((2·352 + 60 + 7200)/3600) ≈ 203,435 —
// would double-charge a crossing the relocator's NPV already charges twice over. Only the steady-state
// fresh-profit rate is 225,000.
func TestObserveRegionsShould_ReportThePlannerProjectedRateOverThePlansOwnWallClock(t *testing.T) {
	h := relocRegionHandler(t, relocRegionFixture(), relocRegionPlanAt("X1-RICH", &routing.TourPlan{
		Feasible: true, ProjectedProfit: 600_000, ProjectedCreditsPerHour: 300_000, DepositValue: 150_000,
	}))

	region := relocRegionOne(t, relocRegionObserve(t, h))

	if !region.RateReadable {
		t.Fatalf("a feasible pre-flight reported no readable rate; the relocator would exclude a ground the planner actually priced. region=%+v", region)
	}
	if region.ProjectedRate != 225_000 {
		t.Fatalf("projected rate %.0f, want 225000 (fresh profit 450000 over the plan's own 2.0 h). 300000 means the raw solver cph was echoed and synthetic deposit value booked as cash; ~203435 means the one-time deadhead was netted into the rate, which the NPV already charges separately", region.ProjectedRate)
	}
	if region.AnchorSystem != "X1-RICH" || region.LandingWaypoint != "X1-RICH-A" {
		t.Fatalf("region anchored at %s (%s), want X1-RICH (X1-RICH-A, the best lane's source waypoint the hull lands at)", region.AnchorSystem, region.LandingWaypoint)
	}
	if region.GateHops != 2 {
		t.Fatalf("gate hops %d, want 2 (X1-ORIG -> X1-H1 -> X1-RICH); the NPV prices the crossing off this, so a 1 would make a two-hop flight look half as costly", region.GateHops)
	}
}

// FAIL CLOSED, every way a projection can be missing. In each case the region must come back
// RateReadable:false — never a fallback, an estimate, or a substituted number.
func TestObserveRegionsShould_ReportNoRateRatherThanFabricateOneWhenThePreflightCannotPriceTheGround(t *testing.T) {
	for name, plan := range map[string]*routing.TourPlan{
		"the solver declined the ground": {Feasible: false, InfeasibleReason: "no_profitable_tour"},
		"the plan carries no time estimate (cph <= 0)": {
			Feasible: true, ProjectedProfit: 600_000, ProjectedCreditsPerHour: 0,
		},
		"the plan projects no profit at all": {
			Feasible: true, ProjectedProfit: 0, ProjectedCreditsPerHour: 300_000,
		},
		"the whole projected profit is synthetic deposit value, so no NEW CASH is earned": {
			Feasible: true, ProjectedProfit: 600_000, ProjectedCreditsPerHour: 300_000, DepositValue: 600_000,
		},
		"the whole projected profit is launch-liquidation of cargo already aboard": {
			Feasible: true, ProjectedProfit: 600_000, ProjectedCreditsPerHour: 300_000, HeldLiquidation: 600_000,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := relocRegionHandler(t, relocRegionFixture(), relocRegionPlanAt("X1-RICH", plan))

			region := relocRegionOne(t, relocRegionObserve(t, h))

			if region.RateReadable {
				t.Fatalf("%s, yet the region reported a readable rate of %.0f; a fabricated rate sends a hull to ground that does not exist", name, region.ProjectedRate)
			}
			if region.ProjectedRate != 0 {
				t.Fatalf("%s, yet the region carries a rate of %.0f alongside RateReadable=false; a non-zero value invites a caller to use it", name, region.ProjectedRate)
			}
		})
	}
}

// A planner ERROR (as distinct from an infeasible verdict) is also RateReadable:false, not an aborted
// observation: one dead candidate must not blind the relocator to the others.
func TestObserveRegionsShould_ReportNoRateWhenThePreflightCallItselfFails(t *testing.T) {
	h := relocRegionHandler(t, relocRegionFixture(), nil)
	// planFn nil + a planner error is the "the pre-flight CALL failed" class, categorically different
	// from a solver that returned an infeasible verdict.
	h.planner = &tourFakeRoutingClient{err: errPreflightUnavailable}

	region := relocRegionOne(t, relocRegionObserve(t, h))

	if region.RateReadable {
		t.Fatal("a failed planner call reported a readable rate; the pre-flight produced no projection at all")
	}
}

// ── the freshness facts the staleness exclusion is judged on ─────────────────────────────────────

// SnapshotAge and Activity must be OBSERVED, and observed CONSERVATIVELY on both axes, because the
// relocator's staleness exclusion is only as good as what this seam reports.
//
// The lane spans two quotes with different ages and different activity levels:
//
//	source X1-RICH-A: observed 40 min ago, activity WEAK   (an 8 h cap)
//	sink   X1-RICH-B: observed 10 min ago, activity STRONG (a 30 min cap)
//
// The age must be the OLDER of the two (40 min): a lane is only as trustworthy as its staler side, and
// taking the fresher one would let a stale sink hide behind a fresh source. The activity must be the
// TIGHTER-capped of the two (STRONG): a lane spanning both is held to the strict end, so this region
// is correctly excluded downstream rather than admitted on WEAK's 8-hour tolerance.
func TestObserveRegionsShould_ReportTheOlderQuotesAgeAndTheTighterCappedActivity(t *testing.T) {
	fx := relocRegionFixture()
	fx.ageByWaypoint = map[string]time.Duration{
		"X1-RICH-A": 40 * time.Minute,
		"X1-RICH-B": 10 * time.Minute,
	}
	fx.activityByWaypoint = map[string]string{
		"X1-RICH-A": "WEAK",
		"X1-RICH-B": "STRONG",
	}
	h := relocRegionHandler(t, fx, relocRegionPlanAt("X1-RICH", &routing.TourPlan{
		Feasible: true, ProjectedProfit: 600_000, ProjectedCreditsPerHour: 300_000,
	}))

	region := relocRegionOne(t, relocRegionObserve(t, h))

	if delta := region.SnapshotAge - 40*time.Minute; delta > time.Minute || delta < -time.Minute {
		t.Fatalf("snapshot age %s, want ~40m (the OLDER of the lane's two quotes); reporting the fresher 10m would let a stale sink hide behind a fresh source", region.SnapshotAge)
	}
	if region.Activity != "STRONG" {
		t.Fatalf("activity %q, want STRONG — the TIGHTER-capped of the lane's two quotes (WEAK source, STRONG sink). Reporting WEAK would admit this region on an 8-hour tolerance when half its pricing is only rankable for 30 minutes", region.Activity)
	}
}

// A region whose markets cannot be read reports NO rate. Reporting an age of zero instead would slip a
// region of unknown vintage past the relocator's staleness exclusion — the same failure as a fabricated
// rate wearing a different hat.
//
// The freshness gate is reached only AFTER a system has already become a candidate, so this drives
// observeOneRegion directly with an anchor whose markets are unreadable. Going through ObserveRegions
// with an all-stale system would prove nothing: scoreRepositionNeighbors rejects such a system as
// "stale-data" before it is ever a candidate, so the region set comes back empty and the gate under
// test is never consulted. In production the gap is a transient market-read blip between the pre-rank
// and the freshness re-read.
//
// The planner is told the ground is FEASIBLE and richly profitable, so the freshness gate is the ONLY
// thing that can withhold the rate.
func TestObserveRegionsShould_ReportNoRateWhenTheRegionsFreshnessCannotBeEstablished(t *testing.T) {
	h := relocRegionHandler(t, relocRegionFixture(), func(routing.TourShipState) *routing.TourPlan {
		return &routing.TourPlan{Feasible: true, ProjectedProfit: 600_000, ProjectedCreditsPerHour: 300_000}
	})
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := &RunTourCoordinatorCommand{PlayerID: 1}
	budget, err := h.relocationPreflightBudget(ctx, cmd)
	if err != nil {
		t.Fatalf("resolving the pre-flight budget: %v", err)
	}
	ship, err := h.relocationOriginHull(ctx, 1, "X1-ORIG")
	if err != nil {
		t.Fatalf("resolving the origin hull: %v", err)
	}
	now := h.clock.Now()

	// X1-NOWHERE holds no cached market at all, so its vintage cannot be established.
	if _, _, known := h.relocationRegionFreshness(ctx, cmd, "X1-NOWHERE", now); known {
		t.Fatal("a system with no cached market reported establishable freshness; every downstream staleness check would then run against a fabricated age")
	}

	region := h.observeOneRegion(ctx, cmd, ship,
		repositionCandidate{system: "X1-NOWHERE", waypoint: "X1-NOWHERE-A", score: 1000, hops: 2},
		budget, now)

	if region.RateReadable {
		t.Fatalf("a region of unestablishable vintage reported a readable rate of %.0f; the relocator's per-activity staleness exclusion would judge it at an age of zero", region.ProjectedRate)
	}
	if region.SnapshotAge != 0 || region.Activity != "" {
		t.Fatalf("region carries SnapshotAge %s / Activity %q alongside an unreadable rate; withheld freshness facts must stay empty rather than look like a fresh reading", region.SnapshotAge, region.Activity)
	}
}

// ── the observation's own preconditions ──────────────────────────────────────────────────────────

// An EMPTY region set is an honest verdict, not an error: no gate-reachable system inside the radius
// carries a fresh cached market. The relocator distinguishes the two — an error counts
// regions_unreadable — so this must not be reported as a failure.
func TestObserveRegionsShould_ReturnAnEmptySetRatherThanAnErrorWhenNoGroundIsReachable(t *testing.T) {
	fx := relocRegionFixture()
	fx.markets = map[string][]string{} // nothing anywhere carries cached market data
	h := relocRegionHandler(t, fx, relocRegionPlanAt("X1-RICH", &routing.TourPlan{Feasible: true, ProjectedProfit: 1, ProjectedCreditsPerHour: 1}))

	regions, err := h.ObserveRegions(common.WithLogger(context.Background(), &tradeCaptureLogger{}), 1, "X1-ORIG", defaultRelocatorRegionHopRadius)

	if err != nil {
		t.Fatalf("an unreachable neighbourhood returned an error (%v); no fresh ground is an honest verdict the relocator must be able to tell apart from an unreadable one", err)
	}
	if len(regions) != 0 {
		t.Fatalf("observed %d regions with no cached market anywhere; got %+v", len(regions), regions)
	}
}

// THE RADIUS MUST BIND. The relocator's region radius is the bead's "system + 2-hop neighbours", and it
// must bound the walk rather than the reposition flight's 12-jump bound — otherwise the relocator would
// consider ground far outside the neighbourhood it was configured for.
func TestObserveRegionsShould_HonourTheRequestedHopRadiusRatherThanTheRepositionJumpBound(t *testing.T) {
	h := relocRegionHandler(t, relocRegionFixture(), relocRegionPlanAt("X1-RICH", &routing.TourPlan{
		Feasible: true, ProjectedProfit: 600_000, ProjectedCreditsPerHour: 300_000,
	}))
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})

	// X1-RICH sits TWO hops out, so a radius of 1 must not reach it.
	narrow, err := h.ObserveRegions(ctx, 1, "X1-ORIG", 1)
	if err != nil {
		t.Fatalf("ObserveRegions at radius 1 errored: %v", err)
	}
	if len(narrow) != 0 {
		t.Fatalf("radius 1 surfaced %+v; X1-RICH is two gate hops out, so the radius is not bounding the walk (the 12-jump reposition bound would reach it)", narrow)
	}

	// At radius 2 the same ground appears — proving the bound is a real radius, not a blanket refusal.
	wide, err := h.ObserveRegions(ctx, 1, "X1-ORIG", 2)
	if err != nil {
		t.Fatalf("ObserveRegions at radius 2 errored: %v", err)
	}
	if len(wide) != 1 || wide[0].AnchorSystem != "X1-RICH" {
		t.Fatalf("radius 2 surfaced %+v, want just X1-RICH; a radius that never reaches anything is a blanket refusal, not a bound", wide)
	}
}

// FAIL CLOSED when the model artifact is unreadable: the version binds what the planner is asked, and
// Handle refuses to run a tour without it. Guessing a version here would price every ground against an
// unknown model.
func TestObserveRegionsShould_ErrorRatherThanGuessAModelVersionWhenTheArtifactIsUnreadable(t *testing.T) {
	h := relocRegionHandler(t, relocRegionFixture(), relocRegionPlanAt("X1-RICH", &routing.TourPlan{Feasible: true, ProjectedProfit: 1, ProjectedCreditsPerHour: 1}))
	h.SetModelArtifactPath(filepath.Join(t.TempDir(), "absent_model.json"))

	if _, err := h.ObserveRegions(common.WithLogger(context.Background(), &tradeCaptureLogger{}), 1, "X1-ORIG", defaultRelocatorRegionHopRadius); err == nil {
		t.Fatal("an unreadable model artifact observed regions anyway; every ground would be priced against a guessed model version")
	}
}

// FAIL CLOSED when no trade hull stands in the origin system: there is no tour to price, and inventing a
// synthetic hull shape would put a fabricated rate in front of the relocator through the back door.
func TestObserveRegionsShould_ErrorRatherThanInventAHullWhenNoneStandsInTheOriginSystem(t *testing.T) {
	fx := relocRegionFixture()
	fx.activeHulls = nil // the fleet holds no trade hull at X1-ORIG
	h := relocRegionHandler(t, fx, relocRegionPlanAt("X1-RICH", &routing.TourPlan{Feasible: true, ProjectedProfit: 1, ProjectedCreditsPerHour: 1}))

	if _, err := h.ObserveRegions(common.WithLogger(context.Background(), &tradeCaptureLogger{}), 1, "X1-ORIG", defaultRelocatorRegionHopRadius); err == nil {
		t.Fatal("regions were observed with no hull to price a tour for; the rate would describe a hull that does not exist")
	}
}

package commands

// run_tour_coordinator_rate_floor_test.go — white-box tests of the rate-floor
// early-reposition trigger through the driving Handle port. Every test drives a continuous run whose
// FIRST tour is PRODUCTIVE (so the trigger — which fires only after a productive tour — is reached),
// seeds telemetry so the hull's realized rate and the fleet median are both controlled, and asserts
// the OBSERVABLE outcome (relocated to the fresh ground / stayed) plus the greppable decision log.
//
// The anti-thrash STAY tests (improvement gate, dwell, anti-herd, fail-closed) pin RepositionMinMargin
// absurdly high so the LEGACY margins-death reposition can never jump — isolating the rate-floor
// decision as the ONLY relocation path, so a Repositions==0 outcome is unambiguously "rate-floor
// stayed" and not "margins-death also declined". A pre-flight call is identified by ship.Cargo == nil
// (planAtCandidate clears the hold), separating the reposition evaluation from the loop's re-plans.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// isolateLegacyReposition is a RepositionMinMargin so high no candidate's fresh profit can clear it,
// so the legacy margins-death reposition NEVER jumps — the rate-floor decision is the only relocation
// path under test. (The rate-floor improvement gate is independent of RepositionMinMargin; it uses its
// own reposition_rate_floor_improvement_pct knob.)
const isolateLegacyReposition = 1_000_000_000

// rfTleg builds one telemetry leg for a given hull/tour (100 units @ price, realized over the span).
func rfTleg(ship, tourID string, isBuy bool, price int, planned, realized time.Time) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{
		TourID: tourID, ShipSymbol: ship, IsBuy: isBuy, RealizedUnits: 100, RealizedUnitPrice: price,
		PlannedAt: planned, RealizedAt: realized, PlayerID: 1,
	}
}

// rfTour builds ONE 1h-span tour whose realized net $/hr = ratePerHour (buy 100@1000, sell 100@sell
// over a 1h span ⇒ net = 100·sell − 100·1000 = ratePerHour ⇒ sell = (ratePerHour+100000)/100). The
// rate may be negative (a losing tour), which MedianTourRate reports as a computable negative rate.
func rfTour(ship, tourID string, ratePerHour int) []trading.TourLegTelemetry {
	base := time.Now().Add(-30 * time.Minute)
	sell := (ratePerHour + 100000) / 100
	return []trading.TourLegTelemetry{
		rfTleg(ship, tourID, true, 1000, base, base),
		rfTleg(ship, tourID, false, sell, base, base.Add(time.Hour)),
	}
}

// rfSeed builds telemetry so BOTH the fleet median (MedianTourRate over ALL tours) and the named
// hull's realized rate (MedianTourRate over the hull's OWN tours) are controlled: the hull gets one
// tour at hullRate; each otherRate is one tour on a distinct fleet hull.
func rfSeed(hullSymbol string, hullRate int, otherRates ...int) []trading.TourLegTelemetry {
	rows := rfTour(hullSymbol, "hull-tour", hullRate)
	for i, r := range otherRates {
		rows = append(rows, rfTour(fmt.Sprintf("FLEET-%d", i), fmt.Sprintf("fleet-tour-%d", i), r)...)
	}
	return rows
}

// rateFloorPlanner builds the shared planFn: S1 flies ONE productive tour then dies; S2 answers the
// rate-floor pre-flight (ship.Cargo == nil) with the given candidate plan, and — once jumped to —
// flies one productive tour then dies (so a relocated run reaches an honest starvation exit).
func rateFloorPlanner(candidate *routing.TourPlan) *tourFakeRoutingClient {
	homeCalls, s2Calls := 0, 0
	return &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1() // one productive home tour → the trigger evaluates after it
			}
			return infeasibleTour() // margins die here if the hull never relocates
		case "X1-S2":
			if ship.Cargo == nil {
				return candidate // rate-floor pre-flight of the best candidate
			}
			s2Calls++
			if s2Calls == 1 {
				return roundTripS2() // post-jump productive tour
			}
			return infeasibleTour() // then S2 dies too → starvation
		}
		return infeasibleTour()
	}}
}

// THE governance gate + the fires proof. Same fixture, same fleet economics (hull 100k/hr, fleet
// median 400k/hr ⇒ hull at 25% < the 40% floor), same richly-better reachable candidate S2. With the
// flag OFF the productive-tour path is byte-identical to today: the hull keeps touring its mediocre
// home, margins die there, and the legacy reposition is pinned out — so it STAYS. With the flag ON the
// rate-floor trigger relocates it OFF the productive-but-mediocre tour to the fresh ground. The OFF
// case is the falsifiable proof the whole trigger is inert until armed.
func TestTourRateFloor_GovernanceGate_RelocatesOnlyWhenArmed(t *testing.T) {
	cases := []struct {
		name            string
		enabled         bool
		wantRepositions int
		wantTours       int
		wantEndSystem   string
	}{
		{"flag OFF: chronic under-earner keeps its mediocre ground (byte-identical to today)", false, 0, 1, "X1-S1"},
		{"flag ON: chronic under-earner relocates to the fresh ground", true, 1, 2, "X1-S2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := repositionFixture()
			h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), &seededTelemetry{rows: rfSeed("TOUR-RF", 100000, 400000, 400000, 400000, 400000)})
			logger := &tradeCaptureLogger{}
			ctx := common.WithLogger(context.Background(), logger)

			resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
				ShipSymbol: "TOUR-RF", PlayerID: 1, ContainerID: "ctr-rf", Iterations: -1,
				RepositionRateFloorEnabled: tc.enabled,
				RepositionMinMargin:        isolateLegacyReposition,
				ModelArtifactPath:          writeTourArtifact(t),
			})
			if err != nil {
				t.Fatalf("rate-floor run returned error: %v", err)
			}
			r := tourResponse(t, resp)

			if r.Repositions != tc.wantRepositions {
				t.Fatalf("Repositions = %d, want %d (%+v)", r.Repositions, tc.wantRepositions, r)
			}
			if r.ToursCompleted != tc.wantTours {
				t.Fatalf("ToursCompleted = %d, want %d", r.ToursCompleted, tc.wantTours)
			}
			if got := shared.ExtractSystemSymbol(fx.location); got != tc.wantEndSystem {
				t.Fatalf("hull ended at %q, want %q", got, tc.wantEndSystem)
			}
			relocated := logger.loggedContaining("Reposition rate-floor", "relocating")
			if relocated != tc.enabled {
				t.Fatalf("rate-floor relocation log present=%v, want %v:\n%s", relocated, tc.enabled, strings.Join(logger.messages, "\n"))
			}
			if !tc.enabled && (len(fx.jumps) != 0) {
				t.Fatalf("flag OFF must not jump, got jumps %v", fx.jumps)
			}
			if tc.enabled && (len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2") {
				t.Fatalf("flag ON must relocate to X1-S2, got jumps %v", fx.jumps)
			}
		})
	}
}

// THE improvement gate (the KEY anti-thrash test). A below-floor hull (100k/hr) whose best reachable
// candidate projects only ~1.5x its rate (152.5k/hr net of deadhead, under the 2x default) is NOT a
// meaningful-enough win, so the hull STAYS. This is the mutation pin: revert the improvement gate
// (accept any strictly-better candidate) and the hull WRONGLY relocates — 152.5k > 100k clears a
// bare strictly-better check but not the 2x bar.
func TestTourRateFloor_ImprovementGate_StaysWhenCandidateBelowBar(t *testing.T) {
	fx := repositionFixture()
	// feasiblePlan(170000,170000): a clean-hold pre-flight priced by repositionCandidateRate to
	// 170000·3600/(352+60+3600) = 152542/hr — strictly better than the hull's 100k but < the 2x
	// (200k) improvement bar. Below-2x → STAY.
	h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(170000, 170000)), &seededTelemetry{rows: rfSeed("TOUR-IMP", 100000, 400000, 400000, 400000, 400000)})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-IMP", PlayerID: 1, ContainerID: "ctr-imp", Iterations: -1,
		RepositionRateFloorEnabled: true,
		RepositionMinMargin:        isolateLegacyReposition,
		ModelArtifactPath:          writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("improvement-gate run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("a below-2x candidate must NOT relocate (thrash guard), got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if got := shared.ExtractSystemSymbol(fx.location); got != "X1-S1" {
		t.Fatalf("the hull must hold its ground X1-S1, got %q", got)
	}
	if !logger.loggedContaining("Reposition rate-floor", "improvement", "staying") {
		t.Fatalf("the decision log must name the below-improvement stay:\n%s", strings.Join(logger.messages, "\n"))
	}
}

// THE dwell gate (the KEY anti-thrash test). A below-floor hull with a richly-better reachable
// candidate that WOULD relocate is instead held because it relocated within the dwell window (seeded
// via noteRateFloorRelocation just before the run — the trFakeClock advances only real microseconds,
// so it is well inside the 15-min window). Mutation pin: revert the dwell check and the hull WRONGLY
// relocates (the candidate clears every other gate).
func TestTourRateFloor_Dwell_StaysWhenRecentlyRelocated(t *testing.T) {
	fx := repositionFixture()
	h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), &seededTelemetry{rows: rfSeed("TOUR-DWELL", 100000, 400000, 400000, 400000, 400000)})
	h.noteRateFloorRelocation("TOUR-DWELL") // this hull relocated "just now" → inside the dwell window
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DWELL", PlayerID: 1, ContainerID: "ctr-dwell", Iterations: -1,
		RepositionRateFloorEnabled: true,
		RepositionMinMargin:        isolateLegacyReposition,
		ModelArtifactPath:          writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("dwell run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("a hull inside its dwell window must NOT relocate (thrash guard), got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if got := shared.ExtractSystemSymbol(fx.location); got != "X1-S1" {
		t.Fatalf("the dwell-locked hull must hold X1-S1, got %q", got)
	}
	if !logger.loggedContaining("Reposition rate-floor", "dwell window", "staying") {
		t.Fatalf("the decision log must name the dwell-window stay:\n%s", strings.Join(logger.messages, "\n"))
	}
}

// FAIL-CLOSED on a bad median. A rate-floor relocation is NEVER decided off an unreadable or
// non-positive fleet median (mirrors the senseBeta contract): a nil telemetry repo, empty
// telemetry (no computable tour), and an all-losing fleet (median <= 0) each leave the hull touring
// its ground even though a richly-better candidate is one hop away and the flag is armed.
func TestTourRateFloor_FailClosed_UnreadableOrNonPositiveMedian(t *testing.T) {
	cases := []struct {
		name string
		tel  trading.TourTelemetryRepository
	}{
		{"nil telemetry repo", nil},
		{"empty telemetry (no computable tour)", &seededTelemetry{}},
		{"non-positive fleet median (all tours losing)", &seededTelemetry{rows: rfSeed("TOUR-FC", -50000, -40000, -60000)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := repositionFixture()
			h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), tc.tel)
			logger := &tradeCaptureLogger{}
			ctx := common.WithLogger(context.Background(), logger)

			resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
				ShipSymbol: "TOUR-FC", PlayerID: 1, ContainerID: "ctr-fc", Iterations: -1,
				RepositionRateFloorEnabled: true,
				RepositionMinMargin:        isolateLegacyReposition,
				ModelArtifactPath:          writeTourArtifact(t),
			})
			if err != nil {
				t.Fatalf("fail-closed run returned error: %v", err)
			}
			r := tourResponse(t, resp)

			if r.Repositions != 0 || len(fx.jumps) != 0 {
				t.Fatalf("a bad median must never relocate (fail-closed), got %d repositions / jumps %v", r.Repositions, fx.jumps)
			}
			if got := shared.ExtractSystemSymbol(fx.location); got != "X1-S1" {
				t.Fatalf("the hull must hold X1-S1 on a bad median, got %q", got)
			}
			if logger.loggedContaining("Reposition rate-floor", "relocating") {
				t.Fatalf("a bad median must produce NO rate-floor relocation:\n%s", strings.Join(logger.messages, "\n"))
			}
		})
	}
}

// ANTI-HERD. The rate-floor path applies excludeHerdedSystems (gate b) even with reposition_reach
// OFF: the only reachable candidate X1-S2 is already at its per-system hull cap, so it is excluded,
// leaving no candidate — the below-floor hull STAYS rather than piling onto a saturated ground.
func TestTourRateFloor_AntiHerd_ExcludesSaturatedCandidate(t *testing.T) {
	fx := repositionFixture()
	fx.activeHulls = []activeHull{{"X1-S2", tradeFleet}} // one trade hull already at S2; cap set to 1 below
	h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), &seededTelemetry{rows: rfSeed("TOUR-HERD", 100000, 400000, 400000, 400000, 400000)})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-HERD", PlayerID: 1, ContainerID: "ctr-herd", Iterations: -1,
		RepositionRateFloorEnabled:       true,
		RepositionReachMaxHullsPerSystem: 1, // S2's single hull is AT the cap → excluded
		RepositionMinMargin:              isolateLegacyReposition,
		ModelArtifactPath:                writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("anti-herd run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("a herd-saturated candidate must be excluded (no relocation), got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if got := shared.ExtractSystemSymbol(fx.location); got != "X1-S1" {
		t.Fatalf("the hull must hold X1-S1 when the only candidate is herd-excluded, got %q", got)
	}
	if !logger.loggedContaining("Reposition rate-floor", "no reachable non-herded candidate") {
		t.Fatalf("the decision log must name the herd-excluded stay:\n%s", strings.Join(logger.messages, "\n"))
	}
}

// THE ATOMIC anti-herd cap (the pre-arm BLOCKER fix). The LANDED hull count lags a full multi-hop
// flight — RepositionToWaypointWithinJumps blocks until arrival — so a restart re-adopting an
// under-earner cohort near-simultaneously would read the richest frontier system as under-cap while
// the early movers are still mid-jump, all pile in, overshoot, dilute, and migrate as a bunch. The
// fix counts IN-FLIGHT movers (pendingRelocationsBySystem) against the cap. Here mover A is GENUINELY
// in flight — a goroutine that took its pending claim on X1-S2 at its commit-decision and holds it
// (blocked, as the jump blocks until arrival) — while evaluator B runs the herd check concurrently:
// landed(1)+in-flight(1)=2 reaches the cap, so B EXCLUDES X1-S2 and the cohort cannot overshoot.
// Mutation pin: remove the pending add in excludeHerdedSystems and B sees only landed(1) < cap(2) →
// X1-S2 wrongly kept (the overshoot).
func TestTourRateFloor_AntiHerd_AtomicPending_ConcurrentEvaluatorRespectsCap(t *testing.T) {
	fx := repositionFixture()
	fx.activeHulls = []activeHull{{"X1-S2", tradeFleet}} // one LANDED trade hull at X1-S2
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "EVAL-B", PlayerID: 1, RepositionReachMaxHullsPerSystem: 2}

	inFlight := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.incrementPendingRelocation("X1-S2") // mover A commits — claims a pending slot at X1-S2
		defer h.decrementPendingRelocation("X1-S2")
		close(inFlight)
		<-release // stay "mid-jump" (holding the claim) until B has run its herd check
	}()
	<-inFlight // A is now in flight (pending=1); its LANDED location has not updated

	kept, excluded := h.excludeHerdedSystems(ctx, cmd, []repositionCandidate{{system: "X1-S2", waypoint: "X1-S2-A", score: 100, hops: 1}})
	close(release)
	<-done

	if reachHasSystem(kept, "X1-S2") || excluded != 1 {
		t.Fatalf("landed(1)+in-flight(1) must reach cap 2 → X1-S2 excluded; got kept=%v excluded=%d", reachCandidateSystems(kept), excluded)
	}
}

// Fix 2 — the anti-thrash guarantee cannot be silently tuned away. Dwell (< a typical 30-60min tour)
// rarely bites across consecutive tours, so the improvement ratchet is the REAL per-relocation
// limiter and is SAFETY-CRITICAL: a configured improvement_pct below the hard floor 150 is clamped
// UP to 150 (an operator can raise the ratchet, never weaken it below 1.5x). The dwell default is
// raised to 45min so it bites across tours; the pct/absent sentinels resolve to their defaults.
func TestTourRateFloor_ResolversClampRatchetAndDefaults(t *testing.T) {
	improvementCases := []struct{ configured, want int }{
		{0, 200},   // absent → default 2x
		{100, 150}, // below the hard floor → clamped to 1.5x (cannot be tuned away)
		{149, 150}, // just below → clamped
		{150, 150}, // at the floor → kept
		{250, 250}, // above → operator override kept
	}
	for _, c := range improvementCases {
		if got := resolveRateFloorImprovementPct(c.configured); got != c.want {
			t.Fatalf("resolveRateFloorImprovementPct(%d) = %d, want %d (the ratchet must not be tunable below 150)", c.configured, got, c.want)
		}
	}
	if got := resolveRateFloorDwellMinutes(0); got != 45 {
		t.Fatalf("resolveRateFloorDwellMinutes(0) = %d, want 45 (must bite across consecutive tours)", got)
	}
	if got := resolveRateFloorPct(0); got != 40 {
		t.Fatalf("resolveRateFloorPct(0) = %d, want 40", got)
	}
}

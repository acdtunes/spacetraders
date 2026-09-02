package replay

import (
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

func rl(ship, wp, good string, isBuy bool, units, price int, at time.Time) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{ShipSymbol: ship, Waypoint: wp, Good: good, IsBuy: isBuy,
		RealizedUnits: units, RealizedUnitPrice: price, RealizedAt: at, PlayerID: 1}
}

func cfg() Config {
	return Config{Window: 24 * time.Hour, Horizon: time.Hour, BoundaryGap: 10 * time.Minute,
		YieldWindowSells: 8, YieldMinSells: 1, ClaimReachHops: 2, TollSecondsPerHop: 1}
}

func TestRun_HullThatJumpedToAPoorerSystemWouldHaveStayed(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	legs := []trading.TourLegTelemetry{
		// H1 in X1-A: two sells at 100/unit, then it jumped to X1-B (actual), where it earned 10/unit.
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		rl("H1", "X1-A-2", "IRON", false, 10, 200, t0.Add(5*time.Minute)),
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0.Add(10*time.Minute)),
		rl("H1", "X1-A-2", "IRON", false, 10, 200, t0.Add(15*time.Minute)),
		rl("H1", "X1-B-1", "IRON", true, 10, 100, t0.Add(30*time.Minute)),
		rl("H1", "X1-B-2", "IRON", false, 10, 110, t0.Add(35*time.Minute)),
		// H2 keeps X1-A rich during the next hour (the loop's evidence that staying pays).
		rl("H2", "X1-A-1", "IRON", true, 10, 100, t0.Add(20*time.Minute)),
		rl("H2", "X1-A-2", "IRON", false, 10, 200, t0.Add(25*time.Minute)),
		rl("H2", "X1-A-1", "IRON", true, 10, 100, t0.Add(50*time.Minute)),
		rl("H2", "X1-A-2", "IRON", false, 10, 200, t0.Add(55*time.Minute)),
		rl("H2", "X1-A-1", "IRON", true, 10, 100, t0.Add(3*time.Hour)),
		rl("H2", "X1-A-2", "IRON", false, 10, 200, t0.Add(3*time.Hour+5*time.Minute)),
	}
	r := Run(legs, map[string][]string{"X1-A": {"X1-B"}, "X1-B": {"X1-A"}}, cfg())
	if r.Hulls != 2 || r.Boundaries == 0 {
		t.Fatalf("hulls=%d boundaries=%d", r.Hulls, r.Boundaries)
	}
	var first *Decision
	for i := range r.Decisions {
		if r.Decisions[i].Hull == "H1" && r.Decisions[i].From == "X1-A" {
			first = &r.Decisions[i]
			break
		}
	}
	if first == nil || first.ActualNext != "X1-B" || first.LoopNext != "X1-A" {
		t.Fatalf("H1's X1-A boundary = %+v", first)
	}
	if r.LoopJumps >= r.ActualJumps {
		t.Fatalf("loop jumps %d must be below actual %d", r.LoopJumps, r.ActualJumps)
	}
	if pass, why := r.Gate(); !pass {
		t.Fatalf("gate should pass: %s", why)
	}
}

func TestRun_EmptyInputAndUnreachableSystems(t *testing.T) {
	r := Run(nil, nil, cfg())
	if r.Hulls != 0 || r.Boundaries != 0 {
		t.Fatalf("empty = %+v", r)
	}
	if pass, _ := r.Gate(); pass {
		t.Fatal("no data can never pass the gate")
	}
	t0 := time.Unix(1_700_000_000, 0)
	legs := []trading.TourLegTelemetry{
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		rl("H1", "X1-A-2", "IRON", false, 10, 101, t0.Add(time.Minute)),
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0.Add(3*time.Hour)),
		rl("H9", "X1-Z-1", "IRON", true, 10, 1, t0),
		rl("H9", "X1-Z-2", "IRON", false, 10, 5000, t0.Add(time.Minute)),
	}
	r = Run(legs, map[string][]string{}, cfg()) // X1-Z unreachable from X1-A
	for _, d := range r.Decisions {
		if d.Hull == "H1" && d.LoopNext == "X1-Z" {
			t.Fatal("unreachable system must never be chosen")
		}
	}
}

func TestRun_LeaveVerdictWhoseTopRankIsHereIsRecordedAsAStay(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	legs := []trading.TourLegTelemetry{
		// H1 earns 1/unit in X1-A, so its EWMA sits far below X1-B's score.
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		rl("H1", "X1-A-2", "IRON", false, 10, 101, t0.Add(5*time.Minute)),
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0.Add(6*time.Minute)),
		rl("H1", "X1-A-2", "IRON", false, 10, 101, t0.Add(10*time.Minute)),
		// ... but X1-A is the richest system on the board, so Rank puts it first.
		rl("H2", "X1-A-1", "IRON", true, 10, 1, t0.Add(time.Minute)),
		rl("H2", "X1-A-2", "IRON", false, 10, 2000, t0.Add(2*time.Minute)),
		rl("H3", "X1-B-1", "IRON", true, 10, 100, t0.Add(time.Minute)),
		rl("H3", "X1-B-2", "IRON", false, 10, 110, t0.Add(2*time.Minute)),
		// H1 actually jumped to X1-B; that later visit is its last, so it is no boundary.
		rl("H1", "X1-B-1", "IRON", true, 10, 100, t0.Add(40*time.Minute)),
		rl("H1", "X1-B-2", "IRON", false, 10, 110, t0.Add(45*time.Minute)),
	}
	r := Run(legs, map[string][]string{"X1-A": {"X1-B"}, "X1-B": {"X1-A"}}, cfg())
	if r.Boundaries != 1 || len(r.Decisions) != 1 {
		t.Fatalf("boundaries=%d decisions=%+v", r.Boundaries, r.Decisions)
	}
	d := r.Decisions[0]
	if d.Hull != "H1" || d.From != "X1-A" || d.ActualNext != "X1-B" {
		t.Fatalf("boundary = %+v", d)
	}
	if d.YieldHere >= d.BestAlternative {
		t.Fatalf("scenario must produce a Leave verdict: here=%.3f alt=%.3f", d.YieldHere, d.BestAlternative)
	}
	if d.LoopNext != "X1-A" || d.Reason != mvt.ReasonStay {
		t.Fatalf("top-ranked here must be a stay: next=%s reason=%s", d.LoopNext, d.Reason)
	}
	if r.LoopJumps != 0 || r.ActualJumps != 1 {
		t.Fatalf("loop=%d actual=%d", r.LoopJumps, r.ActualJumps)
	}
}

// H1 earned 100/unit in X1-A, bought once more and jumped to sell that load in X1-B at
// 10/unit; nobody sold in X1-A afterwards. The loop's stay is unobservable in the forward
// hour, so it is valued on the trailing rate the ranker scored X1-A on and flagged — not
// silently credited zero.
func TestRun_UnobservableStayIsValuedOnTheTrailingRateAndFlagged(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	legs := []trading.TourLegTelemetry{
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		rl("H1", "X1-A-2", "IRON", false, 10, 200, t0.Add(5*time.Minute)),
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0.Add(8*time.Minute)),
		rl("H1", "X1-A-2", "IRON", false, 10, 200, t0.Add(12*time.Minute)),
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0.Add(15*time.Minute)),
		rl("H1", "X1-B-2", "IRON", false, 10, 110, t0.Add(30*time.Minute)),
	}
	r := Run(legs, map[string][]string{"X1-A": {"X1-B"}, "X1-B": {"X1-A"}}, cfg())
	if r.Boundaries != 1 || len(r.Decisions) != 1 {
		t.Fatalf("boundaries=%d decisions=%+v", r.Boundaries, r.Decisions)
	}
	d := r.Decisions[0]
	if d.From != "X1-A" || d.ActualNext != "X1-B" || d.LoopNext != "X1-A" {
		t.Fatalf("decision = %+v", d)
	}
	if !d.Stranded || len(r.Stranded) != 1 {
		t.Fatalf("an unobservable stay must be flagged: %+v stranded=%d", d, len(r.Stranded))
	}
	if d.ActualCredit != 100 || d.LoopCredit != 1000 {
		t.Fatalf("actual=%.0f loop=%.0f, want 100 and 10 units × the trailing 100/unit", d.ActualCredit, d.LoopCredit)
	}
	if r.ActualMarginPerHull != 100 || r.LoopMarginPerHull != 1000 {
		t.Fatalf("margin/hull actual=%.0f loop=%.0f", r.ActualMarginPerHull, r.LoopMarginPerHull)
	}
}

// The mirror case: the loop leaves for X1-B, where H3 sold at 100/unit before the boundary
// and nobody sold after it. The jump is valued on X1-B's trailing rate, exactly as a stay is.
func TestRun_UnobservableJumpIsValuedOnTheTrailingRate(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	legs := []trading.TourLegTelemetry{
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		rl("H1", "X1-A-2", "IRON", false, 10, 101, t0.Add(5*time.Minute)),
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0.Add(6*time.Minute)),
		rl("H1", "X1-A-2", "IRON", false, 10, 101, t0.Add(10*time.Minute)),
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0.Add(12*time.Minute)),
		rl("H3", "X1-B-1", "IRON", true, 10, 100, t0.Add(time.Minute)),
		rl("H3", "X1-B-2", "IRON", false, 10, 200, t0.Add(2*time.Minute)),
		rl("H1", "X1-Z-2", "IRON", false, 10, 110, t0.Add(45*time.Minute)),
	}
	r := Run(legs, map[string][]string{"X1-A": {"X1-B"}, "X1-B": {"X1-A"}}, cfg())
	if r.Boundaries != 1 {
		t.Fatalf("boundaries=%d decisions=%+v", r.Boundaries, r.Decisions)
	}
	d := r.Decisions[0]
	if d.LoopNext != "X1-B" || d.Reason != mvt.ReasonYieldBelow {
		t.Fatalf("scenario must leave for X1-B: %+v", d)
	}
	if !d.Stranded || d.LoopCredit != 1000 || d.ActualCredit != 100 {
		t.Fatalf("decision = %+v, want flagged and credited 10 units × X1-B's trailing 100/unit", d)
	}
}

// One observable decision (loop credit 150 vs actual 100) and one unobservable stay credited
// 1000 on the trailing rate: the headline passes on the counterfactual alone, and each other
// valuation reads the same decisions its own way.
func TestValuations_ReadTheUnobservableDecisionsFourWays(t *testing.T) {
	r := Report{Hulls: 2, Boundaries: 2, ActualJumps: 2, ActualMarginPerHull: 100, LoopMarginPerHull: 575, Decisions: []Decision{
		{Hull: "H1", From: "X1-A", ActualNext: "X1-B", LoopNext: "X1-A", ActualCredit: 100, LoopCredit: 150},
		{Hull: "H2", From: "X1-A", ActualNext: "X1-B", LoopNext: "X1-A", ActualCredit: 100, LoopCredit: 1000, Stranded: true},
	}}
	want := []Valuation{
		{Name: "trailing-rate", Boundaries: 2, ActualJumps: 2, ActualMarginPerHull: 100, LoopMarginPerHull: 575},
		{Name: "observable", Boundaries: 1, ActualJumps: 1, ActualMarginPerHull: 50, LoopMarginPerHull: 75},
		{Name: "neutral", Boundaries: 2, ActualJumps: 2, ActualMarginPerHull: 100, LoopMarginPerHull: 125},
		{Name: "zero-credit", Boundaries: 2, ActualJumps: 2, ActualMarginPerHull: 100, LoopMarginPerHull: 75},
	}
	got := r.Valuations()
	if len(got) != len(want) {
		t.Fatalf("valuations = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("valuation %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if pass, _ := got[0].Gate(); !pass {
		t.Fatal("the headline must pass on the trailing-rate credit")
	}
	if pass, _ := r.Gate(); !pass {
		t.Fatal("Report.Gate must agree with the headline valuation")
	}
	if pass, _ := got[3].Gate(); pass {
		t.Fatal("zero credit must fail: 75 < 100")
	}
	if robust, why := r.Robust(); robust || why != "zero-credit: margin per hull down: loop 75 vs actual 100" {
		t.Fatalf("robust = %v %q", robust, why)
	}
	if vs := (Report{}).Valuations(); vs != nil {
		t.Fatalf("no hulls must read as nothing, got %+v", vs)
	}
}

// Run's own report reads the same under Valuations as under its headline fields.
func TestRun_HeadlineValuationMatchesTheReport(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	legs := []trading.TourLegTelemetry{
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		rl("H1", "X1-A-2", "IRON", false, 10, 200, t0.Add(5*time.Minute)),
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0.Add(8*time.Minute)),
		rl("H1", "X1-A-2", "IRON", false, 10, 200, t0.Add(12*time.Minute)),
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0.Add(15*time.Minute)),
		rl("H1", "X1-B-2", "IRON", false, 10, 110, t0.Add(30*time.Minute)),
	}
	r := Run(legs, map[string][]string{"X1-A": {"X1-B"}, "X1-B": {"X1-A"}}, cfg())
	vs := r.Valuations()
	head := Valuation{Name: "trailing-rate", Boundaries: r.Boundaries, ActualJumps: r.ActualJumps, LoopJumps: r.LoopJumps,
		ActualMarginPerHull: r.ActualMarginPerHull, LoopMarginPerHull: r.LoopMarginPerHull}
	if len(vs) != 4 || vs[0] != head {
		t.Fatalf("headline = %+v, want %+v", vs[0], head)
	}
	if vs[1].Boundaries != 0 || vs[2].LoopMarginPerHull != r.ActualMarginPerHull || vs[3].LoopMarginPerHull != 0 {
		t.Fatalf("an all-unobservable run must read empty/neutral/zero: %+v", vs[1:])
	}
}

// Three hulls whose visits end on the same tick: the report must not depend on map order.
func TestRun_DeterministicOnTiedTimestamps(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	var legs []trading.TourLegTelemetry
	for _, h := range []string{"H1", "H2", "H3"} {
		legs = append(legs,
			rl(h, "X1-A-1", "IRON", true, 10, 100, t0),
			rl(h, "X1-A-2", "IRON", false, 10, 200, t0.Add(5*time.Minute)),
			rl(h, "X1-B-1", "IRON", true, 10, 100, t0.Add(30*time.Minute)),
			rl(h, "X1-B-2", "IRON", false, 10, 110, t0.Add(35*time.Minute)),
		)
	}
	graph := map[string][]string{"X1-A": {"X1-B"}, "X1-B": {"X1-A"}}
	first := Run(legs, graph, cfg())
	if first.Boundaries != 3 {
		t.Fatalf("boundaries = %d, want one tied boundary per hull", first.Boundaries)
	}
	for i := 0; i < 30; i++ {
		again := Run(legs, graph, cfg())
		if fmt.Sprintf("%+v", again) != fmt.Sprintf("%+v", first) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, again, first)
		}
	}
}

// Neither window has a sell at the destination: the decision is flagged and credited zero.
func TestRun_NothingObservedEitherWayCreditsZeroAndFlags(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	legs := []trading.TourLegTelemetry{
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		rl("H1", "X1-B-2", "IRON", false, 10, 110, t0.Add(35*time.Minute)),
	}
	r := Run(legs, map[string][]string{"X1-A": {"X1-B"}, "X1-B": {"X1-A"}}, cfg())
	if r.Boundaries != 1 {
		t.Fatalf("boundaries=%d decisions=%+v", r.Boundaries, r.Decisions)
	}
	d := r.Decisions[0]
	if d.LoopNext != "X1-A" || !d.Stranded || d.LoopCredit != 0 || d.ActualCredit != 100 {
		t.Fatalf("decision = %+v", d)
	}
	if pass, _ := r.Gate(); pass {
		t.Fatal("zero loop credit against real actual margin must fail the gate")
	}
}

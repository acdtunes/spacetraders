package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

type fakeTourReportSource struct {
	rows       []trading.TourLegTelemetry
	failed     int
	tourCPH    float64
	tourCPHOK  bool
	baseline   float64
	baselineOK bool
}

func (s *fakeTourReportSource) TourTelemetry(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error) {
	return s.rows, nil
}
func (s *fakeTourReportSource) FailedTourRunCount(ctx context.Context, playerID int, since time.Time) (int, error) {
	return s.failed, nil
}
func (s *fakeTourReportSource) TourCreditsPerHour(ctx context.Context, playerID int, since time.Time) (float64, bool, error) {
	return s.tourCPH, s.tourCPHOK, nil
}
func (s *fakeTourReportSource) TradeCreditsPerHour(ctx context.Context, playerID int, since time.Time) (float64, bool, error) {
	return s.baseline, s.baselineOK, nil
}

func telRow(tourID, good string, isBuy bool, planned, realized int, at time.Time) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{
		TourID: tourID, Good: good, IsBuy: isBuy,
		PlannedUnits: 40, RealizedUnits: 40,
		PlannedUnitPrice: planned, RealizedUnitPrice: realized,
		PlannedAt: at, RealizedAt: at.Add(time.Minute), PlayerID: 1,
	}
}

// lookbackRow is telRow marked as a look-back manifest buy: same shape, but its plan basis
// is the manifest's CACHED SourceAsk rather than the solver's projection.
func lookbackRow(tourID, good string, planned, realized int, at time.Time) trading.TourLegTelemetry {
	r := telRow(tourID, good, true, planned, realized, at)
	r.LegIndex = trading.LookbackLegIndex
	return r
}

// TestComputeTourGateMetrics_SplitsSolverFromLookbackBasis pins the sp-fpgl2 repair to the
// gate's headline figure.
//
// The gate's own doc calls the median plan-vs-realized price error "the metric that proves
// the model". It was pooling two populations that measure different things: solver legs,
// whose basis is the planner's projection, and look-back legs, whose basis is a cached ask
// the buy is then gated against — so a fresh cache reproduces itself and those legs read a
// median of EXACTLY 0.000%. In production that was 1423 of 3733 rows (38%), dragging the
// reported figure from the solver's 0.518% down to 0.309%.
//
// The repair REPORTS BOTH rather than dropping the look-back rows. Silently narrowing the
// population would be a worse honesty failure than the dilution it fixes: a future reader
// would have no way to tell that 38% of the evidence had been excluded.
//
// Fixture: four solver legs at 10/20/30/40% error (median 25) and four look-back legs at
// exactly 0% (median 0). Pooled they would median to 5 — a number describing neither
// population, and the one the gate used to print.
func TestComputeTourGateMetrics_SplitsSolverFromLookbackBasis(t *testing.T) {
	base := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	rows := []trading.TourLegTelemetry{
		telRow("ctr-1", "FUEL", true, 100, 110, base),                  // solver, 10%
		telRow("ctr-1", "IRON", true, 100, 120, base.Add(time.Hour)),   // solver, 20%
		telRow("ctr-2", "GOLD", true, 100, 130, base.Add(2*time.Hour)), // solver, 30%
		telRow("ctr-2", "SILK", true, 100, 140, base.Add(3*time.Hour)), // solver, 40%
		lookbackRow("ctr-1", "FOOD", 500, 500, base.Add(4*time.Hour)),  // look-back, 0%
		lookbackRow("ctr-1", "ORE", 600, 600, base.Add(5*time.Hour)),   // look-back, 0%
		lookbackRow("ctr-2", "FABRICS", 700, 700, base.Add(6*time.Hour)),
		lookbackRow("ctr-2", "MEDICINE", 800, 800, base.Add(7*time.Hour)),
	}

	m := computeTourGateMetrics(rows, 0, 50_000, true, 10_000, true)

	// The HEADLINE is the solver median — what the gate claims to measure.
	if !m.MedianAvailable {
		t.Fatalf("solver median must be available with four solver legs present")
	}
	if m.MedianPriceErrorPct != 25 {
		t.Fatalf("MedianPriceErrorPct = %.2f, want 25 (solver legs 10/20/30/40 only). "+
			"5 means the look-back legs are still pooled in and the headline still measures neither population",
			m.MedianPriceErrorPct)
	}
	if m.SolverLegCount != 4 {
		t.Fatalf("SolverLegCount = %d, want 4", m.SolverLegCount)
	}

	// The look-back figure is REPORTED, not discarded — with its own count, so a reader can
	// see how much of the evidence it represents.
	if !m.LookbackMedianAvailable {
		t.Fatalf("look-back median must be reported alongside, not dropped — hiding 38%% of the rows is the worse failure")
	}
	if m.LookbackMedianPriceErrorPct != 0 {
		t.Fatalf("LookbackMedianPriceErrorPct = %.2f, want 0 (a cached ask reproducing itself)", m.LookbackMedianPriceErrorPct)
	}
	if m.LookbackLegCount != 4 {
		t.Fatalf("LookbackLegCount = %d, want 4", m.LookbackLegCount)
	}

	// Tour counting is unaffected: a look-back leg is still a real leg of a real tour.
	if m.ToursCompleted != 2 {
		t.Fatalf("ToursCompleted = %d, want 2 — basis classification must not change tour counting", m.ToursCompleted)
	}
}

// TestComputeTourGateMetrics_VerdictGradesSolverBasisOnly proves the PASS/FAIL decision is
// taken on the solver median, and that this can only ever TIGHTEN the gate.
//
// Look-back legs sit at ~0% error, so pooling them can only pull the median DOWN — toward
// passing. Grading the solver basis alone is therefore the conservative direction, which is
// the only direction a measurement may move on its own (RULINGS #4). Here the solver legs
// are all far over the bar while enough 0% look-back legs are present that the POOLED median
// would sit under it: the old pooling would have PASSED this, the split must FAIL it.
func TestComputeTourGateMetrics_VerdictGradesSolverBasisOnly(t *testing.T) {
	base := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	var rows []trading.TourLegTelemetry
	for i := 0; i < 10; i++ { // 10 distinct tours so the tour-count criterion is met
		id := "ctr-" + string(rune('a'+i))
		at := base.Add(time.Duration(i) * time.Hour)
		// One solver leg at 50% error — way over the 15% bar.
		rows = append(rows, telRow(id, "G", true, 100, 150, at))
		// Two look-back legs at 0%, which would drag a pooled median to 0.
		rows = append(rows,
			lookbackRow(id, "H", 100, 100, at.Add(time.Minute)),
			lookbackRow(id, "I", 100, 100, at.Add(2*time.Minute)),
		)
	}

	m := computeTourGateMetrics(rows, 0, 100_000, true, 1_000, true)

	if m.MedianPriceErrorPct != 50 {
		t.Fatalf("MedianPriceErrorPct = %.2f, want 50 (solver only)", m.MedianPriceErrorPct)
	}
	if m.Pass {
		t.Fatalf("gate PASSED with a 50%% solver price error — pooling 0%% look-back legs must not be able to buy a pass")
	}
}

// TestComputeTourGateMetrics_AllLookbackFailsClosed: a window holding only look-back legs has
// no evidence about the model at all, so the gate must not pass on the cached ask's
// self-agreement. Fail-closed, matching the unreadable-cash-rate rule.
func TestComputeTourGateMetrics_AllLookbackFailsClosed(t *testing.T) {
	base := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	var rows []trading.TourLegTelemetry
	for i := 0; i < 10; i++ {
		id := "ctr-" + string(rune('a'+i))
		rows = append(rows, lookbackRow(id, "G", 100, 100, base.Add(time.Duration(i)*time.Hour)))
	}

	m := computeTourGateMetrics(rows, 0, 100_000, true, 1_000, true)

	if m.MedianAvailable {
		t.Fatalf("solver median must be UNAVAILABLE when no solver leg exists, got %.2f", m.MedianPriceErrorPct)
	}
	if m.Pass {
		t.Fatalf("gate PASSED on look-back legs alone — a cached ask agreeing with itself is not evidence about the model")
	}
	if m.LookbackLegCount != 10 {
		t.Fatalf("LookbackLegCount = %d, want 10 — the rows must still be reported even though they cannot grade", m.LookbackLegCount)
	}
}

// The three gate metrics compute from telemetry: distinct tour_ids, the median of the
// per-trade |planned−realized|/planned errors, and (with a baseline) the $/hr ratio.
// Three tours is short of the 10-tour gate → FAIL.
func TestComputeTourGateMetrics_ExactNumbersAndFailVerdict(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	rows := []trading.TourLegTelemetry{
		telRow("ctr-1", "MEDICINE", true, 1000, 1000, base),                   // 0%
		telRow("ctr-1", "MEDICINE", false, 2000, 1800, base.Add(1*time.Hour)), // 10%
		telRow("ctr-2", "FUEL", true, 100, 110, base.Add(2*time.Hour)),        // 10%
		telRow("ctr-2", "FUEL", false, 200, 240, base.Add(3*time.Hour)),       // 20%
		telRow("ctr-3", "FABRICS", false, 500, 500, base.Add(4*time.Hour)),    // 0%
	}
	// sp-461l: tour $/hr is now the transactions-cash tour rate (injected), not telemetry netting.
	m := computeTourGateMetrics(rows, 1 /*failed*/, 14000 /*tourCPH*/, true, 5000 /*singleLane*/, true)

	if m.ToursCompleted != 3 {
		t.Fatalf("ToursCompleted = %d, want 3", m.ToursCompleted)
	}
	if m.GuardViolations != 1 {
		t.Fatalf("GuardViolations = %d, want 1", m.GuardViolations)
	}
	// errors [0,10,10,20,0] → sorted [0,0,10,10,20] → median 10.
	if m.MedianPriceErrorPct != 10 {
		t.Fatalf("MedianPriceErrorPct = %.2f, want 10", m.MedianPriceErrorPct)
	}
	if !m.RatioAvailable {
		t.Fatalf("expected a ratio when the baseline is present")
	}
	if m.Pass {
		t.Fatalf("3 tours (< 10) and 1 violation must FAIL the gate")
	}
}

// The verdict passes only when all four conditions hold: >=10 tours, 0 violations,
// ratio >=1.5x, median error <=15%.
func TestComputeTourGateMetrics_PassesWhenAllMet(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	var rows []trading.TourLegTelemetry
	for i := 0; i < 10; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		id := "ctr-" + string(rune('a'+i))
		rows = append(rows,
			telRow(id, "G", true, 1000, 1000, at),                   // 0% error
			telRow(id, "G", false, 2000, 2000, at.Add(time.Minute)), // 0% error, +40*2000 revenue
		)
	}
	// Cash-true tour rate strongly positive; baseline set low so the ratio clears 1.5x.
	m := computeTourGateMetrics(rows, 0, 100000 /*tourCPH*/, true, 1.0 /*singleLane*/, true)

	if m.ToursCompleted != 10 {
		t.Fatalf("ToursCompleted = %d, want 10", m.ToursCompleted)
	}
	if m.MedianPriceErrorPct != 0 {
		t.Fatalf("MedianPriceErrorPct = %.2f, want 0", m.MedianPriceErrorPct)
	}
	if m.Ratio < tourGateMinRatio {
		t.Fatalf("Ratio = %.2f, want >= %.1f", m.Ratio, tourGateMinRatio)
	}
	if !m.Pass {
		t.Fatalf("all four conditions met but gate did not PASS: %+v", m)
	}
}

// sp-461l (epic sp-g9td): the graduation gate's tour $/hr now comes from the transactions-cash
// tour rate, NOT telemetry netting. sp-rd21 proved telemetry netting read ~2x inflated (dropped
// buy legs); this test pins the SOURCE: the telemetry here would net to a HUGE sells-heavy $/hr,
// but the injected cash rate is the true, lower one — TourCreditsPerHour and the ratio must track
// the CASH rate, so the gate fires on the true rate, not the inflated telemetry net.
func TestComputeTourGateMetrics_TourRateFromCashNotTelemetry(t *testing.T) {
	base := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	// 10 tours of pure sells (no buy legs at all) — the pathological sells-only shape the dropped-
	// buy bug produced. If tour $/hr were still telemetry-netted this would read a large positive
	// $/hr. The cash rate is what actually reconciles to the treasury.
	var rows []trading.TourLegTelemetry
	for i := 0; i < 10; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		id := "ctr-" + string(rune('a'+i))
		rows = append(rows,
			telRow(id, "G", true, 1000, 1000, at),                   // 0% price error
			telRow(id, "G", false, 2000, 2000, at.Add(time.Minute)), // 0% price error
		)
	}

	// Cash-true tour rate = 40,000/hr (the treasury-true value); single-lane baseline 10,000/hr
	// ⇒ ratio 4x. A telemetry net over the rows above would be a very different number.
	const cashTourCPH = 40_000.0
	m := computeTourGateMetrics(rows, 0, cashTourCPH, true, 10_000, true)

	if m.TourCreditsPerHour != cashTourCPH {
		t.Fatalf("TourCreditsPerHour = %.0f, want the injected cash rate %.0f (must NOT be telemetry-netted)", m.TourCreditsPerHour, cashTourCPH)
	}
	if !m.RatioAvailable || m.Ratio != 4.0 {
		t.Fatalf("Ratio = %.2f (available=%v), want 4.00 from cash %0.f / baseline 10000", m.Ratio, m.RatioAvailable, cashTourCPH)
	}
	// Tours-completed and median-price-error still come from telemetry (unchanged).
	if m.ToursCompleted != 10 {
		t.Fatalf("ToursCompleted = %d, want 10 (still counted from telemetry tour_ids)", m.ToursCompleted)
	}
	if !m.MedianAvailable || m.MedianPriceErrorPct != 0 {
		t.Fatalf("median price error = %.2f (available=%v), want 0 from telemetry", m.MedianPriceErrorPct, m.MedianAvailable)
	}
}

// When the transactions-cash tour rate is unreadable (empty tour window), the ratio is n/a and the
// gate cannot pass — fail-closed, never fabricated from telemetry.
func TestComputeTourGateMetrics_UnreadableCashRateFailsClosed(t *testing.T) {
	base := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	rows := []trading.TourLegTelemetry{
		telRow("ctr-1", "G", true, 1000, 1000, base),
		telRow("ctr-1", "G", false, 2000, 2000, base.Add(time.Minute)),
	}
	m := computeTourGateMetrics(rows, 0, 0 /*tourCPH*/, false /*unreadable*/, 10_000, true)
	if m.RatioAvailable {
		t.Fatalf("ratio must be unavailable when the cash tour rate is unreadable")
	}
	if m.Pass {
		t.Fatalf("gate must FAIL when the cash tour rate is unreadable (fail-closed)")
	}
}

// The rendered report ends with the exact GATE verdict line the captain greps for.
func TestRunTourReport_RendersGateLine(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	src := &fakeTourReportSource{
		rows: []trading.TourLegTelemetry{
			telRow("ctr-1", "MEDICINE", true, 1000, 1000, base),
			telRow("ctr-1", "MEDICINE", false, 2000, 1800, base.Add(time.Hour)),
		},
		failed: 0, tourCPH: 8000, tourCPHOK: true, baseline: 4000, baselineOK: true,
	}
	var buf bytes.Buffer
	if err := runTourReport(context.Background(), src, 1, base.Add(-168*time.Hour), &buf); err != nil {
		t.Fatalf("runTourReport: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Completed tours: 1") {
		t.Fatalf("report missing tour count:\n%s", out)
	}
	if !strings.Contains(out, "GATE: FAIL (need: 10 tours, >=1.5x, <=15%)") {
		t.Fatalf("report missing the exact GATE line:\n%s", out)
	}
}

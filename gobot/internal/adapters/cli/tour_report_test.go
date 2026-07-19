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

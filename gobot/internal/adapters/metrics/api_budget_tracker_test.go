package metrics

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// A crash in the budget tracker must never take down the request path it is
// observing — the same best-effort contract as the Prometheus collectors.
func TestAPIBudgetTracker_NilReceiver_DoesNotPanic(t *testing.T) {
	var tr *APIBudgetTracker
	require.NotPanics(t, func() {
		tr.Record("TORWIND-1", apibudget.PurposePoll, apibudget.SourceUnspecified, false, 0)
	})
	require.NotPanics(t, func() {
		report := tr.Report()
		assert.Zero(t, report.Current.TotalRequests)
	})
	require.NotPanics(t, func() {
		assert.Zero(t, tr.NonSourceRate(time.Minute, apibudget.SourceScanning))
	})
}

func TestAPIBudgetTracker_RecordThenReport_ReflectsRecordedEvents(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)}
	tr := NewAPIBudgetTracker(2.0, clock)

	tr.Record("TORWIND-1", apibudget.PurposePoll, apibudget.SourceNavigation, false, 0)
	tr.Record("TORWIND-1", apibudget.PurposeTransact, apibudget.SourceTrading, false, 0)
	tr.Record("", apibudget.PurposeRetry, apibudget.SourceTrading, true, 0) // not ship-scoped, but still counts globally

	report := tr.Report()

	assert.Equal(t, 3, report.Rolling5m.TotalRequests)
	assert.Equal(t, 1, report.Rolling5m.RateLimited429)
	require.Len(t, report.Rolling5m.PerHull, 1)
	assert.Equal(t, "TORWIND-1", report.Rolling5m.PerHull[0].Hull)
	assert.Equal(t, 2, report.Rolling5m.PerHull[0].RequestsInWindow)
}

func TestGlobalAPIBudgetTracker_SetThenGet_ReturnsSameInstance(t *testing.T) {
	t.Cleanup(func() { SetGlobalAPIBudgetTracker(nil) })

	tr := NewAPIBudgetTracker(2.0, nil)
	SetGlobalAPIBudgetTracker(tr)

	assert.Same(t, tr, GetGlobalAPIBudgetTracker())
}

func TestGlobalAPIBudgetTracker_Unset_ReturnsNil(t *testing.T) {
	t.Cleanup(func() { SetGlobalAPIBudgetTracker(nil) })
	SetGlobalAPIBudgetTracker(nil)

	assert.Nil(t, GetGlobalAPIBudgetTracker())
}

func TestNonSourceRate_ExcludesTaggedSource(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	tr := NewAPIBudgetTracker(2.0, clock)
	for i := 0; i < 30; i++ { // 30 scanning calls
		tr.Record("PROBE-1", apibudget.PurposePoll, apibudget.SourceScanning, false, 0)
	}
	for i := 0; i < 12; i++ { // 12 trading calls
		tr.Record("HAULER-1", apibudget.PurposeTransact, apibudget.SourceTrading, false, 0)
	}
	for i := 0; i < 6; i++ { // 6 untagged calls — MUST count as non-sensing
		tr.Record("SHIP-9", apibudget.PurposePoll, apibudget.SourceUnspecified, false, 0)
	}
	clock.Advance(60 * time.Second)
	got := tr.NonSourceRate(60*time.Second, apibudget.SourceScanning, apibudget.SourceCharting)
	want := 18.0 / 60.0 // trading + untagged
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("NonSourceRate = %v, want %v", got, want)
	}
}

// A window wider than what the tracker retains must be clamped to
// retentionWindow, not answered literally: the events beyond retention are
// already gone, so dividing by the requested span would dilute the rate and
// understate the competition sensing is sizing itself against.
func TestNonSourceRate_ClampsWindowToRetention(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	tr := NewAPIBudgetTracker(2.0, clock)
	for i := 0; i < 10; i++ {
		tr.Record("HAULER-1", apibudget.PurposeTransact, apibudget.SourceTrading, false, 0)
	}
	clock.Advance(60 * time.Second)

	got := tr.NonSourceRate(10*time.Minute, apibudget.SourceScanning)

	want := 10.0 / retentionWindow.Seconds() // clamped to 5m, not the requested 10m
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("NonSourceRate over an over-wide window = %v, want %v", got, want)
	}
}

func TestAPIBudgetTracker_PrunesEventsOlderThanRetentionWindow(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)}
	tr := NewAPIBudgetTracker(2.0, clock)

	tr.Record("TORWIND-1", apibudget.PurposePoll, apibudget.SourceUnspecified, false, 0)
	clock.Advance(10 * time.Minute) // well past the 5m rolling window

	report := tr.Report()

	assert.Zero(t, report.Rolling5m.TotalRequests, "events older than the retention window are pruned")
}

func TestAPIBudgetTracker_RecordedWaitsSurfaceAsTheWindowMean(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)}
	tr := NewAPIBudgetTracker(2.0, clock)

	tr.Record("TORWIND-1", apibudget.PurposeTransact, apibudget.SourceTrading, false, 12*time.Second)
	tr.Record("TORWIND-2", apibudget.PurposeTransact, apibudget.SourceTrading, false, 8*time.Second)

	report := tr.Report()

	assert.InDelta(t, 10.0, report.Rolling5m.MeanRateLimitWaitSeconds, 0.0001,
		"the estimator prices contention off this mean, so it must survive the tracker")
}

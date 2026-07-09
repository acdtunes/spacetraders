package metrics

import (
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
		tr.Record("TORWIND-1", apibudget.PurposePoll, false)
	})
	require.NotPanics(t, func() {
		report := tr.Report()
		assert.Zero(t, report.Current.TotalRequests)
	})
}

func TestAPIBudgetTracker_RecordThenReport_ReflectsRecordedEvents(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)}
	tr := NewAPIBudgetTracker(2.0, clock)

	tr.Record("TORWIND-1", apibudget.PurposePoll, false)
	tr.Record("TORWIND-1", apibudget.PurposeTransact, false)
	tr.Record("", apibudget.PurposeRetry, true) // not ship-scoped, but still counts globally

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

func TestAPIBudgetTracker_PrunesEventsOlderThanRetentionWindow(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)}
	tr := NewAPIBudgetTracker(2.0, clock)

	tr.Record("TORWIND-1", apibudget.PurposePoll, false)
	clock.Advance(10 * time.Minute) // well past the 5m rolling window

	report := tr.Report()

	assert.Zero(t, report.Rolling5m.TotalRequests, "events older than the retention window are pruned")
}

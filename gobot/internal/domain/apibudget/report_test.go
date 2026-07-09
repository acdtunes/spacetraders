package apibudget

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fixedNow = time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

// evt is a small constructor to keep fixtures readable: age is how far before
// fixedNow the event occurred.
func evt(hull string, purpose Purpose, age time.Duration, rateLimited bool) Event {
	return Event{
		Hull:        hull,
		Purpose:     purpose,
		Timestamp:   fixedNow.Add(-age),
		RateLimited: rateLimited,
	}
}

func TestComputeReport_NoEvents_ReturnsZeroValueReportWithoutDivideByZero(t *testing.T) {
	report := ComputeReport(nil, fixedNow, 5*time.Minute, 2.0)

	assert.Equal(t, 0, report.TotalRequests)
	assert.Zero(t, report.GlobalReqPerSec)
	assert.Zero(t, report.UtilizationPct)
	assert.Equal(t, 2.0, report.HeadroomReqPerSec, "full ceiling is free headroom when idle")
	assert.Zero(t, report.RateLimited429)
	assert.Zero(t, report.HullsToCeiling)
	assert.Empty(t, report.PerHull)
}

func TestComputeReport_PrunesEventsOutsideWindow(t *testing.T) {
	events := []Event{
		evt("HULL-1", PurposePoll, 10*time.Second, false),  // inside a 1m window
		evt("HULL-1", PurposePoll, 2*time.Minute, false),   // outside a 1m window
		evt("HULL-1", PurposePoll, 90*time.Second, false),  // outside a 1m window
	}

	report := ComputeReport(events, fixedNow, time.Minute, 2.0)

	assert.Equal(t, 1, report.TotalRequests, "only the event inside the window counts")
}

func TestComputeReport_GlobalRateIsCountDividedByWindowSeconds(t *testing.T) {
	events := []Event{
		evt("HULL-1", PurposePoll, 1*time.Second, false),
		evt("HULL-2", PurposeTransact, 2*time.Second, false),
	}

	report := ComputeReport(events, fixedNow, 10*time.Second, 2.0)

	assert.Equal(t, 2, report.TotalRequests)
	assert.InDelta(t, 0.2, report.GlobalReqPerSec, 0.0001, "2 requests / 10s window = 0.2 req/s")
}

func TestComputeReport_UtilizationAndHeadroomAreDerivedFromCeiling(t *testing.T) {
	events := make([]Event, 0, 10)
	for i := 0; i < 10; i++ {
		events = append(events, evt("HULL-1", PurposePoll, time.Duration(i)*time.Millisecond, false))
	}
	// 10 requests in a 10s window = 1 req/s against a 2 req/s ceiling.
	report := ComputeReport(events, fixedNow, 10*time.Second, 2.0)

	assert.InDelta(t, 1.0, report.GlobalReqPerSec, 0.0001)
	assert.InDelta(t, 50.0, report.UtilizationPct, 0.0001, "1 of 2 req/s ceiling = 50%")
	assert.InDelta(t, 1.0, report.HeadroomReqPerSec, 0.0001)
}

func TestComputeReport_UtilizationZeroCeiling_DoesNotDivideByZero(t *testing.T) {
	events := []Event{evt("HULL-1", PurposePoll, time.Second, false)}

	require.NotPanics(t, func() {
		report := ComputeReport(events, fixedNow, 10*time.Second, 0)
		assert.Zero(t, report.UtilizationPct)
		assert.Zero(t, report.HeadroomReqPerSec)
	})
}

func TestComputeReport_SplitsCountsAndSharesByPurpose(t *testing.T) {
	events := []Event{
		evt("HULL-1", PurposePoll, time.Second, false),
		evt("HULL-1", PurposePoll, time.Second, false),
		evt("HULL-1", PurposeTransact, time.Second, false),
		evt("HULL-1", PurposeRetry, time.Second, false),
	}

	report := ComputeReport(events, fixedNow, 10*time.Second, 2.0)

	assert.Equal(t, 2, report.PurposeCounts[PurposePoll])
	assert.Equal(t, 1, report.PurposeCounts[PurposeTransact])
	assert.Equal(t, 1, report.PurposeCounts[PurposeRetry])
	assert.InDelta(t, 50.0, report.PurposeSharePct[PurposePoll], 0.0001)
	assert.InDelta(t, 25.0, report.PurposeSharePct[PurposeTransact], 0.0001)
	assert.InDelta(t, 25.0, report.PurposeSharePct[PurposeRetry], 0.0001)
}

func TestComputeReport_CountsRateLimited429sAndPerMinuteRate(t *testing.T) {
	events := []Event{
		evt("HULL-1", PurposeRetry, time.Second, true),
		evt("HULL-1", PurposeRetry, time.Second, true),
		evt("HULL-1", PurposePoll, time.Second, false),
	}

	// 2 429s in a 60s window = 2/min.
	report := ComputeReport(events, fixedNow, 60*time.Second, 2.0)

	assert.Equal(t, 2, report.RateLimited429)
	assert.InDelta(t, 2.0, report.RateLimited429PerMin, 0.0001)
}

func TestComputeReport_PerHullBreakdownSortedDescByRate(t *testing.T) {
	events := []Event{
		evt("QUIET-1", PurposePoll, time.Second, false),
		evt("BUSY-1", PurposePoll, time.Second, false),
		evt("BUSY-1", PurposePoll, time.Second, false),
		evt("BUSY-1", PurposeTransact, time.Second, false),
	}

	report := ComputeReport(events, fixedNow, 10*time.Second, 2.0)

	require.Len(t, report.PerHull, 2)
	assert.Equal(t, "BUSY-1", report.PerHull[0].Hull, "busiest hull sorts first")
	assert.Equal(t, 3, report.PerHull[0].RequestsInWindow)
	assert.Equal(t, "QUIET-1", report.PerHull[1].Hull)
	assert.Equal(t, 1, report.PerHull[1].RequestsInWindow)
}

func TestComputeReport_EventsWithoutHullAreExcludedFromPerHullBreakdown(t *testing.T) {
	events := []Event{
		evt("", PurposePoll, time.Second, false), // e.g. /my/agent, /systems/* — not ship-scoped
		evt("HULL-1", PurposePoll, time.Second, false),
	}

	report := ComputeReport(events, fixedNow, 10*time.Second, 2.0)

	assert.Equal(t, 2, report.TotalRequests, "global count still includes non-hull-scoped requests")
	require.Len(t, report.PerHull, 1)
	assert.Equal(t, "HULL-1", report.PerHull[0].Hull)
}

func TestComputeReport_HullsToCeiling_IsCeilingDividedByAveragePerHullRate(t *testing.T) {
	events := []Event{
		evt("HULL-1", PurposePoll, time.Second, false),
		evt("HULL-2", PurposePoll, time.Second, false),
	}
	// 2 hulls each doing 1 req / 10s = 0.1 req/s/hull average; ceiling 2 req/s.
	report := ComputeReport(events, fixedNow, 10*time.Second, 2.0)

	assert.InDelta(t, 20.0, report.HullsToCeiling, 0.0001, "2.0 ceiling / 0.1 avg-per-hull-rate = 20 hulls")
}

func TestComputeReport_HullsToCeiling_NoActiveHulls_IsZeroNotInf(t *testing.T) {
	events := []Event{evt("", PurposePoll, time.Second, false)} // no hull-scoped traffic

	report := ComputeReport(events, fixedNow, 10*time.Second, 2.0)

	assert.Zero(t, report.HullsToCeiling)
}

func TestComputeDualReport_ReturnsCurrentAndRolling5mWindows(t *testing.T) {
	events := []Event{
		evt("HULL-1", PurposePoll, 5*time.Second, false),   // inside both windows
		evt("HULL-1", PurposePoll, 4*time.Minute, false),   // outside "current", inside 5m
	}

	dual := ComputeDualReport(events, fixedNow, 2.0)

	assert.Equal(t, 1, dual.Current.TotalRequests, "current window is narrow (10s)")
	assert.Equal(t, 2, dual.Rolling5m.TotalRequests, "rolling window is 5 minutes")
}

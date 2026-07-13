package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeBudgetReporter returns a chosen DualReport so the reader's translation + fail-closed logic is
// pinned without re-testing the tracker's window math (that lives in apibudget/report_test.go).
type fakeBudgetReporter struct{ report apibudget.DualReport }

func (f *fakeBudgetReporter) Report() apibudget.DualReport { return f.report }

// sp-a5dq: the reader surfaces the rolling-5m utilization percent as READABLE — the same
// throughput/ceiling basis the ApproachCeiling alert uses — so the api_util guard can actually
// gate concurrency growth. This is the fix for the "no per-coordinator read path → fail-open" stub.
func TestAutosizerAPIUtilReader_RollingUtilizationIsReadable(t *testing.T) {
	reader := &autosizerAPIUtilReader{reporter: &fakeBudgetReporter{report: apibudget.DualReport{
		Rolling5m: apibudget.Report{CeilingReqPerSec: 2.0, GlobalReqPerSec: 1.8, UtilizationPct: 90},
	}}}

	pct, readable, err := reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.True(t, readable, "a live tracker with a configured ceiling must read as readable")
	require.Equal(t, 90.0, pct, "the reader surfaces the rolling-5m utilization percent")
}

// sp-a5dq / RULINGS #4: when no utilization surface exists the reader fails CLOSED (readable=false),
// so the guard holds growth instead of the old silent fail-open. Covers both an unset reporter and a
// typed-nil *APIBudgetTracker (the daemon-never-set-the-global case, whose nil-safe Report() would
// otherwise masquerade as a readable 0%).
func TestAutosizerAPIUtilReader_AbsentSurface_FailsClosed(t *testing.T) {
	// (a) reporter never wired.
	reader := &autosizerAPIUtilReader{reporter: nil}
	_, readable, err := reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.False(t, readable, "an unwired reporter must fail closed")

	// (b) typed-nil *APIBudgetTracker (global never set): Report() is nil-safe and returns a
	// zero-value DualReport (ceiling 0), which must be treated as unreadable, not a readable 0%.
	reader = &autosizerAPIUtilReader{reporter: metrics.GetGlobalAPIBudgetTracker()}
	_, readable, err = reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.False(t, readable, "a typed-nil tracker (zero ceiling) must fail closed, not read as 0%")

	// (c) a report with an unconfigured ceiling cannot yield a meaningful utilization → fail closed.
	reader = &autosizerAPIUtilReader{reporter: &fakeBudgetReporter{report: apibudget.DualReport{
		Rolling5m: apibudget.Report{CeilingReqPerSec: 0, GlobalReqPerSec: 5},
	}}}
	_, readable, err = reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.False(t, readable, "a zero-ceiling report must fail closed")
}

// sp-a5dq: the REAL *metrics.APIBudgetTracker (the daemon-startup singleton) satisfies the reader's
// reporter seam and yields a readable utilization once wired — proving the ceiling is no longer an
// unreadable stub. Uses a mock clock so the recorded events land inside the rolling window instantly.
func TestAutosizerAPIUtilReader_RealTracker_IsReadable(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}
	tracker := metrics.NewAPIBudgetTracker(2.0, clock) // 2 req/s ceiling — the live limiter's rate
	tracker.Record("SHIP-1", apibudget.PurposePoll, false)
	tracker.Record("SHIP-1", apibudget.PurposeTransact, false)

	reader := &autosizerAPIUtilReader{reporter: tracker}
	pct, readable, err := reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.True(t, readable, "a live tracker must read as readable (not the old fail-open stub)")
	require.Equal(t, tracker.Report().Rolling5m.UtilizationPct, pct, "the reader forwards the tracker's rolling-5m utilization")
}

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

// staticReporter is the test equivalent of the old captured field: a resolver that always hands
// back the same reporter. Production deliberately has no such shape — see globalAPIBudgetReporter.
func staticReporter(r apiBudgetReporter) func() apiBudgetReporter {
	return func() apiBudgetReporter { return r }
}

// The reader surfaces the rolling-5m utilization percent as READABLE — the same
// throughput/ceiling basis the ApproachCeiling alert uses — so the api_util guard can actually
// gate concurrency growth. This is the fix for the "no per-coordinator read path → fail-open" stub.
func TestAutosizerAPIUtilReader_RollingUtilizationIsReadable(t *testing.T) {
	reader := &fleetAPIUtilReader{resolve: staticReporter(&fakeBudgetReporter{report: apibudget.DualReport{
		Rolling5m: apibudget.Report{CeilingReqPerSec: 2.0, GlobalReqPerSec: 1.8, UtilizationPct: 90},
	}})}

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
	// (a) resolver never wired.
	reader := &fleetAPIUtilReader{resolve: nil}
	_, readable, err := reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.False(t, readable, "an unwired reporter must fail closed")

	// (b) typed-nil *APIBudgetTracker (global never set): Report() is nil-safe and returns a
	// zero-value DualReport (ceiling 0), which must be treated as unreadable, not a readable 0%.
	reader = &fleetAPIUtilReader{resolve: staticReporter(metrics.GetGlobalAPIBudgetTracker())}
	_, readable, err = reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.False(t, readable, "a typed-nil tracker (zero ceiling) must fail closed, not read as 0%")

	// (c) a report with an unconfigured ceiling cannot yield a meaningful utilization → fail closed.
	reader = &fleetAPIUtilReader{resolve: staticReporter(&fakeBudgetReporter{report: apibudget.DualReport{
		Rolling5m: apibudget.Report{CeilingReqPerSec: 0, GlobalReqPerSec: 5},
	}})}
	_, readable, err = reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.False(t, readable, "a zero-ceiling report must fail closed")
}

// The REAL *metrics.APIBudgetTracker (the daemon-startup singleton) satisfies the reader's
// reporter seam and yields a readable utilization once wired. Uses a mock clock so the recorded
// events land inside the rolling window instantly.
func TestAutosizerAPIUtilReader_RealTracker_IsReadable(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}
	tracker := metrics.NewAPIBudgetTracker(2.0, clock) // 2 req/s ceiling — the live limiter's rate
	tracker.Record("SHIP-1", apibudget.PurposePoll, apibudget.SourceUnspecified, false)
	tracker.Record("SHIP-1", apibudget.PurposeTransact, apibudget.SourceUnspecified, false)

	reader := &fleetAPIUtilReader{resolve: staticReporter(tracker)}
	pct, readable, err := reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.True(t, readable, "a live tracker must read as readable (not the old fail-open stub)")
	require.Equal(t, tracker.Report().Rolling5m.UtilizationPct, pct, "the reader forwards the tracker's rolling-5m utilization")
}

// ---------------------------------------------------------------------------------------------
// sp-a75fz: WIRING ORDER MUST NOT DECIDE WHETHER THE FLEET CAN GROW
// ---------------------------------------------------------------------------------------------

// THE ACCEPTANCE CRITERION: the port is built BEFORE the tracker exists, and still reads a live
// tracker once one appears.
//
// This is the reversed main.go, reproduced. Today the daemon constructs the tracker at :778 and
// wires the autosizer at :1196, and the old code captured the pointer during that wiring — so it
// was correct by ordering alone, with nothing enforcing the order. Reverse those two lines and the
// captured pointer is nil forever: GuardAPIUtil's nil reader fails CLOSED (correctly), so the fleet
// stops growing everywhere, permanently, with no error and no metric. It reads as "the autosizer
// decided not to grow".
//
// Against the captured-pointer version this test fails, and it fails in the shape that matters:
// readable=false forever rather than for one tick.
func TestAutosizerAPIUtilReader_ResolvesTheTrackerWiredAfterThePortWasBuilt(t *testing.T) {
	t.Cleanup(func() { metrics.SetGlobalAPIBudgetTracker(nil) })
	metrics.SetGlobalAPIBudgetTracker(nil) // the tracker does not exist yet

	// Built exactly as the composition root builds it, but in the reversed order.
	reader := &fleetAPIUtilReader{resolve: globalAPIBudgetReporter}

	_, readable, err := reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.False(t, readable,
		"with no tracker anywhere the guard must fail CLOSED — that half is correct and must not change (RULINGS #4)")

	// ...and now the daemon gets round to constructing it.
	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}
	tracker := metrics.NewAPIBudgetTracker(2.0, clock)
	tracker.Record("SHIP-1", apibudget.PurposePoll, apibudget.SourceUnspecified, false)
	metrics.SetGlobalAPIBudgetTracker(tracker)

	pct, readable, err := reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.True(t, readable,
		"THE BUG: a port built before the tracker must still find it. Capturing the pointer at wiring time pins readable=false for the life of the process, and because the nil guard fails closed the only symptom is a fleet that silently never grows")
	require.Equal(t, tracker.Report().Rolling5m.UtilizationPct, pct,
		"and it must read the LIVE tracker, not a stale or zero one")
}

// THE RESOLVER IS CONSULTED EVERY READ, not memoised after the first success. A resolver called
// once and cached is the captured pointer again, one layer down — same failure, new hiding place.
func TestAutosizerAPIUtilReader_ResolvesOnEveryReadRatherThanCaching(t *testing.T) {
	calls := 0
	live := &fakeBudgetReporter{report: apibudget.DualReport{
		Rolling5m: apibudget.Report{CeilingReqPerSec: 2.0, UtilizationPct: 10},
	}}
	reader := &fleetAPIUtilReader{resolve: func() apiBudgetReporter {
		calls++
		return live
	}}

	for i := 0; i < 3; i++ {
		_, readable, err := reader.UtilizationPct(context.Background())
		require.NoError(t, err)
		require.True(t, readable)
	}
	require.Equal(t, 3, calls,
		"the resolver must run on every read; caching it re-creates the wiring-time capture the fix removes")
}

// A TRACKER REPLACED AT RUNTIME IS PICKED UP. The daemon sets the global once today, so this is a
// property of the design rather than a live path — but it is the property that makes the ordering
// irrelevant, and asserting it is what stops a future memoisation looking harmless.
func TestAutosizerAPIUtilReader_FollowsTheGlobalWhenItIsReplaced(t *testing.T) {
	t.Cleanup(func() { metrics.SetGlobalAPIBudgetTracker(nil) })
	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}

	quiet := metrics.NewAPIBudgetTracker(2.0, clock)
	metrics.SetGlobalAPIBudgetTracker(quiet)
	reader := &fleetAPIUtilReader{resolve: globalAPIBudgetReporter}
	quietPct, readable, err := reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.True(t, readable)

	busy := metrics.NewAPIBudgetTracker(2.0, clock)
	for i := 0; i < 200; i++ {
		busy.Record("SHIP-1", apibudget.PurposeTransact, apibudget.SourceUnspecified, false)
	}
	metrics.SetGlobalAPIBudgetTracker(busy)

	busyPct, readable, err := reader.UtilizationPct(context.Background())
	require.NoError(t, err)
	require.True(t, readable)
	require.Greater(t, busyPct, quietPct,
		"the reader must follow the CURRENT global; reading a saturated tracker as quiet is how the guard permits growth against no budget")
}

// globalAPIBudgetReporter must hand back an untyped nil rather than an interface wrapping a nil
// *APIBudgetTracker. A typed nil is non-nil to `== nil`, so it slips past the reader's own guard and
// leaves the fail-closed decision to the nil-receiver Report() one layer down. Both fail closed
// today; this keeps the legible one legible.
func TestGlobalAPIBudgetReporter_ReturnsUntypedNilWhenNoTrackerIsSet(t *testing.T) {
	t.Cleanup(func() { metrics.SetGlobalAPIBudgetTracker(nil) })
	metrics.SetGlobalAPIBudgetTracker(nil)

	require.Nil(t, globalAPIBudgetReporter(),
		"an interface holding a nil *APIBudgetTracker is NOT nil, and would sail past the reader's nil check")
}

package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- tests: sp-zixw effectiveScanInterval (direct/legacy launch path) -----

// No ScanInterval supplied (the CLI/legacy direct-launch path: workflow
// scout-markets, the daemon ScoutTour RPC) defaults to 15m — the captain's ask —
// replacing the old hardcoded 5-minute wait.
func TestEffectiveScanInterval_ZeroDefaultsTo15Minutes(t *testing.T) {
	require.Equal(t, 15*time.Minute, effectiveScanInterval(0))
}

// A negative interval is also treated as "unset": the <=0 check must catch it
// the same as zero rather than passing it through to clampScanInterval, where it
// would floor to 5m instead of defaulting to 15m.
func TestEffectiveScanInterval_NegativeTreatedAsUnset(t *testing.T) {
	require.Equal(t, 15*time.Minute, effectiveScanInterval(-1*time.Minute))
}

// An explicit in-range interval passes through unchanged.
func TestEffectiveScanInterval_ExplicitValueHonored(t *testing.T) {
	require.Equal(t, 7*time.Minute, effectiveScanInterval(7*time.Minute))
}

// An explicit interval below the floor clamps up to 5m.
func TestEffectiveScanInterval_BelowFloorClampsUp(t *testing.T) {
	require.Equal(t, 5*time.Minute, effectiveScanInterval(1*time.Minute))
}

// An explicit interval above the cap clamps down to 30m.
func TestEffectiveScanInterval_AboveCapClampsDown(t *testing.T) {
	require.Equal(t, 30*time.Minute, effectiveScanInterval(45*time.Minute))
}

// ---- tests: sp-zixw sleepInterruptibly (clock-injected wait) --------------

// sleepInterruptibly is clock-driven: on a MockClock, a 30-minute wait advances
// the mock's CurrentTime by the full duration while consuming no real wall time
// (MockClock.Sleep only advances CurrentTime, it never blocks) — proving
// continuousMarketScanning's wait goes through h.clock rather than a bare
// time.After.
func TestSleepInterruptibly_MockClock_NoWallTime(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := &ScoutTourHandler{clock: clock}
	before := clock.CurrentTime

	start := time.Now()
	completed := h.sleepInterruptibly(context.Background(), 30*time.Minute)
	realElapsed := time.Since(start)

	require.True(t, completed, "the wait must complete normally when ctx is never cancelled")
	require.Equal(t, 30*time.Minute, clock.CurrentTime.Sub(before), "the mock clock must advance by the full wait duration")
	require.Less(t, realElapsed, 1*time.Second, "a MockClock-driven wait must consume no real wall time regardless of the requested duration")
}

// ctx cancellation interrupts the wait promptly, even against a real clock
// sleeping for several seconds. Mirrors the existing sleepInterruptibly
// precedent (run_factory_coordinator_no_work_backoff_test.go's
// TestSleepInterruptibly_ContextCancelled_ReturnsPromptly) and additionally
// asserts the bool return value this handler's copy adds.
func TestSleepInterruptibly_ContextCancelled_ReturnsPromptly(t *testing.T) {
	h := &ScoutTourHandler{clock: shared.NewRealClock()}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	completed := h.sleepInterruptibly(ctx, 5*time.Second)
	elapsed := time.Since(start)

	require.False(t, completed, "a cancelled context must report the wait as not completed")
	require.Less(t, elapsed, 1*time.Second, "expected context cancellation to interrupt a 5s sleep promptly")
}

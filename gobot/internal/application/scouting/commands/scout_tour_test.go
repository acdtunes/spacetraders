package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
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

// ---- tests: sp-x8i5 stableJitter / waitStartJitter (start-of-tour phase jitter) --

// The same id must always produce the same offset — no math/rand, stable across
// restarts, or a daemon restart would reshuffle every scout's phase and could
// re-cohere the very wave this fix is meant to decohere.
func TestStableJitter_DeterministicSameIDSameOffset(t *testing.T) {
	require.Equal(t, stableJitter("PROBE-1", 120*time.Second), stableJitter("PROBE-1", 120*time.Second))
}

// The offset must always land in [0, ceiling) regardless of id.
func TestStableJitter_BoundedByCeiling(t *testing.T) {
	ceiling := 45 * time.Second
	for _, id := range []string{"PROBE-A", "PROBE-B", "PROBE-C", "PROBE-D", "PROBE-E"} {
		jitter := stableJitter(id, ceiling)
		require.GreaterOrEqualf(t, jitter, time.Duration(0), "id %s", id)
		require.Lessf(t, jitter, ceiling, "id %s", id)
	}
}

// A zero or negative ceiling means "no jitter" — callers resolve 0/absent config to
// defaultTourStartJitterMax themselves; stableJitter's own <=0 case must stay 0 so it
// never accidentally divides by a non-positive modulus.
func TestStableJitter_NonPositiveCeilingReturnsZero(t *testing.T) {
	require.Equal(t, time.Duration(0), stableJitter("PROBE-1", 0))
	require.Equal(t, time.Duration(0), stableJitter("PROBE-1", -1*time.Second))
}

// 0/absent StartJitterMaxSecs must defer to defaultTourStartJitterMax, not skip
// jitter entirely — proven on a MockClock so the assertion costs no real wall time
// even though the ceiling is 120s.
func TestWaitStartJitter_ZeroConfigDefersToDefault(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := &ScoutTourHandler{clock: clock}
	cmd := &ScoutTourCommand{ShipSymbol: "PROBE-DEFAULT", StartJitterMaxSecs: 0}
	before := clock.CurrentTime

	completed := h.waitStartJitter(context.Background(), cmd)

	require.True(t, completed)
	wantJitter := stableJitter(cmd.ShipSymbol, defaultTourStartJitterMax)
	require.Equal(t, wantJitter, clock.CurrentTime.Sub(before))
	require.Less(t, clock.CurrentTime.Sub(before), defaultTourStartJitterMax)
}

// An explicit StartJitterMaxSecs must be honored as the ceiling instead of the
// package default.
func TestWaitStartJitter_ExplicitCeilingHonored(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := &ScoutTourHandler{clock: clock}
	cmd := &ScoutTourCommand{ShipSymbol: "PROBE-EXPLICIT", StartJitterMaxSecs: 10}
	before := clock.CurrentTime

	completed := h.waitStartJitter(context.Background(), cmd)

	require.True(t, completed)
	wantJitter := stableJitter(cmd.ShipSymbol, 10*time.Second)
	require.Equal(t, wantJitter, clock.CurrentTime.Sub(before))
	require.Less(t, clock.CurrentTime.Sub(before), 10*time.Second)
}

// ctx cancellation must interrupt the jitter wait promptly, mirroring
// sleepInterruptibly's own cancellation contract (a jittering container must still
// exit promptly on stop).
func TestWaitStartJitter_CtxCancelled_ReturnsPromptly(t *testing.T) {
	h := &ScoutTourHandler{clock: shared.NewRealClock()}
	cmd := &ScoutTourCommand{ShipSymbol: "PROBE-CANCEL", StartJitterMaxSecs: 5}

	// Sanity: this fixture must actually exercise a non-zero jitter, or the wait
	// would return true immediately without ever reaching sleepInterruptibly and the
	// cancellation race below would prove nothing.
	require.NotZero(t, stableJitter(cmd.ShipSymbol, 5*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	completed := h.waitStartJitter(ctx, cmd)
	elapsed := time.Since(start)

	require.False(t, completed, "a cancelled context must interrupt the jitter wait")
	require.Less(t, elapsed, 1*time.Second, "expected cancellation to interrupt a 5s-ceiling jitter wait promptly")
}

// ---- tests: sp-enry circuitPaceInterval (end-of-circuit pacing) ------------

// A circuit shorter than the freshness target waits only the REMAINDER — pacing
// the circuit PERIOD to the target rather than adding a full interval. This is the
// partitioned-probe case: a small partition scans fast, then idles to the target so
// the API-budget invariant (scans/hour ≈ markets/target, independent of N) holds.
func TestCircuitPaceInterval_ShortCircuitWaitsRemainder(t *testing.T) {
	require.Equal(t, 20*time.Minute, circuitPaceInterval(30*time.Minute, 10*time.Minute))
}

// A circuit that already meets or exceeds the target waits ZERO — so a single-hull
// post over a big system loops as fast as travel allows, byte-identical to the
// pre-enry travel-paced multi-market loop (no wait injected).
func TestCircuitPaceInterval_LongCircuitWaitsZero(t *testing.T) {
	require.Equal(t, time.Duration(0), circuitPaceInterval(30*time.Minute, 30*time.Minute))
	require.Equal(t, time.Duration(0), circuitPaceInterval(30*time.Minute, 45*time.Minute))
}

// ---- tests: sp-enry executeMultiMarketTour pacing (per-hop vs per-circuit) --

// fakeTourMediator answers every NavigateRouteCommand instantly WITHOUT advancing
// the injected clock, so the only clock motion an executeMultiMarketTour test sees
// is the coordinator's own end-of-circuit pacing wait.
type fakeTourMediator struct {
	common.Mediator
	navs int
}

func (m *fakeTourMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	m.navs++
	return &shipNav.NavigateRouteResponse{Status: "completed"}, nil
}

// A partitioned probe over several markets waits ONCE per circuit (end-of-circuit),
// never between hops. Driven on a MockClock: the clock advances by exactly the
// pace per completed circuit and NOT during a circuit, proving there is no per-hop
// wait (Admiral doctrine) and that pacing is applied per partition per circuit.
func TestExecuteMultiMarketTour_PacesPerCircuitNotPerHop(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	med := &fakeTourMediator{}
	h := &ScoutTourHandler{mediator: med, clock: clock}

	// 3-market partition, 2 finite circuits, 30m freshness target. Instant navs →
	// each circuit's travel time is 0, so each non-final circuit paces the full 30m.
	cmd := &ScoutTourCommand{
		PlayerID:     shared.MustNewPlayerID(1),
		ShipSymbol:   "PROBE-1",
		Markets:      []string{"M1", "M2", "M3"},
		Iterations:   2,
		ScanInterval: 30 * time.Minute,
	}
	response := &ScoutTourResponse{}
	start := clock.CurrentTime

	require.NoError(t, h.executeMultiMarketTour(context.Background(), cmd, cmd.Markets, response))

	require.Equal(t, 6, med.navs, "2 circuits × 3 markets = 6 navigations, each scanning on arrival")
	require.Equal(t, 6, response.MarketsVisited)
	require.Equal(t, 2, response.Iterations)
	// Exactly ONE end-of-circuit pace happened (after circuit 1; the final circuit is
	// not paced). If pacing were per-hop the clock would have advanced 30m × 5 gaps.
	require.Equal(t, 30*time.Minute, clock.CurrentTime.Sub(start),
		"the clock must advance by exactly one circuit-pace (per-circuit, not per-hop)")
}

// A circuit whose own travel time already meets the target injects NO wait —
// preserving the pre-enry travel-paced behavior for a big single-hull system.
func TestExecuteMultiMarketTour_LongCircuitNoWait(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	// A mediator that advances the clock 40m per nav → a single 2-market circuit
	// takes 80m, well past the 30m target, so no end-of-circuit wait is added.
	med := &clockAdvancingMediator{clock: clock, perNav: 40 * time.Minute}
	h := &ScoutTourHandler{mediator: med, clock: clock}

	cmd := &ScoutTourCommand{
		PlayerID:     shared.MustNewPlayerID(1),
		ShipSymbol:   "PROBE-1",
		Markets:      []string{"M1", "M2"},
		Iterations:   2,
		ScanInterval: 30 * time.Minute,
	}
	response := &ScoutTourResponse{}
	start := clock.CurrentTime

	require.NoError(t, h.executeMultiMarketTour(context.Background(), cmd, cmd.Markets, response))

	// Only the 4 navigations advanced the clock (4 × 40m); no pacing wait was added
	// because each circuit already exceeded the target.
	require.Equal(t, 160*time.Minute, clock.CurrentTime.Sub(start),
		"a circuit longer than the target must add no end-of-circuit wait")
}

// clockAdvancingMediator advances the injected clock by perNav on each navigation,
// simulating real travel time so a circuit can exceed the freshness target.
type clockAdvancingMediator struct {
	common.Mediator
	clock  *shared.MockClock
	perNav time.Duration
}

func (m *clockAdvancingMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	m.clock.Advance(m.perNav)
	return &shipNav.NavigateRouteResponse{Status: "completed"}, nil
}

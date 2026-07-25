package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// The coordinator consumes the tracker through the domain port; the compile
// breaks here if the shapes ever drift apart.
var _ scouting.PressureReader = (*LimiterPressure)(nil)

// The EWMA rises when waits are observed: sustained contention pushes the
// smoothed value up toward the observed wait.
func TestLimiterPressure_RisesOnObserves(t *testing.T) {
	p := NewLimiterPressure(30 * time.Second)
	start := time.Now()

	p.Observe(100*time.Millisecond, start)
	first := p.Current(start)
	require.Greater(t, first, 90*time.Millisecond, "the first observation from silence sets the level")

	// Sustained equal waits keep it there (and never above the observed wait).
	at := start
	for i := 0; i < 10; i++ {
		at = at.Add(500 * time.Millisecond)
		p.Observe(100*time.Millisecond, at)
	}
	settled := p.Current(at)
	require.Greater(t, settled, 90*time.Millisecond)
	require.LessOrEqual(t, settled, 100*time.Millisecond)
}

// The half-life is honoured on the decay side: one silent half-life after a
// 100ms observation, the reading is ~50ms.
func TestLimiterPressure_HalfLifeDecay(t *testing.T) {
	halfLife := 30 * time.Second
	p := NewLimiterPressure(halfLife)
	start := time.Now()

	p.Observe(100*time.Millisecond, start)

	require.InDelta(t, float64(50*time.Millisecond), float64(p.Current(start.Add(halfLife))), float64(2*time.Millisecond),
		"one silent half-life halves the reading")
	require.InDelta(t, float64(25*time.Millisecond), float64(p.Current(start.Add(2*halfLife))), float64(2*time.Millisecond),
		"two silent half-lives quarter it")
}

// Observe BLENDS with the decayed carry — it never replaces the level with
// the latest wait. After settling at 100ms, a 0ms wait observed one half-life
// later reads ~50ms, not 0: the surviving half of the old level plus half of
// the new zero. A last-wait-wins implementation reads 0 here.
func TestLimiterPressure_ObserveBlendsCarry_DropToZero(t *testing.T) {
	halfLife := 30 * time.Second
	p := NewLimiterPressure(halfLife)
	start := time.Now()

	p.Observe(100*time.Millisecond, start)
	p.Observe(0, start.Add(halfLife))

	require.InDelta(t, float64(50*time.Millisecond), float64(p.Current(start.Add(halfLife))), float64(2*time.Millisecond),
		"one half-life of carry survives a zero observation — the EWMA blends, it does not replace")
}

// The blend also bounds spikes: one 10× wait at a small Δt barely moves the
// reading, so a single slow call can never flip the rotation. A last-wait-wins
// implementation jumps straight to the spike.
func TestLimiterPressure_ObserveBlendsCarry_SpikeBarelyMoves(t *testing.T) {
	p := NewLimiterPressure(30 * time.Second)
	start := time.Now()

	p.Observe(100*time.Millisecond, start)
	at := start.Add(1 * time.Second)
	p.Observe(1000*time.Millisecond, at)

	current := p.Current(at)
	require.Greater(t, current, 100*time.Millisecond, "the spike must register")
	require.Less(t, current, 200*time.Millisecond,
		"one spike at a 1s gap must move the smoothed reading only fractionally, never toward itself")
}

// SetHalfLife retunes the decay in place: a 2s half-life halves a 100ms level
// in 2s (the untouched default would still read ~95ms there), and a
// non-positive value falls back to the built-in default. Exercised through the
// client's config seam (SetLimiterPressureHalfLife) so the boot wiring path is
// the thing under test.
func TestSpaceTradersClient_SetLimiterPressureHalfLife_RetunesDecay(t *testing.T) {
	client := NewSpaceTradersClientWithConfig("http://unused", 0, time.Millisecond, nil)
	client.SetLimiterPressureHalfLife(2 * time.Second)

	tracker := client.LimiterPressure()
	start := time.Now()
	tracker.Observe(100*time.Millisecond, start)
	require.InDelta(t, float64(50*time.Millisecond), float64(tracker.Current(start.Add(2*time.Second))), float64(2*time.Millisecond),
		"a retuned 2s half-life must halve the level in 2s")

	// Non-positive selects the built-in default (30s), mirroring the constructor.
	fallback := NewSpaceTradersClientWithConfig("http://unused", 0, time.Millisecond, nil)
	fallback.SetLimiterPressureHalfLife(-5 * time.Second)
	tracker = fallback.LimiterPressure()
	start = time.Now()
	tracker.Observe(100*time.Millisecond, start)
	require.InDelta(t, float64(50*time.Millisecond), float64(tracker.Current(start.Add(30*time.Second))), float64(2*time.Millisecond),
		"a non-positive override must fall back to the 30s default")
}

// With no traffic the signal decays toward zero — a long-idle daemon reads no
// pressure, so scanning is never shed on stale history.
func TestLimiterPressure_DecaysTowardZeroWithSilence(t *testing.T) {
	p := NewLimiterPressure(30 * time.Second)
	start := time.Now()

	p.Observe(4*time.Second, start)

	require.Less(t, p.Current(start.Add(30*time.Minute)), 1*time.Millisecond,
		"an hour-scale silence reads as no pressure")
}

// Reads do not mutate the state: two reads at the same instant agree, and an
// early read does not bleed decay into a later one.
func TestLimiterPressure_ReadsArePure(t *testing.T) {
	p := NewLimiterPressure(30 * time.Second)
	start := time.Now()
	p.Observe(100*time.Millisecond, start)

	at := start.Add(15 * time.Second)
	first := p.Current(at)
	require.Equal(t, first, p.Current(at))

	// Reading earlier then later must give the same later value as reading
	// later alone would.
	require.InDelta(t, float64(50*time.Millisecond), float64(p.Current(start.Add(30*time.Second))), float64(2*time.Millisecond))
}

// A nil tracker is inert: observing records nothing and reading reports no
// pressure, so a zero-value client can never panic on the hot path.
func TestLimiterPressure_NilReceiverSafe(t *testing.T) {
	var p *LimiterPressure
	require.NotPanics(t, func() {
		p.Observe(time.Second, time.Now())
		require.Equal(t, time.Duration(0), p.Current(time.Now()))
	})
}

// The Observe hook is live at the request path's rate-limit wait site: forcing
// real limiter waits through the client leaves a non-zero pressure reading. A
// tracker never observed reads exactly zero, so this fails if the hook line is
// dropped. Metrics are deliberately NOT enabled — pressure must register even
// with no collector.
func TestSpaceTradersClient_RequestsFeedLimiterPressure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"symbol":"PROBE-1"}}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	// Tighten the limiter so the SECOND and THIRD calls genuinely queue
	// (~20ms each) instead of drawing on the production 30-token burst.
	client.rateLimiter = rate.NewLimiter(rate.Limit(50), 1)
	client.scheduler = newPriorityScheduler(client.rateLimiter.Wait, client.clock, defaultPriorityAgingWindow)

	for i := 0; i < 3; i++ {
		_, err := client.GetShip(context.Background(), "PROBE-1", "tok")
		require.NoError(t, err)
	}

	require.Greater(t, client.LimiterPressure().Current(time.Now()), time.Duration(0),
		"queued limiter waits must register as pressure")
}

// Parallel Observe/Current must be race-free (run under -race).
func TestLimiterPressure_ConcurrentObserveAndCurrent(t *testing.T) {
	p := NewLimiterPressure(time.Second)
	start := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				at := start.Add(time.Duration(g*200+i) * time.Millisecond)
				if g%2 == 0 {
					p.Observe(time.Duration(i)*time.Millisecond, at)
				} else {
					_ = p.Current(at)
				}
			}
		}(g)
	}
	wg.Wait()
	require.GreaterOrEqual(t, p.Current(start.Add(time.Hour)), time.Duration(0))
}

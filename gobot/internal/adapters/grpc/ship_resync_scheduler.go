package grpc

import (
	"context"
	"log"
	"math/rand"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

// ShipResyncScheduler periodically re-syncs the live (open-era) player's ships
// from the API into the local DB, so ship state cannot drift vs live API truth
// between the event-driven updates (sp-p1ci). It mirrors ShipStateScheduler's
// sweeper: a timer loop whose tick body runs under supervise.Guard
// (panic-isolated), and which halts promptly on ctx cancellation OR Stop().
//
// The resync core is injected as a callback so the daemon wires syncAllShips
// (syncs s.primaryPlayerID -> SyncAllFromAPI, the sp-bi75/sp-90a3
// dedicated_fleet-safe write path; sp-ig6x scoped it to the open-era player)
// while tests drive it with a fake callback, a short base interval, and a
// deterministic jitter seam. This is ordinary Go with real time; tests use a
// short injected interval rather than a mock clock.
type ShipResyncScheduler struct {
	resync    func(context.Context) error
	base      time.Duration
	jitter    time.Duration
	randFloat func() float64                           // [0,1) seam; rand.Float64 in prod, injected in tests
	logf      func(format string, args ...interface{}) // log sink seam; log.Printf in prod, injected in tests
	stopCh    chan struct{}
}

// NewShipResyncScheduler builds a scheduler that fires resync every base +/-
// jitter. The global math/rand source is auto-seeded (Go 1.20+), so each daemon
// process draws an independent jitter phase — that is the point of the jitter:
// a fleet of daemons cannot stack their ListShips bursts on the same wall-clock
// minute.
func NewShipResyncScheduler(resync func(context.Context) error, base, jitter time.Duration) *ShipResyncScheduler {
	return &ShipResyncScheduler{
		resync:    resync,
		base:      base,
		jitter:    jitter,
		randFloat: rand.Float64,
		logf:      log.Printf,
		stopCh:    make(chan struct{}),
	}
}

// nextDelay returns the wait before the next resync: base plus a random offset
// drawn uniformly from [-jitter, +jitter], floored at 0. Zero/negative jitter
// yields a fixed base cadence.
func (s *ShipResyncScheduler) nextDelay() time.Duration {
	if s.jitter <= 0 {
		return s.base
	}
	offset := time.Duration((2*s.randFloat() - 1) * float64(s.jitter))
	delay := s.base + offset
	if delay < 0 {
		return 0
	}
	return delay
}

// Run blocks, resyncing every base +/- jitter, until ctx is canceled or Stop()
// is called (returns nil in both cases — the supervise layer treats a nil
// return as a clean stop). The tick body runs under supervise.Guard so a panic
// in one resync pass is logged and the loop survives, rather than silently
// killing the daemon's only anti-drift safety net.
func (s *ShipResyncScheduler) Run(ctx context.Context) error {
	timer := time.NewTimer(s.nextDelay())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.stopCh:
			return nil
		case <-timer.C:
			supervise.Guard("ship-resync", func() {
				if err := s.resync(ctx); err != nil {
					s.logf("Periodic ship resync failed: %v", err)
				} else {
					// sp-ig6x (point b): a per-run success heartbeat so a healthy
					// loop is VISIBLE and a stalled one is diagnosable — previously
					// the loop logged ONLY on failure, so a success-less (or
					// non-ticking) loop was invisible. The resync core prints the
					// synced-count summary to stdout; this confirms the pass ran.
					s.logf("Periodic ship resync ok")
				}
			})
			timer.Reset(s.nextDelay())
		}
	}
}

// Stop halts Run. Called once (mirrors ShipStateScheduler.Stop); the daemon
// itself stops the loop via runCtx cancellation, so Stop is primarily the
// explicit test seam.
func (s *ShipResyncScheduler) Stop() {
	close(s.stopCh)
}

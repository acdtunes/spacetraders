// internal/infrastructure/supervise/supervisor.go
package supervise

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// stableUptime: a run that survived this long before failing resets the
	// backoff index — a component crashing once a day must not creep to the
	// max backoff.
	stableUptime = 5 * time.Minute
	// crashLoopWindow/Threshold: N crashes inside the window escalate to ONE
	// interrupt-class daemon.component_crashloop event, then the window
	// re-arms (edge-triggered — mirrors health.StreakTracker semantics, and the
	// watchkeeper's detectCrashLoops 3-in-30min shape for containers).
	crashLoopWindow    = 10 * time.Minute
	crashLoopThreshold = 5
)

// backoffSchedule mirrors container_runner.go's restartBackoffSchedule
// (sp-h0kr): an instantly-failing dependency must not burn restarts in
// milliseconds. Unlike containers there is NO restart cap — a permanently
// dead safety-net component (the arrival sweeper) is strictly worse than one
// retrying at 120s; loudness comes from the escalation event, not death.
var backoffSchedule = []time.Duration{5 * time.Second, 30 * time.Second, 120 * time.Second}

func backoffFor(restartsTaken int) time.Duration {
	if restartsTaken < 0 {
		restartsTaken = 0
	}
	if restartsTaken >= len(backoffSchedule) {
		return backoffSchedule[len(backoffSchedule)-1]
	}
	return backoffSchedule[restartsTaken]
}

// Option configures a Supervisor.
type Option func(*Supervisor)

// WithOnRestart installs a hook called (with the component name) on every
// restart — the metrics layer plugs in here so this package stays free of
// adapter imports.
func WithOnRestart(fn func(component string)) Option {
	return func(s *Supervisor) { s.onRestart = fn }
}

// Supervisor restarts registered background components on failure, with
// backoff and crash-loop escalation to the captain event outbox.
type Supervisor struct {
	rec       captain.EventRecorder // nil = no events (tests, minimal boots)
	playerID  int
	clock     shared.Clock
	onRestart func(string)
	wg        sync.WaitGroup
}

// New builds a Supervisor. rec may be nil (events skipped). clock nil =
// RealClock, matching the repo-wide constructor convention.
func New(rec captain.EventRecorder, playerID int, clock shared.Clock, opts ...Option) *Supervisor {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	s := &Supervisor{rec: rec, playerID: playerID, clock: clock}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Go spawns run under supervision. run should block until done or ctx is
// canceled. nil return = completed (no restart); error or panic = restart
// with backoff. Go returns immediately; use Wait for shutdown joins.
func (s *Supervisor) Go(ctx context.Context, component string, run func(context.Context) error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.supervise(ctx, component, run)
	}()
}

// Wait blocks until every supervised component has stopped (after their ctx
// is canceled). Called from the daemon's shutdown path.
func (s *Supervisor) Wait() { s.wg.Wait() }

func (s *Supervisor) supervise(ctx context.Context, component string, run func(context.Context) error) {
	consecutive := 0
	var crashTimes []time.Time
	for {
		started := s.clock.Now()
		err := runProtected(ctx, component, run)
		if ctx.Err() != nil {
			log.Printf("supervise: %s stopped (shutdown)", component)
			return
		}
		if err == nil {
			log.Printf("supervise: %s completed", component)
			return
		}
		now := s.clock.Now()
		if now.Sub(started) >= stableUptime {
			consecutive = 0
			crashTimes = crashTimes[:0]
		}
		consecutive++
		crashTimes = append(crashTimes, now)
		crashTimes = pruneBefore(crashTimes, now.Add(-crashLoopWindow))
		log.Printf("supervise: %s crashed (restart #%d in %s): %v",
			component, consecutive, backoffFor(consecutive-1), err)
		if s.onRestart != nil {
			s.onRestart(component)
		}
		if len(crashTimes) >= crashLoopThreshold {
			s.emitCrashLoop(component, len(crashTimes), err)
			crashTimes = crashTimes[:0] // re-arm: next escalation needs threshold more
		}
		if waitErr := sleepOrCancel(ctx, s.clock, backoffFor(consecutive-1)); waitErr != nil {
			log.Printf("supervise: %s backoff canceled (shutdown)", component)
			return
		}
	}
}

func runProtected(ctx context.Context, component string, run func(context.Context) error) (err error) {
	defer CapturePanic(&err, component)
	return run(ctx)
}

// emitCrashLoop records the interrupt-class escalation event. Uses its own
// short timeout instead of the (possibly shutdown-canceled) component ctx —
// same doctrine as recordErrorLoopEvent: an outbox failure must never take
// the supervisor down, only log.
func (s *Supervisor) emitCrashLoop(component string, crashesInWindow int, cause error) {
	if s.rec == nil {
		return
	}
	payload, jerr := json.Marshal(map[string]any{
		"component":         component,
		"crashes_in_window": crashesInWindow,
		"window_seconds":    int(crashLoopWindow.Seconds()),
		"error":             cause.Error(),
	})
	if jerr != nil {
		payload = []byte("{}")
	}
	evCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.rec.Record(evCtx, &captain.Event{
		Type:     captain.EventDaemonComponentCrashLoop,
		Ship:     "daemon:" + component, // container-scoped convention (see health.NewErrorLoopEvent)
		PlayerID: s.playerID,
		Payload:  string(payload),
	}); err != nil {
		log.Printf("supervise: %s crash-loop event emission failed: %v", component, err)
	}
}

func pruneBefore(times []time.Time, cutoff time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if !t.Before(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

// sleepOrCancel is the same clock-injected, ctx-interruptible wait as
// ContainerRunner.sleepOrCancel (container_runner.go:1084) — instant under
// MockClock, interruptible under RealClock. The helper goroutine leaking
// until its Sleep elapses on cancellation is the same accepted tradeoff.
func sleepOrCancel(ctx context.Context, clock shared.Clock, d time.Duration) error {
	slept := make(chan struct{})
	go func() {
		clock.Sleep(d)
		close(slept)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-slept:
		return nil
	}
}

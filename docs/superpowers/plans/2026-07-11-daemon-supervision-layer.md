# Daemon Supervision Layer Implementation Plan (sp-i01z)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** No panic anywhere in the daemon can kill the process; every supervised background loop restarts with backoff and escalates crash-loops as an interrupt-class captain event; the sp-e2l1 error-streak primitive becomes a shared package any coordinator can adopt.

**Architecture:** The daemon already has three partial resilience layers — `ContainerRunner` restart-with-backoff (`internal/adapters/grpc/container_runner.go`), the watchkeeper's cross-container crash-loop detector (`internal/captain/detectors.go:789`), and the contract-coordinator-only error-streak tracker (`internal/application/contract/commands/error_streak.go`). This plan does NOT replace them. It adds the two missing pieces — panic isolation (today a panic in any coordinator or timer callback kills the whole daemon; the only `recover()` in production code is in `navigate_route.go:228`) and supervision for the boot-time bare goroutines (ship-state sweeper, container recovery) — as a new `internal/infrastructure/supervise` package, and extracts the streak primitive to `internal/application/health` so the other ~10 coordinators can adopt it (rollout bead sp-6wxq).

**Tech Stack:** Go 1.24, stdlib + existing deps only (no suture — the restart machinery already exists in-repo; adding a library would create a FOURTH layer). `shared.Clock` for testable time, `captain.EventRecorder` for events, prometheus via the existing `internal/adapters/metrics` global-shim pattern.

## Global Constraints

- Module: `github.com/andrescamacho/spacetraders-go`, Go 1.24. Repo root for all paths below: `gobot/`.
- Prefix every shell command with `rtk` (e.g. `rtk go test ./...`).
- No new external dependencies. No new config section (thresholds are package constants, matching `coordinatorErrorStreakThreshold`'s precedent — avoids the dormant-config-consumer hazard).
- Tests: colocated `_test.go`, plain `testing` + `testify/require`, `shared.MockClock` for time (do NOT run the MockClock-based supervisor tests with `-race`; `MockClock` is intentionally unsynchronized, same as the existing `container_runner_restart_backoff_test.go` idiom).
- Commit messages follow repo convention and reference the bead: e.g. `feat(daemon): supervise package core (sp-i01z)`.
- Use `bd` for tracking (this work is bead sp-i01z); do NOT use TodoWrite/TaskCreate.
- Preserve existing doc comments verbatim when moving code (they carry incident IDs like sp-e2l1).

## Failure-mode map this plan closes

| Failure | Today | After |
|---|---|---|
| Panic in a coordinator/worker iteration | kills the entire daemon process | converted to error → existing restart/backoff/terminalize machinery |
| Panic in `runHeartbeat` | kills the daemon | logged + suppressed (heartbeat loss is detected by the watchkeeper's `container.heartbeat_lost`) |
| Panic in a `time.AfterFunc` arrival/cooldown callback | kills the daemon | logged + suppressed (sweeper is the safety net) |
| Ship-state sweeper goroutine dies (panic) | arrivals silently stop being swept, forever | supervisor restarts it with 5s/30s/120s backoff; ≥5 crashes in 10min → interrupt event `daemon.component_crashloop` |
| Container recovery goroutine panics at boot | kills the daemon during startup | logged + suppressed, daemon boots degraded and loudly |
| Coordinator loop errors silently forever (s88 class) | only the contract coordinator detects it | streak primitive extracted to `internal/application/health`; rollout to other coordinators = bead sp-6wxq |

## File Structure

- Create: `internal/infrastructure/supervise/panic.go` — `PanicError`, `CapturePanic`, `Guard`
- Create: `internal/infrastructure/supervise/panic_test.go`
- Create: `internal/infrastructure/supervise/supervisor.go` — `Supervisor`, `New`, `Go`, `Wait`, backoff/crash-loop constants
- Create: `internal/infrastructure/supervise/supervisor_test.go`
- Create: `internal/application/health/streak.go` — moved streak primitive (exported)
- Create: `internal/application/health/streak_test.go` — moved tests
- Create: `internal/application/contract/commands/quarantine.go` — hull-quarantine builders stay in contract
- Modify: `internal/domain/captain/events.go` — new `EventDaemonComponentCrashLoop` + `DefaultInterruptTypes` entry
- Modify: `internal/adapters/metrics/prometheus_collector.go` — `DaemonComponentRecorder` + global shim
- Modify: `internal/adapters/metrics/container_metrics.go` — `daemon_component_restarts_total` counter
- Modify: `internal/adapters/grpc/container_runner.go:436` — panic barrier around `executeIteration`; `:221` heartbeat guard
- Modify: `internal/adapters/grpc/ship_state_scheduler.go` — `RunSweeper(ctx)` replaces `StartBackgroundSweeper()`; AfterFunc guards at `:76` and `:151`
- Modify: `internal/adapters/grpc/daemon_server.go` — `runCtx`/`runCancel`/`sup` fields, supervised spawns, shutdown ordering
- Modify: `internal/application/contract/commands/run_fleet_coordinator.go:246,305,311,411,417,621,627,684,690,1205` — re-point to `health` package
- Delete: `internal/application/contract/commands/error_streak.go` and `error_streak_test.go` (contents move)

---

### Task 1: `supervise` package core — `PanicError`, `CapturePanic`, `Guard`

**Files:**
- Create: `internal/infrastructure/supervise/panic.go`
- Test: `internal/infrastructure/supervise/panic_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only)
- Produces: `type PanicError struct{ Component string; Value any; Stack []byte }` with `Error() string`; `func CapturePanic(errp *error, component string)` (deferred; converts an in-flight panic into `*errp`); `func Guard(component string, fn func())` (runs fn, suppresses+logs a panic; for fire-and-forget callbacks). Tasks 2, 4, 5, 6 rely on these exact names.

- [ ] **Step 1: Write the failing test**

```go
// internal/infrastructure/supervise/panic_test.go
package supervise

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// CapturePanic converts an in-flight panic into an error carrying the
// component name, the panic value, and the stack — so the existing
// error-path restart machinery (ContainerRunner.handleError, Supervisor)
// can treat a panic like any other failure instead of the process dying.
func TestCapturePanic_ConvertsPanicToError(t *testing.T) {
	run := func() (err error) {
		defer CapturePanic(&err, "test-component")
		panic("boom")
	}
	err := run()
	require.Error(t, err)
	var perr *PanicError
	require.ErrorAs(t, err, &perr)
	require.Equal(t, "test-component", perr.Component)
	require.Equal(t, "boom", fmt.Sprintf("%v", perr.Value))
	require.NotEmpty(t, perr.Stack, "stack must be captured for diagnosis")
	require.True(t, strings.Contains(err.Error(), "test-component"))
	require.True(t, strings.Contains(err.Error(), "boom"))
}

// A normal return (nil or a real error) must pass through untouched:
// CapturePanic only acts when recover() is non-nil.
func TestCapturePanic_NoPanicLeavesErrorUntouched(t *testing.T) {
	sentinel := errors.New("real failure")
	run := func() (err error) {
		defer CapturePanic(&err, "test-component")
		return sentinel
	}
	require.ErrorIs(t, run(), sentinel)

	runNil := func() (err error) {
		defer CapturePanic(&err, "test-component")
		return nil
	}
	require.NoError(t, runNil())
}

// Guard is for fire-and-forget callbacks (time.AfterFunc, boot goroutines)
// where there is no error channel: the panic is logged and suppressed, and
// the process survives.
func TestGuard_SuppressesPanic(t *testing.T) {
	require.NotPanics(t, func() {
		Guard("test-callback", func() { panic("boom") })
	})
}

func TestGuard_RunsFnNormally(t *testing.T) {
	ran := false
	Guard("test-callback", func() { ran = true })
	require.True(t, ran)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./internal/infrastructure/supervise/ -run 'TestCapturePanic|TestGuard' -v`
Expected: FAIL (package does not exist / undefined: CapturePanic)

- [ ] **Step 3: Write minimal implementation**

```go
// internal/infrastructure/supervise/panic.go
// Package supervise provides panic isolation and restart supervision for the
// daemon's long-running background components. It complements — not replaces —
// the ContainerRunner restart machinery (containers) and the watchkeeper's
// crash-loop detector (cross-container): supervise covers the pieces that had
// NOTHING before it — boot-time goroutines and panic containment (sp-i01z).
// Before this package, the only recover() in production code was route
// execution (navigate_route.go); a panic in any coordinator iteration, timer
// callback, or boot loop killed the entire daemon process.
package supervise

import (
	"fmt"
	"log"
	"runtime/debug"
)

// PanicError is a recovered panic converted into an error so it can flow
// through ordinary error-handling (ContainerRunner.handleError, Supervisor
// restart). Stack is captured at recovery time for diagnosis.
type PanicError struct {
	Component string
	Value     any
	Stack     []byte
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("panic in %s: %v", e.Component, e.Value)
}

// CapturePanic is deferred inside a function with a named error return:
//
//	func run() (err error) {
//		defer supervise.CapturePanic(&err, "my-component")
//		...
//	}
//
// If the function panics, the panic is converted into a *PanicError assigned
// to *errp (and the stack is logged immediately, since callers usually log
// only err.Error()). If the function returns normally, *errp is untouched.
func CapturePanic(errp *error, component string) {
	if r := recover(); r != nil {
		perr := &PanicError{Component: component, Value: r, Stack: debug.Stack()}
		log.Printf("supervise: %s\n%s", perr.Error(), perr.Stack)
		*errp = perr
	}
}

// Guard runs fn and suppresses (but loudly logs) a panic. For fire-and-forget
// callbacks with no error channel — time.AfterFunc bodies, one-shot boot
// goroutines — where "log and survive" is the only sane recovery.
func Guard(component string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("supervise: panic in %s (suppressed): %v\n%s", component, r, debug.Stack())
		}
	}()
	fn()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `rtk go test ./internal/infrastructure/supervise/ -run 'TestCapturePanic|TestGuard' -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
rtk git add internal/infrastructure/supervise/
rtk git commit -m "feat(daemon): supervise package core — PanicError, CapturePanic, Guard (sp-i01z)"
```

---

### Task 2: `Supervisor.Go` — restart with backoff + crash-loop escalation event

**Files:**
- Create: `internal/infrastructure/supervise/supervisor.go`
- Test: `internal/infrastructure/supervise/supervisor_test.go`
- Modify: `internal/domain/captain/events.go` (new event type + interrupt registration)

**Interfaces:**
- Consumes: `CapturePanic` (Task 1); `shared.Clock` (`internal/domain/shared/clock.go` — `Now()`, `Sleep(d)`); `captain.EventRecorder` (`internal/domain/captain/events.go` — `Record(ctx, *Event) error`)
- Produces: `func New(rec captain.EventRecorder, playerID int, clock shared.Clock, opts ...Option) *Supervisor`; `func (s *Supervisor) Go(ctx context.Context, component string, run func(context.Context) error)`; `func (s *Supervisor) Wait()`; `func WithOnRestart(fn func(component string)) Option`; `captain.EventDaemonComponentCrashLoop EventType = "daemon.component_crashloop"`. Task 6 wires `Go`/`Wait`; Task 3's metric hook plugs into `WithOnRestart`.

Semantics (mirror the container layer where they overlap):
- `run` returns nil → component completed; log INFO; no restart (one-shot components).
- ctx canceled → clean stop, no restart.
- `run` returns error or panics → log ERROR, `onRestart` hook, backoff `[5s, 30s, 120s, 120s, ...]` (same shape/constants as `restartBackoffSchedule`, container_runner.go:54), restart. Never gives up — a permanently-dead sweeper is worse than a hot-looping one at 120s cadence; loudness comes from the escalation event.
- A run that stayed up ≥ 5min before failing resets the backoff index (a component that crashes once a day must not creep to 120s waits).
- ≥ 5 crashes within a 10min sliding window → emit ONE interrupt-class `daemon.component_crashloop` event, then clear the window (re-arms: the NEXT 5 crashes emit again — edge-triggered like `errorStreakTracker.Note`, never one-event-per-crash; sp-6g96 event-spam sensitivity).
- No per-crash captain event (crashes are logged + counted in metrics; the watchkeeper-visible signal is the LOOP, mirroring how `container.crashed` is deferred-class while `container.crashloop` interrupts).

- [ ] **Step 1: Write the failing test**

```go
// internal/infrastructure/supervise/supervisor_test.go
package supervise

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type fakeRecorder struct {
	mu     sync.Mutex
	events []*captain.Event
}

func (f *fakeRecorder) Record(_ context.Context, e *captain.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}

func (f *fakeRecorder) byType(t captain.EventType) []*captain.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*captain.Event
	for _, e := range f.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// waitForCalls drains the invocation-signal channel until n calls were seen
// or the deadline passes. The supervised fn signals each invocation; this is
// the only cross-goroutine synchronization the tests use.
func waitForCalls(t *testing.T, calls <-chan struct{}, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-calls:
		case <-deadline:
			t.Fatalf("timed out waiting for invocation %d/%d", i+1, n)
		}
	}
}

// A component whose run fn returns an error is restarted (with the clock-
// injected backoff), and a clean nil return ends supervision without restart.
func TestSupervisor_RestartsOnErrorThenStopsOnCleanExit(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)}
	rec := &fakeRecorder{}
	var restarts []string
	sup := New(rec, 7, clock, WithOnRestart(func(c string) { restarts = append(restarts, c) }))

	calls := make(chan struct{}, 16)
	invocation := 0
	sup.Go(context.Background(), "sweeper", func(ctx context.Context) error {
		invocation++
		calls <- struct{}{}
		if invocation <= 2 {
			return errors.New("db timeout")
		}
		return nil // third run completes cleanly
	})

	waitForCalls(t, calls, 3)
	sup.Wait()

	require.Equal(t, []string{"sweeper", "sweeper"}, restarts, "two failures → two restarts, clean exit → none")
	require.Empty(t, rec.byType(captain.EventDaemonComponentCrashLoop), "2 crashes is below the loop threshold")
}

// Context cancellation stops supervision without a restart, both when the
// run fn returns because of it and during a backoff wait.
func TestSupervisor_CtxCancelStopsSupervision(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)}
	sup := New(&fakeRecorder{}, 7, clock)

	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan struct{}, 1)
	sup.Go(ctx, "blocker", func(ctx context.Context) error {
		calls <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	waitForCalls(t, calls, 1)
	cancel()
	sup.Wait() // must return; a hang here fails via go test timeout
}

// A panicking component is restarted exactly like an erroring one — the
// panic is converted by CapturePanic, never propagated to the process.
func TestSupervisor_PanicIsConvertedAndRestarted(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)}
	var restarts int
	sup := New(&fakeRecorder{}, 7, clock, WithOnRestart(func(string) { restarts++ }))

	calls := make(chan struct{}, 16)
	invocation := 0
	sup.Go(context.Background(), "panicky", func(ctx context.Context) error {
		invocation++
		calls <- struct{}{}
		if invocation == 1 {
			panic("nil map write")
		}
		return nil
	})
	waitForCalls(t, calls, 2)
	sup.Wait()
	require.Equal(t, 1, restarts)
}

// Crash-loop escalation: the 5th crash inside the window emits ONE
// interrupt-class daemon.component_crashloop event, the window re-arms, and
// the 10th crash emits the second — edge-triggered, never per-crash
// (sp-6g96: event spam saturates the wake cap).
func TestSupervisor_CrashLoopEmitsEdgeTriggeredInterrupt(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)}
	rec := &fakeRecorder{}
	sup := New(rec, 7, clock)

	calls := make(chan struct{}, 32)
	invocation := 0
	sup.Go(context.Background(), "sweeper", func(ctx context.Context) error {
		invocation++
		calls <- struct{}{}
		if invocation <= 10 {
			return errors.New("boom")
		}
		return nil
	})
	waitForCalls(t, calls, 11)
	sup.Wait()

	loops := rec.byType(captain.EventDaemonComponentCrashLoop)
	require.Len(t, loops, 2, "5th and 10th crash each cross the threshold once")
	require.Equal(t, "daemon:sweeper", loops[0].Ship, "container-scoped Ship convention, like coordinator.error_loop")
	require.Equal(t, 7, loops[0].PlayerID)
	require.Contains(t, loops[0].Payload, `"component":"sweeper"`)
	require.Contains(t, loops[0].Payload, "boom")
}

// The new event type must be interrupt class: a crash-looping safety-net
// component (the arrival sweeper) silently degrades the whole fleet, exactly
// the class coordinator.error_loop exists for.
func TestDaemonComponentCrashLoopIsInterruptClass(t *testing.T) {
	require.Contains(t, captain.DefaultInterruptTypes(), captain.EventDaemonComponentCrashLoop)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./internal/infrastructure/supervise/ -run TestSupervisor -v`
Expected: FAIL (undefined: New, captain.EventDaemonComponentCrashLoop)

- [ ] **Step 3: Add the event type**

In `internal/domain/captain/events.go`, inside the `const (...)` block, immediately after the `EventCoordinatorErrorLoop` declaration (line ~69), add:

```go
	// EventDaemonComponentCrashLoop fires when a supervised daemon background
	// component (ship-state sweeper, container recovery, samplers — NOT
	// containers, which have container.crashloop) has crashed and been
	// restarted crashLoopThreshold times within crashLoopWindow (see
	// internal/infrastructure/supervise). Interrupt class for the same reason
	// coordinator.error_loop is: a safety-net component dying in a loop (the
	// arrival sweeper) silently degrades the whole fleet — it must wake the
	// captain, not ride the next cadence. Edge-triggered once per window,
	// never per-crash (sp-6g96 event-spam doctrine).
	EventDaemonComponentCrashLoop EventType = "daemon.component_crashloop"
```

In `DefaultInterruptTypes()` (line ~127), add `EventDaemonComponentCrashLoop,` immediately after the `EventContainerCrashLoop,` entry.

- [ ] **Step 4: Write the Supervisor implementation**

```go
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
	// re-arms (edge-triggered — mirrors errorStreakTracker semantics, and the
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
		Ship:     "daemon:" + component, // container-scoped convention (see buildErrorLoopEvent)
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
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `rtk go test ./internal/infrastructure/supervise/ -v`
Expected: PASS (all Task 1 + Task 2 tests)

- [ ] **Step 6: Run the captain domain tests (event type addition must not break anything)**

Run: `rtk go test ./internal/domain/captain/ ./internal/captain/`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
rtk git add internal/infrastructure/supervise/ internal/domain/captain/events.go
rtk git commit -m "feat(daemon): Supervisor.Go restart/backoff + interrupt-class daemon.component_crashloop (sp-i01z)"
```

---

### Task 3: `daemon_component_restarts_total` metric

**Files:**
- Modify: `internal/adapters/metrics/prometheus_collector.go` (after the `RecordContainerExit` shim, line ~170)
- Modify: `internal/adapters/metrics/container_metrics.go` (constructor field-init list ends line ~155; `Register()` metric slice at line ~163)
- Test: `internal/adapters/metrics/daemon_component_metrics_test.go`

**Interfaces:**
- Consumes: existing `globalCollector`, `Registry`, `namespace` const, `ContainerMetricsCollector`
- Produces: `metrics.RecordDaemonComponentRestart(component string)` (package-level shim; Task 6 wires it into `supervise.WithOnRestart`). Metric: `spacetraders_daemon_component_restarts_total{component}`.

Design note: the shim type-asserts a NEW single-method interface instead of widening `MetricsRecorder`, so existing fakes/implementations of `MetricsRecorder` keep compiling.

- [ ] **Step 1: Write the failing test**

```go
// internal/adapters/metrics/daemon_component_metrics_test.go
package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// The supervise layer reports restarts through the same global-shim pattern
// as RecordContainerRestart; the counter is labeled by component only (a
// small fixed set — no cardinality risk).
func TestRecordDaemonComponentRestart_IncrementsCounter(t *testing.T) {
	InitRegistry()
	defer func() { Registry = nil; globalCollector = nil }()

	collector := NewContainerMetricsCollector(nil)
	require.NoError(t, collector.Register())
	SetGlobalCollector(collector)

	RecordDaemonComponentRestart("ship-state-sweeper")
	RecordDaemonComponentRestart("ship-state-sweeper")

	count := testutil.ToFloat64(collector.daemonComponentRestarts.WithLabelValues("ship-state-sweeper"))
	require.Equal(t, 2.0, count)
}

// With no collector installed the shim is a no-op, never a nil-deref — the
// supervise layer must work in metrics-disabled boots.
func TestRecordDaemonComponentRestart_NoCollectorIsNoop(t *testing.T) {
	prev := globalCollector
	globalCollector = nil
	defer func() { globalCollector = prev }()
	require.NotPanics(t, func() { RecordDaemonComponentRestart("x") })
}

func TestDaemonComponentRestartMetricName(t *testing.T) {
	InitRegistry()
	defer func() { Registry = nil }()
	collector := NewContainerMetricsCollector(nil)
	require.NoError(t, collector.Register())
	collector.RecordDaemonComponentRestart("recovery")

	families, err := Registry.Gather()
	require.NoError(t, err)
	found := false
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "spacetraders_daemon_component_restarts_total") {
			found = true
		}
	}
	require.True(t, found, "metric must be spacetraders_daemon_component_restarts_total")
}
```

Note: `NewContainerMetricsCollector`'s exact parameter list is at `container_metrics.go` (constructor starts ~line 60) — adjust the `NewContainerMetricsCollector(nil)` call in this test to pass the constructor's actual zero arguments (e.g. a nil container-info getter) after reading it; the assertions stay as written.

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./internal/adapters/metrics/ -run TestRecordDaemonComponent -v`
Expected: FAIL (undefined: RecordDaemonComponentRestart)

- [ ] **Step 3: Implement**

In `prometheus_collector.go`, after the `RecordContainerExit` shim:

```go
// DaemonComponentRecorder is implemented by collectors that track supervised
// daemon background components (sp-i01z). A separate single-method interface
// (instead of widening MetricsRecorder) so existing MetricsRecorder
// implementations and test fakes keep compiling.
type DaemonComponentRecorder interface {
	RecordDaemonComponentRestart(component string)
}

// RecordDaemonComponentRestart records a supervised daemon component restart
// globally. Wired into supervise.WithOnRestart at daemon boot.
func RecordDaemonComponentRestart(component string) {
	if globalCollector == nil {
		return
	}
	if rec, ok := globalCollector.(DaemonComponentRecorder); ok {
		rec.RecordDaemonComponentRestart(component)
	}
}
```

In `container_metrics.go`:
1. Add the struct field next to `containerRestarts`: `daemonComponentRestarts *prometheus.CounterVec`.
2. In the constructor's field-init list (after the `containerExitTotal` block, ~line 132):

```go
		// Supervised daemon background component restarts (sp-i01z). Labeled
		// by component only — a small fixed set (ship-state-sweeper,
		// container-recovery, ...), deliberately NOT per-ship.
		daemonComponentRestarts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "daemon",
				Name:      "component_restarts_total",
				Help:      "Restarts of supervised daemon background components",
			},
			[]string{"component"},
		),
```

3. Add `c.daemonComponentRestarts,` to the `metrics := []prometheus.Collector{...}` slice in `Register()`.
4. Add the method:

```go
// RecordDaemonComponentRestart implements DaemonComponentRecorder (sp-i01z).
func (c *ContainerMetricsCollector) RecordDaemonComponentRestart(component string) {
	c.daemonComponentRestarts.WithLabelValues(component).Inc()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `rtk go test ./internal/adapters/metrics/ -v`
Expected: PASS (new tests + all existing metrics tests)

- [ ] **Step 5: Commit**

```bash
rtk git add internal/adapters/metrics/
rtk git commit -m "feat(daemon): spacetraders_daemon_component_restarts_total metric + type-asserted shim (sp-i01z)"
```

---

### Task 4: Panic barrier in `ContainerRunner`

**Files:**
- Modify: `internal/adapters/grpc/container_runner.go` (`:221` heartbeat spawn, `:436` iteration call; add `runIterationProtected` next to `executeIteration` at `:695`)
- Test: `internal/adapters/grpc/container_runner_panic_test.go`

**Interfaces:**
- Consumes: `supervise.CapturePanic`, `supervise.Guard` (Task 1); existing test harness `newCrashTestRunner` (`container_runner_crash_test.go:36`), `fakeRecorder` (`captain_recorder_test.go:12`), `SetCaptainEventRecorder`
- Produces: behavior only — a panic inside any command handler now flows into `handleError` → restart/backoff → (after `MaxRestartAttempts=3`) terminal FAILED + `container.crashed` + `workflow.failed`, exactly like a returned error. No API change.

Today a panic inside `executeIteration` (any coordinator/worker `Handle`) unwinds the bare `go r.execute()` goroutine and **kills the entire daemon** — main.go's own comments call out nil-derefs that "crash the whole daemon".

- [ ] **Step 1: Write the failing test**

```go
// internal/adapters/grpc/container_runner_panic_test.go
package grpc

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

// A panic inside an iteration (here: the nil-mediator deref that
// newCrashTestRunner sets up) must be converted into an error and returned —
// never propagated up the runner goroutine, where it would kill the entire
// daemon process (sp-i01z; before this barrier the ONLY recover() in
// production code was route execution).
func TestRunIterationProtected_ConvertsHandlerPanicToError(t *testing.T) {
	r := newCrashTestRunner(t, "contract-work-TORWIND-3-panic")

	var err error
	require.NotPanics(t, func() { err = r.runIterationProtected() })
	require.Error(t, err)

	var perr *supervise.PanicError
	require.ErrorAs(t, err, &perr, "panic must surface as supervise.PanicError so handleError logs it with the container id")
	require.Contains(t, perr.Component, "contract-work-TORWIND-3-panic")
}

// The barrier must not disturb the normal error path: an iteration that
// RETURNS an error still returns that error unchanged.
func TestRunIterationProtected_PassesThroughOrdinaryErrors(t *testing.T) {
	// mediator that returns an error without panicking
	r := newCrashTestRunner(t, "contract-work-TORWIND-3-err")
	r.mediator = errMediator{err: errors.New("API 4203 insufficient fuel")}

	err := r.runIterationProtected()
	require.Error(t, err)
	var perr *supervise.PanicError
	require.False(t, errors.As(err, &perr), "ordinary errors must not be wrapped as panics")
}
```

Add next to the test (same file), an error-returning mediator fake by embedding the interface (embedding keeps this compiling even if the Mediator interface grows):

```go
import commonMediator "github.com/andrescamacho/spacetraders-go/internal/application/mediator"

type errMediator struct {
	commonMediator.Mediator
	err error
}

func (m errMediator) Send(_ context.Context, _ commonMediator.Request) (commonMediator.Response, error) {
	return nil, m.err
}
```

(Confirm the `Request`/`Response` type names against `internal/application/mediator/mediator.go:10` — `Send(ctx context.Context, request Request) (Response, error)` — and that `ContainerRunner.mediator` is `common.Mediator`, an alias of this interface; adjust the import alias accordingly.)

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./internal/adapters/grpc/ -run TestRunIterationProtected -v`
Expected: FAIL — `TestRunIterationProtected_ConvertsHandlerPanicToError` PANICS (this is the bug being fixed; the `require.NotPanics` demonstrates it) and `runIterationProtected` is undefined.

- [ ] **Step 3: Implement**

In `container_runner.go`, add import `"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"`.

Add immediately above `executeIteration` (line ~695):

```go
// runIterationProtected wraps executeIteration in a panic barrier (sp-i01z):
// a panic inside any command handler is converted to an error so the restart
// machinery below handles it exactly like a returned error — before this,
// one nil-deref in one coordinator killed the entire daemon process.
func (r *ContainerRunner) runIterationProtected() (err error) {
	defer supervise.CapturePanic(&err, "container:"+r.containerEntity.ID())
	return r.executeIteration()
}
```

In `execute()` (line 436), change:

```go
		if err := r.executeIteration(); err != nil {
```

to:

```go
		if err := r.runIterationProtected(); err != nil {
```

In `Start()` (line 221), change `go r.runHeartbeat()` to:

```go
	go supervise.Guard("container-heartbeat:"+r.containerEntity.ID(), r.runHeartbeat)
```

(a dead heartbeat is already detected by the watchkeeper's `container.heartbeat_lost`; suppress-and-log is the correct recovery — the container itself keeps running.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `rtk go test ./internal/adapters/grpc/ -run 'TestRunIterationProtected' -v`
Expected: PASS
Then the whole package (the runner has 8 existing test files that must stay green):
Run: `rtk go test ./internal/adapters/grpc/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
rtk git add internal/adapters/grpc/container_runner.go internal/adapters/grpc/container_runner_panic_test.go
rtk git commit -m "feat(daemon): panic barrier in ContainerRunner — a handler panic restarts the container, not the daemon (sp-i01z)"
```

---

### Task 5: Guard the `ShipStateScheduler` timer callbacks

**Files:**
- Modify: `internal/adapters/grpc/ship_state_scheduler.go` (`ScheduleArrival` AfterFunc at line ~76; `ScheduleCooldownClear` AfterFunc at line ~151)
- Test: `internal/adapters/grpc/ship_state_scheduler_guard_test.go`

**Interfaces:**
- Consumes: `supervise.Guard` (Task 1)
- Produces: behavior only. `time.AfterFunc` callbacks run on runtime timer goroutines — a panic in `handleArrival`/`handleCooldownClear` kills the daemon today.

- [ ] **Step 1: Write the failing test**

```go
// internal/adapters/grpc/ship_state_scheduler_guard_test.go
package grpc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// A panic inside a timer callback must not escape the timer goroutine (it
// would kill the daemon). We force the panic with a nil shipRepo: the
// callback's FindBySymbol nil-derefs. The assertion is simply that the test
// PROCESS survives past the timer firing; without the Guard this test
// crashes the whole `go test` run.
func TestArrivalTimerPanicDoesNotKillProcess(t *testing.T) {
	s := NewShipStateScheduler(nil /* shipRepo: nil-deref on fire */, &shared.RealClock{}, nil)

	arrival := time.Now().Add(1 * time.Millisecond)
	ship := reconstructTestShipInTransit(t, "TORWIND-9", 7, arrival)
	s.ScheduleArrival(ship)

	time.Sleep(150*time.Millisecond + ClockDriftBuffer)
	require.True(t, true, "process survived a panicking timer callback")
}
```

Add the small entity helper (same file) using the domain constructor that the scheduler tests need — `navigation.ReconstructShip` (signature at `internal/domain/navigation/ship.go:810`). Build the minimal IN_TRANSIT ship: read the `ReconstructShip` parameter list and fill symbol `TORWIND-9`, player 7, `NavStatusInTransit`, an arrival pointer of `arrival`, and zero/default values for the rest; that is the only shape this test needs. If a smaller test-scoped ship builder already exists in package `grpc` tests, reuse it instead.

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./internal/adapters/grpc/ -run TestArrivalTimerPanic -v`
Expected: the test binary CRASHES with the nil-deref panic (exit code 2) — demonstrating the daemon-killing behavior.

- [ ] **Step 3: Implement**

In `ship_state_scheduler.go`, add import `"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"`.

`ScheduleArrival` (line ~76):

```go
	s.timers[timerKey] = time.AfterFunc(delay, func() {
		supervise.Guard("ship-arrival-timer", func() {
			s.handleArrival(symbol, playerID)
		})
	})
```

`ScheduleCooldownClear` (line ~151):

```go
	s.timers[timerKey] = time.AfterFunc(delay, func() {
		supervise.Guard("ship-cooldown-timer", func() {
			s.handleCooldownClear(symbol, playerID, timerKey)
		})
	})
```

(Component names deliberately exclude the ship symbol — they flow into logs; keep them a fixed set.)

- [ ] **Step 4: Run test to verify it passes**

Run: `rtk go test ./internal/adapters/grpc/ -run TestArrivalTimerPanic -v`
Expected: PASS (process survives; the suppressed panic is visible in the log output)

- [ ] **Step 5: Commit**

```bash
rtk git add internal/adapters/grpc/ship_state_scheduler.go internal/adapters/grpc/ship_state_scheduler_guard_test.go
rtk git commit -m "feat(daemon): guard ship-state timer callbacks — a callback panic no longer kills the daemon (sp-i01z)"
```

---

### Task 6: Supervise the sweeper + container recovery; wire the Supervisor into the daemon

**Files:**
- Modify: `internal/adapters/grpc/ship_state_scheduler.go` (replace `StartBackgroundSweeper` at line 256 with blocking `RunSweeper(ctx)`)
- Modify: `internal/adapters/grpc/daemon_server.go` (fields at line ~135; `Start()` at line 483; sweeper spawn at line 527; recovery goroutine at line ~568; `handleShutdown()` at line 681)
- Modify: `internal/adapters/grpc/captain_recorder.go` (add package-internal getter)
- Test: `internal/adapters/grpc/ship_state_scheduler_sweeper_test.go`

**Interfaces:**
- Consumes: `supervise.New/Go/Wait/WithOnRestart/Guard` (Tasks 1-2), `metrics.RecordDaemonComponentRestart` (Task 3)
- Produces: `func (s *ShipStateScheduler) RunSweeper(ctx context.Context) error` (blocking; replaces `StartBackgroundSweeper()`; Feature 2's plan assumes this shape). `DaemonServer` fields `runCtx context.Context`, `runCancel context.CancelFunc`, `sup *supervise.Supervisor`.

- [ ] **Step 1: Write the failing test for RunSweeper**

```go
// internal/adapters/grpc/ship_state_scheduler_sweeper_test.go
package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RunSweeper is the supervised replacement for StartBackgroundSweeper: it
// BLOCKS, sweeping every SweeperInterval, and returns nil promptly when its
// context is canceled — the supervise layer treats that as a clean stop.
// (A panic inside a sweep pass propagates to the Supervisor, which restarts
// the sweeper with backoff — that is the sp-i01z point: the old bare
// goroutine died silently and arrivals stopped being swept forever.)
func TestRunSweeper_BlocksUntilCtxCancelThenReturnsNil(t *testing.T) {
	s := NewShipStateScheduler(nil, &shared.RealClock{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.RunSweeper(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("RunSweeper returned before cancel: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RunSweeper did not stop on ctx cancel")
	}
}

// Legacy Stop() must also stop RunSweeper (handleShutdown calls Stop for the
// timers; the sweeper honors both signals).
func TestRunSweeper_StopChAlsoStops(t *testing.T) {
	s := NewShipStateScheduler(nil, &shared.RealClock{}, nil)
	done := make(chan error, 1)
	go func() { done <- s.RunSweeper(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	s.Stop()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RunSweeper did not stop on Stop()")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./internal/adapters/grpc/ -run TestRunSweeper -v`
Expected: FAIL (undefined: RunSweeper)

- [ ] **Step 3: Replace StartBackgroundSweeper with RunSweeper**

In `ship_state_scheduler.go`, replace the whole `StartBackgroundSweeper` method (lines 254-272) with:

```go
// RunSweeper blocks, checking for stuck ships every SweeperInterval, until
// ctx is canceled or Stop() is called. It runs under the daemon Supervisor
// (sp-i01z): a panic inside a sweep pass is captured there and the sweeper
// restarts with backoff instead of dying silently — before this, a dead
// sweeper meant arrivals stopped being swept for the rest of the daemon's
// life with zero signal. Replaces StartBackgroundSweeper.
func (s *ShipStateScheduler) RunSweeper(ctx context.Context) error {
	fmt.Printf("Background sweeper started (interval: %v)\n", SweeperInterval)
	ticker := time.NewTicker(SweeperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.stopCh:
			return nil
		case <-ticker.C:
			s.sweepStuckShips()
		}
	}
}
```

Run `rtk grep -rn "StartBackgroundSweeper" --include='*.go' internal/ cmd/` — the only production caller is `daemon_server.go:527` (updated in Step 5); update any test callers to `RunSweeper` with a canceled-context pattern.

- [ ] **Step 4: Run test to verify it passes**

Run: `rtk go test ./internal/adapters/grpc/ -run TestRunSweeper -v`
Expected: PASS

- [ ] **Step 5: Wire the Supervisor into DaemonServer**

In `captain_recorder.go`, add (next to `SetCaptainEventRecorder`, line ~20):

```go
// currentCaptainEventRecorder returns the recorder installed by main (may be
// nil in minimal boots/tests). Package-internal: the daemon server hands it
// to the supervise layer at Start.
func currentCaptainEventRecorder() captain.EventRecorder {
	captainEventRecorderMu.RLock()
	defer captainEventRecorderMu.RUnlock()
	return captainEventRecorder
}
```

(Match the actual mutex/var names at the top of `captain_recorder.go` — the global and its `sync.RWMutex` are declared around lines 12-18.)

In `daemon_server.go`:

1. Struct fields — after `done chan struct{}` (line ~136):

```go
	// Supervised background components (sp-i01z). runCtx is the daemon
	// lifetime context: canceled first thing in handleShutdown so supervised
	// loops (sweeper) wind down in parallel with the container drain.
	runCtx    context.Context
	runCancel context.CancelFunc
	sup       *supervise.Supervisor
```

2. Add the primary-player resolver (near `syncAllShipsOnStartup`, line ~711). The daemon's existing primary-player idiom is the open era (see the zombie-release block at Start's top, line ~488):

```go
// primaryPlayerID resolves the player that daemon-scoped captain events are
// attributed to: the open era's player (the same identity the zombie-release
// block at Start uses), falling back to the first player row, else 0.
func (s *DaemonServer) primaryPlayerID(ctx context.Context) int {
	if s.db != nil {
		eraRepo := persistence.NewEraRepository(s.db)
		if openEra, err := eraRepo.FindOpenEra(ctx); err == nil && openEra != nil {
			return openEra.PlayerID
		}
	}
	if s.playerRepo != nil {
		if players, err := s.playerRepo.ListAll(ctx); err == nil && len(players) > 0 {
			return players[0].ID.Value()
		}
	}
	return 0
}
```

3. At the top of `Start()` (line 484, immediately after the `fmt.Printf("Daemon server listening...")` line and before the zombie-release block):

```go
	// Supervised-component wiring (sp-i01z). The captain recorder was
	// installed by main before Start; a nil recorder just means no events.
	s.runCtx, s.runCancel = context.WithCancel(context.Background())
	bootCtx, bootCancel := context.WithTimeout(s.runCtx, 10*time.Second)
	s.sup = supervise.New(
		currentCaptainEventRecorder(),
		s.primaryPlayerID(bootCtx),
		s.clock,
		supervise.WithOnRestart(metrics.RecordDaemonComponentRestart),
	)
	bootCancel()
```

4. Replace the sweeper spawn (line 527) — change:

```go
		// Start background sweeper to catch ships that slip through due to failures
		s.shipStateScheduler.StartBackgroundSweeper()
```

to:

```go
		// Start background sweeper under supervision (sp-i01z): restarts
		// with backoff on crash, escalates a crash loop to the captain.
		s.sup.Go(s.runCtx, "ship-state-sweeper", s.shipStateScheduler.RunSweeper)
```

5. Guard the recovery goroutine (line ~568) — change the body of the existing `go func() {...}()` to:

```go
	go supervise.Guard("container-recovery", func() {
		recoveryCtx, recoveryCancel := context.WithTimeout(s.runCtx, 30*time.Second)
		defer recoveryCancel()

		if err := s.RecoverRunningContainers(recoveryCtx); err != nil {
			fmt.Printf("Warning: Container recovery failed: %v\n", err)
		}
	})
```

(Guard, not `sup.Go`: recovery re-adopts containers and is NOT safely re-runnable — a restart could double-adopt. One attempt, loudly logged, panic-isolated; error behavior identical to today.)

6. `handleShutdown()` (line 681): immediately after `fmt.Println("\nShutdown signal received, ...")`, add:

```go
	// Cancel supervised components first (sp-i01z) so the sweeper stops
	// scheduling new writes while containers drain.
	if s.runCancel != nil {
		s.runCancel()
	}
```

and immediately before `close(s.done)` at the end, add:

```go
	// Join supervised components — they exit promptly on runCtx cancel.
	if s.sup != nil {
		s.sup.Wait()
	}
```

Add the `supervise` and (if not present) `metrics` imports to `daemon_server.go`.

- [ ] **Step 6: Build + full grpc package tests**

Run: `rtk go build ./... && rtk go test ./internal/adapters/grpc/`
Expected: build OK, PASS

- [ ] **Step 7: Commit**

```bash
rtk git add internal/adapters/grpc/
rtk git commit -m "feat(daemon): sweeper + recovery under supervise layer; daemon runCtx/shutdown join (sp-i01z)"
```

---

### Task 7: Extract the error-streak primitive to `internal/application/health`

**Files:**
- Create: `internal/application/health/streak.go`
- Create: `internal/application/health/streak_test.go`
- Create: `internal/application/contract/commands/quarantine.go`
- Delete: `internal/application/contract/commands/error_streak.go`, `error_streak_test.go`
- Modify: `internal/application/contract/commands/run_fleet_coordinator.go` (lines 246, 305-311, 411-417, 621-627, 684-690, 1205)

**Interfaces:**
- Consumes: `captain.Event`/`EventCoordinatorErrorLoop` (unchanged)
- Produces (package `health`): `const DefaultStreakThreshold = 5`; `type StreakTracker` + `func NewStreakTracker(threshold int) *StreakTracker` + `func (t *StreakTracker) Note(errMsg string) (streak int, crossed bool)`; `type Monitor` + `func NewMonitor(threshold int) *Monitor` + `func (m *Monitor) Note(site, errMsg string) (streak int, crossed bool)`; `func NewErrorLoopEvent(containerID string, playerID int, checkpoint string, cause error, streak int) *captain.Event`. Rollout bead sp-6wxq wires these into the other coordinators.

This is a mechanical move+rename — behavior must not change. Renames: `errorStreakTracker`→`StreakTracker`, `newErrorStreakTracker`→`NewStreakTracker`, `coordinatorErrorMonitor`→`Monitor`, `newCoordinatorErrorMonitor`→`NewMonitor`, `buildErrorLoopEvent`→`NewErrorLoopEvent`, `coordinatorErrorStreakThreshold`→`DefaultStreakThreshold`. Keep every doc comment verbatim (they carry sp-e2l1 and the 2026-07-05 incident context), adjusting only the renamed identifiers inside them.

- [ ] **Step 1: Create the health package by moving the code**

`internal/application/health/streak.go`: copy `error_streak.go` verbatim, then: package clause `package health`; apply the renames above; keep the `captain` import; the package doc comment:

```go
// Package health holds the coordinator self-observability primitives
// (sp-e2l1, generalized by sp-i01z): consecutive-identical-error streak
// tracking that turns a silently-stuck retry loop into an interrupt-class
// captain event. Extracted from internal/application/contract/commands so
// every coordinator can adopt it (rollout: sp-6wxq).
package health
```

EXCLUDE `hullQuarantineMessage` and `buildHullQuarantineEvent` — those move to `internal/application/contract/commands/quarantine.go` (verbatim, same package `commands`, keeping their doc comments; they need the `captain` and `encoding/json` and `fmt` imports). Their tests already live in `spawn_governor_test.go` and are untouched.

- [ ] **Step 2: Move the tests**

`internal/application/health/streak_test.go`: copy `error_streak_test.go` verbatim; package `health`; apply the renames (`newErrorStreakTracker(...)`→`NewStreakTracker(...)`, `newCoordinatorErrorMonitor`→`NewMonitor`, `buildErrorLoopEvent`→`NewErrorLoopEvent`). All 9 test funcs move (`TestErrorStreakTracker_*` ×6, `TestCoordinatorErrorMonitor_*` ×2, `TestBuildErrorLoopEvent_PopulatesFields`).

Delete `internal/application/contract/commands/error_streak.go` and `error_streak_test.go`.

- [ ] **Step 3: Re-point the contract coordinator**

In `run_fleet_coordinator.go`, add import `"github.com/andrescamacho/spacetraders-go/internal/application/health"` and change:
- line 246: `errMon := newCoordinatorErrorMonitor(coordinatorErrorStreakThreshold)` → `errMon := health.NewMonitor(health.DefaultStreakThreshold)`
- line 1205: `event := buildErrorLoopEvent(cmd.ContainerID, cmd.PlayerID.Value(), checkpoint, cause, streak)` → `event := health.NewErrorLoopEvent(cmd.ContainerID, cmd.PlayerID.Value(), checkpoint, cause, streak)`
- The 8 `errMon.Note(...)` call sites (305, 311, 411, 417, 621, 627, 684, 690) compile unchanged (`Monitor.Note` keeps the same signature).

Run `rtk grep -rn 'newCoordinatorErrorMonitor\|newErrorStreakTracker\|buildErrorLoopEvent\|coordinatorErrorStreakThreshold' --include='*.go' internal/` — expect zero hits outside `health/` after the re-point (the `spawn_governor.go:62` hit is a comment; update the comment's identifier to `health.NewMonitor`).

- [ ] **Step 4: Run the moved tests + both packages**

Run: `rtk go test ./internal/application/health/ ./internal/application/contract/commands/`
Expected: PASS (9 moved tests green in health; contract package fully green including spawn-governor quarantine tests)

- [ ] **Step 5: Commit**

```bash
rtk git add internal/application/health/ internal/application/contract/commands/
rtk git commit -m "refactor(daemon): extract sp-e2l1 error-streak primitive to internal/application/health (sp-i01z)"
```

---

### Task 8: Full verification

- [ ] **Step 1: Vet + full test suite**

Run: `rtk go vet ./... && rtk go test ./...`
Expected: vet clean; all packages PASS.

- [ ] **Step 2: Build the daemon binary**

Run: `rtk go build -o /tmp/spacetraders-daemon-supervise ./cmd/spacetraders-daemon && rm /tmp/spacetraders-daemon-supervise`
Expected: builds clean.

- [ ] **Step 3: Close out**

```bash
rtk git add -A docs/ && rtk git status
```

Update the bead: `bd update sp-i01z --append-notes="implementation complete: supervise pkg, runner panic barrier, timer guards, supervised sweeper+recovery, health extraction"` then follow the repo session-close protocol (commit, `bd dolt push`, `git push`).

---

## Deliberately out of scope (beads filed)

- **sp-6wxq** — wire `health.Monitor` into the remaining ~10 coordinators (stocker, trade-fleet, trade-route/tour, idle-arb, scout-post, gas, goods-factory, worker-rebalancer, frontier-expansion, parallel-manufacturing). Pattern to copy: `run_fleet_coordinator.go:246` (monitor construction), `:305-311` (Note at a checkpoint), `:1200` (`recordErrorLoopEvent`). One commit per coordinator.
- **sp-le87** — duty-cycle sampler + the 4 metrics collectors still spawn bare goroutines (`daemon_server.go:531-565`); a panic in any still kills the daemon. Refactor each to a blocking `Run(ctx) error` and spawn via `s.sup.Go(...)`, mirroring `RunSweeper`.
- Restart caps for containers (`MaxRestartAttempts = 3`) unchanged — this plan formalizes supervision, it does not retune policy.

## Design decisions (for the reviewer)

1. **In-repo, no suture**: restart-with-backoff, crash-loop detection, and streak tracking all already exist here; the gaps were panic isolation and boot-goroutine coverage. A library would add a fourth, differently-shaped layer.
2. **No new config**: thresholds are constants, matching `coordinatorErrorStreakThreshold` precedent; avoids dormant-config hazards. Retuning = edit constant + redeploy, same as today.
3. **Supervisor never gives up** (unlike containers): its components are infrastructure safety nets; permanent death is the failure mode this plan exists to kill. Loudness comes from the edge-triggered interrupt event + restart metric.
4. **No per-crash captain event**: crashes log + count; only the LOOP interrupts — mirrors `container.crashed` (deferred) vs `container.crashloop` (interrupt) and respects the sp-6g96 wake-cap doctrine.

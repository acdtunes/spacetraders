package grpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

const dbOperationTimeout = 5 * time.Second

// restartBackoffSchedule is the escalating wait applied BEFORE each automatic
// restart of a failed container. Without it, a dependency that fails
// instantly — routing down means an immediate connection-refused on localhost —
// burns all container.MaxRestartAttempts restarts in milliseconds, so the
// container terminalizes (and, under supervisor doctrine, stays dead) even
// though the dependency self-heals minutes later. The schedule is indexed by the
// number of restarts already taken (0 before the first restart); it sums to
// ~2m35s across the default three restarts, converting "burn three restarts in
// milliseconds" into riding out a multi-minute outage. The wait is clock-injected
// and ctx-interruptible (see sleepOrCancel), so a Stop/shutdown never waits it
// out and tests advance virtual time instantly.
var restartBackoffSchedule = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	120 * time.Second,
}

// restartBackoffFor returns the backoff to wait before the restart that follows
// restartsTaken prior restarts. Attempts past the end of the schedule reuse its
// final (longest) entry, so a larger container.MaxRestartAttempts can never index
// out of range.
func restartBackoffFor(restartsTaken int) time.Duration {
	if restartsTaken < 0 {
		restartsTaken = 0
	}
	if restartsTaken >= len(restartBackoffSchedule) {
		return restartBackoffSchedule[len(restartBackoffSchedule)-1]
	}
	return restartBackoffSchedule[restartsTaken]
}

// standingIterationFloors is the defence-in-depth iteration pacing for standing
// (infinite-budget) container types whose handler owns its whole run — and its
// pacing — internally, so the runner loop is only ever re-entered when that
// handler RETURNS between ticks. The normal return is terminal (RunTerminalReporter
// → the loop completes above); should the terminal seam break or regress, the
// floor paces each re-entry at the type's own reconcile tick, degrading a standing
// container to its ordinary cadence instead of a same-second full-fleet-refresh
// spin. Types absent here are unaffected (floor 0 = no pacing), so runner-loop
// types that legitimately iterate back-to-back keep their exact behavior.
var standingIterationFloors = map[container.ContainerType]time.Duration{
	container.ContainerTypeBootstrapCoordinator: bootstrapCmd.DefaultTickInterval,
}

// ContainerRunner executes a container operation in a background goroutine
// Manages the lifecycle of a single container including error handling and restarts
type ContainerRunner struct {
	containerEntity *container.Container
	mediator        common.Mediator
	command         interface{} // The command to execute (must implement mediator request)
	logRepo         persistence.ContainerLogRepository
	containerRepo   *persistence.ContainerRepositoryGORM
	shipRepo        navigation.ShipRepository
	clock           shared.Clock

	// out is the log sink's writer seam. Nil in production (os.Stdout); set by
	// tests that need to read what this runner actually printed.
	out io.Writer

	// Execution control
	ctx        context.Context
	cancelFunc context.CancelFunc
	done       chan struct{}
	mu         sync.RWMutex

	// contractRunParked records whether the most recent iteration returned a
	// contract RunWorkflowResponse with Fulfilled=false and a nil Go error —
	// i.e. the credits-park path rather than a true completion. A nil
	// error always drives the loop to a clean exit and signalCompletion(success
	// =true), so without this flag a parked run would be misreported as
	// contract.completed to the captain's income-stall detection.
	// Guarded by mu like the other execution-control fields.
	contractRunParked bool

	// taskIncomplete/taskIncompleteReason record the honest-completion veto
	// (sp-7yej invariant 2): the most recent iteration's response implemented
	// common.CompletionReporter and reported ok=false — the run ended
	// deliberately (nil Go error, so the restart loop stays out of it) but did
	// NOT honestly complete (e.g. cargo bought this run is still aboard,
	// sp-1hj5). finishCleanExit refuses success=true for such a run. The last
	// iteration governs: an implementing response that reports ok=true clears
	// any earlier veto. Guarded by mu like contractRunParked.
	taskIncomplete       bool
	taskIncompleteReason string

	// runTerminal records that the most recent iteration's response implemented
	// common.RunTerminalReporter and reported the command's WHOLE run finished
	// (the bootstrap coordinator's gate-built EXPANSION exit). The iteration
	// loop then completes the container instead of re-entering the handler —
	// load-bearing for infinite-budget containers, whose ShouldContinue never
	// goes false on its own. Guarded by mu like contractRunParked.
	runTerminal bool

	// Heartbeat control
	heartbeatStop chan struct{} // Signal to stop heartbeat goroutine
	heartbeatDone chan struct{} // Signal that heartbeat goroutine has stopped
	heartbeatOnce sync.Once     // Ensures heartbeat is only stopped once
	// heartbeatStarted records that Start actually launched the heartbeat goroutine, so
	// stopHeartbeat knows whether heartbeatDone can ever close. Without it, stopping a
	// runner whose heartbeat never started (Start's claim-failure exit, and every test
	// that drives execute directly) blocks the full 2s timeout waiting on a channel with
	// nobody to close it. Atomic rather than mu-guarded: stopHeartbeat is reached from
	// paths that must not contend for the runner lock.
	heartbeatStarted atomic.Bool

	// Event publisher for completion notifications
	// Publishes WorkerCompletedEvent when container completes or fails
	eventPublisher navigation.ShipEventPublisher

	// In-memory log cache for quick access (logs also persisted to DB)
	logs []LogEntry
}

// NewContainerRunner creates a new container runner
func NewContainerRunner(
	containerEntity *container.Container,
	mediator common.Mediator,
	command interface{},
	logRepo persistence.ContainerLogRepository,
	containerRepo *persistence.ContainerRepositoryGORM,
	shipRepo navigation.ShipRepository,
	clock shared.Clock,
) *ContainerRunner {
	ctx, cancel := context.WithCancel(context.Background())
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &ContainerRunner{
		containerEntity: containerEntity,
		mediator:        mediator,
		command:         command,
		logRepo:         logRepo,
		containerRepo:   containerRepo,
		shipRepo:        shipRepo,
		clock:           clock,
		ctx:             ctx,
		cancelFunc:      cancel,
		done:            make(chan struct{}),
		heartbeatStop:   make(chan struct{}),
		heartbeatDone:   make(chan struct{}),
		logs:            make([]LogEntry, 0),
	}
}

// SetEventPublisher sets the event publisher for completion notifications.
// This should be called before Start().
func (r *ContainerRunner) SetEventPublisher(publisher navigation.ShipEventPublisher) {
	r.eventPublisher = publisher
}

// Container returns the underlying container entity
func (r *ContainerRunner) Container() *container.Container {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.containerEntity
}

// Command returns the built command this runner executes. Its concrete type is
// what identifies the program a container is running (the flow feed reads it to
// tell a tour from an arb without parsing launch metadata).
func (r *ContainerRunner) Command() interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.command
}

// Start begins container execution
func (r *ContainerRunner) Start() error {
	r.mu.Lock()
	if err := r.containerEntity.Start(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	r.log("INFO", "Container started", nil)

	// Persist status update to database (RUNNING)
	if r.containerRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
		defer cancel()

		if err := r.containerRepo.UpdateStatus(
			ctx,
			r.containerEntity.ID(),
			r.containerEntity.PlayerID(),
			container.ContainerStatusRunning,
			nil,
			nil,
			"",
		); err != nil {
			r.log("ERROR", fmt.Sprintf("Failed to persist RUNNING status: %v", err), nil)
		}
	}

	// Create ship assignments if this container uses ships
	// This prevents concurrent containers from operating on the same ship
	if err := r.createShipAssignments(); err != nil {
		wrapped := fmt.Errorf("failed to create ship assignments: %w", err)
		r.log("ERROR", wrapped.Error(), nil)
		// The row above was just persisted as RUNNING, and the heartbeat
		// goroutine (started below, on the success path only) never gets to run on
		// this exit - so without terminalizing here, the row is stuck RUNNING with a
		// heartbeat_at that never advances, and the watchkeeper spams heartbeat_lost
		// for it forever. Terminalize now, the same way a normal failure does.
		r.terminalizeClaimFailure(wrapped)
		return wrapped
	}

	// Start heartbeat goroutine to update heartbeat_at periodically
	// This allows detection of crashed containers that don't update their heartbeat.
	// Guarded: a heartbeat panic is logged + suppressed — a dead
	// heartbeat is already surfaced by the watchkeeper's container.heartbeat_lost,
	// and the container itself keeps running.
	r.heartbeatStarted.Store(true)
	go supervise.Guard("container-heartbeat:"+r.containerEntity.ID(), r.runHeartbeat)

	go r.execute()

	return nil
}

// Stop gracefully stops the container
func (r *ContainerRunner) Stop() error {
	r.mu.Lock()
	if err := r.containerEntity.Stop(); err != nil {
		r.mu.Unlock()
		// A stop refused because the container is ALREADY TERMINAL is not a failure — the work is
		// over. But the one cleanup it still owes is releaseShipAssignments, which is the LAST
		// statement of this function, so returning the error here skipped it: the hull stayed
		// pinned to a dead container with assignment_status='active', the coordinator skipped it
		// as busy, and the watchdog re-attempted the same impossible kill every ~31s for 19
		// minutes while the hull sat laden (sp-vz8hj).
		//
		// Release and report success. It is safe precisely BECAUSE the container is terminal: it
		// is running nothing, so it cannot be mid-flight with the hull. releaseShipAssignments is
		// itself idempotent — it releases only hulls still assigned to THIS container and skips
		// any that moved on (RULINGS #7) — so repeating it costs nothing.
		//
		// EVERY OTHER stop failure still returns, unchanged. That container may genuinely still be
		// flying the hull, and releasing it there is the failure this narrow branch must not
		// become — see TestTradeWatchdog_KillFails_NoRelaunch for the property at the caller.
		//
		// The terminal status is deliberately NOT rewritten: no MarkStopped, no UpdateStatus. A
		// COMPLETED container that ran to completion must not be relabelled STOPPED just because
		// something asked it to stop afterwards.
		if !errors.Is(err, container.ErrContainerAlreadyTerminal) {
			return err
		}
		r.releaseShipAssignments("stopped")
		return nil
	}
	r.mu.Unlock()

	r.log("INFO", "Container stopping...", nil)

	r.stopHeartbeat()

	// Cancel context to signal stop
	r.cancelFunc()

	// Wait for completion (with timeout)
	select {
	case <-r.done:
		r.log("INFO", "Container stopped gracefully", nil)
	case <-time.After(10 * time.Second):
		r.log("WARNING", "Container did not stop within timeout", nil)
	}

	r.mu.Lock()
	r.containerEntity.MarkStopped()
	r.mu.Unlock()

	// Persist STOPPED status to database
	if r.containerRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
		defer cancel()

		now := time.Now()
		if err := r.containerRepo.UpdateStatus(
			ctx,
			r.containerEntity.ID(),
			r.containerEntity.PlayerID(),
			container.ContainerStatusStopped,
			&now,      // stoppedAt
			nil,       // exitCode (nil for graceful stop)
			"stopped", // exitReason
		); err != nil {
			r.log("ERROR", fmt.Sprintf("Failed to persist STOPPED status: %v", err), nil)
		}
	}

	// Release ship assignments for this container
	r.releaseShipAssignments("stopped")

	return nil
}

// runHeartbeat periodically updates the container's heartbeat timestamp
// This allows detection of crashed containers that stop updating their heartbeat
func (r *ContainerRunner) runHeartbeat() {
	defer close(r.heartbeatDone)

	// Update heartbeat every 30 seconds
	// Stale timeout is 2 minutes, so 30s gives us 4 heartbeats before considered stale
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.heartbeatStop:
			return
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			if r.containerRepo != nil {
				ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
				if err := r.containerRepo.UpdateContainerHeartbeat(ctx, r.containerEntity.ID()); err != nil {
					// Log but don't fail - heartbeat is best-effort
					r.log("WARN", fmt.Sprintf("Failed to update heartbeat: %v", err), nil)
				}
				cancel()
			}
		}
	}
}

// stopHeartbeat stops the heartbeat goroutine (safe to call multiple times)
func (r *ContainerRunner) stopHeartbeat() {
	r.heartbeatOnce.Do(func() {
		close(r.heartbeatStop)
		// Nothing ever ran, so heartbeatDone will never close — waiting would burn the
		// full timeout for no signal. Closing the stop channel above is still correct
		// (a no-op for absent receivers) and keeps the Once semantics.
		if !r.heartbeatStarted.Load() {
			return
		}
		// WAIT for the goroutine to return, don't just signal it. This is what orders a
		// heartbeat write BEFORE the terminal status write that follows: an
		// in-flight UpdateContainerHeartbeat completes before runHeartbeat's deferred
		// close fires, so heartbeat_at cannot post-date stopped_at. The timeout bounds a
		// wedged write rather than hanging shutdown — on that path the ordering is
		// best-effort, which is the honest limit of this guarantee.
		select {
		case <-r.heartbeatDone:
			// Heartbeat goroutine stopped
		case <-time.After(2 * time.Second):
			// Timeout waiting for heartbeat to stop
		}
	})
}

// execute runs the container operation loop.
//
// ITERATION SEMANTICS (sp-7yej invariant 3): the loop below is the RUNNER-LOOP
// model — each iteration is one Handle() of the command, and the container's
// maxIterations is the work budget (-1 = infinite). Types whose command owns
// the whole run internally (trade_route's visit budget, scout_tour's tour
// count, the one-shot ship ops) are created and recovered with maxIterations=1
// and MUST NOT be re-entered — their ContainerSpec declares
// CoordinatorOwnsIterations. The full per-type table (unit of work, loop
// owner, restart behavior) lives on containerSpecList in
// command_factory_registry.go.
func (r *ContainerRunner) execute() {
	defer close(r.done)

	if !r.waitStartupJitter() {
		return
	}

	// Iteration loop (supports multi-iteration operations like scout tours)
	for {
		r.mu.RLock()
		shouldContinue := r.containerEntity.ShouldContinue()
		isStopping := r.containerEntity.IsStopping()
		r.mu.RUnlock()

		// Budget exhaustion (a finite maxIterations reached) is the ONLY clean
		// COMPLETION — fall through to finishCleanExit (COMPLETED) below. Checked
		// FIRST so a container that both ran its budget to exhaustion AND was asked
		// to stop still completes honestly.
		if !shouldContinue {
			break
		}

		// A stop REQUEST is an INTERRUPTION, not a completion: returning leaves the row RUNNING
		// for recovery, where COMPLETED would lose the container (a -1 budget never exhausts).
		if isStopping {
			r.log("INFO", "Stop requested — exiting resumable (re-adopted at next boot)", nil)
			return
		}

		if err := r.runIterationProtected(); err != nil {
			if !r.restartAfterFailure(err) {
				return
			}
			continue
		}

		r.mu.Lock()
		r.containerEntity.IncrementIteration()
		r.mu.Unlock()

		metrics.RecordContainerIteration(r.containerEntity)

		r.log("INFO", fmt.Sprintf("Iteration %d completed",
			r.containerEntity.CurrentIteration()), nil)

		// A response reporting the run TERMINAL is the ONLY way an infinite (-1) container
		// completes: ShouldContinue never goes false, so re-entering would loop forever.
		r.mu.RLock()
		runDone := r.runTerminal
		r.mu.RUnlock()
		if runDone {
			r.log("INFO", "Run terminal — the response reports the command's whole run finished; completing without re-entering", nil)
			break
		}

		select {
		case <-r.ctx.Done():
			r.log("INFO", "Stop signal received", nil)
			return
		default:
			// Continue to next iteration
		}

		// Defence in depth: a standing handler returning between ticks without reporting terminal
		// is paced, not spun — each unpaced re-entry is a fully-paginated fleet re-read.
		if floor := standingIterationFloors[r.containerEntity.Type()]; floor > 0 && r.containerEntity.MaxIterations() == -1 {
			if err := r.sleepOrCancel(floor); err != nil {
				r.exitCanceled("Iteration pacing canceled by stop/shutdown")
				return
			}
		}
	}

	r.finishCleanExit()
}

// waitStartupJitter spreads simultaneous container starts across 0-5s so they do not
// stampede the API. Reports false when a shutdown interrupted the wait.
func (r *ContainerRunner) waitStartupJitter() bool {
	jitter := time.Duration(rand.Intn(5000)) * time.Millisecond
	r.log("INFO", fmt.Sprintf("Startup jitter: waiting %v before first API call", jitter), nil)

	if err := r.sleepOrCancel(jitter); err != nil {
		r.log("INFO", "Context canceled during startup jitter", nil)
		return false
	}
	return true
}

// exitCanceled ends a run interrupted by stop/shutdown: complete gracefully and hand the
// hull back so the next boot can re-adopt it.
func (r *ContainerRunner) exitCanceled(reason string) {
	r.log("INFO", reason, nil)
	r.signalCompletion()
	r.releaseShipAssignments("canceled")
}

// restartAfterFailure applies the restart policy to a failed iteration. False means the run
// is over — shutdown interrupted it, or the restart budget is spent — and the caller returns.
func (r *ContainerRunner) restartAfterFailure(err error) bool {
	if r.ctx.Err() != nil {
		r.exitCanceled("Context canceled, stopping container")
		return false
	}

	r.handleError(err)

	r.mu.RLock()
	canRestart := r.containerEntity.CanRestart()
	restartsSoFar := r.containerEntity.RestartCount()
	r.mu.RUnlock()

	if !canRestart {
		// FAILED is persisted only NOW: earlier would drop a live restarting container from
		// recovery. recordCrash is here so container.crashed counts crashes, not every retry.
		r.persistFailed(err.Error())
		r.recordCrash(err)
		r.signalCompletionWithStatus(false, err.Error())
		r.releaseShipAssignments("failed")
		return false
	}

	// A dependency that fails instantly would otherwise burn every restart in milliseconds.
	// restartsSoFar counts restarts already taken, so it indexes the schedule directly.
	backoff := restartBackoffFor(restartsSoFar)
	r.log("INFO", fmt.Sprintf("Retrying after error in %s (attempt %d)",
		backoff, restartsSoFar+1), nil)

	if waitErr := r.sleepOrCancel(backoff); waitErr != nil {
		r.exitCanceled("Restart backoff canceled by stop/shutdown")
		return false
	}

	r.mu.Lock()
	r.containerEntity.ResetForRestart()
	r.containerEntity.Start()
	r.mu.Unlock()

	metrics.RecordContainerRestart(r.containerEntity)
	return true
}

// executeIteration executes a single iteration of the container operation
// runIterationProtected wraps executeIteration in a panic barrier:
// a panic inside any command handler is converted to an error so the restart
// machinery below handles it exactly like a returned error. Without the barrier,
// one nil-deref in one coordinator kills the entire daemon process.
func (r *ContainerRunner) runIterationProtected() (err error) {
	defer supervise.CapturePanic(&err, "container:"+r.containerEntity.ID())
	return r.executeIteration()
}

func (r *ContainerRunner) executeIteration() error {
	r.log("INFO", "Executing iteration", map[string]interface{}{
		"iteration": r.containerEntity.CurrentIteration() + 1,
	})

	// Add logger to context so handlers can log
	ctxWithLogger := common.WithLogger(r.ctx, r)

	result, err := r.mediator.Send(ctxWithLogger, r.command)
	if err != nil {
		return fmt.Errorf("command execution failed: %w", err)
	}

	// Log command result
	r.log("INFO", fmt.Sprintf("Command executed, result type: %T", result), nil)

	// A contract workflow that parked on insufficient credits returns
	// (result, nil) by design — a clean loop exit so CanRestart's no-backoff
	// continue never sees a Go error to crashloop on. Capture the park here so
	// signalCompletionWithStatus can tell "parked" apart from "actually
	// fulfilled" instead of reporting every nil-error exit as contract.completed.
	if resp, ok := result.(*contractCmd.RunWorkflowResponse); ok && !resp.Fulfilled {
		r.mu.Lock()
		r.contractRunParked = true
		r.mu.Unlock()
	}

	// Honest completion (sp-7yej invariant 2): a response that implements
	// common.CompletionReporter can veto the clean-exit success=true — a
	// deliberate nil-error exit (so CanRestart never crashloops it) that
	// nevertheless left its task incomplete, e.g. cargo bought this run still
	// aboard (trade-route, sp-1hj5). Recorded per iteration, last one governs;
	// finishCleanExit turns a standing veto into a FAILED terminalization.
	if rep, ok := result.(common.CompletionReporter); ok {
		outcomeOK, reason := rep.CompletionOutcome()
		r.mu.Lock()
		r.taskIncomplete = !outcomeOK
		r.taskIncompleteReason = reason
		r.mu.Unlock()
	}

	// Run termination: a response that implements common.RunTerminalReporter
	// declares whether the command's WHOLE run is finished (the bootstrap
	// coordinator's gate-built EXPANSION exit). execute() stops iterating on a
	// standing terminal report — the container's iteration budget alone cannot
	// end an infinite (-1) container, so without this the runner would re-enter
	// a finished run forever. Recorded per iteration, last one governs.
	if term, ok := result.(common.RunTerminalReporter); ok {
		r.mu.Lock()
		r.runTerminal = term.RunTerminal()
		r.mu.Unlock()
	}

	return nil
}

// sleepOrCancel blocks for d, returning early with the context error if the
// container's context is canceled (Stop/shutdown) before the sleep completes.
// r.clock.Sleep is instant under the test MockClock and a real sleep in
// production; racing it against ctx.Done keeps a Stop from having to wait the full
// duration out — whether that is the claim-retry backoff, the sp-h0kr
// restart backoff (up to 120s), or the startup jitter. The detached sleeper
// goroutine outlives an early return by at most one sleep before exiting, so it
// cannot leak.
func (r *ContainerRunner) sleepOrCancel(d time.Duration) error {
	slept := make(chan struct{})
	go func() {
		r.clock.Sleep(d)
		close(slept)
	}()

	select {
	case <-r.ctx.Done():
		return r.ctx.Err()
	case <-slept:
		return nil
	}
}

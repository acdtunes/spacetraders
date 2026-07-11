package grpc

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const dbOperationTimeout = 5 * time.Second

// sp-ku8e: a captain CLI chain like `ship orbit` then `ship navigate` issued
// ~1s apart spawns back-to-back containers on the same hull. The second
// container's claim can land in the sub-second window before the first's
// synchronous release has been persisted, surfacing as a *transient*
// ShipAlreadyAssignedError. createShipAssignments retries exactly that failure a
// bounded number of times with growing backoff to absorb the handoff window,
// instead of failing to the captain and forcing a manual retry. A *permanent*
// rejection (captain reservation or foreign-fleet dedication, sp-l7h2) is never
// retried — no amount of waiting clears it. The bound keeps a genuinely-held
// hull from causing a retry storm; the growing backoff (200ms → 3s cap, ~9s
// worst case over the whole budget) resolves the common sub-second race on the
// first short retry while still tolerating a slightly slower release.
const (
	claimRetryMaxAttempts = 7
	claimRetryBaseBackoff = 200 * time.Millisecond
	claimRetryMaxBackoff  = 3 * time.Second
)

// restartBackoffSchedule is the escalating wait applied BEFORE each automatic
// restart of a failed container (sp-h0kr). Without it, a dependency that fails
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

	// Execution control
	ctx        context.Context
	cancelFunc context.CancelFunc
	done       chan struct{}
	mu         sync.RWMutex

	// contractRunParked records whether the most recent iteration returned a
	// contract RunWorkflowResponse with Fulfilled=false and a nil Go error —
	// i.e. the credits-park path (sp-vwhi) rather than a true completion. A nil
	// error always drives the loop to a clean exit and signalCompletion(success
	// =true), so without this flag a parked run would be misreported as
	// contract.completed to the captain's income-stall detection (sp-82qs).
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

	// Heartbeat control
	heartbeatStop chan struct{} // Signal to stop heartbeat goroutine
	heartbeatDone chan struct{} // Signal that heartbeat goroutine has stopped
	heartbeatOnce sync.Once     // Ensures heartbeat is only stopped once

	// Event publisher for completion notifications
	// Publishes WorkerCompletedEvent when container completes or fails
	eventPublisher navigation.ShipEventPublisher

	// In-memory log cache for quick access (logs also persisted to DB)
	logs []LogEntry
}

// LogEntry represents a single log message from a container
type LogEntry struct {
	Timestamp time.Time
	Level     string
	Message   string
	Metadata  map[string]interface{}
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
		// sp-cr86: the row above was just persisted as RUNNING, and the heartbeat
		// goroutine (started below, on the success path only) never gets to run on
		// this exit - so without terminalizing here, the row is stuck RUNNING with a
		// heartbeat_at that never advances, and the watchkeeper spams heartbeat_lost
		// for it forever. Terminalize now, the same way a normal failure does.
		r.terminalizeClaimFailure(wrapped)
		return wrapped
	}

	// Start heartbeat goroutine to update heartbeat_at periodically
	// This allows detection of crashed containers that don't update their heartbeat
	go r.runHeartbeat()

	// Execute the container operation
	go r.execute()

	return nil
}

// terminalizeClaimFailure marks the container row FAILED when Start() cannot claim
// its ship (already assigned to a different container, or reserved by the captain).
// This is the claim-failure exit path: the row was just persisted RUNNING above, but
// neither the heartbeat nor the execute goroutine ever starts on this path - so
// without this, the row is a zombie stuck at RUNNING with a heartbeat_at that never
// advances again, and the watchkeeper spams heartbeat_lost for it forever (sp-cr86).
// Mirrors handleError's terminalization pattern, releases any partial ship state
// (idempotent no-op if nothing was assigned), and signals the coordinator (if any) so
// it doesn't wait forever on a worker that never actually started.
func (r *ContainerRunner) terminalizeClaimFailure(err error) {
	r.mu.Lock()
	r.containerEntity.Fail(err)
	r.mu.Unlock()

	metrics.RecordContainerCompletion(r.containerEntity)
	metrics.RecordContainerExit(r.containerEntity)

	if r.containerRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
		defer cancel()

		now := time.Now()
		exitCode := 1

		if dbErr := r.containerRepo.UpdateStatus(
			ctx,
			r.containerEntity.ID(),
			r.containerEntity.PlayerID(),
			container.ContainerStatusFailed,
			&now,
			&exitCode,
			fmt.Sprintf("claim_failed: %s", err.Error()),
		); dbErr != nil {
			r.log("ERROR", fmt.Sprintf("Failed to persist FAILED status after claim failure: %v", dbErr), nil)
		}
	}

	r.releaseShipAssignments("claim_failed")
	r.signalCompletionWithStatus(false, err.Error())
}

// Stop gracefully stops the container
func (r *ContainerRunner) Stop() error {
	r.mu.Lock()
	if err := r.containerEntity.Stop(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	r.log("INFO", "Container stopping...", nil)

	// Stop the heartbeat goroutine
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
		// Wait for heartbeat goroutine to finish (with timeout)
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

	// Add startup jitter to spread out API calls and prevent thundering herd
	// when multiple containers start simultaneously (0-5 seconds)
	jitter := time.Duration(rand.Intn(5000)) * time.Millisecond
	r.log("INFO", fmt.Sprintf("Startup jitter: waiting %v before first API call", jitter), nil)

	// sp-h0kr: route the startup jitter through the injectable clock (a real
	// 0-5s wait under RealClock, instant under the test MockClock) via the same
	// ctx-interruptible primitive as the restart backoff. A shutdown during jitter
	// still exits promptly, and execute()'s restart loop becomes testable without
	// wall-waiting.
	if err := r.sleepOrCancel(jitter); err != nil {
		// Context canceled during jitter, exit immediately
		r.log("INFO", "Context canceled during startup jitter", nil)
		return
	}

	// Iteration loop (supports multi-iteration operations like scout tours)
	for {
		// Check if we should continue
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

		// A stop/shutdown REQUEST is an INTERRUPTION, not a completion. The daemon's
		// graceful shutdown flips the STOPPING flag via containerEntity.Stop()
		// WITHOUT cancelling ctx (gracefulShutdownWithTimeout), so it surfaces HERE,
		// between iterations, rather than as the ctx-cancellation handled below. A
		// container that has NOT exhausted its budget must exit RESUMABLE so it is
		// re-adopted at the next boot — mirroring the ctx-cancellation exits below and
		// the clean between-iteration exit at the bottom of this loop, which is how
		// every other container type (trade-route/scout/arb) survived the same
		// restart. A -1 (infinite) container NEVER exhausts its budget, so the OLD
		// `!shouldContinue || isStopping` break routed it through finishCleanExit and
		// terminalized it COMPLETED; a COMPLETED row is not in the INTERRUPTED+RUNNING
		// recovery set, so the container is silently lost at restart (sp-ovkn — the
		// JP61 worker-less goods_factory container-loss incident; RULING #2 / sp-7yej
		// invariant 4). Returning here leaves the row RUNNING for recovery to re-adopt
		// (the force-interrupt path marks any still-RUNNING row INTERRUPTED; either
		// status is recovered), and — unlike finishCleanExit — never writes COMPLETED.
		if isStopping {
			r.log("INFO", "Stop requested — exiting resumable (re-adopted at next boot)", nil)
			return
		}

		// Execute single iteration
		if err := r.executeIteration(); err != nil {
			// Check if error is due to context cancellation (shutdown signal)
			// Don't retry on context cancellation - exit immediately
			if r.ctx.Err() != nil {
				r.log("INFO", "Context canceled, stopping container", nil)
				// Signal completion on context cancellation (graceful shutdown)
				r.signalCompletion()
				r.releaseShipAssignments("canceled")
				return
			}

			r.handleError(err)

			// Check if we should retry
			r.mu.RLock()
			canRestart := r.containerEntity.CanRestart()
			restartsSoFar := r.containerEntity.RestartCount()
			r.mu.RUnlock()

			if canRestart {
				// sp-h0kr: wait an escalating, ctx-interruptible backoff BEFORE
				// restarting. A dependency that fails instantly (routing down =>
				// immediate connection-refused on localhost) would otherwise burn
				// every restart in milliseconds and terminalize a container that a
				// self-healing dependency would have let recover. restartsSoFar is
				// the number of restarts already taken (0 before the first), so it
				// indexes the schedule directly.
				backoff := restartBackoffFor(restartsSoFar)
				r.log("INFO", fmt.Sprintf("Retrying after error in %s (attempt %d)",
					backoff, restartsSoFar+1), nil)

				// A Stop/shutdown during the wait must exit promptly, not hang up to
				// the full backoff. Mirror the ctx-canceled-during-iteration path
				// above: signal graceful completion and release the hull.
				if waitErr := r.sleepOrCancel(backoff); waitErr != nil {
					r.log("INFO", "Restart backoff canceled by stop/shutdown", nil)
					r.signalCompletion()
					r.releaseShipAssignments("canceled")
					return
				}

				r.mu.Lock()
				r.containerEntity.ResetForRestart()
				r.containerEntity.Start()
				r.mu.Unlock()

				// Record restart metrics
				metrics.RecordContainerRestart(r.containerEntity)

				continue // DON'T signal completion - we're restarting
			}

			// UNRECOVERABLE ERROR: the container has truly crashed. Surface the crash
			// signature at ERROR and record the strategic crash event here (not in
			// handleError) so container.crashed counts true crashes, not every retry.
			// Only NOW — after the restart decision came back "no more restarts" — do
			// we persist the terminal FAILED row (sp-v63s: never before, or a live
			// restarting container is dropped from recovery), then signal completion
			// and release ships.
			r.persistFailed(err.Error())
			r.recordCrash(err)
			r.signalCompletionWithStatus(false, err.Error())
			r.releaseShipAssignments("failed")
			return // Exit on unrecoverable error
		}

		// Increment iteration counter
		r.mu.Lock()
		r.containerEntity.IncrementIteration()
		r.mu.Unlock()

		// Record iteration metrics
		metrics.RecordContainerIteration(r.containerEntity)

		r.log("INFO", fmt.Sprintf("Iteration %d completed",
			r.containerEntity.CurrentIteration()), nil)

		// Check for stop signal
		select {
		case <-r.ctx.Done():
			r.log("INFO", "Stop signal received", nil)
			return
		default:
			// Continue to next iteration
		}
	}

	r.finishCleanExit()
}

// finishCleanExit terminalizes a container whose iteration loop ended without
// an unrecoverable error. Honest completion (sp-7yej invariant 2) is enforced
// HERE, at the single clean-exit choke point: if the last iteration's response
// vetoed success (common.CompletionReporter reporting ok=false — e.g. a
// trade-route run ending with cargo bought this run still aboard, sp-1hj5),
// the container is terminalized FAILED with the veto reason as its failure
// signature and completion is signaled success=false. The veto path is
// deliberately nil-error (never routed through the restart loop): a
// dynamically-selected task cannot be resumed by a re-run, so retrying would
// work AROUND the incomplete task rather than finish it.
func (r *ContainerRunner) finishCleanExit() {
	// Stop heartbeat before marking as terminal
	r.stopHeartbeat()

	r.mu.RLock()
	incomplete, incompleteReason := r.taskIncomplete, r.taskIncompleteReason
	r.mu.RUnlock()

	if incomplete {
		// handleError owns the shared in-memory failure bookkeeping: ERROR log,
		// Fail() transition, failure metrics. Not a crash — recordCrash is
		// deliberately NOT called (the run ended at a safe exit point; it just may
		// not claim success). This IS a genuine terminal exit (a re-run cannot
		// resume a dynamically-planned task), so persist the terminal FAILED row
		// here — handleError no longer does (sp-v63s: it must not, or a restarting
		// container would be dropped from recovery).
		vetoErr := fmt.Errorf("completion refused (honest-completion contract): %s", incompleteReason)
		r.handleError(vetoErr)
		r.persistFailed(vetoErr.Error())

		// Release before signaling, mirroring the completed path's ordering
		// (the coordinator must never discover a still-claimed hull after the
		// completion event lands).
		r.releaseShipAssignments("failed")
		r.signalCompletionWithStatus(false, incompleteReason)
		return
	}

	// Mark as completed
	r.mu.Lock()
	r.containerEntity.Complete()
	r.mu.Unlock()

	// Record completion metrics
	metrics.RecordContainerCompletion(r.containerEntity)
	metrics.RecordContainerExit(r.containerEntity)

	r.log("INFO", "Container completed successfully", map[string]interface{}{
		"iterations": r.containerEntity.CurrentIteration(),
		"runtime":    r.containerEntity.RuntimeDuration().String(),
	})

	// Persist completion to database
	if r.containerRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
		defer cancel()

		now := time.Now()
		exitCode := 0

		if err := r.containerRepo.UpdateStatus(
			ctx,
			r.containerEntity.ID(),
			r.containerEntity.PlayerID(),
			container.ContainerStatusCompleted,
			&now,
			&exitCode,
			"",
		); err != nil {
			r.log("ERROR", fmt.Sprintf("Failed to persist COMPLETED status: %v", err), nil)
		}
	}

	// CRITICAL: Release ship assignments BEFORE signaling completion
	// This prevents race condition where coordinator discovers ship before it's released
	// causing "ship already assigned to container" errors
	r.releaseShipAssignments("completed")

	// Signal completion to coordinator (if callback set)
	// Now safe to signal - ship is fully released
	r.signalCompletion()
}

// signalCompletion signals container completion via event bus.
func (r *ContainerRunner) signalCompletion() {
	r.signalCompletionWithStatus(true, "")
}

// signalCompletionWithStatus signals container completion with success status and error message via event bus.
func (r *ContainerRunner) signalCompletionWithStatus(success bool, errMsg string) {
	metadata := r.containerEntity.Metadata()

	// Extract ship symbol from container metadata (shared by the captain outbox
	// and the coordinator completion event below).
	shipSymbol, ok := metadata["ship_symbol"].(string)
	containerID := r.containerEntity.ID()
	playerID := r.containerEntity.PlayerID()

	// Record a strategic event for the watchkeeper BEFORE the
	// nil-publisher early return so it fires even when no coordinator is wired.
	eventType := captain.EventWorkflowFinished
	if !success {
		eventType = captain.EventWorkflowFailed
	}
	recordCaptainEvent(eventType, shipSymbol, playerID, map[string]any{
		"container_id": containerID,
		"command_type": string(r.containerEntity.Type()),
		"success":      success,
		"error":        errMsg,
	})

	// A contract workflow reaching a terminal state is a first-class strategic
	// signal, not merely "a workflow finished": in ADDITION to the generic
	// event above, emit contract.completed / contract.failed (sp-82qs) so the
	// watchkeeper receives a contract-grade signal instead of a low-fidelity
	// workflow.finished. Credits and contract-id are not available at this site
	// (the container carries only container/coordinator ids) and are
	// deliberately omitted; payout enrichment is a follow-up.
	r.mu.RLock()
	parked := r.contractRunParked
	r.mu.RUnlock()

	// A parked run (sp-vwhi credits-park) reaches this success=true path via a
	// clean, deliberate loop exit — it is neither a true completion nor a true
	// failure, so contract.completed/contract.failed are both suppressed here.
	// The generic EventWorkflowFinished above still fires (a park IS a clean
	// iteration from the runner's point of view), and the earlier structured
	// WARNING log (credits_needed/credits_available/action=parked) already
	// gives the captain full diagnostic visibility on why no contract event
	// was recorded.
	if r.containerEntity.Type() == container.ContainerTypeContractWorkflow && !(success && parked) {
		contractEvent := captain.EventContractCompleted
		if !success {
			contractEvent = captain.EventContractFailed
		}
		coordinatorID, _ := metadata["coordinator_id"].(string)
		recordCaptainEvent(contractEvent, shipSymbol, playerID, map[string]any{
			"container_id":   containerID,
			"coordinator_id": coordinatorID,
			"success":        success,
			"error":          errMsg,
		})
	}

	publisher := resolveWorkerPublisher(r.eventPublisher)
	if publisher == nil {
		return
	}

	if !ok {
		r.log("WARNING", "No ship_symbol in metadata, cannot signal completion", nil)
		return
	}

	coordinatorID, _ := metadata["coordinator_id"].(string)

	event := navigation.WorkerCompletedEvent{
		ContainerID:   r.containerEntity.ID(),
		PlayerID:      r.containerEntity.PlayerID(),
		ShipSymbol:    shipSymbol,
		CoordinatorID: coordinatorID,
		Success:       success,
		Error:         errMsg,
	}

	publisher.PublishWorkerCompleted(event)
	r.log("INFO", fmt.Sprintf("Published completion event for ship %s (success=%t)", shipSymbol, success), nil)
}

// executeIteration executes a single iteration of the container operation
func (r *ContainerRunner) executeIteration() error {
	r.log("INFO", "Executing iteration", map[string]interface{}{
		"iteration": r.containerEntity.CurrentIteration() + 1,
	})

	// Add logger to context so handlers can log
	ctxWithLogger := common.WithLogger(r.ctx, r)

	// Execute command via mediator
	result, err := r.mediator.Send(ctxWithLogger, r.command)
	if err != nil {
		return fmt.Errorf("command execution failed: %w", err)
	}

	// Log command result
	r.log("INFO", fmt.Sprintf("Command executed, result type: %T", result), nil)

	// A contract workflow that parked on insufficient credits (sp-vwhi) returns
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

	return nil
}

// handleError handles execution errors
// NOTE: This does NOT signal completion or release ships - that's done by the caller
// AFTER determining whether to restart. This prevents premature ship release before restart.
func (r *ContainerRunner) handleError(err error) {
	r.log("ERROR", err.Error(), nil)

	r.mu.Lock()
	r.containerEntity.Fail(err)
	r.mu.Unlock()

	// Record failure metrics
	metrics.RecordContainerCompletion(r.containerEntity)
	metrics.RecordContainerExit(r.containerEntity)

	// NOTE: the terminal FAILED row is intentionally NOT persisted here (sp-v63s).
	// handleError runs on EVERY failed iteration, including transient ones the
	// sp-h0kr restart loop is about to retry. That restart re-runs the container
	// in-memory (ResetForRestart + containerEntity.Start()) but the only site that
	// persists RUNNING is r.Start(), at initial boot — the restart path never
	// re-persists it. Writing FAILED here would therefore leave a still-alive,
	// restarted container carrying a stale FAILED row for the whole restart+backoff
	// (up to ~2min). RecoverRunningContainers queries only INTERRUPTED+RUNNING rows,
	// and the sp-tit8 lost-guard only diffs THAT candidate set, so a FAILED-but-alive
	// container is neither recovered NOR flagged lost when the daemon redeploys — the
	// hull is silently stranded idle-laden (RULING #2). The row instead stays at its
	// last-persisted RUNNING across the restart loop (recovery re-adopts the live
	// container); FAILED is persisted (via persistFailed) ONLY at a genuine terminal
	// exit — restart exhaustion in execute() and the honest-completion veto in
	// finishCleanExit — where it is always paired with the workflow.failed event.

	// NOTE: container.crashed is intentionally NOT recorded here. handleError runs
	// on every failed iteration, including transient errors that are retried and
	// recover, so emitting the strategic crash event here over-counts crashes. It
	// is recorded by recordCrash on the true (unrecoverable) exit path instead.

	// NOTE: signalCompletion and releaseShipAssignments are NOT called here.
	// They are called by execute() ONLY when the container is truly done (not restarting).
	// This prevents the bug where completion is signaled before restart decision.
}

// persistFailed writes the terminal FAILED row (exit code 1) with reason. It is
// called ONLY at a genuine terminal exit — restart-budget exhaustion (execute) and
// the honest-completion veto (finishCleanExit) — never on a failed iteration the
// restart loop will retry. Splitting the persist out of handleError is the sp-v63s
// fix: the DB row must reflect whether the container is recoverable, so a
// still-restarting container keeps its RUNNING row and only flips to FAILED once it
// truly gives up (always alongside the workflow.failed event). Mirrors the
// UpdateStatus shape of terminalizeClaimFailure and finishCleanExit's COMPLETED write.
func (r *ContainerRunner) persistFailed(reason string) {
	if r.containerRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
	defer cancel()

	now := time.Now()
	exitCode := 1
	if dbErr := r.containerRepo.UpdateStatus(
		ctx,
		r.containerEntity.ID(),
		r.containerEntity.PlayerID(),
		container.ContainerStatusFailed,
		&now,
		&exitCode,
		reason,
	); dbErr != nil {
		r.log("ERROR", fmt.Sprintf("Failed to persist FAILED status: %v", dbErr), nil)
	}
}

// recordCrash surfaces a true, unrecoverable container crash. It logs a single
// ERROR line carrying the container id and the underlying error — the actionable
// signature fleet operators need above the INFO respawn chatter — and records the
// strategic container.crashed event for the watchkeeper. Called only from
// execute() when the container exits with an unrecoverable error, so
// container.crashed counts true crashes rather than every retried iteration.
func (r *ContainerRunner) recordCrash(err error) {
	r.log("ERROR", fmt.Sprintf("Container %s crashed (unrecoverable): %v", r.containerEntity.ID(), err), map[string]interface{}{
		"container_id": r.containerEntity.ID(),
		"error":        err.Error(),
	})

	// Ship symbol is not reliably available here, so pass empty string.
	recordCaptainEvent(captain.EventContainerCrashed, "", r.containerEntity.PlayerID(), map[string]any{
		"container_id": r.containerEntity.ID(),
		"error":        err.Error(),
	})
}

// Logging

// Log adds a log entry (implements common.ContainerLogger interface)
func (r *ContainerRunner) Log(level, message string, metadata map[string]interface{}) {
	r.mu.Lock()
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
		Metadata:  metadata,
	}

	// Add to in-memory cache
	r.logs = append(r.logs, entry)
	r.mu.Unlock()

	// Print to stdout
	fmt.Printf("[%s] [%s] %s: %s\n",
		entry.Timestamp.Format(time.RFC3339),
		r.containerEntity.ID(),
		level,
		message,
	)

	// Persist to database (async to avoid blocking)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
		defer cancel()

		if err := r.logRepo.Log(ctx, r.containerEntity.ID(), r.containerEntity.PlayerID(), message, level, metadata); err != nil {
			// Log error to stdout if DB write fails (but don't block execution)
			fmt.Printf("[%s] [%s] ERROR: Failed to persist log to DB: %v\n",
				time.Now().Format(time.RFC3339),
				r.containerEntity.ID(),
				err,
			)
		}
	}()
}

// log is a lowercase alias for backward compatibility with existing code
func (r *ContainerRunner) log(level, message string, metadata map[string]interface{}) {
	r.Log(level, message, metadata)
}

// GetLogs returns all logs for this container
func (r *ContainerRunner) GetLogs(limit *int, level *string) []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Filter logs
	filtered := make([]LogEntry, 0)
	for _, log := range r.logs {
		if level != nil && log.Level != *level {
			continue
		}
		filtered = append(filtered, log)
	}

	// Apply limit
	if limit != nil && *limit < len(filtered) {
		// Return most recent logs
		start := len(filtered) - *limit
		return filtered[start:]
	}

	return filtered
}

// createShipAssignments claims the hull named in the container metadata
// ("ship_symbol") for this container, so concurrent containers can't operate on
// the same ship. It is a no-op for containers that carry no "ship_symbol" (e.g.
// scout-fleet-assignment).
//
// The claim is retried briefly on the transient claim-handoff race (sp-ku8e): a
// captain CLI chain (orbit then navigate ~1s apart) can have navigate's claim
// land before orbit's synchronous release has been persisted, surfacing as a
// ShipAlreadyAssignedError. Retrying absorbs that window instead of failing to
// the captain. A permanent rejection — captain reservation or foreign-fleet
// dedication (sp-l7h2) — is returned on the first attempt, never retried.
func (r *ContainerRunner) createShipAssignments() error {
	if r.shipRepo == nil {
		return nil
	}

	metadata := r.containerEntity.Metadata()

	shipSymbol, ok := metadata["ship_symbol"].(string)
	if !ok {
		// No ship_symbol in config = no ships to assign (e.g. scout-fleet-assignment).
		return nil
	}

	playerID := shared.MustNewPlayerID(r.containerEntity.PlayerID())
	operation, _ := metadata["operation"].(string)
	// captainManualAuthority (sp-sg35 BRIDGE) is set ONLY by the CLI manual-op path
	// (container_ops_ship.go); it lets a deliberate captain op override the
	// dedication guard on the legacy claim path. No automated coordinator sets it.
	captainManualAuthority, _ := metadata[captainManualAuthorityKey].(bool)

	backoff := claimRetryBaseBackoff
	for attempt := 1; ; attempt++ {
		err := r.attemptClaimShip(shipSymbol, operation, captainManualAuthority, playerID)
		if err == nil {
			if attempt > 1 {
				r.log("INFO", fmt.Sprintf("Claimed ship %s on attempt %d — transient claim-handoff race cleared", shipSymbol, attempt), nil)
			}
			return nil
		}

		// Only the transient handoff race is worth waiting on; a permanent
		// rejection (dedication / captain reservation / DB error) fails fast, and
		// the bounded attempt count keeps a genuinely-held hull from a retry storm.
		if !isTransientClaimError(err) || attempt >= claimRetryMaxAttempts {
			return err
		}

		r.log("INFO", fmt.Sprintf("Ship %s lost the claim-handoff race (attempt %d/%d), retrying in %s: %v",
			shipSymbol, attempt, claimRetryMaxAttempts, backoff, err), nil)

		if waitErr := r.sleepOrCancel(backoff); waitErr != nil {
			return fmt.Errorf("failed to claim ship %s: retry canceled: %w", shipSymbol, waitErr)
		}

		backoff *= 2
		if backoff > claimRetryMaxBackoff {
			backoff = claimRetryMaxBackoff
		}
	}
}

// captainManualAuthorityKey (sp-sg35 BRIDGE) is the container-metadata flag that
// marks a deliberate captain-initiated manual CLI op (navigate/dock/orbit/refuel/
// jettison) permitted to operate a fleet-dedicated hull as an explicit, audited
// override of the legacy-path dedication guard. It is set ONLY by the CLI
// manual-op path (container_ops_ship.go); NO automated coordinator sets it, so the
// dedication guard stays fully in force for every automated claim. Single-purpose
// and deprecable: delete this const, the guard's override branch, its audit log,
// and the container_ops_ship.go stamps together when hands-free manual
// repositioning retires the need for it (sp-lxwn/sp-zhii).
const captainManualAuthorityKey = "captain_manual_authority"

// attemptClaimShip performs a single claim of the hull for this container — the
// retryable unit of createShipAssignments. Containers carrying an "operation"
// metadata key (the launcher's fleet identity, e.g. StartTradeRoute's "trade")
// claim through the atomic operation-checked ShipRepository.ClaimShip (sp-l7h2
// Phase 2): assignment and fleet dedication are re-checked inside one row-locked
// transaction, so a hull pinned to a foreign fleet — or grabbed between discovery
// and this write — is rejected, never clobbered. Containers without the key
// (pre-change persisted rows, and every kind whose coordinator claims the hull
// BEFORE starting the runner) keep the legacy read-modify-write path, where the
// already-assigned-to-this-container check makes a recovered container's re-claim
// a no-op. That legacy path ALSO enforces the same fleet-dedication guard now
// (sp-sg35): a hull pinned to a foreign fleet is rejected there too, so the
// absence of an "operation" key is no longer a side door around ownership. The
// SOLE exception is a captainAuthority claim (the captainManualAuthorityKey flag,
// set only by the CLI manual-op path): a deliberate captain override may operate a
// dedicated hull, audited on every use — automated paths never set the flag. Both
// paths surface a transient *shared.ShipAlreadyAssignedError when the hull is
// momentarily still held by a just-finished container; a permanent rejection
// (foreign-fleet dedication, captain reservation) is returned unchanged for
// createShipAssignments to classify.
func (r *ContainerRunner) attemptClaimShip(shipSymbol, operation string, captainAuthority bool, playerID shared.PlayerID) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
	defer cancel()

	if operation != "" {
		if err := r.shipRepo.ClaimShip(ctx, shipSymbol, r.containerEntity.ID(), playerID, operation); err != nil {
			return fmt.Errorf("failed to claim ship %s: %w", shipSymbol, err)
		}
		r.log("INFO", fmt.Sprintf("Claimed ship %s for container (operation %s)", shipSymbol, operation), nil)
		return nil
	}

	ship, err := r.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}

	// Idempotent for a recovered container that already holds this claim.
	if ship.IsAssigned() && ship.ContainerID() == r.containerEntity.ID() {
		r.log("INFO", fmt.Sprintf("Ship %s already assigned to this container (recovered)", shipSymbol), nil)
		return nil
	}

	// sp-sg35: defense-in-depth — enforce the SAME fleet-dedication guard the
	// atomic ClaimShip applies (ship_repository.go: DedicatedFleet != "" &&
	// DedicatedFleet != operation), so a hull pinned to a foreign fleet can never
	// be claimed through this operation-key side door. A container reaching the
	// legacy path carries no verified fleet identity (operation is empty), so any
	// dedicated hull is rejected — the same net effect as the atomic guard with an
	// empty operation. Ordered exactly like ClaimShip's own idempotent-then-
	// dedication sequence: the same-container recovery check above wins first
	// (dedication governs the NEXT acquisition, never eviction of the current
	// holder), and AssignToContainer's reservation/already-assigned guards run
	// after. The rejection is the standing ShipDedicatedToOtherFleetError, so it
	// fails fast (isTransientClaimError == false) and terminalizes the row like any
	// other permanent claim rejection instead of retrying the handoff race.
	if ship.DedicatedFleet() != "" && ship.DedicatedFleet() != operation {
		// CAPTAIN-AUTHORITY MANUAL OVERRIDE (sp-sg35 BRIDGE): a deliberate captain
		// CLI op (navigate/dock/orbit/refuel/jettison) may operate a fleet-dedicated
		// hull without unassigning it first — the captain ruled unassign-first is
		// non-atomic and would strand a heavy unpinned during the high-restart
		// window. This flag is set ONLY by container_ops_ship.go, so NO automated
		// coordinator (tour/trade/contract/factory/scout/stocker/arb) can reach this
		// branch — the guard is fully in force for every automated claim. Every use
		// is logged for audit. DEPRECATE: delete this branch, the flag, and its log
		// when hands-free manual repositioning lands (sp-lxwn/sp-zhii).
		if !captainAuthority {
			return shared.NewShipDedicatedToOtherFleetError(shipSymbol, ship.DedicatedFleet(), operation)
		}
		r.log("WARNING", fmt.Sprintf(
			"Captain-authority override: manual op %s claims %s-dedicated hull %s without unassigning (bridge authority — deprecate with sp-lxwn/sp-zhii)",
			r.containerEntity.Type(), ship.DedicatedFleet(), shipSymbol),
			map[string]interface{}{
				"action":          "captain_manual_authority_override",
				"ship_symbol":     shipSymbol,
				"op":              string(r.containerEntity.Type()),
				"dedicated_fleet": ship.DedicatedFleet(),
				"container_id":    r.containerEntity.ID(),
			})
	}

	if err := ship.AssignToContainer(r.containerEntity.ID(), r.clock); err != nil {
		return fmt.Errorf("failed to assign ship %s: %w", shipSymbol, err)
	}

	if err := r.shipRepo.Save(ctx, ship); err != nil {
		return fmt.Errorf("failed to persist ship %s assignment: %w", shipSymbol, err)
	}

	r.log("INFO", fmt.Sprintf("Assigned ship %s to container", shipSymbol), nil)
	return nil
}

// isTransientClaimError reports whether a claim failure is the transient
// claim-handoff race (sp-ku8e) — the hull is momentarily still assigned to
// another, just-finished container — and is therefore worth a brief retry. A
// captain reservation (ShipReservedByCaptainError) and a foreign-fleet
// dedication (ShipDedicatedToOtherFleetError, sp-l7h2) are standing rejections
// that no wait will clear, so those — and every other error, e.g. a DB failure —
// are permanent and returned to the caller immediately.
func isTransientClaimError(err error) bool {
	var alreadyAssigned *shared.ShipAlreadyAssignedError
	return errors.As(err, &alreadyAssigned)
}

// sleepOrCancel blocks for d, returning early with the context error if the
// container's context is canceled (Stop/shutdown) before the sleep completes.
// r.clock.Sleep is instant under the test MockClock and a real sleep in
// production; racing it against ctx.Done keeps a Stop from having to wait the full
// duration out — whether that is the sp-ku8e claim-retry backoff, the sp-h0kr
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

// releaseShipAssignments releases all ship assignments for this container
// flowRemovalWanted reports whether a container exit for the given reason is
// terminal and should drop the container's flow from the read-only feed. Only the
// resumable "canceled" reason (ctx-cancel before re-adoption) preserves the entry,
// so the re-adopted container keeps its flow until it re-publishes (sp-7yej inv-4).
func flowRemovalWanted(reason string) bool {
	return reason != "canceled"
}

func (r *ContainerRunner) releaseShipAssignments(reason string) {
	if flowRemovalWanted(reason) {
		flowfeed.Remove(r.containerEntity.ID())
	}
	if r.shipRepo == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
	defer cancel()

	playerID := shared.MustNewPlayerID(r.containerEntity.PlayerID())
	assignedShips, err := r.shipRepo.FindByContainer(ctx, r.containerEntity.ID(), playerID)
	if err != nil {
		r.log("ERROR", fmt.Sprintf("Failed to find ships for container: %v", err), nil)
		return
	}

	for _, ship := range assignedShips {
		ship.ForceRelease(reason, r.clock)
		if err := r.shipRepo.Save(ctx, ship); err != nil {
			r.log("ERROR", fmt.Sprintf("Failed to release ship %s: %v", ship.ShipSymbol(), err), nil)
		}
	}

	if len(assignedShips) > 0 {
		r.log("INFO", fmt.Sprintf("Released %d ship assignments (reason: %s)", len(assignedShips), reason), nil)
	}
}

package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// terminalizeClaimFailure marks the container row FAILED when Start() cannot claim
// its ship (already assigned to a different container, or reserved by the captain).
// This is the claim-failure exit path: the row was just persisted RUNNING above, but
// neither the heartbeat nor the execute goroutine ever starts on this path - so
// without this, the row is a zombie stuck at RUNNING with a heartbeat_at that never
// advances again, and the watchkeeper spams heartbeat_lost for it forever.
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
		// here, not in handleError (sp-v63s: it must not, or a restarting
		// container would be dropped from recovery).
		vetoErr := fmt.Errorf("completion refused (honest-completion contract): %s", incompleteReason)
		r.handleError(vetoErr)
		r.persistFailed(vetoErr.Error())

		// Release before signaling, mirroring the completed path's ordering
		// (the coordinator must never discover a still-claimed hull after the
		// completion event lands).
		//
		// The reason NAMES the veto rather than the generic "failed" every other
		// failure stamps. The runner knows exactly why this run was refused — the
		// veto reason is in hand right above — and the hull's release reason is the
		// only channel that survives the container's death. Stamped generically, a
		// hull that came back holding cargo it could not sell is indistinguishable
		// from one whose lane merely had a routing blip, so the fleet coordinator
		// relaunches an identical run onto the identical dead ground, which
		// re-inherits the unsold obligation and is refused again. Naming it lets the
		// next tick route the hull out instead (RULINGS #2 — the decision is derived
		// from durable state; nothing is held between ticks).
		r.releaseShipAssignments(common.ReleaseReasonHonestCompletionVeto)
		r.signalCompletionWithStatus(false, incompleteReason)
		return
	}

	r.mu.Lock()
	r.containerEntity.Complete()
	r.mu.Unlock()

	metrics.RecordContainerCompletion(r.containerEntity)
	metrics.RecordContainerExit(r.containerEntity)

	r.log("INFO", "Container completed successfully", map[string]interface{}{
		"iterations": r.containerEntity.CurrentIteration(),
		"runtime":    r.containerEntity.RuntimeDuration().String(),
	})

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
	shipSymbol, hasShip := metadata["ship_symbol"].(string)
	coordinatorID, _ := metadata["coordinator_id"].(string)

	// Both captain events fire BEFORE the nil-publisher return, so they reach the
	// watchkeeper even when no coordinator is wired.
	r.recordWorkflowCompletionEvent(success, errMsg, shipSymbol)
	r.recordContractCompletionEvent(success, errMsg, shipSymbol, coordinatorID)

	publisher := resolveWorkerPublisher(r.eventPublisher)
	if publisher == nil {
		return
	}

	if !hasShip {
		// A container with no ship_symbol is ship-less BY DESIGN. Only one whose coordinator IS
		// awaiting a signal but cannot name its ship is a real defect.
		if coordinatorID != "" {
			r.log("WARNING", "No ship_symbol in metadata, cannot signal completion to awaiting coordinator", map[string]interface{}{
				"coordinator_id": coordinatorID,
			})
		} else {
			r.log("DEBUG", "Ship-less container clean exit; no worker completion to signal", nil)
		}
		return
	}

	publisher.PublishWorkerCompleted(navigation.WorkerCompletedEvent{
		ContainerID:   r.containerEntity.ID(),
		PlayerID:      r.containerEntity.PlayerID(),
		ShipSymbol:    shipSymbol,
		CoordinatorID: coordinatorID,
		Success:       success,
		Error:         errMsg,
	})
	r.log("INFO", fmt.Sprintf("Published completion event for ship %s (success=%t)", shipSymbol, success), nil)
}

// recordWorkflowCompletionEvent emits workflow.finished/failed. finished is SCOPED by
// parentage: a parented container's exit is consumed by its parent, so an event is noise.
func (r *ContainerRunner) recordWorkflowCompletionEvent(success bool, errMsg, shipSymbol string) {
	if success && r.containerEntity.ParentContainerID() != nil {
		return
	}
	eventType := captain.EventWorkflowFinished
	if !success {
		eventType = captain.EventWorkflowFailed
	}
	recordCaptainEvent(eventType, shipSymbol, r.containerEntity.PlayerID(), map[string]any{
		"container_id": r.containerEntity.ID(),
		"command_type": string(r.containerEntity.Type()),
		"success":      success,
		"error":        errMsg,
	})
}

// recordContractCompletionEvent gives a terminal contract workflow a contract-grade signal.
// A parked run is a deliberate loop exit — neither completion nor failure — so both suppress.
func (r *ContainerRunner) recordContractCompletionEvent(success bool, errMsg, shipSymbol, coordinatorID string) {
	if r.containerEntity.Type() != container.ContainerTypeContractWorkflow {
		return
	}
	r.mu.RLock()
	parked := r.contractRunParked
	r.mu.RUnlock()
	if success && parked {
		return
	}

	contractEvent := captain.EventContractCompleted
	if !success {
		contractEvent = captain.EventContractFailed
	}
	recordCaptainEvent(contractEvent, shipSymbol, r.containerEntity.PlayerID(), map[string]any{
		"container_id":   r.containerEntity.ID(),
		"coordinator_id": coordinatorID,
		"success":        success,
		"error":          errMsg,
	})
}

// handleError handles execution errors
// NOTE: This does NOT signal completion or release ships - that's done by the caller
// AFTER determining whether to restart. This prevents premature ship release before restart.
func (r *ContainerRunner) handleError(err error) {
	r.log("ERROR", err.Error(), nil)

	r.mu.Lock()
	r.containerEntity.Fail(err)
	r.mu.Unlock()

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
	// and the lost-guard only diffs THAT candidate set, so a FAILED-but-alive
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
	// STOP THE HEARTBEAT BEFORE STAMPING THE ROW DEAD. The crash path
	// (execute's unrecoverable-error branch) reached here without stopping it, so the
	// heartbeat goroutine outlived the container it was reporting on and kept writing
	// heartbeat_at to a FAILED row until the daemon itself died — up to 2h39m of
	// post-mortem liveness, on 16 live rows. Any consumer reading a fresh heartbeat as
	// proof of life was being lied to by a corpse.
	//
	// Placed HERE rather than at the call site so every terminal-FAILED writer inherits
	// it, present and future. Idempotent (sync.Once), so finishCleanExit's veto path —
	// which already stopped the heartbeat before calling this — is unaffected, and the
	// ordering it establishes is what makes heartbeat_at <= stopped_at hold: stopHeartbeat
	// waits for the goroutine to return, so an in-flight heartbeat write completes BEFORE
	// the status write below rather than landing after it.
	//
	// This does NOT make liveness depend on the heartbeat: nothing here infers life from a
	// beat. It only stops the runner emitting a signal it knows to be false.
	r.stopHeartbeat()

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

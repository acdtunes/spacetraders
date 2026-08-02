package grpc

// orphaned_container_reap.go: the single seam that terminalizes a container whose
// hull was taken out from under it.
//
// Severing a live work-claim (ReleaseContainerClaim / PreemptForCaptain — `fleet unassign`,
// `ship reserve --force`, depot re-dedication, depot eviction) frees the HULL atomically and
// correctly. What it does NOT do is anything at all to the container that was flying that hull.
// That container's runner goroutine keeps iterating — navigating, buying and selling real cargo
// for real credits — on a hull it no longer owns, and its row stays RUNNING and heartbeating
// indefinitely. The coordinator reads ownership from the hull's single assignment row, so the
// orphan is invisible to it, and it promptly launches a SECOND container onto the same hull.
//
// Nothing reconciles the two until the next daemon RESTART, when recovery tries to re-adopt the
// orphan and fails "ship X is already assigned to container Y". Measured on the live fleet:
// tour-run-TORWIND-61-f0c2c82f ran 4.0h and tour-run-TORWIND-D7-f8033506 2.9h past losing their
// hulls (both to a `fleet unassign`, still logging jumps and trades throughout), and both were
// cleared only by that restart sweep — so uptime made the leak worse, not better. The phantom
// RUNNING rows are also what made two hulls measure at over 100% utilisation.
//
// ReapOrphanedContainer closes the gap at the moment the claim breaks. It does NOT touch the
// hull or weaken any claim guard (RULINGS #4/#7): the reassignment that orphaned the container
// is correct and stands. Only the loser is cleaned up.

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ReapOrphanedContainer terminalizes the container that a just-severed work-claim orphaned, so a
// hull is never flown by two containers and no phantom RUNNING row survives to the next restart.
//
// containerID == "" means the break was a no-op (the hull was already idle) — nothing to reap.
//
// Two cases, because an orphan can outlive the process that ran it:
//
//   - LIVE runner in this daemon: stop it through the daemon's normal StopContainer path — the
//     same path the sp-m3122 tour watchdog and the captain's manual `container stop` use. That
//     cancels the runner's context, waits for the iteration loop to exit, persists STOPPED, and
//     releases assignments. The release is keyed on container_id, which the claim break has
//     ALREADY cleared, so it finds nothing and cannot disturb the hull's NEW owner.
//
//   - No live runner (the row survived a restart that the goroutine did not): the row itself is
//     terminalized. A RUNNING/INTERRUPTED/PENDING row is exactly what the boot recovery sweep
//     re-adopts, so leaving it is what lets a dead container come back and fight for a hull it
//     lost; STOPPED is outside the recovery set and cannot be resurrected.
//
// Best-effort by construction: this is cleanup AFTER the authoritative claim write has already
// committed, so a failure here must never fail the caller's operation. Every outcome is logged.
func (s *DaemonServer) ReapOrphanedContainer(ctx context.Context, containerID string, playerID shared.PlayerID, reason string) {
	if containerID == "" {
		return // the break was a no-op — the hull was already idle, so nothing was orphaned
	}

	s.containersMu.RLock()
	_, live := s.containers[containerID]
	s.containersMu.RUnlock()

	if live {
		if err := s.StopContainer(containerID); err != nil {
			fmt.Printf("Warning: failed to stop orphaned container %s after its claim was broken (%s): %v\n",
				containerID, reason, err)
			return
		}
		fmt.Printf("Reaped orphaned container %s: its hull's work-claim was broken (%s), so it was stopped rather than left running a hull it no longer owns\n",
			containerID, reason)
		return
	}

	s.terminalizeOrphanedContainerRow(ctx, containerID, playerID, reason)
}

// terminalizeOrphanedContainerRow marks a resumable row STOPPED when the claim it held was broken
// but no runner for it lives in this daemon. Only RUNNING / INTERRUPTED / PENDING are rewritten:
// those are precisely the states boot recovery re-adopts (RecoverRunningContainers loads
// INTERRUPTED + RUNNING; a PENDING row holding a claim is the dead-CLI-runner artifact
// IsClaimOrphaned already recognises). An already-terminal row is left exactly as it is — its
// exit reason is history and must not be overwritten by this cleanup.
func (s *DaemonServer) terminalizeOrphanedContainerRow(ctx context.Context, containerID string, playerID shared.PlayerID, reason string) {
	if s.containerRepo == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, dbOperationTimeout)
	defer cancel()

	status, found, err := s.containerRepo.ContainerStatus(ctx, containerID, playerID)
	if err != nil {
		fmt.Printf("Warning: could not read status of orphaned container %s (%s): %v\n", containerID, reason, err)
		return
	}
	if !found {
		return // the row is already gone — nothing to terminalize
	}

	switch status {
	case string(container.ContainerStatusRunning),
		string(container.ContainerStatusInterrupted),
		string(container.ContainerStatusPending):
	default:
		return // already terminal — leave its recorded exit reason alone
	}

	now := time.Now()
	exitCode := 0
	exitReason := fmt.Sprintf("work_claim_broken: %s", reason)
	if err := s.containerRepo.UpdateStatus(
		ctx,
		containerID,
		playerID.Value(),
		container.ContainerStatusStopped,
		&now,
		&exitCode,
		exitReason,
	); err != nil {
		fmt.Printf("Warning: failed to terminalize orphaned container row %s (%s): %v\n", containerID, reason, err)
		return
	}
	fmt.Printf("Terminalized orphaned container row %s (was %s): its hull's work-claim was broken (%s), so it can no longer be re-adopted by recovery\n",
		containerID, status, reason)
}

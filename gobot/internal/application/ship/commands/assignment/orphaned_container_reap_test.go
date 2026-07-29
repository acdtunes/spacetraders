package assignment

// orphaned_container_reap_test.go — sp-h8mbb regression cover.
//
// Breaking a hull's live work-claim frees the HULL, but on its own does nothing to the container
// that was flying it: that container keeps navigating, buying and selling on a hull it no longer
// owns, and its row stays RUNNING until a daemon restart's recovery sweep fails it. Measured on
// the live fleet: tour-run-TORWIND-61-f0c2c82f ran 4.0h and tour-run-TORWIND-D7-f8033506 2.9h past
// losing their hulls to a `fleet unassign`, both still logging jumps and trades throughout.
//
// These tests pin the seam that closes it: every path that severs a live claim must hand the
// orphaned container to the reaper, naming exactly the container that lost the hull.

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// reapCall records one ReapOrphanedContainer invocation.
type reapCall struct {
	containerID string
	playerID    shared.PlayerID
	reason      string
}

// recordingReaper is the OrphanedContainerReaper test double. It records every call so a test can
// assert BOTH that the reap happened and that it named the right container — a reaper that fires
// on the wrong id would stop a healthy container and leave the orphan running.
type recordingReaper struct {
	calls []reapCall
}

func (r *recordingReaper) ReapOrphanedContainer(_ context.Context, containerID string, playerID shared.PlayerID, reason string) {
	r.calls = append(r.calls, reapCall{containerID: containerID, playerID: playerID, reason: reason})
}

// THE REGRESSION. `fleet unassign` on a hull a container is actively flying must reap that
// container. Without the reap the container keeps running the hull — the 4.0h zombie.
func TestUnassign_ReapsTheContainerTheClaimBreakOrphaned(t *testing.T) {
	const orphaned = "tour-run-TORWIND-61-f0c2c82f"

	repo := &assignStubShipRepo{
		ship:                 newFleetTestShip(t, "TORWIND-61", "FRAME_FREIGHTER", "HAULER", 40, "trade"),
		releaseClaimReleased: orphaned, // a LIVE tour held this hull
	}
	reaper := &recordingReaper{}
	handler := NewAssignShipFleetHandler(repo, nil)
	handler.SetOrphanedContainerReaper(reaper)

	pid := 5
	if _, err := handler.Handle(common.WithLogger(context.Background(), &captureLogger{}), &AssignShipFleetCommand{
		ShipSymbol:     "TORWIND-61",
		Fleet:          "",
		BreakWorkClaim: true,
		PlayerID:       &pid,
	}); err != nil {
		t.Fatalf("expected unassign+break to succeed, got: %v", err)
	}

	if len(reaper.calls) != 1 {
		t.Fatalf("breaking a LIVE work-claim must reap the orphaned container exactly once, got %d reap call(s) — "+
			"without it the container keeps flying a hull it no longer owns until the next daemon restart", len(reaper.calls))
	}
	if got := reaper.calls[0].containerID; got != orphaned {
		t.Fatalf("the reap must name the container that LOST the hull (%s), got %q — reaping the wrong id stops a healthy container and leaves the orphan running", orphaned, got)
	}
	if got := reaper.calls[0].playerID.Value(); got != pid {
		t.Fatalf("expected the reap scoped to player %d, got %d", pid, got)
	}
	if reaper.calls[0].reason == "" {
		t.Fatal("the reap must carry a reason — a container stopped with no explanation is indistinguishable from a crash in the logs")
	}
}

// A break that acted on NOTHING (the hull was already idle) must reap nothing. ReleaseContainerClaim
// reports "" for that no-op, and a reaper fired on "" would be a daemon-wide lookup for a container
// that never existed.
func TestUnassign_IdleHullReapsNothing(t *testing.T) {
	repo := &assignStubShipRepo{
		ship:                 newFleetTestShip(t, "TORWIND-61", "FRAME_FREIGHTER", "HAULER", 40, "trade"),
		releaseClaimReleased: "", // no live claim — the break was a no-op
	}
	reaper := &recordingReaper{}
	handler := NewAssignShipFleetHandler(repo, nil)
	handler.SetOrphanedContainerReaper(reaper)

	pid := 5
	if _, err := handler.Handle(common.WithLogger(context.Background(), &captureLogger{}), &AssignShipFleetCommand{
		ShipSymbol:     "TORWIND-61",
		Fleet:          "",
		BreakWorkClaim: true,
		PlayerID:       &pid,
	}); err != nil {
		t.Fatalf("expected unassign to succeed, got: %v", err)
	}

	if len(reaper.calls) != 0 {
		t.Fatalf("an already-idle hull orphans no container, so nothing may be reaped, got %d reap call(s): %+v", len(reaper.calls), reaper.calls)
	}
}

// A plain `fleet assign` — and the automated capacity reconcile, which shares this handler — never
// breaks a live claim, so it must never reap. A reconcile that reaped would stop running workers on
// every restart.
func TestAssign_NeverReaps(t *testing.T) {
	repo := &assignStubShipRepo{
		ship:                 newFleetTestShip(t, "TORWIND-19", "FRAME_FREIGHTER", "HAULER", 40, ""),
		releaseClaimReleased: "contract-worker-9", // would be reaped IF the break ever ran
	}
	reaper := &recordingReaper{}
	handler := NewAssignShipFleetHandler(repo, nil)
	handler.SetOrphanedContainerReaper(reaper)

	pid := 5
	if _, err := handler.Handle(context.Background(), &AssignShipFleetCommand{
		ShipSymbol: "TORWIND-19",
		Fleet:      "contract",
		PlayerID:   &pid,
		// BreakWorkClaim left false — the reconcile path.
	}); err != nil {
		t.Fatalf("expected assign to succeed, got: %v", err)
	}

	if len(reaper.calls) != 0 {
		t.Fatalf("a plain assign/reconcile breaks no claim, so it must reap nothing, got %d reap call(s): %+v", len(reaper.calls), reaper.calls)
	}
}

// A failed break orphaned nothing — the claim still stands and its container is still the rightful
// owner. Reaping there would stop a container that never lost its hull.
func TestUnassign_FailedBreakReapsNothing(t *testing.T) {
	repo := &assignStubShipRepo{
		ship:                 newFleetTestShip(t, "TORWIND-61", "FRAME_FREIGHTER", "HAULER", 40, "trade"),
		releaseClaimReleased: "tour-run-TORWIND-61-f0c2c82f",
		releaseClaimErr:      errors.New("db unavailable"),
	}
	reaper := &recordingReaper{}
	handler := NewAssignShipFleetHandler(repo, nil)
	handler.SetOrphanedContainerReaper(reaper)

	pid := 5
	if _, err := handler.Handle(common.WithLogger(context.Background(), &captureLogger{}), &AssignShipFleetCommand{
		ShipSymbol:     "TORWIND-61",
		Fleet:          "",
		BreakWorkClaim: true,
		PlayerID:       &pid,
	}); err == nil {
		t.Fatal("expected the unassign to surface the failed work-claim break")
	}

	if len(reaper.calls) != 0 {
		t.Fatalf("a break that FAILED left the claim standing, so its container is still the rightful owner and must not be reaped, got %d reap call(s): %+v", len(reaper.calls), reaper.calls)
	}
}

// `ship reserve --force` preempts a coordinator's live claim for the captain. The revoked
// container is orphaned exactly as unassign's is, so it must be reaped too — otherwise a hull the
// captain believes is theirs keeps being flown by a container nobody can see.
func TestReserveForce_ReapsThePreemptedContainer(t *testing.T) {
	const preempted = "tour-run-TORWIND-D7-f8033506"

	repo := &reserveStubShipRepo{preemptedFrom: preempted}
	reaper := &recordingReaper{}
	handler := NewReserveShipHandler(repo, nil)
	handler.SetOrphanedContainerReaper(reaper)

	pid := 5
	if _, err := handler.Handle(context.Background(), &ReserveShipCommand{
		ShipSymbol: "TORWIND-D7",
		Reason:     "captain errand",
		Force:      true,
		PlayerID:   &pid,
	}); err != nil {
		t.Fatalf("expected the forced reserve to succeed, got: %v", err)
	}

	if len(reaper.calls) != 1 {
		t.Fatalf("preempting a LIVE claim must reap the revoked container exactly once, got %d reap call(s) — "+
			"otherwise the coordinator's container keeps flying the hull the captain just took", len(reaper.calls))
	}
	if got := reaper.calls[0].containerID; got != preempted {
		t.Fatalf("the reap must name the preempted container (%s), got %q", preempted, got)
	}
}

// A non-forced `ship reserve` only ever succeeds on an idle hull (a live claim is rejected
// outright), so it orphans nothing and must reap nothing.
func TestReserveWithoutForce_ReapsNothing(t *testing.T) {
	repo := &reserveStubShipRepo{
		preemptedFrom: "tour-run-TORWIND-D7-f8033506", // would be reaped IF the preempt ever ran
	}
	reaper := &recordingReaper{}
	handler := NewReserveShipHandler(repo, nil)
	handler.SetOrphanedContainerReaper(reaper)

	pid := 5
	if _, err := handler.Handle(context.Background(), &ReserveShipCommand{
		ShipSymbol: "TORWIND-D7",
		Reason:     "captain errand",
		PlayerID:   &pid,
		// Force left false.
	}); err != nil {
		t.Fatalf("expected the reserve to succeed, got: %v", err)
	}

	if len(reaper.calls) != 0 {
		t.Fatalf("a non-forced reserve preempts nothing, so it must reap nothing, got %d reap call(s): %+v", len(reaper.calls), reaper.calls)
	}
}

// Any wiring that never sets a reaper (tests, a future embedder) must keep working: the break
// still happens, it simply is not followed by a reap. A nil-reaper panic would take out
// `fleet unassign` entirely.
func TestUnassign_NilReaperDoesNotPanic(t *testing.T) {
	repo := &assignStubShipRepo{
		ship:                 newFleetTestShip(t, "TORWIND-61", "FRAME_FREIGHTER", "HAULER", 40, "trade"),
		releaseClaimReleased: "tour-run-TORWIND-61-f0c2c82f",
	}
	handler := NewAssignShipFleetHandler(repo, nil) // no SetOrphanedContainerReaper

	pid := 5
	if _, err := handler.Handle(common.WithLogger(context.Background(), &captureLogger{}), &AssignShipFleetCommand{
		ShipSymbol:     "TORWIND-61",
		Fleet:          "",
		BreakWorkClaim: true,
		PlayerID:       &pid,
	}); err != nil {
		t.Fatalf("an unwired reaper must leave unassign working, got: %v", err)
	}
	if repo.releaseClaimCalled != 1 {
		t.Fatalf("the claim break must still run without a reaper, got %d call(s)", repo.releaseClaimCalled)
	}
}

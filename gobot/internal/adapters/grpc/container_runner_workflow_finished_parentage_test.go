package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// newParentageEventRunner builds a ContainerRunner around a container with the
// given parentContainerID, carrying the same ship_symbol/coordinator_id
// metadata newContractEventRunner uses. ctype is deliberately a non-contract
// type in most callers so the parentage matrix is isolated from the
// contract.* branch already covered by container_runner_contract_event_test.go.
func newParentageEventRunner(t *testing.T, ctype container.ContainerType, parentContainerID *string) *ContainerRunner {
	t.Helper()
	entity := container.NewContainer(
		"parentage-work-TORWIND-9-xyz",
		ctype,
		3, // playerID
		-1,
		parentContainerID,
		map[string]interface{}{
			"ship_symbol":    "TORWIND-9",
			"coordinator_id": "coord-abc",
		},
		nil,
	)
	require.NoError(t, entity.Start())
	return NewContainerRunner(entity, nil, nil, noopLogRepo{}, nil, nil, nil)
}

// TestSignalCompletion_CoordinatorSpawnedCleanExit_SuppressesWorkflowFinished
// pins sp-6g96 Fix 2's core behavior (bead-validated matrix: "coordinator-spawned
// clean exit -> no event"): a coordinator-spawned (parented) container's clean
// exit is the ordinary, self-absorbed rotation completion the bead identifies as
// producing dozens of clean exits/hour by design — the parent coordinator itself
// consumes the completion and acts, so a generic workflow.finished to the
// watchkeeper is pure noise for this case and must be suppressed.
func TestSignalCompletion_CoordinatorSpawnedCleanExit_SuppressesWorkflowFinished(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	parent := "coordinator-parent-1"
	r := newParentageEventRunner(t, container.ContainerTypeTrading, &parent)
	r.signalCompletionWithStatus(true, "")

	require.Zero(t, countEvents(rec.events, captain.EventWorkflowFinished),
		"a coordinator-spawned container's clean exit must not emit workflow.finished")
}

// TestSignalCompletion_ParentlessCleanExit_EmitsWorkflowFinished pins the other
// half of the matrix ("parentless clean exit -> event"): a parentless
// (human/CLI-launched) container's clean exit — captain-authority moves, manual
// liquidations, canaries, one-shot ferries — has no coordinator waiting on it,
// so workflow.finished must still fire; a human (or a wake.watch armed on this
// container, sp-oyer) is the only consumer of that completion signal.
func TestSignalCompletion_ParentlessCleanExit_EmitsWorkflowFinished(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	r := newParentageEventRunner(t, container.ContainerTypeTrading, nil)
	r.signalCompletionWithStatus(true, "")

	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFinished),
		"a parentless container's clean exit must still emit workflow.finished")
}

// TestSignalCompletion_CoordinatorSpawnedFailure_EmitsWorkflowFailedUnconditionally
// pins the third matrix cell ("either failed -> event"): workflow.failed stays
// unconditional regardless of parentage. A coordinator-spawned worker's failure
// is still a signal worth the watchkeeper's attention — unlike a routine clean
// exit, a failure is never noise — so the parentage scoping applies only to the
// success path.
func TestSignalCompletion_CoordinatorSpawnedFailure_EmitsWorkflowFailedUnconditionally(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	parent := "coordinator-parent-2"
	r := newParentageEventRunner(t, container.ContainerTypeTrading, &parent)
	r.signalCompletionWithStatus(false, "iteration error: X1-TEST market unreachable")

	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFailed),
		"a coordinator-spawned container's failure must still emit workflow.failed unconditionally")
	require.Zero(t, countEvents(rec.events, captain.EventWorkflowFinished),
		"a failed run must not additionally emit workflow.finished")
}

// TestSignalCompletion_ParentlessFailure_EmitsWorkflowFailed rounds out the
// matrix for the parentless side of "either failed -> event": unchanged from
// pre-sp-6g96 behavior, but pinned explicitly here alongside its parented
// sibling above so the full 3-case matrix lives in one file.
func TestSignalCompletion_ParentlessFailure_EmitsWorkflowFailed(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	r := newParentageEventRunner(t, container.ContainerTypeTrading, nil)
	r.signalCompletionWithStatus(false, "iteration error: X1-TEST market unreachable")

	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFailed),
		"a parentless container's failure must emit workflow.failed")
	require.Zero(t, countEvents(rec.events, captain.EventWorkflowFinished),
		"a failed run must not additionally emit workflow.finished")
}

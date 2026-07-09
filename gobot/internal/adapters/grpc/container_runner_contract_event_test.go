package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// newContractEventRunner builds a ContainerRunner around a container of the
// given type carrying the ship_symbol/coordinator_id metadata a real workflow
// container is wired with, so signalCompletionWithStatus has the metadata its
// contract.* payload reads. The publisher is left unwired (nil) so completion
// records the strategic events then early-returns before the worker-completed
// publish path.
func newContractEventRunner(t *testing.T, ctype container.ContainerType) *ContainerRunner {
	t.Helper()
	entity := container.NewContainer(
		"contract-work-TORWIND-3-abc",
		ctype,
		2, // playerID
		-1,
		nil,
		map[string]interface{}{
			"ship_symbol":    "TORWIND-3",
			"coordinator_id": "coord-xyz",
		},
		nil,
	)
	require.NoError(t, entity.Start())
	return NewContainerRunner(entity, nil, nil, noopLogRepo{}, nil, nil, nil)
}

func findEvent(events []*captain.Event, target captain.EventType) *captain.Event {
	for _, e := range events {
		if e.Type == target {
			return e
		}
	}
	return nil
}

// (a) A contract workflow reaching success must emit a first-class
// contract.completed event carrying the ship, player, container id, and
// coordinator id — the contract-grade signal the watchkeeper needs instead of
// only a generic workflow.finished (sp-82qs; the enum was previously dead).
func TestSignalCompletion_ContractWorkflowSuccess_EmitsContractCompleted(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	r := newContractEventRunner(t, container.ContainerTypeContractWorkflow)
	r.signalCompletionWithStatus(true, "")

	require.Equal(t, 1, countEvents(rec.events, captain.EventContractCompleted),
		"a contract workflow reaching success must emit exactly one contract.completed")
	ev := findEvent(rec.events, captain.EventContractCompleted)
	require.NotNil(t, ev)
	require.Equal(t, "TORWIND-3", ev.Ship)
	require.Equal(t, 2, ev.PlayerID)
	require.Contains(t, ev.Payload, "contract-work-TORWIND-3-abc", "payload must carry container_id")
	require.Contains(t, ev.Payload, "coord-xyz", "payload must carry coordinator_id")

	// Additive: the generic workflow.finished still fires for all container types.
	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFinished),
		"contract.completed is additive; generic workflow.finished must still fire")
}

// (b) A contract workflow that fails must emit contract.failed (not
// contract.completed) with the failure signature carried in its payload, so
// the failure is a contract-grade interrupt (contract.failed is interrupt
// class per DefaultInterruptTypes) rather than a generic workflow.failed alone.
func TestSignalCompletion_ContractWorkflowFailure_EmitsContractFailed(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	r := newContractEventRunner(t, container.ContainerTypeContractWorkflow)
	r.signalCompletionWithStatus(false, "contract fulfillment failed: API 4602 deliver terms unmet")

	require.Equal(t, 1, countEvents(rec.events, captain.EventContractFailed),
		"a contract workflow failing must emit exactly one contract.failed")
	require.Zero(t, countEvents(rec.events, captain.EventContractCompleted),
		"a failed contract must not emit contract.completed")
	ev := findEvent(rec.events, captain.EventContractFailed)
	require.NotNil(t, ev)
	require.Equal(t, "TORWIND-3", ev.Ship)
	require.Equal(t, 2, ev.PlayerID)
	require.Contains(t, ev.Payload, "4602", "payload must carry the failure error signature")

	// Additive: the generic workflow.failed still fires.
	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFailed),
		"contract.failed is additive; generic workflow.failed must still fire")
}

// (c) A non-contract container reaching a terminal state must emit ZERO
// contract.* events while the generic workflow.finished still fires — the
// branch must be scoped strictly to CONTRACT_WORKFLOW containers.
func TestSignalCompletion_NonContractContainer_EmitsNoContractEvents(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	// A trade-route/trading container is not a contract workflow.
	r := newContractEventRunner(t, container.ContainerTypeTrading)
	r.signalCompletionWithStatus(true, "")

	require.Zero(t, countEvents(rec.events, captain.EventContractCompleted),
		"a non-contract container must not emit contract.completed")
	require.Zero(t, countEvents(rec.events, captain.EventContractFailed),
		"a non-contract container must not emit contract.failed")

	// The generic strategic event must still fire for non-contract containers.
	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFinished),
		"the generic workflow.finished must still fire for non-contract containers")
}

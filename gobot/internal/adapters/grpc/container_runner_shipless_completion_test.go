package grpc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// newCompletionRunner builds a ContainerRunner around a container carrying the
// supplied metadata, with a fakePublisher wired as the instance publisher so the
// completion-signal path (which early-returns when no publisher resolves) is
// actually exercised. The returned publisher records any WorkerCompletedEvent.
func newCompletionRunner(t *testing.T, ctype container.ContainerType, metadata map[string]interface{}) (*ContainerRunner, *fakePublisher) {
	t.Helper()
	entity := container.NewContainer(
		"completion-"+string(ctype),
		ctype,
		3,   // playerID
		-1,  // maxIterations
		nil, // parentless (coordinator/CLI-launched); orthogonal to this path
		metadata,
		nil,
	)
	require.NoError(t, entity.Start())
	runner := NewContainerRunner(entity, nil, nil, noopLogRepo{}, nil, nil, nil)
	pub := &fakePublisher{}
	runner.SetEventPublisher(pub)
	return runner, pub
}

// countCompletionWarnings counts WARNING-level logs whose message references the
// completion-signal path — the cry-wolf line this bead (sp-hehz) targets.
func countCompletionWarnings(r *ContainerRunner) int {
	n := 0
	for _, entry := range r.GetLogs(nil, nil) {
		if entry.Level == "WARNING" && strings.Contains(entry.Message, "signal completion") {
			n++
		}
	}
	return n
}

// TestSignalCompletion_ShiplessCoordinator_NoCryWolfWarning pins the sp-hehz fix:
// coordinator/dispatcher container types (goods_factory / manufacturing
// coordinator, scout-fleet-assignment, siting, ...) are ship-less BY DESIGN — the
// codebase already treats a missing "ship_symbol" as a legitimate no-op at
// createShipAssignments ("No ship_symbol in config = no ships to assign (e.g.
// scout-fleet-assignment)"). Such a container has no single ship to name AND no
// coordinator awaiting a WorkerCompletedEvent, so its every clean exit must NOT
// emit the "No ship_symbol in metadata, cannot signal completion" WARNING.
func TestSignalCompletion_ShiplessCoordinator_NoCryWolfWarning(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	// Scout-fleet-assignment: the "scout" coordinator named in the bead. No
	// ship_symbol and no coordinator_id — nobody is waiting on a completion signal.
	r, pub := newCompletionRunner(t, container.ContainerTypeScoutFleetAssignment, map[string]interface{}{})
	r.signalCompletionWithStatus(true, "")

	require.Zero(t, countCompletionWarnings(r),
		"a ship-less coordinator's clean exit must not cry wolf about a missing ship_symbol")
	require.Empty(t, pub.events,
		"a ship-less container has no worker completion to publish")
}

// TestSignalCompletion_ManufacturingCoordinator_NoCryWolfWarning pins the
// "goods_factory" half of the bead symptom with a second representative ship-less
// coordinator type, guarding against a fix that special-cases only scouts.
func TestSignalCompletion_ManufacturingCoordinator_NoCryWolfWarning(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	r, pub := newCompletionRunner(t, container.ContainerTypeManufacturingCoordinator, map[string]interface{}{})
	r.signalCompletionWithStatus(true, "")

	require.Zero(t, countCompletionWarnings(r),
		"a ship-less manufacturing coordinator's clean exit must not cry wolf")
	require.Empty(t, pub.events)
}

// TestSignalCompletion_CoordinatorAwaitingButNoShip_StillWarns pins the genuine
// "should-have-signalled-but-couldn't" case that MUST still surface: a container
// that carries a coordinator_id (a coordinator IS subscribed on that id and
// waiting) but somehow reaches completion with no ship_symbol is a real defect —
// the coordinator will never get its WorkerCompletedEvent — so the WARNING must
// still fire. This is the discriminator that separates the benign coordinator
// path (silenced above) from a real wiring bug (kept loud here).
func TestSignalCompletion_CoordinatorAwaitingButNoShip_StillWarns(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	r, pub := newCompletionRunner(t, container.ContainerTypeTrading, map[string]interface{}{
		"coordinator_id": "coord-awaiting-1",
	})
	r.signalCompletionWithStatus(true, "")

	require.Equal(t, 1, countCompletionWarnings(r),
		"a container whose coordinator is awaiting a completion signal must still warn when it cannot name its ship")
	require.Empty(t, pub.events,
		"with no ship_symbol there is still nothing to publish")
}

// TestSignalCompletion_ShipWorker_PublishesWithoutWarning guards the happy path:
// a single-ship worker with a ship_symbol publishes its WorkerCompletedEvent and
// emits no completion warning at all.
func TestSignalCompletion_ShipWorker_PublishesWithoutWarning(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	r, pub := newCompletionRunner(t, container.ContainerTypeTrading, map[string]interface{}{
		"ship_symbol":    "TORWIND-9",
		"coordinator_id": "coord-abc",
	})
	r.signalCompletionWithStatus(true, "")

	require.Zero(t, countCompletionWarnings(r),
		"a ship-carrying worker must not emit the missing-ship_symbol warning")
	require.Len(t, pub.events, 1,
		"a ship-carrying worker publishes exactly one WorkerCompletedEvent")
	require.Equal(t, "TORWIND-9", pub.events[0].ShipSymbol)
}

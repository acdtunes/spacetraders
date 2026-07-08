package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// sp-86yb: even when a storage_operations row is (stale-)RUNNING, a manufacturing
// coordinator must not spawn STORAGE_ACQUIRE_DELIVER against a coordinator whose
// container has already died or stopped - that ship is empty and no longer being
// worked, so every such task would fail forever (the storage wedge). This is
// defense-in-depth alongside DaemonServer.StopContainer terminalizing the row
// on stop: whatever the reason a row is left stale-RUNNING, the coordinator's
// actual container liveness is now consulted before it's ever treated as a
// live source.
func TestFindRunningOperationForGoodExcludesDeadCoordinator(t *testing.T) {
	op := newTestGasSiphonOperation(t, "gas_coordinator-TORWIND-9-dead", 1, "LIQUID_HYDROGEN")
	repo := &pinStubStorageOpRepo{operations: []*storage.StorageOperation{op}}
	reader := &fakeContainerStatusReader{status: string(container.ContainerStatusStopped), found: true}
	finder := NewStorageSourceFinder(repo, reader)

	found := finder.FindRunningOperationForGood(context.Background(), 1, "LIQUID_HYDROGEN")

	require.Nil(t, found, "must not return a storage source whose coordinator container is STOPPED")
}

// The mirror image of the above: a genuinely alive coordinator (container
// confirmed RUNNING) must still be found - the liveness gate must not become a
// blanket regression that breaks the feature it's protecting.
func TestFindRunningOperationForGoodReturnsLiveCoordinator(t *testing.T) {
	op := newTestGasSiphonOperation(t, "gas_coordinator-TORWIND-9-alive", 1, "LIQUID_HYDROGEN")
	repo := &pinStubStorageOpRepo{operations: []*storage.StorageOperation{op}}
	reader := &fakeContainerStatusReader{status: string(container.ContainerStatusRunning), found: true}
	finder := NewStorageSourceFinder(repo, reader)

	found := finder.FindRunningOperationForGood(context.Background(), 1, "LIQUID_HYDROGEN")

	require.NotNil(t, found, "a coordinator confirmed RUNNING must still be usable as a storage source")
	require.Equal(t, op.ID(), found.ID())
}

// A container row that no longer exists at all (found=false) is exactly as dead
// as an explicitly STOPPED one for this purpose - both must exclude the source.
func TestFindRunningOperationForGoodExcludesMissingCoordinatorContainer(t *testing.T) {
	op := newTestGasSiphonOperation(t, "gas_coordinator-TORWIND-9-gone", 1, "LIQUID_HYDROGEN")
	repo := &pinStubStorageOpRepo{operations: []*storage.StorageOperation{op}}
	reader := &fakeContainerStatusReader{found: false}
	finder := NewStorageSourceFinder(repo, reader)

	found := finder.FindRunningOperationForGood(context.Background(), 1, "LIQUID_HYDROGEN")

	require.Nil(t, found, "must not return a storage source whose coordinator container row no longer exists")
}

// With no reader wired at all, the liveness gate is disabled and behavior falls
// back to trusting the storage_operations row status alone - this is the
// pre-sp-86yb behavior and must keep working for any caller that hasn't wired a
// ContainerStatusReader (e.g. constructed via NewStorageSourceFinder(repo, nil)).
func TestFindRunningOperationForGoodTrustsRowStatusWhenNoReaderWired(t *testing.T) {
	op := newTestGasSiphonOperation(t, "gas_coordinator-TORWIND-9-unwired", 1, "LIQUID_HYDROGEN")
	repo := &pinStubStorageOpRepo{operations: []*storage.StorageOperation{op}}
	finder := NewStorageSourceFinder(repo, nil)

	found := finder.FindRunningOperationForGood(context.Background(), 1, "LIQUID_HYDROGEN")

	require.NotNil(t, found, "with no liveness reader wired, the row status alone must still be trusted")
	require.Equal(t, op.ID(), found.ID())
}

// fakeContainerStatusReader is a direct stub of ContainerStatusReader letting
// each test dictate exactly the (status, found, err) triple the coordinator
// liveness check observes, independent of any real container/persistence wiring.
type fakeContainerStatusReader struct {
	status string
	found  bool
	err    error
}

func (r *fakeContainerStatusReader) ContainerStatus(_ context.Context, _ string, _ shared.PlayerID) (string, bool, error) {
	return r.status, r.found, r.err
}

func newTestGasSiphonOperation(t *testing.T, id string, playerID int, good string) *storage.StorageOperation {
	t.Helper()
	op, err := storage.NewStorageOperation(
		id,
		playerID,
		"X1-PIN-GAS",
		storage.OperationTypeGasSiphon,
		[]string{"SHIP-EXT-1"},
		[]string{"SHIP-STORE-1"},
		[]string{good},
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, op.Start())
	return op
}

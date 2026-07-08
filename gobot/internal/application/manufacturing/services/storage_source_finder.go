package services

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// ContainerStatusReader reports the lifecycle status of a container, so
// StorageSourceFinder can tell a genuinely running storage coordinator apart from
// one whose container has already died/stopped while its storage_operations row
// is still (stale-)RUNNING.
//
// This is defense-in-depth for sp-86yb: DaemonServer.StopContainer terminalizes a
// gas coordinator's storage_operations row when its container is stopped, but this
// second, independent check protects manufacturing coordinators against ANY row
// left stale-RUNNING - whatever the cause - so a dead coordinator can never again
// cause STORAGE_ACQUIRE_DELIVER tasks to pile up against an empty, agentless ship.
//
// found=false means the container row no longer exists.
type ContainerStatusReader interface {
	ContainerStatus(ctx context.Context, containerID string, playerID shared.PlayerID) (status string, found bool, err error)
}

// StorageSourceFinder checks if there's a running storage operation that
// produces a specified good. This enables integration between gas siphoning
// operations and the manufacturing pipeline - instead of buying gases from market,
// haulers can pick up cargo directly from storage ships at the extraction site.
type StorageSourceFinder struct {
	storageOpRepo   storage.StorageOperationRepository
	containerReader ContainerStatusReader
}

// NewStorageSourceFinder creates a new StorageSourceFinder.
//
// containerReader may be nil to disable the coordinator-liveness check, trusting
// the storage_operations row status alone (pre-sp-86yb behavior).
func NewStorageSourceFinder(storageOpRepo storage.StorageOperationRepository, containerReader ContainerStatusReader) *StorageSourceFinder {
	return &StorageSourceFinder{storageOpRepo: storageOpRepo, containerReader: containerReader}
}

// FindRunningOperationForGood returns the first RUNNING storage operation that
// supports the specified good AND whose coordinator container is confirmed alive,
// nil otherwise.
func (f *StorageSourceFinder) FindRunningOperationForGood(ctx context.Context, playerID int, good string) *storage.StorageOperation {
	if f == nil || f.storageOpRepo == nil {
		return nil
	}

	logger := common.LoggerFromContext(ctx)

	// Find storage operations that support this good
	operations, err := f.storageOpRepo.FindByGood(ctx, playerID, good)
	if err != nil {
		logger.Log("WARN", "Storage operation lookup: FindByGood failed", map[string]interface{}{
			"good":      good,
			"player_id": playerID,
			"error":     err.Error(),
		})
		return nil
	}

	// Return the first RUNNING operation that supports this good AND whose
	// coordinator container is confirmed alive (sp-86yb).
	for _, op := range operations {
		if !op.IsRunning() || !op.SupportsGood(good) {
			continue
		}
		if !f.isCoordinatorAlive(ctx, op.ID(), playerID) {
			logger.Log("WARN", "Storage operation lookup: skipping source with dead/stopped coordinator", map[string]interface{}{
				"good":         good,
				"player_id":    playerID,
				"operation_id": op.ID(),
			})
			continue
		}
		return op
	}

	return nil
}

// isCoordinatorAlive reports whether the container coordinating a storage
// operation is confirmed RUNNING.
//
// With no reader wired (containerReader == nil), it passes through true - the
// storage_operations row status remains the sole signal, matching pre-sp-86yb
// behavior. Once a reader IS wired, any uncertainty (read error, container row
// gone, non-RUNNING status) is treated as NOT alive: this is a liveness gate, so
// "can't confirm alive" must mean "don't route deliveries here" - the fallback is
// the existing, already-proven market-purchase path, not a wedge.
func (f *StorageSourceFinder) isCoordinatorAlive(ctx context.Context, containerID string, playerID int) bool {
	if f.containerReader == nil {
		return true
	}

	status, found, err := f.containerReader.ContainerStatus(ctx, containerID, shared.MustNewPlayerID(playerID))
	if err != nil || !found {
		return false
	}
	return status == string(container.ContainerStatusRunning)
}

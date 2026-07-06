package services

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// StorageSourceFinder checks if there's a running storage operation that
// produces a specified good. This enables integration between gas siphoning
// operations and the manufacturing pipeline - instead of buying gases from market,
// haulers can pick up cargo directly from storage ships at the extraction site.
type StorageSourceFinder struct {
	storageOpRepo storage.StorageOperationRepository
}

func NewStorageSourceFinder(storageOpRepo storage.StorageOperationRepository) *StorageSourceFinder {
	return &StorageSourceFinder{storageOpRepo: storageOpRepo}
}

// FindRunningOperationForGood returns the first RUNNING storage operation
// that supports the specified good, nil otherwise.
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

	// Return the first RUNNING operation that supports this good
	for _, op := range operations {
		if op.IsRunning() && op.SupportsGood(good) {
			return op
		}
	}

	return nil
}

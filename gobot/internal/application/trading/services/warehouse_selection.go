package services

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// SelectNewestRunningWarehouse deterministically resolves ties when a caller's own
// filter (same waypoint, or same reachable-system graph) matches more than one
// operation in ops: it returns the one with the latest CreatedAt, breaking an exact
// timestamp tie by the higher ID for full determinism. Returns nil for an empty
// slice.
//
// Ties happen when a container stops without terminalizing its storage_operations
// row (sp-3lj5): the stale "zombie" row keeps reading RUNNING forever alongside its
// live replacement (e.g. warehouse-TORWIND-12-bad719ff, stopped at 15:24Z but never
// terminalized, next to the live warehouse-TORWIND-12-3477282e at the same
// waypoint). A naive first-match or lowest-ID pick can silently resolve to the dead
// operation - whose registered storage ships are gone, so it always reads back
// zero free space - instead of the live one, and the caller wrongly concludes the
// warehouse is full.
func SelectNewestRunningWarehouse(ops []*storage.StorageOperation) *storage.StorageOperation {
	var best *storage.StorageOperation
	for _, op := range ops {
		if best == nil || op.CreatedAt().After(best.CreatedAt()) ||
			(op.CreatedAt().Equal(best.CreatedAt()) && op.ID() > best.ID()) {
			best = op
		}
	}
	return best
}

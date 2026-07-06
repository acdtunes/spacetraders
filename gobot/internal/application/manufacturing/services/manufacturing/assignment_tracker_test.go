package manufacturing

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// Untracking a typed assignment must return its per-type allocation count to
// zero. The bug: Untrack never decremented tasksByType, so per-type counts
// grew monotonically until restart, skewing WorkerReservationPolicy decisions.
func TestUntrack_RestoresPerTypeAllocationCount(t *testing.T) {
	tracker := NewAssignmentTracker()

	tracker.Track("task-1", "SHIP-1", "container-1", manufacturing.TaskTypeCollectSell)
	tracker.Track("task-2", "SHIP-2", "container-2", manufacturing.TaskTypeAcquireDeliver)

	tracker.Untrack("task-1")
	tracker.Untrack("task-2")

	allocations := tracker.GetAllocations()
	if allocations.CollectSellCount != 0 {
		t.Errorf("CollectSellCount = %d, want 0 after untrack", allocations.CollectSellCount)
	}
	if allocations.AcquireDeliverCount != 0 {
		t.Errorf("AcquireDeliverCount = %d, want 0 after untrack", allocations.AcquireDeliverCount)
	}
}

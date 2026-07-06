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

func TestUntrack_UnknownOrRepeated_IsSafeNoOpAndNeverNegative(t *testing.T) {
	tracker := NewAssignmentTracker()

	tracker.Untrack("never-tracked")

	unknownAlloc := tracker.GetAllocations()
	if unknownAlloc.CollectSellCount < 0 || unknownAlloc.AcquireDeliverCount < 0 || unknownAlloc.TotalWorkers < 0 {
		t.Errorf("unknown untrack produced negative counts: %+v", unknownAlloc)
	}
	if unknownAlloc.TotalWorkers != 0 {
		t.Errorf("TotalWorkers = %d, want 0 after untracking unknown task", unknownAlloc.TotalWorkers)
	}
	if tracker.GetAssignmentCount() != 0 {
		t.Errorf("GetAssignmentCount = %d, want 0 after untracking unknown task", tracker.GetAssignmentCount())
	}

	tracker.Track("task-1", "SHIP-1", "container-1", manufacturing.TaskTypeCollectSell)
	tracker.Untrack("task-1")
	tracker.Untrack("task-1")
	tracker.Untrack("task-1")

	repeatedAlloc := tracker.GetAllocations()
	if repeatedAlloc.CollectSellCount < 0 || repeatedAlloc.AcquireDeliverCount < 0 || repeatedAlloc.TotalWorkers < 0 {
		t.Errorf("repeated untrack produced negative counts: %+v", repeatedAlloc)
	}
	if repeatedAlloc.CollectSellCount != 0 {
		t.Errorf("CollectSellCount = %d, want 0 after repeated untrack", repeatedAlloc.CollectSellCount)
	}
	if repeatedAlloc.TotalWorkers != 0 {
		t.Errorf("TotalWorkers = %d, want 0 after repeated untrack", repeatedAlloc.TotalWorkers)
	}
	if tracker.IsTaskAssigned("task-1") {
		t.Error("task-1 should not be assigned after untrack")
	}
}

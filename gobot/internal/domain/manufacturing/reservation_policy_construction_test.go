package manufacturing

import "testing"

// Regression guard: WorkerReservationPolicy.ShouldAssign must never skip a
// DELIVER_TO_CONSTRUCTION task, in any allocation state the gate can observe.
func TestShouldAssign_ConstructionIsNeverSkippedByReservationGate(t *testing.T) {
	policy := NewWorkerReservationPolicy()

	// Allocation state where the reservation gate actively skips ACQUIRE_DELIVER to
	// reserve workers for a starved COLLECT_SELL type: COLLECT_SELL is below minimum
	// and has ready tasks, so an ACQUIRE_DELIVER assignment would be deferred.
	gatingAlloc := TaskTypeAllocations{
		CollectSellCount:       0,
		AcquireDeliverCount:    MinAcquireDeliverWorkers, // at minimum, so not below
		HasReadyCollectSell:    true,
		HasReadyAcquireDeliver: false,
	}

	// Precondition: prove the gate really is type-selective in this state.
	if policy.ShouldAssign(TaskTypeAcquireDeliver, gatingAlloc) {
		t.Fatalf("test precondition broken: expected ACQUIRE_DELIVER to be skipped in gating alloc")
	}

	// Invariant: construction is dispatchable in that same gating state.
	if !policy.ShouldAssign(TaskTypeDeliverToConstruction, gatingAlloc) {
		t.Fatalf("DELIVER_TO_CONSTRUCTION was skipped by the reservation gate while a sibling was gated")
	}

	// Construction must be dispatchable across every allocation permutation the gate
	// can observe, so it can never be starved by the reservation policy.
	for _, csCount := range []int{0, MinCollectSellWorkers, MinCollectSellWorkers + 2} {
		for _, adCount := range []int{0, MinAcquireDeliverWorkers, MinAcquireDeliverWorkers + 2} {
			for _, csReady := range []bool{false, true} {
				for _, adReady := range []bool{false, true} {
					alloc := TaskTypeAllocations{
						CollectSellCount:       csCount,
						AcquireDeliverCount:    adCount,
						HasReadyCollectSell:    csReady,
						HasReadyAcquireDeliver: adReady,
					}
					if !policy.ShouldAssign(TaskTypeDeliverToConstruction, alloc) {
						t.Fatalf("DELIVER_TO_CONSTRUCTION skipped for alloc %+v", alloc)
					}
				}
			}
		}
	}
}

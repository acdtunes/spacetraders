package parkedsensing

import (
	"container/heap"
	"time"
)

// intervalCap bounds how long a slot may wait between scans. It is both a
// sanity ceiling on the arithmetic and the fallback for degenerate inputs: a
// fleet whose budget has collapsed degrades to hourly scans rather than
// computing multi-day intervals that would be indistinguishable from a hung
// scanner.
const intervalCap = time.Hour

// SlotSchedule is one waypoint's place in the scan rotation.
type SlotSchedule struct {
	// Waypoint is the symbol being sensed. It also breaks due-time ties, so
	// the rotation is reproducible.
	Waypoint string
	// Weight is this slot's share of the scan budget, from ScanWeight.
	Weight float64
	// LastScan is when this slot last had its TURN — not when it last produced
	// data. The next due time is anchored on it, so a slot that has been waiting
	// longer keeps its place in the queue rather than being reset by a
	// renormalisation.
	//
	// The distinction is load-bearing: the fleet's market-scan budget declines
	// most turns and writes nothing, so anchoring on "last produced data" would
	// leave every declined slot permanently due. The freshness claim
	// is a separate stamp this type never sees.
	LastScan time.Time
}

// Interval is how long a slot of the given weight waits between scans when
// the whole rotation is being served at ratePerSec.
//
// The fleet spends `rate` scans per second across `totalWeight` shares, so one
// share comes round every totalWeight/rate seconds and a slot holding `weight`
// shares comes round `weight` times as often. Degenerate inputs — a
// non-positive rate, weight, or total — fall back to the cap rather than
// dividing by zero, and any computed interval is capped too.
func Interval(totalWeight, weight, ratePerSec float64) time.Duration {
	if ratePerSec <= 0 || weight <= 0 || totalWeight <= 0 {
		return intervalCap
	}

	seconds := totalWeight / (ratePerSec * weight)
	interval := time.Duration(seconds * float64(time.Second))
	if interval > intervalCap || interval <= 0 {
		return intervalCap
	}
	return interval
}

// NextDue is when a slot next falls due for a scan: its last scan plus the
// interval its weight earns at the current rate.
func NextDue(s SlotSchedule, totalWeight, rate float64) time.Time {
	return s.LastScan.Add(Interval(totalWeight, s.Weight, rate))
}

// dueEntry pairs a slot with its due time so ordering never recomputes the
// interval. The due time is derived once at push and refreshed wholesale by
// Rebuild, which keeps Less cheap and, more importantly, total: a comparator
// that recomputed from mutable state could disagree with itself mid-sift and
// corrupt the heap invariant.
type dueEntry struct {
	slot SlotSchedule
	due  time.Time
}

// NextDueHeap is a min-heap of scan slots ordered by next-due time, with ties
// broken on waypoint symbol so the rotation is deterministic.
//
// It implements container/heap, so callers drive it with heap.Push and
// heap.Pop; heap.Pop yields a SlotSchedule. The heap holds the budget totals
// its due times were computed against, because those totals change whenever a
// slot is added or the API budget moves — call Rebuild to renormalise.
type NextDueHeap struct {
	entries     []dueEntry
	totalWeight float64
	rate        float64
}

// Compile-time proof the heap satisfies the interface its callers drive it
// through.
var _ heap.Interface = (*NextDueHeap)(nil)

// NewNextDueHeap returns an empty heap that will compute due times against the
// given budget totals.
func NewNextDueHeap(totalWeight, rate float64) *NextDueHeap {
	return &NextDueHeap{totalWeight: totalWeight, rate: rate}
}

func (h *NextDueHeap) Len() int { return len(h.entries) }

// Less orders by due time, breaking exact ties on waypoint symbol. Without the
// tiebreak, slots sharing a due time — the normal case on a cold start, where
// every slot carries the prior weight and no last scan — would pop in
// whatever order the heap happened to sift them into.
func (h *NextDueHeap) Less(i, j int) bool {
	a, b := h.entries[i], h.entries[j]
	if !a.due.Equal(b.due) {
		return a.due.Before(b.due)
	}
	return a.slot.Waypoint < b.slot.Waypoint
}

func (h *NextDueHeap) Swap(i, j int) { h.entries[i], h.entries[j] = h.entries[j], h.entries[i] }

// Push adds a slot, computing its due time against the heap's current totals.
// It takes a SlotSchedule; per container/heap it is called through heap.Push
// rather than directly.
func (h *NextDueHeap) Push(x any) {
	slot := x.(SlotSchedule)
	h.entries = append(h.entries, dueEntry{
		slot: slot,
		due:  NextDue(slot, h.totalWeight, h.rate),
	})
}

// Pop removes and returns the last entry as a SlotSchedule. Per container/heap
// it is called through heap.Pop, which has already swapped the minimum into
// that position.
func (h *NextDueHeap) Pop() any {
	last := len(h.entries) - 1
	entry := h.entries[last]
	h.entries[last] = dueEntry{} // release the reference before truncating
	h.entries = h.entries[:last]
	return entry.slot
}

// Rebuild renormalises every slot against new budget totals and re-heapifies.
//
// Due times are recomputed from each slot's own weight and last scan, so a
// slot that has been waiting keeps the credit for that wait — renormalisation
// changes the cadence, not each slot's place in the queue. Ordering is
// therefore only stable across a rate change when the slots were last scanned
// together; with staggered last scans the rotation can legitimately reorder,
// which is the point of recomputing rather than merely rescaling.
func (h *NextDueHeap) Rebuild(totalWeight, rate float64) {
	h.totalWeight = totalWeight
	h.rate = rate
	for i := range h.entries {
		h.entries[i].due = NextDue(h.entries[i].slot, totalWeight, rate)
	}
	heap.Init(h)
}

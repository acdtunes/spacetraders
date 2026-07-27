package parkedsensing

import (
	"container/heap"
	"testing"
	"time"
)

// scanEpoch is an arbitrary fixed instant. Nothing in this package reads a
// clock, so the tests only need a stable reference point.
var scanEpoch = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

func assertDurationNear(t *testing.T, label string, got, want, tolerance time.Duration) {
	t.Helper()
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	if diff > tolerance {
		t.Errorf("%s = %v, want %v (tolerance %v)", label, got, want, tolerance)
	}
}

func TestInterval(t *testing.T) {
	tests := []struct {
		name        string
		totalWeight float64
		weight      float64
		rate        float64
		want        time.Duration
		tolerance   time.Duration
	}{
		{
			// 3000 baseline-weight slots at 1.8 req/s: each slot comes round
			// again every 3000/1.8 seconds.
			name:        "baseline slot in a 3000-slot fleet",
			totalWeight: 3000,
			weight:      1,
			rate:        1.8,
			want:        1667 * time.Second,
			tolerance:   time.Second,
		},
		{
			// A slot carrying the maximum weight of 4 is revisited 4x as
			// often as the baseline.
			name:        "max-weight slot is revisited four times as often",
			totalWeight: 3000,
			weight:      4,
			rate:        1.8,
			want:        417 * time.Second,
			tolerance:   time.Second,
		},
		{
			name:        "halving the rate doubles the interval",
			totalWeight: 3000,
			weight:      1,
			rate:        0.9,
			want:        3333 * time.Second,
			tolerance:   time.Second,
		},
		{
			// The cap keeps a starved or misconfigured fleet from computing
			// multi-day intervals that would look like a hung scanner.
			name:        "very slow rate is capped at one hour",
			totalWeight: 3_000_000,
			weight:      1,
			rate:        0.1,
			want:        time.Hour,
			tolerance:   0,
		},
		{
			name:        "zero rate falls back to the cap",
			totalWeight: 3000,
			weight:      1,
			rate:        0,
			want:        time.Hour,
			tolerance:   0,
		},
		{
			name:        "negative rate falls back to the cap",
			totalWeight: 3000,
			weight:      1,
			rate:        -1.8,
			want:        time.Hour,
			tolerance:   0,
		},
		{
			name:        "zero weight falls back to the cap",
			totalWeight: 3000,
			weight:      0,
			rate:        1.8,
			want:        time.Hour,
			tolerance:   0,
		},
		{
			name:        "negative weight falls back to the cap",
			totalWeight: 3000,
			weight:      -1,
			rate:        1.8,
			want:        time.Hour,
			tolerance:   0,
		},
		{
			name:        "zero total weight falls back to the cap",
			totalWeight: 0,
			weight:      1,
			rate:        1.8,
			want:        time.Hour,
			tolerance:   0,
		},
		{
			name:        "negative total weight falls back to the cap",
			totalWeight: -3000,
			weight:      1,
			rate:        1.8,
			want:        time.Hour,
			tolerance:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Interval(tc.totalWeight, tc.weight, tc.rate)
			assertDurationNear(t, "Interval", got, tc.want, tc.tolerance)
		})
	}
}

func TestNextDue(t *testing.T) {
	slot := SlotSchedule{Waypoint: "X1-AU21-K82", Weight: 4, LastScan: scanEpoch}

	got := NextDue(slot, 3000, 1.8)
	want := scanEpoch.Add(Interval(3000, 4, 1.8))
	if !got.Equal(want) {
		t.Errorf("NextDue = %v, want %v", got, want)
	}

	// And it is genuinely anchored on the last scan, not on some implicit now.
	later := SlotSchedule{Waypoint: "X1-AU21-K82", Weight: 4, LastScan: scanEpoch.Add(time.Hour)}
	gotLater := NextDue(later, 3000, 1.8)
	assertDurationNear(t, "NextDue shift with LastScan", gotLater.Sub(got), time.Hour, 0)
}

// popAll drains the heap into the waypoint order it yields.
func popAll(t *testing.T, h *NextDueHeap) []string {
	t.Helper()
	order := make([]string, 0, h.Len())
	for h.Len() > 0 {
		slot, ok := heap.Pop(h).(SlotSchedule)
		if !ok {
			t.Fatalf("heap.Pop returned %T, want SlotSchedule", slot)
		}
		order = append(order, slot.Waypoint)
	}
	return order
}

func assertOrder(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s length = %d (%v), want %d (%v)", label, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

func TestNextDueHeap_PopsInNextDueOrder(t *testing.T) {
	h := NewNextDueHeap(3000, 1.8)

	// Every slot was scanned at the same instant, so due order is purely a
	// function of weight: heavier weight, shorter interval, due sooner.
	heap.Push(h, SlotSchedule{Waypoint: "X1-LIGHT", Weight: 1, LastScan: scanEpoch})
	heap.Push(h, SlotSchedule{Waypoint: "X1-HEAVY", Weight: 4, LastScan: scanEpoch})
	heap.Push(h, SlotSchedule{Waypoint: "X1-MEDIUM", Weight: 2, LastScan: scanEpoch})

	if h.Len() != 3 {
		t.Fatalf("Len after 3 pushes = %d, want 3", h.Len())
	}

	assertOrder(t, "pop order", popAll(t, h), []string{"X1-HEAVY", "X1-MEDIUM", "X1-LIGHT"})
}

func TestNextDueHeap_LastScanDrivesOrderAtEqualWeight(t *testing.T) {
	h := NewNextDueHeap(3000, 1.8)

	heap.Push(h, SlotSchedule{Waypoint: "X1-RECENT", Weight: 1, LastScan: scanEpoch.Add(10 * time.Minute)})
	heap.Push(h, SlotSchedule{Waypoint: "X1-STALE", Weight: 1, LastScan: scanEpoch})
	heap.Push(h, SlotSchedule{Waypoint: "X1-MIDDLE", Weight: 1, LastScan: scanEpoch.Add(5 * time.Minute)})

	assertOrder(t, "pop order", popAll(t, h), []string{"X1-STALE", "X1-MIDDLE", "X1-RECENT"})
}

func TestNextDueHeap_TiesBreakOnWaypoint(t *testing.T) {
	h := NewNextDueHeap(3000, 1.8)

	// Identical weight and last scan: only the waypoint can order these, and
	// it must do so deterministically so scan order is reproducible.
	heap.Push(h, SlotSchedule{Waypoint: "X1-CHARLIE", Weight: 2, LastScan: scanEpoch})
	heap.Push(h, SlotSchedule{Waypoint: "X1-ALPHA", Weight: 2, LastScan: scanEpoch})
	heap.Push(h, SlotSchedule{Waypoint: "X1-BRAVO", Weight: 2, LastScan: scanEpoch})

	assertOrder(t, "pop order", popAll(t, h), []string{"X1-ALPHA", "X1-BRAVO", "X1-CHARLIE"})
}

func TestNextDueHeap_PopPreservesSlotFields(t *testing.T) {
	h := NewNextDueHeap(3000, 1.8)
	want := SlotSchedule{Waypoint: "X1-AU21-K82", Weight: 3, LastScan: scanEpoch}
	heap.Push(h, want)

	got, ok := heap.Pop(h).(SlotSchedule)
	if !ok {
		t.Fatalf("heap.Pop returned %T, want SlotSchedule", got)
	}
	if got.Waypoint != want.Waypoint || got.Weight != want.Weight || !got.LastScan.Equal(want.LastScan) {
		t.Errorf("popped slot = %+v, want %+v", got, want)
	}
	if h.Len() != 0 {
		t.Errorf("Len after draining = %d, want 0", h.Len())
	}
}

// TestNextDueHeap_RebuildPreservesOrderWhenRateHalves covers the common
// renormalisation: when the budget changes but every slot was last scanned at
// the same time, all intervals scale by the same factor, so the rotation
// order is unchanged and only the cadence stretches.
func TestNextDueHeap_RebuildPreservesOrderWhenRateHalves(t *testing.T) {
	h := NewNextDueHeap(3000, 1.8)
	heap.Push(h, SlotSchedule{Waypoint: "X1-LIGHT", Weight: 1, LastScan: scanEpoch})
	heap.Push(h, SlotSchedule{Waypoint: "X1-HEAVY", Weight: 4, LastScan: scanEpoch})
	heap.Push(h, SlotSchedule{Waypoint: "X1-MEDIUM", Weight: 2, LastScan: scanEpoch})

	h.Rebuild(3000, 0.9)

	assertOrder(t, "pop order after rebuild", popAll(t, h), []string{"X1-HEAVY", "X1-MEDIUM", "X1-LIGHT"})
}

// TestNextDueHeap_RebuildRecomputesDueTimes is the mutation guard on Rebuild:
// with staggered last-scan times the rotation order genuinely flips when the
// rate halves, so a Rebuild that failed to recompute (or merely re-heapified
// stale due times) cannot pass.
//
// At Sigma-w 1200: the light slot's interval is 1200/rate and the heavy
// slot's is 1200/(4*rate), and the heavy slot was scanned 600s later. The
// light slot leads while 0.75*1200/rate < 600 (rate > 1.5) and trails once
// the rate drops below that.
func TestNextDueHeap_RebuildRecomputesDueTimes(t *testing.T) {
	const totalWeight = 1200.0

	light := SlotSchedule{Waypoint: "X1-LIGHT", Weight: 1, LastScan: scanEpoch}
	heavy := SlotSchedule{Waypoint: "X1-HEAVY", Weight: 4, LastScan: scanEpoch.Add(600 * time.Second)}

	// Sanity-check the premise directly against the pure functions, so a
	// mis-set fixture shows up here rather than as a confusing heap failure.
	if !NextDue(light, totalWeight, 1.8).Before(NextDue(heavy, totalWeight, 1.8)) {
		t.Fatalf("fixture broken: light slot should lead at rate 1.8")
	}
	if !NextDue(heavy, totalWeight, 0.9).Before(NextDue(light, totalWeight, 0.9)) {
		t.Fatalf("fixture broken: heavy slot should lead at rate 0.9")
	}

	h := NewNextDueHeap(totalWeight, 1.8)
	heap.Push(h, light)
	heap.Push(h, heavy)
	assertOrder(t, "pop order at rate 1.8", popAll(t, h), []string{"X1-LIGHT", "X1-HEAVY"})

	h = NewNextDueHeap(totalWeight, 1.8)
	heap.Push(h, light)
	heap.Push(h, heavy)
	h.Rebuild(totalWeight, 0.9)
	assertOrder(t, "pop order after rebuild at rate 0.9", popAll(t, h), []string{"X1-HEAVY", "X1-LIGHT"})
}

// TestNextDueHeap_RebuildOnEmptyHeap guards the renormalisation path before
// any slot has been declared.
func TestNextDueHeap_RebuildOnEmptyHeap(t *testing.T) {
	h := NewNextDueHeap(3000, 1.8)
	h.Rebuild(0, 0)
	if h.Len() != 0 {
		t.Errorf("Len = %d, want 0", h.Len())
	}

	// The rebuilt totals must be the ones subsequent pushes use.
	h.Rebuild(1200, 0.9)
	slot := SlotSchedule{Waypoint: "X1-LIGHT", Weight: 1, LastScan: scanEpoch}
	heap.Push(h, slot)
	assertOrder(t, "pop order", popAll(t, h), []string{"X1-LIGHT"})
}

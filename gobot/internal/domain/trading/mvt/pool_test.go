package mvt

import "testing"

func TestPoolSize_ScaleAxis(t *testing.T) {
	cases := []struct{ fat, hulls, pct, want int }{
		{5, 1, 10, 0}, // N=1 → floor(0.1) = 0
		{5, 2, 10, 0},
		{5, 10, 10, 1},
		{5, 40, 10, 4},
		{5, 400, 10, 5}, // fat lanes cap the pool
		{0, 400, 10, 0}, // no fat lanes → no specialists
		{3, 400, 0, 0},  // fraction 0 disables the pool
		{-1, 10, 10, 0}, // never negative
	}
	for _, c := range cases {
		if got := PoolSize(c.fat, c.hulls, c.pct); got != c.want {
			t.Fatalf("PoolSize(%d,%d,%d) = %d, want %d", c.fat, c.hulls, c.pct, got, c.want)
		}
	}
}

func TestIsFatLane(t *testing.T) {
	// intra baseline 1000/tranche, multiple 2.0 → net must exceed 2000; fee share 20%
	if !IsFatLane(5000, 600, 2, 500, 1000, 200, 20) { // 5000 - (1200 + 500) = 3300 > 2000
		t.Fatal("lane clearing 2× intra after jump cost is fat")
	}
	if IsFatLane(3600, 600, 2, 500, 1000, 200, 20) { // 3600 - 1700 = 1900 ≤ 2000
		t.Fatal("lane below 2× intra is not fat")
	}
	if IsFatLane(1e9, 0, 0, 0, 0, 200, 20) {
		t.Fatal("no intra baseline → never fat (fail toward no specialists)")
	}
}

// A gate fee eating more than the share of the lane's own margin per tranche disqualifies the
// lane however fat the arithmetic still looks: that is the specialist half of the jump-fee
// guard, and it is what a hot gate does to a lane that was fat this morning.
func TestIsFatLane_FeeShareOfMargin(t *testing.T) {
	// 5000/tranche against a 1000 intra baseline at 2.0: net of a 1500 fee it still clears
	// 2000, but 1500 is 30% of the margin the lane makes.
	if IsFatLane(5000, 0, 0, 1500, 1000, 200, 20) {
		t.Fatal("a fee above 20% of the lane's margin per tranche is not a fat lane")
	}
	if !IsFatLane(5000, 0, 0, 1000, 1000, 200, 20) { // exactly 20% — refuse only ABOVE the share
		t.Fatal("a fee at the share is still fat")
	}
	if !IsFatLane(5000, 0, 0, 1500, 1000, 200, 40) { // the knob is monotone
		t.Fatal("a wider share admits the same lane")
	}
}

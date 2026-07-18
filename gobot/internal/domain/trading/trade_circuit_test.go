package trading

import "testing"

// The bid-floor: keep trading only while the destination bid stays at least
// MinBidMargin (1000) above basis (the acquisition cost). The boundary is
// inclusive at basis+1000 and dead one credit below.
func TestMarginAlive_BidFloorDiscipline(t *testing.T) {
	const basis = 5000

	if !MarginAlive(basis+MinBidMargin, basis) {
		t.Fatalf("bid == basis+%d (%d) must clear the floor", MinBidMargin, basis+MinBidMargin)
	}
	if !MarginAlive(9000, basis) {
		t.Fatal("bid well above the floor must be alive")
	}
	if MarginAlive(basis+MinBidMargin-1, basis) {
		t.Fatalf("bid one credit below basis+%d must be dead", MinBidMargin)
	}
	if MarginAlive(basis, basis) {
		t.Fatal("bid == basis (zero margin) must be dead")
	}
}

// The stale-ask guard: a live source ask that has drifted more than
// StaleAskMovePercent from the ranked basis (in EITHER direction) trips the guard;
// a move at or within the tolerance does not. A degenerate basis never trips it.
func TestAskMovedBeyondTolerance_StaleBasisGuard(t *testing.T) {
	const basis = 1000 // 30% tolerance band = [700, 1300]

	cases := []struct {
		name    string
		liveAsk int
		want    bool
	}{
		{"unchanged ask is within tolerance", 1000, false},
		{"small upward drift within tolerance", 1200, false},
		{"small downward drift within tolerance", 800, false},
		{"exactly +30% is allowed (inclusive boundary)", 1300, false},
		{"exactly -30% is allowed (inclusive boundary)", 700, false},
		{"one credit past +30% trips the guard", 1301, true},
		{"one credit past -30% trips the guard", 699, true},
		{"a 3.6x ask jump trips the guard", 3600, true},
		{"ask collapsed well below basis trips the guard", 100, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AskMovedBeyondTolerance(tc.liveAsk, basis); got != tc.want {
				t.Fatalf("AskMovedBeyondTolerance(%d, %d) = %v, want %v", tc.liveAsk, basis, got, tc.want)
			}
		})
	}

	// A degenerate ranked basis has nothing to compare against and must never block.
	if AskMovedBeyondTolerance(5000, 0) {
		t.Fatal("a zero ranked basis must not trip the guard")
	}
}

// The per-visit tranche is the min of the 18-unit cap, the market's volume this
// tick, and the ship's remaining usable capacity — whichever binds first.
func TestVisitTranche_CapsAtEighteenVolumeAndCapacity(t *testing.T) {
	cases := []struct {
		name      string
		volume    int
		capacity  int
		wantUnits int
	}{
		{"tranche cap dominates deep volume and roomy hold", 100, 80, TrancheCap},
		{"thin market volume binds below the cap", 6, 80, 6},
		{"remaining capacity binds below the cap", 100, 4, 4},
		{"zero market volume yields no tranche", 0, 80, 0},
		{"no remaining capacity yields no tranche", 100, 0, 0},
		{"negative volume clamps to zero", -5, 80, 0},
		{"exactly at the cap", 18, 18, TrancheCap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VisitTranche(tc.volume, tc.capacity); got != tc.wantUnits {
				t.Fatalf("VisitTranche(%d, %d) = %d, want %d", tc.volume, tc.capacity, got, tc.wantUnits)
			}
		})
	}
}

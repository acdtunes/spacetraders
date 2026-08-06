package fleetgrowth

import "testing"

// The runway term dominates on a busy fleet whose hulls all have spend history.
func TestWorkingCapital_RunwayDominates(t *testing.T) {
	got := WorkingCapital(2000, 500_000, 5, 80_000)
	if got != 1_000_000 {
		t.Fatalf("expected the runway term to dominate at 1000000, got %d", got)
	}
}

// The hold-fill term dominates exactly when capacity was just added: the fresh hulls have no
// spend history, so the observed runway does not yet contain them.
func TestWorkingCapital_HoldFillDominatesOnFreshCapacity(t *testing.T) {
	got := WorkingCapital(2000, 100_000, 5, 80_000)
	if got != 400_000 {
		t.Fatalf("expected the hold-fill term to dominate at 400000, got %d", got)
	}
}

// A cold fleet has observed nothing, so it reserves nothing above the immutable floor. This is
// the case that proves the formula adds a term rather than replacing the floor.
func TestWorkingCapital_NothingObservedReservesNothing(t *testing.T) {
	if got := WorkingCapital(2000, 0, 0, 0); got != 0 {
		t.Fatalf("expected 0 working capital with nothing observed, got %d", got)
	}
}

// A malformed observation may only ever make the guard STRICTER, and every case pins the exact
// credit figure rather than a sign — a clamp that stops clamping must change an assertion, and a
// sign check cannot see it because the outer max() hides any single term that goes negative.
//
// Two directions are wrong and both are reachable through the products. A NEGATIVE result would
// lower the effective reserve BELOW the immutable floor, which is the weakening RULINGS #4
// forbids. Two negative factors multiplying into a phantom windfall is the mirror: money reserved
// against a commitment the fleet never made, which starves the buy path on a bad row.
func TestWorkingCapital_MalformedInputsCannotWeakenTheGuard(t *testing.T) {
	cases := []struct {
		name string
		got  int64
		want int64
	}{
		{"negative runway multiplier", WorkingCapital(-9000, 500_000, 5, 80_000), 400_000},
		{"negative outflow", WorkingCapital(2000, -500_000, 5, 80_000), 400_000},
		{"both runway factors negative", WorkingCapital(-9000, -500_000, 5, 80_000), 400_000},
		{"negative hull count", WorkingCapital(0, 0, -5, 80_000), 0},
		{"negative fill", WorkingCapital(0, 0, 5, -80_000), 0},
		{"both hold-fill factors negative", WorkingCapital(0, 0, -5, -80_000), 0},
		{"one factor of each product negative", WorkingCapital(-9000, 500_000, -5, 80_000), 0},
		{"every factor negative", WorkingCapital(-9000, -500_000, -5, -80_000), 0},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s: expected %d credits reserved, got %d", tc.name, tc.want, tc.got)
		}
	}
}

// The product is formed BEFORE the divide, so a sub-hour runway cannot round to zero.
func TestWorkingCapital_SubHourRunwayDoesNotRoundAway(t *testing.T) {
	if got := WorkingCapital(400, 500_000, 0, 0); got != 200_000 {
		t.Fatalf("expected 0.4h of a 500000 outflow = 200000, got %d", got)
	}
}

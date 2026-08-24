package parkedsensing

import (
	"math"
	"testing"
)

func TestLandedYardCost_ChargesBothDeliveryTermsPerHop(t *testing.T) {
	if got := LandedYardCost(25_000, 0, 5_900, 2_000); got != 25_000 {
		t.Fatalf("LandedYardCost(25000, 0 hops) = %d, want 25000 — a counter in the placement's own "+
			"system crosses nothing and must rank at its sticker price", got)
	}
	if got := LandedYardCost(25_000, 1, 5_900, 2_000); got != 32_900 {
		t.Fatalf("LandedYardCost(25000, 1 hop) = %d, want 32900 (25000 + 5900 + 2000)", got)
	}
	if got := LandedYardCost(25_000, 3, 5_900, 2_000); got != 48_700 {
		t.Fatalf("LandedYardCost(25000, 3 hops) = %d, want 48700 — both terms are charged PER "+
			"CROSSING, not once for leaving the system", got)
	}
}

func TestLandedYardCost_CanNeverRankBelowTheAsk(t *testing.T) {
	for _, c := range []struct {
		name                 string
		hops                 int
		gateFee, jumpPenalty int64
	}{
		{"negative hops", -4, 5_900, 2_000},
		{"negative fee", 3, -5_900, 2_000},
		{"negative penalty", 3, 5_900, -2_000},
		{"a negative hop count times two negative charges", -3, -5_900, -2_000},
	} {
		if got := LandedYardCost(25_000, c.hops, c.gateFee, c.jumpPenalty); got < 25_000 {
			t.Fatalf("%s: LandedYardCost = %d, below the 25000 ask — a counter that ranks cheaper "+
				"than it charges is how a malformed delivery term picks the expensive yard", c.name, got)
		}
	}
}

func TestWalkAwayCeiling_IsTheMultipleOfTheCheapestFreshAsk(t *testing.T) {
	if got := WalkAwayCeiling(23_000, DefaultWalkAwayMult); got != 69_000 {
		t.Fatalf("WalkAwayCeiling(23000, %d) = %d, want 69000", DefaultWalkAwayMult, got)
	}
	if got := WalkAwayCeiling(23_000, 1); got != 23_000 {
		t.Fatalf("WalkAwayCeiling(23000, 1) = %d, want 23000 — a multiple of one accepts only the "+
			"cheapest band, which is aggressive but legitimate", got)
	}
}

func TestWalkAwayCeiling_HasNothingToSayWithoutAReference(t *testing.T) {
	for _, c := range []struct {
		name     string
		cheapest int64
		mult     int
	}{
		{"no fresh ask anywhere", 0, DefaultWalkAwayMult},
		{"a negative reference", -23_000, DefaultWalkAwayMult},
		{"a zero multiple", 23_000, 0},
		{"a negative multiple", 23_000, -3},
	} {
		if got := WalkAwayCeiling(c.cheapest, c.mult); got != 0 {
			t.Fatalf("%s: WalkAwayCeiling = %d, want 0 (no ceiling)", c.name, got)
		}
	}
}

func TestWalkAwayCeiling_RefusesToOverflowIntoARefusal(t *testing.T) {
	if got := WalkAwayCeiling(math.MaxInt64/2, 3); got != 0 {
		t.Fatalf("WalkAwayCeiling(MaxInt64/2, 3) = %d, want 0 — a ceiling that cannot be represented "+
			"must disable the guard, never invert it", got)
	}
}

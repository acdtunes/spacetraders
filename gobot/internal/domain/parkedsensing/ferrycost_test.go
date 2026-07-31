package parkedsensing

import "testing"

func TestFerryHops_LocalCounterCostsNothingToReach(t *testing.T) {
	if got := FerryHops(false, 0); got != 0 {
		t.Fatalf("FerryHops(local) = %d, want 0 — a counter in the placement's own system crosses no gate", got)
	}
	// A stale hop count on a local candidate is still local: the flag decides.
	if got := FerryHops(false, 7); got != 0 {
		t.Fatalf("FerryHops(local, 7 hops) = %d, want 0 — the ferried flag is the authority", got)
	}
}

// TestFerryHops_UnknownCrossSystemDistanceChargesTheOneGateMinimum guards the
// exact hole sp-e46yc fell through: a remote counter whose distance was never
// walked must not price as free.
func TestFerryHops_UnknownCrossSystemDistanceChargesTheOneGateMinimum(t *testing.T) {
	for _, hops := range []int{0, -1, -9999} {
		if got := FerryHops(true, hops); got != 1 {
			t.Fatalf("FerryHops(ferried, %d) = %d, want 1 — a cross-system counter is at least one "+
				"gate away by definition, so an unknown distance must never price as free", hops, got)
		}
	}
}

func TestFerryHops_KnownDistanceIsChargedInFull(t *testing.T) {
	if got := FerryHops(true, 3); got != 3 {
		t.Fatalf("FerryHops(ferried, 3) = %d, want 3", got)
	}
}

func TestFerryCost_PricesEachCrossing(t *testing.T) {
	if got := FerryCost(3, 5_900); got != 17_700 {
		t.Fatalf("FerryCost(3, 5900) = %d, want 17700", got)
	}
	if got := FerryCost(0, 5_900); got != 0 {
		t.Fatalf("FerryCost(0, 5900) = %d, want 0", got)
	}
}

// TestFerryCost_ClampsEachFactorIndependently is the money-guard property: two
// negatives must not multiply into a phantom credit. Clamping only the product
// would let this through.
func TestFerryCost_ClampsEachFactorIndependently(t *testing.T) {
	if got := FerryCost(-3, -5_900); got != 0 {
		t.Fatalf("FerryCost(-3, -5900) = %d, want 0 — a negative hop count times a negative fee "+
			"must not become a positive ferry cost", got)
	}
	if got := FerryCost(-3, 5_900); got != 0 {
		t.Fatalf("FerryCost(-3, 5900) = %d, want 0", got)
	}
	if got := FerryCost(3, -5_900); got != 0 {
		t.Fatalf("FerryCost(3, -5900) = %d, want 0", got)
	}
}

func TestLandedProbeCost_AddsDeliveryToTheQuote(t *testing.T) {
	if got := LandedProbeCost(24_356, 17_700); got != 42_056 {
		t.Fatalf("LandedProbeCost(24356, 17700) = %d, want 42056", got)
	}
}

// TestLandedProbeCost_IsNeverWeakerThanTheQuote is what makes this change unable
// to loosen the guard it joins: whatever the ferry term does — including an
// overflow that wraps the sum negative — the floor still demands at least the
// sticker price it demanded before sp-e46yc.
func TestLandedProbeCost_IsNeverWeakerThanTheQuote(t *testing.T) {
	const quote int64 = 24_356
	for _, ferry := range []int64{0, -1, -1_000_000, 1<<62 - 1} {
		if got := LandedProbeCost(quote, ferry); got < quote {
			t.Fatalf("LandedProbeCost(%d, %d) = %d, which is LESS than the quote — this change "+
				"made a money guard more permissive", quote, ferry, got)
		}
	}
	if got := LandedProbeCost(-5_000, 17_700); got != 17_700 {
		t.Fatalf("LandedProbeCost(-5000, 17700) = %d, want 17700 — a malformed quote must not "+
			"discount the ferry", got)
	}
}

// TestDefaultGateFeeCredits_IsTheMeasuredMean pins the constant against the
// measurement it was derived from, so a silent edit has to argue with the data.
// 4,235 recorded jumps, mean 5,930, median 5,504 — the MEAN is correct because
// this scalar is multiplied by a hop count to price a SUM of crossings.
func TestDefaultGateFeeCredits_IsTheMeasuredMean(t *testing.T) {
	if DefaultGateFeeCredits != 5_900 {
		t.Fatalf("DefaultGateFeeCredits = %d, want 5900 (measured mean 5,930 over 4,235 jumps)",
			DefaultGateFeeCredits)
	}
	// Below the median would understate every ferry; the mean sits above it
	// because the fee distribution is right-skewed.
	if DefaultGateFeeCredits < 5_504 {
		t.Fatalf("DefaultGateFeeCredits = %d is below the measured MEDIAN 5,504 — pricing a sum of "+
			"crossings off anything below the mean understates the ferry", DefaultGateFeeCredits)
	}
}

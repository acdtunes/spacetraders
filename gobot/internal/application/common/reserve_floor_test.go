package common

import "testing"

// TestImmutableReserveFloorIsFlat pins the sp-05glh outcome: the working-capital floor is a
// flat, non-tunable 50k with NO proportional-of-treasury computation left to vary it. The
// deleted EffectiveReserveFloor used to shrink an absolute reserve toward this floor as
// treasury fell (max(50k, min(absolute, pct% × liveTreasury))); every caller now uses this
// constant directly, so the "floor" a caller enforces is this value regardless of live
// treasury (RULINGS #5: immutable, const-only, no config/tune/launch seam).
func TestImmutableReserveFloorIsFlat(t *testing.T) {
	const want = 50000
	if ImmutableReserveFloor != want {
		t.Fatalf("ImmutableReserveFloor = %d, want %d (flat everywhere, sp-05glh)", ImmutableReserveFloor, want)
	}
	// The floor is a constant, not a function of treasury — pin it at low/mid/high treasury
	// levels to document that "regardless of treasury" is now trivially true (no computation
	// left to depend on liveTreasury at all, unlike the deleted proportional resolver).
	for _, treasury := range []int64{50_000, 500_000, 5_000_000} {
		if got := int64(ImmutableReserveFloor); got != want {
			t.Fatalf("at treasury %d: floor = %d, want flat %d", treasury, got, want)
		}
	}
}

// TestContractCushionsUnaffected pins that the RULINGS #6 contract cushions — a SEPARATE,
// higher, principled money guard, not part of the deleted 40% rule — are untouched: both
// derive from the same immutable base, so they can never drift from it.
func TestContractCushionsUnaffected(t *testing.T) {
	if ContractReserveCushion != ImmutableReserveFloor+100_000 {
		t.Fatalf("ContractReserveCushion = %d, want base+100_000", ContractReserveCushion)
	}
	if ContractScalerCushion != ImmutableReserveFloor+150_000 {
		t.Fatalf("ContractScalerCushion = %d, want base+150_000", ContractScalerCushion)
	}
}

// TestContractExemptionLeavesTheNonContractFloorsIntact pins the SHAPE of the RULINGS #5
// contract exemption: it added a separate, lower reserve for contract source-buys and moved
// nothing else. The base and every tier derived from it keep their exact values, so every
// non-contract spender that gates on them — trade, factory, construction, arb, stocker,
// outfitting, scaler capex, bootstrap capex — is unchanged. The exemption is a NEW constant
// with two call sites, never an edit to the base, which would have silently dragged all three
// derived tiers with it.
func TestContractExemptionLeavesTheNonContractFloorsIntact(t *testing.T) {
	if ImmutableReserveFloor != 50_000 {
		t.Fatalf("ImmutableReserveFloor = %d, want 50000 — the exemption must not move the base", ImmutableReserveFloor)
	}
	if NonContractWorkingCapitalFloor != 150_000 || ContractReserveCushion != 150_000 || ContractScalerCushion != 200_000 {
		t.Fatalf("derived tiers moved: non-contract %d, cushion %d, scaler %d — want 150000/150000/200000",
			NonContractWorkingCapitalFloor, ContractReserveCushion, ContractScalerCushion)
	}
	if ContractSolvencyReserve >= ImmutableReserveFloor {
		t.Fatalf("ContractSolvencyReserve = %d, must sit strictly BELOW the %d working-capital floor: it exists because contract work is exempt from that floor, so at or above it the exemption is a no-op",
			ContractSolvencyReserve, ImmutableReserveFloor)
	}
	if ContractSolvencyReserve <= 0 {
		t.Fatalf("ContractSolvencyReserve = %d, must be a real positive reserve: a zero floor lets a contract buy drain the treasury to nothing, and a fleet that cannot refuel has no recovery path at all", ContractSolvencyReserve)
	}
}

// TestEffectiveReserveFloorShimIsFlat pins the kept-for-compatibility EffectiveReserveFloor
// shim (sp-05glh): every argument is ignored, the result is always ImmutableReserveFloor —
// no proportional-of-treasury computation survives, regardless of what absolute/pct/treasury
// combination a not-yet-migrated caller passes in.
func TestEffectiveReserveFloorShimIsFlat(t *testing.T) {
	cases := []struct {
		absolute int64
		pct      int
		treasury int64
	}{
		{absolute: 0, pct: 0, treasury: 0},
		{absolute: 200_000, pct: 40, treasury: 500_000},
		{absolute: 1_000_000, pct: 100, treasury: 5_000_000},
	}
	for _, c := range cases {
		if got := EffectiveReserveFloor(c.absolute, c.pct, c.treasury); got != ImmutableReserveFloor {
			t.Fatalf("EffectiveReserveFloor(%d, %d, %d) = %d, want flat %d", c.absolute, c.pct, c.treasury, got, ImmutableReserveFloor)
		}
	}
}

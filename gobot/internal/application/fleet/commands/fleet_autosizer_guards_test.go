package commands

import (
	"strings"
	"testing"
)

// passingRequest is a candidate purchase where EVERY guard passes. Each test flips ONE field to
// pin exactly one guard's refusal (or one fail-closed path), so a regression names the guard it
// broke.
func passingRequest() PurchaseRequest {
	return PurchaseRequest{
		Class:    HullClassLight,
		ShipType: "SHIP_LIGHT_HAULER",

		Shortfall: 3,

		CurrentClassCount: 10,
		ClassCeiling:      35,
		CurrentTotalCount: 20,
		TotalCeiling:      50,

		PurchasesThisTick: 0,
		PerTickCap:        1,

		Price:              437000,
		PriceReadable:      true,
		CheapestKnownPrice: 400000,
		MaxPriceClass:      0, // no absolute cap
		MaxPremiumPct:      50,

		HoursToEraEnd:  20,
		EraReadable:    true,
		EraCutoffHours: 3,
		PaybackSafety:  0.5,

		MarginalRate:  80000,
		RateFloor:     56000,
		RateReadable:  true,
		RateDeclining: false,

		LiveTreasury:      5000000,
		TreasuryReadable:  true,
		ReserveAbsolute:   200000,
		ReservePct:        40,
		MarginOverFloor:   200000,
		TreasuryPctPerBuy: 25,

		APIUtilPct:      40,
		APIUtilReadable: true,
		APIUtilCeiling:  85,
	}
}

func TestGuards_AllPass_Approved(t *testing.T) {
	d := EvaluateGuards(passingRequest())
	if !d.Approved {
		t.Fatalf("expected APPROVED, got blocked by %q; arithmetic: %s", d.BlockedBy, d.Arithmetic())
	}
	if d.BlockedBy != "" {
		t.Fatalf("approved decision must have empty BlockedBy, got %q", d.BlockedBy)
	}
}

func TestGuard_Demand_ZeroShortfallBlocks(t *testing.T) {
	r := passingRequest()
	r.Shortfall = 0
	assertBlockedBy(t, r, GuardDemand)
}

func TestGuard_FleetCeiling_ClassFull(t *testing.T) {
	r := passingRequest()
	r.CurrentClassCount = 35 // == ceiling
	assertBlockedBy(t, r, GuardFleetCeiling)
}

func TestGuard_FleetCeiling_TotalFull(t *testing.T) {
	r := passingRequest()
	r.CurrentTotalCount = 50 // == total ceiling
	assertBlockedBy(t, r, GuardFleetCeiling)
}

func TestGuard_PerTickCap_Exhausted(t *testing.T) {
	r := passingRequest()
	r.PurchasesThisTick = 1 // == cap
	assertBlockedBy(t, r, GuardPerTickCap)
}

func TestGuard_PriceRead_UnreadableFailsClosed(t *testing.T) {
	r := passingRequest()
	r.PriceReadable = false
	// price_read is evaluated before price_ceiling, so it is the first (and named) blocker.
	assertBlockedBy(t, r, GuardPriceRead)
}

func TestGuard_PriceCeiling_AbsoluteCap(t *testing.T) {
	r := passingRequest()
	r.MaxPriceClass = 400000 // price 437000 exceeds the absolute cap
	assertBlockedBy(t, r, GuardPriceCeiling)
}

func TestGuard_PriceCeiling_PremiumOverCheapest(t *testing.T) {
	r := passingRequest()
	r.CheapestKnownPrice = 200000 // cap = 200000 * 1.5 = 300000 < price 437000
	assertBlockedBy(t, r, GuardPriceCeiling)
}

func TestGuard_EraPayback_PastHardCutoff(t *testing.T) {
	r := passingRequest()
	r.HoursToEraEnd = 2 // inside the T-3h last-buy window
	assertBlockedBy(t, r, GuardEraPayback)
}

func TestGuard_EraPayback_TooExpensiveToPayBack(t *testing.T) {
	r := passingRequest()
	// rate 80000 × 4h × 0.5 = 160000 < price 437000 → cannot pay back in the remaining era.
	r.HoursToEraEnd = 4
	assertBlockedBy(t, r, GuardEraPayback)
}

func TestGuard_EraPayback_EraUnreadableFailsClosed(t *testing.T) {
	r := passingRequest()
	r.EraReadable = false
	assertBlockedBy(t, r, GuardEraPayback)
}

func TestGuard_EraPayback_RateUnreadableFailsClosed(t *testing.T) {
	r := passingRequest()
	// The era clock reads fine, but without a marginal rate we cannot prove payback → fail-closed.
	// (guardRealizedRate would also block, but era_payback is evaluated first.)
	r.RateReadable = false
	assertBlockedBy(t, r, GuardEraPayback)
}

func TestGuard_RealizedRate_BelowFloor(t *testing.T) {
	r := passingRequest()
	r.MarginalRate = 50000 // below floor 56000 (era payback still passes: 50000×20×0.5=500000 ≥ 437000)
	assertBlockedBy(t, r, GuardRealizedRate)
}

func TestGuard_RealizedRate_DecliningStopsBuy(t *testing.T) {
	r := passingRequest()
	r.RateDeclining = true // absorption saturating — stop buying even above the floor
	assertBlockedBy(t, r, GuardRealizedRate)
}

func TestGuard_TreasuryPct_TooExpensive(t *testing.T) {
	r := passingRequest()
	// 25% of a 1M treasury = 250000 < price 437000. Keep the floor guard satisfied by a large
	// reserve headroom: treasury 1M − floor(≤250000 at 25%… actually 40%×1M=400000) leaves 600000
	// ≥ 637000? No — pick numbers so ONLY treasury_pct blocks.
	r.LiveTreasury = 1000000
	r.ReservePct = 5           // floor = max(50000, min(200000, 5%×1M=50000)) = 50000
	r.MarginOverFloor = 100000 // spendable 950000 ≥ 437000+100000 = 537000 (treasury_floor passes)
	assertBlockedBy(t, r, GuardTreasuryPct)
}

func TestGuard_TreasuryPct_NotAppliedWhenZero(t *testing.T) {
	r := passingRequest()
	r.TreasuryPctPerBuy = 0 // lights: affordability-% rule off
	r.LiveTreasury = 600000 // would fail a 25% rule, but the rule is off
	r.ReservePct = 5
	r.MarginOverFloor = 50000 // floor 50000, spendable 550000 ≥ 437000+50000 = 487000
	d := EvaluateGuards(r)
	for _, v := range d.Verdicts {
		if v.Guard == GuardTreasuryPct && !v.Passed {
			t.Fatalf("treasury_pct must PASS when the rule is off (pct=0), got block: %s", v.Detail)
		}
	}
}

func TestGuard_APIUtil_AboveCeilingBlocks(t *testing.T) {
	r := passingRequest()
	r.APIUtilPct = 90 // above the 85 ceiling
	assertBlockedBy(t, r, GuardAPIUtil)
}

func TestGuard_APIUtil_UnreadableFailsOpen(t *testing.T) {
	r := passingRequest()
	r.APIUtilReadable = false // dynamic protection unreadable → fail-OPEN (ceilings are the hard bound)
	d := EvaluateGuards(r)
	if !d.Approved {
		t.Fatalf("api_util must fail OPEN when unreadable; got blocked by %q", d.BlockedBy)
	}
}

func TestGuard_TreasuryFloor_InsufficientAfterFloor(t *testing.T) {
	r := passingRequest()
	// Low treasury: even the immutable 50k floor leaves too little for price+margin.
	r.LiveTreasury = 300000 // spendable ≈ 300000 − 120000(40%) = 180000 < 437000+200000
	r.TreasuryPctPerBuy = 0 // isolate the floor guard (25% rule off so it isn't the first blocker)
	assertBlockedBy(t, r, GuardTreasuryFloor)
}

func TestGuard_TreasuryFloor_UnreadableFailsClosed(t *testing.T) {
	r := passingRequest()
	r.TreasuryReadable = false
	// treasury_pct is applied (pct=25) and also fail-closes on unreadable treasury, and is
	// evaluated first — so it is the named blocker. Turn it off to isolate the floor guard.
	r.TreasuryPctPerBuy = 0
	assertBlockedBy(t, r, GuardTreasuryFloor)
}

// The counter-cyclical proportional floor (sp-yqx4) binds below ≈ absolute ÷ (pct/100) of
// treasury, keeping a (1−pct%) slice spendable — so a mid treasury with a high absolute reserve
// still permits an affordable buy rather than dead-locking.
func TestGuard_TreasuryFloor_ProportionalFloorPermitsBuy(t *testing.T) {
	r := passingRequest()
	r.LiveTreasury = 2000000
	r.ReserveAbsolute = 5000000 // a naive absolute floor above treasury would dead-lock every buy
	r.ReservePct = 40           // proportional floor = 40% × 2M = 800000; spendable = 1,200,000
	r.Price = 437000
	r.MarginOverFloor = 200000 // need 637000 ≤ 1,200,000 → permitted
	r.TreasuryPctPerBuy = 0
	d := EvaluateGuards(r)
	if !d.Approved {
		t.Fatalf("proportional floor must permit an affordable buy at mid treasury; blocked by %q: %s", d.BlockedBy, d.Arithmetic())
	}
}

// The decision log carries the full arithmetic for every guard (the iv65 idiom).
func TestDecision_ArithmeticLogsEveryGuard(t *testing.T) {
	d := EvaluateGuards(passingRequest())
	arith := d.Arithmetic()
	for _, name := range []GuardName{GuardDemand, GuardFleetCeiling, GuardPerTickCap, GuardPriceRead, GuardPriceCeiling, GuardEraPayback, GuardRealizedRate, GuardTreasuryPct, GuardAPIUtil, GuardTreasuryFloor} {
		if !strings.Contains(arith, string(name)) {
			t.Errorf("arithmetic log missing guard %q: %s", name, arith)
		}
	}
	// A specific number the captain would retune from must be present.
	if !strings.Contains(arith, "437000") {
		t.Errorf("arithmetic must include the concrete price: %s", arith)
	}
}

// BlockedBy names the FIRST failing guard in evaluation order even when several would block.
func TestDecision_BlockedByFirstFailure(t *testing.T) {
	r := passingRequest()
	r.Shortfall = 0          // demand (first) blocks
	r.CurrentClassCount = 99 // fleet ceiling (later) would also block
	d := EvaluateGuards(r)
	if d.BlockedBy != GuardDemand {
		t.Fatalf("expected first blocker = demand, got %q", d.BlockedBy)
	}
}

func assertBlockedBy(t *testing.T, r PurchaseRequest, want GuardName) {
	t.Helper()
	d := EvaluateGuards(r)
	if d.Approved {
		t.Fatalf("expected BLOCK by %q, got APPROVED; arithmetic: %s", want, d.Arithmetic())
	}
	if d.BlockedBy != want {
		t.Fatalf("expected block by %q, got %q; arithmetic: %s", want, d.BlockedBy, d.Arithmetic())
	}
}

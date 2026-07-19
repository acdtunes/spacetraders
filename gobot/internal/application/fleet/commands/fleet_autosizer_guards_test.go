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

		MarginalRate:        80000,
		RateFloor:           56000,
		RateReadable:        true,
		RateDeclining:       false,
		UnservedDemandFloor: 2,

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

// sp-461l (epic sp-g9td) — the era-payback MONEY GUARD fires on the CASH-TRUE marginal rate, not the
// ~2x-inflated telemetry-netting rate sp-rd21 diagnosed. The autosizer feeds this guard a MarginalRate
// from fleet_autosizer_ports.FleetTourRate → trading.ComputeFleetTourRate, which reads PER-HULL
// telemetry (the min per-ship realized $/hr). That per-hull attribution is why this consumer stays on
// telemetry rather than the transactions-cash rate: the transactions ledger has NO ship column, so it
// cannot yield a per-hull marginal, AND dividing an aggregate cash rate by hull count would raise the
// min-based marginal and WEAKEN this guard (RULINGS #4 forbids). sp-rd21's write-path fix (dropped buy
// legs now recorded) makes ComputeFleetTourRate reconcile 1.00x, so the marginal the guard reads is now
// the TRUE rate. This test pins the consequence at the guard boundary: only the marginal source differs
// between the two requests, and the money guard now REFUSES the overpriced hull the inflated speedo
// would have APPROVED. The threshold arithmetic is unchanged — only the rate it reads is now honest.
func TestGuard_EraPayback_FiresOnCashTrueRateNotInflatedTelemetry(t *testing.T) {
	base := heavyRequest()
	base.HoursToEraEnd = 10
	base.PaybackSafety = 0.5
	base.Price = 300000
	base.MaxPriceClass = 0           // no absolute cap → isolate era_payback as the flip
	base.CheapestKnownPrice = 300000 // premium cap 450k ≥ price → price_ceiling passes both cases
	base.RateFloor = 30000           // both marginals clear the floor → realized_rate is not the flip

	// TRUE (rd21-netted) marginal 40k/hr: maxAffordable = 40k × 10h × 0.5 = 200k < price 300k → REFUSE.
	trueRate := base
	trueRate.MarginalRate = 40000
	assertBlockedBy(t, trueRate, GuardEraPayback)

	// INFLATED (dropped-buy) marginal 80k/hr: maxAffordable = 80k × 10h × 0.5 = 400k ≥ 300k → the
	// overpriced hull would have been APPROVED. This is the exact over-buy the cash-true fix prevents.
	inflated := base
	inflated.MarginalRate = 80000
	if d := EvaluateGuards(inflated); !d.Approved {
		t.Fatalf("the inflated 80k/hr marginal should have (wrongly) APPROVED the 300k hull — proving the guard's sensitivity to the rate — but blocked by %q: %s", d.BlockedBy, d.Arithmetic())
	}
}

// heavyRequest is a HEAVY (trade) candidate where every guard passes — the class the sp-zbe6
// concentration carve-out applies to (its Shortfall is the unserved profitable-lane count). Based on
// the all-pass light request with the class flipped and the rate headroom kept (marginal 80000 ≥
// floor 56000), so flipping ONE realized-rate field pins exactly the declining-stop-buy behaviour.
func heavyRequest() PurchaseRequest {
	r := passingRequest()
	r.Class = HullClassHeavy
	r.ShipType = "SHIP_HEAVY_FREIGHTER"
	return r
}

// sp-zbe6 REGRESSION (the guard that prevents over-buying into a saturated market): a genuinely
// saturated TRADE market — realized rate DECLINING with unserved lanes AT or BELOW the floor (the
// fleet has already spread to nearly every profitable lane) — STILL stops buying, even though the
// marginal clears the rate floor. The concentration fix must not loosen this away.
func TestGuard_RealizedRate_DecliningStopsBuy(t *testing.T) {
	r := heavyRequest()
	r.RateDeclining = true
	r.Shortfall = 2 // == the near-zero floor: genuine saturation, no fresh lane for the next heavy
	assertBlockedBy(t, r, GuardRealizedRate)
}

// sp-zbe6: a DECLINING aggregate tour-rate does NOT stop a HEAVY buy when unserved lanes sit ABOVE
// the floor — that decline is hull CONCENTRATION (the fleet compressed a few fat lanes), not
// absorption saturation; the next heavy flies a FRESH unserved lane. The buy proceeds (the marginal
// still clears the floor). The decision-log detail names the unserved-lane count so it is auditable.
func TestGuard_RealizedRate_DecliningWithUnservedInventory_Proceeds(t *testing.T) {
	r := heavyRequest()
	r.RateDeclining = true
	r.Shortfall = 28 // the live incident: 28 profitable lanes unflown, floor 2
	d := EvaluateGuards(r)
	if !d.Approved {
		t.Fatalf("a declining rate with 28 unserved lanes (> floor 2) must NOT block — concentration, not saturation; blocked by %q: %s", d.BlockedBy, d.Arithmetic())
	}
	arith := d.Arithmetic()
	if !strings.Contains(arith, "28 unserved") {
		t.Errorf("realized_rate detail must name the unserved-lane count for audit, got: %s", arith)
	}
	if !strings.Contains(arith, "concentration") {
		t.Errorf("realized_rate detail must explain the decline is concentration not saturation, got: %s", arith)
	}
}

// sp-zbe6 off-by-one boundary + mutation anchor (heavy): with the floor at 2, the declining stop-buy
// fires for unserved lanes AT or BELOW 2 (genuine near-zero saturation) and is bypassed ABOVE 2
// (unserved inventory present). Input variations of one behavior → one parametrized test (Mandate 5).
func TestGuard_RealizedRate_DecliningStopBuyFloorBoundary(t *testing.T) {
	cases := []struct {
		shortfall  int
		wantBlock  bool
		wantDetail string
	}{
		{shortfall: 1, wantBlock: true, wantDetail: "stop-buy"},       // below floor → saturated
		{shortfall: 2, wantBlock: true, wantDetail: "stop-buy"},       // == floor → still saturated
		{shortfall: 3, wantBlock: false, wantDetail: "concentration"}, // floor+1 → inventory present
		{shortfall: 28, wantBlock: false, wantDetail: "concentration"},
	}
	for _, tc := range cases {
		r := heavyRequest()
		r.RateDeclining = true
		r.UnservedDemandFloor = 2
		r.Shortfall = tc.shortfall
		d := EvaluateGuards(r)
		blocked := d.BlockedBy == GuardRealizedRate
		if blocked != tc.wantBlock {
			t.Errorf("shortfall %d (floor 2): realized_rate blocked=%v, want %v — arithmetic: %s", tc.shortfall, blocked, tc.wantBlock, d.Arithmetic())
		}
		if !strings.Contains(d.Arithmetic(), tc.wantDetail) {
			t.Errorf("shortfall %d: detail must contain %q, got: %s", tc.shortfall, tc.wantDetail, d.Arithmetic())
		}
	}
}

// sp-zbe6 class-scope guard (lens 3 "no behavior change to non-trade classes" + class-gate mutation
// anchor): the concentration carve-out is TRADE-ONLY. A NON-heavy class (light) with a declining
// realized rate STILL stops buying even with a large shortfall — a light's Shortfall is worker slots,
// not unserved lanes, so it carries no concentration story and keeps the unconditional stop-buy.
// Dropping the class gate (making the carve-out generic) makes this fail — proving it trade-scoped.
func TestGuard_RealizedRate_NonHeavyDecliningAlwaysStops(t *testing.T) {
	r := passingRequest() // HullClassLight
	r.RateDeclining = true
	r.Shortfall = 28          // a large shortfall must NOT buy the light out of a declining rate
	r.UnservedDemandFloor = 2 // the heavy floor is irrelevant to a non-heavy class
	assertBlockedBy(t, r, GuardRealizedRate)
	if strings.Contains(EvaluateGuards(r).Arithmetic(), "concentration") {
		t.Errorf("a non-heavy declining rate must NOT get the concentration carve-out: %s", EvaluateGuards(r).Arithmetic())
	}
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

// sp-a5dq: the guard blocks concurrency GROWTH the moment utilization reaches the ceiling — the
// bead's "at/over the ceiling" boundary (a pass requires strictly-below).
func TestGuard_APIUtil_AtCeilingBlocks(t *testing.T) {
	r := passingRequest()
	r.APIUtilPct = 85 // == the 85 ceiling
	assertBlockedBy(t, r, GuardAPIUtil)
}

// sp-a5dq: an unreadable utilization signal fails CLOSED (holds growth) — the fail-OPEN inversion
// let the autosizer grow concurrency into a saturated API that was ALERTED but never PREVENTED.
// RULINGS #4: a guard that cannot read its bound never permits the spend.
func TestGuard_APIUtil_UnreadableFailsClosed(t *testing.T) {
	r := passingRequest()
	r.APIUtilReadable = false // utilization surface unreadable → fail-CLOSED (hold, do not grow)
	assertBlockedBy(t, r, GuardAPIUtil)
}

// Non-regression: a readable, under-ceiling utilization does NOT block — a healthy fleet still
// autosizes normally (only saturation or an unreadable signal holds growth).
func TestGuard_APIUtil_UnderCeilingPasses(t *testing.T) {
	r := passingRequest()
	r.APIUtilPct = 84 // just below the 85 ceiling
	r.APIUtilReadable = true
	d := EvaluateGuards(r)
	if !d.Approved {
		t.Fatalf("a readable under-ceiling utilization must PASS; got blocked by %q: %s", d.BlockedBy, d.Arithmetic())
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

// --- sp-a3yn: the EXPLORER payback exemption (the crux) ----------------------

// explorerPassingRequest is an EXPLORER candidate where every REUSED guard passes and the
// realized-rate inputs are UNSET (MarginalRate=0, RateReadable=false, EraReadable=false) — exactly
// what an explorer looks like (it buys REACH, not income, so it has no marginal rate). A non-
// explorer with these same unset rate inputs would fail the era-payback + realized-rate guards
// CLOSED; the explorer is EXEMPT, so this request must be APPROVED. Each explorer test flips ONE
// field to pin exactly one reused guard's refusal, proving the exemption did NOT open any other gate.
func explorerPassingRequest() PurchaseRequest {
	return PurchaseRequest{
		Class:    HullClassExplorer,
		ShipType: "SHIP_EXPLORER",

		Shortfall: 1,

		CurrentClassCount: 0, // no explorer owned yet
		ClassCeiling:      1, // HARD CAP 1
		CurrentTotalCount: 20,
		TotalCeiling:      50,

		PurchasesThisTick: 0,
		PerTickCap:        1,

		Price:              819000,
		PriceReadable:      true,
		CheapestKnownPrice: 819000,
		MaxPriceClass:      900000, // ~819k + premium price ceiling
		MaxPremiumPct:      50,

		// Realized-rate inputs deliberately UNSET — the explorer has none.
		HoursToEraEnd:  0,
		EraReadable:    false,
		EraCutoffHours: 3,
		PaybackSafety:  0.5,
		MarginalRate:   0,
		RateFloor:      0,
		RateReadable:   false,
		RateDeclining:  false,

		LiveTreasury:      10000000,
		TreasuryReadable:  true,
		ReserveAbsolute:   200000,
		ReservePct:        40,
		MarginOverFloor:   200000,
		TreasuryPctPerBuy: 25, // big-ticket 25%-treasury affordability rule DOES apply to the explorer

		APIUtilPct:      40,
		APIUtilReadable: true,
		APIUtilCeiling:  85,
	}
}

// The exemption itself: an explorer with NO provable payback (era + rate unreadable) is APPROVED —
// it is exploration-justified, not income-justified. This is the whole point of the feature.
func TestGuard_Explorer_ExemptFromRealizedRatePaybackGuards(t *testing.T) {
	d := EvaluateGuards(explorerPassingRequest())
	if !d.Approved {
		t.Fatalf("explorer must be APPROVED despite unset payback/rate (exploration-justified); blocked by %q: %s", d.BlockedBy, d.Arithmetic())
	}
}

// THE CRITICAL REGRESSION + class-gate mutation guard: a NON-explorer (light) with the SAME unset
// payback inputs is STILL REFUSED. If the class-gate on the exemption is removed (exemption applied
// to every class), this test fails — proving the carve-out is scoped to HullClassExplorer ONLY.
func TestGuard_NonExplorer_UnprovablePayback_StillRefused(t *testing.T) {
	r := explorerPassingRequest()
	r.Class = HullClassLight // a light with no provable payback must NOT get the exemption
	d := EvaluateGuards(r)
	if d.Approved {
		t.Fatalf("a NON-explorer with unprovable payback must be REFUSED — the exemption leaked to %q; arithmetic: %s", r.Class, d.Arithmetic())
	}
	if d.BlockedBy != GuardEraPayback {
		t.Fatalf("a non-explorer with unreadable era clock must block on era_payback first, got %q: %s", d.BlockedBy, d.Arithmetic())
	}
}

// The explorer decision log carries an explicit explorer_exempt verdict and does NOT run the two
// income guards — the captain reading the log sees exactly why the payback proof was waived.
func TestGuard_Explorer_ExemptVerdictLoggedAndIncomeGuardsSkipped(t *testing.T) {
	arith := EvaluateGuards(explorerPassingRequest()).Arithmetic()
	if !strings.Contains(arith, string(GuardExplorerExempt)) {
		t.Errorf("explorer arithmetic must carry the explorer_exempt verdict: %s", arith)
	}
	if strings.Contains(arith, string(GuardEraPayback)) || strings.Contains(arith, string(GuardRealizedRate)) {
		t.Errorf("explorer must NOT run the era_payback/realized_rate income guards: %s", arith)
	}
}

// The HARD CAP is enforced by the reused fleet-ceiling guard with ClassCeiling=1: a second explorer
// (one already owned) is refused. The exemption did not disable the ceiling.
func TestGuard_Explorer_HardCapCeilingRefusesSecond(t *testing.T) {
	r := explorerPassingRequest()
	r.CurrentClassCount = 1 // one explorer already owned; ceiling is 1
	assertBlockedBy(t, r, GuardFleetCeiling)
}

// The PRICE CEILING still bites: an explorer priced above the ~819k+premium cap is refused.
func TestGuard_Explorer_PriceCeilingRefusesOverpriced(t *testing.T) {
	r := explorerPassingRequest()
	r.Price = 950000 // above the 900000 class cap
	assertBlockedBy(t, r, GuardPriceCeiling)
}

// The DEMAND gate still bites: no shortfall ⇒ no buy (the explorer is not exempt from needing demand).
func TestGuard_Explorer_DemandGateRefusesZeroShortfall(t *testing.T) {
	r := explorerPassingRequest()
	r.Shortfall = 0
	assertBlockedBy(t, r, GuardDemand)
}

// The REUSED treasury guards still bite for the explorer (fail-closed on unreadable, and the 25%
// affordability rule + reserve floor still gate the ~819k spend).
func TestGuard_Explorer_ReusedTreasuryGuardsStillBite(t *testing.T) {
	unreadable := explorerPassingRequest()
	unreadable.TreasuryReadable = false
	// treasury_pct (25%, applied to the explorer) fail-closes on unreadable treasury and is first.
	assertBlockedBy(t, unreadable, GuardTreasuryPct)

	tooExpensive := explorerPassingRequest()
	tooExpensive.LiveTreasury = 2000000 // 25% = 500000 < price 819000 → affordability rule blocks
	assertBlockedBy(t, tooExpensive, GuardTreasuryPct)

	apiSaturated := explorerPassingRequest()
	apiSaturated.APIUtilReadable = false // sp-a5dq fail-closed still holds for the explorer
	assertBlockedBy(t, apiSaturated, GuardAPIUtil)
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

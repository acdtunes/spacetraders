package commands

import (
	"fmt"
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

		PurchasesThisTick: 0,
		PerTickCap:        1,

		Price:              437000,
		PriceReadable:      true,
		CheapestKnownPrice: 400000,
		MaxPriceClass:      0, // no absolute cap
		MaxPremiumPct:      50,

		LiveTreasury:      5000000,
		TreasuryReadable:  true,
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

// THE ANTI-THRASH STREAK, now part of demand's verdict. A real shortfall that has NOT yet persisted
// StreakMin consecutive ticks must still refuse the buy — a transient spike in the lane ranking must
// not spend ~1.4M. Parametrized over the boundary (Mandate 5: one behaviour, input variations).
//
// Deleting the streak term from guardDemand makes the mid-streak rows approve a purchase the fleet
// deliberately holds.
func TestGuard_Demand_BlocksUntilTheShortfallPersistsTheStreak(t *testing.T) {
	cases := []struct {
		streak    int
		wantBlock bool
	}{
		{streak: 0, wantBlock: true},  // first tick of the episode
		{streak: 1, wantBlock: true},  // mid-streak
		{streak: 2, wantBlock: true},  // one short of the minimum
		{streak: 3, wantBlock: false}, // == minimum → the need is settled, buy
		{streak: 9, wantBlock: false}, // long-standing
	}
	for _, tc := range cases {
		r := heavyRequest()
		r.Shortfall = 17
		r.ShortfallStreak = tc.streak
		r.ShortfallStreakMin = 3
		d := EvaluateGuards(r)
		if blocked := d.BlockedBy == GuardDemand; blocked != tc.wantBlock {
			t.Errorf("streak %d/3: demand blocked=%v, want %v — arithmetic: %s", tc.streak, blocked, tc.wantBlock, d.Arithmetic())
		}
		// The whole go/no-go must be readable on the ONE line — the streak used to log separately.
		if !strings.Contains(d.Arithmetic(), fmt.Sprintf("persisting %d/3 ticks", tc.streak)) {
			t.Errorf("streak %d/3: the demand term must carry the streak arithmetic, got: %s", tc.streak, d.Arithmetic())
		}
	}
}

// A class that does not use the streak (StreakMin 0) is unaffected: a light with a shortfall and no
// streak history buys immediately. The fold must not impose a heavy-only hold on every class.
func TestGuard_Demand_StreakIsANoOpForClassesThatDoNotUseIt(t *testing.T) {
	r := passingRequest() // HullClassLight: ShortfallStreak 0, ShortfallStreakMin 0
	d := EvaluateGuards(r)
	if !d.Approved {
		t.Fatalf("a light with a shortfall and no streak configured must not be held; blocked by %q: %s", d.BlockedBy, d.Arithmetic())
	}
	if strings.Contains(d.Arithmetic(), "anti-thrash") {
		t.Errorf("a class with no streak must not print a streak term: %s", d.Arithmetic())
	}
}

func TestGuard_ClassCeiling_ClassFull(t *testing.T) {
	r := passingRequest()
	r.CurrentClassCount = 35 // == ceiling
	assertBlockedBy(t, r, GuardClassCeiling)
}

func TestGuard_PerTickCap_Exhausted(t *testing.T) {
	r := passingRequest()
	r.PurchasesThisTick = 1 // == cap
	assertBlockedBy(t, r, GuardPerTickCap)
}

// price FAILS CLOSED on an unreadable yard ask (RULINGS #4) — the merge kept price_read's whole
// job. An unpriceable hull is never bought.
func TestGuard_Price_UnreadableFailsClosed(t *testing.T) {
	r := passingRequest()
	r.PriceReadable = false
	assertBlockedBy(t, r, GuardPrice)
}

func TestGuard_Price_AbsoluteCap(t *testing.T) {
	r := passingRequest()
	r.MaxPriceClass = 400000 // price 437000 exceeds the absolute cap
	assertBlockedBy(t, r, GuardPrice)
}

func TestGuard_Price_PremiumOverCheapest(t *testing.T) {
	r := passingRequest()
	r.CheapestKnownPrice = 200000 // cap = 200000 * 1.5 = 300000 < price 437000
	assertBlockedBy(t, r, GuardPrice)
}

// heavyRequest is a HEAVY (trade) candidate where every guard passes — the all-pass light request
// with the class flipped, so a test that flips ONE field pins exactly one heavy-path refusal.
func heavyRequest() PurchaseRequest {
	r := passingRequest()
	r.Class = HullClassHeavy
	r.ShipType = "SHIP_HEAVY_FREIGHTER"
	// The heavy-hull cap is a separate dial with its own tests (heavy_cap_test.go); open it
	// here with a readable census so this file keeps pinning the guard each test is about.
	r.HeaviesOwned = 1
	r.HeavyCap = 5
	r.HeaviesOwnedReadable = true
	return r
}

// CONJUNCTIVE MERGE, TERM 1 OF 2: affordability refuses on the PERCENT term ALONE.
//
// The floor+margin term is deliberately SATISFIED here (spendable 950000 >= 537000), so the only
// thing that can refuse this purchase is the analyst's 25%-of-treasury single-hull rule. Deleting
// the percent term from the merged guard makes this test pass a purchase it must refuse — which is
// exactly the loosening a structural merge must never introduce. This test and its floor twin are
// separate on purpose: one test cannot prove a conjunction kept both of its terms.
func TestGuard_Affordability_RefusesOnThePercentTermAlone(t *testing.T) {
	r := passingRequest()
	r.LiveTreasury = 1000000   // 25% = 250000 < price 437000 → the percent rule refuses
	r.MarginOverFloor = 100000 // floor term PASSES: 1000000 − 50000 = 950000 >= 437000+100000
	assertBlockedBy(t, r, GuardAffordability)
}

// The percent rule is per-class and OFF for lights (pct=0). "Off" must mean this TERM does not
// refuse — never that the merged guard waves the buy through: the floor+margin term below is
// satisfied here, and its own test proves it still bites when it is not.
func TestGuard_Affordability_PercentTermNotAppliedWhenZero(t *testing.T) {
	r := passingRequest()
	r.TreasuryPctPerBuy = 0   // lights: affordability-% rule off
	r.LiveTreasury = 600000   // would fail a 25% rule, but the rule is off
	r.MarginOverFloor = 50000 // flat floor 50000, spendable 550000 ≥ 437000+50000 = 487000
	d := EvaluateGuards(r)
	for _, v := range d.Verdicts {
		if v.Guard == GuardAffordability && !v.Passed {
			t.Fatalf("the percent term must not refuse when the rule is off (pct=0), got block: %s", v.Detail)
		}
	}
}

func TestGuard_APIUtil_AboveCeilingBlocks(t *testing.T) {
	r := passingRequest()
	r.APIUtilPct = 90 // above the 85 ceiling
	assertBlockedBy(t, r, GuardAPIUtil)
}

// The guard blocks concurrency GROWTH the moment utilization reaches the ceiling — the
// "at/over the ceiling" boundary (a pass requires strictly-below).
func TestGuard_APIUtil_AtCeilingBlocks(t *testing.T) {
	r := passingRequest()
	r.APIUtilPct = 85 // == the 85 ceiling
	assertBlockedBy(t, r, GuardAPIUtil)
}

// An unreadable utilization signal fails CLOSED (holds growth). RULINGS #4: a guard that cannot
// read its bound never permits the spend.
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

// CONJUNCTIVE MERGE, TERM 2 OF 2: affordability refuses on the FLOOR+MARGIN term ALONE.
//
// The percent term is deliberately SATISFIED and LEFT ON (pct=25, cap 25% × 2,000,000 = 500000 >=
// price 437000), so the percent rule has no objection to this buy — only the immutable reserve
// floor plus the required margin does. Deleting the floor term from the merged guard makes this
// test approve a purchase that would leave the treasury under its reserve.
func TestGuard_Affordability_RefusesOnTheFloorAndMarginTermAlone(t *testing.T) {
	r := passingRequest()
	r.LiveTreasury = 2000000    // percent term PASSES: 25% = 500000 >= price 437000
	r.MarginOverFloor = 1600000 // floor term REFUSES: 2000000 − 50000 = 1950000 < 437000+1600000
	assertBlockedBy(t, r, GuardAffordability)
}

// RULINGS #4: an unreadable treasury fails CLOSED — a buy must never proceed on an unknown balance.
// Pinned with the percent rule BOTH on and off, because the merged guard hoisted the readability
// check above both terms: with pct=0 the old treasury_pct passed vacuously and only treasury_floor
// refused, so the pair's refusal had to survive the hoist in that case too.
func TestGuard_Affordability_UnreadableTreasuryFailsClosed(t *testing.T) {
	for _, pct := range []int{25, 0} {
		r := passingRequest()
		r.TreasuryReadable = false
		r.TreasuryPctPerBuy = pct
		assertBlockedBy(t, r, GuardAffordability)
	}
}

// The decision log carries the full arithmetic for every guard (the park-line idiom).
func TestDecision_ArithmeticLogsEveryGuard(t *testing.T) {
	d := EvaluateGuards(passingRequest())
	arith := d.Arithmetic()
	for _, name := range []GuardName{GuardDemand, GuardClassCeiling, GuardPerTickCap, GuardPrice, GuardHeavyCap, GuardAffordability, GuardAPIUtil} {
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

// --- The EXPLORER class runs the SAME guard stack as everything else ----------------------

// explorerPassingRequest is an EXPLORER candidate where every REUSED guard passes. Each explorer
// test flips ONE field to pin exactly one reused guard's refusal, proving the exemption did NOT open
// any other gate.
func explorerPassingRequest() PurchaseRequest {
	return PurchaseRequest{
		Class:    HullClassExplorer,
		ShipType: "SHIP_EXPLORER",

		Shortfall: 1,

		CurrentClassCount: 0, // no explorer owned yet
		ClassCeiling:      1, // HARD CAP 1

		PurchasesThisTick: 0,
		PerTickCap:        1,

		Price:              819000,
		PriceReadable:      true,
		CheapestKnownPrice: 819000,
		MaxPriceClass:      900000, // ~819k + premium price ceiling
		MaxPremiumPct:      50,

		LiveTreasury:      10000000,
		TreasuryReadable:  true,
		MarginOverFloor:   200000,
		TreasuryPctPerBuy: 25, // big-ticket 25%-treasury affordability rule DOES apply to the explorer

		APIUtilPct:      40,
		APIUtilReadable: true,
		APIUtilCeiling:  85,
	}
}

// The HARD CAP is enforced by the reused fleet-ceiling guard with ClassCeiling=1: a second explorer
// (one already owned) is refused. The exemption did not disable the ceiling.
func TestGuard_Explorer_HardCapCeilingRefusesSecond(t *testing.T) {
	r := explorerPassingRequest()
	r.CurrentClassCount = 1 // one explorer already owned; ceiling is 1
	assertBlockedBy(t, r, GuardClassCeiling)
}

// The PRICE CEILING still bites: an explorer priced above the ~819k+premium cap is refused.
func TestGuard_Explorer_PriceCeilingRefusesOverpriced(t *testing.T) {
	r := explorerPassingRequest()
	r.Price = 950000 // above the 900000 class cap
	assertBlockedBy(t, r, GuardPrice)
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
	assertBlockedBy(t, unreadable, GuardAffordability)

	tooExpensive := explorerPassingRequest()
	tooExpensive.LiveTreasury = 2000000 // 25% = 500000 < price 819000 → affordability rule blocks
	assertBlockedBy(t, tooExpensive, GuardAffordability)

	apiSaturated := explorerPassingRequest()
	apiSaturated.APIUtilReadable = false // fail-closed still holds for the explorer
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

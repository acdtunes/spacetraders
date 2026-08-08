package commands

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
)

// passingRequest is a candidate purchase where EVERY guard passes. Each test flips ONE field to
// pin exactly one guard's refusal (or one fail-closed path), so a regression names the guard it
// broke.
func passingRequest() PurchaseRequest {
	return PurchaseRequest{
		Class:    HullClassLight,
		ShipType: "SHIP_LIGHT_HAULER",

		Shortfall: 3,

		PurchasesThisTick: 0,
		PerTickCap:        1,

		Price:              437000,
		PriceReadable:      true,
		CheapestKnownPrice: 400000,
		MaxPriceClass:      0, // no absolute cap
		MaxPremiumPct:      50,

		LiveTreasury:     5000000,
		TreasuryReadable: true,
		MarginOverFloor:  200000,
		// The percentage-of-treasury ceiling is the SHIPPED state: unapplied. Tests that are about
		// that term set it explicitly, so none of the others depend on the fixture's choice.
		TreasuryPctPerBuy: 0,

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
// The term is no longer applied by any shipped configuration, so the request opts it back IN — which
// makes this a restorability test as well: an operator who sets the knob gets a working ceiling
// back, without a merge. The floor+margin term is deliberately SATISFIED here (spendable 950000 >=
// 537000), so the only thing that can refuse this purchase is the percentage rule. Deleting the
// percent term from the merged guard makes this test pass a purchase it must refuse. This test and
// its floor twin are separate on purpose: one test cannot prove a conjunction kept both of its terms.
func TestGuard_Affordability_RefusesOnThePercentTermAlone(t *testing.T) {
	r := passingRequest()
	r.TreasuryPctPerBuy = 25   // the operator restored the ceiling
	r.LiveTreasury = 1000000   // 25% = 250000 < price 437000 → the percent rule refuses
	r.MarginOverFloor = 100000 // floor term PASSES: 1000000 − 50000 = 950000 >= 437000+100000
	assertBlockedBy(t, r, GuardAffordability)
}

// THE BEHAVIOUR CHANGE, pinned rather than implied: a hull the floor term can afford is no longer
// refused for costing more than a quarter of the treasury. The counterfactual runs on the IDENTICAL
// request, so this cannot pass on a fixture the floor term was going to allow through anyway — the
// second half proves the ceiling really would have refused it.
func TestGuard_Affordability_RevokedCeilingNoLongerRefusesWhatTheFloorAffords(t *testing.T) {
	r := heavyRequest()
	r.LiveTreasury = 3_237_633
	r.Price = 1_742_500
	r.CheapestKnownPrice = 1_742_500 // priced at the cheapest known ask: no premium objection
	r.MarginOverFloor = 200_000
	// floor term: 3237633 − 50000 = 3187633 >= 1742500 + 200000 = 1942500
	if d := EvaluateGuards(r); !d.Approved {
		t.Fatalf("the floor term affords this hull, so it must no longer be refused; blocked by %q: %s", d.BlockedBy, d.Arithmetic())
	}

	r.TreasuryPctPerBuy = 25 // 25% = 809408 < price 1742500 — what the revoked rule did
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
// The percent term never objects on either row — LEFT ON at 25 its cap is 25% × 2,000,000 = 500000
// >= price 437000, and at 0 it is not applied at all — so only the immutable reserve floor plus the
// required margin can refuse this buy. Running BOTH rows is what proves the shipped configuration
// did not take the floor term with it: deleting the floor term approves a purchase that would leave
// the treasury under its reserve, and it does so whatever the ceiling is set to.
func TestGuard_Affordability_RefusesOnTheFloorAndMarginTermAlone(t *testing.T) {
	for _, pct := range []int{25, 0} {
		r := passingRequest()
		r.TreasuryPctPerBuy = pct
		r.LiveTreasury = 2000000    // percent term PASSES (or is off)
		r.MarginOverFloor = 1600000 // floor term REFUSES: 2000000 − 50000 = 1950000 < 437000+1600000
		assertBlockedBy(t, r, GuardAffordability)
	}
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
	for _, name := range []GuardName{GuardDemand, GuardPerTickCap, GuardPrice, GuardHeavyCap, GuardAffordability, GuardAPIUtil} {
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
	r.Shortfall = 0         // demand (first) blocks
	r.PurchasesThisTick = 1 // per_tick_cap (later) would also block
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

// Working capital is a term ABOVE the immutable floor. A purchase that clears the floor and
// the margin must still be refused when it would not leave the observed working capital behind.
func TestGuardAffordability_WorkingCapitalRaisesTheFloor(t *testing.T) {
	req := PurchaseRequest{
		Class: HullClassHeavy, TreasuryReadable: true,
		LiveTreasury: 1_000_000, Price: 700_000, MarginOverFloor: 200_000,
		// treasury 1_000_000 − floor 50_000 = 950_000 >= 700_000 + 200_000 = 900_000 : PASSES today.
	}
	if v := guardAffordability(req); !v.Passed {
		t.Fatalf("baseline must pass before the new term is added: %s", v.Detail)
	}
	req.WorkingCapital = 100_000
	// 1_000_000 − 50_000 − 100_000 = 850_000 < 900_000 : must now BLOCK.
	v := guardAffordability(req)
	if v.Passed {
		t.Fatalf("working capital must raise the effective floor: %s", v.Detail)
	}
	if !strings.Contains(v.Detail, "working capital") {
		t.Fatalf("the arithmetic must NAME the term an operator would retune: %s", v.Detail)
	}
}

// The term is NEVER waived, including for the heavy purchase itself. The heavy RESERVE is
// waived because that reservation exists FOR this buy; working capital is the trading fleet's
// committed cargo spend, which the purchase does not fund and must not consume.
func TestGuardAffordability_WorkingCapitalIsNotWaivedForHeavies(t *testing.T) {
	req := PurchaseRequest{
		Class: HullClassHeavy, TreasuryReadable: true,
		LiveTreasury: 1_000_000, Price: 700_000, MarginOverFloor: 200_000,
		HeavyReserve: 400_000, WorkingCapital: 100_000,
	}
	v := guardAffordability(req)
	if v.Passed {
		t.Fatalf("the heavy reserve is waived but working capital is not: %s", v.Detail)
	}
}

// A zero term is byte-identical to the pre-existing behaviour — the migration's safety net
// while both coordinators exist.
func TestGuardAffordability_ZeroWorkingCapitalIsUnchanged(t *testing.T) {
	req := PurchaseRequest{
		Class: HullClassHeavy, TreasuryReadable: true,
		LiveTreasury: 1_000_000, Price: 700_000, MarginOverFloor: 200_000,
	}
	if v := guardAffordability(req); !v.Passed {
		t.Fatalf("a zero working-capital term must change nothing: %s", v.Detail)
	}
}

// A NEGATIVE term is the one direction this guard may never move: subtracted as handed it would
// ADD spendable credits the treasury does not hold and BUY a hull the floor refused. The clamp
// must land a malformed derivation on exactly the zero-term verdict — the arithmetic included,
// because a detail that prints the negative is a decision log that lies about the floor it applied.
func TestGuardAffordability_NegativeWorkingCapitalCannotLoosenTheFloor(t *testing.T) {
	for _, class := range []HullClass{HullClassHeavy, HullClassLight} {
		t.Run(string(class), func(t *testing.T) {
			// treasury 1_000_000 − floor 50_000 = 950_000 < 900_000 + 100_000: the fixture BLOCKS
			// with the term at zero, so an unclamped negative is the only thing that can pass it.
			atZero := PurchaseRequest{
				Class: class, TreasuryReadable: true,
				LiveTreasury: 1_000_000, Price: 900_000, MarginOverFloor: 100_000,
			}
			want := guardAffordability(atZero)
			if want.Passed {
				t.Fatalf("the fixture must block at a zero term or the clamp is never exercised: %s", want.Detail)
			}

			negative := atZero
			negative.WorkingCapital = -500_000
			got := guardAffordability(negative)
			if got.Passed {
				t.Fatalf("a negative working capital must not buy headroom the treasury lacks: %s", got.Detail)
			}
			if got.Detail != want.Detail {
				t.Fatalf("the clamped verdict must read identically to the zero term:\n zero: %s\n  neg: %s", want.Detail, got.Detail)
			}
			if !strings.Contains(got.Detail, "working capital 0") {
				t.Fatalf("the clamped term must print the 0 it applied, never the negative it was handed: %s", got.Detail)
			}
		})
	}
}

// THE DECISION LINE MUST NAME THE ARM THAT BOUND. Working capital is a max() over two terms that
// fail for opposite reasons and are answered with different knobs, and a line carrying only the
// total says a heavy was refused without saying by which measure — the diagnosis this guard exists
// to hand an operator becomes a source read.
//
// The frame is the live staging block of 2026-08-08 07:02, with the corrected measure: nine trade
// hulls, a fully recovered cargo position (runway 0) and a hold-bounded float of 1638000.
func TestGuardAffordability_NamesTheBindingWorkingCapitalArm(t *testing.T) {
	req := PurchaseRequest{
		Class: HullClassHeavy, TreasuryReadable: true,
		LiveTreasury: 2_815_910, Price: 1_742_500, MarginOverFloor: 200_000,
		HeavyReserve:       1_742_500,
		WorkingCapital:     1_638_000,
		WorkingCapitalArms: fleetgrowth.WorkingCapitalTerms{Runway: 0, HoldFill: 1_638_000},
	}

	v := guardAffordability(req)
	for _, want := range []string{"working capital 1638000", "hold_fill binds", "runway 0", "hold_fill 1638000"} {
		if !strings.Contains(v.Detail, want) {
			t.Fatalf("the decision line must carry %q so the binding arm is legible in the field: %s", want, v.Detail)
		}
	}
}

// THE ANTI-VACUITY CONTROL FOR THE LINE ABOVE: the same rendering on a cold-start frame names the
// OTHER arm. A clause hard-coded to one arm passes the test above and fails this one.
func TestGuardAffordability_NamesTheRunwayArmWhenItBinds(t *testing.T) {
	req := PurchaseRequest{
		Class: HullClassHeavy, TreasuryReadable: true,
		LiveTreasury: 2_815_910, Price: 1_742_500, MarginOverFloor: 200_000,
		WorkingCapital:     1_000_000,
		WorkingCapitalArms: fleetgrowth.WorkingCapitalTerms{Runway: 1_000_000, HoldFill: 160_000},
	}

	v := guardAffordability(req)
	if !strings.Contains(v.Detail, "runway binds") {
		t.Fatalf("a runway-bound frame must say so: %s", v.Detail)
	}
	if strings.Contains(v.Detail, "hold_fill binds") {
		t.Fatalf("only one arm may be named as binding: %s", v.Detail)
	}
}

// A REQUEST CARRYING NO ARMS PRINTS NO ARM CLAUSE. The arms are a display term filled by the one
// production call site; a caller that has not filled them must not have zeros attributed to it as
// if they were measured, and the un-armed line must read exactly as it did before this change —
// which is also what keeps the clamped-negative verdict above byte-identical to the zero one.
func TestGuardAffordability_UnattributedWorkingCapitalPrintsNoArmClause(t *testing.T) {
	req := PurchaseRequest{
		Class: HullClassHeavy, TreasuryReadable: true,
		LiveTreasury: 1_000_000, Price: 700_000, MarginOverFloor: 200_000,
		WorkingCapital: 100_000,
	}
	if v := guardAffordability(req); strings.Contains(v.Detail, "binds") {
		t.Fatalf("no arm may be named when none was measured: %s", v.Detail)
	}
}

// The term binds EVERY class. The waiver in this guard belongs to the heavy RESERVE alone —
// working capital is the trading fleet's committed cargo spend whatever hull is being priced, so
// scoping it to heavies would let the other spender consume it.
func TestGuardAffordability_WorkingCapitalBindsANonHeavyClass(t *testing.T) {
	req := PurchaseRequest{
		Class: HullClassLight, TreasuryReadable: true,
		LiveTreasury: 1_000_000, Price: 700_000, MarginOverFloor: 200_000,
	}
	if v := guardAffordability(req); !v.Passed {
		t.Fatalf("baseline must pass with the term at zero: %s", v.Detail)
	}
	req.WorkingCapital = 100_000
	// 1_000_000 − 50_000 − 100_000 = 850_000 < 900_000: must BLOCK a light hull too.
	v := guardAffordability(req)
	if v.Passed {
		t.Fatalf("working capital must raise the effective floor for every class: %s", v.Detail)
	}
}

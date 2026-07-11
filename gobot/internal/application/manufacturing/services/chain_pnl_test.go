package services

import (
	"math"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// sp-rh2z (analyst redesign C2). ComputeChainPnL is the per-good realized-P&L math the
// chain kill-switch judges: it adapts the validated per-good panel-502 SQL (sp-i0hl)
// Go-side and adds the lift-cost approximation. These pins exercise the MATH from fixture
// rows, decoupled from the DB reader — the "P&L math from fixture rows" the bead mandates.

// floatEq compares two float64 P&L/hr figures within a cent — the math is integer-derived
// so exact-to-the-credit, but the /window division yields floats.
func floatEq(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// A vertically-integrated pair the way panel 502 sees it: the OUTPUT good (CLOTHING) carries
// its realization (tours sell it) with no input cost under its own symbol, while its INPUT
// good (FABRICS) carries the input spend and a thin local sell. Attribution is atomic per
// good_symbol (sp-i0hl: PURCHASE_CARGO is tagged the input good, never rolled up to output),
// so CLOTHING reads as a strong earner and FABRICS as the loser — exactly the hidden-loser
// visibility the ledger exists to give.
func TestComputeChainPnL_PerGoodMathFromFixtureRows(t *testing.T) {
	raw := manufacturing.ChainPnLRaw{
		Goods: []manufacturing.ChainGoodFlow{
			{Good: "CLOTHING", FactoryCost: 0, FactorySell: 0, TourNet: 600000},
			{Good: "FABRICS", FactoryCost: -300000, FactorySell: 50000, TourNet: 0},
		},
		RefuelPool: -6000, // manufacturing+factory refuel over the window (negative = spend)
	}

	got := ComputeChainPnL(raw, 6)

	clothing, ok := got["CLOTHING"]
	if !ok {
		t.Fatalf("expected a CLOTHING result")
	}
	// CLOTHING: no input spend -> no lift attributed. net = 0 + 600000 + 0 - 0 = 600000 over 6h.
	if clothing.LiftCost != 0 {
		t.Errorf("CLOTHING lift = %d, want 0 (no input spend under its own symbol)", clothing.LiftCost)
	}
	if clothing.Net != 600000 {
		t.Errorf("CLOTHING net = %d, want 600000", clothing.Net)
	}
	if !floatEq(clothing.NetPerHour, 100000) {
		t.Errorf("CLOTHING net/hr = %v, want 100000", clothing.NetPerHour)
	}
	if !clothing.HasRealization {
		t.Errorf("CLOTHING must have realization (tours sold 600k)")
	}

	fabrics, ok := got["FABRICS"]
	if !ok {
		t.Fatalf("expected a FABRICS result")
	}
	// FABRICS is the ONLY input-spending good, so it absorbs the whole 6000 lift pool.
	// net = 50000 (sell) + 0 (tour) - 300000 (input) - 6000 (lift) = -256000 over 6h.
	if fabrics.LiftCost != 6000 {
		t.Errorf("FABRICS lift = %d, want 6000 (sole input-spender absorbs the pool)", fabrics.LiftCost)
	}
	if fabrics.Net != -256000 {
		t.Errorf("FABRICS net = %d, want -256000", fabrics.Net)
	}
	if !floatEq(fabrics.NetPerHour, -256000.0/6.0) {
		t.Errorf("FABRICS net/hr = %v, want %v", fabrics.NetPerHour, -256000.0/6.0)
	}
	if !fabrics.HasRealization {
		t.Errorf("FABRICS must have realization (50k local sell)")
	}
}

// The lift pool splits across input-spending goods in proportion to |input spend|: the
// documented approximation (refuel rows carry no good_symbol, so exact per-chain lift is
// unknowable; input spend is the per-good production-activity proxy).
func TestComputeChainPnL_LiftAttributionProportionalToInputSpend(t *testing.T) {
	raw := manufacturing.ChainPnLRaw{
		Goods: []manufacturing.ChainGoodFlow{
			{Good: "A", FactoryCost: -100000},
			{Good: "B", FactoryCost: -300000},
		},
		RefuelPool: -8000,
	}

	got := ComputeChainPnL(raw, 6)

	// total input spend 400000; A gets 8000*100000/400000=2000, B gets 8000*300000/400000=6000.
	if got["A"].LiftCost != 2000 {
		t.Errorf("A lift = %d, want 2000", got["A"].LiftCost)
	}
	if got["B"].LiftCost != 6000 {
		t.Errorf("B lift = %d, want 6000", got["B"].LiftCost)
	}
	if got["A"].LiftCost+got["B"].LiftCost != 8000 {
		t.Errorf("attributed lift %d must equal the pool 8000 (exact split here)", got["A"].LiftCost+got["B"].LiftCost)
	}
}

// No input spend anywhere -> the lift pool has no per-good production-activity proxy to
// attach to, so it is dropped (not fabricated onto tour-only goods). Documented: a good
// with zero factory buys attracts zero lift.
func TestComputeChainPnL_ZeroInputSpend_NoLiftAttributed(t *testing.T) {
	raw := manufacturing.ChainPnLRaw{
		Goods: []manufacturing.ChainGoodFlow{
			{Good: "ARB_ONLY", FactoryCost: 0, FactorySell: 0, TourNet: 120000},
		},
		RefuelPool: -5000,
	}

	got := ComputeChainPnL(raw, 6)

	if got["ARB_ONLY"].LiftCost != 0 {
		t.Errorf("ARB_ONLY lift = %d, want 0 (no input spend to attach the pool to)", got["ARB_ONLY"].LiftCost)
	}
	if got["ARB_ONLY"].Net != 120000 {
		t.Errorf("ARB_ONLY net = %d, want 120000 (pool dropped, not fabricated on)", got["ARB_ONLY"].Net)
	}
}

// HasRealization is the young-chain guard the kill-switch reads: it is true only when there
// is positive realized output value (factory local sell OR positive tour realization). A
// chain that has bought inputs but not yet realized anything reads false, so the kill-switch
// can fail open on it (realization lags production; killing a pre-realization chain would
// churn kill/resume).
func TestComputeChainPnL_HasRealizationFlag(t *testing.T) {
	raw := manufacturing.ChainPnLRaw{
		Goods: []manufacturing.ChainGoodFlow{
			{Good: "PRE_REALIZATION", FactoryCost: -100000, FactorySell: 0, TourNet: 0},
			{Good: "LOCAL_SELL", FactoryCost: -10000, FactorySell: 40000, TourNet: 0},
			{Good: "TOUR_SELL", FactoryCost: -10000, FactorySell: 0, TourNet: 40000},
			{Good: "TOUR_BUY_ONLY", FactoryCost: 0, FactorySell: 0, TourNet: -40000},
		},
		RefuelPool: 0,
	}

	got := ComputeChainPnL(raw, 6)

	if got["PRE_REALIZATION"].HasRealization {
		t.Errorf("PRE_REALIZATION: input spend but zero realization must read HasRealization=false")
	}
	if !got["LOCAL_SELL"].HasRealization {
		t.Errorf("LOCAL_SELL: a factory local sell is realized output")
	}
	if !got["TOUR_SELL"].HasRealization {
		t.Errorf("TOUR_SELL: positive tour net is realized output")
	}
	if got["TOUR_BUY_ONLY"].HasRealization {
		t.Errorf("TOUR_BUY_ONLY: negative tour net (buys only) is not realized OUTPUT value")
	}
}

// A zero (or absent) window must never divide-by-zero the per-hour figure.
func TestComputeChainPnL_ZeroWindow_NoDivideByZero(t *testing.T) {
	raw := manufacturing.ChainPnLRaw{Goods: []manufacturing.ChainGoodFlow{{Good: "X", FactorySell: 60000}}}
	got := ComputeChainPnL(raw, 0)
	if got["X"].NetPerHour != 0 {
		t.Errorf("zero window must yield net/hr 0 (guarded), got %v", got["X"].NetPerHour)
	}
	// The undivided net is still meaningful and preserved.
	if got["X"].Net != 60000 {
		t.Errorf("net = %d, want 60000", got["X"].Net)
	}
}

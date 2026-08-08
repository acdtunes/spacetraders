package fleetgrowth

import (
	"testing"
	"time"
)

// THE LIVE FRAME (2026-08-08 01:51). The census counted 24 profitable lanes absorbing 741 units in
// one trip while the trade pool's nine hulls carried ~820 units of hold, all of them empty. That
// fleet had outgrown its reachable surface and the wave was demanding sixteen more hulls, which is
// the defect this primitive exists to answer.
func TestTradeSaturated_TheLiveFrameIsSaturated(t *testing.T) {
	if !TradeSaturated(741, 820, DefaultSaturationMarginPct) {
		t.Fatalf("741 units of depth against 820 units of hold must read SATURATED at the default margin")
	}
}

// THE ANTI-VACUITY CONTROL. A surface deeper than the fleet's hold is genuinely capacity-short, and
// the term must be inert there — that state is the ONLY one in which today's code buys a heavy, so
// a primitive that answered "saturated" here would be changing behaviour where it must not.
func TestTradeSaturated_ADeeperSurfaceIsNotSaturated(t *testing.T) {
	if TradeSaturated(821, 820, DefaultSaturationMarginPct) {
		t.Fatalf("one unit of depth past the fleet's hold is capacity-short, not saturated")
	}
}

// PARITY IS SATURATION. A fleet that can lift exactly what the surface absorbs has nothing left for
// another hull to carry, so the boundary is inclusive.
func TestTradeSaturated_ParityIsSaturated(t *testing.T) {
	if !TradeSaturated(820, 820, DefaultSaturationMarginPct) {
		t.Fatalf("depth exactly equal to the fleet's hold must read saturated — the boundary is inclusive")
	}
}

// THE COLD FLEET MUST STAY BUYABLE. With no trade hull standing there is no hold to compare
// against, and a term that read "the surface is not deeper than nothing" as saturation would make
// the FIRST trade hull unbuyable forever — a deadlock no spender could clear.
func TestTradeSaturated_AnEmptyPoolIsNeverSaturated(t *testing.T) {
	for _, depth := range []int{0, 1, 741} {
		if TradeSaturated(depth, 0, DefaultSaturationMarginPct) {
			t.Fatalf("depth %d against an EMPTY trade pool must never read saturated", depth)
		}
	}
	if TradeSaturated(741, -5, DefaultSaturationMarginPct) {
		t.Fatalf("a negative hold is nonsense, and nonsense must not pause the first purchase")
	}
}

// A SURFACE THAT ABSORBS NOTHING IS SATURATED once a pool exists to absorb it: there is no work for
// another hull whatever its hold.
func TestTradeSaturated_ZeroDepthAgainstARealPoolIsSaturated(t *testing.T) {
	if !TradeSaturated(0, 820, DefaultSaturationMarginPct) {
		t.Fatalf("a surface absorbing nothing offers no work for another hull")
	}
}

// THE MARGIN MOVES THE BOUNDARY, AND IN THE DOCUMENTED DIRECTION: raising it declares saturation
// EARLIER (against a smaller fleet), lowering it permits more heavies against the same surface. A
// sign error here inverts the whole knob while every fixed-margin case above still passes.
func TestTradeSaturated_TheMarginMovesTheBoundaryUpward(t *testing.T) {
	const depth, hold = 900, 820
	if TradeSaturated(depth, hold, 100) {
		t.Fatalf("at 100%% a 900-unit surface outruns 820 units of hold")
	}
	if !TradeSaturated(depth, hold, 125) {
		t.Fatalf("at 125%% the same surface must read saturated — raising the margin stops buying earlier")
	}
	if TradeSaturated(741, hold, 50) {
		t.Fatalf("at 50%% the live frame must NOT read saturated — lowering the margin permits more heavies")
	}
}

// A ZERO OR NEGATIVE MARGIN IS AN UNSET KNOB, NOT A SETTING. `tune <key> 0` deletes a key rather
// than setting it, so zero has to mean "the documented default" — and it must mean that HERE too,
// or a resolver bypassed anywhere silently disables the term (0% would make nothing but an empty
// surface saturated).
func TestTradeSaturated_AnUnsetMarginTakesTheDefault(t *testing.T) {
	for _, margin := range []int{0, -1, -100} {
		if TradeSaturated(741, 820, margin) != TradeSaturated(741, 820, DefaultSaturationMarginPct) {
			t.Fatalf("margin %d must resolve to the default, not to a setting of its own", margin)
		}
	}
}

// THE GAUGE AND THE VERDICT ARE ONE ARITHMETIC. SaturationThreshold publishes the number depth is
// compared against, so an operator reads the flip straight off two series; a second computation
// would let the published threshold and the decision drift with nothing failing.
func TestSaturationThreshold_IsTheNumberTheVerdictCompares(t *testing.T) {
	for _, hold := range []int{0, 1, 91, 820, 100_000} {
		for _, margin := range []int{0, 50, 100, 125, 300} {
			threshold := SaturationThreshold(hold, margin)
			for _, depth := range []int{threshold - 1, threshold, threshold + 1} {
				if depth < 0 {
					continue
				}
				want := hold > 0 && depth <= threshold
				if got := TradeSaturated(depth, hold, margin); got != want {
					t.Fatalf("hold=%d margin=%d threshold=%d depth=%d: verdict %v disagrees with the published threshold", hold, margin, threshold, depth, want)
				}
			}
		}
	}
}

// THE DWELL IS A REAL WINDOW. A zero would make the debounce a no-op — the exact tick-to-tick
// oscillation the persistence window exists to prevent — so the default must be positive, and it is
// sized to the growth coordinator's own 900-second tick: the spender must never act on a verdict
// younger than the interval between two of its own decisions.
func TestDefaultSaturationDwell_IsOneGrowthTick(t *testing.T) {
	if DefaultSaturationDwell != 900*time.Second {
		t.Fatalf("the dwell default is %s, want one 900-second growth tick", DefaultSaturationDwell)
	}
}

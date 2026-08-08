package fleetgrowth

import "time"

// TRADE SATURATION — the wave's SWITCH-BACK cue, and the answer to a fleet that has outgrown its
// reachable surface.
//
// The regime's demand clause counts LANES: profitable circuits beyond the trade pool, one wanted
// hull per lane. A lane count says nothing about how much a lane can move, so a fleet whose hold
// already exceeds everything the whole reachable surface absorbs still reads "fifteen lanes
// unserved" and keeps demanding heavies — measured live 2026-08-08 01:51, where 741 units of
// absorbable depth faced ~820 units of hold across nine hulls that all sat EMPTY, and the wave's
// answer was to ask for sixteen more.
//
// THE TWO SIDES ARE THE SAME QUANTITY, WHICH IS WHY THEY MAY BE COMPARED AT ALL. A lane's
// absorbable depth is min(source volume, dest volume) — a PER-TRIP market-absorption bound — and a
// hull's hold is a PER-TRIP capacity. Summed over the counted lanes and over the trade pool, the
// comparison asks one honest question: could this fleet lift, in a single simultaneous pass,
// everything the reachable surface is able to absorb? If it can, another hull carries nothing, and
// the fleet's growth belongs in coverage rather than in capacity.
//
// IT IS DEMAND-REDUCING BY CONSTRUCTION. Saturation only ever forces the PROBE regime, which
// authorises no spend of its own; there is no input to it that can turn a PROBE tick HEAVY.

const (
	// DefaultSaturationMarginPct is the fraction of the trade pool's hold, in percent, that the
	// reachable surface must exceed for the fleet to still count as capacity-short.
	//
	// 100 IS PARITY, AND PARITY IS THE HONEST DEFAULT because both sides are per-trip quantities
	// (see above) — the comparison needs no fudge factor to be dimensionally meaningful. Against the
	// live frame, 741 units of depth on 820 units of hold reads saturated with 79 units (9.6%) to
	// spare: on the right side of the boundary, and by a margin narrow enough that the term would
	// first have bound at roughly 741 units of hold — the ninth hull, which is exactly where the
	// evidence says the fleet stopped being able to fill itself.
	//
	// THE KNOB'S DIRECTION: raising it declares saturation EARLIER, against a smaller fleet, and
	// sends growth into coverage sooner; lowering it permits more heavies against the same surface.
	DefaultSaturationMarginPct = 100

	// DefaultSaturationDwell is how long a CHANGED saturation verdict must hold continuously before
	// it is published — the anti-thrash window.
	//
	// IT IS ONE GROWTH TICK (the coordinator's 900-second default). The wave predicate is pure and
	// stateless because its two consumers tick independently and must agree, so the persistence
	// window cannot live in the predicate the way the heavy buy's shortfall streak lives in the
	// coordinator; it lives in the ONE shared reader both consumers read, measured in WALL CLOCK so
	// that two consumers polling at different rates still see one window rather than two streaks
	// advancing at two speeds. Sizing it to the spender's own tick means no single census reading
	// can flip the regime, the spender never acts on a verdict younger than the interval between two
	// of its own decisions, and the fast-ticking sensing drain cannot whipsaw the probe queue on a
	// scan artifact.
	DefaultSaturationDwell = 900 * time.Second
)

// LaneDemand is ONE tick's reading of the trade surface in BOTH dimensions the regime judges: the
// unserved lane COUNT that has always been there, and the DEPTH comparison that answers it.
//
// IT LIVES IN THE DOMAIN BECAUSE TWO ADAPTERS SHARE IT. The growth coordinator and the sensing
// drain each hold the same reader and must reach the same verdict; a reading type defined in either
// consumer, or in one of their query packages, would put a layer edge between them and invite the
// second derivation this whole design exists to prevent.
//
// EVERY FIELD IS ONE TICK'S OBSERVATION except Saturated, which is the DEBOUNCED verdict — the raw
// comparison of AbsorbableUnits against SaturationThreshold can differ from it inside the reader's
// dwell, and that difference IS the anti-thrash window doing its job.
type LaneDemand struct {
	// UnservedLanes is the profitable, feasible lane count beyond the trade pool, floored at 0 —
	// unchanged by this term, which is added BESIDE the count and never instead of it.
	UnservedLanes int

	// AbsorbableUnits is what the counted lanes can move in ONE TRIP, summed: the depth side.
	AbsorbableUnits int

	// TradeHoldCapacity is the trade-dedicated pool's summed cargo capacity: the fleet side. Hulls
	// dedicated elsewhere are excluded — their holds will never see one of these lanes.
	TradeHoldCapacity int

	// SaturationThreshold is the number AbsorbableUnits was actually compared against, carrying the
	// margin. Published so an operator reads the flip off two series without knowing the knob.
	SaturationThreshold int

	// Saturated is the switch-back verdict AFTER the dwell: the reachable surface absorbs no more,
	// in one trip, than the trade pool's own hold can already lift.
	Saturated bool
}

// SaturationThreshold is the depth, in units, at or below which the reachable surface counts as
// saturated for a trade pool holding holdUnits.
//
// It is EXPORTED and published as a gauge beside the depth it is compared against, so an operator
// reads a flip off two series without knowing the knob. TradeSaturated is defined in terms of it
// rather than restating the arithmetic: a second computation is how a published threshold and the
// decision it claims to explain drift apart with nothing failing.
//
// A non-positive marginPct is an UNSET knob, not a setting of its own — `tune <key> 0` deletes a
// key rather than setting it — so it resolves to DefaultSaturationMarginPct here as well as in the
// config resolver, and a resolver bypassed anywhere cannot silently disable the term.
func SaturationThreshold(holdUnits, marginPct int) int {
	if marginPct <= 0 {
		marginPct = DefaultSaturationMarginPct
	}
	if holdUnits <= 0 {
		return 0
	}
	return int(int64(holdUnits) * int64(marginPct) / 100)
}

// TradeSaturated reports whether the reachable profitable surface can absorb no more than the trade
// pool's own hold, allowing marginPct.
//
// AN EMPTY OR NONSENSICAL POOL IS NEVER SATURATED, and that is the difference between a term and a
// deadlock: with no trade hull standing there is no hold for the surface to be measured against,
// and reading "the surface is not deeper than nothing" as saturation would make the FIRST trade
// hull unbuyable forever, with no spender able to clear it. It is also the direction that leaves
// today's behaviour untouched wherever the term cannot be judged.
func TradeSaturated(depthUnits, holdUnits, marginPct int) bool {
	if holdUnits <= 0 {
		return false
	}
	return depthUnits <= SaturationThreshold(holdUnits, marginPct)
}

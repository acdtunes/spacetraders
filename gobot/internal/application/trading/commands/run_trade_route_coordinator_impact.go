package commands

// run_trade_route_coordinator_impact.go — the era-3 price-impact + cooldown model folded
// into lane ranking. The analyst fitted a per-trade price-impact +
// weak-recovery model on era-3 telemetry that beats snapshot pricing 40-49%
// out-of-sample; this wires it into rankLanesByCircuitRate so a lane a hull's own
// volume would compress, or one the fleet has recently hammered, ranks below its
// snapshot spread and hulls rotate to fresh lanes. The coefficients live in
// config.TradeImpactConfig (refit per era); the ledger lives in domain/trading.

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// laneImpactModel folds the era-3 price-impact coefficients and a live shared
// compression-debt lookup into lane ranking. Its ZERO VALUE is INERT — zero impact
// coefficients, a nil debt lookup and an unset ranking clock — so effectiveSpreadPerUnit
// returns the snapshot spread unchanged and every ranking caller that supplies no model
// (every existing test) ranks exactly as the snapshot ranker did.
type laneImpactModel struct {
	buyImpact  float64 // era-3 config: fractional ask rise per full tradeVolume bought (~0.050)
	sellImpact float64 // era-3 config: fractional bid fall per full tradeVolume sold (~0.015)
	// debt returns the lane's live decayed compression fraction from the shared cooldown
	// ledger. Nil (no ledger wired, or in a unit test) contributes zero.
	debt func(l trading.ArbitrageLane) float64
	// staleness prices what each end's quote AGE costs; rankedAt is the single instant
	// every lane's age is measured from, and a zero one disables the term.
	staleness trading.StalenessDiscount
	rankedAt  time.Time
}

// rankingSpreadPerUnit is the per-unit spread the LANE RANKER scores on:
// effectiveSpreadPerUnit less the staleness haircut on the two quotes the lane was priced
// from. It stays SEPARATE from effectiveSpreadPerUnit because that one is also the
// long-haul engine's marginal-value function, where it SIZES a tranche — and a ranking
// adjustment must not reach a quantity decision. laneCircuitValue calls this one function,
// so there is still exactly one ranking pass over one key.
func (m laneImpactModel) rankingSpreadPerUnit(l trading.ArbitrageLane, plannedUnits int) float64 {
	spread := m.effectiveSpreadPerUnit(l, plannedUnits)
	if m.rankedAt.IsZero() {
		return spread
	}
	spread -= m.staleness.SpreadHaircutPerUnit(l, m.rankedAt)
	if spread < 0 {
		// Bottom of the order, never a negative that sorts as if the lane earned in the
		// other direction. Removing it is the backstop's job, and that runs as its own step.
		return 0
	}
	return spread
}

// effectiveSpreadPerUnit is the per-unit spread the ranker scores a lane on: the
// snapshot spread MINUS (a) the self-compression this hull's own plannedUnits would
// cause at HALF terminal impact (the tranche-average fill) and (b) the live shared
// cooldown debt from the fleet's recent trades on the lane.
//
// Written as snapshot-minus-deltas rather than (EffectiveSellPrice − EffectiveBuyPrice)
// so it is mathematically identical yet returns the snapshot EXACTLY when the model is
// inert or when a lane carries only SpreadPerUnit with Ask/Bid unpopulated (the shape
// of every snapshot-only ranker test) — the delta terms vanish, never re-deriving a zero
// spread from unset prices.
func (m laneImpactModel) effectiveSpreadPerUnit(l trading.ArbitrageLane, plannedUnits int) float64 {
	return float64(l.SpreadPerUnit) - m.selfCompressionCredits(l, plannedUnits) - m.decayedDebtCredits(l)
}

// selfCompressionCredits is the per-unit spread the hull's OWN volume would erase on
// this lane: buying plannedUnits/tv full tradeVolumes lifts the ask and selling them
// drops the bid, each at half terminal impact (the tranche average). Fail-safe: a
// non-positive VolumeCap (unknown tradeVolume) or plannedUnits drops the term entirely
// — never a divide-by-zero, the lane simply ranks on its snapshot (less any debt).
func (m laneImpactModel) selfCompressionCredits(l trading.ArbitrageLane, plannedUnits int) float64 {
	if l.VolumeCap <= 0 || plannedUnits <= 0 {
		return 0
	}
	x := float64(plannedUnits) / float64(l.VolumeCap)
	// (buyEff − Ask) + (Bid − sellEff) = (buyImpact·Ask + sellImpact·Bid)·x/2, the credit
	// narrowing of the spread — identical to SpreadPerUnit − (EffectiveSell − EffectiveBuy).
	return (m.buyImpact*float64(l.SourceAsk) + m.sellImpact*float64(l.DestBid)) * x / 2
}

// decayedDebtCredits converts the lane's live decayed compression FRACTION (from the
// shared ledger) into per-unit spread credits against the lane's mid-price. When
// Ask≈Bid this equals the exact spread narrowing the ask-up + bid-down moves the
// fraction represents. Nil lookup (no ledger) contributes zero.
func (m laneImpactModel) decayedDebtCredits(l trading.ArbitrageLane) float64 {
	if m.debt == nil {
		return 0
	}
	mid := float64(l.SourceAsk+l.DestBid) / 2
	return m.debt(l) * mid
}

// laneCooldownKey is the shared-ledger key for a lane: its (buy-market, sell-market,
// good) identity, so every hull's trade and every rank read address the same entry.
func laneCooldownKey(l trading.ArbitrageLane) trading.LaneKey {
	return trading.LaneKey{Source: l.SourceWaypoint, Dest: l.DestWaypoint, Good: l.Good}
}

// --- optimal-volume extension (sp-mepj §2) ----------------------------------------------
// The long-haul engine reuses this ONE price-impact model (no second model): the same
// buyImpact/sellImpact coefficients that shave the RANKED spread also give the MARGINAL
// unit's spread, from which the optimal tranche falls out directly. effectiveSpreadPerUnit
// above is the tranche AVERAGE (its /2); the marginal is twice that slope.

// marginalCompressionPerUnit is the per-unit slope k of this model's realized-spread
// compression on a lane: k = (buyImpact·Ask + sellImpact·Bid) / VolumeCap — how much the
// NEXT unit's realized spread falls per unit already moved, both sides (source ask up +
// sink bid down). The marginal realized spread at quantity q is SpreadPerUnit − k·q. A
// non-positive VolumeCap (unknown depth) or inert coefficients yield 0 (no modeled
// compression), never a divide-by-zero — matching selfCompressionCredits' own fail-safe.
func (m laneImpactModel) marginalCompressionPerUnit(l trading.ArbitrageLane) float64 {
	if l.VolumeCap <= 0 {
		return 0
	}
	return (m.buyImpact*float64(l.SourceAsk) + m.sellImpact*float64(l.DestBid)) / float64(l.VolumeCap)
}

// marginalSpreadAt is the realized spread the q-th unit clears under this model:
// SpreadPerUnit − k·q (k = marginalCompressionPerUnit). It is the design's marginal
// bid(q)−ask(q); optimalVolume is the largest q for which it still clears the floor.
func (m laneImpactModel) marginalSpreadAt(l trading.ArbitrageLane, q int) float64 {
	return float64(l.SpreadPerUnit) - m.marginalCompressionPerUnit(l)*float64(q)
}

// optimalVolume is the largest tranche whose MARGINAL unit still clears marginalFloor
// credits/unit under this model — the design's optimal_q = argmax Σ(bid(q)−ask(q)) s.t.
// marginal ≥ floor (sp-mepj §2). Since marginal(q) = SpreadPerUnit − k·q, the last unit
// clearing the floor is q ≤ (SpreadPerUnit − floor)/k. Clamped to [0, VolumeCap], the
// single-visit absorption bound. Fail-safe for a MONEY-sizing decision (RULINGS #4 — never
// oversell into a collapsing market):
//   - SpreadPerUnit < floor → 0 (even the first, undepressed unit misses the floor).
//   - VolumeCap ≤ 0 (unknown depth) → 0 (never buy an unbounded amount into an unmeasured
//     sink; this DIVERGES from the ranking path, which fails OPEN and ranks on snapshot —
//     a sizing decision fails closed where an advisory rank does not).
//   - inert model (k ≤ 0, no compression) → VolumeCap (the whole absorption bound clears at
//     the snapshot spread, which already passed the floor check above).
func (m laneImpactModel) optimalVolume(l trading.ArbitrageLane, marginalFloorCredits float64) int {
	if float64(l.SpreadPerUnit) < marginalFloorCredits {
		return 0
	}
	if l.VolumeCap <= 0 {
		return 0
	}
	k := m.marginalCompressionPerUnit(l)
	if k <= 0 {
		return l.VolumeCap
	}
	q := (float64(l.SpreadPerUnit) - marginalFloorCredits) / k
	if q < 0 {
		return 0
	}
	if q > float64(l.VolumeCap) {
		return l.VolumeCap
	}
	return int(q)
}

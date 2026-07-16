package commands

// run_trade_route_coordinator_impact.go — sp-tl68: the era-3 price-impact + cooldown
// model folded into lane ranking. The analyst fitted a per-trade price-impact +
// weak-recovery model on era-3 telemetry that beats snapshot pricing 40-49%
// out-of-sample; this wires it into rankLanesByCircuitRate so a lane a hull's own
// volume would compress, or one the fleet has recently hammered, ranks below its
// snapshot spread and hulls rotate to fresh lanes. The coefficients live in
// config.TradeImpactConfig (refit per era); the ledger lives in domain/trading.

import "github.com/andrescamacho/spacetraders-go/internal/domain/trading"

// laneImpactModel folds the era-3 price-impact coefficients and a live shared
// compression-debt lookup into lane ranking. Its ZERO VALUE is INERT — zero impact
// coefficients and a nil debt lookup — so effectiveSpreadPerUnit returns the snapshot
// spread unchanged and every ranking caller that supplies no model (all pre-sp-tl68
// tests) ranks exactly as the snapshot ranker did.
type laneImpactModel struct {
	buyImpact  float64 // era-3 config: fractional ask rise per full tradeVolume bought (~0.050)
	sellImpact float64 // era-3 config: fractional bid fall per full tradeVolume sold (~0.015)
	// debt returns the lane's live decayed compression fraction from the shared cooldown
	// ledger. Nil (no ledger wired, or in a unit test) contributes zero.
	debt func(l trading.ArbitrageLane) float64
}

// effectiveSpreadPerUnit is the per-unit spread the ranker scores a lane on: the
// snapshot spread MINUS (a) the self-compression this hull's own plannedUnits would
// cause at HALF terminal impact (the tranche-average fill) and (b) the live shared
// cooldown debt from the fleet's recent trades on the lane.
//
// Written as snapshot-minus-deltas rather than (EffectiveSellPrice − EffectiveBuyPrice)
// so it is mathematically identical yet returns the snapshot EXACTLY when the model is
// inert or when a lane carries only SpreadPerUnit with Ask/Bid unpopulated (the shape
// of every pre-sp-tl68 ranker test) — the delta terms vanish, never re-deriving a zero
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

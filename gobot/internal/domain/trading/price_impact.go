package trading

import "time"

// --- era-3 price-impact model (economy-analyst-fitted, out-of-sample validated) ---
//
// The economy analyst fit a per-trade price-impact + weak-recovery model on era-3
// telemetry that beats snapshot pricing 40-49% out-of-sample on next-leg price MAE.
// This file carries the model's FORM (linear price impact in x = units/tradeVolume,
// exponential debt decay); the NUMBERS are era-3-derived and REFIT PER ERA, so they
// live as CONFIG (config.TradeImpactConfig), never as hardcoded operational constants.
// The constants below are only the fallback defaults a zero/absent config resolves to
// — the same "zero value defers to the documented default" idiom the other coordinator
// configs use.
const (
	// DefaultBuyImpactCoefficient is the era-3 fitted ask-impact slope: buying one full
	// tradeVolume (x=1) moves the ask up ~+5.0% (analyst raw fit +0.0499, rounded to
	// 0.050 for the operational knob). dP/P(buy) = +DefaultBuyImpactCoefficient·x.
	DefaultBuyImpactCoefficient = 0.050
	// DefaultSellImpactCoefficient is the era-3 fitted bid-impact slope: selling one full
	// tradeVolume (x=1) moves the bid down ~-1.5% (analyst raw fit -0.0152, rounded to
	// 0.015). dP/P(sell) = -DefaultSellImpactCoefficient·x. The buy leg moves price ~3x
	// harder than the sell leg — buying INTO a thin exporter compresses a lane far more
	// than dumping into a deep importer.
	DefaultSellImpactCoefficient = 0.015
	// DefaultCooldownTau is the era-3 pooled compression-debt decay constant. Organic
	// mean-reversion is WEAK — a compressed lane's price-deviation half-life is ~520 min
	// pooled. Debt decays as exp(-dt/tau); tau=750 min gives a half-life
	// of tau·ln2 ≈ 520 min, matching the pooled fit. Per-tv-tier half-lives (339/663/1681
	// min for tv ≤40 / 41-80 / 81-240) are folded into this pooled default for now; a
	// per-tier tau is a clean future refit (config already carries the single knob).
	DefaultCooldownTau = 750 * time.Minute
)

// EffectiveBuyPrice is the AVERAGE price a hull pays filling a tranche of x=units/tv
// full tradeVolumes at the source, using HALF the terminal impact — the mean fill
// across a tranche that walks the ask from its start to its +buyImpact·x·price
// terminal is ~the midpoint. This is the price the ranker charges a candidate lane so
// a lane this hull's own volume would compress ranks below its snapshot spread.
func EffectiveBuyPrice(ask, x, buyImpact float64) float64 {
	return ask * (1 + buyImpact*x/2)
}

// EffectiveSellPrice is the AVERAGE price a hull receives clearing a tranche of
// x=units/tv full tradeVolumes into the destination, using HALF the terminal impact
// (the mean fill as the bid walks down to its -sellImpact·x·price terminal).
func EffectiveSellPrice(bid, x, sellImpact float64) float64 {
	return bid * (1 - sellImpact*x/2)
}

// PostTradeBuyPrice is the TERMINAL ask after a hull has bought x=units/tv full
// tradeVolumes: ask·(1 + buyImpact·x), i.e. the analyst's fitted dP/P = +buyImpact·x.
// This is the FULL move (not the half the tranche-average effective price uses) — it
// predicts what the NEXT observer sees after this trade, the quantity the analyst's
// out-of-sample MAE was measured against.
func PostTradeBuyPrice(ask, x, buyImpact float64) float64 {
	return ask * (1 + buyImpact*x)
}

// PostTradeSellPrice is the TERMINAL bid after a hull has sold x=units/tv full
// tradeVolumes: bid·(1 - sellImpact·x), the analyst's fitted dP/P = -sellImpact·x.
func PostTradeSellPrice(bid, x, sellImpact float64) float64 {
	return bid * (1 - sellImpact*x)
}

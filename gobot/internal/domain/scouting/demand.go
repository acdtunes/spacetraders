package scouting

import "math"

// demand.go holds the DEMAND-WEIGHT math for the market-freshness sizer (sp-wuksw): a
// per-sink freshness weight derived from where the fleet ACTUALLY earns — realized SELL
// legs — rather than the intrinsic Σ(trade_volume × price) throughput proxy the census
// carries. It supersedes the intrinsic weight as the PRIMARY input to the value-weighted
// freshness percentile so that under probe scarcity the sizer holds SLA on the sinks the
// fleet sells through and lets zero-demand markets breach, while a never-traded market keeps
// its intrinsic prior (byte-identical to pre-sp-wuksw when there is no realized demand).
//
// Like circuit.go this is pure math with no I/O: the application maps tour_leg_telemetry
// SELL legs to the slim SinkSale shape (keeping the scouting domain free of the trading
// package, mirroring autooutfit's toLegSaturation), and this file turns them into weights.

// SinkSale is one realized SELL leg reduced to what the demand weight needs: the sink
// waypoint, the realized sell-value (units × price), and how long ago it realized. A BUY leg
// is not a sink and is filtered by the caller before mapping.
type SinkSale struct {
	Waypoint   string
	Value      float64
	AgeSeconds float64
}

// DemandWeightsBySink computes the realized-demand freshness weight per sink waypoint: an
// exponentially recency-weighted (EWMA-style) sum of realized sell-value, where a sale
// halfLifeSeconds old counts half as much as one just realized (2^-(age/halfLife)). A
// heavily- and recently-sold sink accumulates a large weight, so its staleness pulls the
// freshness percentile onto it and it wins scarce probes; a lane that stopped being traded
// decays away and a newly-hit deep sink climbs — the auto-adaptive behaviour the weighting
// exists to produce.
//
// A non-positive half-life disables decay (every sale counts in full — a plain windowed sum).
// A negative age is floored to 0 so a clock skew cannot inflate a weight, and a non-positive
// value is skipped so a skipped/degraded leg (RealizedUnits=0) never creates a sink entry —
// that market then falls back to its intrinsic prior at the call site. Empty input returns an
// empty map (the cold-start signal: no realized demand, keep every market on its intrinsic
// weight, byte-identical to pre-sp-wuksw).
func DemandWeightsBySink(sales []SinkSale, halfLifeSeconds float64) map[string]float64 {
	weights := make(map[string]float64, len(sales))
	for _, sale := range sales {
		if sale.Value <= 0 {
			continue
		}
		weights[sale.Waypoint] += sale.Value * recencyDecay(sale.AgeSeconds, halfLifeSeconds)
	}
	return weights
}

// recencyDecay is the exponential half-life weight for a sale AgeSeconds old: 1 at age 0,
// 0.5 at one half-life, 0.25 at two. A non-positive half-life disables decay (weight 1); a
// negative age is floored to 0.
func recencyDecay(ageSeconds, halfLifeSeconds float64) float64 {
	if halfLifeSeconds <= 0 {
		return 1
	}
	if ageSeconds < 0 {
		ageSeconds = 0
	}
	return math.Pow(0.5, ageSeconds/halfLifeSeconds)
}

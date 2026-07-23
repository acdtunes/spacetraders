package commands

// run_longhaul_arb_pricing.go — honest multi-hop, realized-price-impact lane pricing for
// the long-haul engine (sp-mepj §2). It REUSES the shared laneImpactModel
// (effectiveSpreadPerUnit / optimalVolume — one price-impact model, no copy) and the shared
// crossSystemHopSeconds hop constant, but prices the REAL gate-graph hop count instead of
// the circuit ranker's fixed 2-hop round-trip surcharge — the multi-hop honesty the 1-hop
// discovery horizon structurally cannot do (the reason this is a separate engine, sp-mtvg).

import (
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// longHaulCandidate is one cross-system directed lane the engine prices: buy Good at the
// source (paying Lane.SourceAsk), sell at the sink (receiving Lane.DestBid), GateHops gate
// crossings apart, with IntraSeconds of within-system travel and FuelCreditsOverRoute of
// fuel spent over the real multi-hop route. The Lane value object is reused verbatim from
// domain/trading (nothing there forces same-system, so it carries a cross-system pair fine).
type longHaulCandidate struct {
	Lane                 trading.ArbitrageLane
	GateHops             int
	IntraSeconds         float64
	FuelCreditsOverRoute int
}

// pricedLongHaulLane adds the realized both-side price-impact economics to a candidate: the
// impact-optimal tranche, the realized net at that tranche (net of route fuel), the honest
// trip time over the real hop count, and the realized credits/hour ranking key.
type pricedLongHaulLane struct {
	longHaulCandidate
	OptimalUnits           int
	RealizedNetCredits     int
	TripSeconds            float64
	RealizedCreditsPerHour float64
}

// tripSeconds is the honest wall-clock of one directed long-haul leg: the REAL gate-graph
// hops, each at the shared per-hop jump+cooldown constant (crossSystemHopSeconds, reused
// from the circuit ranker), plus the within-system travel — the design's
// trip_time = hops·(jump+cooldown) + intra-system legs. Real hops, not the fixed 2 the
// 1-hop-horizon ranker assumes for every crossing.
func (c longHaulCandidate) tripSeconds() float64 {
	return float64(c.GateHops)*crossSystemHopSeconds + c.IntraSeconds
}

// price computes a candidate's realized economics under the impact model at a per-unit
// marginal floor: the impact-capped optimal tranche, the realized net (tranche-average
// effective spread × units, minus route fuel), and realized credits/hour over the honest
// trip time. A zero-unit or non-positive-net lane reports a zero rate (it is filtered out
// by rankLongHaulLanes, never ranked).
func (m laneImpactModel) price(c longHaulCandidate, marginalFloorCredits float64) pricedLongHaulLane {
	q := m.optimalVolume(c.Lane, marginalFloorCredits)
	realizedSpread := m.effectiveSpreadPerUnit(c.Lane, q) // tranche-average, BOTH-side impact
	net := realizedSpread*float64(q) - float64(c.FuelCreditsOverRoute)
	trip := c.tripSeconds()
	perHour := 0.0
	if trip > 0 && q > 0 && net > 0 {
		perHour = net / (trip / 3600)
	}
	return pricedLongHaulLane{
		longHaulCandidate:      c,
		OptimalUnits:           q,
		RealizedNetCredits:     int(net),
		TripSeconds:            trip,
		RealizedCreditsPerHour: perHour,
	}
}

// rankLongHaulLanes prices every candidate under the shared impact model and returns the
// profitable ones ordered by REALIZED credits/hour, descending — the design's "ranked by
// realized $/hr". A candidate is dropped when its snapshot spread misses minSpreadFloor (a
// coarse pre-filter), when the impact-optimal tranche is zero, or when the realized net
// (after route fuel) is non-positive. Ties break by realized net desc then good asc, so a
// thin, huge-per-unit-spread lane a naive per-unit ranker would top ranks BELOW a solid,
// deep lane it can actually fill.
func rankLongHaulLanes(model laneImpactModel, candidates []longHaulCandidate, minSpreadFloor int, marginalFloorCredits float64) []pricedLongHaulLane {
	priced := make([]pricedLongHaulLane, 0, len(candidates))
	for _, c := range candidates {
		if c.Lane.SpreadPerUnit < minSpreadFloor {
			continue
		}
		p := model.price(c, marginalFloorCredits)
		if p.OptimalUnits <= 0 || p.RealizedNetCredits <= 0 {
			continue
		}
		priced = append(priced, p)
	}
	sort.SliceStable(priced, func(i, j int) bool {
		if priced[i].RealizedCreditsPerHour != priced[j].RealizedCreditsPerHour {
			return priced[i].RealizedCreditsPerHour > priced[j].RealizedCreditsPerHour
		}
		if priced[i].RealizedNetCredits != priced[j].RealizedNetCredits {
			return priced[i].RealizedNetCredits > priced[j].RealizedNetCredits
		}
		return priced[i].Lane.Good < priced[j].Lane.Good
	})
	return priced
}

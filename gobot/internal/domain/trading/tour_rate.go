package trading

import (
	"sort"
	"time"
)

// tour_rate.go — the realized fleet-tour-rate summary the fleet autosizer's heavy-demand economic
// gate consumes (sp-4ewi). It is the READ-side twin of the tour graduation report's $/hr math
// (adapters/cli/tour_report.go computeTourGateMetrics): net = Σ(sell − buy) over a leg span, $/hr =
// net / wall-clock hours. Pure over the persisted per-leg telemetry (trading.TourLegTelemetry), so
// the autosizer port is a thin repo-read + this call, and every rate/decline case is unit-tested in
// isolation.
//
// Three lenses, each matching what a heavy guard needs:
//   - FleetAvg  = MEAN of per-SHIP realized $/hr — the reference the realized-rate floor is a
//     fraction of (guard G7 RateFloor = heavy_marginal_rate_floor × FleetAvg). Per-ship spans
//     include the idle gaps BETWEEN a hull's tours, so this measures realized DUTY-CYCLE throughput
//     (the 20x-gap the trade fleet exists to close), not peak per-tour spread.
//   - Marginal  = MIN per-ship realized $/hr — the lowest-earning heavy, the proxy for what the NEXT
//     hull would earn once the pool absorbs another lane (guards G6 payback + G7 floor judge this).
//   - Declining = the per-TOUR rate TREND over time: newer tours earning less than older ones is
//     absorption saturating (guard G7 stop-buy). Per-tour (not per-ship) so the trend tracks tour
//     quality over wall-clock, independent of how tours distribute across hulls.
//
// Readable is FALSE (fail-closed, RULINGS #4) whenever no ship has a computable realized rate — no
// telemetry, all-skipped legs, or bought-but-never-sold hulls. A guard that cannot see the economics
// must not spend; a readable zero is reserved for genuinely-earning-zero, which the data here can
// never assert (a computable rate needs a realized sell).

// FleetTourRateResult is the realized fleet-tour-rate summary (sp-4ewi). Readable=false means no
// computable rate existed — the heavy realized-rate/payback guards then fail closed on their own.
type FleetTourRateResult struct {
	// FleetAvg is the mean of per-ship realized $/hr over the window (0 when unreadable).
	FleetAvg float64
	// Marginal is the minimum per-ship realized $/hr — the lowest-earning heavy (0 when unreadable).
	Marginal float64
	// Declining is true when the per-tour realized rate trends down across the window (newer < older).
	Declining bool
	// Readable is true iff at least one ship had a computable realized rate (hours > 0 AND a realized
	// sell). false ⇒ the heavy realized-rate and era-payback guards fail closed.
	Readable bool
}

// legGroup accumulates one ship's or one tour's realized net and time span.
type legGroup struct {
	net      int64
	earliest time.Time // min PlannedAt seen (the span start)
	latest   time.Time // max RealizedAt seen (the span end)
	hasStart bool
	hasSell  bool // at least one realized sell — required for a meaningful (non-all-buy) rate
}

// add folds one leg into the group: sells add revenue, buys subtract cost, and the span widens to
// cover PlannedAt→RealizedAt.
func (g *legGroup) add(r TourLegTelemetry) {
	value := int64(r.RealizedUnits) * int64(r.RealizedUnitPrice)
	if r.IsBuy {
		g.net -= value
	} else {
		g.net += value
		if r.RealizedUnits > 0 {
			g.hasSell = true
		}
	}
	if !g.hasStart || r.PlannedAt.Before(g.earliest) {
		g.earliest = r.PlannedAt
		g.hasStart = true
	}
	if r.RealizedAt.After(g.latest) {
		g.latest = r.RealizedAt
	}
}

// rate returns the group's realized $/hr and whether it is computable (a realized sell over a
// positive wall-clock span). A group with no sell, or a non-positive span, is not computable.
func (g legGroup) rate() (float64, bool) {
	if !g.hasSell {
		return 0, false
	}
	hours := g.latest.Sub(g.earliest).Hours()
	if hours <= 0 {
		return 0, false
	}
	return float64(g.net) / hours, true
}

// ComputeFleetTourRate summarises the realized fleet-tour rate from per-leg telemetry (sp-4ewi). It
// is pure and window-agnostic — the caller passes only the rows inside its read window (the port
// applies `since` at the repository read), and the computation derives its own span from those rows.
func ComputeFleetTourRate(rows []TourLegTelemetry) FleetTourRateResult {
	// Per-ship realized rates → FleetAvg (mean) and Marginal (min).
	byShip := map[string]*legGroup{}
	for _, r := range rows {
		g := byShip[r.ShipSymbol]
		if g == nil {
			g = &legGroup{}
			byShip[r.ShipSymbol] = g
		}
		g.add(r)
	}
	var shipRates []float64
	for _, g := range byShip {
		if rate, ok := g.rate(); ok {
			shipRates = append(shipRates, rate)
		}
	}
	if len(shipRates) == 0 {
		return FleetTourRateResult{Readable: false} // no computable rate → fail closed
	}
	var sum, marginal float64
	for i, rate := range shipRates {
		sum += rate
		if i == 0 || rate < marginal {
			marginal = rate
		}
	}
	return FleetTourRateResult{
		FleetAvg:  sum / float64(len(shipRates)),
		Marginal:  marginal,
		Declining: tourRateDeclining(rows),
		Readable:  true,
	}
}

// tourRatePoint is one completed tour's realized rate stamped with its completion time, the unit of
// the decline trend.
type tourRatePoint struct {
	rate     float64
	complete time.Time
}

// tourRateDeclining reports whether the per-tour realized rate trends DOWN across the window: it
// computes each completed tour's $/hr, orders them by completion time, and compares the mean of the
// newer half against the older half. Fewer than two computable tours cannot establish a trend
// (returns false — never a spurious stop-buy).
func tourRateDeclining(rows []TourLegTelemetry) bool {
	byTour := map[string]*legGroup{}
	for _, r := range rows {
		g := byTour[r.TourID]
		if g == nil {
			g = &legGroup{}
			byTour[r.TourID] = g
		}
		g.add(r)
	}

	var tours []tourRatePoint
	for _, g := range byTour {
		if rate, ok := g.rate(); ok {
			tours = append(tours, tourRatePoint{rate: rate, complete: g.latest})
		}
	}
	if len(tours) < 2 {
		return false
	}
	sort.SliceStable(tours, func(i, j int) bool { return tours[i].complete.Before(tours[j].complete) })

	mid := len(tours) / 2
	return meanRate(tours[mid:]) < meanRate(tours[:mid])
}

// meanRate averages a slice of tour rates (callers guarantee it is non-empty via the split at mid).
func meanRate(tours []tourRatePoint) float64 {
	var sum float64
	for _, t := range tours {
		sum += t.rate
	}
	return sum / float64(len(tours))
}

package trading

import (
	"sort"
	"time"
)

// relocation_hull_rate.go — the per-hull realized rate the opportunity relocator's NPV treats as
// "what this hull earns where it is now" (sp-zvywu Part 2).
//
// TRANSACTION-BASED, AND THAT IS THE POINT (the shared-cycle rate-metric hazard the bead names).
// The tempting cheap metric is a CYCLE rate: bracket the wall clock between two moments the hold
// went empty and divide the credits that moved by the span. It is wrong here in a way that is
// invisible until it relocates the wrong hull: a load-zero boundary is a property of a LANE, not of
// a hull, so several hulls working one cycle share the same brackets and are handed the same rate.
// A genuinely-idle hull inherits a busy neighbour's number and looks fine; a star hull inherits a
// stalled one's and looks relocatable.
//
// This function instead attributes money the way it was actually earned: every input row is one
// REALIZED TRANSACTION carrying its own ShipSymbol, its own TourID, its realized units and unit
// price. Filter to the hull, group by tour, net the matched trades, divide by the tour's own wall
// clock. No boundary is shared with anything, so no attribution can leak between hulls.
//
// It reuses the package's ONE netting rule (matchedTradesOnly) and ONE per-tour rate definition
// (legGroup.rate, incl. MinTourSpan) rather than restating either — the tour_rate.go doctrine is one
// lens, one definition, and two would drift.

// DefaultHullRateSmoothing is the EWMA smoothing factor applied to a hull's completed-tour rates,
// most recent tour weighted heaviest: s_i = a*r_i + (1-a)*s_(i-1).
//
// WHY AN EWMA RATHER THAN THE MEDIAN the rate-floor trigger uses. The two triggers ask opposite
// questions. The rate-floor rescue asks "is this hull CHRONICALLY under-earning?", where a median
// over the window is right because one bad tour must not license a relocation. The relocator asks
// "is this hull's CURRENT ground still the best available?", and there the most recent tour is the
// most informative reading of the ground it is standing on — a ground that has just been drained
// reports its collapse in the latest tour and nowhere else. A median would keep quoting the
// pre-collapse level for half a window.
//
// 0.5 halves the weight of each older tour: the latest tour carries 50% of the reading, the one
// before 25%, and everything from four tours back contributes under 7% combined. That is responsive
// enough to notice a drained ground within a tour or two, and still damped enough that a single
// blowout tour cannot by itself clear an uplift bar. It is a documented constant under the RULINGS
// #5 bound (2026-07-25): the shape of "recent tours matter more" is not something an operator
// retunes from an observation, and the anti-thrash levers that ARE operational are the uplift bar,
// the NPV threshold and the cooldown.
const DefaultHullRateSmoothing = 0.5

// tourRateAt is one completed tour's realized rate together with the moment it finished, so the
// EWMA can fold the tours in the order they actually happened.
type tourRateAt struct {
	rate     float64
	finished time.Time
}

// EwmaHullTourRate returns shipSymbol's realized credits-per-hour as an exponentially weighted
// moving average over its completed tours in rows, oldest to newest, and whether that rate is
// READABLE at all.
//
// smoothing is the EWMA factor; a value outside (0, 1] falls back to DefaultHullRateSmoothing so an
// unwired or nonsensical setting still yields the fitted behaviour rather than a degenerate average
// (0 would freeze the reading on the oldest tour forever).
//
// ok=false — FAIL CLOSED — whenever the hull has no computable tour: no rows, no rows for this
// hull, buys with no matching sell, or every tour spanning less than MinTourSpan. A readable zero is
// never invented. The relocator consumes this as "cannot prove what this hull earns", and a hull
// whose current rate is unknown is never relocated: the NPV's `current_rate x travel_h` opportunity
// charge and its uplift ratio are both undefined without it, and guessing either one biases toward
// moving the hull.
func EwmaHullTourRate(rows []TourLegTelemetry, shipSymbol string, smoothing float64) (float64, bool) {
	if smoothing <= 0 || smoothing > 1 {
		smoothing = DefaultHullRateSmoothing
	}
	tours := completedTourRates(rows, shipSymbol)
	if len(tours) == 0 {
		return 0, false // no computable tour for this hull → unreadable, never a readable 0
	}
	sort.Slice(tours, func(i, j int) bool { return tours[i].finished.Before(tours[j].finished) })
	ewma := tours[0].rate
	for _, tour := range tours[1:] {
		ewma = smoothing*tour.rate + (1-smoothing)*ewma
	}
	return ewma, true
}

// completedTourRates folds rows down to shipSymbol's per-tour realized rates, each stamped with the
// tour's finish time.
//
// FILTER FIRST, THEN MATCH. matchedTradesOnly is built over the hull's OWN rows so a trade is scored
// only when this hull's window holds both of its halves — the same order senseRateFloor uses for the
// per-hull median. Matching across the whole fleet first would let another hull's sell license
// scoring this hull's purchase.
func completedTourRates(rows []TourLegTelemetry, shipSymbol string) []tourRateAt {
	hullRows := make([]TourLegTelemetry, 0, len(rows))
	for _, row := range rows {
		if row.ShipSymbol == shipSymbol {
			hullRows = append(hullRows, row)
		}
	}
	groups := groupLegs(hullRows, byTour, matchedTradesOnly(hullRows))
	tours := make([]tourRateAt, 0, len(groups))
	for _, group := range groups {
		rate, ok := group.rate()
		if !ok {
			continue // no matched sell, or a span under MinTourSpan — not a computable tour
		}
		tours = append(tours, tourRateAt{rate: rate, finished: group.latest})
	}
	return tours
}

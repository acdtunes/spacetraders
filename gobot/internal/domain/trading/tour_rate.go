package trading

import (
	"sort"
	"time"
)

// tour_rate.go — the realized fleet-tour-rate summary the fleet autosizer's heavy-demand economic
// gate consumes. It is the READ-side twin of the tour graduation report's $/hr math
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
// ONLY MATCHED TRADES ARE SCORED (see matchedTradesOnly). The window admits a leg by planned_at, so
// it routinely holds a ship's PURCHASES with none of the revenue they earn — the sale lands after
// the window closes, or the cargo is still in the hold. Netted whole, that scores cost against no
// income and reports a profitable hull as a large negative: measured live at -240,510/hr and
// -165,541/hr on a fleet where nothing was losing money. A trade is therefore scored only when the
// window holds BOTH of its halves; an unmatched leg still widens the span (the hull was working) but
// contributes no money.
//
// Readable is FALSE (fail-closed, RULINGS #4) whenever no ship has a computable realized rate — no
// telemetry, all-skipped legs, or hulls with no completed trade in the window. A guard that cannot
// see the economics must not spend; a readable zero is reserved for genuinely-earning-zero, which
// the data here can never assert (a computable rate needs a matched sell).

// FleetTourRateResult is the realized fleet-tour-rate summary. Readable=false means no
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
	hasSell  bool // at least one COUNTED realized sell — required for a meaningful (non-all-buy) rate
}

// add folds one leg into the group: sells add revenue, buys subtract cost, and the span widens to
// cover PlannedAt→RealizedAt.
//
// countValue decides whether the leg's MONEY moves the net. The span widens either way, and that
// asymmetry is the whole of the matching rule: a leg whose other half lies outside the window
// describes hours the hull genuinely worked, but a price the window cannot pair with anything. See
// matchedTradesOnly.
//
// KEEPING AN UNCOUNTED LEG IN THE SPAN IS THE CONSERVATIVE CHOICE, and it is deliberate. Dropping
// it from the denominator too would divide the same net by fewer hours and report a HIGHER rate —
// the direction RULINGS #4 forbids a measurement to drift on its own — and it would also be untrue:
// the hull was flying out to buy that cargo, so the time is real work even though the money is not
// yet realized.
func (g *legGroup) add(r TourLegTelemetry, countValue bool) {
	if countValue {
		value := int64(r.RealizedUnits) * int64(r.RealizedUnitPrice)
		if r.IsBuy {
			g.net -= value
		} else {
			g.net += value
			if r.RealizedUnits > 0 {
				g.hasSell = true
			}
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

// --- trade matching -----------------------------------------------------------------------------

// tradeKey addresses one TRADE: a good bought and sold inside a single tour. It is the unit a $/hr
// figure can actually be computed over — a buy and the sale that realizes it — and it is finer than
// the tour, because a tour routinely completes some of its goods and carries the rest.
//
// Measured live: tour-run-TORWIND-40-9d349727 earned +48,820 on ADVANCED_CIRCUITRY and +44,320 on
// LAB_INSTRUMENTS (both bought and sold in-window) while MACHINERY and MICROPROCESSORS sat
// bought-but-unsold for -185,360. Netted whole the tour reads as a deep loss; matched, it is
// making money. Only the second describes anything.
type tradeKey struct{ tour, good string }

// legCounts decides whether one leg's money counts toward a group's net.
type legCounts func(TourLegTelemetry) bool

// countEveryLeg is the pre-matching rule: every leg's money counts, whether or not the window holds
// its other half. MedianTourRate still uses it — see that function for why the split exists.
func countEveryLeg(TourLegTelemetry) bool { return true }

// matchedTradesOnly returns a legCounts admitting only legs whose (tour, good) trade has realized
// units on BOTH sides inside these rows.
//
// SYMMETRIC, AND BOTH DIRECTIONS MATTER. A good bought but not yet sold is cost without revenue and
// reads as a fake LOSS — the artifact this exists for. A good sold whose purchase happened before
// the window opened is revenue without cost and reads as a fake WINDFALL. The window can price
// neither, and admitting the second while excluding the first would trade one bias for a worse one.
//
// The window admits legs by planned_at and a tour's legs are planned incrementally as it runs (25 of
// 27 live tours spread their planned_at by more than a minute, one by 3.6 hours), so a tour
// straddling either boundary is the normal case rather than an edge.
//
// REALIZED UNITS, NOT LEG PRESENCE. A leg that was planned and then skipped carries zero realized
// units and moved no cargo; counting it as half a match would let a trade that never happened
// license scoring the half that did.
func matchedTradesOnly(rows []TourLegTelemetry) legCounts {
	type sides struct{ bought, sold bool }
	seen := map[tradeKey]*sides{}
	for _, r := range rows {
		if r.RealizedUnits <= 0 {
			continue
		}
		key := tradeKey{r.TourID, r.Good}
		s := seen[key]
		if s == nil {
			s = &sides{}
			seen[key] = s
		}
		if r.IsBuy {
			s.bought = true
		} else {
			s.sold = true
		}
	}
	return func(r TourLegTelemetry) bool {
		s := seen[tradeKey{r.TourID, r.Good}]
		return s != nil && s.bought && s.sold
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

// groupLegs folds telemetry rows into legGroups keyed by groupKey — ship symbol for
// per-ship rates, tour id for per-tour rates — counting each leg's money only when counts admits it.
func groupLegs(rows []TourLegTelemetry, groupKey func(TourLegTelemetry) string, counts legCounts) map[string]*legGroup {
	groups := map[string]*legGroup{}
	for _, r := range rows {
		key := groupKey(r)
		g := groups[key]
		if g == nil {
			g = &legGroup{}
			groups[key] = g
		}
		g.add(r, counts(r))
	}
	return groups
}

// byShip and byTour are the two grouping keys, named so a call site reads as the lens it is using.
func byShip(r TourLegTelemetry) string { return r.ShipSymbol }
func byTour(r TourLegTelemetry) string { return r.TourID }

// computableRates collects the realized $/hr of every group with a computable rate
// (unordered — callers take mean/min or sort for the median, all order-insensitive).
func computableRates(groups map[string]*legGroup) []float64 {
	rates := make([]float64, 0, len(groups))
	for _, g := range groups {
		if rate, ok := g.rate(); ok {
			rates = append(rates, rate)
		}
	}
	return rates
}

// minDispersionSample is the smallest per-ship sample the marginal-vs-average comparison can say
// anything with.
//
// The autosizer's realized-rate guard tests MIN against a fraction of MEAN. At n=1 those are the
// same number, so the test is `x >= 0.7x` — true for every positive rate, whatever the economics.
// A fleet that fails the test at six hulls PASSES it at one. That is not a threshold, it is the
// arity below which the statistic has no content, which is why it lives here beside the statistic
// rather than beside the 0.7.
//
// It is applied ONLY to a sample this rule's own exclusions shrank — see ComputeFleetTourRate.
const minDispersionSample = 2

// ComputeFleetTourRate summarises the realized fleet-tour rate from per-leg telemetry. It
// is pure and window-agnostic — the caller passes only the rows inside its read window (the port
// applies `since` at the repository read), and the computation derives its own span from those rows.
//
// ONLY MATCHED TRADES ARE SCORED. A ship is measured on the goods it both bought and sold inside the
// window; a purchase whose sale falls outside contributes its hours but not its price. Without that
// the window scores cost with none of the revenue it earns, and reports a profitable hull as a large
// negative — measured live at -240,510/hr and -165,541/hr on a fleet where nothing was losing money.
// A ship with no matched trade at all is EXCLUDED, exactly as the older sell-only rule excluded a
// ship with no sell: an unmeasurable hull is dropped, never imputed and never scored as a readable
// zero (that value stays reserved for genuinely-earning-zero, which this data can never assert).
func ComputeFleetTourRate(rows []TourLegTelemetry) FleetTourRateResult {
	matched := matchedTradesOnly(rows)

	// Per-ship realized rates → FleetAvg (mean) and Marginal (min).
	shipRates := computableRates(groupLegs(rows, byShip, matched))
	if len(shipRates) == 0 {
		return FleetTourRateResult{Readable: false} // no computable rate → fail closed
	}

	// THE BACK DOOR, CLOSED. Every exclusion above removes a ship from the sample, and removing
	// ships can only ever make the guard's dispersion test EASIER — a dropped hull cannot be the
	// minimum, and dropping a fake-high one lowers the mean and with it the floor. Taken to n=1 the
	// test becomes a tautology and stops gating the spend at all.
	//
	// So when this rule's own exclusions are what took the sample below minDispersionSample, the
	// honest answer is that the window cannot be read (the guard then fails closed on its own,
	// RULINGS #4) rather than that everything is fine.
	//
	// GATED ON "THIS RULE CAUSED IT", not on arity alone, and that is the difference between a
	// measurement fix and a policy change. A fleet that genuinely has one measurable hull was
	// already read that way before matching existed, and is left exactly as it was — measured on
	// live history, four of the seven 12h windows in the last five days that had any measurable ship
	// had exactly one, so failing those closed unconditionally would block the autosizer in a common
	// real state. That the n=1 sample is vacuous even then is a real hole, but it is the owner's
	// threshold to move, not this function's.
	if len(shipRates) < minDispersionSample &&
		len(computableRates(groupLegs(rows, byShip, countEveryLeg))) > len(shipRates) {
		return FleetTourRateResult{Readable: false}
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
//
// IT READS THE SAME MATCHED NETS the per-ship rates do, and it must: the artifact lands hardest
// here, because a tour is exactly the thing that carries unsold cargo. Measured on the live 12h
// window (12 computable tours), the trend halves move from older 62,938 / newer -15,350 to older
// 169,666 / newer 91,461 once unmatched goods stop being scored — the newer half stops reading as a
// fleet losing money per hour. This signal drives a STOP-BUY, so a tour that is quietly profitable
// must not be read as a collapsing one.
func tourRateDeclining(rows []TourLegTelemetry) bool {
	byTourID := groupLegs(rows, byTour, matchedTradesOnly(rows))

	var tours []tourRatePoint
	for _, g := range byTourID {
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

// MedianTourRate is the fleet rolling-MEDIAN realized tour $/hr over the rows the caller passes
// (the placement engine's β): it reuses the same per-TourID legGroup fold as
// tourRateDeclining — group by TourID, .add() each row, keep the tours whose .rate() is computable
// (a realized sell over a positive wall-clock span) — then returns the MEDIAN of those per-tour
// rates (an even count averages the two middle values). The median, not the mean, is deliberate:
// on a small fleet (2-3 live tours) a single blowout tour must not drag β, and a single dead one
// must not crater it. ok=false when NO tour is computable (empty rows, buys-only, or zero-span),
// mirroring FleetTourRateResult.Readable's fail-closed contract — a readable zero is never
// invented, because the placement caller falls back to the legacy static-floor engine when β is
// unreadable rather than deciding off a fabricated rate. The window is applied by the caller at
// the repository read (ListByPlayer's since bound); this function is pure over the rows it sees.
//
// ON MATCHED NETS, the same rule ComputeFleetTourRate uses. It was deliberately left on
// countEveryLeg while its consumers were unassessed; they have since been measured and the
// assessment inverted the call.
//
// WHAT THE CONSUMERS ACTUALLY DO. β feeds two paths, and NEITHER of them accepts or refuses a tour:
//
//   - the placement engine (senseBeta) scores relocation candidates as E_x − β·D_x, and on a
//     non-positive β falls back to the legacy static-floor reposition;
//   - the rate-floor relocation trigger (senseRateFloor) flags a hull earning below
//     rate_floor_pct% of β as an under-earner and MAY relocate it — behind five further gates.
//
// Both fail CLOSED on a non-positive β, so an unmatched-net β never licences a bad tour: it SILENCES
// a mechanism. Correcting it is a noise reduction, not a loosening.
//
// MEASURED ON β's OWN 60-MINUTE WINDOW, which is a different sample from the autosizer's 12-hour one
// and behaves quite differently. Across 37 rolling windows in 24h, β is readable-and-positive in 32
// of 37 under BOTH rules — it is NOT persistently negative, and neither consumer is dormant.
// Correcting it moves β DOWN slightly in aggregate (median 338,883 → 248,158: higher in 9 windows,
// unchanged in 12, lower in 12), because on a one-hour window the dominant artifact is the MIRROR
// one — cargo sold whose purchase fell in an earlier window, i.e. revenue with no cost. The
// practical effect is fewer spurious under-earner flags: 21 of 77 hull-evaluations before, 13 of 67
// after.
//
// One netting rule, one definition of "the realized tour rate". Two would drift.
func MedianTourRate(rows []TourLegTelemetry) (float64, bool) {
	rates := computableRates(groupLegs(rows, byTour, matchedTradesOnly(rows)))
	if len(rates) == 0 {
		return 0, false // no computable tour → fail closed (never a readable 0)
	}
	sort.Float64s(rates)
	mid := len(rates) / 2
	if len(rates)%2 == 1 {
		return rates[mid], true
	}
	return (rates[mid-1] + rates[mid]) / 2, true
}

package trading

import (
	"sort"
	"time"
)

// tour_rate.go — the realized per-TOUR rate the tour coordinator steers on. It is the READ-side
// twin of the tour graduation report's $/hr math (adapters/cli/tour_report.go
// computeTourGateMetrics): net = Σ(sell − buy) over a leg span, $/hr = net / wall-clock hours. Pure
// over the persisted per-leg telemetry (trading.TourLegTelemetry), so a caller is a thin repo-read +
// this call, and every rate case is unit-tested in isolation.
//
// ONE LENS, ONE DEFINITION. MedianTourRate is the fleet rolling-MEDIAN realized tour $/hr, and it
// feeds two consumers — NEITHER of which accepts or refuses a tour:
//
//   - the placement engine's β (run_tour_coordinator_placement.senseBeta) scores relocation
//     candidates as E_x − β·D_x, and on a non-positive β falls back to the legacy static-floor
//     reposition;
//   - the rate-floor relocation trigger (run_tour_coordinator_rate_floor.senseRateFloor) flags a
//     hull earning below rate_floor_pct% of β as an under-earner and MAY relocate it — behind five
//     further gates.
//
// It is per-TOUR rather than per-ship so the figure tracks tour quality over wall clock,
// independent of how tours distribute across hulls. It is a MEDIAN rather than a mean because on a
// small fleet (2-3 live tours) a single blowout tour must not drag β and a single dead one must not
// crater it.
//
// ONLY MATCHED TRADES ARE SCORED (see matchedTradesOnly). The window admits a leg by planned_at, so
// it routinely holds PURCHASES with none of the revenue they earn — the sale lands after the window
// closes, or the cargo is still in the hold. Netted whole, that scores cost against no income and
// reports a profitable tour as a large negative: live tour tour-run-TORWIND-40-9d349727 read as a
// deep loss while in fact earning +48,820 on ADVANCED_CIRCUITRY and +44,320 on LAB_INSTRUMENTS, the
// two goods it completed. A trade is therefore scored only when the window holds BOTH of its halves;
// an unmatched leg still widens the span (the hull was working) but contributes no money.
//
// A rate is UNREADABLE (ok=false, fail-closed, RULINGS #4) whenever no tour has a computable rate —
// no telemetry, all-skipped legs, no completed trade in the window, or a span too short to divide by
// (see MinTourSpan). A consumer that cannot see the economics must not act on it; a readable zero is
// reserved for genuinely-earning-zero, which the data here can never assert (a computable rate needs
// a matched sell).

// legGroup accumulates one tour's realized net and time span.
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

// MinTourSpan is the shortest wall clock over which a realized $/hr means anything.
// A group spanning less than this has NO computable rate — it reads UNREADABLE, never
// a number.
//
// WHY A FLOOR AT ALL. The rate is net ÷ span, so the span is a DIVISOR and a
// near-zero one does not measure productivity, it amplifies whatever the timestamps
// happened to be. Live, a 0.56-second single-leg tour netting 29,772 computed to
// 189,883,886/hr, and the worst reading on the fleet was 836,660,303/hr. The
// pre-existing `hours <= 0` test caught only an exactly-zero or inverted span, which
// is the one case that could not arise from a real tour.
//
// BOTH DIRECTIONS ARE REAL, which is why this is a correctness fix and not a
// cosmetic one. MedianTourRate survives an outlier because a median is robust — but
// the PER-HULL rate behind the under-earner relocation trigger is a median over ONE
// hull's tours, and when that hull has a single tour in the window the outlier IS
// its rate. An astronomical reading makes a hull permanently un-relocatable; the
// mirror case, a near-zero span on a LOSS, makes it permanently relocatable. Four of
// the nine hulls carrying telemetry today have exactly one computable tour.
//
// WHY SIXTY SECONDS. Two independent arguments landing on the same number:
//
//   - AMPLIFICATION. A fixed timestamp discrepancy ε contributes a relative rate
//     error of ε/span. Taking ε ≈ 1s as a conservative bound on write-path skew and
//     rounding, a 60s span holds that under 2%, where a 1s span puts it at 100%.
//   - THE MEASURED DISTRIBUTION. Across the 31 computable tours on the live fleet,
//     floors of 10s, 30s and 60s all discard exactly ONE tour — the 0.56s artefact —
//     and cost NO hull its rate. 120s discards five, cutting real tours at 74.8s,
//     78.2s, 94.6s and 97.0s. Sixty is therefore the TOP of the safe plateau:
//     maximal protection at zero cost to anything a hull actually earned, with the
//     nearest legitimate tour a comfortable 25% beyond it.
//
// UNREADABLE IS STRICTLY SAFER THAN GUESSED, and both consumers are already built
// for it: the relocation trigger fails closed on an uncomputable hull rate (no proof
// of under-earning, so the hull stays) and the placement engine falls back to the
// legacy static-floor reposition on an unreadable β rather than scoring against a
// fabricated one. Dropping a group therefore only ever makes a consumer STRICTER,
// which is the one direction RULINGS #4 permits.
const MinTourSpan = 60 * time.Second

// rate returns the group's realized $/hr and whether it is computable: a realized
// sell over a wall-clock span of at least MinTourSpan. A group with no sell, or too
// short a span to divide by, is not computable — and reports the zero VALUE with
// ok=false rather than a readable zero, which callers distinguish.
func (g legGroup) rate() (float64, bool) {
	if !g.hasSell {
		return 0, false
	}
	span := g.latest.Sub(g.earliest)
	if span < MinTourSpan {
		return 0, false
	}
	return float64(g.net) / span.Hours(), true
}

// groupLegs folds telemetry rows into legGroups keyed by groupKey — tour id, for the per-tour rates
// this package computes — counting each leg's money only when counts admits it. The key is a
// parameter rather than a constant because the fold itself is lens-agnostic.
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

// byTour is the grouping key, named so a call site reads as the lens it is using.
func byTour(r TourLegTelemetry) string { return r.TourID }

// computableRates collects the realized $/hr of every group with a computable rate
// (unordered — the caller sorts for the median, which is order-insensitive).
func computableRates(groups map[string]*legGroup) []float64 {
	rates := make([]float64, 0, len(groups))
	for _, g := range groups {
		if rate, ok := g.rate(); ok {
			rates = append(rates, rate)
		}
	}
	return rates
}

// MedianTourRate is the fleet rolling-MEDIAN realized tour $/hr over the rows the caller passes
// (the placement engine's β): it folds the rows into per-TourID legGroups — group by TourID, .add()
// each row, keep the tours whose .rate() is computable (a realized sell over a wall-clock span of at
// least MinTourSpan) — then returns the MEDIAN of those per-tour rates (an even count averages the
// two middle values). The median, not the mean, is deliberate: on a small fleet (2-3 live tours) a
// single blowout tour must not drag β, and a single dead one must not crater it. A tour spanning
// less than MinTourSpan is dropped from the sample entirely, so one near-zero-span reading can
// neither BE the median nor drag it. ok=false when NO tour is computable (empty rows, buys-only, or
// every span under MinTourSpan) — a readable zero is never invented, because the placement caller
// falls back to the legacy static-floor engine when β is unreadable rather than deciding off a
// fabricated rate. The window is applied by the caller at the repository read (ListByPlayer's since
// bound); this function is pure over the rows it sees.
//
// ON MATCHED NETS. It uses the package's one netting rule, matchedTradesOnly: a trade scores only
// when the window holds BOTH of its halves, and an unmatched leg widens the span but adds no money.
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
// MEASURED ON β's OWN 60-MINUTE WINDOW, which is the sample the consumers actually read.
// Across 37 rolling windows in 24h, β is readable-and-positive in 32
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

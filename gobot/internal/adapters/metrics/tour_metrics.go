package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// tourDurationBuckets bounds the tour_duration_seconds histogram. A tour_run
// container spans a fail-open no-op (sub-second: model artifact unreadable / first-tour
// infeasible) through a one-shot tour (~minutes) to a continuous engine that rotates
// grounds for hours before margins finally die — so the buckets run 5s → 12h with the
// densest resolution in the minutes range where real tours live. Seconds, matching the
// _seconds suffix convention.
var tourDurationBuckets = []float64{5, 15, 30, 60, 120, 300, 600, 1200, 1800, 3600, 7200, 14400, 28800, 43200}

// tourPlanRateBuckets bounds the tour_plan_rate histogram: credits/hour, from
// break-even (a realized loss lands in the le=0 bucket) through the manual-lane class
// (~390k/hr, the 42-min-rifles evidence) up past the fleet-level 1.6-3.2M/hr band, densest
// in the 100-800k range where single-hull plan rates live.
var tourPlanRateBuckets = []float64{0, 25000, 50000, 100000, 150000, 200000, 300000, 400000, 600000, 800000, 1200000, 1600000, 2400000, 3200000}

// TourMetricsCollector holds the six tour/trading emission counters+histogram+gauge
// this instrumentation sweep adds (bopj P3-P5 + nj2b P11-P13). Like the absorption
// burn-in collector they are EVENT-EMITTED from the tour coordinator via the
// package-level Record*/Observe*/Set* globals (no polling goroutine), and they are pure
// OBSERVATION (RULINGS #4): every method is nil-safe and best-effort, so a recording miss
// can never touch a decision path or block a trade.
type TourMetricsCollector struct {
	// repositionsTotal increments once per margins-death reposition EVALUATION,
	// by outcome: success (the hull jumped to a fresh ground and re-planned), no_candidate
	// (no jump-reachable system cleared the reposition floor — the map-wide margin
	// exhaustion signal bopj P3 wants), or failed (the jump/ship-load errored and the run
	// exits resumable). The kill-switch and the one-per-episode guard are NOT counted —
	// they are not evaluations, and counting them would pollute the no_candidate rate.
	repositionsTotal *prometheus.CounterVec

	// placementDecisionsTotal increments once per armed placement/relocation decision,
	// by verdict: jump (scored a foreign argmax and repositioned), stay (the current-system E_s
	// won), hold_park_floor (nothing cleared φ·β), or fallback_legacy (β unreadable → the legacy
	// engine ran this episode). Only the ARMED path (placement_score_enabled) emits; the legacy
	// reposition keeps tour_repositions_total, so the two engines' telemetry never conflate. The
	// series materialize only on first increment ⇒ /metrics is unchanged until a captain arms.
	placementDecisionsTotal *prometheus.CounterVec

	// marginsDeathTotal increments once per confirmed 3-strike tap-out (tourStarvationLimit),
	// whether or not a reposition then rescues the run — so it
	// measures the ground rich->tapped cadence (bopj P4's 3-strike calibration), distinct
	// from tour_exit_total{reason=starvation} which counts only the final honest exit.
	marginsDeathTotal *prometheus.CounterVec

	// reserveFloorEngagementsTotal increments when the buy-time working-capital floor
	// (RULINGS #4) binds a tranche: action=skip (even one unit pierces the
	// floor, the buy is dropped) or action=shrink (the buy is cut to the units the reserve
	// can still afford). Frequent shrink means the 25%-of-treasury caps outrun liquidity
	// (bopj P5's working-capital sizing decision).
	reserveFloorEngagementsTotal *prometheus.CounterVec

	// exitTotal increments once at each tour_run terminal completion, labeled by the REAL
	// exit-reason enum (iterations_exhausted|starvation|tour_unavailable) — the labeled
	// counter nj2b P11 wants in place of text-parsing containers.exit_reason. Only honest
	// completions are counted; a resumable exit (shutdown/treasury-pause/travel error) is
	// re-adopted, not terminal, and emits nothing.
	exitTotal *prometheus.CounterVec

	// durationSeconds observes the wall-time a tour_run container ran before an honest
	// completion (nj2b P12). Scoped to tour_run by virtue of being emitted only here — the
	// existing container histogram is keyed by container_type=TRADING, which blends
	// tour/arb/route/stocker and nj2b ruled unsafe for a duration histogram.
	durationSeconds *prometheus.HistogramVec

	// resolvedMaxSpend records the dynamic per-tour spend cap each time defaultMaxSpend
	// resolves it (25% of live treasury) — the exact value nj2b P13's Guards
	// panel proxies with a treasury x 0.25 line. A gauge (last-write-wins per player):
	// concurrent hulls resolve ~the same 25%-of-treasury figure, so the series tracks the
	// current cap. Not set on the explicit --max-spend constant path (nothing dynamic to
	// track there).
	resolvedMaxSpend *prometheus.GaugeVec

	// jumpLoadedTotal increments once per COMMITTED margins-death reposition jump,
	// labeled loaded=true when the jump carried a look-back manifest
	// (departure-system exports bought for the destination's imports) and loaded=false
	// when it flew empty (no cross-system lane cleared the money floors). The empty-rate
	// (loaded=false / total) is the deadhead metric the look-back-loading acceptance bar
	// reads (HU21->UQ16 <30% empty). Counted only after the jump commits (a resumable
	// travel failure counts nothing), so it measures real crossings.
	jumpLoadedTotal *prometheus.CounterVec

	// planRate observes each tour plan's credits-per-hour twice: once at
	// plan-accept with the solver's PROJECTED cph (phase=projected), once at the tour's
	// honest completion with the REALIZED cash profit over actual wall-clock
	// (phase=realized). The projected/realized pair is what makes ranking quality
	// MEASURABLE: a systematic projected≫realized gap means the time or price estimator
	// is flattering plans, and any future long-haul lane is judged on this
	// same yardstick. Samples pair 1:1 per flown tour (the initial accepted plan only —
	// intra-tour replans are recovery, not selection).
	planRate *prometheus.HistogramVec

	// factoryGoodAcquisitionCost records the per-unit price a tour paid to ACQUIRE a
	// factory good, labeled by good and source=stock|market (C1). It is the
	// T2 acceptance series for planner-visible stock: as tours withdraw factory output
	// from warehouse stock at cost basis (source=stock) instead of buying our own
	// output at laddered market asks (source=market), the acquisition cost must track
	// the RESTED ask series, not the ladder. A gauge (last-write-wins per good+source):
	// the analyst reads the level and its stock/market split, not a distribution.
	factoryGoodAcquisitionCost *prometheus.GaugeVec

	// legPriceDriftOverPlanPct / legPriceDriftUnderPlanPct / legPriceDriftLegs carry each
	// realized tour leg's unit-price drift from plan — (realized-planned)/planned*100 —
	// DECOMPOSED into two one-way totals plus a leg count, all keyed by side (buy|sell)
	// and basis (solver|lookback).
	//
	// WHY THREE COUNTERS AND NOT ONE SUMMARY (sp-fpgl2). This was a SummaryVec with no
	// objectives, exporting _sum/_count so the Plan-vs-Realized panel could read
	// rate(_sum[w])/rate(_count[w]) as the windowed average — "exactly the SQL AVG it
	// replaces". It was not. Drift is SIGNED, so _sum FELL on every under-plan leg
	// (29.9% of buy legs and 24.6% of sell legs in production), and Prometheus reads any
	// decrease in a counter-typed series as a process restart: rate() adds the full
	// pre-reset value back. The panel therefore reported the true average PLUS roughly
	// the accumulated _sum at each false reset, and since that accumulation grows with
	// time since process start, it RAMPED. Replaying the real 06:30-12:20Z buy sequence
	// through rate()'s reset correction reproduced 3.4% climbing to 132.5% against a true
	// mean of 0.16-0.93%. The telemetry table read 0.60% unweighted / 0.48%
	// value-weighted over the identical legs and was right all along.
	//
	// Splitting the signed sum by direction makes every series a genuine monotone
	// counter, which is the only shape rate() is defined on. The windowed average is
	// recovered exactly:
	//
	//	(sum by (side) (rate(over_plan[w])) - sum by (side) (rate(under_plan[w])))
	//	  / sum by (side) (rate(legs[w]))
	//
	// so the panel keeps the same meaning it always claimed, now truthfully. under_plan
	// accumulates the ABSOLUTE value of negative drift — a CounterVec.Add panics on a
	// negative delta, and that panic is precisely the constraint the Summary was chosen
	// to dodge; carrying the magnitude in its own counter respects it instead.
	//
	// NOTE FOR ANYONE READING OLDER FIGURES: the former
	// tour_leg_price_drift_percent{_sum,_count} series are GONE, and every drift number
	// quoted from that panel before sp-fpgl2 (including the "~97-125% buy drift" in the
	// bead) is an artifact of the defect above, not a measurement. The table figures from
	// the same windows are the honest ones.
	//
	// Deliberately UNLABELED by player_id: the panel is a global cross-player average, so
	// a player_id split would fan the two intended buy/sell lines into
	// one-line-per-player. basis IS labeled — mixing the solver's ExpectedUnitPrice with
	// the look-back manifest's cached SourceAsk is what made the panel uninterpretable —
	// and the panel aggregates it away with sum by (side) so the default view still shows
	// exactly two lines while an analyst can split on basis when they want it.
	//
	// A non-positive planned basis is skipped on all three (nothing to divide by —
	// mirrors the SQL NULLIF(planned,0)), which is also why distress liquidations, whose
	// basis is deliberately left at zero, appear in none of them.
	legPriceDriftOverPlanPct  *prometheus.CounterVec
	legPriceDriftUnderPlanPct *prometheus.CounterVec
	legPriceDriftLegs         *prometheus.CounterVec
}

// The basis label vocabulary for the leg price-drift series. Defined here, beside the Help
// text that documents them, so the label values have ONE source of truth rather than string
// literals scattered across the emitter and its tests — a typo in a label value does not fail
// a build, it silently splits a series into two.
//
// PlanBasisSolver: the expectation came from the tour planner's own projection, so the drift
// measures the market model. PlanBasisLookback: it came from the look-back manifest's cached
// SourceAsk, and the buy is gated to a tolerance band around that number, so a fresh cache
// largely reproduces itself — informative about staleness, but NOT evidence about the model.
//
// PlanBasisLiquidation: there was no plan at all — a distress dump or exit sweep records a
// zero basis rather than inventing one. It is passed for completeness and honesty (the label
// matches the row's engine column, sp-fzt09) but NEVER materialises a series: a non-positive
// basis returns before any counter is touched, so no liquidation observation survives to be
// labelled. It exists so the emitter cannot quietly file a non-solver leg under solver.
//
// The values match trading.LegEngine's, which is what the telemetry row stores; the basis
// label and the engine column are one fact rendered twice, and legPlanBasis maps between them.
const (
	PlanBasisSolver      = "solver"
	PlanBasisLookback    = "lookback"
	PlanBasisLiquidation = "liquidation"
)

// NewTourMetricsCollector creates a new tour metrics collector.
func NewTourMetricsCollector() *TourMetricsCollector {
	return &TourMetricsCollector{
		repositionsTotal: newCounterVec(
			"tour_repositions_total",
			"Margins-death reposition evaluations by outcome (outcome=success|no_candidate|failed)",
			"player_id",
			"outcome",
		),

		placementDecisionsTotal: newCounterVec(
			"tour_placement_decisions_total",
			"Armed placement/relocation decisions by verdict (verdict=jump|stay|hold_park_floor|fallback_legacy)",
			"player_id",
			"verdict",
		),

		marginsDeathTotal: newCounterVec(
			"tour_margins_death_total",
			"Confirmed 3-strike ground tap-outs (margins died this episode), counted whether or not a reposition then rescues the run",
			"player_id",
		),

		reserveFloorEngagementsTotal: newCounterVec(
			"tour_reserve_floor_engagements_total",
			"Buy-time working-capital floor engagements (action=skip|shrink)",
			"player_id",
			"action",
		),

		exitTotal: newCounterVec(
			"tour_exit_total",
			"Tour-run terminal completions by exit reason (reason=iterations_exhausted|starvation|tour_unavailable)",
			"player_id",
			"reason",
		),

		durationSeconds: newHistogramVec(
			"tour_duration_seconds",
			"Wall-time a tour_run container ran before an honest completion (tour_run only, not the blended container_type=TRADING histogram)",
			tourDurationBuckets,
			"player_id",
		),

		resolvedMaxSpend: newGaugeVec(
			"tour_resolved_max_spend",
			"The dynamic per-tour spend cap (25% of live treasury) as most recently resolved by defaultMaxSpend, in credits",
			"player_id",
		),

		jumpLoadedTotal: newCounterVec(
			"tour_jump_loaded_total",
			"Margins-death reposition jumps by whether they carried a look-back manifest (loaded=true|false) — the deadhead empty-rate (sp-ed4i)",
			"player_id",
			"loaded",
		),

		planRate: newHistogramVec(
			"tour_plan_rate",
			"Tour plan credits-per-hour by phase (phase=projected at plan-accept from the solver's cph; phase=realized at completion from cash profit over actual wall-clock) — the sp-1wp8 $/hour yardstick",
			tourPlanRateBuckets,
			"player_id",
			"phase",
		),

		factoryGoodAcquisitionCost: newGaugeVec(
			"tour_factory_good_acquisition_cost",
			"Per-unit price a tour paid to acquire a factory good, by source (source=stock: withdrawn from warehouse at cost basis; source=market: bought at market ask). The C1 (sp-64je) T2 acceptance series — must track the rested ask, not the ladder.",
			"player_id",
			"good_symbol",
			"source",
		),

		legPriceDriftOverPlanPct: newCounterVec(
			"tour_leg_price_drift_over_plan_pct_total",
			"Cumulative percentage points by which realized tour leg unit prices came in ABOVE plan, by side (buy|sell) and basis (solver|lookback). Pairs with _under_plan_pct_total and _legs_total: the windowed average drift is (sum by (side) (rate(over[w])) - sum by (side) (rate(under[w]))) / sum by (side) (rate(legs[w])). Split one-way so every series is a true monotone counter — a signed sum breaks rate() (sp-fpgl2).",
			"side",
			"basis",
		),

		legPriceDriftUnderPlanPct: newCounterVec(
			"tour_leg_price_drift_under_plan_pct_total",
			"Cumulative percentage points by which realized tour leg unit prices came in BELOW plan (absolute magnitude), by side (buy|sell) and basis (solver|lookback). Subtracted from _over_plan_pct_total to recover the signed average — see that metric's help (sp-fpgl2).",
			"side",
			"basis",
		),

		legPriceDriftLegs: newCounterVec(
			"tour_leg_price_drift_legs_total",
			"Realized tour legs that contributed a price-drift observation, by side (buy|sell) and basis (solver|lookback) — the DENOMINATOR of the windowed average drift. Excludes legs with a non-positive planned basis (nothing to divide by), which is why distress liquidations never appear (sp-fpgl2).",
			"side",
			"basis",
		),
	}
}

// Register registers the tour metrics with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *TourMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.repositionsTotal,
		c.placementDecisionsTotal,
		c.marginsDeathTotal,
		c.reserveFloorEngagementsTotal,
		c.exitTotal,
		c.durationSeconds,
		c.resolvedMaxSpend,
		c.jumpLoadedTotal,
		c.planRate,
		c.factoryGoodAcquisitionCost,
		c.legPriceDriftOverPlanPct,
		c.legPriceDriftUnderPlanPct,
		c.legPriceDriftLegs,
	)
}

// RecordReposition records one margins-death reposition evaluation by outcome
// (success|no_candidate|failed).
func (c *TourMetricsCollector) RecordReposition(playerID int, outcome string) {
	if c == nil || c.repositionsTotal == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.repositionsTotal.WithLabelValues(strconv.Itoa(playerID), outcome).Inc()
}

// RecordPlacementDecision records one armed placement/relocation decision by verdict
// (jump|stay|hold_park_floor|fallback_legacy). Nil-safe and best-effort (RULINGS #4).
func (c *TourMetricsCollector) RecordPlacementDecision(playerID int, verdict string) {
	if c == nil || c.placementDecisionsTotal == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.placementDecisionsTotal.WithLabelValues(strconv.Itoa(playerID), verdict).Inc()
}

// RecordMarginsDeath records one confirmed 3-strike ground tap-out.
func (c *TourMetricsCollector) RecordMarginsDeath(playerID int) {
	if c == nil || c.marginsDeathTotal == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.marginsDeathTotal.WithLabelValues(strconv.Itoa(playerID)).Inc()
}

// RecordReserveFloorEngagement records one buy-time working-capital floor engagement
// (action="skip"|"shrink").
func (c *TourMetricsCollector) RecordReserveFloorEngagement(playerID int, action string) {
	if c == nil || c.reserveFloorEngagementsTotal == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.reserveFloorEngagementsTotal.WithLabelValues(strconv.Itoa(playerID), action).Inc()
}

// RecordExit records one tour-run terminal completion by exit reason (a tourExit* enum
// value: iterations_exhausted|starvation|tour_unavailable).
func (c *TourMetricsCollector) RecordExit(playerID int, reason string) {
	if c == nil || c.exitTotal == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.exitTotal.WithLabelValues(strconv.Itoa(playerID), reason).Inc()
}

// ObserveDuration observes one tour-run wall-time (seconds) at honest completion.
func (c *TourMetricsCollector) ObserveDuration(playerID int, seconds float64) {
	if c == nil || c.durationSeconds == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.durationSeconds.WithLabelValues(strconv.Itoa(playerID)).Observe(seconds)
}

// SetResolvedMaxSpend records the dynamic per-tour spend cap (credits) as just resolved by
// defaultMaxSpend. A gauge Set (last-write-wins per player).
func (c *TourMetricsCollector) SetResolvedMaxSpend(playerID int, maxSpend int64) {
	if c == nil || c.resolvedMaxSpend == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.resolvedMaxSpend.WithLabelValues(strconv.Itoa(playerID)).Set(float64(maxSpend))
}

// SetFactoryGoodAcquisitionCost records the per-unit price a tour paid to acquire
// a factory good (source=stock|market) — the C1 T2 acceptance series.
func (c *TourMetricsCollector) SetFactoryGoodAcquisitionCost(playerID int, good, source string, unitPrice float64) {
	if c == nil || c.factoryGoodAcquisitionCost == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.factoryGoodAcquisitionCost.WithLabelValues(strconv.Itoa(playerID), good, source).Set(unitPrice)
}

// RecordJumpLoaded records one committed margins-death reposition jump by whether it
// carried a look-back manifest. loaded=true → the departure-export manifest
// rode the jump; loaded=false → an empty deadhead.
func (c *TourMetricsCollector) RecordJumpLoaded(playerID int, loaded bool) {
	if c == nil || c.jumpLoadedTotal == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.jumpLoadedTotal.WithLabelValues(strconv.Itoa(playerID), strconv.FormatBool(loaded)).Inc()
}

// ObservePlanRate observes one tour plan's credits/hour under
// phase="projected" (plan-accept, the solver's cph) or phase="realized" (tour
// completion, cash profit over actual wall-clock; may be negative — a loss lands in
// the le=0 bucket).
func (c *TourMetricsCollector) ObservePlanRate(playerID int, phase string, creditsPerHour float64) {
	if c == nil || c.planRate == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.planRate.WithLabelValues(strconv.Itoa(playerID), phase).Observe(creditsPerHour)
}

// ObserveLegPriceDrift records one realized tour leg's unit-price drift from its plan,
// keyed by side ("buy"|"sell") and basis ("solver"|"lookback"). Drift is
// (realized-planned)/planned*100, and its SIGN is preserved by routing the magnitude to
// one of two one-way counters: over plan (realized above planned) or under plan (below).
// The leg counter always advances, so the two totals and the count together give the
// exact signed average — see the field docs for the panel expression and for why a single
// signed Summary could not be read with rate() (sp-fpgl2).
//
// A non-positive planned basis is SKIPPED on all three series — there is no basis to
// divide by (mirrors the SQL NULLIF(planned,0)), and a leg counted without a contribution
// would dilute the average toward zero, which is the direction a measurement must never
// drift on its own (RULINGS #4).
//
// Best-effort/nil-safe: a recording miss never panics a trade path (RULINGS #4).
func (c *TourMetricsCollector) ObserveLegPriceDrift(side, basis string, planned, realized float64) {
	if c == nil || c.legPriceDriftOverPlanPct == nil || c.legPriceDriftUnderPlanPct == nil || c.legPriceDriftLegs == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	if planned <= 0 {
		return // No planned basis to divide by — skip (mirrors the SQL NULLIF(planned,0)).
	}
	drift := (realized - planned) / planned * 100

	// The MAGNITUDE goes to the matching direction; the sign lives in the choice of series
	// and never in the value, because Counter.Add panics on a negative delta.
	over, under := 0.0, 0.0
	if drift >= 0 {
		over = drift
	} else {
		under = -drift
	}

	// BOTH directions are touched on every observation, even with a zero delta, so the two
	// series always exist as a PAIR for any (side, basis) that has been seen. The panel
	// SUBTRACTS one from the other, and PromQL binary operators drop any label set missing
	// from either side: were the under-plan series left uncreated, a side whose legs had all
	// come in above plan would produce no series to subtract and the line would vanish from
	// the panel entirely — the metric reading as "no data" precisely when the news is good.
	// Add(0) creates the child without moving the total.
	c.legPriceDriftOverPlanPct.WithLabelValues(side, basis).Add(over)
	c.legPriceDriftUnderPlanPct.WithLabelValues(side, basis).Add(under)
	c.legPriceDriftLegs.WithLabelValues(side, basis).Inc()
}

// globalTourCollector is the singleton tour instrumentation collector.
// Set by SetGlobalTourCollector() when metrics are enabled; the tour coordinator emits
// the reposition/margins-death/reserve-floor/exit/duration/resolved-cap series through it.
var globalTourCollector *TourMetricsCollector

// SetGlobalTourCollector sets the global tour instrumentation collector.
func SetGlobalTourCollector(collector *TourMetricsCollector) {
	globalTourCollector = collector
}

// GetGlobalTourCollector returns the global tour instrumentation collector.
// Returns nil if metrics are not enabled.
func GetGlobalTourCollector() *TourMetricsCollector {
	return globalTourCollector
}

// RecordTourReposition records one margins-death reposition evaluation globally.
// No-op when metrics are disabled, so a metrics miss never touches the
// trade path (RULINGS #4).
func RecordTourReposition(playerID int, outcome string) {
	if globalTourCollector != nil {
		globalTourCollector.RecordReposition(playerID, outcome)
	}
}

// RecordTourPlacementDecision records one armed placement/relocation decision globally by
// verdict (jump|stay|hold_park_floor|fallback_legacy). No-op when metrics are disabled,
// so a metrics miss never touches the trade path (RULINGS #4).
func RecordTourPlacementDecision(playerID int, verdict string) {
	if globalTourCollector != nil {
		globalTourCollector.RecordPlacementDecision(playerID, verdict)
	}
}

// RecordTourMarginsDeath records one confirmed 3-strike ground tap-out globally.
// No-op when metrics are disabled.
func RecordTourMarginsDeath(playerID int) {
	if globalTourCollector != nil {
		globalTourCollector.RecordMarginsDeath(playerID)
	}
}

// RecordTourReserveFloorEngagement records one buy-time working-capital floor engagement
// globally. No-op when metrics are disabled.
func RecordTourReserveFloorEngagement(playerID int, action string) {
	if globalTourCollector != nil {
		globalTourCollector.RecordReserveFloorEngagement(playerID, action)
	}
}

// RecordTourExit records one tour-run terminal completion by exit reason globally.
// No-op when metrics are disabled.
func RecordTourExit(playerID int, reason string) {
	if globalTourCollector != nil {
		globalTourCollector.RecordExit(playerID, reason)
	}
}

// RecordTourJumpLoaded records one committed margins-death reposition jump globally by
// whether it carried a look-back manifest. No-op when metrics are disabled, so
// a metrics miss never touches the trade path (RULINGS #4).
func RecordTourJumpLoaded(playerID int, loaded bool) {
	if globalTourCollector != nil {
		globalTourCollector.RecordJumpLoaded(playerID, loaded)
	}
}

// ObserveTourDuration observes one tour-run wall-time (seconds) globally at honest
// completion. No-op when metrics are disabled.
func ObserveTourDuration(playerID int, seconds float64) {
	if globalTourCollector != nil {
		globalTourCollector.ObserveDuration(playerID, seconds)
	}
}

// SetTourResolvedMaxSpend records the dynamic per-tour spend cap globally as just resolved.
// No-op when metrics are disabled.
func SetTourResolvedMaxSpend(playerID int, maxSpend int64) {
	if globalTourCollector != nil {
		globalTourCollector.SetResolvedMaxSpend(playerID, maxSpend)
	}
}

// SetTourFactoryGoodAcquisitionCost records the per-unit price a tour paid to
// acquire a factory good (source=stock|market) — the C1 T2 acceptance
// series. No-op until the global tour collector is wired.
func SetTourFactoryGoodAcquisitionCost(playerID int, good, source string, unitPrice float64) {
	if globalTourCollector != nil {
		globalTourCollector.SetFactoryGoodAcquisitionCost(playerID, good, source, unitPrice)
	}
}

// ObserveTourPlanRate observes one tour plan's credits/hour globally,
// phase="projected" at plan-accept or phase="realized" at completion. No-op when
// metrics are disabled, so a metrics miss never touches the trade path (RULINGS #4).
func ObserveTourPlanRate(playerID int, phase string, creditsPerHour float64) {
	if globalTourCollector != nil {
		globalTourCollector.ObservePlanRate(playerID, phase, creditsPerHour)
	}
}

// ObserveTourLegPriceDrift records one realized tour leg's unit-price drift from plan
// globally, keyed by side ("buy"|"sell") and basis ("solver"|"lookback") —
// (realized-planned)/planned*100, skipping a non-positive planned basis. No-op when
// metrics are disabled, so a metrics miss never touches the trade path (RULINGS #4).
func ObserveTourLegPriceDrift(side, basis string, planned, realized float64) {
	if globalTourCollector != nil {
		globalTourCollector.ObserveLegPriceDrift(side, basis, planned, realized)
	}
}

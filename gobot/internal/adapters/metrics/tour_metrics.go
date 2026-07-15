package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// tourDurationBuckets bounds the tour_duration_seconds histogram (sp-fbih P12). A tour_run
// container spans a fail-open no-op (sub-second: model artifact unreadable / first-tour
// infeasible) through a one-shot tour (~minutes) to a continuous engine that rotates
// grounds for hours before margins finally die — so the buckets run 5s → 12h with the
// densest resolution in the minutes range where real tours live. Seconds, matching the
// _seconds suffix convention.
var tourDurationBuckets = []float64{5, 15, 30, 60, 120, 300, 600, 1200, 1800, 3600, 7200, 14400, 28800, 43200}

// tourPlanRateBuckets bounds the tour_plan_rate histogram (sp-1wp8): credits/hour, from
// break-even (a realized loss lands in the le=0 bucket) through the manual-lane class
// (~390k/hr, the 42-min-rifles evidence) up past the fleet-level 1.6-3.2M/hr band, densest
// in the 100-800k range where single-hull plan rates live.
var tourPlanRateBuckets = []float64{0, 25000, 50000, 100000, 150000, 200000, 300000, 400000, 600000, 800000, 1200000, 1600000, 2400000, 3200000}

// TourMetricsCollector holds the six tour/trading emission counters+histogram+gauge the
// sp-fbih instrumentation sweep adds (bopj P3-P5 + nj2b P11-P13). Like the absorption
// burn-in collector (sp-8cz9) they are EVENT-EMITTED from the tour coordinator via the
// package-level Record*/Observe*/Set* globals (no polling goroutine), and they are pure
// OBSERVATION (RULINGS #4): every method is nil-safe and best-effort, so a recording miss
// can never touch a decision path or block a trade.
type TourMetricsCollector struct {
	// repositionsTotal increments once per margins-death reposition EVALUATION (sp-zhii),
	// by outcome: success (the hull jumped to a fresh ground and re-planned), no_candidate
	// (no jump-reachable system cleared the reposition floor — the map-wide margin
	// exhaustion signal bopj P3 wants), or failed (the jump/ship-load errored and the run
	// exits resumable). The kill-switch and the one-per-episode guard are NOT counted —
	// they are not evaluations, and counting them would pollute the no_candidate rate.
	repositionsTotal *prometheus.CounterVec

	// marginsDeathTotal increments once per confirmed 3-strike tap-out (sp-m5kv
	// tourStarvationLimit), whether or not a reposition then rescues the run — so it
	// measures the ground rich->tapped cadence (bopj P4's 3-strike calibration), distinct
	// from tour_exit_total{reason=starvation} which counts only the final honest exit.
	marginsDeathTotal *prometheus.CounterVec

	// reserveFloorEngagementsTotal increments when the buy-time working-capital floor
	// (sp-agzj / RULINGS #4) binds a tranche: action=skip (even one unit pierces the
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
	// resolves it (sp-7z7j: 25% of live treasury) — the exact value nj2b P13's Guards
	// panel proxies with a treasury x 0.25 line. A gauge (last-write-wins per player):
	// concurrent hulls resolve ~the same 25%-of-treasury figure, so the series tracks the
	// current cap. Not set on the explicit --max-spend constant path (nothing dynamic to
	// track there).
	resolvedMaxSpend *prometheus.GaugeVec

	// jumpLoadedTotal increments once per COMMITTED margins-death reposition jump
	// (sp-ed4i), labeled loaded=true when the jump carried a look-back manifest
	// (departure-system exports bought for the destination's imports) and loaded=false
	// when it flew empty (no cross-system lane cleared the money floors). The empty-rate
	// (loaded=false / total) is the deadhead metric the look-back-loading acceptance bar
	// reads (HU21->UQ16 <30% empty). Counted only after the jump commits (a resumable
	// travel failure counts nothing), so it measures real crossings.
	jumpLoadedTotal *prometheus.CounterVec

	// planRate observes each tour plan's credits-per-hour twice (sp-1wp8): once at
	// plan-accept with the solver's PROJECTED cph (phase=projected), once at the tour's
	// honest completion with the REALIZED cash profit over actual wall-clock
	// (phase=realized). The projected/realized pair is what makes ranking quality
	// MEASURABLE: a systematic projected≫realized gap means the time or price estimator
	// is flattering plans, and any future long-haul lane (sp-mepj) is judged on this
	// same yardstick. Samples pair 1:1 per flown tour (the initial accepted plan only —
	// intra-tour replans are recovery, not selection).
	planRate *prometheus.HistogramVec

	// factoryGoodAcquisitionCost records the per-unit price a tour paid to ACQUIRE a
	// factory good, labeled by good and source=stock|market (C1, sp-64je). It is the
	// T2 acceptance series for planner-visible stock: as tours withdraw factory output
	// from warehouse stock at cost basis (source=stock) instead of buying our own
	// output at laddered market asks (source=market), the acquisition cost must track
	// the RESTED ask series, not the ladder. A gauge (last-write-wins per good+source):
	// the analyst reads the level and its stock/market split, not a distribution.
	factoryGoodAcquisitionCost *prometheus.GaugeVec

	// legPriceDriftPercent observes each realized tour leg's unit-price drift from the
	// solver's plan — (realized-planned)/planned*100 — keyed by side (buy|sell)
	// (sp-umyb). A SummaryVec with NO objectives: it exports only _sum and _count (no
	// quantile streams), so the Plan-vs-Realized panel reads
	// rate(_sum[$smooth])/rate(_count[$smooth]) = the windowed AVERAGE drift, exactly
	// the SQL AVG it replaces. Deliberately UNLABELED by player_id: the panel is a
	// global cross-player average and the prescribed expr does no aggregation, so a
	// player_id split would fan the two intended buy/sell lines into one-line-per-player.
	// A non-positive planned basis is skipped (nothing to divide by — mirrors the SQL
	// NULLIF(planned,0)); drift is SIGNED, so Summary (not a CounterVec, whose Add
	// panics on the negative sell-side drift) is the correct shape.
	legPriceDriftPercent *prometheus.SummaryVec
}

// NewTourMetricsCollector creates a new tour metrics collector (sp-fbih).
func NewTourMetricsCollector() *TourMetricsCollector {
	return &TourMetricsCollector{
		repositionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_repositions_total",
				Help:      "Margins-death reposition evaluations by outcome (outcome=success|no_candidate|failed)",
			},
			[]string{"player_id", "outcome"},
		),

		marginsDeathTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_margins_death_total",
				Help:      "Confirmed 3-strike ground tap-outs (margins died this episode), counted whether or not a reposition then rescues the run",
			},
			[]string{"player_id"},
		),

		reserveFloorEngagementsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_reserve_floor_engagements_total",
				Help:      "Buy-time working-capital floor engagements (action=skip|shrink)",
			},
			[]string{"player_id", "action"},
		),

		exitTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_exit_total",
				Help:      "Tour-run terminal completions by exit reason (reason=iterations_exhausted|starvation|tour_unavailable)",
			},
			[]string{"player_id", "reason"},
		),

		durationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_duration_seconds",
				Help:      "Wall-time a tour_run container ran before an honest completion (tour_run only, not the blended container_type=TRADING histogram)",
				Buckets:   tourDurationBuckets,
			},
			[]string{"player_id"},
		),

		resolvedMaxSpend: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_resolved_max_spend",
				Help:      "The dynamic per-tour spend cap (25% of live treasury) as most recently resolved by defaultMaxSpend, in credits",
			},
			[]string{"player_id"},
		),

		jumpLoadedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_jump_loaded_total",
				Help:      "Margins-death reposition jumps by whether they carried a look-back manifest (loaded=true|false) — the deadhead empty-rate (sp-ed4i)",
			},
			[]string{"player_id", "loaded"},
		),

		planRate: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_plan_rate",
				Help:      "Tour plan credits-per-hour by phase (phase=projected at plan-accept from the solver's cph; phase=realized at completion from cash profit over actual wall-clock) — the sp-1wp8 $/hour yardstick",
				Buckets:   tourPlanRateBuckets,
			},
			[]string{"player_id", "phase"},
		),

		factoryGoodAcquisitionCost: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_factory_good_acquisition_cost",
				Help:      "Per-unit price a tour paid to acquire a factory good, by source (source=stock: withdrawn from warehouse at cost basis; source=market: bought at market ask). The C1 (sp-64je) T2 acceptance series — must track the rested ask, not the ladder.",
			},
			[]string{"player_id", "good_symbol", "source"},
		),

		legPriceDriftPercent: prometheus.NewSummaryVec(
			prometheus.SummaryOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_leg_price_drift_percent",
				Help:      "Realized-vs-planned tour leg unit-price drift percent by side (side=buy|sell): (realized-planned)/planned*100. No objectives — exports _sum/_count for a windowed-average panel rate(_sum[w])/rate(_count[w]); a non-positive planned basis is skipped (sp-umyb).",
			},
			[]string{"side"},
		),
	}
}

// Register registers the tour metrics with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *TourMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
		c.repositionsTotal,
		c.marginsDeathTotal,
		c.reserveFloorEngagementsTotal,
		c.exitTotal,
		c.durationSeconds,
		c.resolvedMaxSpend,
		c.jumpLoadedTotal,
		c.planRate,
		c.factoryGoodAcquisitionCost,
		c.legPriceDriftPercent,
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
}

// RecordReposition records one margins-death reposition evaluation by outcome
// (success|no_candidate|failed).
func (c *TourMetricsCollector) RecordReposition(playerID int, outcome string) {
	if c == nil || c.repositionsTotal == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.repositionsTotal.WithLabelValues(strconv.Itoa(playerID), outcome).Inc()
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
// a factory good (source=stock|market) — the C1 (sp-64je) T2 acceptance series.
func (c *TourMetricsCollector) SetFactoryGoodAcquisitionCost(playerID int, good, source string, unitPrice float64) {
	if c == nil || c.factoryGoodAcquisitionCost == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.factoryGoodAcquisitionCost.WithLabelValues(strconv.Itoa(playerID), good, source).Set(unitPrice)
}

// RecordJumpLoaded records one committed margins-death reposition jump by whether it
// carried a look-back manifest (sp-ed4i). loaded=true → the departure-export manifest
// rode the jump; loaded=false → an empty deadhead.
func (c *TourMetricsCollector) RecordJumpLoaded(playerID int, loaded bool) {
	if c == nil || c.jumpLoadedTotal == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.jumpLoadedTotal.WithLabelValues(strconv.Itoa(playerID), strconv.FormatBool(loaded)).Inc()
}

// ObservePlanRate observes one tour plan's credits/hour (sp-1wp8) under
// phase="projected" (plan-accept, the solver's cph) or phase="realized" (tour
// completion, cash profit over actual wall-clock; may be negative — a loss lands in
// the le=0 bucket).
func (c *TourMetricsCollector) ObservePlanRate(playerID int, phase string, creditsPerHour float64) {
	if c == nil || c.planRate == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.planRate.WithLabelValues(strconv.Itoa(playerID), phase).Observe(creditsPerHour)
}

// ObserveLegPriceDrift observes one realized tour leg's unit-price drift from the
// solver's plan, keyed by side ("buy"|"sell") (sp-umyb). Drift is
// (realized-planned)/planned*100 (SIGNED: buy over plan is positive, sell under plan
// is negative). A non-positive planned basis is SKIPPED — there is no basis to divide
// by (mirrors the SQL NULLIF(planned,0)). Best-effort/nil-safe: a recording miss never
// panics a trade path (RULINGS #4).
func (c *TourMetricsCollector) ObserveLegPriceDrift(side string, planned, realized float64) {
	if c == nil || c.legPriceDriftPercent == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	if planned <= 0 {
		return // No planned basis to divide by — skip (mirrors the SQL NULLIF(planned,0)).
	}
	drift := (realized - planned) / planned * 100
	c.legPriceDriftPercent.WithLabelValues(side).Observe(drift)
}

package metrics

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// gatherGauge / gatherHistogramCount mirror gatherCounter (absorption_metrics_test.go) for
// the two non-counter tour metrics: they read a single series off the registry via Gather()
// — the same path promhttp.HandlerFor(Registry) serves on /metrics. ok=false means the
// series is absent (never registered, or never observed and therefore never exported).
func gatherGauge(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			if len(got) != len(labels) {
				continue
			}
			match := true
			for k, v := range labels {
				if got[k] != v {
					match = false
					break
				}
			}
			if match {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

// gatherHistogramCount returns the observation COUNT of a histogram series (the _count the
// exposition emits), which is all the value-level assertions here need.
func gatherHistogramCount(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) (uint64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			if len(got) != len(labels) {
				continue
			}
			match := true
			for k, v := range labels {
				if got[k] != v {
					match = false
					break
				}
			}
			if match {
				return m.GetHistogram().GetSampleCount(), true
			}
		}
	}
	return 0, false
}

// TestTourMetrics_LegPriceDrift pins the Plan-vs-Realized drift metric feeding panel 16:
// each realized leg's SIGNED drift (realized-planned)/planned*100 is decomposed into the
// over-plan and under-plan totals plus a leg count, so the panel recovers the windowed
// average as (over-under)/legs; buy and sell are independent; solver and look-back carry a
// distinct basis; and a non-positive planned basis is skipped on all three series (no basis
// to divide by, mirroring the SQL NULLIF(planned,0)).
func TestTourMetrics_LegPriceDrift(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Two buy legs realized ABOVE plan → the over-plan total accumulates 15 points over 2
	// legs, so the panel's (over-under)/legs is their average, +7.5%.
	c.ObserveLegPriceDrift("buy", PlanBasisSolver, 1000, 1100) // (1100-1000)/1000*100 = +10
	c.ObserveLegPriceDrift("buy", PlanBasisSolver, 1000, 1050) // (1050-1000)/1000*100 = +5

	// A sell leg realized BELOW plan → its MAGNITUDE lands in the under-plan total, and the
	// recovered average is negative. The sign survives without any series ever falling.
	c.ObserveLegPriceDrift("sell", PlanBasisSolver, 2000, 1800) // (1800-2000)/2000*100 = -10

	// A non-positive planned basis is skipped on BOTH sides — nothing recorded anywhere,
	// including the leg count, which would otherwise dilute the average toward zero.
	c.ObserveLegPriceDrift("buy", PlanBasisSolver, 0, 500)
	c.ObserveLegPriceDrift("sell", PlanBasisSolver, -50, 500)

	buy := gatherRateInputs(t, Registry, driftFamilyPrefix, map[string]string{"side": "buy"})
	if got := buy[driftFamilyPrefix+"_legs_total"]; got != 2 {
		t.Errorf("side=buy legs = %v, want 2 (two buys; the planned=0 buy is skipped)", got)
	}
	if got := buy[driftFamilyPrefix+"_over_plan_pct_total"]; math.Abs(got-15) > 1e-9 {
		t.Errorf("side=buy over-plan total = %v, want 15 (+10 and +5)", got)
	}
	if got := buy[driftFamilyPrefix+"_under_plan_pct_total"]; got != 0 {
		t.Errorf("side=buy under-plan total = %v, want 0 (neither buy came in below plan)", got)
	}
	if mean, ok := recoverSignedMeanDrift(t, Registry, map[string]string{"side": "buy"}); !ok {
		t.Errorf("side=buy mean drift not recoverable")
	} else if math.Abs(mean-7.5) > 1e-9 {
		t.Errorf("side=buy mean drift = %v, want +7.5 (realized above plan)", mean)
	}

	sell := gatherRateInputs(t, Registry, driftFamilyPrefix, map[string]string{"side": "sell"})
	if got := sell[driftFamilyPrefix+"_legs_total"]; got != 1 {
		t.Errorf("side=sell legs = %v, want 1 (the planned<=0 sell is skipped)", got)
	}
	if got := sell[driftFamilyPrefix+"_under_plan_pct_total"]; math.Abs(got-10) > 1e-9 {
		t.Errorf("side=sell under-plan total = %v, want 10 (the MAGNITUDE of -10%%)", got)
	}
	if mean, ok := recoverSignedMeanDrift(t, Registry, map[string]string{"side": "sell"}); !ok {
		t.Errorf("side=sell mean drift not recoverable")
	} else if math.Abs(mean-(-10)) > 1e-9 {
		t.Errorf("side=sell mean drift = %v, want -10 (realized below plan stays NEGATIVE)", mean)
	}

	// basis separates the two plan bases, which is the whole point of labeling it: a
	// look-back leg's drift measures a cached ask reproducing itself, not the market model.
	c.ObserveLegPriceDrift("buy", PlanBasisLookback, 1000, 1000) // exactly on its cached basis
	lookback := gatherRateInputs(t, Registry, driftFamilyPrefix, map[string]string{"side": "buy", "basis": PlanBasisLookback})
	if got := lookback[driftFamilyPrefix+"_legs_total"]; got != 1 {
		t.Errorf("basis=lookback legs = %v, want 1 — the basis label must not collapse into solver", got)
	}
	solver := gatherRateInputs(t, Registry, driftFamilyPrefix, map[string]string{"side": "buy", "basis": PlanBasisSolver})
	if got := solver[driftFamilyPrefix+"_legs_total"]; got != 2 {
		t.Errorf("basis=solver legs = %v, want 2 — the look-back leg must not be counted as solver", got)
	}

	// The nil-safe contract every sibling emitter keeps (RULINGS #4): a recording miss on
	// a typed-nil receiver or an uninitialized collector degrades to a no-op, never a
	// SIGSEGV that would take down the trade path.
	var nilC *TourMetricsCollector
	nilC.ObserveLegPriceDrift("buy", PlanBasisSolver, 1000, 1100)
	(&TourMetricsCollector{}).ObserveLegPriceDrift("buy", PlanBasisSolver, 1000, 1100)
}

// TestTourMetrics_LegPriceDrift_BothDirectionsExportAsAPair proves a one-directional side
// still exports BOTH direction series.
//
// The panel SUBTRACTS under-plan from over-plan, and PromQL binary operators drop any label
// set absent from either operand. A side whose legs had all come in above plan would, if the
// under-plan child were never created, produce no series to subtract at all — the line would
// disappear from the panel exactly when every leg beat its plan. "No data" is the one thing a
// healthy fleet must not look like.
func TestTourMetrics_LegPriceDrift_BothDirectionsExportAsAPair(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Strictly ONE direction ever fires: every leg lands above plan.
	c.ObserveLegPriceDrift("buy", PlanBasisSolver, 1000, 1100)
	c.ObserveLegPriceDrift("buy", PlanBasisSolver, 1000, 1200)

	series := gatherRateInputs(t, Registry, driftFamilyPrefix, map[string]string{"side": "buy"})
	for _, want := range []string{
		driftFamilyPrefix + "_over_plan_pct_total",
		driftFamilyPrefix + "_under_plan_pct_total",
		driftFamilyPrefix + "_legs_total",
	} {
		if _, ok := series[want]; !ok {
			t.Errorf("%s absent after only-above-plan legs; the panel's subtraction yields NO series and the line vanishes", want)
		}
	}
	if mean, ok := recoverSignedMeanDrift(t, Registry, map[string]string{"side": "buy"}); !ok {
		t.Errorf("mean drift not recoverable when only one direction fired — this is the vanishing-line bug")
	} else if math.Abs(mean-15) > 1e-9 {
		t.Errorf("mean drift = %v, want +15 (+10 and +20)", mean)
	}
}

// driftFamilyPrefix is the metric-family prefix every plan-vs-realized drift series shares.
// The monotonicity invariant below is asserted over the PREFIX rather than an exact family
// name on purpose: the invariant binds whatever shape the drift metric takes, so a rename or
// a split into several families must not silently stop the assertion from finding anything.
const driftFamilyPrefix = "spacetraders_daemon_tour_leg_price_drift"

// gatherRateInputs collects EVERY exported sample that a PromQL rate() could legally be
// applied to, across all metric families whose name starts with prefix and whose labels
// CONTAIN labels, keyed by "<family><suffix>". Counter value, Summary/Histogram _sum and
// _count, and each Histogram bucket's cumulative count all qualify; a Gauge does not (rate()
// is meaningless on one) and is skipped.
//
// SUBSET label matching, not exact-set: the drift series carry a basis label (solver vs
// look-back manifest) that these assertions are not about, and an exact-set match would
// quietly find nothing and pass vacuously.
//
// Samples sharing a key are SUMMED, not overwritten — with basis in play, one family holds
// several series per side, and summing them is exactly the sum by (side) the panel applies.
// Overwriting would silently report whichever basis happened to be gathered last.
func gatherRateInputs(t *testing.T, reg *prometheus.Registry, prefix string, labels map[string]string) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	out := map[string]float64{}
	for _, f := range families {
		if !strings.HasPrefix(f.GetName(), prefix) {
			continue
		}
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			match := true
			for k, v := range labels {
				if got[k] != v {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			base := f.GetName()
			switch {
			case m.GetCounter() != nil:
				out[base] += m.GetCounter().GetValue()
			case m.GetSummary() != nil:
				out[base+"_sum"] += m.GetSummary().GetSampleSum()
				out[base+"_count"] += float64(m.GetSummary().GetSampleCount())
			case m.GetHistogram() != nil:
				out[base+"_sum"] += m.GetHistogram().GetSampleSum()
				out[base+"_count"] += float64(m.GetHistogram().GetSampleCount())
				for _, b := range m.GetHistogram().GetBucket() {
					out[fmt.Sprintf("%s_bucket{le=%v}", base, b.GetUpperBound())] += float64(b.GetCumulativeCount())
				}
			}
		}
	}
	return out
}

// recoverSignedMeanDrift computes the signed mean drift the Plan-vs-Realized panel reads,
// from the exported series, by MIRRORING the panel's PromQL. It is the executable statement of
// that expression, so it MUST be changed in lockstep with both the metric shape and the expr in
// configs/grafana/dashboards/financial.json — if the three ever disagree, the panel is lying
// again and this helper is the thing that catches it.
//
// Panel expr:
//
//	(sum by (side) (rate(_over_plan_pct_total[w])) - sum by (side) (rate(_under_plan_pct_total[w])))
//	  / sum by (side) (rate(_legs_total[w]))
//
// This helper is the instantaneous form of it (the registry holds exactly one window's
// worth), summing across the basis label the same way the panel does. ok=false when there is
// nothing to divide — which is also the answer if either direction's series is missing, since
// PromQL's subtraction would likewise yield no series at all.
func recoverSignedMeanDrift(t *testing.T, reg *prometheus.Registry, labels map[string]string) (float64, bool) {
	t.Helper()
	series := gatherRateInputs(t, reg, driftFamilyPrefix, labels)
	legs, ok := series[driftFamilyPrefix+"_legs_total"]
	if !ok || legs == 0 {
		return 0, false
	}
	over, haveOver := series[driftFamilyPrefix+"_over_plan_pct_total"]
	under, haveUnder := series[driftFamilyPrefix+"_under_plan_pct_total"]
	if !haveOver || !haveUnder {
		return 0, false
	}
	return (over - under) / legs, true
}

// TestTourMetrics_LegPriceDrift_NegativeDriftNeverDecreasesARateInput pins the invariant the
// Plan-vs-Realized panel actually depends on: NO series the panel applies rate() to may ever
// DECREASE, including when a leg realizes BELOW its plan.
//
// This is the sp-fpgl2 defect. Drift is SIGNED, so a Summary's _sum falls on every
// under-plan leg — 29.9% of buy legs and 24.6% of sell legs in production. Prometheus reads
// any decrease in a counter-typed series as a process restart and adds the FULL pre-reset
// value back, so rate(_sum[w])/rate(_count[w]) does not return the windowed average drift; it
// returns that average plus roughly the accumulated _sum at each false reset. Because the
// accumulation grows with time since process start, the panel RAMPS. Replaying the real
// 06:30-12:20Z buy sequence through rate()'s reset correction reproduced 3.4% climbing to
// 132.5% against a true mean of 0.16-0.93%, which is the "0->~125%" the bead reported, while
// the telemetry table read 0.60% unweighted and 0.48% value-weighted over the identical legs.
// Neither number was wrong about its own data: the panel was not measuring drift at all.
//
// Asserted structurally rather than against one shape. A Summary whose _sum can fall, a
// Histogram's _sum, and a plain Counter that is decremented would all fail this; a signed sum
// decomposed into two monotone counters, or a Histogram read only through its (monotone)
// buckets, both pass. That keeps the test binding on the invariant instead of on an
// implementation, so it still guards the panel after any later reshaping.
func TestTourMetrics_LegPriceDrift_NegativeDriftNeverDecreasesARateInput(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	sell := map[string]string{"side": "sell"}

	// A leg ABOVE plan first, so every series is non-zero and a later fall is unambiguous
	// (a series that merely stays at zero would hide the defect).
	c.ObserveLegPriceDrift("sell", PlanBasisSolver, 1000, 1100) // +10%
	before := gatherRateInputs(t, Registry, driftFamilyPrefix, sell)
	if len(before) == 0 {
		t.Fatalf("no rate()-able series exported for side=sell under prefix %q — the invariant would pass vacuously", driftFamilyPrefix)
	}

	// The leg that breaks it: realized BELOW plan, so signed drift is negative.
	c.ObserveLegPriceDrift("sell", PlanBasisSolver, 1000, 900) // -10%
	after := gatherRateInputs(t, Registry, driftFamilyPrefix, sell)

	for name, was := range before {
		now, still := after[name]
		if !still {
			t.Errorf("series %s disappeared after a negative-drift observation", name)
			continue
		}
		if now < was {
			t.Errorf("series %s DECREASED %v -> %v on an under-plan leg. Prometheus rate() reads "+
				"that as a counter reset and adds the whole %v back, which is what ramped the "+
				"Plan-vs-Realized panel to 132%% against a true 0.6%%. Every rate() input must be "+
				"monotonically non-decreasing.", name, was, now, was)
		}
	}

	// A monotone series is worthless if the signed average is no longer recoverable from it,
	// so pin the economics too: two legs at +10% and -10% average to EXACTLY zero drift.
	// Whatever shape carries the sum must still be able to express that the two cancel.
	if mean, ok := recoverSignedMeanDrift(t, Registry, sell); !ok {
		t.Errorf("signed mean drift not recoverable from the exported side=sell series; the panel has nothing to divide")
	} else if math.Abs(mean) > 1e-9 {
		t.Errorf("recovered mean drift = %v, want 0 (+10%% and -10%% cancel)", mean)
	}
}

// TestTourMetrics_LegPriceDrift_NeverPanicsOnAnyRealizedPrice is the RULINGS #4 guard for the
// shape change itself.
//
// The drift series are CounterVecs now, and prometheus Counter.Add PANICS on a negative delta
// ("counter cannot decrease in value") — that panic is the very thing the old SummaryVec was
// chosen to avoid. Trading a non-panicking accumulator for a panicking one on a code path the
// executor calls per leg is only safe if no reachable input can reach Add with a negative
// value, and "I reasoned it cannot" is not the standard for something that would take down a
// trade. So drive the adversarial inputs through and require survival.
//
// The routing is what makes it safe: the magnitude is negated into under-plan precisely when
// drift is negative, so both operands are non-negative by construction, and planned > 0 is
// already enforced upstream so neither NaN nor an infinity can be formed. This test is what
// keeps that true if the routing is ever edited.
func TestTourMetrics_LegPriceDrift_NeverPanicsOnAnyRealizedPrice(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	cases := []struct {
		name              string
		planned, realized float64
	}{
		{"realized zero — the most negative drift possible, -100%", 1000, 0},
		{"realized one credit under plan", 1000, 999},
		{"realized exactly on plan — signed zero must not read as negative", 1000, 1000},
		{"realized far above plan", 1, 1e9},
		{"tiny basis, large realized — huge but finite drift", 1e-9, 1e9},
		{"tiny basis, zero realized", 1e-9, 0},
		{"negative realized price — never expected, must still not panic", 1000, -5000},
		{"both at the smallest positive basis", 1e-300, 1e-300},
	}
	for _, tc := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: ObserveLegPriceDrift PANICKED (%v) — a metrics call must never "+
						"be able to kill a trade leg (RULINGS #4)", tc.name, r)
				}
			}()
			c.ObserveLegPriceDrift("buy", PlanBasisSolver, tc.planned, tc.realized)
			c.ObserveLegPriceDrift("sell", PlanBasisLookback, tc.planned, tc.realized)
		}()
	}

	// Survival is not enough — the series must still be readable afterwards, or a "safe"
	// no-panic path could be one that quietly recorded nothing.
	if _, ok := recoverSignedMeanDrift(t, Registry, map[string]string{"side": "buy"}); !ok {
		t.Errorf("no readable drift series after the adversarial sweep")
	}
}

// TestTourMetrics_RegisterAndExport proves EVERY tour metric family REGISTERS on the daemon's
// registry AND actually appears by name once observed. A registered CounterVec/HistogramVec/
// GaugeVec exports nothing until a label combination is touched — the bopj P10 trap where a
// family was "registered" yet never showed on /metrics. Registration alone is not export.
//
// The list below must stay EXHAUSTIVE. It previously omitted the plan-rate and price-drift
// families, so it passed without ever gathering them — and a panel reading a family no test
// exports is precisely how sp-fpgl2 went unnoticed for as long as it did.
func TestTourMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Observe one combination of each family so the registry has something to gather.
	c.RecordReposition(1, "success")
	c.RecordMarginsDeath(1)
	c.RecordReserveFloorEngagement(1, "shrink")
	c.RecordExit(1, "starvation")
	c.ObserveDuration(1, 420)
	c.SetResolvedMaxSpend(1, 250000)
	c.RecordJumpLoaded(1, true)
	c.SetFactoryGoodAcquisitionCost(1, "CLOTHING", "stock", 100)
	c.ObservePlanRate(1, "projected", 12345)
	c.ObserveLegPriceDrift("buy", PlanBasisSolver, 1000, 1100)

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	for _, want := range []string{
		"spacetraders_daemon_tour_repositions_total",
		"spacetraders_daemon_tour_margins_death_total",
		"spacetraders_daemon_tour_reserve_floor_engagements_total",
		"spacetraders_daemon_tour_exit_total",
		"spacetraders_daemon_tour_duration_seconds",
		"spacetraders_daemon_tour_resolved_max_spend",
		"spacetraders_daemon_tour_jump_loaded_total",
		"spacetraders_daemon_tour_factory_good_acquisition_cost",
		"spacetraders_daemon_tour_plan_rate",
		"spacetraders_daemon_tour_leg_price_drift_over_plan_pct_total",
		"spacetraders_daemon_tour_leg_price_drift_under_plan_pct_total",
		"spacetraders_daemon_tour_leg_price_drift_legs_total",
	} {
		if !got[want] {
			t.Errorf("metric %q registered but not exported on the registry", want)
		}
	}
}

// TestTourMetrics_FactoryGoodAcquisitionCost pins the C1 T2 series: the
// per-good acquisition price splits by source, so the stock (basis) and market
// (ladder) series for one good are distinct and each holds its last-set value —
// exactly what lets the analyst check that acquisition tracks the rested ask, not
// the ladder.
func TestTourMetrics_FactoryGoodAcquisitionCost(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.SetFactoryGoodAcquisitionCost(1, "CLOTHING", "stock", 100)
	c.SetFactoryGoodAcquisitionCost(1, "CLOTHING", "market", 340)

	const name = "spacetraders_daemon_tour_factory_good_acquisition_cost"
	stock, ok := gatherGauge(t, Registry, name, map[string]string{"player_id": "1", "good_symbol": "CLOTHING", "source": "stock"})
	if !ok || stock != 100 {
		t.Fatalf("expected stock acquisition cost 100, got %v (ok=%v)", stock, ok)
	}
	market, ok := gatherGauge(t, Registry, name, map[string]string{"player_id": "1", "good_symbol": "CLOTHING", "source": "market"})
	if !ok || market != 340 {
		t.Fatalf("expected market acquisition cost 340, got %v (ok=%v)", market, ok)
	}

	// A re-record on the stock series is last-write-wins (a gauge), independent of market.
	c.SetFactoryGoodAcquisitionCost(1, "CLOTHING", "stock", 110)
	stock, _ = gatherGauge(t, Registry, name, map[string]string{"player_id": "1", "good_symbol": "CLOTHING", "source": "stock"})
	if stock != 110 {
		t.Fatalf("expected stock series to update to 110, got %v", stock)
	}
}

// A nil collector and nil gauge must never panic (best-effort, RULINGS #4).
func TestTourMetrics_FactoryGoodAcquisitionCost_NilSafe(t *testing.T) {
	var c *TourMetricsCollector
	c.SetFactoryGoodAcquisitionCost(1, "CLOTHING", "stock", 100) // nil receiver
	(&TourMetricsCollector{}).SetFactoryGoodAcquisitionCost(1, "CLOTHING", "stock", 100)
	SetTourFactoryGoodAcquisitionCost(1, "CLOTHING", "stock", 100) // nil global
}

// TestTourMetrics_LabelsAndValues pins the label sets and that repeat records accumulate on
// the right series: reposition keyed by outcome, floor by action, exit by reason; the gauge
// holds the last-set cap; the histogram counts each observation.
func TestTourMetrics_LabelsAndValues(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordReposition(7, "success")
	c.RecordReposition(7, "no_candidate")
	c.RecordReposition(7, "no_candidate")
	c.RecordReposition(7, "failed")
	c.RecordMarginsDeath(7)
	c.RecordMarginsDeath(7)
	c.RecordReserveFloorEngagement(7, "skip")
	c.RecordReserveFloorEngagement(7, "shrink")
	c.RecordReserveFloorEngagement(7, "shrink")
	c.RecordExit(7, "starvation")
	c.RecordExit(7, "iterations_exhausted")
	c.ObserveDuration(7, 120)
	c.ObserveDuration(7, 3600)
	c.SetResolvedMaxSpend(7, 100000)
	c.SetResolvedMaxSpend(7, 250000) // last-write-wins
	c.RecordJumpLoaded(7, true)      // a loaded look-back jump
	c.RecordJumpLoaded(7, false)     // an empty deadhead
	c.RecordJumpLoaded(7, false)     // another empty — both labels accumulate independently

	const (
		repoName  = "spacetraders_daemon_tour_repositions_total"
		deathName = "spacetraders_daemon_tour_margins_death_total"
		floorName = "spacetraders_daemon_tour_reserve_floor_engagements_total"
		exitName  = "spacetraders_daemon_tour_exit_total"
		capName   = "spacetraders_daemon_tour_resolved_max_spend"
		durName   = "spacetraders_daemon_tour_duration_seconds"
		loadName  = "spacetraders_daemon_tour_jump_loaded_total"
	)

	counterCases := []struct {
		name   string
		metric string
		labels map[string]string
		want   float64
	}{
		{"reposition success", repoName, map[string]string{"player_id": "7", "outcome": "success"}, 1},
		{"reposition no_candidate", repoName, map[string]string{"player_id": "7", "outcome": "no_candidate"}, 2},
		{"reposition failed", repoName, map[string]string{"player_id": "7", "outcome": "failed"}, 1},
		{"margins death", deathName, map[string]string{"player_id": "7"}, 2},
		{"floor skip", floorName, map[string]string{"player_id": "7", "action": "skip"}, 1},
		{"floor shrink", floorName, map[string]string{"player_id": "7", "action": "shrink"}, 2},
		{"exit starvation", exitName, map[string]string{"player_id": "7", "reason": "starvation"}, 1},
		{"exit iterations", exitName, map[string]string{"player_id": "7", "reason": "iterations_exhausted"}, 1},
		{"jump loaded true", loadName, map[string]string{"player_id": "7", "loaded": "true"}, 1},
		{"jump loaded false", loadName, map[string]string{"player_id": "7", "loaded": "false"}, 2},
	}
	for _, tc := range counterCases {
		got, ok := gatherCounter(t, Registry, tc.metric, tc.labels)
		if !ok {
			t.Errorf("%s: series %s%v not found", tc.name, tc.metric, tc.labels)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: %s%v = %v, want %v", tc.name, tc.metric, tc.labels, got, tc.want)
		}
	}

	if got, ok := gatherGauge(t, Registry, capName, map[string]string{"player_id": "7"}); !ok {
		t.Errorf("gauge %s{player_id=7} not found", capName)
	} else if got != 250000 {
		t.Errorf("gauge %s{player_id=7} = %v, want 250000 (last-write-wins)", capName, got)
	}

	if got, ok := gatherHistogramCount(t, Registry, durName, map[string]string{"player_id": "7"}); !ok {
		t.Errorf("histogram %s{player_id=7} not found", durName)
	} else if got != 2 {
		t.Errorf("histogram %s{player_id=7} sample count = %v, want 2", durName, got)
	}
}

// TestTourMetrics_NilSafe mirrors the absorption collector's guarantee (RULINGS #4 —
// observation only): a recording miss on a typed-nil receiver or an uninitialized collector
// must degrade to a no-op, never a SIGSEGV that would take down the trade path.
func TestTourMetrics_NilSafe(t *testing.T) {
	var nilC *TourMetricsCollector
	nilC.RecordReposition(1, "success")
	nilC.RecordMarginsDeath(1)
	nilC.RecordReserveFloorEngagement(1, "skip")
	nilC.RecordExit(1, "starvation")
	nilC.ObserveDuration(1, 10)
	nilC.SetResolvedMaxSpend(1, 100)
	nilC.RecordJumpLoaded(1, true)

	empty := &TourMetricsCollector{}
	empty.RecordReposition(1, "failed")
	empty.RecordMarginsDeath(1)
	empty.RecordReserveFloorEngagement(1, "shrink")
	empty.RecordExit(1, "iterations_exhausted")
	empty.ObserveDuration(1, 20)
	empty.SetResolvedMaxSpend(1, 200)
	empty.RecordJumpLoaded(1, false)
}

// TestTourMetrics_PlanRateHistogram_RegistersExportsAndPairsPhases: the
// tour_plan_rate histogram registers, exports once observed, keys projected and realized
// as SEPARATE phase series (the pair is what makes ranking quality measurable), and
// accepts a negative realized rate (a losing tour) without panicking.
func TestTourMetrics_PlanRateHistogram_RegistersExportsAndPairsPhases(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.ObservePlanRate(9, "projected", 390000) // the 42-min-rifles class
	c.ObservePlanRate(9, "projected", 150000)
	c.ObservePlanRate(9, "realized", 212000)
	c.ObservePlanRate(9, "realized", -18000) // a losing tour is still an observation

	projected, ok := gatherHistogramCount(t, Registry, "spacetraders_daemon_tour_plan_rate",
		map[string]string{"player_id": "9", "phase": "projected"})
	if !ok || projected != 2 {
		t.Fatalf("expected 2 projected observations on the phase=projected series, got %d (ok=%v)", projected, ok)
	}
	realized, ok := gatherHistogramCount(t, Registry, "spacetraders_daemon_tour_plan_rate",
		map[string]string{"player_id": "9", "phase": "realized"})
	if !ok || realized != 2 {
		t.Fatalf("expected 2 realized observations on the phase=realized series, got %d (ok=%v)", realized, ok)
	}

	// The nil-safe contract every sibling emitter keeps (RULINGS #4).
	var nilC *TourMetricsCollector
	nilC.ObservePlanRate(9, "projected", 1)
}

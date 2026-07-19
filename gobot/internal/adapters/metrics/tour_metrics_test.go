package metrics

import (
	"math"
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

// gatherSummary reads one SummaryVec series off the registry via Gather() — the same
// path promhttp serves on /metrics — returning the exported _sum and _count (a
// no-objectives summary exports exactly those). ok=false means the series is absent
// (never registered, or never observed and therefore never exported).
func gatherSummary(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) (sampleSum float64, sampleCount uint64, ok bool) {
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
				s := m.GetSummary()
				return s.GetSampleSum(), s.GetSampleCount(), true
			}
		}
	}
	return 0, 0, false
}

// TestTourMetrics_LegPriceDrift pins the Plan-vs-Realized drift metric feeding
// panel 16: each realized leg observes SIGNED drift (realized-planned)/planned*100 under
// its side; the metric exports the _sum/_count pair the panel's
// rate(_sum[$smooth])/rate(_count[$smooth]) windowed-average reads (so two buys sum to
// their combined drift over a count of two = their average); buy and sell are
// independent series; and a non-positive planned basis is skipped (no basis to divide by,
// mirroring the SQL NULLIF(planned,0)).
func TestTourMetrics_LegPriceDrift(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	const name = "spacetraders_daemon_tour_leg_price_drift_percent"

	// Two buy legs realized ABOVE plan → positive drift on side=buy. They accumulate so
	// the _sum/_count pair is the AVERAGE drift the panel divides for: +10% and +5% →
	// sum 15 over count 2 (avg 7.5).
	c.ObserveLegPriceDrift("buy", 1000, 1100) // (1100-1000)/1000*100 = +10
	c.ObserveLegPriceDrift("buy", 1000, 1050) // (1050-1000)/1000*100 = +5

	// A sell leg realized BELOW plan → negative drift on a distinct side=sell series.
	c.ObserveLegPriceDrift("sell", 2000, 1800) // (1800-2000)/2000*100 = -10

	// A non-positive planned basis is skipped on BOTH sides — no observation recorded.
	c.ObserveLegPriceDrift("buy", 0, 500)
	c.ObserveLegPriceDrift("sell", -50, 500)

	buySum, buyCount, ok := gatherSummary(t, Registry, name, map[string]string{"side": "buy"})
	if !ok {
		t.Fatalf("side=buy series not exported")
	}
	if buyCount != 2 {
		t.Errorf("side=buy count = %d, want 2 (two buys; the planned=0 buy is skipped)", buyCount)
	}
	if math.Abs(buySum-15) > 1e-9 {
		t.Errorf("side=buy sum = %v, want 15 (+10 and +5); rate(_sum)/rate(_count) is the avg drift", buySum)
	}
	if buySum <= 0 {
		t.Errorf("side=buy drift sum = %v, want positive (realized above plan)", buySum)
	}

	sellSum, sellCount, ok := gatherSummary(t, Registry, name, map[string]string{"side": "sell"})
	if !ok {
		t.Fatalf("side=sell series not exported")
	}
	if sellCount != 1 {
		t.Errorf("side=sell count = %d, want 1 (the planned<=0 sell is skipped)", sellCount)
	}
	if math.Abs(sellSum-(-10)) > 1e-9 {
		t.Errorf("side=sell sum = %v, want -10 (realized below plan → negative drift)", sellSum)
	}

	// The nil-safe contract every sibling emitter keeps (RULINGS #4): a recording miss on
	// a typed-nil receiver or an uninitialized collector degrades to a no-op, never a
	// SIGSEGV that would take down the trade path.
	var nilC *TourMetricsCollector
	nilC.ObserveLegPriceDrift("buy", 1000, 1100)
	(&TourMetricsCollector{}).ObserveLegPriceDrift("buy", 1000, 1100)
}

// TestTourMetrics_RegisterAndExport proves ALL SIX tour metrics REGISTER on the daemon's
// registry AND actually appear by name once observed. A registered CounterVec/HistogramVec/
// GaugeVec exports nothing until a label combination is touched — the bopj P10 trap where a
// family was "registered" yet never showed on /metrics. Registration alone is not export.
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

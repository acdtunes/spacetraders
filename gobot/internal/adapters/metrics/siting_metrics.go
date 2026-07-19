package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// SitingMetricsCollector houses the factory-SITING coordinator's decision counters.
// The coordinator is the standing "brain" that automates factory discovery, placement, and
// capacity planning; these series make its portfolio decisions observable on /metrics. Three
// counters, one per action the coordinator takes on the running factory portfolio:
//
//   - siting_launches_total{good,system}: a COUNTER incremented once each time the coordinator
//     launches a missing top-K chain through the guard stack (ACT). The dashboard's view of what
//     the brain decided to STAND UP.
//   - siting_retires_total{good,system}: a COUNTER incremented once each time the coordinator
//     retires a chain that fell out of top-K past the hysteresis window (ACT). What it decided to
//     TEAR DOWN.
//   - siting_scout_demands_total{system}: a COUNTER incremented once per scout-demand the
//     coordinator emits for a stale-but-promising desired site (EMIT) — the discovery-loop signal.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch a siting decision, so every
// method is nil-safe and best-effort. The coordinator's launch/retire/emit paths fail
// independently of this collector — an unwired collector silently drops the metric, it never
// affects whether a chain is launched, retired, or a scout-demand emitted. Dry-run "would
// launch/retire" decisions take no real action and are deliberately NOT counted.
type SitingMetricsCollector struct {
	// launchesTotal increments once per chain the coordinator actually launches (not dry-run).
	launchesTotal *prometheus.CounterVec
	// retiresTotal increments once per chain the coordinator actually retires (not dry-run).
	retiresTotal *prometheus.CounterVec
	// scoutDemandsTotal increments once per NEW scout-demand emitted (deduped upstream).
	scoutDemandsTotal *prometheus.CounterVec
}

// NewSitingMetricsCollector creates a new factory-siting metrics collector.
func NewSitingMetricsCollector() *SitingMetricsCollector {
	return &SitingMetricsCollector{
		launchesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "siting_launches_total",
				Help:      "Factory chains the siting coordinator launched into the top-K portfolio through the guard stack, counted once per launch (sp-vdld)",
			},
			[]string{"good", "system"},
		),
		retiresTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "siting_retires_total",
				Help:      "Factory chains the siting coordinator retired after they fell out of top-K past the hysteresis window, counted once per retire (sp-vdld)",
			},
			[]string{"good", "system"},
		),
		scoutDemandsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "siting_scout_demands_total",
				Help:      "Scout-demands the siting coordinator emitted for stale-but-promising desired sites, counted once per emission (sp-vdld)",
			},
			[]string{"system"},
		),
	}
}

// Register registers the siting metrics with the Prometheus registry. A nil Registry (metrics
// disabled) is a no-op, matching the sibling collectors.
func (c *SitingMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}
	if err := Registry.Register(c.launchesTotal); err != nil {
		return err
	}
	if err := Registry.Register(c.retiresTotal); err != nil {
		return err
	}
	return Registry.Register(c.scoutDemandsTotal)
}

// RecordLaunch increments the launch counter for a (good, system) chain. Called by ACT once a
// launch through the guard stack succeeds.
func (c *SitingMetricsCollector) RecordLaunch(good, system string) {
	if c == nil || c.launchesTotal == nil {
		return // Recording is best-effort; never panic the ACT path (RULINGS #4).
	}
	c.launchesTotal.WithLabelValues(good, system).Inc()
}

// RecordRetire increments the retire counter for a (good, system) chain. Called by ACT once a
// clean container stop succeeds.
func (c *SitingMetricsCollector) RecordRetire(good, system string) {
	if c == nil || c.retiresTotal == nil {
		return // Recording is best-effort; never panic the ACT path (RULINGS #4).
	}
	c.retiresTotal.WithLabelValues(good, system).Inc()
}

// RecordScoutDemand increments the scout-demand counter for a system. Called by EMIT once a NEW
// (non-deduped) scout-demand is recorded.
func (c *SitingMetricsCollector) RecordScoutDemand(system string) {
	if c == nil || c.scoutDemandsTotal == nil {
		return // Recording is best-effort; never panic the EMIT path (RULINGS #4).
	}
	c.scoutDemandsTotal.WithLabelValues(system).Inc()
}

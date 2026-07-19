package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// FleetAutosizerMetricsCollector houses the fleet capacity autosizer's observation series.
// The autosizer sizes the hull pool to demand and auto-buys hulls behind the guard
// stack; these series make its buy decisions observable on /metrics:
//
//   - autosizer_purchases_total{class}: a COUNTER incremented once per hull the autosizer actually
//     buys (lights / heavies / warehouse) — real spend, real news.
//   - autosizer_blocked_total{class,guard}: a COUNTER incremented once each time a guard blocks a
//     candidate buy, labelled by the blocking guard — the dashboard's view of WHY the autosizer is
//     not buying (which knob to retune).
//   - autosizer_demand_hulls{class} / autosizer_current_hulls{class}: GAUGES of the sized demand vs
//     the live pool per class, so the shortfall the autosizer is chasing is visible.
//   - autosizer_zero_effect_alarm_total: a COUNTER incremented once per edge-triggered zero-effect
//     alarm episode (demand persisted, nothing bought for N ticks) — backs the ZeroEffect alert.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch a buy decision, so every method
// is nil-safe and best-effort. The autosizer's guard/buy paths run independently of this collector.
type FleetAutosizerMetricsCollector struct {
	purchasesTotal  *prometheus.CounterVec
	blockedTotal    *prometheus.CounterVec
	demandHulls     *prometheus.GaugeVec
	currentHulls    *prometheus.GaugeVec
	zeroEffectTotal prometheus.Counter
}

// NewFleetAutosizerMetricsCollector creates a new autosizer metrics collector.
func NewFleetAutosizerMetricsCollector() *FleetAutosizerMetricsCollector {
	return &FleetAutosizerMetricsCollector{
		purchasesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "autosizer_purchases_total",
				Help:      "Hulls the fleet autosizer bought behind the guard stack, counted once per purchase, by class (sp-1txd)",
			},
			[]string{"class"},
		),
		blockedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "autosizer_blocked_total",
				Help:      "Candidate autosizer buys blocked by a guard, counted once per block, by class and blocking guard (sp-1txd)",
			},
			[]string{"class", "guard"},
		),
		demandHulls: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "autosizer_demand_hulls",
				Help:      "The hull count the autosizer's demand model wants standing, by class (sp-1txd)",
			},
			[]string{"class"},
		),
		currentHulls: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "autosizer_current_hulls",
				Help:      "The live hull count the autosizer sees, by class (sp-1txd)",
			},
			[]string{"class"},
		),
		zeroEffectTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "autosizer_zero_effect_alarm_total",
				Help:      "Edge-triggered zero-effect alarm episodes (demand persisted but nothing bought for N ticks) (sp-1txd)",
			},
		),
	}
}

// Register registers the autosizer metrics with the Prometheus registry. A nil Registry (metrics
// disabled) is a no-op, matching the sibling collectors.
func (c *FleetAutosizerMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	if err := Registry.Register(c.purchasesTotal); err != nil {
		return err
	}
	if err := Registry.Register(c.blockedTotal); err != nil {
		return err
	}
	if err := Registry.Register(c.demandHulls); err != nil {
		return err
	}
	if err := Registry.Register(c.currentHulls); err != nil {
		return err
	}
	return Registry.Register(c.zeroEffectTotal)
}

// RecordPurchase increments the purchase counter for a class (called once per executed buy).
func (c *FleetAutosizerMetricsCollector) RecordPurchase(class string) {
	if c == nil || c.purchasesTotal == nil {
		return
	}
	c.purchasesTotal.WithLabelValues(class).Inc()
}

// RecordBlocked increments the blocked counter for a (class, guard) (once per guard block).
func (c *FleetAutosizerMetricsCollector) RecordBlocked(class, guard string) {
	if c == nil || c.blockedTotal == nil {
		return
	}
	c.blockedTotal.WithLabelValues(class, guard).Inc()
}

// RecordDemand sets the demand/current gauges for a class (once per tick per class).
func (c *FleetAutosizerMetricsCollector) RecordDemand(class string, demand, current int) {
	if c == nil {
		return
	}
	if c.demandHulls != nil {
		c.demandHulls.WithLabelValues(class).Set(float64(demand))
	}
	if c.currentHulls != nil {
		c.currentHulls.WithLabelValues(class).Set(float64(current))
	}
}

// RecordZeroEffectAlarm increments the zero-effect alarm counter (once per edge-triggered episode).
func (c *FleetAutosizerMetricsCollector) RecordZeroEffectAlarm() {
	if c == nil || c.zeroEffectTotal == nil {
		return
	}
	c.zeroEffectTotal.Inc()
}

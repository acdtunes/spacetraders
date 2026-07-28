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

	// The heavy-trade series (sp-fwk8z). Labelled by player_id ONLY: these are per-player
	// fleet facts, not per-class ones, and the reservation in particular must be readable
	// as a single number — an operator asking "why has nothing bought?" wants one series,
	// not a sum over classes.
	heavyReserveCredits *prometheus.GaugeVec
	heaviesOwned        *prometheus.GaugeVec
	heavyCap            *prometheus.GaugeVec
	heavyPricePremium   *prometheus.SummaryVec
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
		heavyReserveCredits: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "autosizer_heavy_reserve_credits",
				Help:      "Credits held back for the NEXT heavy purchase, derived per tick (sp-fwk8z). A non-zero value beside stalled probe/light buying means the fleet is SAVING for a heavy, not broken — it is the series that tells accumulation from failure",
			},
			[]string{"player_id"},
		),
		heaviesOwned: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "autosizer_heavies_owned",
				Help:      "Owned HEAVY hulls counted tag-independently (frame list primary, cargo-capacity safety net) — capital exposure, NOT the trade-pool count behind fleet_ceiling_heavies (sp-fwk8z)",
			},
			[]string{"player_id"},
		),
		heavyCap: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "autosizer_heavy_cap",
				Help:      "The operator's heavy-hull cap in force this tick, after the live-config read. 0 is a deliberate HOLD, not an unset knob (sp-fwk8z)",
			},
			[]string{"player_id"},
		),
		heavyPricePremium: prometheus.NewSummaryVec(
			prometheus.SummaryOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "autosizer_heavy_price_premium_percent",
				Help:      "Per heavy purchase: how far above the cheapest KNOWN yard ask we actually paid, in percent (spec measurement 3 — the price of buying at the cheapest yard WITH PRESENCE rather than the cheapest absolutely). 0 means we paid the best known price; a persistent positive is what presence-lag costs",
			},
			[]string{"player_id"},
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
	if err := Registry.Register(c.heavyReserveCredits); err != nil {
		return err
	}
	if err := Registry.Register(c.heaviesOwned); err != nil {
		return err
	}
	if err := Registry.Register(c.heavyCap); err != nil {
		return err
	}
	if err := Registry.Register(c.heavyPricePremium); err != nil {
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

// RecordHeavyReserve sets the per-tick heavy-trade gauges: the derived reservation, the
// tag-independent owned-heavy census, and the cap in force. Called once per tick, whatever the
// outcome — a reserve that is only recorded when something happens is exactly the series an
// operator cannot use to tell "saving" from "stuck".
func (c *FleetAutosizerMetricsCollector) RecordHeavyReserve(playerID string, reserve int64, owned, cap int) {
	if c == nil {
		return
	}
	if c.heavyReserveCredits != nil {
		c.heavyReserveCredits.WithLabelValues(playerID).Set(float64(reserve))
	}
	if c.heaviesOwned != nil {
		c.heaviesOwned.WithLabelValues(playerID).Set(float64(owned))
	}
	if c.heavyCap != nil {
		c.heavyCap.WithLabelValues(playerID).Set(float64(cap))
	}
}

// ObserveHeavyPricePremium records what one heavy purchase cost ABOVE the cheapest known yard ask,
// in percent. A non-positive cheapest basis is skipped rather than divided by (mirroring
// ObserveLegPriceDrift): an unknown basis is not a 0% premium.
func (c *FleetAutosizerMetricsCollector) ObserveHeavyPricePremium(playerID string, paid, cheapestKnown int64) {
	if c == nil || c.heavyPricePremium == nil || cheapestKnown <= 0 {
		return
	}
	premium := float64(paid-cheapestKnown) / float64(cheapestKnown) * 100
	c.heavyPricePremium.WithLabelValues(playerID).Observe(premium)
}

package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// The wave's two reader names. They are the label values of fleet_growth_wave, and the whole point
// of that label: the failure this series exists to catch is the two consumers DISAGREEING, which a
// single-writer gauge cannot see.
const (
	// WaveReaderGrowth is the fleet-growth coordinator — the consumer that SPENDS on the wave.
	WaveReaderGrowth = "growth"
	// WaveReaderDrain is the probe buy queue — the consumer that PAUSES on it.
	WaveReaderDrain = "drain"
)

// waveProbeReasonNoneLabel is the label value for "HEAVY path, no probe reason".
//
// THE EMPTY REASON IS MAPPED HERE, AT THE METRIC BOUNDARY, and nowhere else. The Go constant for
// "no reason" is the empty string, which is right in code — but in PromQL a selector reason="" also
// matches every series that LACKS the label entirely, so an empty label value conflates "the wave
// was HEAVY" with "this series was never labelled", defeating the exact discrimination the reason
// series exists to provide. Mapping inside the single writer means no caller can bypass it.
const waveProbeReasonNoneLabel = "none"

// FleetGrowthMetricsCollector houses the fleet-growth coordinator's observation series, and the
// wave gauges both of the wave's consumers publish:
//
//   - fleet_growth_wave{player_id,reader}: the regime as derived THIS TICK by THAT reader.
//   - fleet_growth_wave_probe_reason{player_id,reader,reason}: which clause forced PROBE, per reader.
//   - growth_purchases_total / growth_blocked_total / growth_demand_hulls / growth_current_hulls /
//     growth_zero_effect_alarm_total: the buy path, mirroring the autosizer's series.
//   - growth_heavy_reserve_credits / growth_heavy_reserve_target_credits / growth_heavies_owned /
//     growth_heavy_cap / growth_working_capital_credits: the money the coordinator is holding back
//     and what it is holding it back against.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch a buy decision, so every method
// is nil-safe and best-effort.
type FleetGrowthMetricsCollector struct {
	wave            *prometheus.GaugeVec
	waveProbeReason *prometheus.GaugeVec

	purchasesTotal  *prometheus.CounterVec
	blockedTotal    *prometheus.CounterVec
	demandHulls     *prometheus.GaugeVec
	currentHulls    *prometheus.GaugeVec
	zeroEffectTotal prometheus.Counter

	heavyReserveCredits *prometheus.GaugeVec
	heavyReserveTarget  *prometheus.GaugeVec
	heaviesOwned        *prometheus.GaugeVec
	heavyCap            *prometheus.GaugeVec
	workingCapital      *prometheus.GaugeVec
	heavyPricePremium   *prometheus.SummaryVec

	laneSurface *prometheus.GaugeVec

	growthEnabled *prometheus.GaugeVec

	// mu guards seenReasons, which is how a superseded reason is driven to 0 rather than left
	// standing: a gauge that only sets the CURRENT reason leaves the previous one reading 1 forever.
	// Keyed per (player, READER) — scoped to the player alone, each reader would zero the other's
	// live reason every tick, the same conflation the reader label removes one layer down.
	mu          sync.Mutex
	seenReasons map[string]map[string]struct{}
}

// NewFleetGrowthMetricsCollector creates a new fleet-growth metrics collector.
func NewFleetGrowthMetricsCollector() *FleetGrowthMetricsCollector {
	return &FleetGrowthMetricsCollector{
		wave: newGaugeVec(
			"fleet_growth_wave",
			"The heavy/probe regime as derived THIS TICK: 1=HEAVY (probe buying paused so the treasury can reach a hull's ask), 0=PROBE. Emitted every tick by BOTH readers — reader=growth is the coordinator, reader=drain is the probe buy queue — because the failure this series exists to catch is the two DISAGREEING, which a single-writer gauge cannot see. A gap in either reader is a stalled consumer, not a regime",
			"player_id", "reader",
		),
		waveProbeReason: newGaugeVec(
			"fleet_growth_wave_probe_reason",
			"Which clause forced PROBE this tick, one series per reader per reason, 1=this reason 0=not. PROBE otherwise has one name and several meanings, and an operator watching probe buying continue cannot tell 'the fleet cannot plausibly reach a hull this era' from 'the lane surface is down' without it. reason=none is the HEAVY path. Labelled by READER like fleet_growth_wave beside it: two readers writing one reason series would each drive the other's to 0, and a reason DIVERGENCE names which input the two consumers saw differently",
			"player_id", "reader", "reason",
		),
		purchasesTotal: newCounterVec(
			"growth_purchases_total",
			"Hulls the fleet-growth coordinator bought behind the guard stack, counted once per purchase, by class",
			"class",
		),
		blockedTotal: newCounterVec(
			"growth_blocked_total",
			"Candidate growth buys blocked by a guard, counted once per block, by class and blocking guard — the dashboard's view of WHY nothing is being bought (which knob to retune)",
			"class", "guard",
		),
		demandHulls: newGaugeVec(
			"growth_demand_hulls",
			"The hull count the growth coordinator's demand model wants standing, by class",
			"class",
		),
		currentHulls: newGaugeVec(
			"growth_current_hulls",
			"The live trade-pool hull count the growth coordinator sees, by class",
			"class",
		),
		heavyReserveCredits: newGaugeVec(
			"growth_heavy_reserve_credits",
			"Credits ACTUALLY held back for the NEXT heavy purchase, derived per tick and treasury-bounded. A non-zero value beside stalled probe buying means the fleet is SAVING for a heavy, not broken. Read it BESIDE growth_heavy_reserve_target_credits: 0 here with a positive target means the ask is out of reach this era",
			"player_id",
		),
		heavyReserveTarget: newGaugeVec(
			"growth_heavy_reserve_target_credits",
			"The target yard's ask the fleet is SAVING TOWARD for the next heavy — an aspiration, not a hold. The gap between this and growth_heavy_reserve_credits is the treasury bound doing its job. Without this series a bounded hold of 0 is indistinguishable from 'no heavy yard is priced'",
			"player_id",
		),
		heaviesOwned: newGaugeVec(
			"growth_heavies_owned",
			"Owned HEAVY hulls counted tag-independently — capital exposure, NOT the tag-scoped trade-pool count behind heavy DEMAND",
			"player_id",
		),
		heavyCap: newGaugeVec(
			"growth_heavy_cap",
			"The operator's heavy-hull cap in force this tick, after the live-config read. 0 is a deliberate HOLD, not an unset knob",
			"player_id",
		),
		workingCapital: newGaugeVec(
			"growth_working_capital_credits",
			"Credits the trading fleet's OBSERVED activity has already spoken for, held back ON TOP OF the immutable reserve floor before a heavy may be bought. Read it beside growth_blocked_total{guard=\"affordability\"}: a rising value there is the runway term refusing hulls the fleet cannot afford to stop trading for",
			"player_id",
		),
		heavyPricePremium: prometheus.NewSummaryVec(
			prometheus.SummaryOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "growth_heavy_price_premium_percent",
				Help:      "Per heavy purchase: how far above the cheapest KNOWN yard ask we actually paid, in percent. 0 means we paid the best known price; a persistent positive is what presence-lag costs",
			},
			[]string{"player_id"},
		),
		laneSurface: newGaugeVec(
			"growth_lane_surface",
			"The wave's deciding input, published in PARTS. component=unserved is the number DeriveWave actually tests (profitable minus trade_pool, floored at 0); component=profitable is the ranked floor-clearing lane count; component=trade_pool is the hulls tagged 'trade' that subtract from it; component=systems_scanned is how many systems the ranking covered; component=readable is 1 when the surface was read at all. Publishing only the difference is what made a PROBE wave unexplainable: probe_reason=lanes_served fires whenever unserved<=0, and a correct zero, an under-scoped ranking (within-system pairs only) and a narrow system sweep all print identically. READ readable BESIDE THE COUNTS: on an unreadable surface the counts are published as 0, and a 0 with readable=0 is a blind read, never a verdict",
			"player_id", "component",
		),
		growthEnabled: newGaugeVec(
			"growth_enabled",
			"The growth_enabled master switch as read this tick: 1=growing, 0=PAUSED by operator tune. Emitted every tick on both paths — at 0 the coordinator reads nothing and buys nothing, which is deliberate, NOT a stalled coordinator, and the wave it publishes is PROBE",
			"player_id",
		),
		zeroEffectTotal: newCounter(
			"growth_zero_effect_alarm_total",
			"Edge-triggered zero-effect alarm episodes (trade-capacity demand persisted but nothing bought for N ticks)",
		),
		seenReasons: make(map[string]map[string]struct{}),
	}
}

// Register registers the fleet-growth metrics with the Prometheus registry. A nil Registry (metrics
// disabled) is a no-op, matching the sibling collectors.
func (c *FleetGrowthMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.wave,
		c.waveProbeReason,
		c.purchasesTotal,
		c.blockedTotal,
		c.demandHulls,
		c.currentHulls,
		c.heavyReserveCredits,
		c.heavyReserveTarget,
		c.heaviesOwned,
		c.heavyCap,
		c.workingCapital,
		c.heavyPricePremium,
		c.laneSurface,
		c.growthEnabled,
		c.zeroEffectTotal,
	)
}

// RecordWave publishes one reader's regime for this tick, and which clause forced PROBE for THAT
// reader. Every reason previously published for this (player, reader) is driven to 0 before the
// current one is set to 1, so a superseded reason cannot linger as a second series claiming the
// same tick. The empty reason arrives here as the "none" label; see waveProbeReasonNoneLabel.
func (c *FleetGrowthMetricsCollector) RecordWave(playerID, reader string, heavy bool, reason string) {
	value := 0.0
	if heavy {
		value = 1
	}
	c.wave.WithLabelValues(playerID, reader).Set(value)

	label := reason
	if label == "" {
		label = waveProbeReasonNoneLabel
	}
	scope := playerID + "\x00" + reader
	c.mu.Lock()
	seen := c.seenReasons[scope]
	if seen == nil {
		seen = make(map[string]struct{})
		c.seenReasons[scope] = seen
	}
	for prior := range seen {
		if prior != label {
			c.waveProbeReason.WithLabelValues(playerID, reader, prior).Set(0)
		}
	}
	seen[label] = struct{}{}
	c.mu.Unlock()
	c.waveProbeReason.WithLabelValues(playerID, reader, label).Set(1)
}

// RecordLaneSurface publishes the capacity-short signal in parts. No reader label: both wave
// consumers share ONE UnservedLaneReader, so neither computes this and neither can disagree.
func (c *FleetGrowthMetricsCollector) RecordLaneSurface(playerID string, unserved, profitable, tradePool, systemsScanned int, readable bool) {
	readableValue := 0.0
	if readable {
		readableValue = 1
	}
	c.laneSurface.WithLabelValues(playerID, "unserved").Set(float64(unserved))
	c.laneSurface.WithLabelValues(playerID, "profitable").Set(float64(profitable))
	c.laneSurface.WithLabelValues(playerID, "trade_pool").Set(float64(tradePool))
	c.laneSurface.WithLabelValues(playerID, "systems_scanned").Set(float64(systemsScanned))
	c.laneSurface.WithLabelValues(playerID, "readable").Set(readableValue)
}

// RecordGrowthEnabled sets the master-switch gauge from the value read this tick.
func (c *FleetGrowthMetricsCollector) RecordGrowthEnabled(playerID string, enabled bool) {
	value := 0.0
	if enabled {
		value = 1
	}
	c.growthEnabled.WithLabelValues(playerID).Set(value)
}

// RecordHeavyReserve sets the per-tick heavy gauges: what is withheld, what it is withheld toward,
// what is owned and the cap in force.
func (c *FleetGrowthMetricsCollector) RecordHeavyReserve(playerID string, reserve, target int64, owned, capacity int) {
	c.heavyReserveCredits.WithLabelValues(playerID).Set(float64(reserve))
	c.heavyReserveTarget.WithLabelValues(playerID).Set(float64(target))
	c.heaviesOwned.WithLabelValues(playerID).Set(float64(owned))
	c.heavyCap.WithLabelValues(playerID).Set(float64(capacity))
}

// RecordWorkingCapital sets the observed-commitment gauge for this tick.
func (c *FleetGrowthMetricsCollector) RecordWorkingCapital(playerID string, credits int64) {
	c.workingCapital.WithLabelValues(playerID).Set(float64(credits))
}

// RecordDemand sets the demand/current gauges for a class.
func (c *FleetGrowthMetricsCollector) RecordDemand(class string, demand, current int) {
	c.demandHulls.WithLabelValues(class).Set(float64(demand))
	c.currentHulls.WithLabelValues(class).Set(float64(current))
}

// RecordPurchase increments the purchase counter for a class.
func (c *FleetGrowthMetricsCollector) RecordPurchase(class string) {
	c.purchasesTotal.WithLabelValues(class).Inc()
}

// RecordBlocked increments the blocked counter for a (class, guard).
func (c *FleetGrowthMetricsCollector) RecordBlocked(class, guard string) {
	c.blockedTotal.WithLabelValues(class, guard).Inc()
}

// RecordZeroEffectAlarm increments the zero-effect alarm counter.
func (c *FleetGrowthMetricsCollector) RecordZeroEffectAlarm() {
	c.zeroEffectTotal.Inc()
}

// ObserveHeavyPricePremium records one heavy purchase's premium over the cheapest known yard ask.
func (c *FleetGrowthMetricsCollector) ObserveHeavyPricePremium(playerID string, paid, cheapestKnown int64) {
	if cheapestKnown <= 0 {
		return
	}
	premium := float64(paid-cheapestKnown) / float64(cheapestKnown) * 100
	if premium < 0 {
		premium = 0
	}
	c.heavyPricePremium.WithLabelValues(playerID).Observe(premium)
}

// globalFleetGrowthCollector is the singleton fleet-growth collector. Set by
// SetGlobalFleetGrowthCollector() when metrics are enabled; every Record func below is a no-op
// while it is nil, so a metrics-disabled daemon costs the buy path nothing.
var globalFleetGrowthCollector *FleetGrowthMetricsCollector

// SetGlobalFleetGrowthCollector sets the global fleet-growth collector. Pass nil to disable.
func SetGlobalFleetGrowthCollector(collector *FleetGrowthMetricsCollector) {
	globalFleetGrowthCollector = collector
}

// RecordFleetGrowthWave publishes one reader's wave globally. BOTH consumers of the wave call this
// — reader is what tells them apart, and a series present for one reader and absent for the other
// is the split-brain this design forbids.
func RecordFleetGrowthWave(playerID, reader string, heavy bool, reason string) {
	if globalFleetGrowthCollector != nil {
		globalFleetGrowthCollector.RecordWave(playerID, reader, heavy, reason)
	}
}

// RecordGrowthLaneSurface publishes the lane surface globally, on EVERY read: a series that stops on
// a failed read leaves the last good numbers standing and makes a blind tick look like a quiet one.
func RecordGrowthLaneSurface(playerID string, unserved, profitable, tradePool, systemsScanned int, readable bool) {
	if globalFleetGrowthCollector != nil {
		globalFleetGrowthCollector.RecordLaneSurface(playerID, unserved, profitable, tradePool, systemsScanned, readable)
	}
}

// RecordGrowthEnabled publishes the growth_enabled master switch as read this tick.
func RecordGrowthEnabled(playerID string, enabled bool) {
	if globalFleetGrowthCollector != nil {
		globalFleetGrowthCollector.RecordGrowthEnabled(playerID, enabled)
	}
}

// RecordGrowthHeavyReserve sets the per-tick heavy gauges globally.
func RecordGrowthHeavyReserve(playerID string, reserve, target int64, owned, capacity int) {
	if globalFleetGrowthCollector != nil {
		globalFleetGrowthCollector.RecordHeavyReserve(playerID, reserve, target, owned, capacity)
	}
}

// RecordGrowthWorkingCapital sets the observed-commitment gauge globally.
func RecordGrowthWorkingCapital(playerID string, credits int64) {
	if globalFleetGrowthCollector != nil {
		globalFleetGrowthCollector.RecordWorkingCapital(playerID, credits)
	}
}

// RecordGrowthDemand sets the growth demand/current gauges for a class globally.
func RecordGrowthDemand(class string, demand, current int) {
	if globalFleetGrowthCollector != nil {
		globalFleetGrowthCollector.RecordDemand(class, demand, current)
	}
}

// RecordGrowthPurchase increments the growth purchase counter globally.
func RecordGrowthPurchase(class string) {
	if globalFleetGrowthCollector != nil {
		globalFleetGrowthCollector.RecordPurchase(class)
	}
}

// RecordGrowthBlocked increments the growth blocked counter for a (class, guard) globally.
func RecordGrowthBlocked(class, guard string) {
	if globalFleetGrowthCollector != nil {
		globalFleetGrowthCollector.RecordBlocked(class, guard)
	}
}

// RecordGrowthZeroEffectAlarm increments the growth zero-effect alarm counter globally.
func RecordGrowthZeroEffectAlarm() {
	if globalFleetGrowthCollector != nil {
		globalFleetGrowthCollector.RecordZeroEffectAlarm()
	}
}

// ObserveGrowthHeavyPricePremium records one heavy purchase's premium over the cheapest known ask.
func ObserveGrowthHeavyPricePremium(playerID string, paid, cheapestKnown int64) {
	if globalFleetGrowthCollector != nil {
		globalFleetGrowthCollector.ObserveHeavyPricePremium(playerID, paid, cheapestKnown)
	}
}

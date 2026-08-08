package metrics

// opportunity_relocator_metrics.go — the Prometheus half of the opportunity relocator's telemetry
//. A NEW FILE and a NEW series set: nothing here edits or reuses the tour collector.
//
// WHY A SEPARATE SERIES rather than the existing tour_repositions_total. This package already decided
// that question in writing, for the same shape of problem: tour_metrics.go records that "the legacy
// reposition keeps tour_repositions_total, so the two engines' telemetry never conflate." Two
// hull-relocating engines already got separate series with that reason stated. The relocator is a
// third, and sharing would be wrong three times over — it would contradict that policy; its reason
// vocabulary (claimed_at_actuation, region_rate_unreadable, within_cooldown, ...) has no overlap with
// the tour's success|failed|no_candidate; and it would pollute a denominator that file deliberately
// curates ("the kill-switch and the one-per-episode guard are NOT counted ... counting them would
// pollute the no_candidate rate").
//
// WHAT THESE ANSWER. The relocator shipped emitting nothing, so a relocator losing EVERY decision was
// indistinguishable from one with nothing to do, and "3 of the first 4 decisions lost to the claim
// race" had to be hand-derived by joining daemon.log against the containers table. These three series
// turn that into a query, and — the point of the whole bead — make a mitigation's before/after
// readable instead of re-derived:
//
//	rate(relocator_skips_total{reason="claimed_at_actuation"}[1h])              did the race get better?
//	sum(rate(relocator_ticks_total{verdict="BLOCKED"}[1h])) / sum(rate(...))    what fraction of ticks refuse?
//	rate(relocator_decisions_total[1h])                                         is it moving anything at all?
//
// All symbols are Relocator-prefixed so this file cannot collide with the tour collector it sits
// beside.

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// RelocatorMetricsCollector holds the opportunity relocator's counters.
type RelocatorMetricsCollector struct {
	// ticksTotal counts ticks by three-way verdict (PROGRESS / IDLE / BLOCKED). It is the DENOMINATOR
	// that makes the others legible: a hundred blocked ticks means something different on a fleet
	// ticking every 15 minutes than on one ticking every 15 seconds.
	ticksTotal *prometheus.CounterVec
	// decisionsTotal counts hulls actually moved, split relocated vs resumed — a fleet whose only
	// movement is post-restart resumptions is not finding new ground.
	decisionsTotal *prometheus.CounterVec
	// skipsTotal counts exclusions by reason — the per-tick aggregate the reconciler already
	// computes and would otherwise discard.
	skipsTotal *prometheus.CounterVec
}

// NewRelocatorMetricsCollector builds the relocator's collector, mirroring the sibling collectors'
// constructor idiom.
func NewRelocatorMetricsCollector() *RelocatorMetricsCollector {
	return &RelocatorMetricsCollector{
		ticksTotal: newCounterVec(
			"relocator_ticks_total",
			"Opportunity relocator ticks by verdict: PROGRESS (a hull moved), IDLE (nothing to do, correctly), BLOCKED (a relocation was licensed and could not be carried out, or a signal the decision needed was unreadable)",
			"player_id",
			"verdict",
		),
		decisionsTotal: newCounterVec(
			"relocator_decisions_total",
			"Hulls the opportunity relocator actually moved: relocated (a fresh decision) or resumed (an interrupted move finished after a restart)",
			"player_id",
			"outcome",
		),
		skipsTotal: newCounterVec(
			"relocator_skips_total",
			"Opportunity relocator exclusions by reason. claimed_at_actuation is the claim race (the economics said yes and the hull was gone by actuation); the economic verdicts (no_uplift, below_npv_threshold) are the reconciler working, not failing",
			"player_id",
			"reason",
		),
	}
}

// Register registers the relocator metrics with the Prometheus registry. A nil Registry (metrics
// disabled) is a no-op, matching the sibling collectors.
func (c *RelocatorMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.ticksTotal,
		c.decisionsTotal,
		c.skipsTotal,
	)
}

// RecordTick counts one tick by verdict. Nil-safe and best-effort: a metrics miss must never touch the
// relocation path (RULINGS #4).
func (c *RelocatorMetricsCollector) RecordTick(playerID int, verdict string) {
	if c == nil || c.ticksTotal == nil {
		return
	}
	c.ticksTotal.WithLabelValues(strconv.Itoa(playerID), verdict).Inc()
}

// RecordDecision counts one hull moved, by how it moved.
func (c *RelocatorMetricsCollector) RecordDecision(playerID int, outcome string) {
	if c == nil || c.decisionsTotal == nil {
		return
	}
	c.decisionsTotal.WithLabelValues(strconv.Itoa(playerID), outcome).Inc()
}

// RecordSkip adds one tick's exclusion count for a reason. It ADDS rather than increments because the
// tick already aggregated the reason across every hull it considered.
func (c *RelocatorMetricsCollector) RecordSkip(playerID int, reason string, count int) {
	if c == nil || c.skipsTotal == nil || count <= 0 {
		return
	}
	c.skipsTotal.WithLabelValues(strconv.Itoa(playerID), reason).Add(float64(count))
}

// globalRelocatorCollector is the process-wide collector, following the package's established
// global-setter idiom. Nil until the daemon enables metrics, and every method is nil-safe, so an unset
// collector simply records nothing.
var globalRelocatorCollector *RelocatorMetricsCollector

// SetGlobalRelocatorCollector installs the process-wide collector.
func SetGlobalRelocatorCollector(c *RelocatorMetricsCollector) {
	globalRelocatorCollector = c
}

// GetGlobalRelocatorCollector returns the process-wide collector, which may be nil when metrics are
// disabled.
func GetGlobalRelocatorCollector() *RelocatorMetricsCollector {
	return globalRelocatorCollector
}

// RelocatorMetricsPort is the thin adapter the relocator's application-layer sink is satisfied by. It
// resolves the global collector LAZILY, per call, because coordinator wiring runs before the collector
// constructor does — the same reasoning the stall port documents.
type RelocatorMetricsPort struct{}

// NewRelocatorMetricsPort builds the port.
func NewRelocatorMetricsPort() *RelocatorMetricsPort { return &RelocatorMetricsPort{} }

// RecordTick forwards to the process-wide collector, if one is installed.
func (p *RelocatorMetricsPort) RecordTick(playerID int, verdict string) {
	GetGlobalRelocatorCollector().RecordTick(playerID, verdict)
}

// RecordDecision forwards to the process-wide collector, if one is installed.
func (p *RelocatorMetricsPort) RecordDecision(playerID int, outcome string) {
	GetGlobalRelocatorCollector().RecordDecision(playerID, outcome)
}

// RecordSkip forwards to the process-wide collector, if one is installed.
func (p *RelocatorMetricsPort) RecordSkip(playerID int, reason string, count int) {
	GetGlobalRelocatorCollector().RecordSkip(playerID, reason, count)
}

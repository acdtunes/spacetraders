package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// StallMetricsCollector is the Prometheus half of the coordinator stall-escalation layer
// (internal/application/health/stall.go). It publishes the two things an operator needs to tell
// a fleet that has nothing to do from a fleet that cannot do anything:
//
//   - coordinator_stall_streak{coordinator,scope,reason}: a GAUGE of the LIVE consecutive-BLOCKED
//     tick count. It is SET on every observation including the drain back to zero when the block
//     clears, so a recovered coordinator stops reading as wedged instead of holding its last
//     non-zero value until the series goes stale (the parked-sensing collector's rule).
//
//   - coordinator_stall_escalations_total{coordinator,scope,reason}: a COUNTER incremented ONCE
//     per stall episode, on the tick the streak reaches health.StallEscalationTicks. Episodes, not
//     ticks: the eighteen-tick production streak is one increment, not sixteen.
//
// The reason label is drawn from a small stable vocabulary (a guard name, a port that could not be
// read, a pass that could not select a target) — never an error string or a live number, which
// would explode cardinality AND restart the streak every tick so nothing could ever escalate.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch a decision path, so every
// method is nil-safe and best-effort, mirroring FleetHealthMetricsCollector.
type StallMetricsCollector struct {
	streak      *prometheus.GaugeVec
	escalations *prometheus.CounterVec
}

// NewStallMetricsCollector creates a new coordinator-stall collector.
func NewStallMetricsCollector() *StallMetricsCollector {
	return &StallMetricsCollector{
		streak: newGaugeVec(
			"coordinator_stall_streak",
			"Consecutive ticks a coordinator has reported BLOCKED (had work to do and could not do it) on the SAME reason. 0 means the block has cleared; escalation fires at the threshold",
			"coordinator",
			"scope",
			"reason",
		),
		escalations: newCounterVec(
			"coordinator_stall_escalations_total",
			"Coordinator stall EPISODES escalated: counted once per streak on the tick it reaches the consecutive-BLOCKED threshold, never once per tick past it",
			"coordinator",
			"scope",
			"reason",
		),
	}
}

// Register registers the stall metrics with the Prometheus registry. A nil Registry (metrics
// disabled) is a no-op, matching the sibling collectors.
func (c *StallMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.streak,
		c.escalations,
	)
}

// RecordStallStreak sets the live consecutive-BLOCKED tick count for one coordinator/scope/reason.
// A streak of 0 is the deliberate drain on recovery, not an absent sample.
func (c *StallMetricsCollector) RecordStallStreak(coordinator, scope, reason string, streak int) {
	if c == nil || c.streak == nil {
		return // Recording is best-effort; never panic a coordinator tick (RULINGS #4).
	}
	c.streak.WithLabelValues(coordinator, scope, reason).Set(float64(streak))
}

// RecordStallEscalation increments the episode counter, called once per stall streak.
func (c *StallMetricsCollector) RecordStallEscalation(coordinator, scope, reason string) {
	if c == nil || c.escalations == nil {
		return
	}
	c.escalations.WithLabelValues(coordinator, scope, reason).Inc()
}

// globalStallCollector is the process-wide collector, following the package's established
// global-setter idiom. Nil until the daemon enables metrics, and every method is nil-safe, so an
// unset collector simply records nothing.
var globalStallCollector *StallMetricsCollector

// SetGlobalStallCollector installs the process-wide collector.
func SetGlobalStallCollector(c *StallMetricsCollector) {
	globalStallCollector = c
}

// GetGlobalStallCollector returns the process-wide collector, which may be nil when metrics are
// disabled.
func GetGlobalStallCollector() *StallMetricsCollector {
	return globalStallCollector
}

// StallMetricsPort is the seam the escalator holds. It resolves the global collector LAZILY on
// every call because handler wiring runs BEFORE NewDaemonServer builds the collectors — a port
// that captured the reference at construction would be permanently nil, which is precisely the
// "ships wired to nothing" failure this lane exists to end. It satisfies
// health.StallMetricsSink structurally, so this adapter never imports the application layer.
type StallMetricsPort struct{}

// NewStallMetricsPort returns the lazily-resolving stall metrics port.
func NewStallMetricsPort() *StallMetricsPort { return &StallMetricsPort{} }

// RecordStallStreak forwards to the process-wide collector, if one is installed.
func (p *StallMetricsPort) RecordStallStreak(coordinator, scope, reason string, streak int) {
	GetGlobalStallCollector().RecordStallStreak(coordinator, scope, reason, streak)
}

// RecordStallEscalation forwards to the process-wide collector, if one is installed.
func (p *StallMetricsPort) RecordStallEscalation(coordinator, scope, reason string) {
	GetGlobalStallCollector().RecordStallEscalation(coordinator, scope, reason)
}

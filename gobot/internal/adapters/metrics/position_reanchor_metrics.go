package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// PositionReanchorCollector is the Prometheus half of the ship-position re-anchor signal
// (internal/adapters/api/ship_position_reanchor.go). It publishes the one number an
// operator needs to know that the fleet's durable state is drifting from reality:
//
//   - ship_position_reanchors_total{believed_system,actual_system}: a COUNTER of hulls
//     found in a DIFFERENT system from the one their ships row claimed. Every increment is
//     a completed move that was never persisted. In a healthy fleet this series is flat at
//     zero forever, which is exactly what makes any increment worth alerting on — unlike
//     an error rate, there is no acceptable background level.
//
// The labels are system symbols, not hull symbols: a stuck hull re-anchoring every sync
// would otherwise mint a new series per ship, and the PAIR of systems is what identifies
// which mover lost the write (the believed system is where the hull departed from, the
// actual one where it ended up). The hull itself is carried on the captain event and the
// log line, which are the surfaces you read once the counter has told you to look.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch a decision path, so
// every method is nil-safe and best-effort, mirroring StallMetricsCollector.
type PositionReanchorCollector struct {
	reanchors *prometheus.CounterVec
}

// NewPositionReanchorCollector creates a new position-re-anchor collector.
func NewPositionReanchorCollector() *PositionReanchorCollector {
	return &PositionReanchorCollector{
		reanchors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "ship_position_reanchors_total",
				Help:      "Hulls a sync found in a DIFFERENT system from the one their persisted row claimed. Every increment is a completed move that was never written; a healthy fleet holds this at zero",
			},
			[]string{"believed_system", "actual_system"},
		),
	}
}

// Register registers the re-anchor metric with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *PositionReanchorCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}
	return Registry.Register(c.reanchors)
}

// RecordPositionReanchor counts one discovery that a hull's persisted system was wrong.
func (c *PositionReanchorCollector) RecordPositionReanchor(believedSystem, actualSystem string) {
	if c == nil || c.reanchors == nil {
		return // Recording is best-effort; never panic the sync that is reporting (RULINGS #4).
	}
	c.reanchors.WithLabelValues(believedSystem, actualSystem).Inc()
}

// globalPositionReanchorCollector is the process-wide collector, following the package's
// established global-setter idiom. Nil until the daemon enables metrics, and every method
// is nil-safe, so an unset collector simply records nothing.
var globalPositionReanchorCollector *PositionReanchorCollector

// SetGlobalPositionReanchorCollector installs the process-wide collector.
func SetGlobalPositionReanchorCollector(c *PositionReanchorCollector) {
	globalPositionReanchorCollector = c
}

// GetGlobalPositionReanchorCollector returns the process-wide collector, which may be nil
// when metrics are disabled.
func GetGlobalPositionReanchorCollector() *PositionReanchorCollector {
	return globalPositionReanchorCollector
}

// RecordPositionReanchor forwards to the process-wide collector, resolving it LAZILY on
// every call because the ship repository is wired BEFORE NewDaemonServer builds the
// collectors — a reference captured at construction would be permanently nil, which is
// precisely the "wired to nothing" failure this signal exists to end (the same reasoning
// as StallMetricsPort).
func RecordPositionReanchor(believedSystem, actualSystem string) {
	GetGlobalPositionReanchorCollector().RecordPositionReanchor(believedSystem, actualSystem)
}

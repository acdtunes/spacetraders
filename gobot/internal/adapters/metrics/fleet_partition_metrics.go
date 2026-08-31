package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// FleetPartitionMetricsCollector publishes how the routing service answered each
// fleet-partition request: solved, or fallen back to its own round-robin.
//
// WHY THIS EXISTS. The partitioner returns a round-robin under a success flag
// whenever its solver cannot answer, so a solver that has stopped solving looks
// exactly like one that works — and did, fleet-wide, unwatched. This makes "the
// partitioner is degraded" a question a scrape can answer.
//
// Pure OBSERVATION (RULINGS #4): recording is nil-safe and never touches whether
// a partition is accepted.
type FleetPartitionMetricsCollector struct {
	// outcomes counts every answered partition. One series per (outcome, status).
	outcomes *prometheus.CounterVec
}

// Partition outcome label values.
const (
	// PartitionOutcomeSolved is an assignment the VRP actually solved.
	PartitionOutcomeSolved = "solved"
	// PartitionOutcomeFallback is the service's round-robin, solver unable.
	PartitionOutcomeFallback = "fallback"
)

// NewFleetPartitionMetricsCollector creates a new fleet-partition collector.
func NewFleetPartitionMetricsCollector() *FleetPartitionMetricsCollector {
	return &FleetPartitionMetricsCollector{
		outcomes: newCounterVec(
			"fleet_partition_answers_total",
			"Fleet-partition responses from the routing service, by outcome (solved or fallback) and the status the service reported",
			"outcome",
			"status",
		),
	}
}

// Register registers the fleet-partition metric with the Prometheus registry.
func (c *FleetPartitionMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(c.outcomes)
}

// RecordAnswer counts one partition response.
func (c *FleetPartitionMetricsCollector) RecordAnswer(fallback bool, status string) {
	if c == nil || c.outcomes == nil {
		return
	}
	outcome := PartitionOutcomeSolved
	if fallback {
		outcome = PartitionOutcomeFallback
	}
	if status == "" {
		status = "unknown"
	}
	c.outcomes.WithLabelValues(outcome, status).Inc()
}

var globalFleetPartitionCollector *FleetPartitionMetricsCollector

// SetGlobalFleetPartitionCollector installs the process-wide collector.
func SetGlobalFleetPartitionCollector(c *FleetPartitionMetricsCollector) {
	globalFleetPartitionCollector = c
}

// GetGlobalFleetPartitionCollector returns the process-wide collector, or nil.
func GetGlobalFleetPartitionCollector() *FleetPartitionMetricsCollector {
	return globalFleetPartitionCollector
}

// RecordFleetPartitionAnswer is the nil-safe package-level convenience the
// routing adapter records through.
func RecordFleetPartitionAnswer(fallback bool, status string) {
	if c := GetGlobalFleetPartitionCollector(); c != nil {
		c.RecordAnswer(fallback, status)
	}
}

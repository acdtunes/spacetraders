package parkedsensing

// metrics_port.go is the sensing loop's observation seam. It exists only to keep
// the application layer free of a Prometheus import, and it resolves the
// collector LAZILY on every call for a wiring-order reason: the daemon builds
// its handlers before it constructs the server that installs the metrics
// collectors, so a reference captured at wiring time would be permanently nil.

import "github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"

// MetricsPort publishes the parked-probe sensing gauges.
//
// Every method is a no-op when metrics are disabled, which is the collector's
// own nil-safe contract rather than a check repeated here. Recording is
// best-effort throughout (RULINGS #4): observation must never be able to fail a
// reconcile.
type MetricsPort struct{}

// NewMetricsPort wires the sensing gauges.
func NewMetricsPort() *MetricsPort { return &MetricsPort{} }

// RecordRate publishes the scan pacer's current rate.
func (MetricsPort) RecordRate(playerID int, reqPerSec float64) {
	metrics.GetGlobalParkedSensingCollector().RecordRate(playerID, reqPerSec)
}

// RecordStaleness publishes one market-staleness percentile tier.
func (MetricsPort) RecordStaleness(playerID int, tier string, seconds float64) {
	metrics.GetGlobalParkedSensingCollector().RecordStaleness(playerID, tier, seconds)
}

// RecordSlots publishes the placement count for one lifecycle state.
func (MetricsPort) RecordSlots(playerID int, state string, count int) {
	metrics.GetGlobalParkedSensingCollector().RecordSlots(playerID, state, count)
}

// RecordYardCatalogue publishes one state of the free shipyard-catalogue sweep.
func (MetricsPort) RecordYardCatalogue(playerID int, state string, count int) {
	metrics.GetGlobalParkedSensingCollector().RecordYardCatalogue(playerID, state, count)
}

// RecordYardPresence publishes one outcome of the presence-dispatch pass.
func (MetricsPort) RecordYardPresence(playerID int, outcome string, count int) {
	metrics.GetGlobalParkedSensingCollector().RecordYardPresence(playerID, outcome, count)
}

// RecordYardSlots publishes one stage of the buy queue's dark-yard accounting.
func (MetricsPort) RecordYardSlots(playerID int, stage string, count int) {
	metrics.GetGlobalParkedSensingCollector().RecordYardSlots(playerID, stage, count)
}

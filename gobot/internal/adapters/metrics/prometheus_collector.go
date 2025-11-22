package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// Namespace for all metrics
	namespace = "spacetraders"
	// Subsystem for daemon metrics
	subsystem = "daemon"
)

var (
	// Registry is the global Prometheus registry for all metrics
	Registry *prometheus.Registry

	// globalCollector is the singleton metrics collector
	// Set by SetGlobalCollector() when metrics are enabled
	globalCollector MetricsRecorder
)

// MetricsRecorder defines the interface for recording metrics events
// This interface is used by domain/application code to record metrics
type MetricsRecorder interface {
	RecordContainerCompletion(containerInfo ContainerInfo)
	RecordContainerRestart(containerInfo ContainerInfo)
	RecordContainerIteration(containerInfo ContainerInfo)
}

// InitRegistry initializes the Prometheus registry
// Should be called once at application startup if metrics are enabled
func InitRegistry() {
	Registry = prometheus.NewRegistry()
}

// GetRegistry returns the global Prometheus registry
// Returns nil if metrics are not initialized
func GetRegistry() *prometheus.Registry {
	return Registry
}

// IsEnabled returns true if metrics collection is enabled
func IsEnabled() bool {
	return Registry != nil
}

// SetGlobalCollector sets the global metrics collector
// This should be called after the collector is created and started
func SetGlobalCollector(collector MetricsRecorder) {
	globalCollector = collector
}

// RecordContainerCompletion records a container completion event globally
func RecordContainerCompletion(containerInfo ContainerInfo) {
	if globalCollector != nil {
		globalCollector.RecordContainerCompletion(containerInfo)
	}
}

// RecordContainerRestart records a container restart event globally
func RecordContainerRestart(containerInfo ContainerInfo) {
	if globalCollector != nil {
		globalCollector.RecordContainerRestart(containerInfo)
	}
}

// RecordContainerIteration records a container iteration completion globally
func RecordContainerIteration(containerInfo ContainerInfo) {
	if globalCollector != nil {
		globalCollector.RecordContainerIteration(containerInfo)
	}
}

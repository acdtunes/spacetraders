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

// Registry is the global Prometheus registry for all metrics
var Registry *prometheus.Registry

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

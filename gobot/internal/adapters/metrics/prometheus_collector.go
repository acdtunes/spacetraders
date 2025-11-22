package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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

	// globalCollector is the singleton container metrics collector
	// Set by SetGlobalCollector() when metrics are enabled
	globalCollector MetricsRecorder

	// globalNavigationCollector is the singleton navigation metrics collector
	// Set by SetGlobalNavigationCollector() when metrics are enabled
	globalNavigationCollector NavigationMetricsRecorder

	// globalFinancialCollector is the singleton financial metrics collector
	// Set by SetGlobalFinancialCollector() when metrics are enabled
	globalFinancialCollector FinancialMetricsRecorder
)

// MetricsRecorder defines the interface for recording container metrics events
// This interface is used by domain/application code to record metrics
type MetricsRecorder interface {
	RecordContainerCompletion(containerInfo ContainerInfo)
	RecordContainerRestart(containerInfo ContainerInfo)
	RecordContainerIteration(containerInfo ContainerInfo)
}

// NavigationMetricsRecorder defines the interface for recording navigation metrics
type NavigationMetricsRecorder interface {
	RecordRouteCompletion(playerID int, status navigation.RouteStatus, duration float64, distance int, fuelConsumed int)
	RecordSegmentCompletion(playerID int, distance int, fuelRequired int)
	RecordFuelPurchase(playerID int, waypoint string, units int)
	RecordFuelConsumption(playerID int, flightMode shared.FlightMode, units int)
}

// FinancialMetricsRecorder defines the interface for recording financial metrics
type FinancialMetricsRecorder interface {
	RecordTransaction(playerID int, agentSymbol string, transactionType string, category string, amount int, creditsBalance int)
	RecordTrade(playerID int, goodSymbol string, buyPrice int, sellPrice int, quantity int)
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

// SetGlobalNavigationCollector sets the global navigation metrics collector
func SetGlobalNavigationCollector(collector NavigationMetricsRecorder) {
	globalNavigationCollector = collector
}

// RecordRouteCompletion records a route completion event globally
func RecordRouteCompletion(playerID int, status navigation.RouteStatus, duration float64, distance int, fuelConsumed int) {
	if globalNavigationCollector != nil {
		globalNavigationCollector.RecordRouteCompletion(playerID, status, duration, distance, fuelConsumed)
	}
}

// RecordSegmentCompletion records a route segment completion globally
func RecordSegmentCompletion(playerID int, distance int, fuelRequired int) {
	if globalNavigationCollector != nil {
		globalNavigationCollector.RecordSegmentCompletion(playerID, distance, fuelRequired)
	}
}

// RecordFuelPurchase records a fuel purchase event globally
func RecordFuelPurchase(playerID int, waypoint string, units int) {
	if globalNavigationCollector != nil {
		globalNavigationCollector.RecordFuelPurchase(playerID, waypoint, units)
	}
}

// RecordFuelConsumption records fuel consumption globally
func RecordFuelConsumption(playerID int, flightMode shared.FlightMode, units int) {
	if globalNavigationCollector != nil {
		globalNavigationCollector.RecordFuelConsumption(playerID, flightMode, units)
	}
}

// SetGlobalFinancialCollector sets the global financial metrics collector
func SetGlobalFinancialCollector(collector FinancialMetricsRecorder) {
	globalFinancialCollector = collector
}

// RecordTransaction records a transaction event globally
func RecordTransaction(playerID int, agentSymbol string, transactionType string, category string, amount int, creditsBalance int) {
	if globalFinancialCollector != nil {
		globalFinancialCollector.RecordTransaction(playerID, agentSymbol, transactionType, category, amount, creditsBalance)
	}
}

// RecordTrade records trade profitability metrics globally
func RecordTrade(playerID int, goodSymbol string, buyPrice int, sellPrice int, quantity int) {
	if globalFinancialCollector != nil {
		globalFinancialCollector.RecordTrade(playerID, goodSymbol, buyPrice, sellPrice, quantity)
	}
}

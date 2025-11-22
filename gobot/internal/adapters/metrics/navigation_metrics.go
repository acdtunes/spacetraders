package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// NavigationMetricsCollector handles all navigation and fuel-related metrics
type NavigationMetricsCollector struct {
	// Route metrics
	routesTotal            *prometheus.CounterVec
	routeDuration          *prometheus.HistogramVec
	routeDistanceTraveled  *prometheus.CounterVec
	routeFuelConsumed      *prometheus.CounterVec
	routeSegmentsCompleted *prometheus.CounterVec

	// Fuel metrics
	fuelPurchased  *prometheus.CounterVec
	fuelConsumed   *prometheus.CounterVec
	fuelEfficiency *prometheus.HistogramVec
}

// NewNavigationMetricsCollector creates a new navigation metrics collector
func NewNavigationMetricsCollector() *NavigationMetricsCollector {
	return &NavigationMetricsCollector{
		// Route completions/failures counter
		routesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "routes_total",
				Help:      "Total number of route lifecycle events by status",
			},
			[]string{"player_id", "status"},
		),

		// Route execution duration histogram
		routeDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "route_duration_seconds",
				Help:      "Route execution duration distribution",
				Buckets:   []float64{10, 30, 60, 120, 300, 600, 1200, 1800},
			},
			[]string{"player_id", "status"},
		),

		// Total distance traveled counter
		routeDistanceTraveled: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "route_distance_traveled_total",
				Help:      "Total distance traveled across all routes",
			},
			[]string{"player_id"},
		),

		// Total fuel consumed by routes counter
		routeFuelConsumed: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "route_fuel_consumed_total",
				Help:      "Total fuel consumed by route execution",
			},
			[]string{"player_id"},
		),

		// Route segments completed counter
		routeSegmentsCompleted: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "route_segments_completed_total",
				Help:      "Total number of route segments completed",
			},
			[]string{"player_id"},
		),

		// Fuel purchases counter
		fuelPurchased: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "fuel_purchased_units_total",
				Help:      "Total units of fuel purchased",
			},
			[]string{"player_id", "waypoint"},
		),

		// Fuel consumption by flight mode counter
		fuelConsumed: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "fuel_consumed_units_total",
				Help:      "Total units of fuel consumed by flight mode",
			},
			[]string{"player_id", "flight_mode"},
		),

		// Fuel efficiency histogram
		fuelEfficiency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "fuel_efficiency_ratio",
				Help:      "Fuel efficiency distribution (distance per fuel unit)",
				Buckets:   []float64{0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 20.0},
			},
			[]string{"player_id"},
		),
	}
}

// Register registers all navigation metrics with the Prometheus registry
func (c *NavigationMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
		c.routesTotal,
		c.routeDuration,
		c.routeDistanceTraveled,
		c.routeFuelConsumed,
		c.routeSegmentsCompleted,
		c.fuelPurchased,
		c.fuelConsumed,
		c.fuelEfficiency,
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
}

// RecordRouteCompletion records a route completion event
func (c *NavigationMetricsCollector) RecordRouteCompletion(
	playerID int,
	status navigation.RouteStatus,
	duration float64,
	distance int,
	fuelConsumed int,
) {
	playerIDStr := strconv.Itoa(playerID)
	statusStr := string(status)

	// Increment route counter
	c.routesTotal.WithLabelValues(playerIDStr, statusStr).Inc()

	// Record duration (only for completed/failed routes)
	if status == navigation.RouteStatusCompleted || status == navigation.RouteStatusFailed {
		c.routeDuration.WithLabelValues(playerIDStr, statusStr).Observe(duration)
	}

	// Record distance and fuel (only for completed routes)
	if status == navigation.RouteStatusCompleted {
		c.routeDistanceTraveled.WithLabelValues(playerIDStr).Add(float64(distance))
		c.routeFuelConsumed.WithLabelValues(playerIDStr).Add(float64(fuelConsumed))
	}
}

// RecordSegmentCompletion records a route segment completion
func (c *NavigationMetricsCollector) RecordSegmentCompletion(
	playerID int,
	distance int,
	fuelRequired int,
) {
	playerIDStr := strconv.Itoa(playerID)

	// Increment segment counter
	c.routeSegmentsCompleted.WithLabelValues(playerIDStr).Inc()

	// Calculate and record fuel efficiency
	if fuelRequired > 0 {
		efficiency := float64(distance) / float64(fuelRequired)
		c.fuelEfficiency.WithLabelValues(playerIDStr).Observe(efficiency)
	}
}

// RecordFuelPurchase records a fuel purchase event
func (c *NavigationMetricsCollector) RecordFuelPurchase(
	playerID int,
	waypoint string,
	units int,
) {
	playerIDStr := strconv.Itoa(playerID)

	c.fuelPurchased.WithLabelValues(playerIDStr, waypoint).Add(float64(units))
}

// RecordFuelConsumption records fuel consumption
func (c *NavigationMetricsCollector) RecordFuelConsumption(
	playerID int,
	flightMode shared.FlightMode,
	units int,
) {
	playerIDStr := strconv.Itoa(playerID)
	flightModeStr := flightMode.Name()

	c.fuelConsumed.WithLabelValues(playerIDStr, flightModeStr).Add(float64(units))
}

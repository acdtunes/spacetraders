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

	// Jump claim-record hygiene
	strandedJumpContainers *prometheus.CounterVec

	// Refuel failures that were absorbed instead of failing the route
	nonEssentialRefuelFailures *prometheus.CounterVec
}

// NewNavigationMetricsCollector creates a new navigation metrics collector
func NewNavigationMetricsCollector() *NavigationMetricsCollector {
	return &NavigationMetricsCollector{
		// Route completions/failures counter
		routesTotal: newCounterVec(
			"routes_total",
			"Total number of route lifecycle events by status",
			"player_id",
			"status",
		),

		// Route execution duration histogram
		routeDuration: newHistogramVec(
			"route_duration_seconds",
			"Route execution duration distribution",
			[]float64{10, 30, 60, 120, 300, 600, 1200, 1800},
			"player_id",
			"status",
		),

		// Total distance traveled counter
		routeDistanceTraveled: newCounterVec(
			"route_distance_traveled_total",
			"Total distance traveled across all routes",
			"player_id",
		),

		// Total fuel consumed by routes counter
		routeFuelConsumed: newCounterVec(
			"route_fuel_consumed_total",
			"Total fuel consumed by route execution",
			"player_id",
		),

		// Route segments completed counter
		routeSegmentsCompleted: newCounterVec(
			"route_segments_completed_total",
			"Total number of route segments completed",
			"player_id",
		),

		// Fuel purchases counter
		fuelPurchased: newCounterVec(
			"fuel_purchased_units_total",
			"Total units of fuel purchased",
			"player_id",
			"waypoint",
		),

		// Fuel consumption by flight mode counter
		fuelConsumed: newCounterVec(
			"fuel_consumed_units_total",
			"Total units of fuel consumed by flight mode",
			"player_id",
			"flight_mode",
		),

		// Fuel efficiency histogram
		fuelEfficiency: newHistogramVec(
			"fuel_efficiency_ratio",
			"Fuel efficiency distribution (distance per fuel unit)",
			[]float64{0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 20.0},
			"player_id",
		),

		// Stranded jump-claim records found and cleared. A jump's
		// container row is a pure FK placeholder that its own handler deletes on
		// the way out; a row that outlives its jump is a leak. Labelled by outcome
		// so a reaper that never fires (0) is distinguishable from one that fires
		// and fails (clear_failed) — otherwise a broken reaper and a clean fleet
		// emit the identical signal.
		strandedJumpContainers: newCounterVec(
			"stranded_jump_containers_total",
			"Leftover jump container rows found by the post-claim reap, by outcome (cleared/clear_failed)",
			"player_id",
			"outcome",
		),

		// A refuel failure the route survived. Without it, "the top-up failed and we
		// carried on" and "the hull is genuinely stranded" are the same silence at the
		// metrics layer, and telling them apart means reading log cadence by hand — which
		// is how the incident this closes was found in the first place. kind separates the
		// discretionary top-up from a planned stop the remaining legs turned out not to need.
		nonEssentialRefuelFailures: newCounterVec(
			"non_essential_refuel_failures_total",
			"Refuel failures absorbed without failing the route, by kind (opportunistic/planned_not_required)",
			"player_id",
			"kind",
		),
	}
}

// Register registers all navigation metrics with the Prometheus registry
func (c *NavigationMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.routesTotal,
		c.routeDuration,
		c.routeDistanceTraveled,
		c.routeFuelConsumed,
		c.routeSegmentsCompleted,
		c.fuelPurchased,
		c.fuelConsumed,
		c.fuelEfficiency,
		c.strandedJumpContainers,
		c.nonEssentialRefuelFailures,
	)
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

// RecordNonEssentialRefuelFailure records globally one refuel failure the route survived.
func RecordNonEssentialRefuelFailure(playerID int, kind string) {
	if globalNavigationCollector != nil {
		globalNavigationCollector.RecordNonEssentialRefuelFailure(playerID, kind)
	}
}

// RecordStrandedJumpContainer records one leftover jump container row the
// post-claim reap found, under the outcome it reached ("cleared" /
// "clear_failed").
func (c *NavigationMetricsCollector) RecordStrandedJumpContainer(
	playerID int,
	outcome string,
) {
	c.strandedJumpContainers.WithLabelValues(strconv.Itoa(playerID), outcome).Inc()
}

// globalNavigationCollector is the singleton navigation metrics collector
// Set by SetGlobalNavigationCollector() when metrics are enabled
var globalNavigationCollector NavigationMetricsRecorder

// NavigationMetricsRecorder defines the interface for recording navigation metrics
type NavigationMetricsRecorder interface {
	RecordRouteCompletion(playerID int, status navigation.RouteStatus, duration float64, distance int, fuelConsumed int)
	RecordSegmentCompletion(playerID int, distance int, fuelRequired int)
	RecordFuelPurchase(playerID int, waypoint string, units int)
	RecordFuelConsumption(playerID int, flightMode shared.FlightMode, units int)
	RecordStrandedJumpContainer(playerID int, outcome string)
	RecordNonEssentialRefuelFailure(playerID int, kind string)
}

// RecordNonEssentialRefuelFailure records one refuel failure that did not fail its route.
func (c *NavigationMetricsCollector) RecordNonEssentialRefuelFailure(playerID int, kind string) {
	c.nonEssentialRefuelFailures.WithLabelValues(strconv.Itoa(playerID), kind).Inc()
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

// RecordStrandedJumpContainer records one leftover jump container row the
// post-claim reap found, under the outcome it reached.
func RecordStrandedJumpContainer(playerID int, outcome string) {
	if globalNavigationCollector != nil {
		globalNavigationCollector.RecordStrandedJumpContainer(playerID, outcome)
	}
}

package navigation

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RouteStatus represents route execution status
type RouteStatus string

const (
	RouteStatusPlanned   RouteStatus = "PLANNED"
	RouteStatusExecuting RouteStatus = "EXECUTING"
	RouteStatusCompleted RouteStatus = "COMPLETED"
	RouteStatusFailed    RouteStatus = "FAILED"
	RouteStatusAborted   RouteStatus = "ABORTED"
)

// RouteSegment represents an immutable route segment
type RouteSegment struct {
	FromWaypoint   *shared.Waypoint
	ToWaypoint     *shared.Waypoint
	Distance       float64
	FuelRequired   int
	TravelTime     int
	FlightMode     shared.FlightMode
	RequiresRefuel bool
}

// NewRouteSegment creates a new route segment
func NewRouteSegment(
	from, to *shared.Waypoint,
	distance float64,
	fuelRequired, travelTime int,
	mode shared.FlightMode,
	requiresRefuel bool,
) *RouteSegment {
	return &RouteSegment{
		FromWaypoint:   from,
		ToWaypoint:     to,
		Distance:       distance,
		FuelRequired:   fuelRequired,
		TravelTime:     travelTime,
		FlightMode:     mode,
		RequiresRefuel: requiresRefuel,
	}
}

func (r *RouteSegment) String() string {
	refuel := ""
	if r.RequiresRefuel {
		refuel = " [REFUEL]"
	}
	return fmt.Sprintf("%s → %s (%.1fu, %d⛽, %s)%s",
		r.FromWaypoint.Symbol, r.ToWaypoint.Symbol,
		r.Distance, r.FuelRequired, r.FlightMode, refuel)
}

// Route aggregate root - represents a complete navigation plan
//
// Invariants:
// - Segments form connected path (segment[i].to == segment[i+1].from)
// - Total fuel required does not exceed ship capacity
// - Route can only be executed from PLANNED status
//
// Lifecycle Integration:
// - Uses LifecycleStateMachine for timestamp and error management
// - Maps RouteStatus to LifecycleStatus for consistent lifecycle handling
type Route struct {
	routeID               string
	shipSymbol            string
	playerID              int
	segments              []*RouteSegment
	shipFuelCapacity      int
	refuelBeforeDeparture bool
	lifecycle             *shared.LifecycleStateMachine
	currentSegmentIndex   int
}

// NewRoute creates a new route with validation
func NewRoute(
	routeID, shipSymbol string,
	playerID int,
	segments []*RouteSegment,
	shipFuelCapacity int,
	refuelBeforeDeparture bool,
) (*Route, error) {
	r := &Route{
		routeID:               routeID,
		shipSymbol:            shipSymbol,
		playerID:              playerID,
		segments:              segments,
		shipFuelCapacity:      shipFuelCapacity,
		refuelBeforeDeparture: refuelBeforeDeparture,
		lifecycle:             shared.NewLifecycleStateMachine(nil), // Use real clock
		currentSegmentIndex:   0,
	}

	// Only validate if we have segments
	if len(segments) > 0 {
		if err := r.validate(); err != nil {
			return nil, err
		}
	}

	return r, nil
}

func (r *Route) validate() error {
	// Check segments form connected path
	for i := 0; i < len(r.segments)-1; i++ {
		current := r.segments[i]
		next := r.segments[i+1]
		if current.ToWaypoint.Symbol != next.FromWaypoint.Symbol {
			return fmt.Errorf("segments not connected: %s → %s",
				current.ToWaypoint.Symbol, next.FromWaypoint.Symbol)
		}
	}

	// Check fuel requirements don't exceed capacity
	maxFuelNeeded := 0
	for _, seg := range r.segments {
		if seg.FuelRequired > maxFuelNeeded {
			maxFuelNeeded = seg.FuelRequired
		}
	}
	if maxFuelNeeded > r.shipFuelCapacity {
		return fmt.Errorf("segment requires %d fuel but ship capacity is %d",
			maxFuelNeeded, r.shipFuelCapacity)
	}

	return nil
}

// Getters

func (r *Route) RouteID() string {
	return r.routeID
}

func (r *Route) ShipSymbol() string {
	return r.shipSymbol
}

func (r *Route) PlayerID() int {
	return r.playerID
}

func (r *Route) Segments() []*RouteSegment {
	// Return a copy to prevent mutation
	segments := make([]*RouteSegment, len(r.segments))
	copy(segments, r.segments)
	return segments
}

// Status returns the current route status
// Maps LifecycleStatus to RouteStatus for domain-specific semantics
func (r *Route) Status() RouteStatus {
	switch r.lifecycle.Status() {
	case shared.LifecycleStatusPending:
		return RouteStatusPlanned
	case shared.LifecycleStatusRunning:
		return RouteStatusExecuting
	case shared.LifecycleStatusCompleted:
		return RouteStatusCompleted
	case shared.LifecycleStatusFailed:
		return RouteStatusFailed
	case shared.LifecycleStatusStopped:
		return RouteStatusAborted
	default:
		return RouteStatusPlanned // Safe default
	}
}

// Lifecycle timestamp accessors

func (r *Route) CreatedAt() time.Time {
	return r.lifecycle.CreatedAt()
}

func (r *Route) UpdatedAt() time.Time {
	return r.lifecycle.UpdatedAt()
}

func (r *Route) StartedAt() *time.Time {
	return r.lifecycle.StartedAt()
}

func (r *Route) CompletedAt() *time.Time {
	return r.lifecycle.StoppedAt()
}

func (r *Route) LastError() error {
	return r.lifecycle.LastError()
}

func (r *Route) CurrentSegmentIndex() int {
	return r.currentSegmentIndex
}

func (r *Route) RefuelBeforeDeparture() bool {
	return r.refuelBeforeDeparture
}

// Route execution

// StartExecution begins route execution
// Delegates to lifecycle state machine for state management
func (r *Route) StartExecution() error {
	status := r.Status()
	if status != RouteStatusPlanned {
		return fmt.Errorf("cannot start route in status %s", status)
	}
	return r.lifecycle.Start()
}

// CompleteSegment marks current segment as complete and advances
func (r *Route) CompleteSegment() error {
	status := r.Status()
	if status != RouteStatusExecuting {
		return fmt.Errorf("cannot complete segment when route status is %s", status)
	}

	r.currentSegmentIndex++
	r.lifecycle.UpdateTimestamp()

	// Check if route complete
	if r.currentSegmentIndex >= len(r.segments) {
		return r.lifecycle.Complete()
	}

	return nil
}

// FailRoute marks route as failed
// Delegates to lifecycle state machine with error tracking
func (r *Route) FailRoute(reason string) {
	err := fmt.Errorf("route failed: %s", reason)
	_ = r.lifecycle.Fail(err) // Ignore error, failure always succeeds
}

// AbortRoute aborts route execution
// Delegates to lifecycle state machine
func (r *Route) AbortRoute(reason string) {
	_ = r.lifecycle.Stop() // Ignore error, stop always succeeds
}

// Route queries

// TotalDistance calculates total distance of route
func (r *Route) TotalDistance() float64 {
	total := 0.0
	for _, seg := range r.segments {
		total += seg.Distance
	}
	return total
}

// TotalFuelRequired calculates total fuel required (assuming refuels at stops)
func (r *Route) TotalFuelRequired() int {
	total := 0
	for _, seg := range r.segments {
		total += seg.FuelRequired
	}
	return total
}

// TotalTravelTime calculates total travel time in seconds
func (r *Route) TotalTravelTime() int {
	total := 0
	for _, seg := range r.segments {
		total += seg.TravelTime
	}
	return total
}

// CurrentSegment gets current segment being executed
func (r *Route) CurrentSegment() *RouteSegment {
	if r.currentSegmentIndex < len(r.segments) {
		return r.segments[r.currentSegmentIndex]
	}
	return nil
}

// RemainingSegments gets remaining segments to execute
func (r *Route) RemainingSegments() []*RouteSegment {
	if r.currentSegmentIndex >= len(r.segments) {
		return []*RouteSegment{}
	}
	remaining := make([]*RouteSegment, len(r.segments)-r.currentSegmentIndex)
	copy(remaining, r.segments[r.currentSegmentIndex:])
	return remaining
}

func (r *Route) String() string {
	return fmt.Sprintf("Route(id=%s, ship=%s, segments=%d, status=%s)",
		r.routeID, r.shipSymbol, len(r.segments), r.Status())
}

// NextSegment returns the next segment to execute (current segment)
// Returns nil if route is complete
func (r *Route) NextSegment() *RouteSegment {
	return r.CurrentSegment()
}

// HasRefuelAtStart checks if route requires refuel before departure
func (r *Route) HasRefuelAtStart() bool {
	return r.refuelBeforeDeparture
}

// IsComplete checks if route execution is complete
func (r *Route) IsComplete() bool {
	return r.Status() == RouteStatusCompleted
}

// IsFailed checks if route execution has failed
func (r *Route) IsFailed() bool {
	return r.Status() == RouteStatusFailed
}

package navigation

import (
	"fmt"

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
type Route struct {
	routeID               string
	shipSymbol            string
	playerID              int
	segments              []*RouteSegment
	shipFuelCapacity      int
	refuelBeforeDeparture bool
	status                RouteStatus
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
		status:                RouteStatusPlanned,
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

func (r *Route) Status() RouteStatus {
	return r.status
}

func (r *Route) CurrentSegmentIndex() int {
	return r.currentSegmentIndex
}

func (r *Route) RefuelBeforeDeparture() bool {
	return r.refuelBeforeDeparture
}

// Route execution

// StartExecution begins route execution
func (r *Route) StartExecution() error {
	if r.status != RouteStatusPlanned {
		return fmt.Errorf("cannot start route in status %s", r.status)
	}
	r.status = RouteStatusExecuting
	return nil
}

// CompleteSegment marks current segment as complete and advances
func (r *Route) CompleteSegment() error {
	if r.status != RouteStatusExecuting {
		return fmt.Errorf("cannot complete segment when route status is %s", r.status)
	}

	r.currentSegmentIndex++

	// Check if route complete
	if r.currentSegmentIndex >= len(r.segments) {
		r.status = RouteStatusCompleted
	}

	return nil
}

// FailRoute marks route as failed
func (r *Route) FailRoute(reason string) {
	r.status = RouteStatusFailed
}

// AbortRoute aborts route execution
func (r *Route) AbortRoute(reason string) {
	r.status = RouteStatusAborted
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
		r.routeID, r.shipSymbol, len(r.segments), r.status)
}

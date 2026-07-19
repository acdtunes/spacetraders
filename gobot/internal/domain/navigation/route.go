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

// routeStatusByLifecycle projects each shared lifecycle state onto the
// route-facing status (PLANNED/EXECUTING/COMPLETED/FAILED/ABORTED). A lifecycle
// state absent here falls back to RouteStatusPlanned (the former switch's safe
// default).
var routeStatusByLifecycle = map[shared.LifecycleStatus]RouteStatus{
	shared.LifecycleStatusPending:   RouteStatusPlanned,
	shared.LifecycleStatusRunning:   RouteStatusExecuting,
	shared.LifecycleStatusCompleted: RouteStatusCompleted,
	shared.LifecycleStatusFailed:    RouteStatusFailed,
	shared.LifecycleStatusStopped:   RouteStatusAborted,
}

// Status returns the current route status
// Maps LifecycleStatus to RouteStatus for domain-specific semantics
func (r *Route) Status() RouteStatus {
	return shared.ProjectStatus(r.lifecycle, routeStatusByLifecycle, RouteStatusPlanned)
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

// Route execution

func (r *Route) StartExecution() error {
	status := r.Status()
	if status != RouteStatusPlanned {
		return fmt.Errorf("cannot start route in status %s", status)
	}
	return r.lifecycle.Start()
}

func (r *Route) CompleteSegment() error {
	status := r.Status()
	if status != RouteStatusExecuting {
		return fmt.Errorf("cannot complete segment when route status is %s", status)
	}

	r.currentSegmentIndex++
	r.lifecycle.UpdateTimestamp()

	if r.currentSegmentIndex >= len(r.segments) {
		return r.lifecycle.Complete()
	}

	return nil
}

func (r *Route) FailRoute(reason string) error {
	return r.lifecycle.Fail(fmt.Errorf("route failed: %s", reason))
}

// Route queries

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

func (r *Route) String() string {
	return fmt.Sprintf("Route(id=%s, ship=%s, segments=%d, status=%s)",
		r.routeID, r.shipSymbol, len(r.segments), r.Status())
}

// NextSegment returns the next segment to execute (current segment)
// Returns nil if route is complete
func (r *Route) NextSegment() *RouteSegment {
	if r.currentSegmentIndex < len(r.segments) {
		return r.segments[r.currentSegmentIndex]
	}
	return nil
}

func (r *Route) HasRefuelAtStart() bool {
	return r.refuelBeforeDeparture
}

func (r *Route) IsComplete() bool {
	return r.Status() == RouteStatusCompleted
}

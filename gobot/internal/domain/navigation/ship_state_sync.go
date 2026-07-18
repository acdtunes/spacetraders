package navigation

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// =============================================================================
// DB-as-Source-of-Truth Methods
// =============================================================================

// FlightMode returns the ship's current flight mode
func (s *Ship) FlightMode() string {
	if s.flightMode == "" {
		return defaultFlightModeName
	}
	return s.flightMode
}

// ArrivalTime returns when the ship will arrive (for IN_TRANSIT ships)
func (s *Ship) ArrivalTime() *time.Time {
	return s.arrivalTime
}

// CooldownExpiration returns when the ship's cooldown expires
func (s *Ship) CooldownExpiration() *time.Time {
	return s.cooldownExpiration
}

// DedicatedFleet returns the coordinator this ship is permanently reserved
// for (e.g. "contract"), or "" if the ship is unreserved and available to
// any coordinator's normal discovery (sp-snmb).
func (s *Ship) DedicatedFleet() string {
	return s.dedicatedFleet
}

// SetFlightMode sets the ship's flight mode
func (s *Ship) SetFlightMode(mode string) {
	s.flightMode = mode
}

// SetDedicatedFleet marks (or clears, with "") the ship as permanently
// reserved for the named coordinator. Used by repositories when loading from
// database, and by a coordinator's startup reconciliation of its configured
// --dedicated-ships list (sp-snmb).
func (s *Ship) SetDedicatedFleet(fleet string) {
	s.dedicatedFleet = fleet
}

// SetArrivalTime sets when the ship will arrive
func (s *Ship) SetArrivalTime(t time.Time) {
	s.arrivalTime = &t
}

// ClearArrivalTime clears the arrival time (ship has arrived)
func (s *Ship) ClearArrivalTime() {
	s.arrivalTime = nil
}

// OriginSymbol returns the waypoint the current transit departed from (sp-vp9k),
// or "" when the ship is not in transit.
func (s *Ship) OriginSymbol() string {
	return s.originSymbol
}

// OriginX returns the x coordinate of the current transit's origin (sp-vp9k).
func (s *Ship) OriginX() float64 {
	return s.originX
}

// OriginY returns the y coordinate of the current transit's origin (sp-vp9k).
func (s *Ship) OriginY() float64 {
	return s.originY
}

// DepartureTime returns when the current transit departed (sp-vp9k), or nil when
// the ship is not in transit.
func (s *Ship) DepartureTime() *time.Time {
	return s.departureTime
}

// SetTransitOrigin records where the current transit departed from (waypoint
// symbol + coordinates) and when (sp-vp9k). Set from the API nav.route on sync
// and reloaded on reconstruct so the values survive a domain Save. A nil
// departure and empty symbol represent a ship that is not in transit.
func (s *Ship) SetTransitOrigin(symbol string, x, y float64, departure *time.Time) {
	s.originSymbol = symbol
	s.originX = x
	s.originY = y
	s.departureTime = departure
}

// SetCooldown sets the cooldown expiration time
func (s *Ship) SetCooldown(t time.Time) {
	s.cooldownExpiration = &t
}

// ClearCooldown clears the cooldown (cooldown has expired)
func (s *Ship) ClearCooldown() {
	s.cooldownExpiration = nil
}

// SetLocation updates the ship's current location
func (s *Ship) SetLocation(w *shared.Waypoint) {
	s.currentLocation = w
}

// SetNavStatus sets the navigation status directly
// Used by repositories when loading from database
func (s *Ship) SetNavStatus(status NavStatus) {
	s.navStatus = status
}

// SetReactor sets the ship's reactor data (symbol, name, power output, and
// the reactor's own requirements). Used by repositories when loading from
// database and when enriching a ship from a fresh API sync. Reactors have no
// swap/upgrade endpoint in the SpaceTraders API - powerOutput is permanent
// for the life of the hull (sp-el60).
func (s *Ship) SetReactor(symbol, name string, powerOutput int, requirements ShipRequirements) {
	s.reactorSymbol = symbol
	s.reactorName = name
	s.reactorPowerOutput = powerOutput
	s.reactorRequirements = requirements
}

// SetSlots sets the frame's fixed module slot and mounting point budgets.
// Frames have no swap/upgrade endpoint - these values are permanent for the
// life of the hull (sp-el60).
func (s *Ship) SetSlots(moduleSlots, mountingPoints int) {
	s.moduleSlots = moduleSlots
	s.mountingPoints = mountingPoints
}

// SetMounts sets the ship's installed mounts.
// Used by repositories when loading from database.
func (s *Ship) SetMounts(mounts []*ShipMount) {
	s.mounts = mounts
}

// SetCrew sets the ship's crew current/required/capacity counts.
// Used by repositories when loading from database.
func (s *Ship) SetCrew(current, required, capacity int) {
	s.crewCurrent = current
	s.crewRequired = required
	s.crewCapacity = capacity
}

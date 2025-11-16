package navigation

import (
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// NavStatus represents ship navigation status
type NavStatus string

const (
	NavStatusDocked    NavStatus = "DOCKED"
	NavStatusInOrbit   NavStatus = "IN_ORBIT"
	NavStatusInTransit NavStatus = "IN_TRANSIT"
)

var validNavStatuses = map[NavStatus]bool{
	NavStatusDocked:    true,
	NavStatusInOrbit:   true,
	NavStatusInTransit: true,
}

// Ship entity - represents a player's spacecraft with navigation capabilities
//
// Invariants:
// - ShipSymbol must be unique and non-empty
// - PlayerID must be positive
// - NavStatus must be one of: IN_ORBIT, DOCKED, IN_TRANSIT
// - Fuel operations respect capacity limits
// - CargoUnits cannot exceed CargoCapacity
// - EngineSpeed must be positive
//
// Navigation state machine:
// - DOCKED -> Depart() -> IN_ORBIT
// - IN_ORBIT -> Navigate() -> IN_TRANSIT
// - IN_TRANSIT -> Arrive() -> IN_ORBIT
// - IN_ORBIT -> Dock() -> DOCKED
type Ship struct {
	shipSymbol      string
	playerID        int
	currentLocation *shared.Waypoint
	fuel            *shared.Fuel
	fuelCapacity    int
	cargoCapacity   int
	cargo           *shared.Cargo
	engineSpeed     int
	frameSymbol     string // Frame type (e.g., "FRAME_PROBE", "FRAME_DRONE", "FRAME_MINER")
	navStatus       NavStatus
}

// NewShip creates a new Ship entity with validation
func NewShip(
	shipSymbol string,
	playerID int,
	currentLocation *shared.Waypoint,
	fuel *shared.Fuel,
	fuelCapacity int,
	cargoCapacity int,
	cargo *shared.Cargo,
	engineSpeed int,
	frameSymbol string,
	navStatus NavStatus,
) (*Ship, error) {
	s := &Ship{
		shipSymbol:      shipSymbol,
		playerID:        playerID,
		currentLocation: currentLocation,
		fuel:            fuel,
		fuelCapacity:    fuelCapacity,
		cargoCapacity:   cargoCapacity,
		cargo:           cargo,
		engineSpeed:     engineSpeed,
		frameSymbol:     frameSymbol,
		navStatus:       navStatus,
	}

	if err := s.validate(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Ship) validate() error {
	if s.shipSymbol == "" {
		return shared.NewInvalidShipDataError("ship_symbol cannot be empty")
	}

	if s.playerID <= 0 {
		return shared.NewInvalidShipDataError("player_id must be positive")
	}

	if s.fuel == nil {
		return shared.NewInvalidShipDataError("fuel cannot be nil")
	}

	if s.fuelCapacity < 0 {
		return shared.NewInvalidShipDataError("fuel_capacity cannot be negative")
	}

	if s.fuel.Capacity != s.fuelCapacity {
		return shared.NewInvalidShipDataError("fuel capacity must match fuel_capacity")
	}

	if s.cargo == nil {
		return shared.NewInvalidShipDataError("cargo cannot be nil")
	}

	if s.cargoCapacity < 0 {
		return shared.NewInvalidShipDataError("cargo_capacity cannot be negative")
	}

	if s.cargo.Units < 0 {
		return shared.NewInvalidShipDataError("cargo_units cannot be negative")
	}

	if s.cargo.Units > s.cargoCapacity {
		return shared.NewInvalidShipDataError("cargo_units cannot exceed cargo_capacity")
	}

	if s.engineSpeed <= 0 {
		return shared.NewInvalidShipDataError("engine_speed must be positive")
	}

	if !validNavStatuses[s.navStatus] {
		return shared.NewInvalidShipDataError(fmt.Sprintf("invalid nav_status: %s", s.navStatus))
	}

	return nil
}

// Getters

func (s *Ship) ShipSymbol() string {
	return s.shipSymbol
}

func (s *Ship) PlayerID() int {
	return s.playerID
}

func (s *Ship) CurrentLocation() *shared.Waypoint {
	return s.currentLocation
}

func (s *Ship) Fuel() *shared.Fuel {
	return s.fuel
}

func (s *Ship) FuelCapacity() int {
	return s.fuelCapacity
}

func (s *Ship) CargoCapacity() int {
	return s.cargoCapacity
}

func (s *Ship) Cargo() *shared.Cargo {
	return s.cargo
}

func (s *Ship) CargoUnits() int {
	if s.cargo == nil {
		return 0
	}
	return s.cargo.Units
}

func (s *Ship) EngineSpeed() int {
	return s.engineSpeed
}

func (s *Ship) NavStatus() NavStatus {
	return s.navStatus
}

func (s *Ship) FrameSymbol() string {
	return s.frameSymbol
}

// Frame Type Queries

// IsProbe checks if ship is a probe type (FRAME_PROBE)
func (s *Ship) IsProbe() bool {
	return s.frameSymbol == "FRAME_PROBE"
}

// IsDrone checks if ship is a drone type (FRAME_DRONE)
func (s *Ship) IsDrone() bool {
	return s.frameSymbol == "FRAME_DRONE"
}

// IsScoutType checks if ship is either a probe or drone (suitable for scouting)
func (s *Ship) IsScoutType() bool {
	return s.IsProbe() || s.IsDrone()
}

// Navigation Status Management

// EnsureInOrbit ensures ship is in orbit (state machine orchestration)
//
// Transitions:
// - DOCKED → IN_ORBIT (automatic transition)
// - IN_ORBIT → IN_ORBIT (no-op)
// - IN_TRANSIT → error (cannot transition while traveling)
//
// Returns true if state was changed, false if already in orbit
func (s *Ship) EnsureInOrbit() (bool, error) {
	if s.navStatus == NavStatusInOrbit {
		return false, nil
	}

	if s.navStatus == NavStatusInTransit {
		return false, shared.NewInvalidNavStatusError("cannot orbit while in transit")
	}

	// Must be docked, use internal transition
	if err := s.depart(); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureDocked ensures ship is docked (state machine orchestration)
//
// Transitions:
// - IN_ORBIT → DOCKED (automatic transition)
// - DOCKED → DOCKED (no-op)
// - IN_TRANSIT → error (cannot transition while traveling)
//
// Returns true if state was changed, false if already docked
func (s *Ship) EnsureDocked() (bool, error) {
	if s.navStatus == NavStatusDocked {
		return false, nil
	}

	if s.navStatus == NavStatusInTransit {
		return false, shared.NewInvalidNavStatusError("cannot dock while in transit")
	}

	// Must be in orbit, use internal transition
	if err := s.dock(); err != nil {
		return false, err
	}
	return true, nil
}

// depart transitions from docked to orbit (internal state transition)
func (s *Ship) depart() error {
	if s.navStatus != NavStatusDocked {
		return shared.NewInvalidNavStatusError(fmt.Sprintf("ship must be docked to depart, currently: %s", s.navStatus))
	}
	s.navStatus = NavStatusInOrbit
	return nil
}

// dock transitions from orbit to docked (internal state transition)
func (s *Ship) dock() error {
	if s.navStatus != NavStatusInOrbit {
		return shared.NewInvalidNavStatusError(fmt.Sprintf("ship must be in orbit to dock, currently: %s", s.navStatus))
	}
	s.navStatus = NavStatusDocked
	return nil
}

// Arrive transitions from in-transit to orbit
func (s *Ship) Arrive() error {
	if s.navStatus != NavStatusInTransit {
		return shared.NewInvalidNavStatusError(fmt.Sprintf("ship must be in transit to arrive, currently: %s", s.navStatus))
	}
	s.navStatus = NavStatusInOrbit
	return nil
}

// StartTransit begins transit to destination
func (s *Ship) StartTransit(destination *shared.Waypoint) error {
	if s.navStatus != NavStatusInOrbit {
		return shared.NewInvalidNavStatusError(fmt.Sprintf("ship must be in orbit to start transit, currently: %s", s.navStatus))
	}
	if s.currentLocation.Symbol == destination.Symbol {
		return fmt.Errorf("cannot transit to same location")
	}
	s.navStatus = NavStatusInTransit
	s.currentLocation = destination
	return nil
}

// Fuel Management

// ConsumeFuel consumes fuel from ship's tanks
func (s *Ship) ConsumeFuel(amount int) error {
	if amount < 0 {
		return fmt.Errorf("fuel amount cannot be negative")
	}

	if s.fuel.Current < amount {
		return shared.NewInsufficientFuelError(amount, s.fuel.Current)
	}

	newFuel, err := s.fuel.Consume(amount)
	if err != nil {
		return err
	}
	s.fuel = newFuel
	return nil
}

// Refuel adds fuel to ship's tanks
func (s *Ship) Refuel(amount int) error {
	if amount < 0 {
		return fmt.Errorf("fuel amount cannot be negative")
	}

	newFuel, err := s.fuel.Add(amount)
	if err != nil {
		return err
	}
	s.fuel = newFuel
	return nil
}

// RefuelToFull refuels ship to full capacity and returns amount added
func (s *Ship) RefuelToFull() (int, error) {
	fuelNeeded := s.fuelCapacity - s.fuel.Current
	if fuelNeeded > 0 {
		if err := s.Refuel(fuelNeeded); err != nil {
			return 0, err
		}
	}
	return fuelNeeded, nil
}

// Navigation Calculations

// CanNavigateTo checks if ship can navigate to destination with current fuel
func (s *Ship) CanNavigateTo(destination *shared.Waypoint) bool {
	distance := s.currentLocation.DistanceTo(destination)
	minFuelRequired := shared.FlightModeDrift.FuelCost(distance)
	return s.fuel.Current >= minFuelRequired
}

// CalculateFuelForTrip calculates fuel required for trip to destination
func (s *Ship) CalculateFuelForTrip(destination *shared.Waypoint, mode shared.FlightMode) int {
	distance := s.currentLocation.DistanceTo(destination)
	return mode.FuelCost(distance)
}

// NeedsRefuelForJourney checks if ship needs refueling before journey
func (s *Ship) NeedsRefuelForJourney(destination *shared.Waypoint, safetyMargin float64) bool {
	distance := s.currentLocation.DistanceTo(destination)
	fuelRequired := shared.FlightModeCruise.FuelCost(distance)
	return !s.fuel.CanTravel(fuelRequired, safetyMargin)
}

// CalculateTravelTime calculates travel time to destination
func (s *Ship) CalculateTravelTime(destination *shared.Waypoint, mode shared.FlightMode) int {
	distance := s.currentLocation.DistanceTo(destination)
	return mode.TravelTime(distance, s.engineSpeed)
}

// SelectOptimalFlightMode selects optimal flight mode for a given distance
func (s *Ship) SelectOptimalFlightMode(distance float64) shared.FlightMode {
	// Calculate costs for each mode
	cruiseCost := shared.FlightModeCruise.FuelCost(distance)

	// Use shared SelectOptimalFlightMode with ship's current fuel
	// Safety margin of 4 ensures we don't run out mid-flight
	return shared.SelectOptimalFlightMode(s.fuel.Current, cruiseCost, 4)
}

// Cargo Management

// HasCargoSpace checks if ship has available cargo space
func (s *Ship) HasCargoSpace(units int) bool {
	if s.cargo == nil {
		return units <= s.cargoCapacity
	}
	return (s.cargo.Units + units) <= s.cargoCapacity
}

// AvailableCargoSpace returns available cargo space
func (s *Ship) AvailableCargoSpace() int {
	if s.cargo == nil {
		return s.cargoCapacity
	}
	return s.cargo.AvailableCapacity()
}

// IsCargoEmpty checks if cargo hold is empty
func (s *Ship) IsCargoEmpty() bool {
	if s.cargo == nil {
		return true
	}
	return s.cargo.IsEmpty()
}

// IsCargoFull checks if cargo hold is full
func (s *Ship) IsCargoFull() bool {
	if s.cargo == nil {
		return false
	}
	return s.cargo.Units >= s.cargoCapacity
}

// State Queries

// IsDocked checks if ship is docked
func (s *Ship) IsDocked() bool {
	return s.navStatus == NavStatusDocked
}

// IsInOrbit checks if ship is in orbit
func (s *Ship) IsInOrbit() bool {
	return s.navStatus == NavStatusInOrbit
}

// IsInTransit checks if ship is in transit
func (s *Ship) IsInTransit() bool {
	return s.navStatus == NavStatusInTransit
}

// IsAtLocation checks if ship is at specified waypoint
func (s *Ship) IsAtLocation(waypoint *shared.Waypoint) bool {
	return s.currentLocation.Symbol == waypoint.Symbol
}

func (s *Ship) String() string {
	return fmt.Sprintf("Ship(symbol=%s, location=%s, status=%s, fuel=%s)",
		s.shipSymbol, s.currentLocation.Symbol, s.navStatus, s.fuel)
}

// Route Execution Decision Methods

// ShouldRefuelOpportunistically determines if ship should refuel at a waypoint
// even if not planned by routing engine (defense-in-depth safety check)
//
// Returns true if:
// - Ship is at a fuel station
// - Fuel is below safety threshold (safetyMargin, e.g., 0.9 = 90%)
// - Ship has fuel capacity > 0
func (s *Ship) ShouldRefuelOpportunistically(waypoint *shared.Waypoint, safetyMargin float64) bool {
	if s.fuelCapacity == 0 {
		return false
	}

	if !waypoint.HasFuel {
		return false
	}

	// Check if ship is at this waypoint
	if s.currentLocation.Symbol != waypoint.Symbol {
		return false
	}

	fuelPercentage := float64(s.fuel.Current) / float64(s.fuelCapacity)
	return fuelPercentage < safetyMargin
}

// ShouldPreventDriftMode determines if ship should refuel before using DRIFT mode
// to prevent unnecessary fuel emergencies at fuel stations
//
// Returns true if:
// - Segment uses DRIFT mode
// - Ship is at the segment's starting waypoint (departure point)
// - Starting waypoint has fuel
// - Fuel is below safety threshold
func (s *Ship) ShouldPreventDriftMode(segment *RouteSegment, safetyMargin float64) bool {
	if s.fuelCapacity == 0 {
		return false
	}

	// Check if using DRIFT mode
	if segment.FlightMode != shared.FlightModeDrift {
		return false
	}

	// Check if at departure waypoint
	if s.currentLocation.Symbol != segment.FromWaypoint.Symbol {
		return false
	}

	// Check if departure waypoint has fuel
	if !segment.FromWaypoint.HasFuel {
		return false
	}

	fuelPercentage := float64(s.fuel.Current) / float64(s.fuelCapacity)
	return fuelPercentage < safetyMargin
}

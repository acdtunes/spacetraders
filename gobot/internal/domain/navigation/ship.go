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

const (
	// DefaultFuelSafetyMargin is the minimum fuel reserve (in units) to maintain
	// for safety during navigation. Prevents running out of fuel due to
	// miscalculations or unexpected detours.
	DefaultFuelSafetyMargin = 4
)

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
	shipSymbol         string
	playerID           shared.PlayerID
	currentLocation    *shared.Waypoint
	fuel               *shared.Fuel
	fuelCapacity       int
	cargoCapacity      int
	cargo              *shared.Cargo
	engineSpeed        int
	frameSymbol        string // Frame type (e.g., "FRAME_PROBE", "FRAME_DRONE", "FRAME_MINER")
	role               string // Ship role from registration (e.g., "EXCAVATOR", "COMMAND", "SATELLITE")
	modules            []*ShipModule // Installed ship modules (jump drives, mining equipment, etc.)
	navStatus          NavStatus
	fuelService        *ShipFuelService
	navigationCalc     *ShipNavigationCalculator
}

// NewShip creates a new Ship entity with validation
func NewShip(
	shipSymbol string,
	playerID shared.PlayerID,
	currentLocation *shared.Waypoint,
	fuel *shared.Fuel,
	fuelCapacity int,
	cargoCapacity int,
	cargo *shared.Cargo,
	engineSpeed int,
	frameSymbol string,
	role string,
	modules []*ShipModule,
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
		role:            role,
		modules:         modules,
		navStatus:       navStatus,
		fuelService:     NewShipFuelService(),
		navigationCalc:  NewShipNavigationCalculator(),
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

	if s.playerID.IsZero() {
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

func (s *Ship) PlayerID() shared.PlayerID {
	return s.playerID
}

func (s *Ship) CurrentLocation() *shared.Waypoint {
	return s.currentLocation
}

func (s *Ship) IsAtLocation(waypoint *shared.Waypoint) bool {
	return s.currentLocation.Symbol == waypoint.Symbol
}

func (s *Ship) Fuel() *shared.Fuel {
	return s.fuel
}

func (s *Ship) FuelCapacity() int {
	return s.fuelCapacity
}

// UpdateFuelFromAPI updates the ship's fuel state from API response data.
// This allows avoiding a separate GetShip API call after navigation/refuel.
func (s *Ship) UpdateFuelFromAPI(current, capacity int) {
	fuel, err := shared.NewFuel(current, capacity)
	if err == nil {
		s.fuel = fuel
		s.fuelCapacity = capacity
	}
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

// Role returns the ship's role from registration
func (s *Ship) Role() string {
	return s.role
}

// Modules returns the ship's installed modules
func (s *Ship) Modules() []*ShipModule {
	return s.modules
}

// HasJumpDrive checks if ship has any jump drive module installed
func (s *Ship) HasJumpDrive() bool {
	for _, module := range s.modules {
		if module.IsJumpDrive() {
			return true
		}
	}
	return false
}

// GetJumpDriveRange returns the range of the ship's jump drive, or 0 if none
func (s *Ship) GetJumpDriveRange() int {
	for _, module := range s.modules {
		if module.IsJumpDrive() {
			return module.Range()
		}
	}
	return 0
}

// CloneAtLocation creates a copy of the ship at a different location with specified fuel
// This is used for route planning to simulate ship state at intermediate waypoints
func (s *Ship) CloneAtLocation(location *shared.Waypoint, currentFuel int) *Ship {
	return &Ship{
		shipSymbol:      s.shipSymbol,
		playerID:        s.playerID,
		currentLocation: location,
		fuel: &shared.Fuel{
			Current:  currentFuel,
			Capacity: s.fuelCapacity,
		},
		fuelCapacity:   s.fuelCapacity,
		cargoCapacity:  s.cargoCapacity,
		cargo:          s.cargo, // Share cargo (immutable for planning)
		engineSpeed:    s.engineSpeed,
		frameSymbol:    s.frameSymbol,
		role:           s.role,
		modules:        s.modules, // Share modules (immutable)
		navStatus:      NavStatusInOrbit, // Assume in orbit for routing
		fuelService:    s.fuelService,
		navigationCalc: s.navigationCalc,
	}
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

// IsScoutType checks if ship is suitable for scouting (SATELLITE role)
// Excludes EXCAVATOR and other mining/hauling roles
func (s *Ship) IsScoutType() bool {
	return s.role == "SATELLITE"
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
	fuelNeeded := s.fuelService.CalculateFuelNeededToFull(s.fuel.Current, s.fuelCapacity)
	if fuelNeeded > 0 {
		if err := s.Refuel(fuelNeeded); err != nil {
			return 0, err
		}
	}
	return fuelNeeded, nil
}

// Navigation Calculations
//
// NOTE: Navigation calculation methods have been moved to ShipFuelService and
// ShipNavigationCalculator to improve separation of concerns. Callers should use
// these services directly for navigation and fuel calculations.

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

func (s *Ship) String() string {
	return fmt.Sprintf("Ship(symbol=%s, location=%s, status=%s, fuel=%s)",
		s.shipSymbol, s.currentLocation.Symbol, s.navStatus, s.fuel)
}

// Route Execution Decision Methods
//
// NOTE: Refueling decision methods have been moved to ShipFuelService to improve
// separation of concerns and enforce Tell-Don't-Ask principle. Callers should use
// ShipFuelService methods directly for refueling decisions.

package navigation

import (
	"fmt"
	"time"

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

	roleSatellite         = "SATELLITE"
	defaultFlightModeName = "CRUISE"
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
//
// Assignment state:
// - Ships can be assigned to containers (operations)
// - Assignment is managed through aggregate methods
// - Repository persists assignment state to database
type Ship struct {
	shipSymbol      string
	playerID        shared.PlayerID
	currentLocation *shared.Waypoint
	fuel            *shared.Fuel
	fuelCapacity    int
	cargoCapacity   int
	cargo           *shared.Cargo
	engineSpeed     int
	frameSymbol     string        // Frame type (e.g., "FRAME_PROBE", "FRAME_DRONE", "FRAME_MINER")
	role            string        // Ship role from registration (e.g., "EXCAVATOR", "COMMAND", "SATELLITE")
	modules         []*ShipModule // Installed ship modules (jump drives, mining equipment, etc.)
	navStatus       NavStatus
	assignment      *ShipAssignment // Container assignment state (persisted to DB)
	fuelService     *ShipFuelService

	// Power/slot/crew data. Reactors and frames have no swap/upgrade
	// endpoint in the SpaceTraders API - reactorPowerOutput, moduleSlots, and
	// mountingPoints are fixed for the life of the hull. Only modules/mounts
	// can be installed or removed to fit within these permanent budgets.
	reactorSymbol       string
	reactorName         string
	reactorPowerOutput  int
	reactorRequirements ShipRequirements
	moduleSlots         int
	mountingPoints      int
	mounts              []*ShipMount // Installed ship mounts (mining lasers, gas siphons, sensor arrays, etc.)
	crewCurrent         int
	crewRequired        int
	crewCapacity        int

	// DB-as-source-of-truth fields
	flightMode         string     // Current flight mode (CRUISE, DRIFT, BURN, STEALTH)
	arrivalTime        *time.Time // When IN_TRANSIT ship will arrive
	cooldownExpiration *time.Time // When cooldown expires (mining, surveying, etc.)

	// Nav route origin + departure for the current transit, carried
	// from the API nav.route so a persisted IN_TRANSIT ship exposes where it
	// departed from and when — the DB consumers compute exact transit progress
	// from these. originSymbol/X/Y are empty/zero and departureTime nil when the
	// ship is not in transit. Reloaded on reconstruct so they round-trip through a
	// domain Save (whole-row UpdateAll upsert) instead of being clobbered to zero.
	originSymbol  string
	originX       float64
	originY       float64
	departureTime *time.Time

	// dedicatedFleet marks the ship as permanently reserved for a specific
	// coordinator (e.g. "contract"), set by the operator via CLI/config rather
	// than derived at runtime. Empty means unreserved - the ship is fair game
	// for any coordinator's normal discovery.
	dedicatedFleet string

	// reservationOverrides is the per-hull cargo do-not-sell override set:
	// good symbol -> explicit reservation decision that WINS over the
	// default MODULE_*/MOUNT_* classification. true force-reserves a good the
	// default would sell; false force-allows the sale of a default-reserved module
	// (the rare deliberate resale). A good absent from the map follows
	// IsDefaultReservedCargo. Persisted as a JSONB column and reloaded on boot
	// (RULINGS #2) so a reservation survives a daemon restart.
	reservationOverrides map[string]bool
	// reservationStateCorrupt is set when the persisted override state could not be
	// parsed. It fails the guard CLOSED: IsCargoReserved then treats EVERY good as
	// reserved (nothing is sold from this hull) rather than risk selling a good the
	// unreadable override set had protected (RULINGS #4).
	reservationStateCorrupt bool

	// persistedVersion is the ships.version value this entity was loaded at
	// (0 = never loaded from a row, e.g. API-born). Infrastructure carries it
	// for the Save CAS tripwire: it is NOT domain state and has no
	// behavior here.
	persistedVersion int
}

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
// The API is authoritative and can over-report current fuel against a shrunk
// capacity; that snapshot is clamped to capacity rather than rejected so a
// transient value never leaves stale fuel driving routing. Genuinely
// invalid data (negative values) still surfaces an error.
func (s *Ship) UpdateFuelFromAPI(current, capacity int) error {
	fuel, err := shared.ReconstructFuel(current, capacity)
	if err != nil {
		return err
	}
	s.fuel = fuel
	s.fuelCapacity = capacity
	return nil
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

func (s *Ship) Role() string {
	return s.role
}

func (s *Ship) Modules() []*ShipModule {
	return s.modules
}

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

// HasWarpDrive reports whether the ship has a warp drive module installed
// (MODULE_WARP_DRIVE_*). Only a ship with a warp drive can execute an off-gate
// warp leg between systems; RouteExecutor fails a warp request closed
// when this is false rather than letting the live API reject it.
func (s *Ship) HasWarpDrive() bool {
	for _, module := range s.modules {
		if module.IsWarpDrive() {
			return true
		}
	}
	return false
}

// IsScoutType checks if ship is suitable for scouting (SATELLITE role)
// Excludes EXCAVATOR and other mining/hauling roles
func (s *Ship) IsScoutType() bool {
	return s.role == roleSatellite
}

// Power/Slot/Crew Queries

// ReactorSymbol returns the reactor type symbol (e.g., "REACTOR_SOLAR_I").
func (s *Ship) ReactorSymbol() string {
	return s.reactorSymbol
}

func (s *Ship) ReactorName() string {
	return s.reactorName
}

// ReactorPowerOutput returns the hull's total power budget. Reactors have no
// swap/upgrade endpoint in the SpaceTraders API - this value is permanent for
// the life of the ship.
func (s *Ship) ReactorPowerOutput() int {
	return s.reactorPowerOutput
}

// ReactorRequirements returns the reactor's own power/crew/slot requirements.
func (s *Ship) ReactorRequirements() ShipRequirements {
	return s.reactorRequirements
}

// ModuleSlots returns the frame's total module slot capacity. Frames have no
// swap/upgrade endpoint - this value is permanent for the life of the ship.
func (s *Ship) ModuleSlots() int {
	return s.moduleSlots
}

// MountingPoints returns the frame's total mounting point capacity. Frames
// have no swap/upgrade endpoint - this value is permanent for the life of
// the ship.
func (s *Ship) MountingPoints() int {
	return s.mountingPoints
}

// Mounts returns the ship's installed mounts (mining lasers, gas siphons,
// sensor arrays, weapons, etc.).
func (s *Ship) Mounts() []*ShipMount {
	return s.mounts
}

func (s *Ship) CrewCurrent() int {
	return s.crewCurrent
}

// CrewRequired returns the crew required to operate the ship at its current
// module/mount loadout.
func (s *Ship) CrewRequired() int {
	return s.crewRequired
}

func (s *Ship) CrewCapacity() int {
	return s.crewCapacity
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

func (s *Ship) depart() error {
	if s.navStatus != NavStatusDocked {
		return shared.NewInvalidNavStatusError(fmt.Sprintf("ship must be docked to depart, currently: %s", s.navStatus))
	}
	s.navStatus = NavStatusInOrbit
	return nil
}

func (s *Ship) dock() error {
	if s.navStatus != NavStatusInOrbit {
		return shared.NewInvalidNavStatusError(fmt.Sprintf("ship must be in orbit to dock, currently: %s", s.navStatus))
	}
	s.navStatus = NavStatusDocked
	return nil
}

func (s *Ship) Arrive() error {
	if s.navStatus != NavStatusInTransit {
		return shared.NewInvalidNavStatusError(fmt.Sprintf("ship must be in transit to arrive, currently: %s", s.navStatus))
	}
	s.navStatus = NavStatusInOrbit
	return nil
}

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

func (s *Ship) RefuelToFull() (int, error) {
	fuelNeeded := s.fuelService.CalculateFuelNeededToFull(s.fuel.Current, s.fuelCapacity)
	if fuelNeeded > 0 {
		if err := s.Refuel(fuelNeeded); err != nil {
			return 0, err
		}
	}
	return fuelNeeded, nil
}

// State Queries

func (s *Ship) IsDocked() bool {
	return s.navStatus == NavStatusDocked
}

func (s *Ship) IsInOrbit() bool {
	return s.navStatus == NavStatusInOrbit
}

func (s *Ship) IsInTransit() bool {
	return s.navStatus == NavStatusInTransit
}

func (s *Ship) String() string {
	return fmt.Sprintf("Ship(symbol=%s, location=%s, status=%s, fuel=%s)",
		s.shipSymbol, s.currentLocation.Symbol, s.navStatus, s.fuel)
}

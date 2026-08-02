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

func (s *Ship) String() string {
	return fmt.Sprintf("Ship(symbol=%s, location=%s, status=%s, fuel=%s)",
		s.shipSymbol, s.currentLocation.Symbol, s.navStatus, s.fuel)
}

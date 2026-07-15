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

	// Power/slot/crew data (sp-el60). Reactors and frames have no swap/upgrade
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

	// Nav route origin + departure for the current transit (sp-vp9k), carried
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
	// for any coordinator's normal discovery (sp-snmb).
	dedicatedFleet string

	// reservationOverrides is the per-hull cargo do-not-sell override set
	// (sp-1vhv): good symbol -> explicit reservation decision that WINS over the
	// default MODULE_*/MOUNT_* classification. true force-reserves a good the
	// default would sell; false force-allows the sale of a default-reserved module
	// (the rare deliberate resale). A good absent from the map follows
	// IsDefaultReservedCargo. Persisted as a JSONB column and reloaded on boot
	// (RULINGS #2) so a reservation survives a daemon restart.
	reservationOverrides map[string]bool
	// reservationStateCorrupt is set when the persisted override state could not be
	// parsed. It fails the guard CLOSED: IsCargoReserved then treats EVERY good as
	// reserved (nothing is sold from this hull) rather than risk selling a good the
	// unreadable override set had protected (sp-1vhv, RULINGS #4).
	reservationStateCorrupt bool

	// persistedVersion is the ships.version value this entity was loaded at
	// (0 = never loaded from a row, e.g. API-born). Infrastructure carries it
	// for the Save CAS tripwire (sp-60ff): it is NOT domain state and has no
	// behavior here.
	persistedVersion int
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
// transient value never leaves stale fuel driving routing (sp-xxhn). Genuinely
// invalid data (negative values) still surfaces an error (#12).
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

// HasWarpDrive reports whether the ship has a warp drive module installed
// (MODULE_WARP_DRIVE_*). Only a ship with a warp drive can execute an off-gate
// warp leg between systems (sp-0xd0); RouteExecutor fails a warp request closed
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

// Power/Slot/Crew Queries (sp-el60)

// ReactorSymbol returns the reactor type symbol (e.g., "REACTOR_SOLAR_I").
func (s *Ship) ReactorSymbol() string {
	return s.reactorSymbol
}

// ReactorName returns the reactor's display name.
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

// CrewCurrent returns the ship's current crew count.
func (s *Ship) CrewCurrent() int {
	return s.crewCurrent
}

// CrewRequired returns the crew required to operate the ship at its current
// module/mount loadout.
func (s *Ship) CrewRequired() int {
	return s.crewRequired
}

// CrewCapacity returns the maximum crew the ship can carry.
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

// PersistedVersion reports the row version this entity was reconstructed at
// (0 = unknown/API-born). See sp-60ff conflict telemetry.
func (s *Ship) PersistedVersion() int { return s.persistedVersion }

// SetPersistedVersion is called by the persistence layer at reconstruction
// and after a committed save.
func (s *Ship) SetPersistedVersion(v int) { s.persistedVersion = v }

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

// Assignment Management
//
// These methods manage the ship's container assignment state.
// Assignments are persisted to database via ShipRepository.Save().

// Assignment returns the ship's current assignment (may be nil if never assigned)
func (s *Ship) Assignment() *ShipAssignment {
	return s.assignment
}

// IsIdle returns true if the ship is available for assignment
// A ship is idle if it has no assignment or its assignment is in idle state
func (s *Ship) IsIdle() bool {
	return s.assignment == nil || s.assignment.IsIdle()
}

// IsAssigned returns true if the ship is currently assigned to a container
func (s *Ship) IsAssigned() bool {
	return s.assignment != nil && s.assignment.IsActive()
}

// ContainerID returns the ID of the container this ship is assigned to
// Returns empty string if ship is not assigned
func (s *Ship) ContainerID() string {
	if s.assignment == nil {
		return ""
	}
	return s.assignment.ContainerID()
}

// AssignToContainer assigns the ship to a container operation.
// Returns a *shared.ShipAlreadyAssignedError if the ship is already assigned to
// another container, or a *shared.ShipReservedByCaptainError if the captain has
// reserved the ship for direct manual use (sp-i1ku) — coordinators must not
// silently steal a captain reservation. The two are distinct types on purpose:
// the already-assigned case can be a transient claim-handoff race that clears
// on a brief retry (sp-ku8e), whereas a captain reservation is a standing
// rejection the caller must honour immediately.
func (s *Ship) AssignToContainer(containerID string, clock shared.Clock) error {
	if s.IsAssigned() {
		if s.assignment.IsCaptainReservation() {
			return shared.NewShipReservedByCaptainError(s.shipSymbol, s.CaptainReservationReason())
		}
		return shared.NewShipAlreadyAssignedError(s.shipSymbol, s.assignment.ContainerID())
	}

	s.assignment = NewActiveAssignment(containerID, clock.Now())
	return nil
}

// ReserveByCaptain reserves the ship for the captain's direct manual use,
// hiding it from every coordinator's assignment discovery. A captain
// reservation is modeled as an active assignment owned by the captain instead
// of a container — it is therefore already invisible to every coordinator
// claim path through the exact same IsAssigned() check they use today
// (AssignToContainer, and any coordinator that mirrors it), so no coordinator
// needs to change (sp-i1ku).
func (s *Ship) ReserveByCaptain(reason string, clock shared.Clock) error {
	if s.IsAssigned() {
		if s.assignment.IsCaptainReservation() {
			return fmt.Errorf("ship %s is already reserved by the captain", s.shipSymbol)
		}
		return fmt.Errorf("ship %s is already assigned to container %s",
			s.shipSymbol, s.assignment.ContainerID())
	}

	s.assignment = NewCaptainReservation(reason, clock.Now())
	return nil
}

// ReleaseCaptainReservation clears a captain reservation, returning the ship
// to idle so normal coordinator discovery can claim it again. Returns a
// ShipNotReservedError if the ship is not currently reserved by the captain
// (sp-i1ku) — release is specifically for captain reservations, not a generic
// "clear any assignment" escape hatch.
func (s *Ship) ReleaseCaptainReservation(reason string, clock shared.Clock) error {
	if !s.IsReservedByCaptain() {
		return shared.NewShipNotReservedError(s.shipSymbol)
	}

	s.assignment = s.assignment.Released(reason, clock.Now())
	return nil
}

// IsReservedByCaptain returns true if the ship's active assignment is a
// captain reservation rather than a container claim (sp-i1ku).
func (s *Ship) IsReservedByCaptain() bool {
	return s.assignment != nil && s.assignment.IsCaptainReservation()
}

// CaptainReservationReason returns the free-text reason given at reserve
// time, or "" if the ship is not captain-reserved or no reason was given
// (sp-i1ku).
func (s *Ship) CaptainReservationReason() string {
	if !s.IsReservedByCaptain() {
		return ""
	}
	if r := s.assignment.ReservationReason(); r != nil {
		return *r
	}
	return ""
}

// Release releases the ship from its current assignment.
// Returns error if ship is not currently assigned.
func (s *Ship) Release(reason string, clock shared.Clock) error {
	if !s.IsAssigned() {
		return fmt.Errorf("ship %s is not assigned to any container", s.shipSymbol)
	}

	s.assignment = s.assignment.Released(reason, clock.Now())
	return nil
}

// ForceRelease forcefully releases the ship regardless of current state.
// Used for cleanup operations (e.g., daemon restart).
func (s *Ship) ForceRelease(reason string, clock shared.Clock) {
	if s.assignment == nil {
		s.assignment = NewIdleAssignment()
		return
	}

	s.assignment = s.assignment.Released(reason, clock.Now())
}

// TransferToContainer transfers the ship to a different container.
// Returns error if ship is not currently assigned.
func (s *Ship) TransferToContainer(newContainerID string, clock shared.Clock) error {
	if !s.IsAssigned() {
		return fmt.Errorf("ship %s is not assigned to any container", s.shipSymbol)
	}

	s.assignment = s.assignment.TransferredTo(newContainerID, clock.Now())
	return nil
}

// SetAssignment sets the ship's assignment state directly.
// Used by repositories when loading from database.
// NOTE: Prefer using AssignToContainer/Release for domain operations.
func (s *Ship) SetAssignment(assignment *ShipAssignment) {
	s.assignment = assignment
}

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

// IsCargoReserved reports whether a cargo good must NOT be sold from this hull by
// any coordinator or the CLI (sp-1vhv). Resolution order, fail-closed:
//  1. If the persisted override state is corrupt/unreadable, EVERY good is treated
//     as reserved — a read failure never converts reserved cargo into sellable
//     manifest (RULINGS #4).
//  2. An explicit per-hull override wins: true = reserved, false = sellable (the
//     deliberate module-resale escape hatch).
//  3. Otherwise the default classification applies: ship hardware
//     (MODULE_*/MOUNT_*) is reserved, everything else is sellable.
func (s *Ship) IsCargoReserved(good string) bool {
	if s.reservationStateCorrupt {
		return true
	}
	if decision, ok := s.reservationOverrides[good]; ok {
		return decision
	}
	return IsDefaultReservedCargo(good)
}

// SetReservationOverrides loads the per-hull override set and corrupt flag from
// persisted state — used by the repository on reconstruct. A corrupt flag makes
// IsCargoReserved fail closed (see there). A nil map clears the override set.
func (s *Ship) SetReservationOverrides(overrides map[string]bool, corrupt bool) {
	s.reservationOverrides = overrides
	s.reservationStateCorrupt = corrupt
}

// ReservationOverrides returns a copy of the per-hull override set
// (good -> reserved decision) for persistence and CLI display. Never nil.
func (s *Ship) ReservationOverrides() map[string]bool {
	out := make(map[string]bool, len(s.reservationOverrides))
	for k, v := range s.reservationOverrides {
		out[k] = v
	}
	return out
}

// ReservationStateCorrupt reports whether this hull's persisted override state
// failed to parse — the fail-closed signal read by IsCargoReserved.
func (s *Ship) ReservationStateCorrupt() bool {
	return s.reservationStateCorrupt
}

// SetCargoReservation sets an explicit per-hull override for a good: reserved=true
// force-protects it, reserved=false force-allows its sale (releasing the default
// module reservation for a deliberate resale). The domain mutation behind the
// `ship reserve-cargo`/`unreserve-cargo` CLI verbs.
func (s *Ship) SetCargoReservation(good string, reserved bool) {
	if s.reservationOverrides == nil {
		s.reservationOverrides = map[string]bool{}
	}
	s.reservationOverrides[good] = reserved
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

// ReconstructShip creates a Ship from persisted state (used by repository)
// This is used when loading a ship from the database.
func ReconstructShip(
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
	flightMode string,
	arrivalTime *time.Time,
	cooldownExpiration *time.Time,
	assignment *ShipAssignment,
	dedicatedFleet string,
	reactorSymbol string,
	reactorName string,
	reactorPowerOutput int,
	reactorRequirements ShipRequirements,
	moduleSlots int,
	mountingPoints int,
	mounts []*ShipMount,
	crewCurrent int,
	crewRequired int,
	crewCapacity int,
) (*Ship, error) {
	s := &Ship{
		shipSymbol:          shipSymbol,
		playerID:            playerID,
		currentLocation:     currentLocation,
		fuel:                fuel,
		fuelCapacity:        fuelCapacity,
		cargoCapacity:       cargoCapacity,
		cargo:               cargo,
		engineSpeed:         engineSpeed,
		frameSymbol:         frameSymbol,
		role:                role,
		modules:             modules,
		navStatus:           navStatus,
		flightMode:          flightMode,
		arrivalTime:         arrivalTime,
		cooldownExpiration:  cooldownExpiration,
		assignment:          assignment,
		dedicatedFleet:      dedicatedFleet,
		reactorSymbol:       reactorSymbol,
		reactorName:         reactorName,
		reactorPowerOutput:  reactorPowerOutput,
		reactorRequirements: reactorRequirements,
		moduleSlots:         moduleSlots,
		mountingPoints:      mountingPoints,
		mounts:              mounts,
		crewCurrent:         crewCurrent,
		crewRequired:        crewRequired,
		crewCapacity:        crewCapacity,
		fuelService:         NewShipFuelService(),
	}

	if err := s.validate(); err != nil {
		return nil, err
	}

	return s, nil
}

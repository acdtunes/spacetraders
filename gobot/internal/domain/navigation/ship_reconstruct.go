package navigation

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// PersistedVersion reports the row version this entity was reconstructed at
// (0 = unknown/API-born). See sp-60ff conflict telemetry.
func (s *Ship) PersistedVersion() int { return s.persistedVersion }

// SetPersistedVersion is called by the persistence layer at reconstruction
// and after a committed save.
func (s *Ship) SetPersistedVersion(v int) { s.persistedVersion = v }

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

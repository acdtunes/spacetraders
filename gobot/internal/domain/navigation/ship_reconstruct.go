package navigation

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// PersistedVersion reports the row version this entity was reconstructed at
// (0 = unknown/API-born).
func (s *Ship) PersistedVersion() int { return s.persistedVersion }

// SetPersistedVersion is called by the persistence layer at reconstruction
// and after a committed save.
func (s *Ship) SetPersistedVersion(v int) { s.persistedVersion = v }

type ShipReconstruction struct {
	ShipSymbol          string
	PlayerID            shared.PlayerID
	CurrentLocation     *shared.Waypoint
	Fuel                *shared.Fuel
	FuelCapacity        int
	CargoCapacity       int
	Cargo               *shared.Cargo
	EngineSpeed         int
	FrameSymbol         string
	Role                string
	Modules             []*ShipModule
	NavStatus           NavStatus
	FlightMode          string
	ArrivalTime         *time.Time
	CooldownExpiration  *time.Time
	Assignment          *ShipAssignment
	DedicatedFleet      string
	ReactorSymbol       string
	ReactorName         string
	ReactorPowerOutput  int
	ReactorRequirements ShipRequirements
	ModuleSlots         int
	MountingPoints      int
	Mounts              []*ShipMount
	CrewCurrent         int
	CrewRequired        int
	CrewCapacity        int
}

func ReconstructShip(d ShipReconstruction) (*Ship, error) {
	s := &Ship{
		shipSymbol:          d.ShipSymbol,
		playerID:            d.PlayerID,
		currentLocation:     d.CurrentLocation,
		fuel:                d.Fuel,
		fuelCapacity:        d.FuelCapacity,
		cargoCapacity:       d.CargoCapacity,
		cargo:               d.Cargo,
		engineSpeed:         d.EngineSpeed,
		frameSymbol:         d.FrameSymbol,
		role:                d.Role,
		modules:             d.Modules,
		navStatus:           d.NavStatus,
		flightMode:          d.FlightMode,
		arrivalTime:         d.ArrivalTime,
		cooldownExpiration:  d.CooldownExpiration,
		assignment:          d.Assignment,
		dedicatedFleet:      d.DedicatedFleet,
		reactorSymbol:       d.ReactorSymbol,
		reactorName:         d.ReactorName,
		reactorPowerOutput:  d.ReactorPowerOutput,
		reactorRequirements: d.ReactorRequirements,
		moduleSlots:         d.ModuleSlots,
		mountingPoints:      d.MountingPoints,
		mounts:              d.Mounts,
		crewCurrent:         d.CrewCurrent,
		crewRequired:        d.CrewRequired,
		crewCapacity:        d.CrewCapacity,
		fuelService:         NewShipFuelService(),
	}

	if err := s.validate(); err != nil {
		return nil, err
	}

	return s, nil
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// modelToAssignment converts DB model to domain value object
func (r *ShipRepository) modelToAssignment(model *persistence.ShipModel) *navigation.ShipAssignment {
	containerID := ""
	if model.ContainerID != nil {
		containerID = *model.ContainerID
	}

	var assignedAt time.Time
	if model.AssignedAt != nil {
		assignedAt = *model.AssignedAt
	}

	// Default to "container" for rows written before the migration
	// backfilled this column.
	owner := navigation.AssignmentOwner(model.AssignmentOwner)
	if owner == "" {
		owner = navigation.AssignmentOwnerContainer
	}

	var reservationReason *string
	if model.AssignmentReason != "" {
		reservationReason = &model.AssignmentReason
	}

	return navigation.ReconstructAssignment(
		containerID,
		navigation.AssignmentStatus(model.AssignmentStatus),
		assignedAt,
		model.ReleasedAt,
		&model.ReleaseReason,
		owner,
		reservationReason,
	)
}

// shipToModel converts ship aggregate to DB model for persistence (full state)
func (r *ShipRepository) shipToModel(ship *navigation.Ship) persistence.ShipModel {
	model := persistence.ShipModel{
		ShipSymbol:       ship.ShipSymbol(),
		PlayerID:         ship.PlayerID().Value(),
		AssignmentStatus: "idle",
		SyncedAt:         r.clock.Now(),
		Version:          ship.PersistedVersion() + 1,
	}

	// Navigation state
	model.NavStatus = string(ship.NavStatus())
	model.FlightMode = ship.FlightMode()
	model.ArrivalTime = ship.ArrivalTime()

	// Nav route origin + departure: persist from the domain ship so a
	// whole-row Save upsert (UpdateAll) does not clobber the synced transit origin
	// back to zero. modelToDomain reloads these onto the ship, so this write is
	// the round-trip's return leg.
	model.OriginSymbol = ship.OriginSymbol()
	model.OriginX = ship.OriginX()
	model.OriginY = ship.OriginY()
	model.DepartureTime = ship.DepartureTime()

	// Location
	if ship.CurrentLocation() != nil {
		model.LocationSymbol = ship.CurrentLocation().Symbol
		model.LocationX = ship.CurrentLocation().X
		model.LocationY = ship.CurrentLocation().Y
		model.SystemSymbol = shared.ExtractSystemSymbol(ship.CurrentLocation().Symbol)
	}

	// Fuel
	if ship.Fuel() != nil {
		model.FuelCurrent = ship.Fuel().Current
		model.FuelCapacity = ship.Fuel().Capacity
	}

	// Cargo
	model.CargoCapacity = ship.CargoCapacity()
	if ship.Cargo() != nil {
		model.CargoUnits = ship.Cargo().Units
		model.CargoInventory = marshalJSONColumn(cargoColumnsFromDomain(ship.Cargo().Inventory))
	}

	// Ship specifications
	model.EngineSpeed = ship.EngineSpeed()
	model.FrameSymbol = ship.FrameSymbol()
	model.Role = ship.Role()

	model.Modules = marshalJSONColumn(moduleColumnsFromDomain(ship.Modules()))
	model.Mounts = marshalJSONColumn(mountColumnsFromDomain(ship.Mounts()))

	applyHullSpecColumns(&model, ship)

	// Cooldown
	model.CooldownExpiration = ship.CooldownExpiration()

	// Dedicated fleet: permanent coordinator reservation, independent
	// of the transient container assignment below.
	model.DedicatedFleet = ship.DedicatedFleet()

	// ReservationOverrides() never returns nil, so an empty set persists as "{}".
	model.ReservationOverrides = marshalJSONColumn(ship.ReservationOverrides())

	model.RetiringAt = ship.RetiringAt()

	model.AssignmentOwner = string(navigation.AssignmentOwnerContainer)
	applyAssignmentColumns(&model, ship.Assignment())

	return model
}

// marshalJSONColumn leaves a column at its zero value rather than persisting a
// corrupt string when encoding fails.
func marshalJSONColumn(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func cargoColumnsFromDomain(inventory []*shared.CargoItem) []persistence.CargoItemJSON {
	cargoItems := make([]persistence.CargoItemJSON, 0)
	for _, item := range inventory {
		cargoItems = append(cargoItems, persistence.CargoItemJSON{
			Symbol:      item.Symbol,
			Name:        item.Name,
			Description: item.Description,
			Units:       item.Units,
		})
	}
	return cargoItems
}

func moduleColumnsFromDomain(modules []*navigation.ShipModule) []persistence.ModuleJSON {
	moduleItems := make([]persistence.ModuleJSON, 0)
	for _, mod := range modules {
		req := mod.Requirements()
		moduleItems = append(moduleItems, persistence.ModuleJSON{
			Symbol:   mod.Symbol(),
			Capacity: mod.Capacity(),
			Range:    mod.Range(),
			Requirements: persistence.RequirementsJSON{
				Power: req.Power(),
				Crew:  req.Crew(),
				Slots: req.Slots(),
			},
		})
	}
	return moduleItems
}

func mountColumnsFromDomain(mounts []*navigation.ShipMount) []persistence.MountJSON {
	mountItems := make([]persistence.MountJSON, 0)
	for _, mnt := range mounts {
		req := mnt.Requirements()
		mountItems = append(mountItems, persistence.MountJSON{
			Symbol:   mnt.Symbol(),
			Name:     mnt.Name(),
			Strength: mnt.Strength(),
			Deposits: mnt.Deposits(),
			Requirements: persistence.RequirementsJSON{
				Power: req.Power(),
				Crew:  req.Crew(),
				Slots: req.Slots(),
			},
		})
	}
	return mountItems
}

// applyHullSpecColumns writes the reactor/slot/crew columns, fixed for the life of the hull.
func applyHullSpecColumns(model *persistence.ShipModel, ship *navigation.Ship) {
	model.ReactorSymbol = ship.ReactorSymbol()
	model.ReactorName = ship.ReactorName()
	model.ReactorPowerOutput = ship.ReactorPowerOutput()
	reactorReq := ship.ReactorRequirements()
	model.ReactorRequirementsPower = reactorReq.Power()
	model.ReactorRequirementsCrew = reactorReq.Crew()
	model.ReactorRequirementsSlots = reactorReq.Slots()
	model.ModuleSlots = ship.ModuleSlots()
	model.MountingPoints = ship.MountingPoints()
	model.CrewCurrent = ship.CrewCurrent()
	model.CrewRequired = ship.CrewRequired()
	model.CrewCapacity = ship.CrewCapacity()
}

func applyAssignmentColumns(model *persistence.ShipModel, assignment *navigation.ShipAssignment) {
	if assignment == nil {
		return
	}

	model.AssignmentStatus = string(assignment.Status())

	if assignment.ContainerID() != "" {
		containerID := assignment.ContainerID()
		model.ContainerID = &containerID
	}

	assignedAt := assignment.AssignedAt()
	if !assignedAt.IsZero() {
		model.AssignedAt = &assignedAt
	}

	if assignment.ReleasedAt() != nil {
		model.ReleasedAt = assignment.ReleasedAt()
	}

	if assignment.ReleaseReason() != nil {
		model.ReleaseReason = *assignment.ReleaseReason()
	}

	// Persist who holds the assignment (container vs captain) and
	// the captain's free-text reservation reason, if any.
	if assignment.Owner() != "" {
		model.AssignmentOwner = string(assignment.Owner())
	}
	if assignment.ReservationReason() != nil {
		model.AssignmentReason = *assignment.ReservationReason()
	}
}

// modelToDomain converts DB model to domain entity
func (r *ShipRepository) modelToDomain(ctx context.Context, model *persistence.ShipModel, playerID shared.PlayerID) (*navigation.Ship, error) {
	// Get full waypoint data including HasFuel from waypoint provider
	// This ensures ships can refuel at locations with fuel stations
	location, err := r.waypointProvider.GetWaypoint(ctx, model.LocationSymbol, model.SystemSymbol, playerID.Value())
	if err != nil {
		// Fallback to denormalized data if waypoint lookup fails
		location = &shared.Waypoint{
			Symbol:       model.LocationSymbol,
			X:            model.LocationX,
			Y:            model.LocationY,
			SystemSymbol: model.SystemSymbol,
		}
	}

	// Create fuel value object from the persisted (API-derived) snapshot,
	// clamping a stored current>capacity to capacity so restart ship-refresh
	// doesn't sideline the hull.
	fuel, err := shared.ReconstructFuel(model.FuelCurrent, model.FuelCapacity)
	if err != nil {
		return nil, fmt.Errorf("failed to create fuel: %w", err)
	}

	cargoItems := cargoItemsFromColumn(model.CargoInventory)

	// Create cargo value object
	cargo, err := shared.NewCargo(model.CargoCapacity, model.CargoUnits, cargoItems)
	if err != nil {
		return nil, fmt.Errorf("failed to create cargo: %w", err)
	}

	modules := modulesFromColumn(model.Modules)
	mounts := mountsFromColumn(model.Mounts)

	// Build assignment from model
	assignment := r.modelToAssignment(model)

	// Reactor requirements
	reactorRequirements := navigation.NewShipRequirements(
		model.ReactorRequirementsPower,
		model.ReactorRequirementsCrew,
		model.ReactorRequirementsSlots,
	)

	// Create ship using reconstruction constructor
	ship, err := navigation.ReconstructShip(navigation.ShipReconstruction{
		ShipSymbol:          model.ShipSymbol,
		PlayerID:            playerID,
		CurrentLocation:     location,
		Fuel:                fuel,
		FuelCapacity:        model.FuelCapacity,
		CargoCapacity:       model.CargoCapacity,
		Cargo:               cargo,
		EngineSpeed:         model.EngineSpeed,
		FrameSymbol:         model.FrameSymbol,
		Role:                model.Role,
		Modules:             modules,
		NavStatus:           navigation.NavStatus(model.NavStatus),
		FlightMode:          model.FlightMode,
		ArrivalTime:         model.ArrivalTime,
		CooldownExpiration:  model.CooldownExpiration,
		Assignment:          assignment,
		DedicatedFleet:      model.DedicatedFleet,
		ReactorSymbol:       model.ReactorSymbol,
		ReactorName:         model.ReactorName,
		ReactorPowerOutput:  model.ReactorPowerOutput,
		ReactorRequirements: reactorRequirements,
		ModuleSlots:         model.ModuleSlots,
		MountingPoints:      model.MountingPoints,
		Mounts:              mounts,
		CrewCurrent:         model.CrewCurrent,
		CrewRequired:        model.CrewRequired,
		CrewCapacity:        model.CrewCapacity,
	})
	if err != nil {
		return nil, err
	}

	// Reservation overrides: load the per-hull cargo do-not-sell set. A
	// malformed column reconstructs the hull with the corrupt flag set, so the
	// domain guard fails CLOSED (treats all cargo as reserved) rather than dropping
	// protections it cannot read.
	overrides, corrupt := parseReservationOverrides(model.ReservationOverrides)
	ship.SetReservationOverrides(overrides, corrupt)

	ship.SetRetiringAt(model.RetiringAt)

	// Nav route origin + departure: reload the persisted transit origin
	// onto the domain ship so it survives a subsequent whole-row Save instead of
	// being clobbered to zero (see shipToModel).
	ship.SetTransitOrigin(model.OriginSymbol, model.OriginX, model.OriginY, model.DepartureTime)

	ship.SetPersistedVersion(model.Version)
	return ship, nil
}

// decodeJSONColumn yields nil for an empty or unreadable column, so a garbled
// value reconstructs as "no rows" instead of failing the whole hull.
func decodeJSONColumn[T any](raw string) []T {
	if raw == "" || raw == "[]" {
		return nil
	}
	var decoded []T
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil
	}
	return decoded
}

func cargoItemsFromColumn(raw string) []*shared.CargoItem {
	var cargoItems []*shared.CargoItem
	for _, item := range decodeJSONColumn[persistence.CargoItemJSON](raw) {
		cargoItem, err := shared.NewCargoItem(item.Symbol, item.Name, item.Description, item.Units)
		if err == nil {
			cargoItems = append(cargoItems, cargoItem)
		}
	}
	return cargoItems
}

func modulesFromColumn(raw string) []*navigation.ShipModule {
	var modules []*navigation.ShipModule
	for _, mod := range decodeJSONColumn[persistence.ModuleJSON](raw) {
		requirements := navigation.NewShipRequirements(mod.Requirements.Power, mod.Requirements.Crew, mod.Requirements.Slots)
		modules = append(modules, navigation.NewShipModule(mod.Symbol, mod.Capacity, mod.Range, requirements))
	}
	return modules
}

func mountsFromColumn(raw string) []*navigation.ShipMount {
	var mounts []*navigation.ShipMount
	for _, mnt := range decodeJSONColumn[persistence.MountJSON](raw) {
		requirements := navigation.NewShipRequirements(mnt.Requirements.Power, mnt.Requirements.Crew, mnt.Requirements.Slots)
		mounts = append(mounts, navigation.NewShipMount(mnt.Symbol, mnt.Name, mnt.Strength, mnt.Deposits, requirements))
	}
	return mounts
}

// parseReservationOverrides decodes the per-hull cargo do-not-sell override JSON.
// Empty/absent/"{}"/"null" is a clean empty set. A malformed value
// returns corrupt=true so the domain guard fails CLOSED (treats all cargo as
// reserved) rather than silently dropping protections a garbled column may hold.
func parseReservationOverrides(raw string) (map[string]bool, bool) {
	if raw == "" || raw == "{}" || raw == "null" {
		return map[string]bool{}, false
	}
	var overrides map[string]bool
	if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
		return nil, true
	}
	if overrides == nil {
		overrides = map[string]bool{}
	}
	return overrides, false
}

func (r *ShipRepository) modelsToShips(ctx context.Context, models []persistence.ShipModel) []*navigation.Ship {
	ships := make([]*navigation.Ship, 0, len(models))
	for _, model := range models {
		playerID, _ := shared.NewPlayerID(model.PlayerID)
		ship, err := r.modelToDomain(ctx, &model, playerID)
		if err != nil {
			continue
		}
		ships = append(ships, ship)
	}
	return ships
}

// Total: every fallible step inside (waypoint lookup, RFC3339 parse, JSON marshal) is
// best-effort and leaves its field zero-valued, so there is no error to report.
func (r *ShipRepository) shipDataToModel(ctx context.Context, data *navigation.ShipData, playerID shared.PlayerID, now time.Time) *persistence.ShipModel {
	model := &persistence.ShipModel{
		ShipSymbol:       data.Symbol,
		PlayerID:         playerID.Value(),
		SyncedAt:         now,
		Version:          1,
		AssignmentStatus: "idle",
	}

	// Navigation state
	model.NavStatus = data.NavStatus
	model.FlightMode = data.FlightMode
	if model.FlightMode == "" {
		model.FlightMode = "CRUISE" // Default fallback
	}

	model.ArrivalTime = parseAPITimestamp(data.ArrivalTime)

	// Nav route origin + departure: carried directly from the API
	// nav.route (not the waypoint provider) so an IN_TRANSIT ship persists where
	// and when its current transit began, letting DB consumers compute exact
	// transit progress. Empty/zero and NULL for a ship that is not in transit.
	model.OriginSymbol = data.OriginSymbol
	model.OriginX = data.OriginX
	model.OriginY = data.OriginY
	model.DepartureTime = parseAPITimestamp(data.DepartureTime)

	model.CooldownExpiration = parseAPITimestamp(data.CooldownExpiration)

	// Location
	model.LocationSymbol = data.Location
	model.SystemSymbol = shared.ExtractSystemSymbol(data.Location)
	// We need to get coordinates from waypoint provider
	if waypoint, err := r.waypointProvider.GetWaypoint(ctx, data.Location, model.SystemSymbol, playerID.Value()); err == nil {
		model.LocationX = waypoint.X
		model.LocationY = waypoint.Y
	}

	// Fuel — clamp a transient API over-report (current>capacity) at the
	// persistence boundary so we never store an invariant-violating row.
	model.FuelCurrent = min(data.FuelCurrent, data.FuelCapacity)
	model.FuelCapacity = data.FuelCapacity

	// Cargo
	model.CargoCapacity = data.CargoCapacity
	model.CargoUnits = data.CargoUnits
	if data.Cargo != nil {
		model.CargoInventory = marshalJSONColumn(cargoColumnsFromAPI(data.Cargo.Inventory))
	}

	// Ship specifications
	model.EngineSpeed = data.EngineSpeed
	model.FrameSymbol = data.FrameSymbol
	model.Role = data.Role

	model.Modules = marshalJSONColumn(moduleColumnsFromAPI(data.Modules))
	model.Mounts = marshalJSONColumn(mountColumnsFromAPI(data.Mounts))

	applyAPIHullSpecColumns(model, data)

	return model
}

// parseAPITimestamp yields nil for an absent or unparseable value, leaving the
// column NULL rather than stamping a wrong time.
func parseAPITimestamp(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &parsed
}

func cargoColumnsFromAPI(inventory []shared.CargoItem) []persistence.CargoItemJSON {
	cargoItems := make([]persistence.CargoItemJSON, 0)
	for _, item := range inventory {
		cargoItems = append(cargoItems, persistence.CargoItemJSON{
			Symbol:      item.Symbol,
			Name:        item.Name,
			Description: item.Description,
			Units:       item.Units,
		})
	}
	return cargoItems
}

func moduleColumnsFromAPI(modules []navigation.ModuleData) []persistence.ModuleJSON {
	moduleItems := make([]persistence.ModuleJSON, 0)
	for _, mod := range modules {
		moduleItems = append(moduleItems, persistence.ModuleJSON{
			Symbol:   mod.Symbol,
			Capacity: mod.Capacity,
			Range:    mod.Range,
			Requirements: persistence.RequirementsJSON{
				Power: mod.Requirements.Power,
				Crew:  mod.Requirements.Crew,
				Slots: mod.Requirements.Slots,
			},
		})
	}
	return moduleItems
}

func mountColumnsFromAPI(mounts []navigation.MountData) []persistence.MountJSON {
	mountItems := make([]persistence.MountJSON, 0)
	for _, mnt := range mounts {
		mountItems = append(mountItems, persistence.MountJSON{
			Symbol:   mnt.Symbol,
			Name:     mnt.Name,
			Strength: mnt.Strength,
			Deposits: mnt.Deposits,
			Requirements: persistence.RequirementsJSON{
				Power: mnt.Requirements.Power,
				Crew:  mnt.Requirements.Crew,
				Slots: mnt.Requirements.Slots,
			},
		})
	}
	return mountItems
}

// applyAPIHullSpecColumns writes the reactor/slot/crew columns, fixed for the life of the hull.
func applyAPIHullSpecColumns(model *persistence.ShipModel, data *navigation.ShipData) {
	model.ReactorSymbol = data.ReactorSymbol
	model.ReactorName = data.ReactorName
	model.ReactorPowerOutput = data.ReactorPowerOutput
	model.ReactorRequirementsPower = data.ReactorRequirements.Power
	model.ReactorRequirementsCrew = data.ReactorRequirements.Crew
	model.ReactorRequirementsSlots = data.ReactorRequirements.Slots
	model.ModuleSlots = data.ModuleSlots
	model.MountingPoints = data.MountingPoints
	model.CrewCurrent = data.CrewCurrent
	model.CrewRequired = data.CrewRequired
	model.CrewCapacity = data.CrewCapacity
}

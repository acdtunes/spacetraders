package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// shipListCacheTTL defines how long ship list cache is valid
// 15 seconds is enough to prevent redundant calls across coordinators
// while still allowing fresh data for navigation decisions
const shipListCacheTTL = 15 * time.Second

// cachedShipList stores ship list with timestamp for TTL expiration
type cachedShipList struct {
	ships     []*navigation.Ship
	fetchedAt time.Time
}

// ShipRepository implements ShipRepository using the SpaceTraders API + Database
// After daemon startup, the database is the source of truth for ship state.
// Ships are synced from API on startup, and all queries read from the database.
// API calls are only made for state-changing operations (navigate, dock, orbit, refuel, cargo).
//
// Caching Strategy:
//   - In-memory cache (15s TTL): Prevents redundant DB reads
//     when multiple coordinators call FindAllByPlayer in quick succession
type ShipRepository struct {
	apiClient        domainPorts.APIClient
	playerRepo       player.PlayerRepository
	waypointRepo     system.WaypointRepository
	waypointProvider system.IWaypointProvider
	db               *gorm.DB     // Database connection for ship state persistence
	clock            shared.Clock // Clock for timestamps
	shipListCache    sync.Map     // key: playerID (int) -> *cachedShipList

	// Optional arrival scheduler - notified after navigation to schedule state transition
	arrivalScheduler navigation.ArrivalScheduler
}

// NewShipRepository creates a new hybrid API+DB ship repository
func NewShipRepository(
	apiClient domainPorts.APIClient,
	playerRepo player.PlayerRepository,
	waypointRepo system.WaypointRepository,
	waypointProvider system.IWaypointProvider,
	db *gorm.DB,
	clock shared.Clock,
) *ShipRepository {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &ShipRepository{
		apiClient:        apiClient,
		playerRepo:       playerRepo,
		waypointRepo:     waypointRepo,
		waypointProvider: waypointProvider,
		db:               db,
		clock:            clock,
	}
}

// SetArrivalScheduler sets the scheduler that will be notified after navigation
// to schedule arrival state transitions. This uses setter injection to avoid
// circular dependencies during construction.
func (r *ShipRepository) SetArrivalScheduler(scheduler navigation.ArrivalScheduler) {
	r.arrivalScheduler = scheduler
}

// FindBySymbol retrieves a ship by symbol and player ID from database.
// If not found in DB, syncs from API first.
// Database is the source of truth after daemon startup.
func (r *ShipRepository) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	var model persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("ship_symbol = ? AND player_id = ?", symbol, playerID.Value()).
		First(&model).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Ship not in DB - might be newly purchased, sync from API
		return r.SyncShipFromAPI(ctx, symbol, playerID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query ship: %w", err)
	}

	return r.modelToDomain(ctx, &model, playerID)
}

// GetShipData retrieves raw ship data from API (includes arrival time for IN_TRANSIT ships)
func (r *ShipRepository) GetShipData(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.ShipData, error) {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Fetch ship data from API (includes ArrivalTime for IN_TRANSIT ships)
	shipData, err := r.apiClient.GetShip(ctx, symbol, player.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship from API: %w", err)
	}

	return shipData, nil
}

// FindAllByPlayer retrieves all ships for a player from database with short-lived caching.
// Database is the source of truth after daemon startup.
//
// Caching: Returns cached ship list if within 15 seconds of last fetch.
// This prevents redundant DB reads when multiple coordinators call this method.
func (r *ShipRepository) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	cacheKey := playerID.Value()

	// Check cache first
	if cached, ok := r.shipListCache.Load(cacheKey); ok {
		cachedList := cached.(*cachedShipList)
		if time.Since(cachedList.fetchedAt) < shipListCacheTTL {
			// Return a copy to prevent mutation of cached data
			shipsCopy := make([]*navigation.Ship, len(cachedList.ships))
			copy(shipsCopy, cachedList.ships)
			return shipsCopy, nil
		}
	}

	// Fetch all ships from database
	var models []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID.Value()).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query ships: %w", err)
	}

	// Convert DB models to domain entities
	ships := make([]*navigation.Ship, 0, len(models))
	for _, model := range models {
		ship, err := r.modelToDomain(ctx, &model, playerID)
		if err != nil {
			log.Printf("Warning: failed to convert ship %s: %v", model.ShipSymbol, err)
			continue
		}
		ships = append(ships, ship)
	}

	// Cache the result
	r.shipListCache.Store(cacheKey, &cachedShipList{
		ships:     ships,
		fetchedAt: r.clock.Now(),
	})

	return ships, nil
}

// FindBySymbolCached retrieves a ship from the cached ship list if available,
// otherwise falls back to a direct DB query.
//
// OPTIMIZATION: When selecting ships from a known list (e.g., idle haulers),
// use this method to avoid N individual DB queries. The cached list is refreshed
// every 15 seconds via FindAllByPlayer.
//
// Use cases:
//   - Ship selection loops (SelectClosestShip, RebalanceFleet)
//   - Any code that iterates through ship symbols to load ship data
//
// Falls back to FindBySymbol (direct DB query) if:
//   - Ship not found in cache
//   - Cache is stale or empty
func (r *ShipRepository) FindBySymbolCached(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	// First try to find in cached ship list
	allShips, err := r.FindAllByPlayer(ctx, playerID)
	if err != nil {
		// Cache miss/error - fall back to direct API call
		return r.FindBySymbol(ctx, symbol, playerID)
	}

	// Search for ship in cached list
	for _, ship := range allShips {
		if ship.ShipSymbol() == symbol {
			return ship, nil
		}
	}

	// Not found in cache - this could mean:
	// 1. Ship was just purchased and cache is stale
	// 2. Ship symbol is wrong
	// Fall back to direct API call for definitive answer
	return r.FindBySymbol(ctx, symbol, playerID)
}

// FindManyBySymbolsCached retrieves multiple ships from the cached ship list.
//
// OPTIMIZATION: Replaces loops that call FindBySymbol for each ship.
// Instead of N API calls, this uses a single cached FindAllByPlayer call
// and filters in memory.
//
// Returns ships in the same order as requested symbols.
// Missing ships are omitted from the result (no error).
func (r *ShipRepository) FindManyBySymbolsCached(ctx context.Context, symbols []string, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	if len(symbols) == 0 {
		return nil, nil
	}

	// Get all ships from cache
	allShips, err := r.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ships: %w", err)
	}

	// Build lookup map for efficient searching
	shipMap := make(map[string]*navigation.Ship, len(allShips))
	for _, ship := range allShips {
		shipMap[ship.ShipSymbol()] = ship
	}

	// Collect ships in requested order
	result := make([]*navigation.Ship, 0, len(symbols))
	for _, symbol := range symbols {
		if ship, found := shipMap[symbol]; found {
			result = append(result, ship)
		}
	}

	return result, nil
}

// Navigate executes ship navigation via API and persists state to database.
// Returns navigation result with arrival time from API.
func (r *ShipRepository) Navigate(ctx context.Context, ship *navigation.Ship, destination *shared.Waypoint, playerID shared.PlayerID) (*navigation.Result, error) {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to navigate ship
	navResult, err := r.apiClient.NavigateShip(ctx, ship.ShipSymbol(), destination.Symbol, player.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate ship: %w", err)
	}

	// Update ship domain entity state from API response
	if err := ship.StartTransit(destination); err != nil {
		return nil, fmt.Errorf("failed to update ship state: %w", err)
	}

	// Consume fuel based on API response
	if err := ship.ConsumeFuel(navResult.FuelConsumed); err != nil {
		return nil, fmt.Errorf("failed to consume fuel: %w", err)
	}

	// Set flight mode from result
	if navResult.FlightMode != "" {
		ship.SetFlightMode(navResult.FlightMode)
	}

	// Set arrival time from API response
	if navResult.ArrivalTimeStr != "" {
		if arrivalTime, err := time.Parse(time.RFC3339, navResult.ArrivalTimeStr); err == nil {
			ship.SetArrivalTime(arrivalTime)
		}
	}

	// Persist state to database
	if err := r.Save(ctx, ship); err != nil {
		log.Printf("Warning: failed to persist ship %s after navigate: %v", ship.ShipSymbol(), err)
	}

	// Invalidate cache for this player
	r.shipListCache.Delete(playerID.Value())

	// Notify arrival scheduler to set up transition timer
	if r.arrivalScheduler != nil {
		r.arrivalScheduler.ScheduleArrival(ship)
	}

	return navResult, nil
}

// Dock docks the ship via API (idempotent) and persists state to database.
func (r *ShipRepository) Dock(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to dock ship (API itself is idempotent - will succeed if already docked)
	if err := r.apiClient.DockShip(ctx, ship.ShipSymbol(), player.Token); err != nil {
		// Check if error is because ship is already docked (API returns specific error)
		// If so, this is idempotent behavior - not an error
		if !isAlreadyDockedError(err) {
			return fmt.Errorf("failed to dock ship: %w", err)
		}
	}

	// Update ship domain entity state using idempotent method
	if _, err := ship.EnsureDocked(); err != nil {
		return fmt.Errorf("failed to update ship state: %w", err)
	}

	// Persist state to database
	if err := r.Save(ctx, ship); err != nil {
		log.Printf("Warning: failed to persist ship %s after dock: %v", ship.ShipSymbol(), err)
	}

	// Invalidate cache for this player
	r.shipListCache.Delete(playerID.Value())

	return nil
}

// Orbit puts ship in orbit via API (idempotent) and persists state to database.
func (r *ShipRepository) Orbit(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to orbit ship (API itself is idempotent - will succeed if already in orbit)
	if err := r.apiClient.OrbitShip(ctx, ship.ShipSymbol(), player.Token); err != nil {
		// Check if error is because ship is already in orbit (API returns specific error)
		// If so, this is idempotent behavior - not an error
		if !isAlreadyInOrbitError(err) {
			return fmt.Errorf("failed to orbit ship: %w", err)
		}
	}

	// Update ship domain entity state using idempotent method
	if _, err := ship.EnsureInOrbit(); err != nil {
		return fmt.Errorf("failed to update ship state: %w", err)
	}

	// Clear arrival time when ship arrives in orbit
	ship.ClearArrivalTime()

	// Persist state to database
	if err := r.Save(ctx, ship); err != nil {
		log.Printf("Warning: failed to persist ship %s after orbit: %v", ship.ShipSymbol(), err)
	}

	// Invalidate cache for this player
	r.shipListCache.Delete(playerID.Value())

	return nil
}

// Refuel refuels the ship via API and persists state to database.
func (r *ShipRepository) Refuel(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID, units *int) (*navigation.RefuelResult, error) {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to refuel ship
	refuelResult, err := r.apiClient.RefuelShip(ctx, ship.ShipSymbol(), player.Token, units)
	if err != nil {
		return nil, fmt.Errorf("failed to refuel ship: %w", err)
	}

	// Update ship domain entity state
	// If units specified, add that amount, otherwise refuel to full
	if units != nil {
		// Add specific amount
		if err := ship.Refuel(*units); err != nil {
			return nil, fmt.Errorf("failed to update ship fuel: %w", err)
		}
	} else {
		// Refuel to full
		if _, err := ship.RefuelToFull(); err != nil {
			return nil, fmt.Errorf("failed to update ship fuel: %w", err)
		}
	}

	// Persist state to database
	if err := r.Save(ctx, ship); err != nil {
		log.Printf("Warning: failed to persist ship %s after refuel: %v", ship.ShipSymbol(), err)
	}

	// Invalidate cache for this player
	r.shipListCache.Delete(playerID.Value())

	return refuelResult, nil
}

// SetFlightMode sets the ship's flight mode via API and persists state to database.
func (r *ShipRepository) SetFlightMode(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID, mode string) error {
	// Skip if already set
	if ship.FlightMode() == mode {
		return nil
	}

	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to set flight mode
	if err := r.apiClient.SetFlightMode(ctx, ship.ShipSymbol(), mode, player.Token); err != nil {
		return fmt.Errorf("failed to set flight mode: %w", err)
	}

	// Update domain entity
	ship.SetFlightMode(mode)

	// Persist state to database
	if err := r.Save(ctx, ship); err != nil {
		log.Printf("Warning: failed to persist ship %s after set flight mode: %v", ship.ShipSymbol(), err)
	}

	// Invalidate cache for this player
	r.shipListCache.Delete(playerID.Value())

	return nil
}

// JettisonCargo jettisons cargo from the ship via API
func (r *ShipRepository) JettisonCargo(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID, goodSymbol string, units int) error {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to jettison cargo
	if err := r.apiClient.JettisonCargo(ctx, ship.ShipSymbol(), goodSymbol, units, player.Token); err != nil {
		return fmt.Errorf("failed to jettison cargo: %w", err)
	}

	// Note: Cargo is updated by the API, and we refetch ship state when needed
	// No need to update the domain entity here

	return nil
}

// shipDataToDomain converts API ship DTO to domain entity
func (r *ShipRepository) shipDataToDomain(ctx context.Context, data *navigation.ShipData, playerID shared.PlayerID) (*navigation.Ship, error) {
	// Get current location waypoint (auto-fetches from API if not cached)
	// Extract system symbol using domain function
	systemSymbol := shared.ExtractSystemSymbol(data.Location)
	location, err := r.waypointProvider.GetWaypoint(ctx, data.Location, systemSymbol, playerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to get location waypoint %s: %w", data.Location, err)
	}

	// Create fuel value object from the authoritative API snapshot, clamping a
	// transient current>capacity over-report to capacity so it doesn't sideline
	// the hull (sp-xxhn).
	fuel, err := shared.ReconstructFuel(data.FuelCurrent, data.FuelCapacity)
	if err != nil {
		return nil, fmt.Errorf("failed to create fuel: %w", err)
	}

	// Convert cargo items
	var cargoItems []*shared.CargoItem
	if data.Cargo != nil {
		for _, item := range data.Cargo.Inventory {
			cargoItem, err := shared.NewCargoItem(item.Symbol, item.Name, item.Description, item.Units)
			if err != nil {
				return nil, fmt.Errorf("failed to create cargo item: %w", err)
			}
			cargoItems = append(cargoItems, cargoItem)
		}
	}

	// Create cargo value object
	cargo, err := shared.NewCargo(data.CargoCapacity, data.CargoUnits, cargoItems)
	if err != nil {
		return nil, fmt.Errorf("failed to create cargo: %w", err)
	}

	// Convert modules
	var modules []*navigation.ShipModule
	for _, mod := range data.Modules {
		requirements := navigation.NewShipRequirements(mod.Requirements.Power, mod.Requirements.Crew, mod.Requirements.Slots)
		module := navigation.NewShipModule(mod.Symbol, mod.Capacity, mod.Range, requirements)
		modules = append(modules, module)
	}

	// Convert nav status
	navStatus := navigation.NavStatus(data.NavStatus)

	// Create ship domain entity
	ship, err := navigation.NewShip(
		data.Symbol,
		playerID,
		location,
		fuel,
		data.FuelCapacity,
		data.CargoCapacity,
		cargo,
		data.EngineSpeed,
		data.FrameSymbol,
		data.Role,
		modules,
		navStatus,
	)
	if err != nil {
		return nil, err
	}

	// Enrich with power/slot/crew data (sp-el60) so a ship built straight
	// from a fresh API payload is immediately outfit-feasibility computable.
	mounts := make([]*navigation.ShipMount, len(data.Mounts))
	for i, mnt := range data.Mounts {
		mountRequirements := navigation.NewShipRequirements(mnt.Requirements.Power, mnt.Requirements.Crew, mnt.Requirements.Slots)
		mounts[i] = navigation.NewShipMount(mnt.Symbol, mnt.Name, mnt.Strength, mnt.Deposits, mountRequirements)
	}
	ship.SetMounts(mounts)
	ship.SetSlots(data.ModuleSlots, data.MountingPoints)
	reactorRequirements := navigation.NewShipRequirements(
		data.ReactorRequirements.Power,
		data.ReactorRequirements.Crew,
		data.ReactorRequirements.Slots,
	)
	ship.SetReactor(data.ReactorSymbol, data.ReactorName, data.ReactorPowerOutput, reactorRequirements)
	ship.SetCrew(data.CrewCurrent, data.CrewRequired, data.CrewCapacity)

	return ship, nil
}

// isAlreadyDockedError checks if the error is due to ship already being docked
// API returns error code 4237 when trying to dock an already docked ship
func isAlreadyDockedError(err error) bool {
	if err == nil {
		return false
	}
	// Check if error message contains indication that ship is already docked
	errMsg := err.Error()
	return strings.Contains(errMsg, "already docked") || strings.Contains(errMsg, "4237")
}

// isAlreadyInOrbitError checks if the error is due to ship already being in orbit
// API returns error code when trying to orbit an already orbiting ship
//
// sp-423c: the old fallback also matched the bare substring "in orbit", which
// "already in orbit" is trivially a superset of (making the fallback both
// redundant for the true positive AND a false-positive risk for any other
// real API error that merely mentions orbit as a precondition, e.g. "Ship
// must be in orbit to jettison cargo"). Orbit() (ship_repository.go) treats a
// true result as "not a real error, proceed as success", so the broad match
// could have silently swallowed a genuine failure - exactly the class of
// real-API-contract mismatch this gate exists to catch.
func isAlreadyInOrbitError(err error) bool {
	if err == nil {
		return false
	}
	// Check if error message contains indication that ship is already in orbit
	errMsg := err.Error()
	return strings.Contains(errMsg, "already in orbit")
}

// =============================================================================
// Assignment Query & Persistence Methods (DB operations)
// =============================================================================

// enrichWithAssignment loads assignment data for a single ship from DB
func (r *ShipRepository) enrichWithAssignment(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	if r.db == nil {
		return nil // DB not configured, skip enrichment
	}

	var model persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("ship_symbol = ? AND player_id = ?", ship.ShipSymbol(), playerID.Value()).
		First(&model).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil // No assignment exists
	}
	if err != nil {
		return err
	}

	assignment := r.modelToAssignment(&model)
	ship.SetAssignment(assignment)
	return nil
}

// batchEnrichWithAssignments loads assignments for multiple ships in one query
func (r *ShipRepository) batchEnrichWithAssignments(ctx context.Context, ships []*navigation.Ship, playerID shared.PlayerID) error {
	if r.db == nil || len(ships) == 0 {
		return nil
	}

	symbols := make([]string, len(ships))
	for i, s := range ships {
		symbols[i] = s.ShipSymbol()
	}

	var models []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("ship_symbol IN ? AND player_id = ?", symbols, playerID.Value()).
		Find(&models).Error
	if err != nil {
		return err
	}

	// Build lookup map
	assignmentMap := make(map[string]*navigation.ShipAssignment)
	for i := range models {
		assignmentMap[models[i].ShipSymbol] = r.modelToAssignment(&models[i])
	}

	// Enrich ships
	for _, ship := range ships {
		if assignment, ok := assignmentMap[ship.ShipSymbol()]; ok {
			ship.SetAssignment(assignment)
		}
	}

	return nil
}

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

	// Default to "container" for rows written before sp-i1ku's migration
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
		Version:          1,
	}

	// Navigation state
	model.NavStatus = string(ship.NavStatus())
	model.FlightMode = ship.FlightMode()
	model.ArrivalTime = ship.ArrivalTime()

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
		cargoItems := make([]persistence.CargoItemJSON, 0)
		for _, item := range ship.Cargo().Inventory {
			cargoItems = append(cargoItems, persistence.CargoItemJSON{
				Symbol:      item.Symbol,
				Name:        item.Name,
				Description: item.Description,
				Units:       item.Units,
			})
		}
		if cargoJSON, err := json.Marshal(cargoItems); err == nil {
			model.CargoInventory = string(cargoJSON)
		}
	}

	// Ship specifications
	model.EngineSpeed = ship.EngineSpeed()
	model.FrameSymbol = ship.FrameSymbol()
	model.Role = ship.Role()

	// Modules
	moduleItems := make([]persistence.ModuleJSON, 0)
	for _, mod := range ship.Modules() {
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
	if modulesJSON, err := json.Marshal(moduleItems); err == nil {
		model.Modules = string(modulesJSON)
	}

	// Mounts (sp-el60)
	mountItems := make([]persistence.MountJSON, 0)
	for _, mnt := range ship.Mounts() {
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
	if mountsJSON, err := json.Marshal(mountItems); err == nil {
		model.Mounts = string(mountsJSON)
	}

	// Reactor/slots/crew (sp-el60): fixed for the life of the hull.
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

	// Cooldown
	model.CooldownExpiration = ship.CooldownExpiration()

	// Dedicated fleet (sp-snmb): permanent coordinator reservation, independent
	// of the transient container assignment below.
	model.DedicatedFleet = ship.DedicatedFleet()

	// Reservation overrides (sp-1vhv): the per-hull cargo do-not-sell override set.
	// A marshal failure leaves the column at its zero value rather than persisting a
	// corrupt string; ReservationOverrides() never returns nil, so this is "{}" for
	// an empty set.
	if overridesJSON, err := json.Marshal(ship.ReservationOverrides()); err == nil {
		model.ReservationOverrides = string(overridesJSON)
	}

	// Assignment
	model.AssignmentOwner = string(navigation.AssignmentOwnerContainer)
	if ship.Assignment() != nil {
		assignment := ship.Assignment()
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

		// sp-i1ku: persist who holds the assignment (container vs captain) and
		// the captain's free-text reservation reason, if any.
		if assignment.Owner() != "" {
			model.AssignmentOwner = string(assignment.Owner())
		}
		if assignment.ReservationReason() != nil {
			model.AssignmentReason = *assignment.ReservationReason()
		}
	}

	return model
}

// Save persists ship aggregate state (including full state) to DB
func (r *ShipRepository) Save(ctx context.Context, ship *navigation.Ship) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	model := r.shipToModel(ship)
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			UpdateAll: true,
		}).
		Create(&model).Error

	if err == nil {
		// Invalidate cache to ensure assignment changes are immediately visible
		// This prevents stale assignment data from causing ships to be incorrectly
		// seen as idle when they've been assigned to containers (e.g., storage ships)
		r.shipListCache.Delete(ship.PlayerID().Value())
	}

	return err
}

// SaveAll batch persists multiple ship aggregates
func (r *ShipRepository) SaveAll(ctx context.Context, ships []*navigation.Ship) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}
	if len(ships) == 0 {
		return nil
	}

	models := make([]persistence.ShipModel, len(ships))
	playerIDs := make(map[int]bool)
	for i, ship := range ships {
		models[i] = r.shipToModel(ship)
		playerIDs[ship.PlayerID().Value()] = true
	}

	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			UpdateAll: true,
		}).
		Create(&models).Error

	if err == nil {
		// Invalidate cache for all affected players
		for playerID := range playerIDs {
			r.shipListCache.Delete(playerID)
		}
	}

	return err
}

// FindByContainer retrieves all ships assigned to a specific container
func (r *ShipRepository) FindByContainer(ctx context.Context, containerID string, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	// Get all ships for player
	allShips, err := r.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, err
	}

	// Filter by container
	var result []*navigation.Ship
	for _, ship := range allShips {
		if ship.ContainerID() == containerID {
			result = append(result, ship)
		}
	}

	return result, nil
}

// FindIdleByPlayer retrieves all idle (unassigned) ships for a player
func (r *ShipRepository) FindIdleByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	// Get all ships for player
	allShips, err := r.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, err
	}

	// Filter idle ships
	var result []*navigation.Ship
	for _, ship := range allShips {
		if ship.IsIdle() {
			result = append(result, ship)
		}
	}

	return result, nil
}

// FindActiveByPlayer retrieves all actively assigned ships for a player
func (r *ShipRepository) FindActiveByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	// Get all ships for player
	allShips, err := r.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, err
	}

	// Filter assigned ships
	var result []*navigation.Ship
	for _, ship := range allShips {
		if ship.IsAssigned() {
			result = append(result, ship)
		}
	}

	return result, nil
}

// CountByContainerPrefix counts active assignments where container ID starts with prefix
func (r *ShipRepository) CountByContainerPrefix(ctx context.Context, prefix string, playerID shared.PlayerID) (int, error) {
	if r.db == nil {
		return 0, fmt.Errorf("database not configured")
	}

	var count int64
	err := r.db.WithContext(ctx).
		Model(&persistence.ShipModel{}).
		Where("container_id LIKE ?", prefix+"%").
		Where("player_id = ?", playerID.Value()).
		Where("assignment_status = ?", "active").
		Count(&count).Error

	if err != nil {
		return 0, fmt.Errorf("failed to count assignments by prefix: %w", err)
	}

	return int(count), nil
}

// ReleaseAllActive releases all active ship assignments for the given player (bulk operation)
// Used during daemon startup to clean up zombie assignments from previous runs.
//
// Captain reservations (assignment_owner="captain") are deliberately excluded:
// they use the same assignment_status="active" as a live coordinator claim, but
// a reservation's whole purpose (sp-i1ku) is to survive daemon restarts, so an
// owner-blind release here would silently un-reserve a captain-held hull on
// every restart.
func (r *ShipRepository) ReleaseAllActive(ctx context.Context, playerID shared.PlayerID, reason string) (int, error) {
	if r.db == nil {
		return 0, fmt.Errorf("database not configured")
	}

	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&persistence.ShipModel{}).
		Where("player_id = ?", playerID.Value()).
		Where("assignment_status = ?", "active").
		Where("assignment_owner IS NULL OR assignment_owner != ?", string(navigation.AssignmentOwnerCaptain)).
		Updates(map[string]interface{}{
			"assignment_status": "idle",
			"container_id":      nil,
			"released_at":       now,
			"release_reason":    reason,
		})

	if result.Error != nil {
		return 0, fmt.Errorf("failed to release all active assignments: %w", result.Error)
	}

	return int(result.RowsAffected), nil
}

// ClaimShip exclusively assigns an idle ship to a container using row-level locking.
// Returns ShipAlreadyAssignedError if ship is already assigned to another container.
//
// operation is the claiming coordinator's fleet identity ("contract",
// "manufacturing", ...). A free hull whose DedicatedFleet tag names a
// different fleet is rejected with ShipDedicatedToOtherFleetError — inside
// the same locked transaction as the other guards, so a claim racing a
// concurrent `fleet assign` cannot slip through on stale discovery data
// (sp-l7h2, layer 2; the FindIdleLightHaulers exclude filter is layer 1).
func (r *ShipRepository) ClaimShip(ctx context.Context, shipSymbol string, containerID string, playerID shared.PlayerID, operation string) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model persistence.ShipModel

		// Lock the row with SELECT FOR UPDATE to prevent race conditions
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("ship_symbol = ? AND player_id = ?", shipSymbol, playerID.Value()).
			First(&model).Error

		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("ship %s not found for player %d", shipSymbol, playerID.Value())
		}
		if err != nil {
			return fmt.Errorf("failed to lock ship: %w", err)
		}

		// sp-i1ku: a captain reservation has no container_id (it was never a
		// container claim), so it would otherwise fall through both of the
		// container-comparison guards below and get silently overwritten by
		// the unconditional assign-to-container update. Reject it explicitly,
		// before either guard runs.
		if model.AssignmentStatus == "active" && model.AssignmentOwner == string(navigation.AssignmentOwnerCaptain) {
			return shared.NewShipReservedByCaptainError(shipSymbol, model.AssignmentReason)
		}

		// Check if already assigned to another container
		if model.AssignmentStatus == "active" && model.ContainerID != nil && *model.ContainerID != containerID {
			return shared.NewShipAlreadyAssignedError(shipSymbol, *model.ContainerID)
		}

		// Already assigned to this container - idempotent success. Checked
		// BEFORE the dedication guard on purpose: dedication is ownership of
		// the NEXT acquisition, not eviction of the current holder (sp-l7h2).
		// A worker re-claiming its own hull mid-job (crash recovery) must keep
		// it even if the captain re-dedicated the ship while the job ran — the
		// new fleet takes over when this claim is released, not by yanking a
		// hull out from under a running operation.
		if model.AssignmentStatus == "active" && model.ContainerID != nil && *model.ContainerID == containerID {
			return nil
		}

		// sp-l7h2: the hull is free — a NEW acquisition. A dedicated ship may
		// only be newly claimed by its own fleet's operation. Symmetric to the
		// captain-reservation guard above, and atomic with the assignment
		// write below: the discovery-time exclude filter alone has a TOCTOU
		// window between a coordinator's read and this write.
		if model.DedicatedFleet != "" && model.DedicatedFleet != operation {
			return shared.NewShipDedicatedToOtherFleetError(shipSymbol, model.DedicatedFleet, operation)
		}

		// Assign ship to container
		now := r.clock.Now()
		err = tx.Model(&model).Updates(map[string]interface{}{
			"container_id":      containerID,
			"assignment_status": "active",
			"assigned_at":       now,
			"released_at":       nil,
			"release_reason":    "",
			"assignment_owner":  string(navigation.AssignmentOwnerContainer),
			"assignment_reason": "",
		}).Error

		if err != nil {
			return fmt.Errorf("failed to assign ship: %w", err)
		}

		// Invalidate cache since assignment changed
		r.shipListCache.Delete(playerID.Value())

		return nil
	})
}

// ReserveForCaptain atomically reserves an idle ship for the captain's direct,
// manual use, using the same row-level locking as ClaimShip so a concurrent
// coordinator claim can never be silently overwritten by a captain reservation,
// or vice versa (sp-i1ku). This is the exact claim-race class the bead exists to
// kill, applied to the write path: a plain FindBySymbol + Save read-modify-write
// would have a TOCTOU window where a coordinator's ClaimShip could commit between
// the read and the write, and the reservation's Save (a full-row upsert) would
// silently clobber it. Returns ShipAlreadyAssignedError if a container already
// holds the claim.
func (r *ShipRepository) ReserveForCaptain(ctx context.Context, shipSymbol string, reason string, playerID shared.PlayerID) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model persistence.ShipModel

		// Lock the row with SELECT FOR UPDATE to prevent race conditions
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("ship_symbol = ? AND player_id = ?", shipSymbol, playerID.Value()).
			First(&model).Error

		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("ship %s not found for player %d", shipSymbol, playerID.Value())
		}
		if err != nil {
			return fmt.Errorf("failed to lock ship: %w", err)
		}

		// Already reserved by the captain - reject rather than silently update the
		// reason. Mirrors Ship.ReserveByCaptain's domain rule: change the reason via
		// release + reserve, so `ship reserve`'s CLI output always means "this just
		// took effect," never a possible no-op.
		if model.AssignmentStatus == "active" && model.AssignmentOwner == string(navigation.AssignmentOwnerCaptain) {
			return fmt.Errorf("ship %s is already reserved by the captain", shipSymbol)
		}

		// Held by a container - reject. The captain must let the coordinator
		// release it first, never silently steal an active claim out from under a
		// running worker.
		if model.AssignmentStatus == "active" && model.ContainerID != nil {
			return shared.NewShipAlreadyAssignedError(shipSymbol, *model.ContainerID)
		}

		// Reserve for the captain
		now := r.clock.Now()
		err = tx.Model(&model).Updates(map[string]interface{}{
			"container_id":      nil,
			"assignment_status": "active",
			"assigned_at":       now,
			"released_at":       nil,
			"release_reason":    "",
			"assignment_owner":  string(navigation.AssignmentOwnerCaptain),
			"assignment_reason": reason,
		}).Error

		if err != nil {
			return fmt.Errorf("failed to reserve ship: %w", err)
		}

		// Invalidate cache since assignment changed
		r.shipListCache.Delete(playerID.Value())

		return nil
	})
}

// ReleaseCaptainReservation atomically clears a captain reservation, returning
// the ship to idle so normal coordinator discovery can claim it again. Uses the
// same row-level locking as ClaimShip/ReserveForCaptain (sp-i1ku).
// Returns ShipNotReservedError if the ship is not currently reserved by the
// captain — release is specifically for captain reservations, not a generic
// "clear any assignment" escape hatch (that already exists as ReleaseAllActive /
// ForceRelease for the reconciliation path).
func (r *ShipRepository) ReleaseCaptainReservation(ctx context.Context, shipSymbol string, reason string, playerID shared.PlayerID) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model persistence.ShipModel

		// Lock the row with SELECT FOR UPDATE to prevent race conditions
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("ship_symbol = ? AND player_id = ?", shipSymbol, playerID.Value()).
			First(&model).Error

		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("ship %s not found for player %d", shipSymbol, playerID.Value())
		}
		if err != nil {
			return fmt.Errorf("failed to lock ship: %w", err)
		}

		if model.AssignmentStatus != "active" || model.AssignmentOwner != string(navigation.AssignmentOwnerCaptain) {
			return shared.NewShipNotReservedError(shipSymbol)
		}

		now := r.clock.Now()
		err = tx.Model(&model).Updates(map[string]interface{}{
			"assignment_status": "idle",
			"container_id":      nil,
			"released_at":       now,
			"release_reason":    reason,
			"assignment_owner":  string(navigation.AssignmentOwnerContainer),
			"assignment_reason": "",
		}).Error

		if err != nil {
			return fmt.Errorf("failed to release captain reservation: %w", err)
		}

		// Invalidate cache since assignment changed
		r.shipListCache.Delete(playerID.Value())

		return nil
	})
}

// AssignFleet atomically sets the ship's DedicatedFleet tag — the single
// write path for fleet dedication (sp-l7h2). fleet == "" clears it. Uses the
// same row-level locking as ClaimShip so an assignment can never interleave
// with a concurrent claim's read-check-write. Deliberately does NOT reject a
// claimed or captain-reserved hull: dedication is permanent ownership ("who
// may claim this next"), orthogonal to current occupancy — the tag takes
// effect when the present claim is released, it does not evict the holder.
// Idempotent: writing the already-persisted value performs zero DB writes,
// keeping every-restart reconciliation cheap.
func (r *ShipRepository) AssignFleet(ctx context.Context, shipSymbol string, fleet string, playerID shared.PlayerID) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model persistence.ShipModel

		// Lock the row with SELECT FOR UPDATE to prevent race conditions
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("ship_symbol = ? AND player_id = ?", shipSymbol, playerID.Value()).
			First(&model).Error

		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("ship %s not found for player %d", shipSymbol, playerID.Value())
		}
		if err != nil {
			return fmt.Errorf("failed to lock ship: %w", err)
		}

		// Already tagged with this fleet — idempotent success, zero writes.
		if model.DedicatedFleet == fleet {
			return nil
		}

		if err := tx.Model(&model).Update("dedicated_fleet", fleet).Error; err != nil {
			return fmt.Errorf("failed to assign fleet: %w", err)
		}

		// Invalidate the ship-list cache: a freshly-dedicated ship must not
		// linger in another coordinator's discovery for a stale-cache window
		// (design note, sp-l7h2).
		r.shipListCache.Delete(playerID.Value())

		return nil
	})
}

// SetCargoReservation atomically sets or releases a single cargo do-not-sell
// override on a hull (sp-1vhv) — the single write path behind the
// `ship reserve-cargo`/`unreserve-cargo` verbs. reserved=true force-protects the
// good; reserved=false force-allows its sale, releasing the default MODULE_/MOUNT_
// reservation for a deliberate resale. Uses the same row-level SELECT FOR UPDATE
// as AssignFleet so a reservation edit can never interleave with a concurrent ship
// write and lose the other's update, and is idempotent (writing the already-
// persisted decision performs zero DB writes). A previously-corrupt override
// column is repaired to a fresh set carrying just this decision.
func (r *ShipRepository) SetCargoReservation(ctx context.Context, shipSymbol, good string, reserved bool, playerID shared.PlayerID) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model persistence.ShipModel

		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("ship_symbol = ? AND player_id = ?", shipSymbol, playerID.Value()).
			First(&model).Error

		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("ship %s not found for player %d", shipSymbol, playerID.Value())
		}
		if err != nil {
			return fmt.Errorf("failed to lock ship: %w", err)
		}

		overrides, corrupt := parseReservationOverrides(model.ReservationOverrides)
		if overrides == nil {
			overrides = map[string]bool{}
		}
		// Idempotent: the decision is already persisted and the column is readable.
		if existing, ok := overrides[good]; ok && existing == reserved && !corrupt {
			return nil
		}
		overrides[good] = reserved

		encoded, err := json.Marshal(overrides)
		if err != nil {
			return fmt.Errorf("failed to encode reservation overrides: %w", err)
		}
		if err := tx.Model(&model).Update("reservation_overrides", string(encoded)).Error; err != nil {
			return fmt.Errorf("failed to set cargo reservation: %w", err)
		}

		r.shipListCache.Delete(playerID.Value())
		return nil
	})
}

// =============================================================================
// DB-as-Source-of-Truth Methods
// =============================================================================

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
	// doesn't sideline the hull (sp-xxhn).
	fuel, err := shared.ReconstructFuel(model.FuelCurrent, model.FuelCapacity)
	if err != nil {
		return nil, fmt.Errorf("failed to create fuel: %w", err)
	}

	// Parse cargo inventory from JSON
	var cargoItems []*shared.CargoItem
	if model.CargoInventory != "" && model.CargoInventory != "[]" {
		var cargoJSON []persistence.CargoItemJSON
		if err := json.Unmarshal([]byte(model.CargoInventory), &cargoJSON); err == nil {
			for _, item := range cargoJSON {
				cargoItem, err := shared.NewCargoItem(item.Symbol, item.Name, item.Description, item.Units)
				if err == nil {
					cargoItems = append(cargoItems, cargoItem)
				}
			}
		}
	}

	// Create cargo value object
	cargo, err := shared.NewCargo(model.CargoCapacity, model.CargoUnits, cargoItems)
	if err != nil {
		return nil, fmt.Errorf("failed to create cargo: %w", err)
	}

	// Parse modules from JSON
	var modules []*navigation.ShipModule
	if model.Modules != "" && model.Modules != "[]" {
		var modulesJSON []persistence.ModuleJSON
		if err := json.Unmarshal([]byte(model.Modules), &modulesJSON); err == nil {
			for _, mod := range modulesJSON {
				requirements := navigation.NewShipRequirements(mod.Requirements.Power, mod.Requirements.Crew, mod.Requirements.Slots)
				module := navigation.NewShipModule(mod.Symbol, mod.Capacity, mod.Range, requirements)
				modules = append(modules, module)
			}
		}
	}

	// Parse mounts from JSON (sp-el60)
	var mounts []*navigation.ShipMount
	if model.Mounts != "" && model.Mounts != "[]" {
		var mountsJSON []persistence.MountJSON
		if err := json.Unmarshal([]byte(model.Mounts), &mountsJSON); err == nil {
			for _, mnt := range mountsJSON {
				requirements := navigation.NewShipRequirements(mnt.Requirements.Power, mnt.Requirements.Crew, mnt.Requirements.Slots)
				mounts = append(mounts, navigation.NewShipMount(mnt.Symbol, mnt.Name, mnt.Strength, mnt.Deposits, requirements))
			}
		}
	}

	// Build assignment from model
	assignment := r.modelToAssignment(model)

	// Reactor requirements (sp-el60)
	reactorRequirements := navigation.NewShipRequirements(
		model.ReactorRequirementsPower,
		model.ReactorRequirementsCrew,
		model.ReactorRequirementsSlots,
	)

	// Create ship using reconstruction constructor
	ship, err := navigation.ReconstructShip(
		model.ShipSymbol,
		playerID,
		location,
		fuel,
		model.FuelCapacity,
		model.CargoCapacity,
		cargo,
		model.EngineSpeed,
		model.FrameSymbol,
		model.Role,
		modules,
		navigation.NavStatus(model.NavStatus),
		model.FlightMode,
		model.ArrivalTime,
		model.CooldownExpiration,
		assignment,
		model.DedicatedFleet,
		model.ReactorSymbol,
		model.ReactorName,
		model.ReactorPowerOutput,
		reactorRequirements,
		model.ModuleSlots,
		model.MountingPoints,
		mounts,
		model.CrewCurrent,
		model.CrewRequired,
		model.CrewCapacity,
	)
	if err != nil {
		return nil, err
	}

	// Reservation overrides (sp-1vhv): load the per-hull cargo do-not-sell set. A
	// malformed column reconstructs the hull with the corrupt flag set, so the
	// domain guard fails CLOSED (treats all cargo as reserved) rather than dropping
	// protections it cannot read.
	overrides, corrupt := parseReservationOverrides(model.ReservationOverrides)
	ship.SetReservationOverrides(overrides, corrupt)
	return ship, nil
}

// parseReservationOverrides decodes the per-hull cargo do-not-sell override JSON
// (sp-1vhv). Empty/absent/"{}"/"null" is a clean empty set. A malformed value
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

// shipDataToModel converts API ship data to DB model for sync
func (r *ShipRepository) shipDataToModel(ctx context.Context, data *navigation.ShipData, playerID shared.PlayerID, now time.Time) (*persistence.ShipModel, error) {
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

	// Parse arrival time if present
	if data.ArrivalTime != "" {
		if arrivalTime, err := time.Parse(time.RFC3339, data.ArrivalTime); err == nil {
			model.ArrivalTime = &arrivalTime
		}
	}

	// Parse cooldown expiration if present
	if data.CooldownExpiration != "" {
		if cooldownExp, err := time.Parse(time.RFC3339, data.CooldownExpiration); err == nil {
			model.CooldownExpiration = &cooldownExp
		}
	}

	// Location
	model.LocationSymbol = data.Location
	model.SystemSymbol = shared.ExtractSystemSymbol(data.Location)
	// We need to get coordinates from waypoint provider
	if waypoint, err := r.waypointProvider.GetWaypoint(ctx, data.Location, model.SystemSymbol, playerID.Value()); err == nil {
		model.LocationX = waypoint.X
		model.LocationY = waypoint.Y
	}

	// Fuel — clamp a transient API over-report (current>capacity) at the
	// persistence boundary so we never store an invariant-violating row (sp-xxhn).
	model.FuelCurrent = min(data.FuelCurrent, data.FuelCapacity)
	model.FuelCapacity = data.FuelCapacity

	// Cargo
	model.CargoCapacity = data.CargoCapacity
	model.CargoUnits = data.CargoUnits
	if data.Cargo != nil {
		cargoItems := make([]persistence.CargoItemJSON, 0)
		for _, item := range data.Cargo.Inventory {
			cargoItems = append(cargoItems, persistence.CargoItemJSON{
				Symbol:      item.Symbol,
				Name:        item.Name,
				Description: item.Description,
				Units:       item.Units,
			})
		}
		if cargoJSON, err := json.Marshal(cargoItems); err == nil {
			model.CargoInventory = string(cargoJSON)
		}
	}

	// Ship specifications
	model.EngineSpeed = data.EngineSpeed
	model.FrameSymbol = data.FrameSymbol
	model.Role = data.Role

	// Modules
	moduleItems := make([]persistence.ModuleJSON, 0)
	for _, mod := range data.Modules {
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
	if modulesJSON, err := json.Marshal(moduleItems); err == nil {
		model.Modules = string(modulesJSON)
	}

	// Mounts (sp-el60)
	mountItems := make([]persistence.MountJSON, 0)
	for _, mnt := range data.Mounts {
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
	if mountsJSON, err := json.Marshal(mountItems); err == nil {
		model.Mounts = string(mountsJSON)
	}

	// Reactor/slots/crew (sp-el60): fixed for the life of the hull.
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

	return model, nil
}

// SyncAllFromAPI fetches all ships from API and upserts to database
func (r *ShipRepository) SyncAllFromAPI(ctx context.Context, playerID shared.PlayerID) (int, error) {
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to get player: %w", err)
	}

	// Fetch all ships from API
	shipsData, err := r.apiClient.ListShips(ctx, player.Token)
	if err != nil {
		return 0, fmt.Errorf("failed to list ships from API: %w", err)
	}

	now := r.clock.Now()
	models := make([]persistence.ShipModel, 0, len(shipsData))

	for _, data := range shipsData {
		model, err := r.shipDataToModel(ctx, data, playerID, now)
		if err != nil {
			log.Printf("Warning: failed to convert ship %s: %v", data.Symbol, err)
			continue
		}

		// Preserve existing assignment data
		var existingModel persistence.ShipModel
		if err := r.db.WithContext(ctx).
			Where("ship_symbol = ? AND player_id = ?", model.ShipSymbol, model.PlayerID).
			First(&existingModel).Error; err == nil {
			// Preserve assignment data
			model.ContainerID = existingModel.ContainerID
			model.AssignmentStatus = existingModel.AssignmentStatus
			model.AssignedAt = existingModel.AssignedAt
			model.ReleasedAt = existingModel.ReleasedAt
			model.ReleaseReason = existingModel.ReleaseReason
			// sp-w870: AssignmentOwner/AssignmentReason must also be preserved.
			// shipDataToModel builds the fresh model from raw API data, which has
			// no concept of captain reservations, so these are left at their Go
			// zero value on every ship synced from the API. Without copying them
			// here, the UpdateAll upsert below silently clobbers a captain
			// reservation's ownership back to the "container" default on every
			// daemon restart (syncAllShipsOnStartup runs this unconditionally,
			// immediately after ReleaseAllActive correctly leaves it alone).
			model.AssignmentOwner = existingModel.AssignmentOwner
			model.AssignmentReason = existingModel.AssignmentReason
			// sp-bi75: DedicatedFleet (sp-l7h2) must also be preserved, same
			// bug class as AssignmentOwner/AssignmentReason above. shipDataToModel
			// builds the fresh model from raw API data, which has no concept of
			// the bot's fleet-dedication tag, so it is left at its Go zero value
			// on every ship synced from the API. Without copying it here, the
			// UpdateAll upsert below silently wipes every `fleet assign` pin back
			// to "" on every daemon restart (syncAllShipsOnStartup runs this
			// unconditionally) - re-opening the hull to poaching by whichever
			// coordinator claims it first.
			model.DedicatedFleet = existingModel.DedicatedFleet
			// sp-1vhv: ReservationOverrides is a standing per-hull tag like
			// DedicatedFleet above — raw API data has no concept of it, so without
			// copying it forward the UpdateAll upsert wipes every do-not-sell
			// reservation on the next restart, re-exposing a staged outfitting module
			// to coordinator liquidation (the exact loss this bead closes).
			model.ReservationOverrides = existingModel.ReservationOverrides
		}

		models = append(models, *model)
	}

	// Batch upsert all ships
	if len(models) > 0 {
		err = r.db.WithContext(ctx).
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
				UpdateAll: true,
			}).
			Create(&models).Error
		if err != nil {
			return 0, fmt.Errorf("failed to upsert ships: %w", err)
		}
	}

	// Invalidate cache
	r.shipListCache.Delete(playerID.Value())

	return len(models), nil
}

// SyncShipFromAPI fetches a single ship from API and persists to database
func (r *ShipRepository) SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, err
	}

	// Fetch from API
	shipData, err := r.apiClient.GetShip(ctx, symbol, player.Token)
	if err != nil {
		return nil, err
	}

	// Convert to model and persist
	now := r.clock.Now()
	model, err := r.shipDataToModel(ctx, shipData, playerID, now)
	if err != nil {
		return nil, err
	}

	// Preserve existing assignment data
	var existingModel persistence.ShipModel
	if err := r.db.WithContext(ctx).
		Where("ship_symbol = ? AND player_id = ?", model.ShipSymbol, model.PlayerID).
		First(&existingModel).Error; err == nil {
		// Preserve assignment data
		model.ContainerID = existingModel.ContainerID
		model.AssignmentStatus = existingModel.AssignmentStatus
		model.AssignedAt = existingModel.AssignedAt
		model.ReleasedAt = existingModel.ReleasedAt
		model.ReleaseReason = existingModel.ReleaseReason
		// sp-w870: see matching comment in SyncAllFromAPI - without this, a
		// captain reservation's ownership is silently clobbered back to the
		// "container" default the next time this ship is synced from the API.
		model.AssignmentOwner = existingModel.AssignmentOwner
		model.AssignmentReason = existingModel.AssignmentReason
		// sp-bi75: see matching comment in SyncAllFromAPI - without this, a
		// `fleet assign` pin is silently wiped back to "" the next time this
		// ship is synced from the API, opening it up to poaching.
		model.DedicatedFleet = existingModel.DedicatedFleet
		// sp-1vhv: see the matching comment in SyncAllFromAPI — without this a
		// do-not-sell reservation is silently wiped the next time this ship is
		// synced from the API, re-exposing a staged outfitting module.
		model.ReservationOverrides = existingModel.ReservationOverrides
	}

	err = r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			UpdateAll: true,
		}).
		Create(model).Error
	if err != nil {
		return nil, fmt.Errorf("failed to persist ship: %w", err)
	}

	// Invalidate cache
	r.shipListCache.Delete(playerID.Value())

	return r.modelToDomain(ctx, model, playerID)
}

// FindInTransitWithPastArrival finds ships that should have arrived (IN_TRANSIT with arrival_time in the past)
func (r *ShipRepository) FindInTransitWithPastArrival(ctx context.Context) ([]*navigation.Ship, error) {
	var models []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("nav_status = ?", "IN_TRANSIT").
		Where("arrival_time IS NOT NULL").
		Where("arrival_time <= ?", r.clock.Now()).
		Find(&models).Error
	if err != nil {
		return nil, err
	}

	return r.modelsToShips(ctx, models), nil
}

// FindInTransitWithFutureArrival finds ships that will arrive in the future (for scheduling)
func (r *ShipRepository) FindInTransitWithFutureArrival(ctx context.Context) ([]*navigation.Ship, error) {
	var models []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("nav_status = ?", "IN_TRANSIT").
		Where("arrival_time IS NOT NULL").
		Where("arrival_time > ?", r.clock.Now()).
		Find(&models).Error
	if err != nil {
		return nil, err
	}

	return r.modelsToShips(ctx, models), nil
}

// FindWithExpiredCooldown finds ships with past cooldowns
func (r *ShipRepository) FindWithExpiredCooldown(ctx context.Context) ([]*navigation.Ship, error) {
	var models []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("cooldown_expiration IS NOT NULL").
		Where("cooldown_expiration <= ?", r.clock.Now()).
		Find(&models).Error
	if err != nil {
		return nil, err
	}

	return r.modelsToShips(ctx, models), nil
}

// FindWithFutureCooldown finds ships with cooldowns expiring in the future (for scheduling)
func (r *ShipRepository) FindWithFutureCooldown(ctx context.Context) ([]*navigation.Ship, error) {
	var models []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("cooldown_expiration IS NOT NULL").
		Where("cooldown_expiration > ?", r.clock.Now()).
		Find(&models).Error
	if err != nil {
		return nil, err
	}

	return r.modelsToShips(ctx, models), nil
}

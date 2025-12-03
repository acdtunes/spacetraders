package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
// - In-memory cache (15s TTL): Prevents redundant DB reads
//   when multiple coordinators call FindAllByPlayer in quick succession
type ShipRepository struct {
	apiClient        domainPorts.APIClient
	playerRepo       player.PlayerRepository
	waypointRepo     system.WaypointRepository
	waypointProvider system.IWaypointProvider
	db               *gorm.DB     // Database connection for ship state persistence
	clock            shared.Clock // Clock for timestamps
	shipListCache    sync.Map     // key: playerID (int) -> *cachedShipList
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

	// Create fuel value object
	fuel, err := shared.NewFuel(data.FuelCurrent, data.FuelCapacity)
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
		module := navigation.NewShipModule(mod.Symbol, mod.Capacity, mod.Range)
		modules = append(modules, module)
	}

	// Convert nav status
	navStatus := navigation.NavStatus(data.NavStatus)

	// Create ship domain entity
	return navigation.NewShip(
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
}

// isAlreadyDockedError checks if the error is due to ship already being docked
// API returns error code 4237 when trying to dock an already docked ship
func isAlreadyDockedError(err error) bool {
	if err == nil {
		return false
	}
	// Check if error message contains indication that ship is already docked
	errMsg := err.Error()
	return contains(errMsg, "already docked") || contains(errMsg, "4237")
}

// isAlreadyInOrbitError checks if the error is due to ship already being in orbit
// API returns error code when trying to orbit an already orbiting ship
func isAlreadyInOrbitError(err error) bool {
	if err == nil {
		return false
	}
	// Check if error message contains indication that ship is already in orbit
	errMsg := err.Error()
	return contains(errMsg, "already in orbit") || contains(errMsg, "in orbit")
}

// contains checks if a string contains a substring (case-insensitive helper)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && indexSubstring(s, substr) >= 0))
}

// indexSubstring finds the index of substr in s (simple implementation)
func indexSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
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

	return navigation.ReconstructAssignment(
		containerID,
		navigation.AssignmentStatus(model.AssignmentStatus),
		assignedAt,
		model.ReleasedAt,
		&model.ReleaseReason,
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
		moduleItems = append(moduleItems, persistence.ModuleJSON{
			Symbol:   mod.Symbol(),
			Capacity: mod.Capacity(),
			Range:    mod.Range(),
		})
	}
	if modulesJSON, err := json.Marshal(moduleItems); err == nil {
		model.Modules = string(modulesJSON)
	}

	// Cooldown
	model.CooldownExpiration = ship.CooldownExpiration()

	// Assignment
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
	}

	return model
}

// Save persists ship aggregate state (including full state) to DB
func (r *ShipRepository) Save(ctx context.Context, ship *navigation.Ship) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	model := r.shipToModel(ship)
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			UpdateAll: true,
		}).
		Create(&model).Error
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
	for i, ship := range ships {
		models[i] = r.shipToModel(ship)
	}

	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			UpdateAll: true,
		}).
		Create(&models).Error
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

// ReleaseAllActive releases all active ship assignments (bulk operation)
// Used during daemon startup to clean up zombie assignments from previous runs
func (r *ShipRepository) ReleaseAllActive(ctx context.Context, reason string) (int, error) {
	if r.db == nil {
		return 0, fmt.Errorf("database not configured")
	}

	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&persistence.ShipModel{}).
		Where("assignment_status = ?", "active").
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

// =============================================================================
// DB-as-Source-of-Truth Methods
// =============================================================================

// modelToDomain converts DB model to domain entity
func (r *ShipRepository) modelToDomain(ctx context.Context, model *persistence.ShipModel, playerID shared.PlayerID) (*navigation.Ship, error) {
	// Build location waypoint from denormalized data
	location := &shared.Waypoint{
		Symbol:       model.LocationSymbol,
		X:            model.LocationX,
		Y:            model.LocationY,
		SystemSymbol: model.SystemSymbol,
	}

	// Create fuel value object
	fuel, err := shared.NewFuel(model.FuelCurrent, model.FuelCapacity)
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
				module := navigation.NewShipModule(mod.Symbol, mod.Capacity, mod.Range)
				modules = append(modules, module)
			}
		}
	}

	// Build assignment from model
	assignment := r.modelToAssignment(model)

	// Create ship using reconstruction constructor
	return navigation.ReconstructShip(
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
	)
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
	model.FlightMode = "CRUISE" // Default

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

	// Fuel
	model.FuelCurrent = data.FuelCurrent
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
		})
	}
	if modulesJSON, err := json.Marshal(moduleItems); err == nil {
		model.Modules = string(modulesJSON)
	}

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

	ships := make([]*navigation.Ship, 0, len(models))
	for _, model := range models {
		playerID, _ := shared.NewPlayerID(model.PlayerID)
		ship, err := r.modelToDomain(ctx, &model, playerID)
		if err != nil {
			continue
		}
		ships = append(ships, ship)
	}
	return ships, nil
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

	ships := make([]*navigation.Ship, 0, len(models))
	for _, model := range models {
		playerID, _ := shared.NewPlayerID(model.PlayerID)
		ship, err := r.modelToDomain(ctx, &model, playerID)
		if err != nil {
			continue
		}
		ships = append(ships, ship)
	}
	return ships, nil
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

	ships := make([]*navigation.Ship, 0, len(models))
	for _, model := range models {
		playerID, _ := shared.NewPlayerID(model.PlayerID)
		ship, err := r.modelToDomain(ctx, &model, playerID)
		if err != nil {
			continue
		}
		ships = append(ships, ship)
	}
	return ships, nil
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

	ships := make([]*navigation.Ship, 0, len(models))
	for _, model := range models {
		playerID, _ := shared.NewPlayerID(model.PlayerID)
		ship, err := r.modelToDomain(ctx, &model, playerID)
		if err != nil {
			continue
		}
		ships = append(ships, ship)
	}
	return ships, nil
}

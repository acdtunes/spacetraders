package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
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

// ShipRepository implements ShipRepository using the SpaceTraders API
// This repository adapts API responses to domain entities
//
// Caching Strategy:
// - Ship list cache (15s TTL): Prevents redundant ListShips API calls
//   when multiple coordinators call FindAllByPlayer in quick succession
type ShipRepository struct {
	apiClient        domainPorts.APIClient
	playerRepo       player.PlayerRepository
	waypointRepo     system.WaypointRepository
	waypointProvider system.IWaypointProvider
	shipListCache    sync.Map // key: playerID (int) -> *cachedShipList
}

// NewShipRepository creates a new API ship repository
func NewShipRepository(
	apiClient domainPorts.APIClient,
	playerRepo player.PlayerRepository,
	waypointRepo system.WaypointRepository,
	waypointProvider system.IWaypointProvider,
) *ShipRepository {
	return &ShipRepository{
		apiClient:        apiClient,
		playerRepo:       playerRepo,
		waypointRepo:     waypointRepo,
		waypointProvider: waypointProvider,
	}
}

// FindBySymbol retrieves a ship by symbol and player ID from API
// Converts API DTO to domain entity with full waypoint reconstruction
func (r *ShipRepository) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Fetch ship from API
	shipData, err := r.apiClient.GetShip(ctx, symbol, player.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship from API: %w", err)
	}

	// Convert API DTO to domain entity
	ship, err := r.shipDataToDomain(ctx, shipData, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert ship data: %w", err)
	}

	return ship, nil
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

// FindAllByPlayer retrieves all ships for a player from API with short-lived caching
// Converts API DTOs to domain entities with full waypoint reconstruction
//
// Caching: Returns cached ship list if within 15 seconds of last fetch.
// This prevents redundant API calls when multiple coordinators (manufacturing,
// gas, contract fleet) call FindIdleLightHaulers in quick succession.
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

	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Fetch all ships from API
	shipsData, err := r.apiClient.ListShips(ctx, player.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to list ships from API: %w", err)
	}

	// Convert API DTOs to domain entities
	ships := make([]*navigation.Ship, len(shipsData))
	for i, shipData := range shipsData {
		ship, err := r.shipDataToDomain(ctx, shipData, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to convert ship %s: %w", shipData.Symbol, err)
		}
		ships[i] = ship
	}

	// Cache the result
	r.shipListCache.Store(cacheKey, &cachedShipList{
		ships:     ships,
		fetchedAt: time.Now(),
	})

	return ships, nil
}

// Navigate executes ship navigation via API
// Returns navigation result with arrival time from API (following Python implementation pattern)
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

	return navResult, nil
}

// Dock docks the ship via API (idempotent)
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

	return nil
}

// Orbit puts ship in orbit via API (idempotent)
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

	return nil
}

// Refuel refuels the ship via API
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

	return refuelResult, nil
}

// SetFlightMode sets the ship's flight mode via API
func (r *ShipRepository) SetFlightMode(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID, mode string) error {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to set flight mode
	if err := r.apiClient.SetFlightMode(ctx, ship.ShipSymbol(), mode, player.Token); err != nil {
		return fmt.Errorf("failed to set flight mode: %w", err)
	}

	// Note: The API response updates the ship's flight mode,
	// but we don't need to update the domain entity here
	// as the ship's flight mode is not part of its core state

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

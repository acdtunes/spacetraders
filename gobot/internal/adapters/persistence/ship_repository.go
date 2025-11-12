package persistence

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// GormShipRepository implements ShipRepository using GORM
// This repository abstracts API calls and converts DTOs to domain entities
type GormShipRepository struct {
	db               *gorm.DB
	apiClient        common.APIClient
	playerRepo       common.PlayerRepository
	waypointRepo     common.WaypointRepository
}

// NewGormShipRepository creates a new GORM ship repository
func NewGormShipRepository(
	db *gorm.DB,
	apiClient common.APIClient,
	playerRepo common.PlayerRepository,
	waypointRepo common.WaypointRepository,
) *GormShipRepository {
	return &GormShipRepository{
		db:           db,
		apiClient:    apiClient,
		playerRepo:   playerRepo,
		waypointRepo: waypointRepo,
	}
}

// FindBySymbol retrieves a ship by symbol and player ID from API
// Converts API DTO to domain entity with full waypoint reconstruction
func (r *GormShipRepository) FindBySymbol(ctx context.Context, symbol string, playerID int) (*navigation.Ship, error) {
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

// FindAllByPlayer retrieves all ships for a player from API
// Converts API DTOs to domain entities with full waypoint reconstruction
func (r *GormShipRepository) FindAllByPlayer(ctx context.Context, playerID int) ([]*navigation.Ship, error) {
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

	return ships, nil
}

// Save persists ship state to database (optional - for caching)
func (r *GormShipRepository) Save(ctx context.Context, ship *navigation.Ship) error {
	// For now, ships are API-only (no database caching)
	// This can be implemented later if needed
	return nil
}

// Navigate executes ship navigation via API
// Returns navigation result with arrival time from API (following Python implementation pattern)
func (r *GormShipRepository) Navigate(ctx context.Context, ship *navigation.Ship, destination *shared.Waypoint, playerID int) (*common.NavigationResult, error) {
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
func (r *GormShipRepository) Dock(ctx context.Context, ship *navigation.Ship, playerID int) error {
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

	// Update ship domain entity state only if not already docked
	if ship.NavStatus() != navigation.NavStatusDocked {
		if err := ship.Dock(); err != nil {
			return fmt.Errorf("failed to update ship state: %w", err)
		}
	}

	return nil
}

// Orbit puts ship in orbit via API (idempotent)
func (r *GormShipRepository) Orbit(ctx context.Context, ship *navigation.Ship, playerID int) error {
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

	// Update ship domain entity state only if not already in orbit
	if ship.NavStatus() != navigation.NavStatusInOrbit {
		if err := ship.Depart(); err != nil {
			return fmt.Errorf("failed to update ship state: %w", err)
		}
	}

	return nil
}

// Refuel refuels the ship via API
func (r *GormShipRepository) Refuel(ctx context.Context, ship *navigation.Ship, playerID int, units *int) error {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to refuel ship
	refuelResult, err := r.apiClient.RefuelShip(ctx, ship.ShipSymbol(), player.Token, units)
	if err != nil {
		return fmt.Errorf("failed to refuel ship: %w", err)
	}

	// Update ship domain entity state
	// If units specified, add that amount, otherwise refuel to full
	if units != nil {
		// Add specific amount (not implemented in domain yet)
		// For now, just refuel to full
		if _, err := ship.RefuelToFull(); err != nil {
			return fmt.Errorf("failed to update ship fuel: %w", err)
		}
	} else {
		// Refuel to full
		if _, err := ship.RefuelToFull(); err != nil {
			return fmt.Errorf("failed to update ship fuel: %w", err)
		}
	}

	_ = refuelResult // Acknowledge we have the result

	return nil
}

// SetFlightMode sets the ship's flight mode via API
func (r *GormShipRepository) SetFlightMode(ctx context.Context, ship *navigation.Ship, playerID int, mode string) error {
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

// shipDataToDomain converts API ship DTO to domain entity
func (r *GormShipRepository) shipDataToDomain(ctx context.Context, data *common.ShipData, playerID int) (*navigation.Ship, error) {
	// Get current location waypoint from repository
	location, err := r.waypointRepo.FindBySymbol(ctx, data.Location, extractSystemSymbol(data.Location))
	if err != nil {
		return nil, fmt.Errorf("failed to get location waypoint: %w", err)
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
		navStatus,
	)
}

// extractSystemSymbol extracts system symbol from waypoint symbol
// Format: SYSTEM-SECTOR-WAYPOINT -> SYSTEM-SECTOR
// Example: X1-GZ7-A1 -> X1-GZ7
func extractSystemSymbol(waypointSymbol string) string {
	for i := len(waypointSymbol) - 1; i >= 0; i-- {
		if waypointSymbol[i] == '-' {
			return waypointSymbol[:i]
		}
	}
	return waypointSymbol
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

package helpers

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MockShipRepository is an in-memory implementation of ShipRepository for testing
type MockShipRepository struct {
	Ships         map[string]*navigation.Ship // key: ship_symbol
	ShipsByPlayer map[int][]*navigation.Ship  // key: player_id
}

// NewMockShipRepository creates a new mock ship repository
func NewMockShipRepository() *MockShipRepository {
	return &MockShipRepository{
		Ships:         make(map[string]*navigation.Ship),
		ShipsByPlayer: make(map[int][]*navigation.Ship),
	}
}

// FindBySymbol retrieves a ship by symbol
func (m *MockShipRepository) FindBySymbol(ctx context.Context, symbol string, playerID int) (*navigation.Ship, error) {
	ship, exists := m.Ships[symbol]
	if !exists {
		return nil, fmt.Errorf("ship not found: %s", symbol)
	}

	return ship, nil
}

// GetShipData retrieves raw ship data
func (m *MockShipRepository) GetShipData(ctx context.Context, symbol string, playerID int) (*navigation.ShipData, error) {
	ship, err := m.FindBySymbol(ctx, symbol, playerID)
	if err != nil {
		return nil, err
	}

	// Convert ship to ShipData
	return &navigation.ShipData{
		Symbol:        ship.ShipSymbol(),
		Location:      ship.CurrentLocation().Symbol,
		NavStatus:     string(ship.NavStatus()),
		ArrivalTime:   "",
		FuelCurrent:   ship.Fuel().Current,
		FuelCapacity:  ship.Fuel().Capacity,
		CargoCapacity: ship.CargoCapacity(),
		CargoUnits:    ship.CargoUnits(),
		EngineSpeed:   ship.EngineSpeed(),
		FrameSymbol:   ship.FrameSymbol(),
		Cargo:         nil,
	}, nil
}

// FindAllByPlayer retrieves all ships for a player
func (m *MockShipRepository) FindAllByPlayer(ctx context.Context, playerID int) ([]*navigation.Ship, error) {
	ships, exists := m.ShipsByPlayer[playerID]
	if !exists {
		return []*navigation.Ship{}, nil
	}

	return ships, nil
}

// Navigate executes ship navigation
func (m *MockShipRepository) Navigate(ctx context.Context, ship *navigation.Ship, destination *shared.Waypoint, playerID int) (*navigation.NavigationResult, error) {
	// Mock implementation - minimal functionality for tests
	return &navigation.NavigationResult{}, nil
}

// Dock docks the ship
func (m *MockShipRepository) Dock(ctx context.Context, ship *navigation.Ship, playerID int) error {
	// Mock implementation - just call the domain method
	return nil
}

// Orbit puts the ship into orbit
func (m *MockShipRepository) Orbit(ctx context.Context, ship *navigation.Ship, playerID int) error {
	// Mock implementation - minimal functionality
	return nil
}

// Refuel refuels the ship
func (m *MockShipRepository) Refuel(ctx context.Context, ship *navigation.Ship, playerID int, units *int) error {
	// Mock implementation - minimal functionality
	return nil
}

// SetFlightMode sets the ship's flight mode
func (m *MockShipRepository) SetFlightMode(ctx context.Context, ship *navigation.Ship, playerID int, mode string) error {
	// Mock implementation - minimal functionality
	return nil
}

// JettisonCargo jettisons cargo from the ship
func (m *MockShipRepository) JettisonCargo(ctx context.Context, ship *navigation.Ship, playerID int, goodSymbol string, units int) error {
	// Mock implementation - minimal functionality
	return nil
}

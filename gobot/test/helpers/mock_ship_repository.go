package helpers

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MockShip represents a simplified ship for testing
type MockShip struct {
	Symbol    string
	FrameType string
	System    string
	Location  string
	PlayerID  int
}

// MockShipRepository is a test double for ShipRepository interface
type MockShipRepository struct {
	mu        sync.RWMutex
	ships     map[string]*navigation.Ship // symbol -> ship
	mockShips map[string]*MockShip        // symbol -> mock ship (for frame type testing)
}

// NewMockShipRepository creates a new mock ship repository
func NewMockShipRepository() *MockShipRepository {
	return &MockShipRepository{
		ships:     make(map[string]*navigation.Ship),
		mockShips: make(map[string]*MockShip),
	}
}

// AddShip adds a ship to the mock repository (full domain entity)
func (m *MockShipRepository) AddShip(ship interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch v := ship.(type) {
	case *navigation.Ship:
		m.ships[v.ShipSymbol()] = v
	case *MockShip:
		m.mockShips[v.Symbol] = v
		// Also create a basic domain ship for compatibility
		waypoint, _ := shared.NewWaypoint(v.Location, 0, 0)
		fuel, _ := shared.NewFuel(100, 100)
		cargo, _ := shared.NewCargo(100, 0, []*shared.CargoItem{})
		domainShip, _ := navigation.NewShip(
			v.Symbol,
			v.PlayerID,
			waypoint,
			fuel,
			100,
			100,
			cargo,
			10,
			navigation.NavStatusDocked,
		)
		m.ships[v.Symbol] = domainShip
	}
}

// FindBySymbol retrieves a ship by symbol
func (m *MockShipRepository) FindBySymbol(ctx context.Context, symbol string, playerID int) (*navigation.Ship, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ship, ok := m.ships[symbol]
	if !ok {
		return nil, fmt.Errorf("ship not found: %s", symbol)
	}

	// Validate player ownership
	if ship.PlayerID() != playerID {
		return nil, fmt.Errorf("ship not found: %s", symbol)
	}

	return ship, nil
}

// FindAllByPlayer retrieves all ships for a player
func (m *MockShipRepository) FindAllByPlayer(ctx context.Context, playerID int) ([]*navigation.Ship, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var ships []*navigation.Ship
	for _, ship := range m.ships {
		if ship.PlayerID() == playerID {
			ships = append(ships, ship)
		}
	}

	return ships, nil
}

// GetMockShips returns all mock ships (with frame type info)
func (m *MockShipRepository) GetMockShips() []*MockShip {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var ships []*MockShip
	for _, ship := range m.mockShips {
		ships = append(ships, ship)
	}

	return ships
}

// GetMockShipsByFrameType filters mock ships by frame type
func (m *MockShipRepository) GetMockShipsByFrameType(frameTypes ...string) []*MockShip {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var ships []*MockShip
	for _, ship := range m.mockShips {
		for _, frameType := range frameTypes {
			if ship.FrameType == frameType {
				ships = append(ships, ship)
				break
			}
		}
	}

	return ships
}

// GetMockShipsBySystem filters mock ships by system
func (m *MockShipRepository) GetMockShipsBySystem(systemSymbol string) []*MockShip {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var ships []*MockShip
	for _, ship := range m.mockShips {
		if ship.System == systemSymbol {
			ships = append(ships, ship)
		}
	}

	return ships
}

// Save persists ship state
func (m *MockShipRepository) Save(ctx context.Context, ship *navigation.Ship) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ships[ship.ShipSymbol()] = ship
	return nil
}

// Navigate executes ship navigation
func (m *MockShipRepository) Navigate(ctx context.Context, ship *navigation.Ship, destination *shared.Waypoint, playerID int) (*navigation.NavigationResult, error) {
	// Simple mock: just update ship's position
	// Real implementation would call API
	return &navigation.NavigationResult{
		Destination:    destination.Symbol,
		ArrivalTime:    60,
		ArrivalTimeStr: "",
		FuelConsumed:   10,
	}, nil
}

// Dock docks the ship
func (m *MockShipRepository) Dock(ctx context.Context, ship *navigation.Ship, playerID int) error {
	// Mock implementation - just succeed
	return nil
}

// Orbit puts ship in orbit
func (m *MockShipRepository) Orbit(ctx context.Context, ship *navigation.Ship, playerID int) error {
	// Mock implementation - just succeed
	return nil
}

// Refuel refuels the ship
func (m *MockShipRepository) Refuel(ctx context.Context, ship *navigation.Ship, playerID int, units *int) error {
	// Mock implementation - just succeed
	return nil
}

// SetFlightMode sets the ship's flight mode
func (m *MockShipRepository) SetFlightMode(ctx context.Context, ship *navigation.Ship, playerID int, mode string) error {
	// Mock implementation - just succeed
	return nil
}

// JettisonCargo jettisons cargo from the ship
func (m *MockShipRepository) JettisonCargo(ctx context.Context, ship *navigation.Ship, playerID int, goodSymbol string, units int) error {
	// Mock implementation - just succeed
	return nil
}

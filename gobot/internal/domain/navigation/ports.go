package navigation

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ShipRepository defines ship persistence and API operations
// Following hexagonal architecture: repositories abstract both database and API operations
type ShipRepository interface {
	// FindBySymbol retrieves a ship (from API with waypoint reconstruction)
	FindBySymbol(ctx context.Context, symbol string, playerID int) (*Ship, error)

	// GetShipData retrieves raw ship data from API (includes arrival time for IN_TRANSIT ships)
	GetShipData(ctx context.Context, symbol string, playerID int) (*ShipData, error)

	// FindAllByPlayer retrieves all ships for a player (from API with waypoint reconstruction)
	FindAllByPlayer(ctx context.Context, playerID int) ([]*Ship, error)

	// Navigate executes ship navigation (updates via API)
	// Returns navigation result with arrival time from API
	Navigate(ctx context.Context, ship *Ship, destination *shared.Waypoint, playerID int) (*NavigationResult, error)

	// Dock docks the ship (updates via API)
	Dock(ctx context.Context, ship *Ship, playerID int) error

	// Orbit puts ship in orbit (updates via API)
	Orbit(ctx context.Context, ship *Ship, playerID int) error

	// Refuel refuels the ship (updates via API)
	Refuel(ctx context.Context, ship *Ship, playerID int, units *int) error

	// SetFlightMode sets the ship's flight mode (updates via API)
	SetFlightMode(ctx context.Context, ship *Ship, playerID int, mode string) error

	// JettisonCargo jettisons cargo from the ship (updates via API)
	JettisonCargo(ctx context.Context, ship *Ship, playerID int, goodSymbol string, units int) error
}

// DTOs for ship operations

type ShipData struct {
	Symbol          string
	Location        string
	NavStatus       string
	ArrivalTime     string // ISO8601 timestamp when IN_TRANSIT (e.g., "2024-01-01T12:00:00Z"), empty otherwise
	FuelCurrent     int
	FuelCapacity    int
	CargoCapacity   int
	CargoUnits      int
	EngineSpeed     int
	FrameSymbol     string // Frame type (e.g., "FRAME_PROBE", "FRAME_DRONE", "FRAME_MINER")
	Role            string // Ship role from registration (e.g., "EXCAVATOR", "COMMAND", "SATELLITE")
	Cargo           *CargoData
}

type CargoData struct {
	Capacity  int
	Units     int
	Inventory []CargoItemData
}

type CargoItemData struct {
	Symbol      string
	Name        string
	Description string
	Units       int
}

type NavigationResult struct {
	Destination      string
	ArrivalTime      int    // Calculated seconds
	ArrivalTimeStr   string // ISO8601 timestamp from API (e.g., "2024-01-01T12:00:00Z")
	FuelConsumed     int
}

type RefuelResult struct {
	FuelAdded int
	CreditsCost int
}

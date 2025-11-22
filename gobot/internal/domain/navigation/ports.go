package navigation

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ShipQueryRepository handles ship data queries.
//
// This interface follows the Interface Segregation Principle (ISP) by focusing
// exclusively on read operations. Implementations that only need to query ship
// data don't need to implement command operations.
type ShipQueryRepository interface {
	// FindBySymbol retrieves a ship (from API with waypoint reconstruction)
	FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*Ship, error)

	// GetShipData retrieves raw ship data from API (includes arrival time for IN_TRANSIT ships)
	GetShipData(ctx context.Context, symbol string, playerID shared.PlayerID) (*ShipData, error)

	// FindAllByPlayer retrieves all ships for a player (from API with waypoint reconstruction)
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*Ship, error)
}

// ShipCommandRepository handles ship actions and state changes.
//
// This interface follows ISP by focusing on write operations that modify ship state.
// Separating commands from queries enables CQRS pattern adoption in the future.
type ShipCommandRepository interface {
	// Navigate executes ship navigation (updates via API)
	// Returns navigation result with arrival time from API
	Navigate(ctx context.Context, ship *Ship, destination *shared.Waypoint, playerID shared.PlayerID) (*Result, error)

	// Dock docks the ship (updates via API)
	Dock(ctx context.Context, ship *Ship, playerID shared.PlayerID) error

	// Orbit puts ship in orbit (updates via API)
	Orbit(ctx context.Context, ship *Ship, playerID shared.PlayerID) error

	// Refuel refuels the ship (updates via API)
	Refuel(ctx context.Context, ship *Ship, playerID shared.PlayerID, units *int) error

	// SetFlightMode sets the ship's flight mode (updates via API)
	SetFlightMode(ctx context.Context, ship *Ship, playerID shared.PlayerID, mode string) error
}

// ShipCargoRepository handles cargo operations.
//
// This interface follows ISP by isolating cargo-specific operations.
// Implementations that only need cargo management don't need navigation capabilities.
type ShipCargoRepository interface {
	// JettisonCargo jettisons cargo from the ship (updates via API)
	JettisonCargo(ctx context.Context, ship *Ship, playerID shared.PlayerID, goodSymbol string, units int) error
}

// ShipRepository combines all ship repository interfaces for convenience.
//
// This composite interface maintains backward compatibility while enabling
// ISP-compliant implementations. Use this when you need full ship repository
// capabilities, or use the focused interfaces (ShipQueryRepository,
// ShipCommandRepository, ShipCargoRepository) when you only need specific operations.
//
// Following hexagonal architecture: repositories abstract both database and API operations.
type ShipRepository interface {
	ShipQueryRepository
	ShipCommandRepository
	ShipCargoRepository
}

// DTOs for ship operations

type ShipData struct {
	Symbol        string
	Location      string
	NavStatus     string
	ArrivalTime   string // ISO8601 timestamp when IN_TRANSIT (e.g., "2024-01-01T12:00:00Z"), empty otherwise
	FuelCurrent   int
	FuelCapacity  int
	CargoCapacity int
	CargoUnits    int
	EngineSpeed   int
	FrameSymbol   string       // Frame type (e.g., "FRAME_PROBE", "FRAME_DRONE", "FRAME_MINER")
	Role          string       // Ship role from registration (e.g., "EXCAVATOR", "COMMAND", "SATELLITE")
	Modules       []ModuleData // Installed ship modules (jump drives, mining equipment, etc.)
	Cargo         *CargoData
}

type ModuleData struct {
	Symbol   string
	Capacity int
	Range    int
}

type CargoData struct {
	Capacity  int
	Units     int
	Inventory []shared.CargoItem
}

type Result struct {
	Destination    string
	ArrivalTime    int    // Calculated seconds
	ArrivalTimeStr string // ISO8601 timestamp from API (e.g., "2024-01-01T12:00:00Z")
	FuelConsumed   int
}

type RefuelResult struct {
	FuelAdded   int
	CreditsCost int
}

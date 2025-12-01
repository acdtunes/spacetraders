package types

import "github.com/andrescamacho/spacetraders-go/internal/domain/shared"

// Ship command types - shared between handlers and RouteExecutor to avoid circular imports

// OrbitShipCommand - Command to put a ship into orbit at its current waypoint
type OrbitShipCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
}

// OrbitShipResponse - Response from orbit ship command
type OrbitShipResponse struct {
	Status string // "in_orbit" or "already_in_orbit"
}

// DockShipCommand - Command to dock a ship at its current waypoint
type DockShipCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
}

// DockShipResponse - Response from dock ship command
type DockShipResponse struct {
	Status string // "docked" or "already_docked"
}

// RefuelShipCommand - Command to refuel a ship at its current waypoint
// To link transactions to a parent operation, add OperationContext to the context using
// shared.WithOperationContext() before sending this command.
type RefuelShipCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
	Units      *int // nil = refuel to full
}

// RefuelShipResponse - Response from refuel ship command
type RefuelShipResponse struct {
	Status       string
	FuelAdded    int
	CreditsCost  int
	CurrentFuel  int
	FuelCapacity int
}

// SetFlightModeCommand - Command to set a ship's flight mode
type SetFlightModeCommand struct {
	ShipSymbol string
	Mode       shared.FlightMode
	PlayerID   shared.PlayerID
}

// SetFlightModeResponse - Response from set flight mode command
type SetFlightModeResponse struct {
	Status string
	Mode   shared.FlightMode
}

// NavigateDirectCommand - Low-level command for single-hop navigation
// This is used internally by RouteExecutor - applications should use NavigateRouteCommand
type NavigateDirectCommand struct {
	ShipSymbol  string
	Destination string
	FlightMode  string
	PlayerID    shared.PlayerID
}

// NavigateDirectResponse - Response from navigate direct command
type NavigateDirectResponse struct {
	Status         string
	ArrivalTime    int    // Calculated seconds
	ArrivalTimeStr string // ISO8601 from API
	FuelConsumed   int
	TravelDuration int
	// Fuel state from API response (avoids separate GetShip call)
	FuelCurrent  int
	FuelCapacity int
}

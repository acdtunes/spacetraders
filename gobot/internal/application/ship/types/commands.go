package types

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Ship command types - shared between handlers and RouteExecutor to avoid circular imports

type ShipCommand interface {
	GetShip() *navigation.Ship
	GetShipSymbol() string
	GetPlayerID() shared.PlayerID
}

func LoadShip(ctx context.Context, shipRepo navigation.ShipRepository, cmd ShipCommand) (*navigation.Ship, error) {
	if cmd.GetShip() != nil {
		return cmd.GetShip(), nil
	}
	ship, err := shipRepo.FindBySymbol(ctx, cmd.GetShipSymbol(), cmd.GetPlayerID())
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

// OrbitShipCommand - Command to put a ship into orbit at its current waypoint
type OrbitShipCommand struct {
	Ship       *navigation.Ship // Primary: use when ship is already loaded (avoids API call)
	ShipSymbol string           // Fallback: used only if Ship is nil
	PlayerID   shared.PlayerID
}

func (c *OrbitShipCommand) GetShip() *navigation.Ship    { return c.Ship }
func (c *OrbitShipCommand) GetShipSymbol() string        { return c.ShipSymbol }
func (c *OrbitShipCommand) GetPlayerID() shared.PlayerID { return c.PlayerID }

// OrbitShipResponse - Response from orbit ship command
type OrbitShipResponse struct {
	Status string // "in_orbit" or "already_in_orbit"
}

// DockShipCommand - Command to dock a ship at its current waypoint
type DockShipCommand struct {
	Ship       *navigation.Ship // Primary: use when ship is already loaded (avoids API call)
	ShipSymbol string           // Fallback: used only if Ship is nil
	PlayerID   shared.PlayerID
}

func (c *DockShipCommand) GetShip() *navigation.Ship    { return c.Ship }
func (c *DockShipCommand) GetShipSymbol() string        { return c.ShipSymbol }
func (c *DockShipCommand) GetPlayerID() shared.PlayerID { return c.PlayerID }

// DockShipResponse - Response from dock ship command
type DockShipResponse struct {
	Status string // "docked" or "already_docked"
}

// RefuelShipCommand - Command to refuel a ship at its current waypoint
// To link transactions to a parent operation, add OperationContext to the context using
// shared.WithOperationContext() before sending this command.
type RefuelShipCommand struct {
	Ship       *navigation.Ship // Primary: use when ship is already loaded (avoids API call)
	ShipSymbol string           // Fallback: used only if Ship is nil
	PlayerID   shared.PlayerID
	Units      *int // nil = refuel to full
}

func (c *RefuelShipCommand) GetShip() *navigation.Ship    { return c.Ship }
func (c *RefuelShipCommand) GetShipSymbol() string        { return c.ShipSymbol }
func (c *RefuelShipCommand) GetPlayerID() shared.PlayerID { return c.PlayerID }

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
	Ship       *navigation.Ship // Primary: use when ship is already loaded (avoids API call)
	ShipSymbol string           // Fallback: used only if Ship is nil
	Mode       shared.FlightMode
	PlayerID   shared.PlayerID
}

func (c *SetFlightModeCommand) GetShip() *navigation.Ship    { return c.Ship }
func (c *SetFlightModeCommand) GetShipSymbol() string        { return c.ShipSymbol }
func (c *SetFlightModeCommand) GetPlayerID() shared.PlayerID { return c.PlayerID }

// SetFlightModeResponse - Response from set flight mode command
type SetFlightModeResponse struct {
	Status string
	Mode   shared.FlightMode
}

// NavigateDirectCommand - Low-level command for single-hop navigation
// This is used internally by RouteExecutor - applications should use NavigateRouteCommand
type NavigateDirectCommand struct {
	Ship                *navigation.Ship // Primary: use when ship is already loaded (avoids API call)
	ShipSymbol          string           // Fallback: used only if Ship is nil
	Destination         string           // Destination waypoint symbol
	DestinationWaypoint *shared.Waypoint // Primary: enriched waypoint with HasFuel (avoids DB lookup)
	FlightMode          string
	PlayerID            shared.PlayerID
}

func (c *NavigateDirectCommand) GetShip() *navigation.Ship    { return c.Ship }
func (c *NavigateDirectCommand) GetShipSymbol() string        { return c.ShipSymbol }
func (c *NavigateDirectCommand) GetPlayerID() shared.PlayerID { return c.PlayerID }

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

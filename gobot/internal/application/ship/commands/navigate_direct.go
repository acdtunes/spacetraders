package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// NavigateDirectCommand - LOW-LEVEL atomic command for single-hop navigation
//
// ⚠️  WARNING: This is a LOW-LEVEL command used internally by RouteExecutor.
// ⚠️  DO NOT use this directly in application workflows!
// ⚠️  Use NavigateRouteCommand instead for route planning + multi-hop navigation.
//
// This command performs a simple orbit → navigate → API call without:
// - Route planning
// - Refueling stops
// - Multi-hop navigation
// - Flight mode optimization
//
// Only use this command if you're implementing low-level route execution logic.
type NavigateDirectCommand struct {
	ShipSymbol  string
	Destination string
	PlayerID    shared.PlayerID
	FlightMode  string // Optional, uses ship default if empty
}

// NavigateDirectResponse - Response from navigate direct command
type NavigateDirectResponse struct {
	ArrivalTime    int    // Calculated seconds
	ArrivalTimeStr string // ISO8601 from API (e.g., "2024-01-01T12:00:00Z")
	FuelConsumed   int
	Status         string // "navigating", "already_at_destination"
}

// NavigateDirectHandler - Handles navigate direct commands
type NavigateDirectHandler struct {
	shipRepo     navigation.ShipRepository
	waypointRepo system.WaypointRepository
}

// NewNavigateDirectHandler creates a new navigate direct handler
func NewNavigateDirectHandler(
	shipRepo navigation.ShipRepository,
	waypointRepo system.WaypointRepository,
) *NavigateDirectHandler {
	return &NavigateDirectHandler{
		shipRepo:     shipRepo,
		waypointRepo: waypointRepo,
	}
}

// Handle executes the navigate direct command
func (h *NavigateDirectHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*NavigateDirectCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	if cmd.Destination == "" {
		return nil, fmt.Errorf("invalid destination waypoint")
	}

	ship, err := h.loadShip(ctx, cmd)
	if err != nil {
		return nil, err
	}

	destination, err := h.loadDestinationWaypoint(ctx, cmd.Destination)
	if err != nil {
		return nil, err
	}

	if ship.IsAtLocation(destination) {
		return &NavigateDirectResponse{
			Status: "already_at_destination",
		}, nil
	}

	if _, err = ship.EnsureInOrbit(); err != nil {
		return nil, fmt.Errorf("failed to ensure ship in orbit: %w", err)
	}

	navResult, err := h.shipRepo.Navigate(ctx, ship, destination, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate: %w", err)
	}

	return &NavigateDirectResponse{
		Status:         "navigating",
		ArrivalTime:    navResult.ArrivalTime,
		ArrivalTimeStr: navResult.ArrivalTimeStr,
		FuelConsumed:   navResult.FuelConsumed,
	}, nil
}

func (h *NavigateDirectHandler) loadShip(ctx context.Context, cmd *NavigateDirectCommand) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

func (h *NavigateDirectHandler) loadDestinationWaypoint(ctx context.Context, destinationSymbol string) (*shared.Waypoint, error) {
	systemSymbol := shared.ExtractSystemSymbol(destinationSymbol)
	destination, err := h.waypointRepo.FindBySymbol(ctx, destinationSymbol, systemSymbol)
	if err != nil {
		destination, err = shared.NewWaypoint(destinationSymbol, 0, 0)
		if err != nil {
			return nil, fmt.Errorf("invalid destination: %w", err)
		}
	}
	return destination, nil
}

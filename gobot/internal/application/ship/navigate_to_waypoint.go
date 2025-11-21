package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// NavigateToWaypointCommand - LOW-LEVEL atomic command for single-hop navigation
//
// ⚠️  WARNING: This is a LOW-LEVEL command used internally by RouteExecutor.
// ⚠️  DO NOT use this directly in application workflows!
// ⚠️  Use NavigateShipCommand instead for route planning + multi-hop navigation.
//
// This command performs a simple orbit → navigate → API call without:
// - Route planning
// - Refueling stops
// - Multi-hop navigation
// - Flight mode optimization
//
// Only use this command if you're implementing low-level route execution logic.
type NavigateToWaypointCommand struct {
	ShipSymbol  string
	Destination string
	PlayerID    shared.PlayerID
	FlightMode  string // Optional, uses ship default if empty
}

// NavigateToWaypointResponse - Response from navigate to waypoint command
type NavigateToWaypointResponse struct {
	ArrivalTime    int    // Calculated seconds
	ArrivalTimeStr string // ISO8601 from API (e.g., "2024-01-01T12:00:00Z")
	FuelConsumed   int
	Status         string // "navigating", "already_at_destination"
}

// NavigateToWaypointHandler - Handles navigate to waypoint commands
type NavigateToWaypointHandler struct {
	shipRepo     navigation.ShipRepository
	waypointRepo system.WaypointRepository
}

// NewNavigateToWaypointHandler creates a new navigate to waypoint handler
func NewNavigateToWaypointHandler(
	shipRepo navigation.ShipRepository,
	waypointRepo system.WaypointRepository,
) *NavigateToWaypointHandler {
	return &NavigateToWaypointHandler{
		shipRepo:     shipRepo,
		waypointRepo: waypointRepo,
	}
}

// Handle executes the navigate to waypoint command
func (h *NavigateToWaypointHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*NavigateToWaypointCommand)
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
		return &NavigateToWaypointResponse{
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

	return &NavigateToWaypointResponse{
		Status:         "navigating",
		ArrivalTime:    navResult.ArrivalTime,
		ArrivalTimeStr: navResult.ArrivalTimeStr,
		FuelConsumed:   navResult.FuelConsumed,
	}, nil
}

func (h *NavigateToWaypointHandler) loadShip(ctx context.Context, cmd *NavigateToWaypointCommand) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

func (h *NavigateToWaypointHandler) loadDestinationWaypoint(ctx context.Context, destinationSymbol string) (*shared.Waypoint, error) {
	systemSymbol := extractSystemSymbolFromWaypoint(destinationSymbol)
	destination, err := h.waypointRepo.FindBySymbol(ctx, destinationSymbol, systemSymbol)
	if err != nil {
		destination, err = shared.NewWaypoint(destinationSymbol, 0, 0)
		if err != nil {
			return nil, fmt.Errorf("invalid destination: %w", err)
		}
	}
	return destination, nil
}

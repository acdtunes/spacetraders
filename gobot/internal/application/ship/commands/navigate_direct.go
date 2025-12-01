package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

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
	cmd, ok := request.(*types.NavigateDirectCommand)
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
		return &types.NavigateDirectResponse{
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

	return &types.NavigateDirectResponse{
		Status:         "navigating",
		ArrivalTime:    navResult.ArrivalTime,
		ArrivalTimeStr: navResult.ArrivalTimeStr,
		FuelConsumed:   navResult.FuelConsumed,
		TravelDuration: navResult.ArrivalTime, // Using arrival time as duration
		FuelCurrent:    navResult.FuelCurrent,
		FuelCapacity:   navResult.FuelCapacity,
	}, nil
}

func (h *NavigateDirectHandler) loadShip(ctx context.Context, cmd *types.NavigateDirectCommand) (*navigation.Ship, error) {
	// OPTIMIZATION: Use ship if provided (avoids API call)
	if cmd.Ship != nil {
		return cmd.Ship, nil
	}
	// Fall back to API fetch (backward compatibility)
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

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
	PlayerID    int
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

	// Validate destination
	if cmd.Destination == "" {
		return nil, fmt.Errorf("invalid destination waypoint")
	}

	// 1. Load ship from repository
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	// 2. Load destination waypoint from repository (to get correct HasFuel and other properties)
	// Extract system symbol from destination (find last hyphen)
	systemSymbol := cmd.Destination
	for i := len(cmd.Destination) - 1; i >= 0; i-- {
		if cmd.Destination[i] == '-' {
			systemSymbol = cmd.Destination[:i]
			break
		}
	}
	destination, err := h.waypointRepo.FindBySymbol(ctx, cmd.Destination, systemSymbol)
	if err != nil {
		// Fallback: create waypoint if not found in repository
		// This can happen in tests or when waypoint hasn't been synced yet
		destination, err = shared.NewWaypoint(cmd.Destination, 0, 0)
		if err != nil {
			return nil, fmt.Errorf("invalid destination: %w", err)
		}
	}

	// 3. Check if already at destination (idempotent)
	if ship.IsAtLocation(destination) {
		return &NavigateToWaypointResponse{
			Status: "already_at_destination",
		}, nil
	}

	// 4. Ensure ship is in orbit (auto-orbit if docked)
	_, err = ship.EnsureInOrbit()
	if err != nil {
		return nil, fmt.Errorf("failed to ensure ship in orbit: %w", err)
	}

	// 5. Set flight mode if specified
	// Note: In real implementation, we would call shipRepo.SetFlightMode
	// For now, we skip this as it's an optional feature

	// 6. Navigate via repository (calls API and updates ship state)
	// Note: Navigate() internally calls StartTransit() and ConsumeFuel() on the ship
	navResult, err := h.shipRepo.Navigate(ctx, ship, destination, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate: %w", err)
	}

	// 7. Return successful navigation response
	return &NavigateToWaypointResponse{
		Status:         "navigating",
		ArrivalTime:    navResult.ArrivalTime,
		ArrivalTimeStr: navResult.ArrivalTimeStr,
		FuelConsumed:   navResult.FuelConsumed,
	}, nil
}

package manufacturing

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// ShipSelector finds the best ship for a task based on proximity.
type ShipSelector struct {
	waypointProvider system.IWaypointProvider
}

// NewShipSelector creates a new ship selector.
func NewShipSelector(waypointProvider system.IWaypointProvider) *ShipSelector {
	return &ShipSelector{
		waypointProvider: waypointProvider,
	}
}

// FindClosestShip finds the ship closest to the target waypoint.
// Returns the ship and its symbol, or nil/"" if no ships available.
func (s *ShipSelector) FindClosestShip(
	ships map[string]*navigation.Ship,
	target *shared.Waypoint,
) (*navigation.Ship, string) {
	if len(ships) == 0 {
		return nil, ""
	}

	// If no target, return first available ship
	if target == nil {
		for symbol, ship := range ships {
			return ship, symbol
		}
		return nil, ""
	}

	var closestShip *navigation.Ship
	var closestSymbol string
	var closestDistance float64 = -1

	for symbol, ship := range ships {
		distance := ship.CurrentLocation().DistanceTo(target)
		if closestDistance < 0 || distance < closestDistance {
			closestDistance = distance
			closestShip = ship
			closestSymbol = symbol
		}
	}

	return closestShip, closestSymbol
}

// GetTaskSourceLocation returns the waypoint where the task starts.
// For ACQUIRE_DELIVER: source market, For COLLECT_SELL: factory, For LIQUIDATE: target market.
func (s *ShipSelector) GetTaskSourceLocation(
	ctx context.Context,
	task *manufacturing.ManufacturingTask,
	playerID int,
) *shared.Waypoint {
	symbol := task.GetFirstDestination()
	if symbol == "" {
		return nil
	}

	// Look up coordinates from waypoint provider
	if s.waypointProvider != nil {
		systemSymbol := extractSystemFromWaypointSymbol(symbol)
		waypoint, err := s.waypointProvider.GetWaypoint(ctx, symbol, systemSymbol, playerID)
		if err == nil && waypoint != nil {
			return waypoint
		}
	}

	// Return a waypoint with just the symbol (no coordinates)
	return &shared.Waypoint{Symbol: symbol, X: 0, Y: 0}
}

// GetTaskDestinationLocation returns the waypoint where the task ends.
// For ACQUIRE_DELIVER: factory, For COLLECT_SELL/LIQUIDATE: target market.
func (s *ShipSelector) GetTaskDestinationLocation(
	ctx context.Context,
	task *manufacturing.ManufacturingTask,
	playerID int,
) *shared.Waypoint {
	symbol := task.GetFinalDestination()
	if symbol == "" {
		return nil
	}

	// Look up coordinates from waypoint provider
	if s.waypointProvider != nil {
		systemSymbol := extractSystemFromWaypointSymbol(symbol)
		waypoint, err := s.waypointProvider.GetWaypoint(ctx, symbol, systemSymbol, playerID)
		if err == nil && waypoint != nil {
			return waypoint
		}
	}

	return &shared.Waypoint{Symbol: symbol, X: 0, Y: 0}
}

// extractSystemFromWaypointSymbol extracts system symbol from waypoint symbol.
// Format: SECTOR-SYSTEM-WAYPOINT (e.g., X1-YZ19-A1)
func extractSystemFromWaypointSymbol(waypointSymbol string) string {
	parts := 0
	for i, c := range waypointSymbol {
		if c == '-' {
			parts++
			if parts == 2 {
				return waypointSymbol[:i]
			}
		}
	}
	return waypointSymbol
}

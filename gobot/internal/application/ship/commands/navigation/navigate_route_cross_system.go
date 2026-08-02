package navigation

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// tryCrossSystemNavigate handles a destination in a DIFFERENT system than the ship.
// The intra-system route planner cannot move a hull across systems, so without this seam
// a bare cross-system NavigateRouteCommand fails closed at validateWaypointCache
// ("waypoint <dest> not found in cache for system <current>"). Both live victims — the
// contract worker sourcing a home good from another system, and the frontier's
// target-aware probe-buy navigating an idle hull from home to a frontier shipyard —
// converge on this one seam.
//
// It returns handled=false (deferring to the unchanged in-system path) in three cases,
// so the seam is strictly additive:
//   - no cross-system router is wired;
//   - the destination is in the ship's CURRENT system (an ordinary in-system navigate);
//   - the destination is genuinely UNKNOWN even after an on-demand sync — the existing
//     validateWaypointCache then produces the exact same fail-closed error (last resort).
//
// Otherwise it delegates the physical, gate-crossing move to the shared travel machinery
// (RepositionToWaypoint — the SAME multi-jump path arb/trade/scout circuits ride) and
// reports the completed navigation.
func (h *NavigateRouteHandler) tryCrossSystemNavigate(
	ctx context.Context,
	cmd *NavigateRouteCommand,
	ship *domainNavigation.Ship,
	logger common.ContainerLogger,
) (common.Response, bool, error) {
	if h.crossSystemRouter == nil {
		return nil, false, nil
	}

	currentSystem := ship.CurrentLocation().SystemSymbol
	destinationSystem := shared.ExtractSystemSymbol(cmd.Destination)
	if destinationSystem == currentSystem {
		return nil, false, nil
	}

	if !h.crossSystemDestinationResolvable(ctx, cmd, destinationSystem, logger) {
		// Genuinely unknown even after a sync: defer to the existing fail-closed
		// validation rather than committing a hull to a journey to a phantom waypoint.
		return nil, false, nil
	}

	logger.Log("INFO", "Cross-system destination - delegating to gate-crossing travel", map[string]interface{}{
		"ship_symbol":        cmd.ShipSymbol,
		"action":             "cross_system_navigate",
		"current_system":     currentSystem,
		"destination":        cmd.Destination,
		"destination_system": destinationSystem,
	})

	if err := h.crossSystemRouter.RepositionToWaypoint(ctx, cmd.ShipSymbol, cmd.Destination, cmd.PlayerID.Value()); err != nil {
		return nil, true, fmt.Errorf("failed to navigate %s to cross-system destination %s: %w", cmd.ShipSymbol, cmd.Destination, err)
	}

	response, err := h.buildCrossSystemArrivalResponse(ctx, cmd, ship)
	return response, true, err
}

// crossSystemDestinationResolvable reports whether cmd.Destination is a REAL, known
// waypoint in its own system, auto-syncing that system's graph on-demand. It is
// the production-safety gate: a hull is committed to a multi-jump journey only to a
// destination the graph provider can confirm exists.
//
// Frugality: the first read is CACHE-FIRST (forceRefresh=false), so a warm cache — e.g.
// the same frontier target quoted twice in a cycle — costs zero API and the system is
// synced at most once per window. A force-refresh fires at most ONCE, and only when the
// cache-first read still missed the waypoint (a stale/partial cache), mirroring the
// in-system self-heal. A nil or unreadable provider fails closed (returns false).
func (h *NavigateRouteHandler) crossSystemDestinationResolvable(
	ctx context.Context,
	cmd *NavigateRouteCommand,
	destinationSystem string,
	logger common.ContainerLogger,
) bool {
	if h.graphProvider == nil {
		return false
	}
	if h.destinationInSystemGraph(ctx, cmd.Destination, destinationSystem, cmd.PlayerID.Value(), false) {
		return true
	}

	logger.Log("INFO", "Cross-system destination absent from cache - force-syncing its system", map[string]interface{}{
		"ship_symbol":        cmd.ShipSymbol,
		"action":             "cross_system_auto_sync",
		"destination":        cmd.Destination,
		"destination_system": destinationSystem,
	})
	return h.destinationInSystemGraph(ctx, cmd.Destination, destinationSystem, cmd.PlayerID.Value(), true)
}

// destinationInSystemGraph fetches the destination's own system graph via the shared
// provider (cache-first when forceRefresh is false) and reports whether the waypoint is
// present. Any provider error or empty graph reads as "not resolvable" (fail closed).
func (h *NavigateRouteHandler) destinationInSystemGraph(
	ctx context.Context,
	destination, systemSymbol string,
	playerID int,
	forceRefresh bool,
) bool {
	result, err := h.graphProvider.GetGraph(ctx, systemSymbol, forceRefresh, playerID)
	if err != nil || result == nil || result.Graph == nil {
		return false
	}
	return result.Graph.HasWaypoint(destination)
}

// buildCrossSystemArrivalResponse reports a completed cross-system navigation. The shared
// travel machinery has already moved and persisted the hull, so this reloads it for the
// caller (delivery_executor reads NavigateRouteResponse.Ship) and reports the destination
// as the current location. The route is an empty completed route, mirroring
// handleAlreadyAtDestination, so callers that inspect Route never dereference nil.
func (h *NavigateRouteHandler) buildCrossSystemArrivalResponse(
	ctx context.Context,
	cmd *NavigateRouteCommand,
	ship *domainNavigation.Ship,
) (*NavigateRouteResponse, error) {
	arrived := ship
	if reloaded, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID); err == nil && reloaded != nil {
		arrived = reloaded
	}

	route, err := domainNavigation.NewRoute(
		fmt.Sprintf("%s_cross_system", cmd.ShipSymbol),
		cmd.ShipSymbol,
		cmd.PlayerID.Value(),
		[]*domainNavigation.RouteSegment{},
		arrived.FuelCapacity(),
		false,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build cross-system route record: %w", err)
	}
	route.StartExecution()
	route.CompleteSegment()

	return &NavigateRouteResponse{
		Status:          "completed",
		CurrentLocation: cmd.Destination,
		FuelRemaining:   arrived.Fuel().Current,
		Route:           route,
		Ship:            arrived,
	}, nil
}

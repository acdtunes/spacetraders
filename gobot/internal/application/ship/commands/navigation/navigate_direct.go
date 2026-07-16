package navigation

import (
	"context"
	"fmt"
	"strings"

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

	ship, err := types.LoadShip(ctx, h.shipRepo, cmd)
	if err != nil {
		return nil, err
	}

	destination, err := h.loadDestinationWaypoint(ctx, cmd)
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

	navResult, err := h.navigateWithOrbitSelfHeal(ctx, ship, destination, cmd.PlayerID)
	if err != nil {
		// The server reports error 4204 ("ship is currently located at the
		// destination") when the daemon's cached position lags the game server
		// by one waypoint. The navigate is effectively a no-op - the ship IS
		// already at the destination - so treat it as success, reconcile the
		// stale cache from authoritative server state, and let the tour continue
		// instead of crash-looping on the phantom hop.
		if isAlreadyAtDestinationError(err) {
			return h.reconcileAtDestination(ctx, cmd, ship), nil
		}
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

// navigateWithOrbitSelfHeal navigates, self-healing a wrong idempotent-orbit
// skip (sp-yd84 SAFETY item 2). The idempotent orbit optimization (CUT 1) trusts
// the in-memory NavStatus; if that has drifted from server reality the skipped
// orbit leaves the ship docked, and the navigate is rejected with the live API's
// 4236 "not currently in orbit". Rather than fail the leg, issue a REAL orbit
// (h.shipRepo.Orbit fires the API unconditionally, correcting the drift) and
// retry the navigate exactly once. Any other error — including 4204
// already-at-destination, handled by the caller — is propagated unchanged so a
// genuine failure is never masked. Mirrors jumpWithOrbitRetry (sp-28n2).
func (h *NavigateDirectHandler) navigateWithOrbitSelfHeal(ctx context.Context, ship *navigation.Ship, destination *shared.Waypoint, playerID shared.PlayerID) (*navigation.Result, error) {
	navResult, err := h.shipRepo.Navigate(ctx, ship, destination, playerID)
	if err == nil {
		return navResult, nil
	}
	if !isNotInOrbitError(err) {
		return nil, err
	}

	common.LoggerFromContext(ctx).Log("WARNING", "Navigate rejected as not-in-orbit (4236) - orbiting live and retrying (idempotent-skip drift self-heal, sp-yd84)", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "navigate_orbit_self_heal",
		"destination": destination.Symbol,
	})

	if oerr := h.shipRepo.Orbit(ctx, ship, playerID); oerr != nil {
		return nil, fmt.Errorf("self-heal orbit after a not-in-orbit navigate rejection failed: %w", oerr)
	}
	return h.shipRepo.Navigate(ctx, ship, destination, playerID)
}

// reconcileAtDestination handles a server-reported "already at destination"
// (API 4204). It refreshes ship state from GET /my/ships so the position cache
// stops lagging the server, then reports the navigate as a no-op success.
func (h *NavigateDirectHandler) reconcileAtDestination(ctx context.Context, cmd *types.NavigateDirectCommand, ship *navigation.Ship) *types.NavigateDirectResponse {
	// Best-effort reconcile: if the refresh fails, still report success so the
	// tour continues; the next arrival will re-sync the cache anyway.
	if fresh, err := h.shipRepo.SyncShipFromAPI(ctx, ship.ShipSymbol(), cmd.PlayerID); err == nil && fresh != nil {
		*ship = *fresh
	}
	return &types.NavigateDirectResponse{
		Status:       "already_at_destination",
		FuelCurrent:  ship.Fuel().Current,
		FuelCapacity: ship.Fuel().Capacity,
	}
}

// isAlreadyAtDestinationError reports whether the API rejected a navigate
// because the ship is already located at the requested waypoint (error 4204).
func isAlreadyAtDestinationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "4204") || strings.Contains(msg, "located at the destination")
}

func (h *NavigateDirectHandler) loadDestinationWaypoint(ctx context.Context, cmd *types.NavigateDirectCommand) (*shared.Waypoint, error) {
	// Primary: use provided waypoint (avoids DB lookup, has correct HasFuel)
	if cmd.DestinationWaypoint != nil {
		return cmd.DestinationWaypoint, nil
	}

	// Fallback: lookup from database
	destinationSymbol := cmd.Destination
	systemSymbol := shared.ExtractSystemSymbol(destinationSymbol)
	destination, err := h.waypointRepo.FindBySymbol(ctx, destinationSymbol, systemSymbol)
	if err != nil {
		// Last resort fallback: create minimal waypoint
		// WARNING: This waypoint won't have HasFuel set correctly
		destination, err = shared.NewWaypoint(destinationSymbol, 0, 0)
		if err != nil {
			return nil, fmt.Errorf("invalid destination: %w", err)
		}
	}
	return destination, nil
}

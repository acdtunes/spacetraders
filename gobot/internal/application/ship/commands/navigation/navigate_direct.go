package navigation

import (
	"context"
	"fmt"
	"strings"
	"time"

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
		// 4214 "ship is in transit": the server holds a transit the local
		// snapshot never recorded (typically a navigate whose response was
		// lost after the server had applied it, silently re-sent by the
		// client's network-error retry). The server nav is authoritative -
		// reconcile from it instead of surfacing an error the container
		// crash-loops on for the rest of the transit.
		if types.IsShipInTransitAPIError(err) {
			return h.reconcileInTransit(ctx, cmd, ship, err)
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

// reconcileInTransit handles a navigate rejected with 4214 ("ship is in
// transit"). It pulls the authoritative nav from the server and adopts it into
// the caller's ship (local state must never lead the server's):
//   - transit already heading to THIS command's destination => the earlier
//     send succeeded and only its response was lost; report the navigation as
//     in progress with the SERVER's arrival so the caller waits it out;
//   - hull already parked at the destination (the transit ended in the race
//     window) => no-op success, mirroring the 4204 reconcile;
//   - transit heading elsewhere => typed ErrShipInTransit so route-level
//     callers wait out the adopted transit instead of failing the container.
//
// Without the authoritative read nothing is adopted and the original
// rejection propagates unchanged - never fabricate nav state from a guess.
func (h *NavigateDirectHandler) reconcileInTransit(ctx context.Context, cmd *types.NavigateDirectCommand, ship *navigation.Ship, cause error) (common.Response, error) {
	fresh, syncErr := h.shipRepo.SyncShipFromAPI(ctx, ship.ShipSymbol(), cmd.PlayerID)
	if syncErr != nil || fresh == nil {
		common.LoggerFromContext(ctx).Log("WARNING", "Navigate rejected as in-transit (4214) but the authoritative resync failed - propagating the rejection", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "navigate_in_transit_resync_failed",
			"destination": cmd.Destination,
		})
		return nil, fmt.Errorf("failed to navigate: %w", cause)
	}
	*ship = *fresh

	atDestination := ship.CurrentLocation() != nil && ship.CurrentLocation().Symbol == cmd.Destination
	if ship.NavStatus() != navigation.NavStatusInTransit {
		if atDestination {
			return &types.NavigateDirectResponse{
				Status:       "already_at_destination",
				FuelCurrent:  ship.Fuel().Current,
				FuelCapacity: ship.Fuel().Capacity,
			}, nil
		}
		return nil, fmt.Errorf("failed to navigate: %w", cause)
	}

	var arrival time.Time
	arrivalStr := ""
	if at := ship.ArrivalTime(); at != nil {
		arrival = *at
		arrivalStr = at.UTC().Format(time.RFC3339)
	}
	// While IN_TRANSIT, CurrentLocation() is the transit destination.
	if atDestination {
		common.LoggerFromContext(ctx).Log("WARNING", "Navigate rejected as in-transit (4214) toward this very destination - adopting the server transit and reporting the navigation in progress", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "navigate_in_transit_adopted_same_destination",
			"destination": cmd.Destination,
			"arrival":     arrivalStr,
		})
		return &types.NavigateDirectResponse{
			Status:         "navigating",
			ArrivalTimeStr: arrivalStr,
			FuelCurrent:    ship.Fuel().Current,
			FuelCapacity:   ship.Fuel().Capacity,
		}, nil
	}

	transitDest := ""
	if loc := ship.CurrentLocation(); loc != nil {
		transitDest = loc.Symbol
	}
	common.LoggerFromContext(ctx).Log("WARNING", "Navigate rejected as in-transit (4214) toward another waypoint - adopted the server transit for the caller to wait out", map[string]interface{}{
		"ship_symbol":         ship.ShipSymbol(),
		"action":              "navigate_in_transit_adopted_elsewhere",
		"requested":           cmd.Destination,
		"transit_destination": transitDest,
		"arrival":             arrivalStr,
	})
	return nil, &types.ErrShipInTransit{
		ShipSymbol:  ship.ShipSymbol(),
		Destination: transitDest,
		Arrival:     arrival,
		Cause:       cause,
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

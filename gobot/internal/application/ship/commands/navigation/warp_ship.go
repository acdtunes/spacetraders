package navigation

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// WarpLegExecutor is the off-gate warp port the verb rides: warp this hull to this
// waypoint in another system, enforcing the executor's own fail-closed guards. It is
// satisfied by *ship.RouteExecutor. Narrowing the dependency to the single method
// states exactly what the verb touches — one guarded leg, no routing logic of its own.
type WarpLegExecutor interface {
	ExecuteWarpLeg(ctx context.Context, ship *domainNavigation.Ship, destination *shared.Waypoint, playerID shared.PlayerID) error
}

// WarpShipLoader loads the hull to warp (satisfied by domainNavigation.ShipRepository).
type WarpShipLoader interface {
	FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*domainNavigation.Ship, error)
}

// WarpDestinationSource lists a system's waypoints so an operator-typed destination
// symbol can be placed (satisfied by *ship.GraphWaypointSource). It fetches through
// on a cache miss, which matters here: a warp target is frequently in a system the
// fleet has never charted, and a cache-only lookup would refuse every such leg.
type WarpDestinationSource interface {
	ChartWaypoints(ctx context.Context, systemSymbol string, playerID shared.PlayerID) ([]*shared.Waypoint, error)
}

// WarpShipCommand is a one-shot off-gate move: warp ShipSymbol to Destination, a
// waypoint in another system reachable without a jump gate. It is the operator-facing
// primitive behind `spacetraders ship warp` — until it existed a warp could only
// happen as a side effect of any coordinator choosing to send a hull
// off-gate, so warp could not be commanded and therefore could not be measured.
type WarpShipCommand struct {
	ShipSymbol  string
	Destination string // a waypoint in another system, off the jump-gate network
	PlayerID    shared.PlayerID
}

// WarpShipResponse reports the completed leg. FuelRemaining is the post-warp fuel the
// executor folded back onto the hull — the number a warp is flown to observe.
type WarpShipResponse struct {
	ShipSymbol    string
	Destination   string
	FuelRemaining int
}

// WarpShipHandler warps a claimed hull to a waypoint in another system by delegating
// the leg to the existing warp executor. It is deliberately thin: it resolves the hull
// and the destination waypoint, then hands both to ExecuteWarpLeg. No warp-capability
// check, fuel arithmetic or strand test lives here — every warp goes through the
// executor's guards, and their typed refusals are surfaced unflattened.
type WarpShipHandler struct {
	warps        WarpLegExecutor
	ships        WarpShipLoader
	destinations WarpDestinationSource
}

// NewWarpShipHandler wires the warp verb over the warp executor, the hull loader and
// the destination-waypoint source.
func NewWarpShipHandler(warps WarpLegExecutor, ships WarpShipLoader, destinations WarpDestinationSource) *WarpShipHandler {
	return &WarpShipHandler{warps: warps, ships: ships, destinations: destinations}
}

// Handle executes the one-shot warp. A refusal or failure is returned so the container
// FAILS honestly (the runner releases the claim and logs the cause verbatim) rather
// than reporting a move that never happened.
func (h *WarpShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*WarpShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type %T for WarpShipHandler", request)
	}

	hull, err := h.loadHull(ctx, cmd)
	if err != nil {
		return nil, err
	}
	destination, err := h.resolveDestination(ctx, cmd)
	if err != nil {
		return nil, err
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf("Warping %s to %s (off-gate)", cmd.ShipSymbol, cmd.Destination), map[string]interface{}{
		"action":      "ship_warp_start",
		"ship_symbol": cmd.ShipSymbol,
		"destination": cmd.Destination,
		"fuel_before": hull.Fuel().Current,
	})

	// The wrap adds only the hull's identity; %w keeps ErrShipHasNoWarpDrive and
	// ErrWarpWouldStrand recoverable and their messages — including the strand's
	// required/available/capacity numbers — readable by the operator.
	if err := h.warps.ExecuteWarpLeg(ctx, hull, destination, cmd.PlayerID); err != nil {
		return nil, fmt.Errorf("warp of %s: %w", cmd.ShipSymbol, err)
	}

	logger.Log("INFO", fmt.Sprintf("%s warped to %s", cmd.ShipSymbol, cmd.Destination), map[string]interface{}{
		"action":      "ship_warp_complete",
		"ship_symbol": cmd.ShipSymbol,
		"destination": cmd.Destination,
		"fuel_after":  hull.Fuel().Current,
	})

	return &WarpShipResponse{
		ShipSymbol:    cmd.ShipSymbol,
		Destination:   cmd.Destination,
		FuelRemaining: hull.Fuel().Current,
	}, nil
}

func (h *WarpShipHandler) loadHull(ctx context.Context, cmd *WarpShipCommand) (*domainNavigation.Ship, error) {
	hull, err := h.ships.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship %s not loadable for warp: %w", cmd.ShipSymbol, err)
	}
	if hull == nil {
		return nil, fmt.Errorf("ship %s not found for warp", cmd.ShipSymbol)
	}
	return hull, nil
}

// resolveDestination places the operator-typed waypoint symbol in its own system.
// It fails CLOSED on an unknown waypoint: a warp is issued to a concrete destination,
// so one that cannot be placed must never be approximated.
func (h *WarpShipHandler) resolveDestination(ctx context.Context, cmd *WarpShipCommand) (*shared.Waypoint, error) {
	systemSymbol := shared.ExtractSystemSymbol(cmd.Destination)
	waypoints, err := h.destinations.ChartWaypoints(ctx, systemSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve warp destination %s in system %s: %w", cmd.Destination, systemSymbol, err)
	}
	for _, waypoint := range waypoints {
		if waypoint != nil && waypoint.Symbol == cmd.Destination {
			return waypoint, nil
		}
	}
	return nil, fmt.Errorf("warp destination %s is not a known waypoint of system %s — no warp attempted", cmd.Destination, systemSymbol)
}

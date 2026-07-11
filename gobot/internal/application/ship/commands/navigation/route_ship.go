package navigation

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// CrossSystemRouter is the narrow movement port the route verb rides (sp-6hjw):
// fly shipSymbol to destinationWaypoint, crossing gates as needed. It is satisfied
// by the trade-route coordinator's exported RepositionToWaypoint, which delegates to
// the SAME multi-jump travel() the arb/trade/scout circuits use (gate-graph BFS +
// per-hop cooldown waits + source/arrival gate hops). Narrowing the dependency to
// this one strict method keeps the verb testable with a tiny fake and states exactly
// what it touches: movement, nothing else — no buying, selling, or bookkeeping. It
// deliberately uses the STRICT fetch-through resolver (not the expendable-probe
// bounded variant): a route verb moves a real, kept hull, so an unroutable
// destination must fail closed rather than route past unreadable frontier gates.
type CrossSystemRouter interface {
	RepositionToWaypoint(ctx context.Context, shipSymbol, destinationWaypoint string, playerID int) error
}

// RouteShipCommand is a one-shot cross-system point-to-point move: fly ShipSymbol to
// Destination in ANY reachable system (sp-6hjw). It is the operator-facing primitive
// behind `spacetraders ship route` — the gap that made every manual cross-gate hull
// move (warehouse dispatch, spare repositioning, era-end consolidation) a fragile
// hand-rolled navigate-to-gate + jump + navigate. Unlike NavigateRouteCommand (which
// plans within the ship's CURRENT system only and fails cross-system with "waypoint
// not found in cache for system X"), this crosses gates by reusing the shared travel
// machinery; unlike JumpShipCommand (a single gate hop) it resolves the whole
// multi-jump path itself.
type RouteShipCommand struct {
	ShipSymbol  string
	Destination string // a waypoint in any reachable system
	PlayerID    shared.PlayerID
}

// RouteShipResponse reports the completed move. Because the verb is one-shot and the
// container wraps a single iteration, it is observed only on completion.
type RouteShipResponse struct {
	ShipSymbol  string
	Destination string
}

// RouteShipHandler flies a claimed hull to a waypoint in any reachable system by
// delegating to the shared multi-jump travel machinery (sp-6hjw). It is deliberately
// tiny: all it does is forward one move and report — no route planning or jump logic
// lives here (REUSE ruling). The container it runs under already claims the hull
// (container_ops_ship.go metadata "ship_symbol"), so travel()'s SkipClaim jumps trust
// that claim exactly as the trade/arb circuits and scout/ferry reposition workers do.
type RouteShipHandler struct {
	router CrossSystemRouter
}

// NewRouteShipHandler wires the route verb with the cross-system movement port.
func NewRouteShipHandler(router CrossSystemRouter) *RouteShipHandler {
	return &RouteShipHandler{router: router}
}

// Handle executes the one-shot cross-system move. A travel error is returned so the
// container FAILS honestly (the runner releases the claim) rather than reporting a
// false success on a hull left stranded mid-route.
func (h *RouteShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RouteShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type %T for RouteShipHandler", request)
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf("Routing %s to %s (cross-system point-to-point)", cmd.ShipSymbol, cmd.Destination), map[string]interface{}{
		"action":      "ship_route_start",
		"ship_symbol": cmd.ShipSymbol,
		"destination": cmd.Destination,
	})

	if err := h.router.RepositionToWaypoint(ctx, cmd.ShipSymbol, cmd.Destination, cmd.PlayerID.Value()); err != nil {
		return nil, fmt.Errorf("route of %s to %s failed: %w", cmd.ShipSymbol, cmd.Destination, err)
	}

	logger.Log("INFO", fmt.Sprintf("%s routed to %s", cmd.ShipSymbol, cmd.Destination), map[string]interface{}{
		"action":      "ship_route_complete",
		"ship_symbol": cmd.ShipSymbol,
		"destination": cmd.Destination,
	})

	return &RouteShipResponse{
		ShipSymbol:  cmd.ShipSymbol,
		Destination: cmd.Destination,
	}, nil
}

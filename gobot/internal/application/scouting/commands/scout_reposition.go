package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Repositioner is the narrow movement port the reposition worker rides (sp-s232):
// fly shipSymbol to destinationWaypoint, crossing gates as needed. It is satisfied
// by the trade-route coordinator's exported RepositionToWaypoint, which delegates to
// the SAME multi-jump travel() the arb/trade circuits use — the coordinator reuses
// that machinery rather than re-implementing jump logic (RULINGS: reuse). Narrowing
// the dependency to this one method keeps the worker testable with a tiny fake and
// states exactly what it touches: movement, nothing else.
type Repositioner interface {
	RepositionToWaypoint(ctx context.Context, shipSymbol, destinationWaypoint string, playerID int) error
}

// ScoutRepositionCommand is a one-shot cross-gate relay: jump-route a claimed idle
// satellite to DestinationWaypoint in an unmanned post's system (sp-s232). The
// scout_post_coordinator spawns it as a managed worker (like a scout_tour) — the
// satellite is already claimed to this container, so the relay owns the hull for the
// whole flight and nothing poaches it mid-jump (RULINGS #7). On arrival the container
// exits and the next in-system reconcile mans the post; manning stays in-system only
// (the sp-qxa4 invariant is untouched — this just moves the hull there first).
type ScoutRepositionCommand struct {
	PlayerID            shared.PlayerID
	ShipSymbol          string
	DestinationWaypoint string // a waypoint in the target post's system (a discovered market)

	// CoordinatorID names the scout_post_coordinator that spawned this relay as a
	// managed worker. Persisted into the container config so daemon restart recovery
	// SKIPS it (marks it worker_interrupted, preserving the ship claim) and leaves
	// re-dispatch to the coordinator's reconcile pass — the scout_tour worker pattern
	// (sp-cxpq). A restart re-dispatches from the hull's CURRENT position: travel()
	// waits out any in-transit leg and re-plans the gate path, so a mid-relay restart
	// resumes rather than strands (RULINGS #2).
	CoordinatorID string
}

// ScoutRepositionResponse reports the completed relay. Because the relay is one-shot
// and the container wraps a single iteration, it is observed only on completion.
type ScoutRepositionResponse struct {
	ShipSymbol          string
	DestinationWaypoint string
}

// ScoutRepositionHandler flies a claimed satellite to an unmanned post's system by
// delegating to the shared multi-jump travel machinery (sp-s232). It is deliberately
// tiny: all reposition bookkeeping (nearest-by-hops selection, the claim, the
// one-relay-per-post dedupe, backoff) lives in the coordinator's reconcile; this
// worker just moves the hull and reports.
type ScoutRepositionHandler struct {
	repositioner Repositioner
}

// NewScoutRepositionHandler wires the reposition worker with the movement port.
func NewScoutRepositionHandler(repositioner Repositioner) *ScoutRepositionHandler {
	return &ScoutRepositionHandler{repositioner: repositioner}
}

// Handle executes the one-shot cross-gate relay. A travel error is returned so the
// container FAILS honestly (the runner releases the claim; the coordinator re-parks
// the post for a bounded retry) rather than reporting a false success.
func (h *ScoutRepositionHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*ScoutRepositionCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf("Repositioning satellite %s to %s (cross-gate relay)", cmd.ShipSymbol, cmd.DestinationWaypoint), map[string]interface{}{
		"action":      "scout_reposition_start",
		"ship_symbol": cmd.ShipSymbol,
		"destination": cmd.DestinationWaypoint,
	})

	if err := h.repositioner.RepositionToWaypoint(ctx, cmd.ShipSymbol, cmd.DestinationWaypoint, cmd.PlayerID.Value()); err != nil {
		return nil, fmt.Errorf("scout reposition of %s to %s failed: %w", cmd.ShipSymbol, cmd.DestinationWaypoint, err)
	}

	logger.Log("INFO", fmt.Sprintf("Satellite %s repositioned to %s — ready for in-system manning", cmd.ShipSymbol, cmd.DestinationWaypoint), map[string]interface{}{
		"action":      "scout_reposition_complete",
		"ship_symbol": cmd.ShipSymbol,
		"destination": cmd.DestinationWaypoint,
	})

	return &ScoutRepositionResponse{
		ShipSymbol:          cmd.ShipSymbol,
		DestinationWaypoint: cmd.DestinationWaypoint,
	}, nil
}

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
	// RepositionToWaypointWithinJumps flies a claimed satellite across gates to
	// destinationWaypoint, resolving the jump path over the PERSISTED stored adjacency
	// bounded to maxJumps (sp-8k9m) — routing PAST unreadable frontier gates so an
	// expendable probe can reach a post the strict fetch-through cap rejects. maxJumps
	// <= 0 degrades to the strict resolver.
	RepositionToWaypointWithinJumps(ctx context.Context, shipSymbol, destinationWaypoint string, playerID, maxJumps int) error

	// RepositionToSystemGateAndChart flies shipSymbol onto its CURRENT system's jump gate and
	// charts that gate on arrival (sp-4yse). It is the 0-hop gate-reconcile path: a probe already
	// IN a market-known-but-gate-dark system that the plain same-system reposition would leave at
	// a market, never charting the gate. Scoped to the reconcile's charting intent — normal
	// same-system travel never reaches it — and best-effort (a chart failure never fails the relay).
	RepositionToSystemGateAndChart(ctx context.Context, shipSymbol string, playerID, maxJumps int) error
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

	// MaxRepositionJumps bounds the stored-adjacency jump path this relay may resolve
	// (sp-8k9m [scouting] max_reposition_jumps). It is the expendable-probe reach past
	// the strict fetch-through cap; <= 0 degrades to the strict resolver. Persisted with
	// the container so a restart re-dispatches at the same reach.
	MaxRepositionJumps int

	// ChartGateOnArrival marks a gate-reconcile relay that must chart the TARGET system's jump
	// gate on arrival (sp-4yse). The gate-reconcile sweep sets it for 0-hop dispatches only — a
	// probe already IN the market-known-but-gate-dark system, which travelWithJumpBound's
	// same-system branch would otherwise leave at a market, never charting the gate (the
	// VH23/TD90 stuck-marker hole). Set false, the relay is the plain manning/reposition move
	// (byte-for-byte the pre-sp-4yse behavior), so a normal reposition never detours to the gate.
	// Persisted so the flag survives the persist->config->rebuild boundary the worker's start
	// path runs through (StartScoutReposition rebuilds the command from config, not from memory).
	ChartGateOnArrival bool
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

	// sp-4yse: a gate-reconcile relay flagged ChartGateOnArrival routes the present probe onto
	// its OWN system's jump gate and charts it (the 0-hop path travelWithJumpBound's same-system
	// branch leaves uncharted); every other relay takes the plain market reposition unchanged.
	// Exactly ONE move fires — the charting path must never also run the plain reposition.
	relayMove := func() error {
		if cmd.ChartGateOnArrival {
			return h.repositioner.RepositionToSystemGateAndChart(ctx, cmd.ShipSymbol, cmd.PlayerID.Value(), cmd.MaxRepositionJumps)
		}
		return h.repositioner.RepositionToWaypointWithinJumps(ctx, cmd.ShipSymbol, cmd.DestinationWaypoint, cmd.PlayerID.Value(), cmd.MaxRepositionJumps)
	}
	if err := relayMove(); err != nil {
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

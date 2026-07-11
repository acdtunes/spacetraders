package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Repositioner is the narrow movement port the worker ferry rides (sp-f5pr): fly
// shipSymbol to destinationWaypoint, crossing gates as needed. It is satisfied by the
// trade-route coordinator's exported RepositionToWaypointWithinJumps, which delegates to
// the SAME multi-jump travel() the arb/trade circuits use — the ferry reuses that
// machinery rather than re-implementing jump logic (RULINGS: reuse). Narrowing the
// dependency to this one method keeps the worker testable with a tiny fake and states
// exactly what it touches: movement, nothing else. (Twin of the scouting package's
// Repositioner; the trading/commands package has no Repositioner interface of its own —
// only the methods on RunTradeRouteCoordinatorHandler — so it is declared here, mirroring
// scout_reposition.)
type Repositioner interface {
	// RepositionToWaypointWithinJumps flies a claimed hull across gates to
	// destinationWaypoint, resolving the cross-system jump path over the PERSISTED stored
	// adjacency bounded to maxJumps (RepositionPath, sp-8k9m) rather than the strict
	// fetch-through Path — routing PAST unreadable frontier gates so a ferry can reach a
	// worker-starved factory system the strict cap rejects (sp-fwxm/sp-kl16). A ferry is a
	// hull MOVE (no money commitment), so it earns the same relaxation as a tour/scout
	// reposition; maxJumps <= 0 degrades to the strict resolver, so a mis-wired caller can
	// never accidentally relax. Buy/delivery routing stays strict — the guard line is
	// money-commitment vs hull-movement.
	RepositionToWaypointWithinJumps(ctx context.Context, shipSymbol, destinationWaypoint string, playerID, maxJumps int) error
}

// WorkerFerryCommand is a one-shot cross-system relay: jump-route a claimed idle
// light-hauler to DestinationWaypoint in a worker-starved factory system (sp-f5pr). The
// worker_rebalancer_coordinator spawns it as a managed worker (like a scout_reposition
// relay) — the hull is already claimed to this container, so the ferry owns it for the
// whole flight and nothing poaches it mid-jump (RULINGS #7). On arrival the container
// exits and the destination factory's own idle-hauler discovery claims the now-idle hull
// in-system; the ferry just moves it there first.
type WorkerFerryCommand struct {
	PlayerID            shared.PlayerID
	ShipSymbol          string
	DestinationWaypoint string // a marketplace waypoint in the vacancy system

	// CoordinatorID names the worker_rebalancer_coordinator that spawned this ferry as
	// a managed worker. Persisted into the container config so daemon restart recovery
	// SKIPS it (marks it worker_interrupted, preserving the ship claim) and leaves the
	// reclaim/re-evaluation to the coordinator's reconcile pass — the scout_reposition
	// worker pattern (sp-s232). A restart re-dispatches from the hull's CURRENT position:
	// travel() waits out any in-transit leg and re-plans the gate path, so a mid-ferry
	// restart resumes rather than strands (RULINGS #2).
	CoordinatorID string

	// RepositionJumpBound bounds the stored-adjacency jump path this ferry may resolve over
	// (sp-fwxm, the [trade_fleet].reposition_jump_bound the tour reposition also rides,
	// sp-kl16). It is the hull-MOVEMENT reach past the strict fetch-through cap, so a ferry
	// reaches a worker-starved factory whose gate is in the unreadable-backoff set; <= 0
	// degrades to resolveRepositionJumpBound's default (12). Stamped into the launch config
	// from the daemon's live [trade_fleet] config at PersistWorkerFerryWorker and read back
	// by buildWorkerFerryCommand, so it survives the persist→rebuild boundary a restart or a
	// coordinator-managed start crosses (the sp-o34q class — the ferry, unlike the top-level
	// tour, IS persisted via PersistContainer, so an unthreaded bound would be DROPPED).
	RepositionJumpBound int
}

// WorkerFerryResponse reports the completed relay. Because the ferry is one-shot and the
// container wraps a single iteration, it is observed only on completion.
type WorkerFerryResponse struct {
	ShipSymbol          string
	DestinationWaypoint string
}

// WorkerFerryHandler flies a claimed light-hauler to a worker-starved factory system by
// delegating to the shared multi-jump travel machinery (sp-f5pr). It is deliberately
// tiny: all ferry bookkeeping (vacancy detection, nearest-source-by-hops selection, the
// claim, cooldown/concurrency caps, the reclaim on arrival/interruption) lives in the
// coordinator's reconcile; this worker just moves the hull and reports. Twin of
// ScoutRepositionHandler.
type WorkerFerryHandler struct {
	repositioner Repositioner
}

// NewWorkerFerryHandler wires the ferry worker with the movement port (the trade-route
// coordinator's RepositionToWaypoint).
func NewWorkerFerryHandler(repositioner Repositioner) *WorkerFerryHandler {
	return &WorkerFerryHandler{repositioner: repositioner}
}

// Handle executes the one-shot cross-system ferry. A travel error is returned so the
// container FAILS honestly (the runner releases the claim; the coordinator re-evaluates
// the hull next tick) rather than reporting a false success on a stranded hull.
func (h *WorkerFerryHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*WorkerFerryCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf("Ferrying light-hauler %s to %s (cross-system relay to a worker-starved factory system)", cmd.ShipSymbol, cmd.DestinationWaypoint), map[string]interface{}{
		"action":      "worker_ferry_start",
		"ship_symbol": cmd.ShipSymbol,
		"destination": cmd.DestinationWaypoint,
	})

	// A ferry is a hull MOVE, so it rides the bounded stored-adjacency resolver (past
	// unreadable frontier gates), NOT the strict fetch-through Path — resolveRepositionJumpBound
	// applies the same 0/absent → default (12) rule the tour reposition uses (sp-fwxm/sp-kl16),
	// so an unset bound never degrades the ferry to the strict resolver that fail-closes a
	// far-cluster launch. Buy/delivery stays strict elsewhere: the guard line is money vs move.
	if err := h.repositioner.RepositionToWaypointWithinJumps(ctx, cmd.ShipSymbol, cmd.DestinationWaypoint, cmd.PlayerID.Value(), resolveRepositionJumpBound(cmd.RepositionJumpBound)); err != nil {
		return nil, fmt.Errorf("worker ferry of %s to %s failed: %w", cmd.ShipSymbol, cmd.DestinationWaypoint, err)
	}

	logger.Log("INFO", fmt.Sprintf("Light-hauler %s ferried to %s — ready for in-system factory manning", cmd.ShipSymbol, cmd.DestinationWaypoint), map[string]interface{}{
		"action":      "worker_ferry_complete",
		"ship_symbol": cmd.ShipSymbol,
		"destination": cmd.DestinationWaypoint,
	})

	return &WorkerFerryResponse{
		ShipSymbol:          cmd.ShipSymbol,
		DestinationWaypoint: cmd.DestinationWaypoint,
	}, nil
}

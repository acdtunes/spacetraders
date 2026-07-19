package capacity

// The capacity CONVERGE actuator: the thin wrapper
// over the EXISTING primitives for the CHEAP tiers (1-3). It drives — never
// reinvents — fleet-assign (tier 1), reposition/navigate + the worker-rebalancer
// (tier 2), and the depot warehouse buffer config (tier 3). It DECIDES nothing:
// the loop only ever hands it actions DIFF+GOVERN already approved, so each
// method just translates one Action's machine-readable routing fields into a
// call to the primitive that already performs that work.
//
// The frozen domainCapacity.Actuator interface carries no player on its methods,
// so the reconciling player for the tick is resolved from the ambient auth token
// the loop's mediator pass injected into ctx (PlayerResolver) — the same
// token-scoped identity the SENSE lane's treasury read rides.
//
// ExecuteCapital (tier 4) is a fail-closed stub here: capital is post-approval
// ONLY, wired by the proposal-approval lane, and the CONVERGE
// capital gate already refuses tier-4 under the v1 threshold, so this is never
// reached in armed cheap-tier operation. It builds NO purchase.

import (
	"context"
	"fmt"

	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- primitive ports -------------------------------------------------------
//
// Each cheap tier is dispatched to a narrow port the production adapter wraps
// the real existing primitive at (see actuator_adapters.go). The actuator's
// unit tests spy these ports — the primitive-port boundary.

// HullReassigner dedicates an idle hull to a role fleet (tier 1) — the single
// fleet-assign write path (application/ship/commands/assignment).
type HullReassigner interface {
	ReassignHull(ctx context.Context, playerID shared.PlayerID, shipSymbol, fleet string) error
}

// HullRepositioner moves an owned hull to a target waypoint (tier 2) — the
// reposition/navigate primitive.
type HullRepositioner interface {
	RepositionHull(ctx context.Context, playerID shared.PlayerID, shipSymbol, destinationWaypoint string) error
}

// WorkerRebalancer drives the existing worker-rebalancer toward a
// shortfall hub (tier 2); the rebalancer owns the actual per-worker moves.
type WorkerRebalancer interface {
	RebalanceWorkers(ctx context.Context, playerID shared.PlayerID, hubSymbol, workerWaypoint string, count int) error
}

// BufferConfigurator sets one good's depot warehouse buffer cap (tier 3);
// unitsCap 0 de-whitelists the good. Drives the existing depot buffer-config
// write path (supported_goods + target_units).
type BufferConfigurator interface {
	AdjustBufferGood(ctx context.Context, playerID shared.PlayerID, hubSymbol, good string, unitsCap int) error
}

// PlayerResolver yields the reconciling player for the current tick from the
// ambient auth token in ctx — the frozen Actuator interface carries no player.
type PlayerResolver interface {
	ResolvePlayer(ctx context.Context) (shared.PlayerID, error)
}

// Role fleet tags a reassigned idle hull is dedicated to. These MUST match the
// operation/fleet identities the role coordinators claim under, or a reassigned
// hull is invisible to the coordinator that should adopt it:
//   - container_ops_warehouse.go operationWarehouse == "warehouse"
//   - container_ops_stocker.go   operationStocker   == "stocker"
//   - the worker role uses the exported depot.DeliveryHullFleet.
const (
	fleetWarehouse = "warehouse"
	fleetStocker   = "stocker"
)

// Actuator implements domainCapacity.Actuator over the cheap-tier primitives.
type Actuator struct {
	reassigner   HullReassigner
	repositioner HullRepositioner
	workers      WorkerRebalancer
	buffers      BufferConfigurator
	players      PlayerResolver
}

var _ domainCapacity.Actuator = (*Actuator)(nil)

// NewActuator wires the cheap-tier actuator to its primitive ports.
func NewActuator(
	reassigner HullReassigner,
	repositioner HullRepositioner,
	workers WorkerRebalancer,
	buffers BufferConfigurator,
	players PlayerResolver,
) *Actuator {
	return &Actuator{
		reassigner:   reassigner,
		repositioner: repositioner,
		workers:      workers,
		buffers:      buffers,
		players:      players,
	}
}

// ReuseIdleHull (tier 1) dedicates the differ-chosen idle hull to the fleet its
// role's coordinator claims under. The role is read from GapKind, never Reason.
func (a *Actuator) ReuseIdleHull(ctx context.Context, action domainCapacity.Action) error {
	playerID, err := a.players.ResolvePlayer(ctx)
	if err != nil {
		return fmt.Errorf("reassign hull %s: resolve player: %w", action.ShipSymbol, err)
	}
	fleet, err := fleetForRole(action.GapKind)
	if err != nil {
		return err
	}
	if err := a.reassigner.ReassignHull(ctx, playerID, action.ShipSymbol, fleet); err != nil {
		return fmt.Errorf("reassign hull %s to %s fleet: %w", action.ShipSymbol, fleet, err)
	}
	return nil
}

// Rebalance (tier 2) either repositions a misplaced hull onto its anchor or
// drives the worker-rebalancer toward a shortfall hub, by the action's verb.
func (a *Actuator) Rebalance(ctx context.Context, action domainCapacity.Action) error {
	playerID, err := a.players.ResolvePlayer(ctx)
	if err != nil {
		return fmt.Errorf("rebalance %s: resolve player: %w", action.Verb, err)
	}
	switch action.Verb {
	case domainCapacity.VerbRepositionHull:
		if err := a.repositioner.RepositionHull(ctx, playerID, action.ShipSymbol, action.TargetWaypoint); err != nil {
			return fmt.Errorf("reposition hull %s to %s: %w", action.ShipSymbol, action.TargetWaypoint, err)
		}
		return nil
	case domainCapacity.VerbRebalanceWorkers:
		if err := a.workers.RebalanceWorkers(ctx, playerID, action.HubSymbol, action.TargetWaypoint, action.Count); err != nil {
			return fmt.Errorf("rebalance %d worker(s) toward %s: %w", action.Count, action.HubSymbol, err)
		}
		return nil
	}
	return fmt.Errorf("rebalance: unexpected tier-2 verb %q — refusing dispatch", action.Verb)
}

// AdjustBuffer (tier 3) sets the good's desired cap on the hub warehouse; a
// UnitsCap of 0 de-whitelists it. Both tier-3 verbs share this primitive call —
// the differ already encoded add / cap-change / remove into (Good, UnitsCap).
func (a *Actuator) AdjustBuffer(ctx context.Context, action domainCapacity.Action) error {
	playerID, err := a.players.ResolvePlayer(ctx)
	if err != nil {
		return fmt.Errorf("adjust buffer %s@%s: resolve player: %w", action.Good, action.HubSymbol, err)
	}
	if err := a.buffers.AdjustBufferGood(ctx, playerID, action.HubSymbol, action.Good, action.UnitsCap); err != nil {
		return fmt.Errorf("adjust buffer %s@%s to cap %d: %w", action.Good, action.HubSymbol, action.UnitsCap, err)
	}
	return nil
}

// ExecuteCapital (tier 4) is fail-closed in the cheap-tier actuator: capital is
// executed ONLY post-approval by the proposal lane. It builds no
// purchase and never reaches an autobuy path from here.
func (a *Actuator) ExecuteCapital(_ context.Context, action domainCapacity.Action) error {
	return fmt.Errorf(
		"capacity actuator: capital execution not wired — tier-4 %s (%s) executes only via the approved-proposal path (st-0h8 owns capital); refusing to auto-execute a purchase",
		action.Verb, action.GapKind)
}

// fleetForRole maps a role-bearing gap to the fleet a reassigned hull joins.
// A non-role gap fails CLOSED — the actuator never guesses a fleet and silently
// mis-pins a hull.
func fleetForRole(gap domainCapacity.GapKind) (string, error) {
	switch gap {
	case domainCapacity.GapWarehouseShort:
		return fleetWarehouse, nil
	case domainCapacity.GapStockerShort:
		return fleetStocker, nil
	case domainCapacity.GapWorkerShort:
		return depot.DeliveryHullFleet, nil
	}
	return "", fmt.Errorf("reassign hull: gap kind %q carries no hull role — cannot pick a fleet (refusing to guess)", gap)
}

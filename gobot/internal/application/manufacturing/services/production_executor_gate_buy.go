package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// BuyAtTerminalFactory buys `units` of `good` at a PINNED source — the waypoint phase 1's
// GateTopology.TerminalFactory already resolved as this era's exporter of the good.
//
// It deliberately does NOT call selectInputSource. That selector is free to pick a different
// market, and doing so here would silently undo the topology resolution: the delivery fleet's
// whole contract is that it buys the terminal factory's OUTPUT, and a source chosen by
// supply/activity/price could be any market in the system. The caller resolved the waypoint by
// ROLE; this method's job is to honour that, not to re-decide it. Refusing an unusable source
// is correct; substituting another market is the failure mode this phase exists to prevent.
//
// EVERY MONEY GUARD IS UNCHANGED. The fill runs through the same fillFromSource loop the
// selected-source path uses, so the per-tranche working-capital floor (spendFloorBreached, re-read
// against live treasury) and the cross-container concurrent-spend reservation both still govern
// every tranche and both still fail CLOSED — the loop stops and delivers what is aboard rather than
// forcing a buy. Nothing here reads, moves, re-orders or weakens a floor (RULINGS #4). The floor is
// whatever spendFloorBreached enforces: defaultWorkingCapitalReserve, the NON-CONTRACT band, raised
// further by the per-operation capital budget when a work sensor is wired.
//
// The price ceiling also still applies per tranche. Price is deliberately not a gate for this
// fleet, but the degradation is graceful — a laddering ask stops the fill and the hull delivers
// what it has — so the existing guard stays.
//
// units is a CAP from the trip plan, not a target to chase: exceeding it would over-supply a
// material whose bill the trip already sized, or consume hold space allocated to the other material
// on a mixed trip.
//
// Refuses (error, nil result) on a nil or unnamed source, a non-positive allocation, or a source
// with no transaction size. Each is a caller bug, and the alternative — resolving a source here —
// is precisely the pinning this method exists to preserve.
//
// sink names what these goods are FOR, and the caller MUST choose. This method serves both gate
// fleets — the delivery fleet, whose purchases are consumed by a construction site against a bill,
// and the factory fleet, whose purchases are sold into a factory's import listing — and only the
// second has a saturation threshold to clear. Without the parameter the factory fleet's floor
// silently governs construction buys.
func (e *ProductionExecutor) BuyAtTerminalFactory(
	ctx context.Context,
	ship *navigation.Ship,
	good string,
	source *MarketLocatorResult,
	units int,
	systemSymbol string,
	playerID int,
	opContext *shared.OperationContext,
	sink TrancheSink,
) (*ProductionResult, error) {
	if source == nil {
		return nil, fmt.Errorf("cannot buy %s: no terminal factory was resolved — refusing to pick a source here", good)
	}
	if source.WaypointSymbol == "" {
		return nil, fmt.Errorf("cannot buy %s: the resolved terminal factory has no waypoint", good)
	}
	if units <= 0 {
		return nil, fmt.Errorf("cannot buy %s at %s: the trip allocated %d units", good, source.WaypointSymbol, units)
	}
	if source.TradeVolume <= 0 {
		return nil, fmt.Errorf("cannot buy %s at %s: the market reports no per-transaction trade volume", good, source.WaypointSymbol)
	}

	if opContext != nil && opContext.IsValid() {
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	// Stamp the trip's allocation as the fill target so the tranche loop tops the hold up
	// toward it (bounded by hull capacity) instead of carrying a single trade-volume tranche.
	// fraction 0 resolves to the full hull; units is the binding cap.
	ctx = WithHullFillTarget(ctx, units, 0)

	// sourceModeEligible keeps the per-tranche price-ceiling re-check live, exactly as it is on
	// the selected-source path. Passing a weaker mode would disable an existing guard.
	return e.fillFromSource(ctx, ship, good, source, systemSymbol, playerID, sourceModeEligible, sink)
}

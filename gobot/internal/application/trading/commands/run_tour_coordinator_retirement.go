package commands

// run_tour_coordinator_retirement.go — the retirement drain: a hull the operator marked
// retiring stops ACQUIRING and disposes of what it holds, from INSIDE the container already
// flying it, until its hold is empty and it stands down ready to scrap.
//
// Three rungs, descended at the tour boundary and re-entered every pass:
//  1. the hull is empty            -> stand down drained (nothing plans it again);
//  2. something here bids for it   -> sell it at the best CURRENT-system bid, floor-free;
//  3. nothing here bids for it     -> jump toward the best reachable sink, then rung 2 there.
//
// Exhausted, it stands down still holding and names the residue: what is left has no
// reachable buyer at all, so it is worth zero credits and the operator clears it by hand
// (`ship jettison`) before scrapping. Nothing on this path ever buys.

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// retirementReachJumpLimit bounds how many times ONE run may jump a retiring hull toward a sink
// for cargo its own system will not buy. The ladder only jumps when a reachable market actually
// bids for the residue, so this is the backstop that makes the drain provably finite rather than
// the thing that normally ends it — two grounds is enough for a mixed hold, and a relaunch (paced
// by the fleet coordinator) re-earns the budget against a hold that only ever gets smaller.
const retirementReachJumpLimit = 2

// retirementDisposalKind labels the retirement ladder's sales in the shared liquidation sale
// path, so a drain reads apart from a margins-death distress dump in the log.
var retirementDisposalKind = liquidationKind{
	prefix: "Retirement disposal", action: "retirement_disposal", bead: "sp-58zaj",
}

// retirementState reads the operator's mark FRESH off the hull, so a mark set while this
// container is mid-plan binds without a restart. Fail OPEN: a hull that cannot be read is
// reported unmarked and keeps touring exactly as one that never was, because retirement is an
// operator convenience and must never become a stall source.
func (h *RunTourCoordinatorHandler) retirementState(ctx context.Context, cmd *RunTourCoordinatorCommand) (retiring, drained bool) {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil {
		return false, false
	}
	return ship.IsRetiring(), ship.RetirementDrained()
}

// standDownRetiring ends the run through the ordinary completion path — the runner releases
// the claim and publishes completion — naming the retirement once so an operator can see the
// mark actually took.
func (h *RunTourCoordinatorHandler) standDownRetiring(cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, logger common.ContainerLogger) {
	response.RetirementStandDown = true
	response.ExitReason = tourExitRetired
	response.ExitDetail = fmt.Sprintf("retiring hull stands down drained after %d productive tour(s)", response.ToursCompleted)
	logger.Log("INFO", fmt.Sprintf(
		"Retiring hull stands down drained - %s is marked retiring and its hold is empty at this boundary; it is planned no further legs",
		cmd.ShipSymbol), map[string]interface{}{
		"action": "tour_retirement_stand_down", "ship_symbol": cmd.ShipSymbol,
		"container_id": cmd.ContainerID, "tours_completed": response.ToursCompleted,
	})
}

// standDownRetiringHolding ends the run for a marked hull the ladder could not drain: nothing
// within reach bids for what is still aboard, so the residue's liquidation value is zero and
// there is nothing left to try. It names what is stuck, because that load is what blocks the
// scrap.
func (h *RunTourCoordinatorHandler) standDownRetiringHolding(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	logger common.ContainerLogger,
) {
	held, waypoint := h.retirementHold(ctx, cmd)
	units := 0
	parts := make([]string, 0, len(held))
	for good, n := range held {
		units += n
		parts = append(parts, fmt.Sprintf("%d %s", n, good))
	}
	sort.Strings(parts) // deterministic message (RULINGS #2)
	response.RetirementStandDown = true
	response.RetirementResidualUnits = units
	response.ExitReason = tourExitRetiredHolding
	response.ExitDetail = fmt.Sprintf("retiring hull stands down still holding %d unit(s) no reachable market bids for", units)
	logger.Log("WARNING", fmt.Sprintf(
		"Retiring hull stands down UNDRAINED - %s is marked retiring and still holds %v at %s; nothing within reach bids for it, so the drain has nothing left to try. Clear it by hand (ship jettison) before scrapping - a loaded hull cannot be scrapped and this load has no reachable buyer",
		cmd.ShipSymbol, parts, waypoint), map[string]interface{}{
		"action": "tour_retirement_stand_down_holding", "ship_symbol": cmd.ShipSymbol,
		"container_id": cmd.ContainerID, "residual_hold": parts, "residual_units": units,
		"waypoint": waypoint,
	})
}

// retirementHold reads the hull's sellable hold and where it stands. Reserved cargo (a staged
// module, an operator `ship reserve-cargo` override) is withheld by tourShipState, so the
// ladder never plans to sell what the executor would refuse.
func (h *RunTourCoordinatorHandler) retirementHold(ctx context.Context, cmd *RunTourCoordinatorCommand) (map[string]int, string) {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, "unknown"
	}
	waypoint := "unknown"
	if loc := ship.CurrentLocation(); loc != nil {
		waypoint = loc.Symbol
	}
	return h.tourShipState(ship).Cargo, waypoint
}

// retirementDisposalPass is rung 2: sell every good the marked hull holds at the best bid its
// CURRENT system offers. SELL-ONLY — it never calls the planner and never buys — so RULINGS #4's
// buy-side stack is untouched, and no sell floor is relaxed for it either: the shared
// liquidation sale path it reuses has always been floor-free for cargo already owned.
//
// sold=true means units actually left the hull, which is the caller's progress signal. A
// non-nil error is a resumable travel/dock/sell failure the runner retries.
func (h *RunTourCoordinatorHandler) retirementDisposalPass(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
) (bool, error) {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	held := h.tourShipState(ship).Cargo
	if len(held) == 0 {
		return false, nil // empty, or laden only with reserved cargo nobody may sell
	}
	listings, lerr := h.legs.collectSystemListings(ctx, ship.CurrentLocation().SystemSymbol, cmd.PlayerID)
	if lerr != nil {
		return false, nil // markets unreadable — never sell on an unreadable price (RULINGS #4)
	}
	// The RepositionDisabled kill-switch means the operator opted this hull out of moving.
	// Honour it exactly as the exit sweep does: sell only where the hull already stands.
	if cmd.RepositionDisabled {
		listings = listingsAt(listings, ship.CurrentLocation().Symbol)
	}
	sinks := bestLocalDistressSinks(freshListings(listings, h.clock.Now(), h.listingMaxAge(ctx, cmd.PlayerID)), held)
	if len(sinks) == 0 {
		return false, nil // nothing here bids for any of it — the caller escalates to reach
	}

	goods := make([]string, 0, len(sinks))
	for good := range sinks {
		goods = append(goods, good)
	}
	sort.Strings(goods) // deterministic disposal order (RULINGS #2)

	soldAny := false
	legs := newLiquidationLegIndex()
	for _, good := range goods {
		sink := sinks[good]
		sold, serr := h.liquidateGoodAtSink(ctx, cmd, response, netBought, good, sink, legs.at(sink.waypoint), retirementDisposalKind)
		if serr != nil {
			// A partial disposal may already be booked; return the resumable error so the
			// runner retries and the ladder re-reads the lighter hold on the next pass.
			return false, serr
		}
		if sold {
			legs.arrived(sink.waypoint)
			response.RetirementDisposalSales++
		}
		soldAny = soldAny || sold
	}
	return soldAny, nil
}

// retirementReachSink is rung 3: nothing in the hull's own system bids for what it still holds,
// so jump toward the reachable system whose markets absorb the most of that residue. Ranking,
// candidate discovery, anti-herd exclusion, persist-before-jump and the bounded jump are the
// margins-death offload's, reused; what differs is that retirement has no laden threshold — a
// hull leaving service must empty whatever it carries, not just a half-full hold — and does not
// spend the margins-death episode's one rescue.
//
// It declines when no reachable candidate can absorb any of the residue, which is what makes
// "unsellable in reach" terminate instead of hunting.
func (h *RunTourCoordinatorHandler) retirementReachSink(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
) (bool, error) {
	if cmd.RepositionDisabled {
		return false, nil
	}
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	held := h.tourShipState(ship).Cargo
	if len(held) == 0 {
		return false, nil
	}
	currentSystem := ship.CurrentLocation().SystemSymbol
	candidates := h.buildRepositionCandidates(ctx, cmd, currentSystem)
	candidates, _ = h.excludeHerdedSystems(ctx, cmd, candidates)
	if len(candidates) == 0 {
		return false, nil
	}
	best, value, ok := h.bestHeldCargoSink(ctx, cmd, candidates, held)
	if !ok {
		return false, nil // no reachable buyer for the residue — the drain has nothing left to try
	}

	h.incrementPendingRelocation(best.system)
	defer h.decrementPendingRelocation(best.system)

	// Persist the in-flight destination FIRST (RULINGS #2): a restart mid-jump resumes toward
	// the same ground through the same generic resume block every reposition uses.
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: true, TargetSystem: best.system, TargetWaypoint: best.waypoint})
	jumpBound := resolveRepositionJumpBound(cmd.RepositionJumpBound)
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Retirement reach: %s is retiring and %s bids for none of its remaining load - jumping to %s (%s) within %d stored-adjacency jumps as the best reachable sink for it, recoverable ~%d (sell-side cash recovery, it buys nothing)",
		cmd.ShipSymbol, currentSystem, best.system, best.waypoint, jumpBound, value), map[string]interface{}{
		"action": "tour_retirement_reach", "ship_symbol": cmd.ShipSymbol,
		"from_system": currentSystem, "to_system": best.system, "to_waypoint": best.waypoint,
		"held_cargo_sink_value": value, "reposition_jump_bound": jumpBound,
	})

	if terr := h.legs.RepositionToWaypointWithinJumps(ctx, cmd.ShipSymbol, best.waypoint, cmd.PlayerID, jumpBound); terr != nil {
		// Leave the persisted in-progress state set: a restart resumes toward the same sink.
		metrics.RecordTourReposition(cmd.PlayerID, "failed")
		return false, fmt.Errorf("retirement reach jump of %s to %s failed: %w", cmd.ShipSymbol, best.waypoint, terr)
	}
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: false})
	response.Repositions++
	metrics.RecordTourReposition(cmd.PlayerID, "success")
	return true, nil
}

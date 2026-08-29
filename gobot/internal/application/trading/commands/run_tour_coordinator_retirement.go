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
// Rungs 2 and 3 live in run_tour_coordinator_disposal.go, shared with the margins-death
// pre-release drain; what is here is retirement's own frame — the mark and the stand-downs.
//
// Exhausted, it stands down still holding and names the residue: what is left has no
// reachable buyer at all, so it is worth zero credits and the operator clears it by hand
// (`ship jettison`) before scrapping. Nothing on this path ever buys.

import (
	"context"
	"fmt"
	"sort"

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

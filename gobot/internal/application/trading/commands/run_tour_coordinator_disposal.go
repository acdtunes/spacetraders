package commands

// run_tour_coordinator_disposal.go — the sell-only disposal ladder (sp-58zaj's, shared by the
// retirement drain and the sp-b9alf margins-death pre-release drain): sell every held good at the
// best non-EXPORT bid the CURRENT system offers; else jump toward the reachable system whose
// markets absorb the most of the residue and sell there; else stop, leaving the residue for the
// caller to report. Nothing here buys or calls the planner — it disposes of a hold and stops.

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// strandDisposalReachJumpLimit bounds how many times ONE run may jump a margins-dead hull toward
// a sink for cargo its own system will not buy — the backstop that makes the drain provably
// finite, not the thing that normally ends it.
const strandDisposalReachJumpLimit = 2

var strandDisposalKind = liquidationKind{
	prefix: "Stranded-hold disposal", action: "strand_disposal", bead: "sp-b9alf",
}

// disposeStrandedHold is the LAST rung before a margins-dead continuous run releases its hull.
// Unlike the rescues above it, it has NO laden threshold and does not read the episode's
// one-rescue budget (already spent by the time it runs) — which is what lets it serve the hull
// they could not.
//
// TERMINATION: a pass that sells strictly shrinks a finite hold; a pass that sells nothing spends
// one of this run's strandDisposalReachJumpLimit jumps, NEVER refunded; a pass that can do
// neither ends. `jumps` is a caller-owned counter that only ever rises.
//
// progressed=true means the hull sold something or moved, so the caller may keep touring it. A
// non-nil error is a resumable travel/dock/sell failure the runner retries.
func (h *RunTourCoordinatorHandler) disposeStrandedHold(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	episode *repositionEpisode,
	netBought map[string]int,
	jumps *int,
) (bool, error) {
	progressed := false
	for {
		if err := ctx.Err(); err != nil {
			return progressed, err
		}
		sales, derr := h.disposalPass(ctx, cmd, response, netBought, strandDisposalKind)
		response.StrandDisposalSales += sales
		if derr != nil {
			return progressed, derr
		}
		if sales > 0 {
			progressed = true
			continue
		}
		if *jumps >= strandDisposalReachJumpLimit {
			return progressed, nil
		}
		reached, rerr := h.reachDisposalSink(ctx, cmd, response, episode, strandDisposalKind)
		if rerr != nil {
			return progressed, rerr
		}
		if !reached {
			return progressed, nil
		}
		*jumps++
		progressed = true
	}
}

// disposalPass is rung 2: sell every good the hull holds at the best bid its CURRENT system
// offers. SELL-ONLY — never the planner, never a buy — so RULINGS #4's buy-side stack is
// untouched, and no sell floor is relaxed either: the shared liquidation sale path it reuses has
// always been floor-free for cargo already owned. It returns how many goods left the hull.
func (h *RunTourCoordinatorHandler) disposalPass(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	kind liquidationKind,
) (int, error) {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0, err
	}
	held := h.tourShipState(ship).Cargo
	if len(held) == 0 {
		return 0, nil // empty, or laden only with reserved cargo nobody may sell
	}
	listings, lerr := h.legs.collectSystemListings(ctx, ship.CurrentLocation().SystemSymbol, cmd.PlayerID)
	if lerr != nil {
		return 0, nil // markets unreadable — never sell on an unreadable price (RULINGS #4)
	}
	// The RepositionDisabled kill-switch means the operator opted this hull out of moving.
	// Honour it exactly as the exit sweep does: sell only where the hull already stands.
	if cmd.RepositionDisabled {
		listings = listingsAt(listings, ship.CurrentLocation().Symbol)
	}
	sinks := bestLocalDistressSinks(freshListings(listings, h.clock.Now(), h.listingMaxAge(ctx, cmd.PlayerID)), held)
	if len(sinks) == 0 {
		return 0, nil // nothing here bids for any of it — the caller escalates to reach
	}

	goods := make([]string, 0, len(sinks))
	for good := range sinks {
		goods = append(goods, good)
	}
	sort.Strings(goods) // deterministic disposal order (RULINGS #2)

	sales := 0
	legs := newLiquidationLegIndex()
	for _, good := range goods {
		sink := sinks[good]
		sold, serr := h.liquidateGoodAtSink(ctx, cmd, response, netBought, good, sink, legs.at(sink.waypoint), kind)
		if serr != nil {
			// A partial disposal may already be booked; return the resumable error so the
			// runner retries and the ladder re-reads the lighter hold on the next pass.
			return sales, serr
		}
		if sold {
			legs.arrived(sink.waypoint)
			sales++
		}
	}
	return sales, nil
}

// reachDisposalSink is rung 3: nothing in the hull's own system bids for what it still holds, so
// jump toward the reachable system whose markets absorb the most of that residue. Ranking,
// candidate discovery, anti-herd exclusion, persist-before-jump and the bounded jump are the
// margins-death offload's, reused; what differs is that disposal has no laden threshold. It
// declines when no reachable candidate can absorb any of the residue, which is what makes
// "unsellable in reach" terminate instead of hunting.
//
// A non-nil episode is marked SPENT on a successful jump, so this hop cannot be followed by a
// fresh-arb rotation before the hull trades again. The retirement caller passes nil — a hull
// leaving service is never planned another ground.
func (h *RunTourCoordinatorHandler) reachDisposalSink(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	episode *repositionEpisode,
	kind liquidationKind,
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
		"%s reach: %s bids for none of the load %s still holds - jumping to %s (%s) within %d stored-adjacency jumps as the best reachable sink for it, recoverable ~%d (sell-side cash recovery, it buys nothing)",
		kind.prefix, currentSystem, cmd.ShipSymbol, best.system, best.waypoint, jumpBound, value), map[string]interface{}{
		"action": kind.action, "ship_symbol": cmd.ShipSymbol, "bead": kind.bead,
		"from_system": currentSystem, "to_system": best.system, "to_waypoint": best.waypoint,
		"held_cargo_sink_value": value, "reposition_jump_bound": jumpBound,
	})

	if terr := h.legs.RepositionToWaypointWithinJumps(ctx, cmd.ShipSymbol, best.waypoint, cmd.PlayerID, jumpBound); terr != nil {
		// Leave the persisted in-progress state set: a restart resumes toward the same sink.
		metrics.RecordTourReposition(cmd.PlayerID, "failed")
		return false, fmt.Errorf("%s jump of %s to %s failed: %w", kind.action, cmd.ShipSymbol, best.waypoint, terr)
	}
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: false})
	if episode != nil {
		episode.repositioned = true
		episode.fromSystem = currentSystem
		episode.toSystem = best.system
	}
	response.Repositions++
	metrics.RecordTourReposition(cmd.PlayerID, "success")
	return true, nil
}

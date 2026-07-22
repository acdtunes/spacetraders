package commands

// run_tour_coordinator_offload.go — sp-2v69u: when a continuous tour's margins die AND the
// fresh-arb reposition rescue (maybeReposition) finds no candidate worth a fresh-profit jump, a
// LADEN hull (cargo > offloadLadenThresholdPct of capacity) is not actually stuck — it is
// carrying a load some reachable market will pay real cash for. This pass ranks jump-reachable
// candidates by how much of the hull's CURRENTLY HELD cargo each can absorb (reusing the same
// net_absorption / market-depth reads maybeReposition's own pre-rank uses) and jumps to the
// best one. It is margin-EXEMPT (cash recovery, not fresh profit — RULINGS #4 is unaffected
// because this only SELLS, never spends) and, critically, it NEVER clears the hull's cargo
// before ranking. planAtCandidate's shipState.Cargo = nil (the sp-m9co Class B fix) is correct
// for FRESH-arb ranking — it is exactly why a laden hull was invisible to its own held-cargo
// value there: every candidate priced as a bare infeasible tour and the run parked holding
// cargo it could never route toward a buyer. This pass never calls the solver at all, so that
// clearing never enters the picture. The actual SALE of the held cargo happens for free, via
// the ordinary next-iteration re-plan at the new position: the real (never-cleared) ship cargo
// rides the jump and the live tourShipState hands it to the planner as launch inventory, which
// prices it as HeldLiquidation exactly as any other laden arrival — no new sell-side execution
// code is needed here.
//
// Gated on the stuck-laden CONDITION itself (cargo > 50% capacity), not a separate on/off
// config flag (Admiral's ruling): a healthy or unladen hull's laden check simply says no, and a
// hull whose margins-death was already rescued by fresh arb never reaches this function at all
// — the caller only tries it when maybeReposition returns repositioned=false.

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// offloadLadenThresholdPct is the bead's literal stuck-laden condition: a hull whose hold is
// more than half full (physical units, not sellable-only) is laden enough that liquidating its
// held cargo toward a reachable sink is worth a jump even with no fresh profit on offer. A
// named threshold (RULINGS #5), not a magic literal — deliberately not a captain-configurable
// knob, per the Admiral's ruling that the stuck-laden condition itself is the gate, not a
// dormant flag: a healthy hull never crosses it, so there is nothing to arm.
const offloadLadenThresholdPct = 50

// isLadenForOffload reports whether units/capacity is STRICTLY more than
// offloadLadenThresholdPct percent full, using integer math (units*100 > capacity*pct) so the
// comparison never rounds a genuine half-full hold into either bucket. capacity <= 0 (no hold,
// or an unreadable capacity) is defensively never laden.
func isLadenForOffload(units, capacity int) bool {
	if capacity <= 0 {
		return false
	}
	return units*100 > capacity*offloadLadenThresholdPct
}

// heldCargoSinkValue is the pure ranking math for one candidate system: for every good the hull
// currently holds, find that candidate's best non-EXPORT bid (an EXPORT market's bid is a low
// sellback, never a real sink — mirrors bestLaneForGood/BuildTourSnapshot's own sink filter),
// cap the absorbable units at that listing's traded volume (a market-absorption bound, not a
// hold-sized one — the same discipline CappedSpread applies), and sum the recoverable cash
// across every held good. A good with no reachable non-EXPORT listing at this candidate
// contributes nothing.
func heldCargoSinkValue(listings []trading.GoodListing, held map[string]int) int64 {
	bestBid := map[string]trading.GoodListing{}
	for _, l := range listings {
		if l.Bid <= 0 || l.TradeType == lookbackExportType {
			continue
		}
		if cur, ok := bestBid[l.Good]; !ok || l.Bid > cur.Bid {
			bestBid[l.Good] = l
		}
	}
	var total int64
	for good, units := range held {
		if units <= 0 {
			continue
		}
		listing, ok := bestBid[good]
		if !ok {
			continue
		}
		absorbable := units
		if listing.Volume > 0 && listing.Volume < absorbable {
			absorbable = listing.Volume
		}
		total += int64(absorbable) * int64(listing.Bid)
	}
	return total
}

// bestHeldCargoSink evaluates every candidate's reachable market listings against the held
// cargo and returns the one with the highest recoverable sink value. Reuses the identical
// per-system listing read maybeReposition's own pre-rank uses (h.legs.collectSystemListings +
// freshListings), so a candidate whose only cached data is stale is excluded exactly as it
// would be from fresh-arb ranking. ok=false when no candidate can absorb any of the held cargo
// at all.
func (h *RunTourCoordinatorHandler) bestHeldCargoSink(
	ctx context.Context, cmd *RunTourCoordinatorCommand, candidates []repositionCandidate, held map[string]int,
) (repositionCandidate, int64, bool) {
	now := h.clock.Now()
	var best repositionCandidate
	var bestValue int64
	found := false
	for _, cand := range candidates {
		listings, err := h.legs.collectSystemListings(ctx, cand.system, cmd.PlayerID)
		if err != nil || len(listings) == 0 {
			continue
		}
		value := heldCargoSinkValue(freshListings(listings, now, maxListingAge), held)
		if value <= 0 {
			continue
		}
		if !found || value > bestValue {
			best, bestValue, found = cand, value, true
		}
	}
	return best, bestValue, found
}

// maybeOffloadHeldCargo is the sp-2v69u held-cargo offload rescue: the FALLBACK tried only when
// the fresh-arb margins-death reposition (maybeReposition) already found no candidate worth a
// fresh-profit jump. A hull that is not LADEN (cargo <= 50% capacity) has nothing this pass can
// usefully rescue — no jump is worth burning purely to shed a light load — so it exits
// immediately, byte-identical to a run with no held-cargo rescue at all. Shares the same
// kill-switch and one-reposition-per-episode budget as maybeReposition (RepositionDisabled,
// episode.repositioned) so an operator's existing controls govern both rescues uniformly, and
// reuses the identical candidate discovery + anti-herd exclusion + in-flight registration +
// persist-before-jump + travel primitives. The only NEW decision this function makes is HOW
// candidates are ranked (held-cargo sink absorption, margin-exempt); it deliberately never
// calls planAtCandidate/the solver, so the shipState.Cargo=nil pre-flight clear never enters
// this path at all.
func (h *RunTourCoordinatorHandler) maybeOffloadHeldCargo(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	episode *repositionEpisode,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	if cmd.RepositionDisabled {
		return false, nil // shares the fresh-arb kill-switch
	}
	if episode.repositioned {
		return false, nil // already spent this episode's one reposition — no hop-scotching
	}

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		metrics.RecordTourReposition(cmd.PlayerID, "failed")
		return false, err
	}
	if !isLadenForOffload(ship.CargoUnits(), ship.CargoCapacity()) {
		return false, nil // not stuck-laden — nothing for this rescue to do
	}
	held := h.tourShipState(ship).Cargo
	if len(held) == 0 {
		return false, nil // laden entirely with reserved (MODULE_*/MOUNT_*) cargo — nothing sellable
	}

	currentSystem := ship.CurrentLocation().SystemSymbol
	candidates := h.buildRepositionCandidates(ctx, cmd, currentSystem)
	candidates, _ = h.excludeHerdedSystems(ctx, cmd, candidates)
	if len(candidates) == 0 {
		return false, nil // no jump-reachable candidate at all — exit honestly (margins died)
	}

	best, value, ok := h.bestHeldCargoSink(ctx, cmd, candidates, held)
	if !ok {
		// No reachable candidate can absorb any of the held cargo — exit honestly exactly as
		// pre-sp-2v69u; the caller falls through to the standard starvation exit.
		return false, nil
	}

	// sp-lq64 anti-herd registration, mirroring maybeReposition: claim the target for the whole
	// synchronous flight so a concurrent stuck-laden hull's discovery sees this one in flight.
	h.incrementPendingRelocation(best.system)
	defer h.decrementPendingRelocation(best.system)

	// Persist the in-flight destination FIRST (RULINGS #2): a daemon restart mid-jump resumes
	// toward the same ground via the SAME generic restart-resume block maybeReposition's jump
	// uses — it keys only on RepositionInProgress/TargetSystem/TargetWaypoint, not on which
	// rescue committed them.
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: true, TargetSystem: best.system, TargetWaypoint: best.waypoint})
	jumpBound := resolveRepositionJumpBound(cmd.RepositionJumpBound)
	logger.Log("INFO", fmt.Sprintf("Offload: margins died at %s with no fresh-arb candidate worth the jump, hull is laden (%d/%d) - jumping to %s (%s) within %d stored-adjacency jumps as the best reachable sink for its held cargo, recoverable ~%d (margin-exempt cash recovery)", currentSystem, ship.CargoUnits(), ship.CargoCapacity(), best.system, best.waypoint, jumpBound, value), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "from_system": currentSystem, "to_system": best.system,
		"to_waypoint": best.waypoint, "held_cargo_sink_value": value, "reposition_jump_bound": jumpBound, "bead": "sp-2v69u",
	})

	// Deliberately NO look-back buy here (offload SELLS, it doesn't buy) and no fresh-arb
	// planner pre-flight — the hull's real cargo rides the jump untouched and is liquidated by
	// the ordinary next-iteration re-plan at the new position.
	if terr := h.legs.RepositionToWaypointWithinJumps(ctx, cmd.ShipSymbol, best.waypoint, cmd.PlayerID, jumpBound); terr != nil {
		// Leave the persisted in-progress state set: a restart resumes toward the same
		// destination.
		metrics.RecordTourReposition(cmd.PlayerID, "failed")
		return false, fmt.Errorf("held-cargo offload jump of %s to %s failed: %w", cmd.ShipSymbol, best.waypoint, terr)
	}
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: false})

	episode.repositioned = true
	episode.fromSystem = currentSystem
	episode.toSystem = best.system
	response.Repositions++
	metrics.RecordTourReposition(cmd.PlayerID, "success")
	return true, nil
}

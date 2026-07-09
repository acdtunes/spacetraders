package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// --- cross-system travel (sp-wlev: the multi-system gate-crossing unlock) ---

const (
	// DefaultCooldownMarginFactor mirrors ship.DefaultArrivalMarginFactor
	// (sp-ht1f) exactly: the multiplicative term scales naturally with
	// however long the wait is, so the same ratio is appropriate whether
	// the thing being waited on is an arrival or a jump cooldown.
	DefaultCooldownMarginFactor = 1.25

	// DefaultCooldownMinMargin is sized for jump cooldowns specifically -
	// NOT copied from arrival's 2-minute floor. Arrival's 2-minute margin
	// is a small fraction of transits that are themselves minutes-to-hours
	// long; reusing it here would make the margin the DOMINANT term for a
	// ~60s jump cooldown (a 3x inflation) instead of the small clock-skew/
	// API-latency correction it's meant to be. 10s comfortably absorbs
	// that jitter without ballooning the wait on the much shorter cooldown
	// timescale.
	DefaultCooldownMinMargin = 10 * time.Second
)

// calculateCooldownWaitBudget mirrors calculateArrivalWaitBudget's formula
// (internal/application/ship/arrival_wait.go, sp-ht1f pattern):
// budget = max(remaining*marginFactor, remaining+minMargin). The margin
// absorbs scheduler jitter and API latency around the real cooldown-expiry
// instant; it is not what keeps a short cooldown from being polled early. A
// negative remaining (clock skew already putting "now" past the reported
// expiry) is clamped to zero before either term is computed.
func calculateCooldownWaitBudget(remaining time.Duration, marginFactor float64, minMargin time.Duration) time.Duration {
	if remaining < 0 {
		remaining = 0
	}
	scaled := time.Duration(float64(remaining) * marginFactor)
	floor := remaining + minMargin
	if scaled > floor {
		return scaled
	}
	return floor
}

// waitForJumpCooldown waits out the cooldown the jump API just reported,
// using an ETA-scaled budget (sp-ht1f pattern) instead of a flat buffer
// (contrast run_siphon_worker.go's waitForShipCooldown, which adds a flat
// 1s - that resumes a cooldown persisted from a PREVIOUS session and has no
// fresher number to scale from; here the jump response just told us the
// exact cooldown synchronously, so the ETA-scaled budget applies cleanly).
func (h *RunTradeRouteCoordinatorHandler) waitForJumpCooldown(ctx context.Context, cooldownSeconds int) {
	if cooldownSeconds <= 0 {
		return
	}
	remaining := time.Duration(cooldownSeconds) * time.Second
	budget := calculateCooldownWaitBudget(remaining, DefaultCooldownMarginFactor, DefaultCooldownMinMargin)

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Waiting out jump cooldown before continuing the circuit", map[string]interface{}{
		"action":              "jump_cooldown_wait",
		"cooldown_seconds":    cooldownSeconds,
		"wait_budget_seconds": int(budget.Seconds()),
	})
	h.clock.Sleep(budget)
}

// travel moves the ship toward destinationWaypoint, crossing a system
// boundary via jump (sp-n0x7's ship-jump verb) when needed - the sp-wlev
// multi-system trade-route unlock. A same-system destination takes the
// existing navigate/dock fast path unchanged, returning the SAME ship
// pointer untouched. A cross-system destination jumps instead: the jump
// opts out of taking its own claim (SkipClaim: true) since the coordinator
// already holds the ship claimed under its own container for the whole
// circuit, waits out the resulting cooldown, then reloads the ship from the
// repository so the caller continues with a pointer reflecting the ship's
// new system - every downstream verb (dock/purchase/sell) already
// dispatches by ship SYMBOL, never the cached pointer, so this reload is
// the only place staleness could otherwise leak in.
func (h *RunTradeRouteCoordinatorHandler) travel(
	ctx context.Context,
	ship *navigation.Ship,
	destinationWaypoint string,
	playerID int,
) (*navigation.Ship, error) {
	currentSystem := ship.CurrentLocation().SystemSymbol
	destSystem := shared.ExtractSystemSymbol(destinationWaypoint)

	if currentSystem == destSystem {
		if err := h.navigate(ctx, ship, destinationWaypoint, playerID); err != nil {
			return ship, err
		}
		return ship, nil
	}

	// sp-5nqx departure hop — the SOURCE-side mirror of the sp-vzxu gate->waypoint
	// arrival hop below. The jump verb requires a DRIVELESS hull (which the arb/
	// trade haulers are) to already be sitting ON a jump gate: jump_ship.go rejects
	// "no jump drive module and not at a jump gate" for a driveless hull UP FRONT,
	// before its own find-nearest-gate hop can run (that hop only rescues drive-
	// equipped hulls). So a cross-system leg that starts at a market waypoint (e.g.
	// K79) must fly the waypoint->gate hop HERE first, or the jump fails and the
	// bought tranche strands at the source (the live sp-5nqx incident). GUARDED: a
	// hull already sitting on a jump gate skips the hop entirely, so a gate-origin
	// lane still costs exactly one jump and zero extra navigates.
	if !ship.CurrentLocation().IsJumpGate() {
		gateResp, gerr := h.mediator.Send(ctx, &shipQueries.FindNearestJumpGateQuery{
			ShipSymbol: ship.ShipSymbol(),
			PlayerID:   &playerID,
		})
		if gerr != nil {
			return ship, fmt.Errorf("find source jump gate for %s in %s failed: %w", ship.ShipSymbol(), currentSystem, gerr)
		}
		gate, ok := gateResp.(*shipQueries.FindNearestJumpGateResponse)
		if !ok || gate.JumpGate == nil {
			return ship, fmt.Errorf("no source jump gate resolved for %s in %s (response %T)", ship.ShipSymbol(), currentSystem, gateResp)
		}
		if err := h.navigate(ctx, ship, gate.JumpGate.Symbol, playerID); err != nil {
			return ship, fmt.Errorf("navigate %s from %s to source jump gate %s failed: %w", ship.ShipSymbol(), ship.CurrentLocation().Symbol, gate.JumpGate.Symbol, err)
		}
	}

	resp, err := h.mediator.Send(ctx, &navCmd.JumpShipCommand{
		ShipSymbol:        ship.ShipSymbol(),
		DestinationSystem: destSystem,
		PlayerID:          &playerID,
		SkipClaim:         true,
	})
	if err != nil {
		return ship, fmt.Errorf("jump %s to %s failed: %w", ship.ShipSymbol(), destSystem, err)
	}
	jumpResp, ok := resp.(*navCmd.JumpShipResponse)
	if !ok {
		return ship, fmt.Errorf("unexpected jump response type %T", resp)
	}

	h.waitForJumpCooldown(ctx, jumpResp.CooldownSeconds)

	freshShip, err := h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), shared.MustNewPlayerID(playerID))
	if err != nil {
		return ship, fmt.Errorf("failed to reload ship %s after jump to %s: %w", ship.ShipSymbol(), destSystem, err)
	}

	// The jump lands the hull on destSystem's JUMP GATE, not on
	// destinationWaypoint's market. Fly the final gate->waypoint hop so the
	// caller's dock+sell fire at the market that actually trades the good -
	// without it the sell fired at the gate (which doesn't trade the good) and
	// stranded the whole load (sp-vzxu: observed -510k, 54-72 unsold
	// lab_instruments). GUARDED: when the jump already landed the hull ON
	// destinationWaypoint (the destination IS the gate), the hop is redundant
	// and skipped, so a gate-market lane still costs exactly one jump.
	if freshShip.CurrentLocation().Symbol != destinationWaypoint {
		if err := h.navigate(ctx, freshShip, destinationWaypoint, playerID); err != nil {
			return freshShip, fmt.Errorf("navigate %s from gate to %s after jump to %s failed: %w", freshShip.ShipSymbol(), destinationWaypoint, destSystem, err)
		}
	}
	return freshShip, nil
}

// --- cross-system ranking penalty (sp-wlev) ---

// crossSystemRankingPenaltyPerUnit is subtracted from a lane's per-unit
// spread, for RANKING PURPOSES ONLY, when its source and destination sit in
// different systems - representing the jump-plus-cooldown time cost against
// an equally-profitable same-system lane. It never mutates the lane's real
// economics (SpreadPerUnit, CappedSpread, ClearsFloor all still read the
// lane's own untouched values), so a cross-system lane that wins the
// ranking is still evaluated for floor-discipline on its true numbers.
const crossSystemRankingPenaltyPerUnit = 200

// maxListingAge bounds how old a cached market observation may be and still feed
// UNDIRECTED lane ranking (sp-xwa1). The ranker scores lanes off cached prices; a
// lane priced from an observation this stale can already have moved, so ranking it
// chases a spread that no longer exists (the analyst's arb-board finding: the
// ranker "picks lanes that already moved"). 75 minutes is deliberately generous —
// a frontier market a hull hasn't visited in over an hour is genuinely unreliable,
// while a lane re-observed within the hour (every completed trade refreshes its
// own two markets, see scanLanes' refreshMarketData note) stays eligible. It gates
// only undirected auto-scan: an operator-directed --dest lane is re-verified LIVE
// at execution (staleAskAborts + the per-visit margin re-check), so staleness must
// not silently veto it — see scanLanes.
const maxListingAge = 75 * time.Minute

// partitionListingsByAge splits listings into those observed within maxAge of now
// (fresh) and those older (stale), preserving input order in each. A listing with a
// zero ObservedAt is treated as FRESH — an unknown age is not evidence of staleness,
// and callers that never populate the timestamp (older tests, non-cache sources)
// must rank unchanged. Pure and now-injected so the age gate is unit-testable
// without a clock; scanLanes supplies h.clock.Now().
func partitionListingsByAge(listings []trading.GoodListing, now time.Time, maxAge time.Duration) (fresh, stale []trading.GoodListing) {
	for _, l := range listings {
		if !l.ObservedAt.IsZero() && now.Sub(l.ObservedAt) > maxAge {
			stale = append(stale, l)
			continue
		}
		fresh = append(fresh, l)
	}
	return fresh, stale
}

// staleListingSummary renders up to a few stale listings into a compact,
// message-text one-liner (waypoint:good) so the exclusion is greppable in
// `container logs`, which drops the structured metadata map (the sp-149h/sp-iqyq
// renderer defect). Bounded so a system-wide staleness event doesn't flood one log
// line with every excluded row.
func staleListingSummary(stale []trading.GoodListing) string {
	const sampleLimit = 5
	parts := make([]string, 0, sampleLimit)
	for i, l := range stale {
		if i >= sampleLimit {
			parts = append(parts, fmt.Sprintf("+%d more", len(stale)-sampleLimit))
			break
		}
		parts = append(parts, fmt.Sprintf("%s:%s", l.Waypoint, l.Good))
	}
	return strings.Join(parts, ", ")
}

// rankLanesWithGatePenalty re-orders lanes already ranked by trading.RankSpreads
// into ONE unified score that folds in two independent ranking-only adjustments
// the pure per-unit-spread view can't see:
//
//   - a cross-system gate penalty: cross-system lanes must clear a materially
//     higher bar than same-system ones, reflecting the extra time cost of a
//     jump plus cooldown.
//   - hold-fit weighting (sp-pnx0): a lane's VolumeCap is a market-absorption
//     bound, not a hold-sized one - a hull far bigger than VolumeCap will not
//     clear a single tranche at that depth before moving the price.
//     trading.HoldFitWeight scores how much of a lane's cap the hull can
//     actually absorb, saturating to 1.0 once the cap meets or exceeds the
//     hold. shipCapacity <= 0 (no ship context) disables this term entirely
//     (weight 1.0 for every lane), matching trading.RankSpreadsForHold's own
//     "zero disables" convention.
//
// These two adjustments MUST be folded into a single score computation rather
// than chained as two sequential re-rankings: this function and
// trading.RankSpreadsForHold/reorderByHoldFit are both "recompute-from-scratch"
// rankers that derive their score purely from each lane's own persistent
// fields, never from the order of the slice passed in. Composing them as
// funcB(funcA(lanes)) does not combine their effects - funcB completely
// overrides funcA's reordering, since funcB re-derives its own ranking from
// scratch using only the lanes' raw fields. (This is exactly the bug an earlier
// version of this call site had: scanLanes wrapped this function around
// trading.RankSpreadsForHold's already hold-weighted output, but silently
// discarded that weighting because this function's own score ignored input
// order entirely.)
//
// It returns a NEW slice of the original, unmodified ArbitrageLane values;
// only ordering changes, mirroring RankSpreads' own tie-break chain (score
// desc, then the lane's REAL unpenalized SpreadPerUnit desc, then Good asc)
// with the adjusted score substituted as the primary key only.
//
// targetDest (sp-xwa1's --dest override) waives the cross-system penalty for
// whichever lane laneMatchesTarget reports as the operator-directed one: a
// directed lane already carries the extra jump-gate time cost the penalty
// exists to warn against, so re-penalizing it in its own score would fight
// the operator's explicit choice. Every other cross-system lane is still
// penalized unchanged - targetDest narrows the exemption to the one lane
// asked for, it does not disable the penalty generally. targetDest=="" (the
// undirected auto-scan path) matches nothing, so behavior is byte-for-byte
// identical to before this lever existed.
func rankLanesWithGatePenalty(lanes []trading.ArbitrageLane, shipCapacity int, targetDest string) []trading.ArbitrageLane {
	type scoredLane struct {
		lane  trading.ArbitrageLane
		score float64
	}

	scored := make([]scoredLane, len(lanes))
	for i, lane := range lanes {
		effectiveSpread := lane.SpreadPerUnit
		crossSystem := shared.ExtractSystemSymbol(lane.SourceWaypoint) != shared.ExtractSystemSymbol(lane.DestWaypoint)
		if crossSystem && !laneMatchesTarget(lane, targetDest) {
			effectiveSpread -= crossSystemRankingPenaltyPerUnit
		}
		weight := 1.0
		if shipCapacity > 0 {
			weight = trading.HoldFitWeight(lane.VolumeCap, shipCapacity)
		}
		scored[i] = scoredLane{lane: lane, score: float64(effectiveSpread*lane.VolumeCap) * weight}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if scored[i].lane.SpreadPerUnit != scored[j].lane.SpreadPerUnit {
			return scored[i].lane.SpreadPerUnit > scored[j].lane.SpreadPerUnit
		}
		return scored[i].lane.Good < scored[j].lane.Good
	})

	result := make([]trading.ArbitrageLane, len(scored))
	for i, s := range scored {
		result[i] = s.lane
	}
	return result
}

// --- lane-targeting override (sp-xwa1) ---

// laneMatchesTarget reports whether lane is the operator-directed destination
// requested via --dest (RunTradeRouteCoordinatorCommand.TargetDest). An empty
// target never matches anything - the zero value means "no directive", not
// "match every lane" - so every caller can treat target=="" as the plain
// undirected path without a separate branch. A non-empty target matches
// either the lane's exact destination waypoint or just its destination
// SYSTEM, so an operator can aim at a whole system ("X1-ABC") without knowing
// which waypoint inside it currently carries the best market, or pin an exact
// waypoint for precision.
func laneMatchesTarget(lane trading.ArbitrageLane, target string) bool {
	if target == "" {
		return false
	}
	return lane.DestWaypoint == target || shared.ExtractSystemSymbol(lane.DestWaypoint) == shared.ExtractSystemSymbol(target)
}

// selectLane is the single lane-selection entry point for both the undirected
// auto-scan and the directed --dest override, so callers never duplicate the
// branch. Undirected (target=="") defers entirely to
// trading.FirstDisciplinedLane's existing ranked-order walk, unchanged.
// Directed (target!="") PINS to the first target-matching lane that clears
// the floor (ClearsFloor - the same discipline FirstDisciplinedLane enforces
// on the undirected path), walking the caller-supplied order rather than
// searching for the single highest-ranked lane overall: an operator who names
// a destination gets that destination if it is flyable at all, never a
// silent substitute the ranker would have preferred instead. If no
// target-matching lane clears the floor, it reports ok=false rather than
// falling back to an auto-picked lane the operator didn't ask for (the same
// "fail rather than silently substitute" contract the batch-purchase
// ship-type guard already established, sp-e7je).
func selectLane(lanes []trading.ArbitrageLane, target string) (trading.ArbitrageLane, bool) {
	if target == "" {
		return trading.FirstDisciplinedLane(lanes)
	}
	for _, l := range lanes {
		if laneMatchesTarget(l, target) && l.ClearsFloor() {
			return l, true
		}
	}
	return trading.ArbitrageLane{}, false
}

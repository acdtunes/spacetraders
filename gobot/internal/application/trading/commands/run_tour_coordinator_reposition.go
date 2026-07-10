package commands

// run_tour_coordinator_reposition.go — sp-zhii: on a continuous tour's margins-death,
// rank jump-reachable systems by expected tour margin, jump to the best one, and re-plan
// there instead of stranding the hull on its own freshly-sold-out ground. Bounded to ONE
// reposition per margins-death episode. The jump rides the SHARED cooldown-riding travel
// machinery (sp-wc5h via legs.RepositionToWaypoint), never new plumbing.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	// repositionMinMarginDefault is the default FRESH-profit floor a reposition
	// destination's planned tour must clear to justify the jump (sp-zhii) when the
	// captain leaves reposition_min_margin at 0. A jump costs antimatter + fuel + a
	// one-way ~352s hop (crossSystemHopSeconds) the hull spends NOT trading; at the
	// fleet's ~75 cr/s opportunity rate (hullOpportunityCreditsPerSecond) that hop is
	// worth ~26k in foregone home-lane earning, so a destination whose planned fresh
	// profit is below this bar is not worth relocating for — the coordinator exits
	// honestly (margins died) exactly as it did pre-sp-zhii. A named config knob, not a
	// magic constant (RULINGS #5): retune as the fleet's hop cost / opportunity rate
	// shifts. Deliberately a touch under the ~26k one-way hop cost so a genuinely
	// productive ground is never rejected for being a few thousand short.
	repositionMinMarginDefault = 25000

	// repositionMaxCandidatesDefault bounds how many pre-ranked candidate systems get a
	// real planner call per margins-death episode (sp-zhii) when the captain leaves
	// reposition_max_candidates at 0. The cheap in-system-spread pre-rank orders EVERY
	// jump-reachable candidate; only the top-K get a solver call, so the fan-out is
	// bounded (never one call per neighbor — RULINGS: never unbounded solver fan-out).
	// Small by design: the best few candidates by cached spread almost always contain
	// the best by planned tour margin, and a margins-death episode is rare (~30-60min per
	// hull), so a handful of extra planner calls at that boundary is cheap insurance
	// against a wasted jump.
	repositionMaxCandidatesDefault = 3
)

// repositionEpisode is the in-memory state of the current margins-death episode (sp-zhii):
// whether this run has already spent its ONE reposition since the last productive tour,
// and the systems involved so the honest exit can name both. It is reset by every
// productive tour (a fresh ground earned starts a new episode) and does not itself persist
// — the restart-durable slice (the in-flight destination) is RepositionEpisode, threaded
// through the container config.
type repositionEpisode struct {
	repositioned bool
	fromSystem   string
	toSystem     string
}

// repositionCandidate is one jump-reachable system in the reposition candidate set: the
// destination system, a representative market waypoint the hull will land at (and the
// planner prices the candidate tour from), and the cheap pre-rank score (best cached
// in-system capped spread) used to bound which candidates get a real planner call.
type repositionCandidate struct {
	system   string
	waypoint string
	score    int
}

// repositionScore is one candidate's evaluated result for the ranking-table log and the
// floor decision: its pre-rank score plus the planner's projected FRESH profit (the honest
// expected margin the floor gates against). feasible=false marks a candidate the planner
// declined (no profitable tour there / infeasible), which never wins.
type repositionScore struct {
	system      string
	waypoint    string
	prerank     int
	freshProfit int64
	feasible    bool
}

// maybeReposition is the sp-zhii margins-death rescue. Instead of exiting the instant a
// continuous tour's margins die, it RANKS jump-reachable systems by expected tour margin,
// JUMPS to the best one that clears the reposition floor, and lets the loop re-plan there
// — a hull stranded on its own freshly-sold-out ground rotates to a fresh renewable one.
// Bounded to ONE reposition per margins-death episode (episode.repositioned) so a
// persistently-dead neighbourhood cannot hop-scotch forever.
//
// Returns repositioned=true when the hull was relocated (the caller resets the streak and
// continues the loop, which re-plans from the arrived position). Returns false when no
// candidate is worth the jump — no jump-reachable system with cached data, or none whose
// planned tour clears the floor — so the caller exits honestly (margins dead). A non-nil
// error is an OPERATIONAL travel failure the runner should retry (resumable), never a "no
// candidate" verdict; the persisted in-flight destination is left set on a travel error so
// a restart resumes the jump (RULINGS #2).
func (h *RunTourCoordinatorHandler) maybeReposition(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	episode *repositionEpisode,
	maxHops int,
	maxSpend, reserve int64,
	modelVersion string,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	if cmd.RepositionDisabled {
		return false, nil // kill-switch: exit as pre-sp-zhii
	}
	if episode.repositioned {
		return false, nil // already spent this episode's one reposition — no hop-scotching
	}

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	currentSystem := ship.CurrentLocation().SystemSymbol

	candidates := h.buildRepositionCandidates(ctx, cmd, currentSystem)
	if len(candidates) == 0 {
		logger.Log("INFO", fmt.Sprintf("Reposition: no jump-reachable candidate system with cached market data from %s - exiting honestly (margins died)", currentSystem), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "current_system": currentSystem,
		})
		return false, nil
	}

	k := cmd.RepositionMaxCandidates
	if k <= 0 {
		k = repositionMaxCandidatesDefault
	}
	floor := int64(cmd.RepositionMinMargin)
	if floor <= 0 {
		floor = repositionMinMarginDefault
	}

	// Evaluate at most K pre-ranked candidates with a real planner call, ranking them by
	// planned FRESH profit (total minus held-liquidation minus synthetic deposit value —
	// the honest new-cash-earning a relocation buys, so a laden hull's launch-liquidation
	// can't flatter a dead ground into looking worth the jump).
	var evaluated []repositionScore
	var best *repositionScore
	for i, cand := range candidates {
		if i >= k {
			break
		}
		s := repositionScore{system: cand.system, waypoint: cand.waypoint, prerank: cand.score}
		plan, perr := h.planAtCandidate(ctx, ship, cand, maxHops, maxSpend, reserve, cmd, modelVersion)
		if perr == nil && plan != nil && plan.Feasible {
			s.feasible = true
			s.freshProfit = plan.ProjectedProfit - plan.HeldLiquidation - plan.DepositValue
			if best == nil || s.freshProfit > best.freshProfit {
				chosen := s
				best = &chosen
			}
		}
		evaluated = append(evaluated, s)
	}

	logRepositionRanking(logger, cmd.ShipSymbol, currentSystem, evaluated, best, floor)

	if best == nil || best.freshProfit < floor {
		// Nothing clears the floor: the jump costs more than the best destination is worth
		// (RULINGS #5) — exit honestly, exactly as pre-sp-zhii.
		return false, nil
	}

	// Commit the reposition. Persist the in-flight destination FIRST (RULINGS #2) so a
	// restart mid-jump resumes toward the same ground, jump through the shared
	// cooldown-riding travel machinery (sp-wc5h; SkipClaim — the container already holds
	// the hull, RULINGS #7), then clear the persisted flag once it lands.
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: true, TargetSystem: best.system, TargetWaypoint: best.waypoint})
	logger.Log("INFO", fmt.Sprintf("Reposition: margins died at %s - jumping to %s (%s), planned fresh profit %d >= floor %d, then re-planning there", currentSystem, best.system, best.waypoint, best.freshProfit, floor), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "from_system": currentSystem, "to_system": best.system,
		"to_waypoint": best.waypoint, "planned_fresh_profit": best.freshProfit, "floor": floor,
	})
	if terr := h.legs.RepositionToWaypoint(ctx, cmd.ShipSymbol, best.waypoint, cmd.PlayerID); terr != nil {
		// Leave the persisted in-progress state set: a restart resumes toward the same
		// destination. Surface the error resumable (the runner re-adopts and retries).
		return false, fmt.Errorf("reposition jump of %s to %s failed: %w", cmd.ShipSymbol, best.waypoint, terr)
	}
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: false})

	episode.repositioned = true
	episode.fromSystem = currentSystem
	episode.toSystem = best.system
	response.Repositions++
	return true, nil
}

// buildRepositionCandidates assembles the reposition candidate set: every system one
// jump-gate hop away from currentSystem (jump-reachable via the SAME neighbor-scan the
// tour graph uses) that has cached market data, EXCLUDING the current (dead) system. Each
// candidate carries a representative market waypoint (the source of its best cached lane,
// or its first cached market) and a cheap pre-rank score (that lane's capped spread), and
// the set is returned ordered best-score-first so the caller can bound the real planner
// calls to the top-K. Fail-open throughout: an unreadable neighbor simply contributes no
// candidate, never an aborted reposition.
func (h *RunTourCoordinatorHandler) buildRepositionCandidates(ctx context.Context, cmd *RunTourCoordinatorCommand, currentSystem string) []repositionCandidate {
	seen := map[string]bool{currentSystem: true} // exclude the current (dead) ground
	var candidates []repositionCandidate
	for _, sys := range h.legs.neighborSystems(ctx, currentSystem, cmd.PlayerID) {
		if sys == "" || seen[sys] {
			continue
		}
		seen[sys] = true
		listings, err := h.legs.collectSystemListings(ctx, sys, cmd.PlayerID)
		if err != nil || len(listings) == 0 {
			continue // no cached market data → not a candidate (requirement: cached-data systems only)
		}
		waypoint, score := bestInSystemLane(listings)
		if waypoint == "" {
			continue
		}
		candidates = append(candidates, repositionCandidate{system: sys, waypoint: waypoint, score: score})
	}
	// Cheap pre-rank: highest cached in-system capped spread first (a proxy for tour
	// margin), system symbol as a stable tie-break so the top-K bound is deterministic.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].system < candidates[j].system
	})
	return candidates
}

// bestInSystemLane ranks a candidate system's cached in-system arbitrage lanes and returns
// the source waypoint of the best one (the representative position the hull lands at and
// the planner prices the candidate tour from) plus that lane's capped spread (the cheap
// pre-rank score). A system with cached listings but no in-system lane still yields a
// representative waypoint (its first cached market) with score 0 — it stays a candidate (it
// may tour with a neighbour or its held cargo) but ranks below systems showing a live
// in-system spread.
func bestInSystemLane(listings []trading.GoodListing) (string, int) {
	lanes := trading.RankSpreads(listings)
	if len(lanes) > 0 {
		return lanes[0].SourceWaypoint, lanes[0].CappedSpread
	}
	return listings[0].Waypoint, 0
}

// planAtCandidate asks the planner for the tour the hull WOULD fly if it were already at
// the candidate system, WITHOUT moving it first (the pre-flight that avoids burning a jump
// on a dead destination). It builds a SYNTHETIC ship state positioned at the candidate's
// representative waypoint, carrying the hull's real capacity / cargo / fuel / engine, over
// the candidate-centred tour graph (the candidate plus its own jump-gate neighbours). The
// returned plan's projected profit is the honest expected margin the reposition floor
// gates against (requirement #3). Budget re-uses the current iteration's already-resolved
// max-spend (RULINGS #4: no money-guard change — the dynamic 25%/fixed cap is untouched).
func (h *RunTourCoordinatorHandler) planAtCandidate(
	ctx context.Context,
	ship *navigation.Ship,
	cand repositionCandidate,
	maxHops int,
	maxSpend, reserve int64,
	cmd *RunTourCoordinatorCommand,
	modelVersion string,
) (*routing.TourPlan, error) {
	shipState := h.tourShipState(ship)
	shipState.CurrentSystem = cand.system
	shipState.CurrentWaypoint = cand.waypoint
	allowedSystems := h.tourSystemsFrom(ctx, cand.system, cmd.PlayerID)
	return h.planForState(ctx, shipState, allowedSystems, maxHops, maxSpend, reserve, cmd, modelVersion)
}

// persistReposition writes the in-flight reposition destination (or its clearing) into the
// container config so a daemon restart mid-jump resumes toward the same ground (sp-zhii,
// RULINGS #2). Best-effort: no persister wired (tests, pre-wiring) or no container id → a
// no-op; a persistence error is advisory (durability, never a movement guard) so it is
// logged and swallowed, mirroring the arb buy-cost persister contract.
func (h *RunTourCoordinatorHandler) persistReposition(ctx context.Context, cmd *RunTourCoordinatorCommand, ep RepositionEpisode) {
	if h.repositionPersister == nil || cmd.ContainerID == "" {
		return
	}
	if err := h.repositionPersister.PersistRepositionState(ctx, cmd.ContainerID, cmd.PlayerID, ep); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to persist reposition state (restart-resume durability degraded, run continues): %v", err), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID, "in_progress": ep.InProgress, "error": err.Error(),
		})
	}
}

// logRepositionRanking emits the ranking table (requirement #1) as a greppable one-liner
// in the MESSAGE TEXT (which `container logs` keeps even though it drops the structured
// metadata map — the sp-149h/sp-iqyq renderer defect): each evaluated candidate's system,
// pre-rank score and planner-projected fresh profit, plus which was chosen and the floor.
func logRepositionRanking(logger common.ContainerLogger, shipSymbol, fromSystem string, evaluated []repositionScore, best *repositionScore, floor int64) {
	parts := make([]string, 0, len(evaluated))
	for _, s := range evaluated {
		if s.feasible {
			parts = append(parts, fmt.Sprintf("%s(prerank=%d,fresh=%d)", s.system, s.prerank, s.freshProfit))
		} else {
			parts = append(parts, fmt.Sprintf("%s(prerank=%d,infeasible)", s.system, s.prerank))
		}
	}
	chosen := "none (none cleared the floor)"
	if best != nil {
		chosen = fmt.Sprintf("%s (fresh %d)", best.system, best.freshProfit)
	}
	logger.Log("INFO", fmt.Sprintf("Reposition ranking from %s [%s] - floor %d, chosen %s", fromSystem, strings.Join(parts, ", "), floor, chosen), map[string]interface{}{
		"ship_symbol": shipSymbol, "from_system": fromSystem, "floor": floor, "candidates_evaluated": len(evaluated),
	})
}

// starvationExitDetail renders the human exit explanation the tourExitStarvation constant
// abbreviates. When a reposition was already spent this episode it NAMES BOTH the origin
// and the destination system (requirement #2: "margins died at X, repositioned to Y, died
// there too"), so a captain reading a completed continuous tour sees the full rotation
// story, not a bare "starvation".
func starvationExitDetail(episode repositionEpisode, base string) string {
	if !episode.repositioned {
		return base
	}
	if episode.fromSystem != "" {
		return fmt.Sprintf("%s; repositioned %s -> %s this episode but margins died there too", base, episode.fromSystem, episode.toSystem)
	}
	// fromSystem lost across a restart-resume — name the destination we rotated to.
	return fmt.Sprintf("%s; repositioned to %s this episode (resumed post-restart) but margins died there too", base, episode.toSystem)
}

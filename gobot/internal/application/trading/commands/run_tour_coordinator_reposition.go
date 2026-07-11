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
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
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

	// repositionReplanAllowanceSeconds prices the post-jump re-plan overhead (snapshot
	// assembly + the solver call + the reserve round-trip) into a candidate's
	// time-to-value for the sp-1wp8 rate ranking. Small next to the jump and the plan
	// itself, but pricing it keeps the denominator honest end-to-end: the rate a
	// candidate is ranked on is fresh profit over EVERYTHING between "decide to jump"
	// and "profit booked".
	repositionReplanAllowanceSeconds = 60.0
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
	// rate is the candidate's projected fresh credits/HOUR over its full time-to-value
	// (jump + re-plan + the plan's own projected wall-clock) — the sp-1wp8 ranking key.
	// hasRate=false means the pre-flight plan carried no usable time estimate (cph<=0);
	// the episode then falls back to absolute-fresh ordering (selectRepositionWinner).
	rate    float64
	hasRate bool
	// reason is WHY a non-feasible candidate was rejected, for the ranking log (sp-lxwn):
	// the solver's OWN infeasibility reason (e.g. "no_profitable_tour"), a "planner-error"
	// marker when the pre-flight CALL itself failed, or "" for a contender. Empty renders as
	// the bare "infeasible" fallback so the pre-sp-lxwn line shape is preserved when unset.
	reason string
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
	netBought map[string]int,
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
		metrics.RecordTourReposition(cmd.PlayerID, "failed") // sp-fbih P3: attempted, errored (resumable)
		return false, err
	}
	currentSystem := ship.CurrentLocation().SystemSymbol

	candidates := h.buildRepositionCandidates(ctx, cmd, currentSystem)
	if len(candidates) == 0 {
		metrics.RecordTourReposition(cmd.PlayerID, "no_candidate") // sp-fbih P3: map-wide margin exhaustion signal
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

	// Evaluate at most K pre-ranked candidates with a real planner call. Each feasible
	// candidate carries its planned FRESH profit (total minus held-liquidation minus
	// synthetic deposit value — the honest new-cash-earning a relocation buys, so a laden
	// hull's launch-liquidation can't flatter a dead ground into looking worth the jump)
	// AND its projected fresh-$/HOUR (sp-1wp8): the winner is the best RATE among
	// floor-clearing candidates, because a fresh=200k ground five minutes of plan away
	// earns more per hour than a fresh=345k ground twenty-five minutes away.
	var evaluated []repositionScore
	for i, cand := range candidates {
		if i >= k {
			break
		}
		s := repositionScore{system: cand.system, waypoint: cand.waypoint, prerank: cand.score}
		plan, perr := h.planAtCandidate(ctx, ship, cand, maxHops, maxSpend, reserve, cmd, modelVersion)
		if perr == nil && plan != nil && plan.Feasible {
			s.feasible = true
			s.freshProfit = plan.ProjectedProfit - plan.HeldLiquidation - plan.DepositValue
			s.rate, s.hasRate = repositionCandidateRate(s.freshProfit, plan)
		} else {
			// sp-lxwn: capture WHY this candidate is not a contender so the ranking log names
			// it. The solver returns "no_profitable_tour" for a tapped/depleted ground (it built
			// tours but none cleared profit>0) — distinct from a "planner-error" (the pre-flight
			// CALL failed) which the pre-fix code silently folded into the same bare "infeasible".
			s.reason = repositionCandidateReason(plan, perr)
		}
		evaluated = append(evaluated, s)
	}

	best, _, _ := selectRepositionWinner(evaluated, floor)
	logRepositionRanking(logger, cmd.ShipSymbol, currentSystem, evaluated, best, floor)

	if best == nil || best.freshProfit < floor {
		// Nothing clears the floor: the jump costs more than the best destination is worth
		// (RULINGS #5) — exit honestly, exactly as pre-sp-zhii.
		metrics.RecordTourReposition(cmd.PlayerID, "no_candidate") // sp-fbih P3: candidates ranked, none worth the jump
		return false, nil
	}

	// Commit the reposition. Persist the in-flight destination FIRST (RULINGS #2) so a
	// restart mid-jump resumes toward the same ground, jump through the shared
	// cooldown-riding travel machinery (sp-wc5h; SkipClaim — the container already holds
	// the hull, RULINGS #7), then clear the persisted flag once it lands.
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: true, TargetSystem: best.system, TargetWaypoint: best.waypoint})
	rateNote := ""
	if best.hasRate {
		rateNote = fmt.Sprintf(" (projected %.0f fresh/hr)", best.rate)
	}
	logger.Log("INFO", fmt.Sprintf("Reposition: margins died at %s - jumping to %s (%s), planned fresh profit %d >= floor %d%s, then re-planning there", currentSystem, best.system, best.waypoint, best.freshProfit, floor, rateNote), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "from_system": currentSystem, "to_system": best.system,
		"to_waypoint": best.waypoint, "planned_fresh_profit": best.freshProfit, "floor": floor,
		"planned_fresh_rate_per_hour": best.rate,
	})

	// sp-ed4i look-back loading: the reposition jump was a structural deadhead (RepositionToWaypoint
	// is a pure empty move). Before jumping, buy the best floor-clearing manifest of THIS system's
	// exports that the destination imports, so the crossing carries value. It is booked into
	// netBought/response, rides the jump, and the post-jump re-plan liquidates it as launch cargo
	// (sp-m5kv). No floor-clearing lane → nothing bought → an empty jump, exactly as pre-sp-ed4i.
	// Persisted-in-progress is set FIRST (above), so a restart mid-load resumes the jump carrying
	// whatever was already bought (RULINGS #2). Best-effort: it never blocks the reposition rescue.
	loadedUnits := h.loadLookbackManifest(ctx, cmd, response, netBought, currentSystem, best.system, maxSpend, reserve)

	if terr := h.legs.RepositionToWaypoint(ctx, cmd.ShipSymbol, best.waypoint, cmd.PlayerID); terr != nil {
		// Leave the persisted in-progress state set: a restart resumes toward the same
		// destination (carrying any look-back cargo already bought). Surface the error
		// resumable (the runner re-adopts and retries).
		metrics.RecordTourReposition(cmd.PlayerID, "failed") // sp-fbih P3: jump attempted, travel errored (resumable)
		return false, fmt.Errorf("reposition jump of %s to %s failed: %w", cmd.ShipSymbol, best.waypoint, terr)
	}
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: false})
	// sp-ed4i: the crossing committed — record whether it carried a manifest (loaded) or flew
	// empty, so the deadhead empty-rate (loaded=false / total) is a dashboard read.
	metrics.RecordTourJumpLoaded(cmd.PlayerID, loadedUnits > 0)

	episode.repositioned = true
	episode.fromSystem = currentSystem
	episode.toSystem = best.system
	response.Repositions++
	metrics.RecordTourReposition(cmd.PlayerID, "success") // sp-fbih P3: hull rotated to a fresh ground
	return true, nil
}

// repositionNeighborEdge is one directly-gated neighbor of the origin system in the reposition
// candidate scan, carrying the gate's build state so an under-construction neighbor is rejected
// with a named reason (sp-1ki5 #3) rather than pre-flighted into a hop-time crash.
type repositionNeighborEdge struct {
	system            string
	underConstruction bool
}

// neighborRejection is one directly-gated neighbor the reposition scan considered but did NOT
// turn into a candidate, with the reason (unbuilt / no-cached-market / stale-data / …). It is the
// per-neighbor detail the empty-discovery log names so a "no candidates" verdict is
// self-diagnosing and never again costs a canary flight to explain (sp-1ki5 #3).
type neighborRejection struct {
	system string
	reason string
}

// buildRepositionCandidates assembles the reposition candidate set: every system one jump-gate
// hop away from currentSystem that has fresh cached market data, EXCLUDING the current (dead)
// system. Each candidate carries a representative market waypoint (the source of its best cached
// lane, or its first cached market) and a cheap pre-rank score (that lane's capped spread), and
// the set is returned ordered best-score-first so the caller can bound the real planner calls to
// the top-K.
//
// Neighbor resolution is DURABLE-FIRST (sp-1ki5): it reads the persisted era-scoped gate_edges
// adjacency (h.legs.repositionNeighbors), which answers regardless of the origin's charting/ship
// state, instead of depending solely on the live GetJumpGate scan the tour graph uses — that live
// call refuses an uncharted origin gate with 4001 and fails open to nil, which is exactly how
// discovery returned ZERO candidates from X1-DP51 while its direct neighbor X1-GQ92 sat
// 1-min-fresh. Fail-open throughout: an unreadable neighbor simply contributes no candidate, never
// an aborted reposition. An EMPTY result logs WHY per rejected neighbor (requirement #3).
func (h *RunTourCoordinatorHandler) buildRepositionCandidates(ctx context.Context, cmd *RunTourCoordinatorCommand, currentSystem string) []repositionCandidate {
	now := h.clock.Now()
	neighbors, originReason := h.legs.repositionNeighbors(ctx, currentSystem, cmd.PlayerID)
	seen := map[string]bool{currentSystem: true} // exclude the current (dead) ground
	var candidates []repositionCandidate
	var rejections []neighborRejection
	for _, nb := range neighbors {
		sys := nb.system
		if sys == "" || seen[sys] {
			continue
		}
		seen[sys] = true
		// sp-8qhu/sp-1ki5: a neighbor whose gate is still building cannot be a candidate — the jump
		// would fail at hop time. Name it "unbuilt" in the empty-discovery log, never a silent drop.
		if nb.underConstruction {
			rejections = append(rejections, neighborRejection{system: sys, reason: "unbuilt"})
			continue
		}
		listings, err := h.legs.collectSystemListings(ctx, sys, cmd.PlayerID)
		if err != nil {
			rejections = append(rejections, neighborRejection{system: sys, reason: "market-read-error"})
			continue
		}
		if len(listings) == 0 {
			rejections = append(rejections, neighborRejection{system: sys, reason: "no-cached-market"})
			continue // no cached market data → not a candidate (requirement: cached-data systems only)
		}
		// sp-lxwn: pre-rank only on FRESH listings — the same maxListingAge cap the solver's
		// tour snapshot (BuildTourSnapshot) applies. The pre-rank ignored ObservedAt entirely
		// (bestLaneForGood never checks it), so a candidate whose headline lane priced off a
		// >75-min-stale market read HEALTHY here yet the solver, whose snapshot dropped that
		// stale row, found no profitable tour (field: X1-ZC66 pre-ranked 157500 off a 131-min
		// -stale source, solver-infeasible). Worse, a stale-inflated score could crowd a
		// genuinely-fresh candidate out of the bounded top-K solver pre-flight. Aligning the
		// pre-rank freshness with the snapshot keeps the top-K ordered by tradeable spread.
		fresh := freshListings(listings, now, maxListingAge)
		// sp-k7q5 layer 2: count the lanes this pre-rank drops for staleness, the same
		// exclusion BuildTourSnapshot counts on the solver side — so a candidate system
		// going invisible to the reposition ranking shows up on
		// tour_lanes_stale_excluded_total, not just silently. Observation only (RULINGS #4).
		if dropped := len(listings) - len(fresh); dropped > 0 {
			metrics.RecordTourLanesStaleExcluded(cmd.PlayerID, sys, dropped)
		}
		if len(fresh) == 0 {
			rejections = append(rejections, neighborRejection{system: sys, reason: "stale-data"})
			continue // every cached row is stale → the solver would see no fresh data here either
		}
		waypoint, score := bestInSystemLane(fresh)
		if waypoint == "" {
			rejections = append(rejections, neighborRejection{system: sys, reason: "no-waypoint"})
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
	// sp-1ki5 #3: an empty discovery must name WHY — the origin-level reason when no gated
	// neighbor resolved at all (the X1-DP51 shape), or the per-neighbor rejection reasons when
	// neighbors were found but each fell out. Emitted ONLY on empty; a populated scan logs its
	// ranking table downstream (logRepositionRanking).
	if len(candidates) == 0 {
		logRepositionDiscoveryEmpty(common.LoggerFromContext(ctx), cmd.ShipSymbol, currentSystem, neighbors, rejections, originReason)
	}
	return candidates
}

// logRepositionDiscoveryEmpty explains WHY the reposition candidate scan came up empty, so a "no
// candidates" verdict is self-diagnosing (sp-1ki5 #3 — the pre-fix empty cost a canary flight to
// diagnose). It names either the origin-level reason (no gated neighbor reachable from the origin —
// the X1-DP51 shape, where the live jump-gate API refused the uncharted origin gate and no durable
// edge answered) or, when neighbors WERE resolved but each fell out, the per-neighbor rejection
// reason (unbuilt / no-cached-market / stale-data). Put in the MESSAGE TEXT, which `container logs`
// keeps even though it drops the structured metadata map (the sp-149h/sp-iqyq renderer defect).
func logRepositionDiscoveryEmpty(logger common.ContainerLogger, shipSymbol, originSystem string, neighbors []repositionNeighborEdge, rejections []neighborRejection, originReason string) {
	if len(neighbors) == 0 {
		reason := originReason
		if reason == "" {
			reason = "no-neighbors"
		}
		logger.Log("INFO", fmt.Sprintf("Reposition discovery empty from %s - no directly-gated neighbor resolved (%s); durable gate_edges adjacency is origin-independent, so this is a genuine no-adjacency, not an uncharted-origin-gate live-API refusal", originSystem, reason), map[string]interface{}{
			"ship_symbol": shipSymbol, "origin_system": originSystem, "origin_reason": reason,
		})
		return
	}
	parts := make([]string, 0, len(rejections))
	for _, r := range rejections {
		parts = append(parts, fmt.Sprintf("%s(%s)", r.system, r.reason))
	}
	logger.Log("INFO", fmt.Sprintf("Reposition discovery empty from %s - %d directly-gated neighbor(s) resolved but none became a candidate: %s", originSystem, len(neighbors), strings.Join(parts, ", ")), map[string]interface{}{
		"ship_symbol": shipSymbol, "origin_system": originSystem, "neighbors_resolved": len(neighbors), "rejected": len(rejections),
	})
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

// freshListings drops cached rows older than maxAge relative to now, so the reposition
// pre-rank scores candidates only on markets the solver's tour snapshot (BuildTourSnapshot,
// same maxListingAge cap) would also admit (sp-lxwn). A zero ObservedAt means "unknown age"
// and is kept — the fail-open GoodListing/BuildTourSnapshot convention (an unstamped row ranks
// as fresh rather than being silently discarded).
func freshListings(listings []trading.GoodListing, now time.Time, maxAge time.Duration) []trading.GoodListing {
	fresh := make([]trading.GoodListing, 0, len(listings))
	for _, l := range listings {
		if l.ObservedAt.IsZero() || now.Sub(l.ObservedAt) <= maxAge {
			fresh = append(fresh, l)
		}
	}
	return fresh
}

// repositionCandidateRate prices a candidate as projected FRESH credits/HOUR over its
// full time-to-value (sp-1wp8): the one-way jump (crossSystemHopSeconds — reposition
// candidates are one gate hop away by construction, buildRepositionCandidates), the
// post-jump re-plan allowance, and the candidate plan's own projected wall-clock,
// recovered by inverting the solver's cph (cph = profit/(seconds/3600) ⇒ seconds =
// profit/cph×3600 — pure algebra on the response, no proto change). ok=false when the
// plan carries no usable time estimate (cph<=0, e.g. a degenerate/mocked planner);
// callers then fall back to absolute-fresh ordering rather than ranking a real rate
// against a guess (the sp-1wp8 divide-by-zero regression pin).
func repositionCandidateRate(freshProfit int64, plan *routing.TourPlan) (float64, bool) {
	if plan == nil || plan.ProjectedProfit <= 0 || plan.ProjectedCreditsPerHour <= 0 {
		return 0, false
	}
	planSeconds := float64(plan.ProjectedProfit) / plan.ProjectedCreditsPerHour * 3600
	hours := (crossSystemHopSeconds + repositionReplanAllowanceSeconds + planSeconds) / 3600
	return float64(freshProfit) / hours, true
}

// selectRepositionWinner picks the reposition destination (sp-1wp8): the highest
// projected fresh-$/hr among FLOOR-CLEARING feasible candidates — the floor stays
// ABSOLUTE (a blazing rate on a sub-floor fresh profit never justifies the jump) —
// with equal-rate ties broken on absolute fresh profit. When any floor-clearing
// candidate lacks a time estimate the whole choice falls back to absolute-fresh
// ordering (rateMode=false): comparing a real rate against a guess is not a ranking.
// profitMax names the absolute-fresh leader among the same floor-clearing set so the
// ranking log can show when rate REORDERED the choice (the acceptance evidence).
// When no candidate clears the floor, winner is the best feasible by fresh profit —
// preserving the pre-sp-1wp8 caller contract where the floor gate and the honest
// "best X < floor" exit log read it; winner is nil only when nothing is feasible.
func selectRepositionWinner(evaluated []repositionScore, floor int64) (winner, profitMax *repositionScore, rateMode bool) {
	var clearing []*repositionScore
	for i := range evaluated {
		if evaluated[i].feasible && evaluated[i].freshProfit >= floor {
			clearing = append(clearing, &evaluated[i])
		}
	}
	if len(clearing) == 0 {
		var best *repositionScore
		for i := range evaluated {
			s := &evaluated[i]
			if s.feasible && (best == nil || s.freshProfit > best.freshProfit) {
				best = s
			}
		}
		return best, best, false
	}
	rateMode = true
	for _, s := range clearing {
		if !s.hasRate {
			rateMode = false
			break
		}
	}
	winner, profitMax = clearing[0], clearing[0]
	for _, s := range clearing[1:] {
		if s.freshProfit > profitMax.freshProfit {
			profitMax = s
		}
		if rateMode {
			if s.rate > winner.rate || (s.rate == winner.rate && s.freshProfit > winner.freshProfit) {
				winner = s
			}
		} else if s.freshProfit > winner.freshProfit {
			winner = s
		}
	}
	return winner, profitMax, rateMode
}

// repositionCandidateReason renders WHY a pre-flight candidate is not a contender, for the
// ranking log (sp-lxwn). It disambiguates the two failure classes the pre-fix bare "infeasible"
// conflated:
//   - the solver returned a verdict → its OWN infeasibility reason (e.g. "no_profitable_tour":
//     tours were built but none cleared profit>0 — a tapped ground), plus the best rejected
//     tour when the solver named one (barely-negative vs nothing-at-all is the diagnostic tell);
//   - the pre-flight CALL itself failed (a gRPC/snapshot error) → a "planner-error" marker, a
//     categorically different failure the old code silently swallowed as "infeasible".
//
// Commas and parentheses are neutralised and the result is length-bounded so the reason stays a
// single well-formed token inside the comma-joined, paren-delimited ranking line. A feasible
// plan is a contender, not a rejection, and yields "".
func repositionCandidateReason(plan *routing.TourPlan, perr error) string {
	switch {
	case perr != nil:
		return truncateReason("planner-error: " + sanitizeReasonToken(perr.Error()))
	case plan == nil:
		return "no-plan"
	case plan.Feasible:
		return ""
	default:
		reason := plan.InfeasibleReason
		if reason == "" {
			reason = "infeasible"
		}
		reason = sanitizeReasonToken(reason)
		if len(plan.TopRejected) > 0 {
			reason += "; best: " + sanitizeReasonToken(plan.TopRejected[0])
		}
		return truncateReason(reason)
	}
}

// sanitizeReasonToken neutralises the delimiters the comma-joined, paren-delimited ranking line
// relies on so an externally-sourced reason (solver reason, error text, rejected-tour summary)
// can never fracture the greppable one-line format.
func sanitizeReasonToken(s string) string {
	return strings.NewReplacer(",", ";", "(", "", ")", "").Replace(s)
}

// truncateReason bounds a reason token so a chatty solver reason (a long rejected-tour path)
// cannot blow up the ranking line, cutting on a rune boundary so a multibyte glyph (the "→"
// in tour summaries) is never split into invalid UTF-8.
func truncateReason(s string) string {
	const max = 160
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "..."
	}
	return s
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
	// sp-m9co Class B: the hull is NOT at the candidate yet — it will JUMP there, riding the
	// shared cooldown-riding travel machinery (which refuels en route), and arrive with a
	// hold ready to trade. So the pre-flight must measure the CANDIDATE's FRESH arb potential
	// against a ready-to-tour hull: an AVAILABLE hold and full fuel.
	//
	// Carrying the drained hull's CURRENT cargo here was the field bug (2B-0691151d): the
	// solver seats launch cargo in every hold slot for the whole tour (tour_solver.py
	// occ = [total_initial]*n), so a hull whose hold is clogged with cargo the candidate
	// cannot sink has ZERO slack for fresh arb. Because that cargo is unsellable at every
	// foreign candidate alike, EVERY candidate then pre-flights infeasible — which is exactly
	// how three healthy-prerank grounds (X1-UQ16/FQ55/XT71) all read "infeasible" and the
	// hull completed on its home ground instead of rotating. The leftover cargo stays on the
	// REAL hull and is handled by the post-jump LIVE re-plan (liquidated as launch inventory
	// where a sink exists, or surfaced by the stranded veto) — it must never veto the
	// reconnaissance that decides whether the fresh ground is worth the jump. Clearing it
	// also zeroes HeldLiquidation, so the reposition floor gates on pure fresh profit, never
	// on launch-liquidation the destination can't actually pay.
	//
	// (Fuel is inert to today's solver, but full fuel is the honest post-travel arrival state
	// and mirrors a fresh launch — future-proofing a fuel-aware solver at no cost.)
	shipState.Cargo = nil
	shipState.FuelCurrent = shipState.FuelCapacity
	allowedSystems := h.tourSystemsFrom(ctx, cand.system, cmd.PlayerID)
	// The pre-flight only PRICES a candidate ground (it reserves nothing — no plan is
	// committed here, sp-78ai L3); the snapshot and netted absorption the reserve/accept
	// path would need are discarded (no plan is accepted here, so no burn-in emission).
	plan, _, _, err := h.planForState(ctx, shipState, allowedSystems, maxHops, maxSpend, reserve, cmd, modelVersion)
	return plan, err
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
// pre-rank score and planner verdict, plus which was chosen and the floor.
//
// The verdict distinguishes THREE states (sp-m9co: the pre-fix line conflated them, which
// cost diagnosis time on the 2B episode — every candidate read "infeasible" yet the summary
// said "none cleared the floor", implying they were merely thin):
//   - infeasible: the solver declined a real tour (the ground itself cannot be toured);
//   - fresh=N,below-floor: tourable, but the planned fresh margin is under the relocation
//     floor (RULINGS #5) — feasible yet not worth the jump, NOT the same as infeasible;
//   - fresh=N[,rate=R/hr]: tourable and clears the floor (a real contender / the chosen
//     one), with its projected fresh-$/hr when the plan carried a time estimate (sp-1wp8).
//
// sp-1wp8 acceptance evidence: when the RATE ordering chose a different candidate than
// absolute fresh profit would have, the chosen note names the out-ranked profit-max
// candidate ("rate-reorder over profit-max …") so both orderings are visible on the line.
func logRepositionRanking(logger common.ContainerLogger, shipSymbol, fromSystem string, evaluated []repositionScore, best *repositionScore, floor int64) {
	parts := make([]string, 0, len(evaluated))
	for _, s := range evaluated {
		switch {
		case !s.feasible:
			// sp-lxwn: name the SPECIFIC rejection reason (the solver's own "no_profitable_tour",
			// a "planner-error", etc.) instead of the pre-fix opaque bare "infeasible". Fall back
			// to "infeasible" when no reason was captured, preserving the old line shape.
			reason := s.reason
			if reason == "" {
				reason = "infeasible"
			}
			parts = append(parts, fmt.Sprintf("%s(prerank=%d,%s)", s.system, s.prerank, reason))
		case s.freshProfit < floor:
			parts = append(parts, fmt.Sprintf("%s(prerank=%d,fresh=%d,below-floor)", s.system, s.prerank, s.freshProfit))
		case s.hasRate:
			parts = append(parts, fmt.Sprintf("%s(prerank=%d,fresh=%d,rate=%.0f/hr)", s.system, s.prerank, s.freshProfit, s.rate))
		default:
			parts = append(parts, fmt.Sprintf("%s(prerank=%d,fresh=%d)", s.system, s.prerank, s.freshProfit))
		}
	}
	var chosen string
	switch {
	case best != nil && best.freshProfit >= floor:
		chosen = fmt.Sprintf("%s (fresh %d)", best.system, best.freshProfit)
		// Re-derive the choice context from the same pure selector the caller used, so the
		// annotation can never drift from the actual decision: in rate mode the chosen
		// line carries the winner's rate, and — when rate REORDERED the choice — names the
		// profit-max candidate it out-ranked (both orderings visible, sp-1wp8).
		if _, profitMax, rateMode := selectRepositionWinner(evaluated, floor); rateMode && best.hasRate {
			chosen = fmt.Sprintf("%s (fresh %d, rate %.0f/hr)", best.system, best.freshProfit, best.rate)
			if profitMax != nil && profitMax.system != best.system {
				chosen += fmt.Sprintf("; rate-reorder over profit-max %s (fresh %d)", profitMax.system, profitMax.freshProfit)
			}
		}
	case best != nil:
		// A best feasible candidate exists but falls under the floor — name it so the log
		// shows the ground WAS tourable, just not worth the jump (distinct from all-infeasible).
		chosen = fmt.Sprintf("none (best %s fresh %d < floor %d)", best.system, best.freshProfit, floor)
	default:
		chosen = fmt.Sprintf("none (all %d candidate(s) solver-infeasible)", len(evaluated))
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

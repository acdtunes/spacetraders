package commands

import (
	"context"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// sp-jsng — "Wider candidate set" (epic sp-fguo build-decomp #5). This file owns the
// allowedSystems PRODUCER only: the arming gate, the hop-depth/shortlist resolvers, the
// widened breadth-first candidate gather, the profitable-edge shortlist, and the 1-hop
// floor. It does NOT touch tour_solver.py, the TourConstraints proto, MAX_TOUR_SYSTEMS,
// the planner seam, the tranche scorer, or the absorption ledger — those are sibling
// children. Everything here is dormant behind two independent guards (a hop-depth knob
// defaulting to 1 AND an arming gate keyed to the solver clamp), so it is byte-identical
// at the epic defaults; a prod flip is governed by the sp-f1yk replay gate.

const (
	candidateHopDepthDefault      = 1
	maxCandidateHopDepth          = 3 // spec "2-3 gate hops"; caps a `candidate_hop_depth: 30` typo
	candidateShortlistTopNDefault = 6
)

// resolveCandidateHopDepth maps the configured gate-hop radius to its effective value:
// 0/negative → the default (1, today's exact behavior); otherwise clamped to
// [1, maxCandidateHopDepth] so a fat-fingered `candidate_hop_depth: 30` can never fan the
// durable BFS out past the spec's 3-hop ceiling.
func resolveCandidateHopDepth(configured int) int {
	if configured <= 0 {
		return candidateHopDepthDefault
	}
	if configured > maxCandidateHopDepth {
		return maxCandidateHopDepth
	}
	return configured
}

// resolveCandidateShortlistTopN maps the configured shortlist bound: 0/negative → the
// default (6); otherwise the value.
func resolveCandidateShortlistTopN(configured int) int {
	if configured <= 0 {
		return candidateShortlistTopNDefault
	}
	return configured
}

// effectiveCandidateHopDepth is the sp-jsng ARMING GATE. The configured depth only takes
// EFFECT once the Python solver clamp has actually been lifted; otherwise it is floored to
// 1. This makes the depth>=2 widening branch unreachable while tour_solver.py still clamps
// at MAX_TOUR_SYSTEMS=2 with flat INTER_SYSTEM_TRAVEL_SECONDS pricing, so a lone live-config
// edit of candidate_hop_depth can never underprice a non-gate-adjacent multi-hop deadhead.
//
// Integration checkpoint (sp-syaz): syaz threads cmd.MaxTourSystems with 0-as-absent
// semantics — 0 maps to the solver's MAX_TOUR_SYSTEMS default of 2, and there is no Go
// resolver (the 0→2 mapping lives in tour_solver.py). So both 0 and 2 mean "clamp
// unchanged, do not widen"; only a value strictly greater than 2 opens the gate.
func (h *RunTourCoordinatorHandler) effectiveCandidateHopDepth(cmd *RunTourCoordinatorCommand) int {
	depth := resolveCandidateHopDepth(cmd.CandidateHopDepth)
	if depth <= 1 {
		return depth
	}
	if cmd.MaxTourSystems <= 2 {
		return 1 // clamp not lifted — widening would be flat-priced; hold at today's behavior
	}
	return depth
}

// widenedTourSystems (depth>=2, arming-gated) returns oneHop ∪ {top-N ≥2-hop systems with a
// profitable incident edge}. It can NEVER be narrower than oneHop.
func (h *RunTourCoordinatorHandler) widenedTourSystems(
	ctx context.Context, home string, cmd *RunTourCoordinatorCommand, oneHop []string,
) []string {
	// No durable graph → cannot widen; return the exact depth-1 set. WITHOUT this explicit
	// check, repositionNeighborsWithinJumps falls back to the LIVE neighborSystems scan and
	// returns a NON-EMPTY set, so the shortlist would then FILTER the live 1-hop neighbors —
	// collapsing NARROWER than baseline. Floor is not enough on its own; the gate is here.
	if h.legs.gateGraph == nil {
		return oneHop
	}
	depth := h.effectiveCandidateHopDepth(cmd)
	far, _ := h.legs.repositionNeighborsWithinJumps(ctx, home, cmd.PlayerID, depth)
	if len(far) == 0 {
		return oneHop // present graph but genuinely no adjacency — depth-1 baseline stands
	}
	shortlist := h.shortlistByProfitableEdge(ctx, home, far, cmd.PlayerID,
		resolveCandidateShortlistTopN(cmd.CandidateShortlistTopN))
	// FLOOR: union oneHop so widened ⊇ 1-hop ALWAYS. The 1-hop neighbors are today's baseline;
	// the shortlist's drop-zero-edge applies only to the ≥2-hop systems it ADDS.
	return unionSystems(oneHop, shortlist)
}

// shortlistByProfitableEdge scores each FAR system by the max CappedSpread of any RankSpreads
// lane incident to it (as SourceWaypoint OR DestWaypoint) across the union of fresh listings
// from home+far, and returns the top-N positive-score far systems. Home is NOT included here
// (the caller floors it). CappedSpread > 0 by construction, so only PROFITABLE edges score;
// zero-edge systems drop. Deterministic: score desc, then system symbol asc (mirrors the
// sibling reposition sort).
func (h *RunTourCoordinatorHandler) shortlistByProfitableEdge(
	ctx context.Context, home string, far []repositionNeighborEdge, playerID, topN int,
) []string {
	// Build the fresh-listing union AND a waypoint->system map. There is no SystemFromWaypoint
	// helper, so tag at collection time — every row from collectSystemListings(sys) is in sys.
	wpSystem := map[string]string{}
	var union []trading.GoodListing
	collect := func(sys string) {
		rows, err := h.legs.collectSystemListings(ctx, sys, playerID)
		if err != nil {
			return // unreadable system contributes no lanes (fail-open)
		}
		for _, row := range freshListings(rows, h.clock.Now(), maxListingAge) {
			wpSystem[row.Waypoint] = sys
			union = append(union, row)
		}
	}
	collect(home)
	for _, edge := range far {
		collect(edge.system)
	}

	// Score each system by the deepest capped spread of an incident cross-system lane.
	best := map[string]int{}
	for _, lane := range trading.RankSpreads(union) {
		scoreLaneEndpoint(best, wpSystem, lane.SourceWaypoint, lane.CappedSpread)
		scoreLaneEndpoint(best, wpSystem, lane.DestWaypoint, lane.CappedSpread)
	}

	type scored struct {
		system string
		score  int
	}
	cands := make([]scored, 0, len(far))
	for _, edge := range far {
		if score := best[edge.system]; score > 0 && edge.system != home {
			cands = append(cands, scored{edge.system, score})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score // score desc
		}
		return cands[i].system < cands[j].system // symbol asc — deterministic cut
	})
	if len(cands) > topN {
		cands = cands[:topN]
	}
	out := make([]string, 0, len(cands))
	for _, candidate := range cands {
		out = append(out, candidate.system)
	}
	return out
}

// scoreLaneEndpoint raises the incident system's best score to cappedSpread when a lane
// touches waypoint. Unknown waypoints (not in the union map) contribute nothing.
func scoreLaneEndpoint(best map[string]int, wpSystem map[string]string, waypoint string, cappedSpread int) {
	sys, ok := wpSystem[waypoint]
	if !ok || cappedSpread <= best[sys] {
		return
	}
	best[sys] = cappedSpread
}

// unionSystems returns the entries of a (in order, deduped) followed by every entry of b not
// already present. Empty strings are dropped. Used to FLOOR the widened set: a is oneHop, b
// is the ≥2-hop shortlist, so the result is always a superset of oneHop with stable order.
func unionSystems(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	appendUnseen := func(systems []string) {
		for _, sys := range systems {
			if sys == "" || seen[sys] {
				continue
			}
			seen[sys] = true
			out = append(out, sys)
		}
	}
	appendUnseen(a)
	appendUnseen(b)
	return out
}

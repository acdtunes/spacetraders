package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
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

// unprovenCrossingReason labels crossings the hop matrix could not price from stored topology.
// This counter staying non-zero means gate coverage is thin enough that some allowed pairs are
// being refused rather than ranked — it is the guard reporting, not a fault.
const unprovenCrossingReason = "inter_system_crossing_unproven"

// unprovenCrossingHops is what a crossing the matrix could not prove is CHARGED. The solver has
// no way to express an impassable leg — it reads an ABSENT pair as one gate hop, its cheapest
// possible charge — so a route that cannot be proven must still be priced, and priced past every
// distance the walk was able to resolve. One hop beyond the walk's own bound is the tightest
// honest statement available: whatever the route is, it is longer than anything measurable here.
func unprovenCrossingHops(bound int) int { return bound + 1 }

// canonicalSystemPair orders a system pair so a crossing found from either endpoint keys the same.
func canonicalSystemPair(a, b string) [2]string {
	if b < a {
		a, b = b, a
	}
	return [2]string{a, b}
}

// tourInterSystemHops resolves the gate-hop distance between every pair of systems in
// allowedSystems, so the solver prices a cross-system crossing at gate_hops x the per-crossing
// charge instead of a flat 1 hop. That flat charge is why the map exists: the solver defaults an
// absent pair to ONE hop, so a three-hop crossing left unresolved ranks roughly three times too
// well and the planner picks tours whose travel it has understated.
//
// Distances come from the STORE ALONE (StoredRankingDistances): one adjacency read plus an exact
// depth-bounded walk, no fetch-through and therefore no live API. That matters twice over. The
// walk is depth-bounded but NOT breadth-bounded, so a dense neighbourhood around either endpoint
// can never exhaust a discovery budget and leave a genuinely-near pair looking unreachable; and
// the fetch-through seam every other resolver reaches its neighbours through would spend live gate
// requests per system per plan, on a budget already at the server ceiling.
//
// This is the RANKING walk, not the far-sink admission guard's proof-grade one. A price is not a
// commitment — the executor still resolves the real route strictly at flight time — so topology
// merely past its freshness window is read rather than refused, which is the accurate answer and
// not a laxer one: a gate's build state moves only toward built, and a set that has since gained
// an edge over-estimates. What stays refused is what is genuinely unusable — an under-construction
// gate, and a system never cached at all.
//
// EVERY allowed system is walked rather than one endpoint per pair: the walk refuses to expand
// THROUGH a system whose own topology is unverified but resolves one it merely ARRIVES at, so a
// pair unprovable from one end is frequently provable from the other, and the shortest proven
// route wins.
//
// A pair either prices at its proven distance or is REFUSED at unprovenCrossingHops and counted —
// never silently dropped into the solver's 1-hop default. Only >1-hop distances are emitted; a
// proven 1-hop crossing already prices exactly at the flat charge. One entry per unordered pair
// (from < to), deterministically ordered so the payload and its logs are reproducible.
//
// COMPUTED ONLY when the horizon is actually widened (MaxTourSystems > 2): at the default cap a
// tour touches at most 2 systems (start + one gate neighbor), so every crossing is a single gate
// hop the flat charge already prices exactly — the map is empty and the wire is byte-identical.
// A nil gate graph (graph-less tests / pre-wiring) or a sub-2 system set likewise yields no map.
func (h *RunTourCoordinatorHandler) tourInterSystemHops(
	ctx context.Context, allowedSystems []string, cmd *RunTourCoordinatorCommand,
) []routing.InterSystemHopDistance {
	// Arming gate: below the raised cap a tour touches at most 2 systems, so every crossing is a
	// single gate hop the flat charge prices exactly — no map, byte-identical. A nil gate graph or
	// a sub-2 system set likewise cannot (and need not) resolve any multi-hop distance.
	if cmd.MaxTourSystems <= 2 || h.legs.gateGraph == nil || len(allowedSystems) < 2 {
		return nil
	}
	// The bound must span the widest pairwise distance among allowed systems. Candidate-walk
	// systems sit within effectiveCandidateHopDepth gate hops of home, so any two are within twice
	// that (via home); an admitted far sink sits within the executor's flight bound of every other
	// allowed system, so the bound must clear that too.
	bound := 2 * h.effectiveCandidateHopDepth(cmd)
	if bound < gategraph.MaxJumpPath {
		bound = gategraph.MaxJumpPath
	}
	proven := make(map[[2]string]int)
	for _, from := range allowedSystems {
		hops, err := h.legs.gateGraph.StoredRankingDistances(ctx, from, allowedSystems, bound)
		if err != nil {
			// An unreadable store proves nothing. Whatever it leaves unproven is refused below —
			// never priced on the strength of a read that failed.
			continue
		}
		for to, distance := range hops {
			if to == from || distance <= 0 {
				continue
			}
			pair := canonicalSystemPair(from, to)
			if best, seen := proven[pair]; !seen || distance < best {
				proven[pair] = distance
			}
		}
	}
	out := make([]routing.InterSystemHopDistance, 0, len(proven))
	priced := make(map[[2]string]bool, len(allowedSystems))
	unproven := 0
	for i, from := range allowedSystems {
		for _, to := range allowedSystems[i+1:] {
			pair := canonicalSystemPair(from, to)
			if pair[0] == pair[1] || priced[pair] {
				continue
			}
			priced[pair] = true
			hops, resolved := proven[pair]
			if !resolved {
				hops = unprovenCrossingHops(bound)
				unproven++
			}
			if hops <= 1 {
				continue
			}
			out = append(out, routing.InterSystemHopDistance{
				FromSystem: pair[0], ToSystem: pair[1], GateHops: hops,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FromSystem != out[j].FromSystem {
			return out[i].FromSystem < out[j].FromSystem
		}
		return out[i].ToSystem < out[j].ToSystem
	})
	h.logUnprovenCrossings(ctx, cmd.PlayerID, unproven, len(priced), unprovenCrossingHops(bound))
	return out
}

// logUnprovenCrossings makes the refusals countable and legible: a crossing charged past the
// walk's reach is a decision the planner acted on, so it must be readable from the metric and
// the log rather than inferred from a tour that quietly never got picked.
func (h *RunTourCoordinatorHandler) logUnprovenCrossings(ctx context.Context, playerID, unproven, pairs, charge int) {
	if unproven == 0 {
		return
	}
	metrics.RecordTourCandidateDropped(playerID, unprovenCrossingReason, unproven)
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Tour hop matrix could not prove %d of %d system crossing(s) over stored gate topology; each unproven crossing is charged %d gate hops rather than the solver's 1-hop default",
		unproven, pairs, charge),
		map[string]interface{}{
			"action":            "tour_inter_system_crossing_unproven",
			"unproven":          unproven,
			"pairs":             pairs,
			"charged_gate_hops": charge,
		})
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
	// Resolved once, outside collect: the cap is a live read and collect runs per system.
	maxAge := h.listingMaxAge(ctx, playerID)
	collect := func(sys string) {
		rows, err := h.legs.collectSystemListings(ctx, sys, playerID)
		if err != nil {
			return // unreadable system contributes no lanes (fail-open)
		}
		for _, row := range freshListings(rows, h.clock.Now(), maxAge) {
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

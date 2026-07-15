package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// This file holds the sp-rjgr DEPTH slice: the breadth-vs-depth balance the frontier
// coordinator uses to escape pure BFS. Today the coordinator scores virgin systems and
// declares by score; every hop-1 virgin scores above every hop-2 (the hop penalty), and scout
// throughput (~5 posts) is smaller than the hop-1 ring width (12+), so ring 1 never drains and
// ring 2 never opens — 84 hop-1 declarations to exactly 1 hop-2 EVER, so a probe never reaches
// the depth a heavy-freighter yard sits at. Pure BFS cannot self-correct into depth; it must be
// TOLD to. The depth slice reserves a tunable fraction of frontier capacity for PATHFINDERS that
// ignore the near-ring score and declare the DEEPEST-reachable virgin along DISTINCT corridors,
// bounded by a max-depth cap and biased further outward while a deep-resource objective is unmet.
//
// It reuses the existing declaration machinery wholesale: a depth post is an ordinary sweep-once
// post the reconciler mans and relays exactly like a breadth post (declareSweepOncePost). Only
// the SELECTION differs — which is why this policy is a self-contained add-on to ReconcileOnce
// and never touches the probe-buy path.

// DepthObjectiveReader surfaces the deep-resource objective the depth slice chases (sp-rjgr §4):
// whether the fleet needs heavy-freight capacity it cannot yet buy because no heavy-freighter
// yard has been discovered. When the objective is UNMET the split biases toward depth (punch
// outward to find the yard); once a yard is known — or there is no shortfall — it relaxes back to
// the baseline split. It is a driven port: the adapter combines the autosizer heavy-shortfall
// signal (sp-4ewi) with the shipyard-inventory yard-known predicate (sp-42ow). Optional-injection;
// a nil or unreadable reader applies NO bias (fail-safe — this shifts a policy split, never a
// spend decision, so an unreadable signal simply leaves the baseline split standing).
type DepthObjectiveReader interface {
	// HeavyYardObjective returns the objective state: heavyShortfall is the autosizer's heavy
	// capacity shortfall (>0 ⇒ we need more heavies), heavyYardKnown is whether ANY heavy-freighter
	// yard has been discovered this era. readable=false ⇒ the signal is unreadable; the caller
	// applies no bias.
	HeavyYardObjective(ctx context.Context, playerID int) (heavyShortfall int, heavyYardKnown bool, readable bool, err error)
}

// SetDepthObjectiveReader wires the optional deep-resource objective signal for the depth bias
// (sp-rjgr §4). Leaving it unset keeps the split on its baseline fraction with no objective shift.
func (h *RunFrontierExpansionCoordinatorHandler) SetDepthObjectiveReader(o DepthObjectiveReader) {
	h.objective = o
}

// dispatchDepthPathfinders declares this cycle's depth pathfinders and returns how many were
// declared (added to the cycle's manning demand by the caller). It reserves a slice of frontier
// capacity for the deepest-reachable virgins along distinct corridors, additive to the breadth
// head and bounded by the SAME in-flight cap. A 0% depth split (breadth 100, no objective bias)
// short-circuits before any scan — pure BFS, exactly as before sp-rjgr.
func (h *RunFrontierExpansionCoordinatorHandler) dispatchDepthPathfinders(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	posts []*domainScouting.ScoutPost,
	breadthDeclared string,
	inFlightCount int,
) int {
	if h.scanner == nil {
		return 0
	}
	quota := depthQuota(h.effectiveDepthPercent(ctx, cmd, cfg), cfg.MaxFrontierPostsInFlight, cfg.MaxDepthPathfinders)
	if quota <= 0 {
		return 0 // the split reserves nothing for depth this cycle — pure BFS
	}
	candidates, err := h.scanner.ExpansionCandidates(ctx, cmd.PlayerID.Value(), cfg.MaxDepthHops)
	if err != nil || len(candidates) == 0 {
		return 0
	}
	depthInFlight, claimedBranches := inFlightDepthBranches(posts, indexCandidates(candidates))
	slots := depthSlotsThisCycle(cfg, quota, depthInFlight, inFlightCount, breadthDeclared)
	if slots <= 0 {
		return 0 // the depth slice is already at quota, or the in-flight cap leaves no room
	}
	targets := selectDepthTargets(candidates, coveredWithBreadth(posts, breadthDeclared), claimedBranches, slots, cfg.MaxDepthHops)
	return h.declareDepthTargets(ctx, cmd, cfg, targets)
}

// effectiveDepthPercent is this cycle's depth share: the baseline (100 - breadth) shifted toward
// depth by the objective bias while the heavy-yard objective is unmet, clamped to [0, 100].
func (h *RunFrontierExpansionCoordinatorHandler) effectiveDepthPercent(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand, cfg frontierConfig) int {
	depthPercent := 100 - cfg.BreadthFractionPercent
	if depthPercent < 0 {
		depthPercent = 0
	}
	depthPercent += h.objectiveDepthBias(ctx, cmd, cfg)
	if depthPercent > 100 {
		depthPercent = 100
	}
	return depthPercent
}

// objectiveDepthBias returns the extra depth percentage points the deep-resource objective asks
// for: the configured aggressiveness while heavy capacity is short AND no heavy yard is known, 0
// otherwise. A missing or unreadable reader biases nothing (fail-safe).
func (h *RunFrontierExpansionCoordinatorHandler) objectiveDepthBias(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand, cfg frontierConfig) int {
	if h.objective == nil {
		return 0
	}
	shortfall, yardKnown, readable, err := h.objective.HeavyYardObjective(ctx, cmd.PlayerID.Value())
	if err != nil || !readable {
		return 0
	}
	if shortfall > 0 && !yardKnown {
		return cfg.ObjectiveBiasPercent
	}
	return 0
}

// declareDepthTargets declares each selected system as a sweep-once post (the SAME seam breadth
// uses) and returns the count declared. In dry-run it logs the intent and counts it toward demand
// without writing, mirroring the breadth head's dry-run behavior.
func (h *RunFrontierExpansionCoordinatorHandler) declareDepthTargets(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand, cfg frontierConfig, targets []string) int {
	logger := common.LoggerFromContext(ctx)
	declared := 0
	for _, systemSymbol := range targets {
		if cmd.DryRun {
			logger.Log("INFO", fmt.Sprintf("DRY-RUN: would declare DEPTH pathfinder post %s (deepest-reachable virgin, distinct bearing)", systemSymbol), map[string]interface{}{
				"action":        "frontier_depth_declare_dryrun",
				"system_symbol": systemSymbol,
			})
			declared++
			continue
		}
		if err := h.declareSweepOncePost(ctx, cmd, cfg, systemSymbol); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to declare DEPTH pathfinder post %s: %v", systemSymbol, err), nil)
			continue
		}
		declared++
		logger.Log("INFO", fmt.Sprintf("Declared DEPTH pathfinder sweep-once post %s — punching outward to the deepest-reachable virgin; reconciler will relay a probe", systemSymbol), map[string]interface{}{
			"action":        "frontier_depth_pathfinder_declared",
			"system_symbol": systemSymbol,
		})
	}
	return declared
}

// depthQuota is how many concurrent depth pathfinders the split reserves: round(depthPercent% of
// the in-flight cap), floored to 1 whenever the slice is non-empty (a reserved slice always fields
// at least one pathfinder), and capped by the max-pathfinders knob. A 0% depth share reserves none.
func depthQuota(depthPercent, maxInFlight, maxPathfinders int) int {
	if depthPercent <= 0 {
		return 0
	}
	quota := (depthPercent*maxInFlight + 50) / 100 // round to nearest
	if quota < 1 {
		quota = 1
	}
	if quota > maxPathfinders {
		quota = maxPathfinders
	}
	return quota
}

// depthSlotsThisCycle is how many depth posts may be declared this tick: the quota not yet filled
// by in-flight depth posts, capped by the frontier in-flight budget the breadth head has not
// already consumed.
func depthSlotsThisCycle(cfg frontierConfig, quota, depthInFlight, inFlightCount int, breadthDeclared string) int {
	slots := quota - depthInFlight
	remaining := cfg.MaxFrontierPostsInFlight - inFlightCount
	if breadthDeclared != "" {
		remaining-- // the breadth head just consumed one in-flight slot
	}
	if remaining < slots {
		slots = remaining
	}
	return slots
}

// selectDepthTargets picks up to `slots` deepest-reachable virgin candidates on DISTINCT corridors
// — deepest first, one per branch, never a branch a claimed (in-flight) pathfinder already holds.
// This is the fan-out that stops the depth drive betting the whole outward push on one direction.
func selectDepthTargets(candidates []ExpansionCandidate, covered, claimedBranches map[string]bool, slots, maxDepthHops int) []string {
	eligible := eligibleDepthTargets(candidates, covered, maxDepthHops)
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].Hops != eligible[j].Hops {
			return eligible[i].Hops > eligible[j].Hops // deepest first — depth ignores the near-ring score
		}
		return eligible[i].SystemSymbol < eligible[j].SystemSymbol // deterministic tiebreak
	})
	claimed := copyBranchSet(claimedBranches)
	picked := make([]string, 0, slots)
	for _, candidate := range eligible {
		if len(picked) >= slots {
			break
		}
		if claimed[candidate.BranchRoot] {
			continue // this corridor already has a pathfinder — fan out, do not stack
		}
		picked = append(picked, candidate.SystemSymbol)
		claimed[candidate.BranchRoot] = true
	}
	return picked
}

// eligibleDepthTargets filters candidates to declarable depth targets: an uncovered depth-kind
// system (uncharted virgin, hops >= 2), within the max-depth cap, with a resolvable corridor.
func eligibleDepthTargets(candidates []ExpansionCandidate, covered map[string]bool, maxDepthHops int) []ExpansionCandidate {
	out := make([]ExpansionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if covered[candidate.SystemSymbol] {
			continue
		}
		if candidate.Hops > maxDepthHops {
			continue // the max-depth cap bounds the pathfinder
		}
		if !isDepthTarget(candidate) {
			continue
		}
		if candidate.BranchRoot == "" {
			continue // no resolvable corridor → cannot fan it distinctly
		}
		out = append(out, candidate)
	}
	return out
}

// isDepthTarget reports whether a candidate is a depth-pathfinder target: an UNCHARTED virgin at
// least one ring BEYOND the near ring (hops >= 2). Depth pushes the uncharted EDGE outward, so a
// charted-but-unscanned interior system (breadth's market-coverage job) and a hop-1 virgin (the
// near ring breadth already fills) are NOT depth targets. A swept-marketless system is never a
// target (defensive — a virgin is never scanned). The virgin+hop>=2 predicate is also the depth
// vs breadth classifier for in-flight posts, so the quota accounting matches what depth declares.
func isDepthTarget(c ExpansionCandidate) bool {
	if c.Charted {
		return false
	}
	if c.Hops < 2 {
		return false
	}
	if c.Scanned && c.KnownMarkets == 0 {
		return false
	}
	return true
}

// inFlightDepthBranches classifies the current sweep-once posts: how many are depth pathfinders
// (a virgin at hops >= 2, by the same isDepthTarget predicate depth declares on) and which
// corridors they hold. A post beyond the current depth horizon is a pathfinder that has already
// pushed out of view — counted toward the quota, its corridor unresolved.
func inFlightDepthBranches(posts []*domainScouting.ScoutPost, bySystem map[string]ExpansionCandidate) (int, map[string]bool) {
	count := 0
	branches := make(map[string]bool)
	for _, post := range posts {
		if post.Kind != domainScouting.PostKindSweepOnce {
			continue
		}
		candidate, known := bySystem[post.SystemSymbol]
		if !known {
			count++ // a pathfinder past the horizon — counts toward the quota, corridor out of view
			continue
		}
		if !isDepthTarget(candidate) {
			continue // a near-ring / charted breadth post, not a depth pathfinder
		}
		count++
		if candidate.BranchRoot != "" {
			branches[candidate.BranchRoot] = true
		}
	}
	return count, branches
}

// coveredWithBreadth is the set of systems depth must not re-declare: every posted system plus the
// breadth head just declared this cycle.
func coveredWithBreadth(posts []*domainScouting.ScoutPost, breadthDeclared string) map[string]bool {
	covered := postSystemSet(posts)
	if breadthDeclared != "" {
		covered[breadthDeclared] = true
	}
	return covered
}

// indexCandidates keys candidates by system symbol for the in-flight classifier.
func indexCandidates(candidates []ExpansionCandidate) map[string]ExpansionCandidate {
	bySystem := make(map[string]ExpansionCandidate, len(candidates))
	for _, candidate := range candidates {
		bySystem[candidate.SystemSymbol] = candidate
	}
	return bySystem
}

// copyBranchSet clones a claimed-branch set so selection can extend it without mutating the caller's.
func copyBranchSet(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for branch, held := range src {
		dst[branch] = held
	}
	return dst
}

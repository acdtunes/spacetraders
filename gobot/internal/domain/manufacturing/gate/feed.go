package gate

import (
	"fmt"
	"strings"
)

// DefaultFeedDepthCap is how deep the feed walk may descend below a gate material before it
// stops and says so. Root is depth 0, its direct inputs depth 1.
//
// 3 is deliberately the SAME number as services.defaultFabricateMaxDepth. It is not a coupling —
// the two caps bound different walks — but they bound the same recipe graph for the same reason,
// and a gate chain that the resolver would fabricate three levels down is exactly the chain this
// walk must be able to feed three levels down. Both gate materials bottom out on the curated raw
// predicate well inside it (FAB_MATS at depth 2, ADVANCED_CIRCUITRY at depth 3), so on live data
// the cap is a BACKSTOP that never fires — which is the whole point of keeping it.
//
// This is a tunable's default, not a feature flag: an unset (or negative) cap resolves here
// rather than disabling the guard. There is no unbounded mode.
const DefaultFeedDepthCap = 3

// Feed-walk stop reasons. Every good the walk declines to descend records one, because a walk
// that stops silently rebuilds the exact opacity this design exists to remove: a starved factory
// and a satisfied one would look identical.
const (
	// FeedStopRaw: the curated mineable-raw predicate bottomed the chain out. This is the
	// PRIMARY terminator and the one that should fire on live data.
	FeedStopRaw = "raw"
	// FeedStopAlreadyPlanned: the good is already on the walk. This is the CYCLE GUARD and the
	// diamond de-duplicator in one — the same mechanism, two effects. The step is still recorded
	// (this parent's factory really does need that input); only the traversal stops.
	FeedStopAlreadyPlanned = "already_planned"
	// FeedStopDepthCap: the backstop fired. On live data this should never appear; if it does,
	// either the recipe map changed or the curated raw list has fallen behind it.
	FeedStopDepthCap = "depth_cap"
)

// Recipe is the narrow view of the era's recipe graph the feed walk needs.
// *services.GateTopology satisfies it.
//
// IT MUST BE GateTopology.Inputs, NEVER goods.GetRequiredInputs. The two diverge in CONTENT, not
// merely in shape: since sp-4irrr, Inputs("IRON_ORE") is nil (the curated predicate calls it raw)
// while GetRequiredInputs("IRON_ORE") is still {"EXPLOSIVES"}. A walk built on the latter descends
// an ore into IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY -> IRON -> IRON_ORE and stops
// terminating. Declaring the seam here, consumer-side, and importing no goods package in this file,
// makes that substitution inexpressible IN THIS FILE — which is narrower than it first sounds and
// must not be mistaken for a repo-wide guarantee. Any application-layer adapter may still implement
// this interface over goods.GetRequiredInputs, and that is exactly where the hazard lives, because
// such a seam violates the biconditional below: it answers IsRaw("IRON_ORE") true while handing back
// {"EXPLOSIVES"} from Inputs. PlanFeed defends against precisely that by consulting IsRaw BEFORE it
// ever calls Inputs, so a leaked recipe is never enumerated — see
// TestPlanFeed_TerminatesAtTheCuratedRawEvenWhenTheSeamLeaksARawsRecipe, which is the executable
// form of this paragraph.
type Recipe interface {
	// IsRaw reports whether good must be bought rather than fabricated — the curated
	// mineable-raw predicate, plus "absent from the recipe map".
	IsRaw(good string) bool
	// Inputs returns what good's factory must be fed, or nil when good is raw.
	Inputs(good string) []string
}

// FeedStep is one input that must reach one factory: buy Input at whatever exports it, deliver it
// into the factory that exports Target.
//
// Target is the GOOD, not a waypoint. Locations are resolved at runtime by market role — waypoint
// numbering is regenerated every era, so a symbol at this layer is a bug that survives exactly
// until the next era rolls.
type FeedStep struct {
	Input  string
	Target string
	Depth  int
}

// FeedStop is one good the walk declined to descend, and why.
type FeedStop struct {
	Good   string
	Reason string
	Depth  int
}

// FeedPlan is the whole feeding requirement for one gate material.
type FeedPlan struct {
	Root  string
	Steps []FeedStep
	Stops []FeedStop
}

// LogLine renders the plan for the container log. Everything is in the MESSAGE: the container log
// renderer drops metadata maps, so a plan that reported itself only in metadata would be exactly
// as invisible as one that said nothing.
func (p FeedPlan) LogLine() string {
	steps := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		steps = append(steps, fmt.Sprintf("%s->%s(d%d)", s.Input, s.Target, s.Depth))
	}
	stops := make([]string, 0, len(p.Stops))
	for _, s := range p.Stops {
		stops = append(stops, fmt.Sprintf("%s: %s", s.Good, s.Reason))
	}

	stepText := "nothing"
	if len(steps) > 0 {
		stepText = strings.Join(steps, ", ")
	}
	if len(stops) == 0 {
		return fmt.Sprintf("Gate factory feed plan for %s: %d step(s) — %s", p.Root, len(p.Steps), stepText)
	}
	return fmt.Sprintf("Gate factory feed plan for %s: %d step(s) — %s; walk stopped at %s",
		p.Root, len(p.Steps), stepText, strings.Join(stops, ", "))
}

// PlanFeed walks the recipe graph below root and returns every input that must reach every
// factory in the chain, shallowest first.
//
// SHALLOWEST FIRST is a breadth-first walk on purpose. The terminal factory's own inputs are what
// it is actually starved of, so a hull that runs one step per trip works the binding constraint
// before anything deeper.
//
// TERMINATION IS THREE-FOLD AND NONE OF THE THREE IS REDUNDANT:
//
//  1. recipes.IsRaw — the CURATED mineable-raw predicate. It records the raw stop, it orders ahead
//     of the cap (see below), and it terminates a NON-CONFORMING seam. It is NOT what cuts the
//     IRON_ORE loop against a conforming one: a conforming Inputs returns nil for a raw
//     (the biconditional on Recipe), so the loop is unreachable through it whether this check runs
//     or not. Its termination role is real but conditional, which is why it is pinned by a
//     deliberately leaky double rather than by the live map alone.
//  2. visited — the cycle guard. The recipe map is game data and the curated list is
//     hand-maintained, so neither is a proof of acyclicity.
//  3. depthCap — the backstop. THE SPEC'S ORIGINAL "the DAG bottoms out, so delete the cap" WAS
//     FALSE AND IS STRUCK: goods.ExportToImportMap closes at least
//     IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY -> IRON -> IRON_ORE, and both gate
//     materials feed into it.
//
// Note that (2) and (3) each terminate ALONE, while (1) does not. That is deliberate: it is what
// lets each be broken individually in a mutation probe without hanging the test process, and it
// is exactly why neither may be deleted on the other's strength.
//
// THE RAW CHECK PRECEDES THE CAP CHECK, and the order is load-bearing rather than stylistic. On
// live data ADVANCED_CIRCUITRY reaches COPPER_ORE at exactly depth 3 — the cap — and COPPER_ORE is
// a curated raw. Testing the cap first would record a depth_cap stop for a chain that in fact
// bottomed out cleanly, i.e. it would report the backstop firing on a walk that terminated
// correctly, and an operator would go looking for a truncated chain that does not exist.
//
// VISITED BOUNDS THE WALK, NOT THE WORK. A good reached from two parents still yields one step per
// parent — MICROPROCESSORS' factory needs SILICON_CRYSTALS delivered even though ELECTRONICS'
// factory needs it too — but it is queued once.
//
// Total over degenerate input: a nil seam, an empty root and a non-positive cap all answer rather
// than panic. The drain calls this per material per leg, and an unwired collaborator must not take
// a tick down.
func PlanFeed(root string, recipes Recipe, depthCap int) FeedPlan {
	plan := FeedPlan{Root: root}
	if recipes == nil || root == "" {
		return plan
	}
	if depthCap <= 0 {
		depthCap = DefaultFeedDepthCap
	}

	type pending struct {
		good  string
		depth int
	}
	visited := map[string]bool{root: true}
	queue := []pending{{good: root, depth: 0}}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if recipes.IsRaw(current.good) {
			plan.Stops = append(plan.Stops, FeedStop{Good: current.good, Reason: FeedStopRaw, Depth: current.depth})
			continue
		}
		if current.depth >= depthCap {
			plan.Stops = append(plan.Stops, FeedStop{Good: current.good, Reason: FeedStopDepthCap, Depth: current.depth})
			continue
		}

		for _, input := range recipes.Inputs(current.good) {
			// The step is recorded even for a good already on the walk: THIS factory needs THIS
			// input regardless of who else does. Only the traversal below is de-duplicated.
			plan.Steps = append(plan.Steps, FeedStep{Input: input, Target: current.good, Depth: current.depth + 1})
			if visited[input] {
				plan.Stops = append(plan.Stops, FeedStop{Good: input, Reason: FeedStopAlreadyPlanned, Depth: current.depth + 1})
				continue
			}
			visited[input] = true
			queue = append(queue, pending{good: input, depth: current.depth + 1})
		}
	}
	return plan
}

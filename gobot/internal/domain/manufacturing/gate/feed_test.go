package gate

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// mapRecipe mirrors services.GateTopology's Inputs/IsRaw contract over an arbitrary recipe map,
// so the walk can be exercised against BOTH the real game data and synthetic pathologies without
// the domain package importing the services layer.
//
// It reproduces the post-sp-4irrr semantics exactly: IsRaw answers the CURATED mineable predicate
// OR "absent from the map", and Inputs returns nil for anything IsRaw calls raw. That
// biconditional is what the walk's termination rests on, so a fake that got it wrong would test a
// contract production does not have.
type mapRecipe struct {
	recipes  map[string][]string
	mineable map[string]bool
}

func (m mapRecipe) IsRaw(good string) bool {
	if m.mineable[good] {
		return true
	}
	inputs, ok := m.recipes[good]
	return !ok || len(inputs) == 0
}

func (m mapRecipe) Inputs(good string) []string {
	if m.IsRaw(good) {
		return nil
	}
	inputs := m.recipes[good]
	out := make([]string, len(inputs))
	copy(out, inputs)
	return out
}

// realRecipe is the LIVE game data behind the same seam: the exact map and the exact curated
// predicate GateTopology reads.
func realRecipe() mapRecipe {
	mineable := make(map[string]bool)
	for good := range goods.ExportToImportMap {
		if goods.IsMineableRawMaterial(good) {
			mineable[good] = true
		}
	}
	return mapRecipe{recipes: goods.ExportToImportMap, mineable: mineable}
}

// leakyRecipe is a DELIBERATELY NON-CONFORMING seam, and it is the only fixture here that is
// allowed to be: it answers the curated raw predicate correctly but returns a raw good's recipe
// anyway, breaking the IsRaw(g) <=> Inputs(g)==nil biconditional that GateTopology upholds.
//
// It is the goods.GetRequiredInputs SHAPE — the exact substitution this task exists to prevent, and
// the one an application-layer adapter can still make freely, since gate/feed.go's "imports no
// goods package" only constrains that file. Against a CONFORMING seam the walk's IsRaw check cannot
// be shown to terminate anything (Inputs returns nil for a raw either way, so the cycle is
// unreachable regardless); against this one it can, because reaching Inputs("IRON_ORE") at all
// hands back {"EXPLOSIVES"} and opens the sp-4irrr loop.
type leakyRecipe struct{}

func (leakyRecipe) IsRaw(good string) bool {
	return goods.IsMineableRawMaterial(good) || len(goods.ExportToImportMap[good]) == 0
}

func (leakyRecipe) Inputs(good string) []string {
	return goods.GetRequiredInputs(good)
}

func stepKeys(plan FeedPlan) []string {
	keys := make([]string, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		keys = append(keys, s.Input+"->"+s.Target)
	}
	return keys
}

func hasStop(plan FeedPlan, good, reason string) bool {
	for _, s := range plan.Stops {
		if s.Good == good && s.Reason == reason {
			return true
		}
	}
	return false
}

// THE REAL MAP TERMINATES, from both gate materials, and bottoms out on the CURATED raw
// predicate rather than on the cap. Every era's gate needs these two goods and the recipe graph
// is a game constant, so naming them directly is era-invariant (locations are not, and none
// appear here).
func TestPlanFeed_TerminatesOnRealGameDataFromBothGateMaterials(t *testing.T) {
	for _, root := range []string{"FAB_MATS", "ADVANCED_CIRCUITRY"} {
		plan := PlanFeed(root, realRecipe(), DefaultFeedDepthCap)

		if len(plan.Steps) == 0 {
			t.Fatalf("PlanFeed(%s) produced no feeding work; both gate materials are fabricated goods with inputs", root)
		}
		for _, stop := range plan.Stops {
			if stop.Reason == FeedStopDepthCap {
				t.Fatalf("PlanFeed(%s) hit the DEPTH CAP at %s (depth %d) — the curated raw predicate should bottom this chain out first; the cap is the backstop, not the terminator", root, stop.Good, stop.Depth)
			}
		}
	}

	// ...and the ABSENCE of a depth_cap stop is not enough on its own, because it is VACUOUSLY
	// SATISFIABLE. A chain that SHRANK produces no depth_cap stop either: if COPPER gained a
	// curated-raw entry, or lost its recipe entry, the walk would stop one level early, nothing
	// above would go red, and the fleet would silently stop feeding COPPER_ORE into COPPER's
	// factory. ADVANCED_CIRCUITRY is the deepest live chain and the one sitting at exactly the cap,
	// so pin its deepest edge POSITIVELY: the chain reached full live depth AND bottomed out on the
	// curated predicate there.
	//
	// This stays true under a future cap RAISE (it asserts presence, not a depth number), which is
	// the property that lets the cap constant move without rewriting the test.
	//
	// FAB_MATS needs no equivalent here: its deepest edge (IRON_ORE->IRON) is already pinned by
	// TestPlanFeed_FabMatsFeedsIronAndQuartzThenStopsAtTheOre.
	deepest := PlanFeed("ADVANCED_CIRCUITRY", realRecipe(), DefaultFeedDepthCap)
	if !strings.Contains(strings.Join(stepKeys(deepest), " "), "COPPER_ORE->COPPER") {
		t.Fatalf("steps %v are missing COPPER_ORE->COPPER — the deepest live edge of the gate's deepest chain; the walk stopped short and no depth_cap stop said so", stepKeys(deepest))
	}
	if !hasStop(deepest, "COPPER_ORE", FeedStopRaw) {
		t.Fatalf("stops = %+v, want COPPER_ORE to bottom out on the CURATED RAW predicate — that is what makes the cap a backstop here rather than the terminator", deepest.Stops)
	}
}

// FAB_MATS = {IRON, QUARTZ_SAND}; IRON = {IRON_ORE}; QUARTZ_SAND and IRON_ORE are curated
// mineable raws. The walk must feed the FAB_MATS factory with both of its inputs and feed IRON's
// factory with the ore — and must STOP at both raws.
func TestPlanFeed_FabMatsFeedsIronAndQuartzThenStopsAtTheOre(t *testing.T) {
	plan := PlanFeed("FAB_MATS", realRecipe(), DefaultFeedDepthCap)

	for _, want := range []string{"IRON->FAB_MATS", "QUARTZ_SAND->FAB_MATS", "IRON_ORE->IRON"} {
		if !strings.Contains(strings.Join(stepKeys(plan), " "), want) {
			t.Fatalf("PlanFeed(FAB_MATS) steps %v are missing %q", stepKeys(plan), want)
		}
	}
	for _, raw := range []string{"QUARTZ_SAND", "IRON_ORE"} {
		if !hasStop(plan, raw, FeedStopRaw) {
			t.Fatalf("PlanFeed(FAB_MATS) did not stop at the curated raw %s; stops = %+v", raw, plan.Stops)
		}
	}
	// IRON_ORE's own recipe entry is {EXPLOSIVES}. Descending it is the sp-4irrr cycle
	// (IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY -> IRON -> IRON_ORE).
	for _, key := range stepKeys(plan) {
		if strings.HasPrefix(key, "EXPLOSIVES->") {
			t.Fatalf("the walk descended an ore into EXPLOSIVES (%q) — that is the cyclic branch, and Inputs() returns nil for a curated raw precisely so it cannot be entered", key)
		}
	}
}

// THE RAW PREDICATE AS A GENUINE TERMINATOR. Against a conforming seam this cannot be demonstrated
// at all: Inputs returns nil for a raw, so a raw's children are unreachable whether the walk checks
// IsRaw or not, and the check is then only a diagnostic (it records the stop and orders ahead of the
// cap). Its termination role is real exactly against a seam that LEAKS a raw's recipe — the
// GetRequiredInputs shape — and that is what this pins: the walk consults IsRaw BEFORE Inputs, so a
// leaked recipe is never enumerated and the ore never opens the cycle.
func TestPlanFeed_TerminatesAtTheCuratedRawEvenWhenTheSeamLeaksARawsRecipe(t *testing.T) {
	seam := leakyRecipe{}

	// CALIBRATE THE FIXTURE FIRST. If the double is not actually non-conforming where it matters,
	// every assertion below passes vacuously against a seam that behaves like GateTopology after
	// all — and this test would silently stop covering the only case it exists for.
	if !seam.IsRaw("IRON_ORE") {
		t.Fatalf("fixture is not exercising the hazard: the curated predicate no longer calls IRON_ORE raw")
	}
	if leaked := seam.Inputs("IRON_ORE"); len(leaked) == 0 {
		t.Fatalf("fixture is not exercising the hazard: a LEAKY seam must still hand back a raw's recipe, got %v", leaked)
	}

	plan := PlanFeed("FAB_MATS", seam, DefaultFeedDepthCap)

	if !hasStop(plan, "IRON_ORE", FeedStopRaw) {
		t.Fatalf("stops = %+v, want IRON_ORE stopped on the curated raw predicate even though this seam offers it a recipe", plan.Stops)
	}
	for _, key := range stepKeys(plan) {
		if strings.HasPrefix(key, "EXPLOSIVES->") {
			t.Fatalf("the walk enumerated a leaked raw recipe and planned %q — IsRaw must be consulted BEFORE Inputs, or an ore opens IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY -> IRON -> IRON_ORE; steps = %v", key, stepKeys(plan))
		}
	}
	// It bottomed out on the RAW predicate, not on the backstop: that is the difference between
	// "terminated" and "was cut off", and only the former is correct here.
	for _, stop := range plan.Stops {
		if stop.Reason == FeedStopDepthCap {
			t.Fatalf("the leaky seam was stopped by the DEPTH CAP at %s (depth %d) rather than by the curated raw predicate; stops = %+v", stop.Good, stop.Depth, plan.Stops)
		}
	}
}

// SHALLOWEST FIRST. The terminal factory's OWN inputs are what it is starved of, so they must be
// planned ahead of anything deeper: a hull that runs one step per trip works the binding
// constraint first.
func TestPlanFeed_OrdersStepsShallowestFirst(t *testing.T) {
	plan := PlanFeed("ADVANCED_CIRCUITRY", realRecipe(), DefaultFeedDepthCap)

	last := 0
	for _, step := range plan.Steps {
		if step.Depth < last {
			t.Fatalf("steps are not depth-ordered: %+v came after depth %d", step, last)
		}
		last = step.Depth
	}
	// Fail cleanly rather than panicking on the index below: a walk that planned nothing is a real
	// failure mode of this function, and a panic reports it as a broken test instead of a broken walk.
	if len(plan.Steps) == 0 {
		t.Fatal("PlanFeed(ADVANCED_CIRCUITRY) planned no steps at all; there is no first step to order")
	}
	if plan.Steps[0].Target != "ADVANCED_CIRCUITRY" {
		t.Fatalf("first step targets %s; the terminal factory's own inputs must be planned first", plan.Steps[0].Target)
	}
}

// A good reached from TWO parents yields a step per parent — MICROPROCESSORS' factory needs
// SILICON_CRYSTALS even though ELECTRONICS' factory needs it too. visited bounds the WALK, not
// the WORK, and conflating the two would silently drop real feeding.
func TestPlanFeed_ASharedInputIsFedToEveryFactoryThatNeedsIt(t *testing.T) {
	plan := PlanFeed("ADVANCED_CIRCUITRY", realRecipe(), DefaultFeedDepthCap)

	keys := strings.Join(stepKeys(plan), " ")
	for _, want := range []string{"SILICON_CRYSTALS->ELECTRONICS", "SILICON_CRYSTALS->MICROPROCESSORS"} {
		if !strings.Contains(keys, want) {
			t.Fatalf("steps %v are missing %q — visited must bound traversal, not the work list", stepKeys(plan), want)
		}
	}
}

// CYCLE GUARD, falsifiable without a hang. The cap is still in force, so a walk that lost
// `visited` terminates and produces DUPLICATE steps rather than spinning — which is what makes
// this assertable at all.
func TestPlanFeed_ACyclicRecipeProducesNoDuplicateSteps(t *testing.T) {
	cyclic := mapRecipe{recipes: map[string][]string{
		"ALPHA": {"BETA"},
		"BETA":  {"ALPHA"},
	}}

	plan := PlanFeed("ALPHA", cyclic, DefaultFeedDepthCap)

	seen := map[string]bool{}
	for _, key := range stepKeys(plan) {
		if seen[key] {
			t.Fatalf("PlanFeed walked the ALPHA<->BETA cycle and planned %q twice; steps = %v", key, stepKeys(plan))
		}
		seen[key] = true
	}
	if !hasStop(plan, "ALPHA", FeedStopAlreadyPlanned) {
		t.Fatalf("the cycle closed back on ALPHA but no already_planned stop was recorded; stops = %+v — an operator cannot tell a cut cycle from an empty chain", plan.Stops)
	}
}

// DEPTH CAP, falsifiable without a hang. A deep ACYCLIC chain of non-raw goods: `visited` never
// fires here, so only the cap can stop it.
func TestPlanFeed_ADeepAcyclicChainStopsAtTheDepthCap(t *testing.T) {
	deep := mapRecipe{recipes: map[string][]string{
		"L0": {"L1"}, "L1": {"L2"}, "L2": {"L3"}, "L3": {"L4"}, "L4": {"L5"},
	}}

	plan := PlanFeed("L0", deep, 3)

	for _, step := range plan.Steps {
		if step.Depth > 3 {
			t.Fatalf("step %+v is at depth %d, past the cap of 3 — the cap is the backstop the cyclic recipe map makes non-optional", step, step.Depth)
		}
	}
	if !hasStop(plan, "L3", FeedStopDepthCap) {
		t.Fatalf("no depth_cap stop was recorded at L3; stops = %+v — a truncated chain must say it was truncated, or a starved factory looks like a satisfied one", plan.Stops)
	}
}

// An unset cap resolves to the armed default. There is no off state: a 0 is an unset knob, never
// a disabled guard, and a NEGATIVE cap must not disable it either.
func TestPlanFeed_UnsetOrNegativeCapResolvesToTheArmedDefault(t *testing.T) {
	deep := mapRecipe{recipes: map[string][]string{
		"L0": {"L1"}, "L1": {"L2"}, "L2": {"L3"}, "L3": {"L4"}, "L4": {"L5"},
	}}

	for _, requestedCap := range []int{0, -1} {
		plan := PlanFeed("L0", deep, requestedCap)
		for _, step := range plan.Steps {
			if step.Depth > DefaultFeedDepthCap {
				t.Fatalf("cap %d: step %+v exceeded the default cap %d — an unset cap must never mean unbounded", requestedCap, step, DefaultFeedDepthCap)
			}
		}
	}
}

// A raw ROOT has no feeding work, and says so. Returning an empty plan silently would be
// indistinguishable from a walk that failed.
func TestPlanFeed_ARawRootPlansNothingAndRecordsWhy(t *testing.T) {
	plan := PlanFeed("IRON_ORE", realRecipe(), DefaultFeedDepthCap)

	if len(plan.Steps) != 0 {
		t.Fatalf("PlanFeed(IRON_ORE) planned %v; a curated raw material is bought, never fed", stepKeys(plan))
	}
	if !hasStop(plan, "IRON_ORE", FeedStopRaw) {
		t.Fatalf("stops = %+v, want a raw stop for the root", plan.Stops)
	}
}

// Degenerate inputs are answered, not panicked on: the drain calls this per material per leg and
// a nil seam (an unwired collaborator) must not take the tick down.
func TestPlanFeed_IsTotalOverDegenerateInput(t *testing.T) {
	if plan := PlanFeed("", realRecipe(), DefaultFeedDepthCap); len(plan.Steps) != 0 {
		t.Fatalf("PlanFeed(\"\") planned %v", stepKeys(plan))
	}
	if plan := PlanFeed("FAB_MATS", nil, DefaultFeedDepthCap); len(plan.Steps) != 0 {
		t.Fatalf("PlanFeed with a nil Recipe planned %v", stepKeys(plan))
	}
}

// OBSERVABILITY. A feed plan must be diagnosable from the log alone: what it is feeding, how many
// steps, and where the walk stopped WITH the reason — all in the MESSAGE, because the container
// log renderer drops metadata maps.
func TestFeedPlan_LogLineNamesTheRootTheStepsAndEveryStopReason(t *testing.T) {
	line := PlanFeed("FAB_MATS", realRecipe(), DefaultFeedDepthCap).LogLine()

	// "IRON->FAB_MATS" rather than a bare "IRON": the latter is satisfied as a SUBSTRING of
	// "IRON_ORE", so it would pass on a log line that never named the IRON step at all.
	for _, want := range []string{"FAB_MATS", "IRON->FAB_MATS", "QUARTZ_SAND", FeedStopRaw} {
		if !strings.Contains(line, want) {
			t.Fatalf("feed plan log line %q does not name %q", line, want)
		}
	}

	// EVERY stop reason, which this test is named for and FAB_MATS alone cannot deliver — it only
	// ever produces `raw`. ADVANCED_CIRCUITRY adds already_planned (SILICON_CRYSTALS and COPPER are
	// each reached from two parents) and the deep synthetic chain adds depth_cap. An unrendered
	// reason is an operator staring at a walk that stopped for a cause the log never names.
	circuitry := PlanFeed("ADVANCED_CIRCUITRY", realRecipe(), DefaultFeedDepthCap).LogLine()
	for _, want := range []string{FeedStopRaw, FeedStopAlreadyPlanned} {
		if !strings.Contains(circuitry, want) {
			t.Fatalf("feed plan log line %q does not name stop reason %q", circuitry, want)
		}
	}
	truncated := PlanFeed("L0", mapRecipe{recipes: map[string][]string{
		"L0": {"L1"}, "L1": {"L2"}, "L2": {"L3"}, "L3": {"L4"}, "L4": {"L5"},
	}}, 3).LogLine()
	if !strings.Contains(truncated, FeedStopDepthCap) {
		t.Fatalf("feed plan log line %q does not name stop reason %q; a truncated chain that does not say so reads as a satisfied one", truncated, FeedStopDepthCap)
	}

	empty := PlanFeed("IRON_ORE", realRecipe(), DefaultFeedDepthCap).LogLine()
	if empty == "" {
		t.Fatal("an empty feed plan produced no log line; a factory fleet with nothing to feed and one that failed to plan must not look identical")
	}
}

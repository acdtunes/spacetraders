package services

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// These tests run against the REAL goods.ExportToImportMap, never a fixture. That is not a
// stylistic preference — the defect this file pins is a false claim ABOUT THAT DATA,
// so a synthetic recipe map would prove nothing. Any fixture an author writes by hand to
// "represent" the recipe graph will be acyclic, because acyclic is what people draw; the real
// map is not, and the old IsRaw terminated only on maps that were.

// TestExportToImportMap_ContainsTheCycleThatIsRawMustSurvive pins the premise the walk test
// below depends on. It is the rig calibration: if the game data ever loses this cycle, the
// termination test stops proving what its name claims (it would then be walking a DAG, which
// terminates under any rawness rule) and someone must revisit the doc comment on IsRaw rather
// than assume it is still true.
//
// The cycle is the one named in run_construction_coordinator_budget.go's staticSupplyChainDepth,
// which has documented it correctly all along.
func TestExportToImportMap_ContainsTheCycleThatIsRawMustSurvive(t *testing.T) {
	cycle := []string{"IRON_ORE", "EXPLOSIVES", "LIQUID_HYDROGEN", "MACHINERY", "IRON", "IRON_ORE"}

	for i := 0; i+1 < len(cycle); i++ {
		from, to := cycle[i], cycle[i+1]
		inputs, ok := goods.ExportToImportMap[from]
		if !ok {
			t.Fatalf("recipe map has no entry for %q, so the cycle %v is broken at step %d", from, cycle, i)
		}
		if !containsGood(inputs, to) {
			t.Fatalf("recipe map: %q inputs are %v, expected to contain %q to close the cycle %v", from, inputs, to, cycle)
		}
	}
}

// TestGateTopology_IsRawTreatsCuratedMineableMaterialsAsRaw covers the correction itself.
//
// "Has no recipe" was never the same predicate as "is raw". Every ore and crystal in the game
// HAS a recipe entry (they are all {EXPLOSIVES}), so the old !hasRecipe rule called none of them
// raw — including IRON_ORE, the literal good that stranded a hauler at 80/80 in sp-b27a2. The
// curated goods.IsMineableRawMaterial list is the domain's already-correct answer.
//
// The second half of the rule is kept and asserted here too: a good absent from the map entirely
// is still raw. Deleting it in favour of the curated list alone would make every unknown good
// fabricable, which is the opposite failure.
//
// Each case also asserts the biconditional the later recursion depends on: IsRaw(g) is true
// exactly when Inputs(g) is nil. Widening IsRaw necessarily changes what Inputs returns for the
// newly-raw goods (Inputs("IRON_ORE") is now nil, not {"EXPLOSIVES"}) — that is the intended
// effect, and the pairing must survive it.
func TestGateTopology_IsRawTreatsCuratedMineableMaterialsAsRaw(t *testing.T) {
	topo := NewGateTopology(nil, goods.ExportToImportMap)

	cases := []struct {
		good   string
		isRaw  bool
		reason string
	}{
		{"IRON_ORE", true, "curated mineable raw material, despite its {EXPLOSIVES} recipe"},
		{"SILICON_CRYSTALS", true, "curated mineable raw material, despite its {EXPLOSIVES} recipe"},
		{"QUARTZ_SAND", true, "curated mineable raw material, despite its {EXPLOSIVES} recipe"},
		{"COPPER_ORE", true, "curated mineable raw material, despite its {EXPLOSIVES} recipe"},
		{"NOT_A_REAL_GOOD", true, "absent from the recipe map entirely"},
		{"IRON", false, "fabricable from IRON_ORE and not curated as mineable"},
		{"FAB_MATS", false, "a gate material — the root of a fabrication chain"},
		{"ADVANCED_CIRCUITRY", false, "a gate material — the root of a fabrication chain"},
		{"ELECTRONICS", false, "a fabricable intermediate"},
		{"LIQUID_HYDROGEN", false, "deliberately NOT curated as mineable: it needs MACHINERY"},
	}

	for _, tc := range cases {
		if got := topo.IsRaw(tc.good); got != tc.isRaw {
			t.Errorf("IsRaw(%q) = %v, want %v — %s", tc.good, got, tc.isRaw, tc.reason)
		}
		if got := topo.Inputs(tc.good); (got == nil) != tc.isRaw {
			t.Errorf("Inputs(%q) = %v, want nil==%v to match IsRaw — %s", tc.good, got, tc.isRaw, tc.reason)
		}
	}
}

// TestGateTopology_RecipeWalkOverTheRealMapTerminates is the termination proof.
//
// It descends the REAL recipe graph from each gate material using only IsRaw/Inputs and NO
// visited set. Omitting cycle detection is deliberate and is the whole point: it makes
// termination a property of IsRaw alone. With the old !hasRecipe rule this walk runs forever
// (FAB_MATS -> IRON -> IRON_ORE -> EXPLOSIVES -> LIQUID_* -> MACHINERY -> IRON -> ...), and the
// step budget converts that into a named failure instead of a hung test process.
//
// This does NOT license deleting the fabricate depth cap. Production walks must still carry
// cycle detection and the cap: this test proves the two gate chains terminate on today's data,
// not that the graph is acyclic — it is not, as the test above pins.
func TestGateTopology_RecipeWalkOverTheRealMapTerminates(t *testing.T) {
	const stepBudget = 1000 // both real chains settle in ~10 steps; a runaway blows this at once

	topo := NewGateTopology(nil, goods.ExportToImportMap)

	cases := []struct {
		gateMaterial string
		wantTerminal []string
		wantExpanded []string
	}{
		{
			gateMaterial: "FAB_MATS",
			wantTerminal: []string{"IRON_ORE", "QUARTZ_SAND"},
			wantExpanded: []string{"FAB_MATS", "IRON"},
		},
		{
			gateMaterial: "ADVANCED_CIRCUITRY",
			wantTerminal: []string{"SILICON_CRYSTALS", "COPPER_ORE"},
			wantExpanded: []string{"ADVANCED_CIRCUITRY", "ELECTRONICS", "MICROPROCESSORS", "COPPER"},
		},
	}

	for _, tc := range cases {
		outcome, terminated := walkRecipeGraph(topo, tc.gateMaterial, stepBudget)
		if !terminated {
			t.Fatalf("walk from %s did not terminate within %d steps — IsRaw is not bounding the recursion; "+
				"expanded %d goods including %v", tc.gateMaterial, stepBudget, len(outcome.expanded), outcome.expanded)
		}

		for _, raw := range tc.wantTerminal {
			if !outcome.terminal[raw] {
				t.Errorf("walk from %s: %s was never reached as a terminal", tc.gateMaterial, raw)
			}
			if outcome.expanded[raw] {
				t.Errorf("walk from %s: %s was EXPANDED into its recipe instead of stopping there — "+
					"it is a mineable raw material and must be bought, not fabricated", tc.gateMaterial, raw)
			}
		}
		for _, node := range tc.wantExpanded {
			if !outcome.expanded[node] {
				t.Errorf("walk from %s: %s was not expanded, so the walk stopped short of the chain it must cover",
					tc.gateMaterial, node)
			}
		}

		// EXPLOSIVES is the tell. It is reachable ONLY by expanding an ore or crystal, which is
		// exactly the mistake !hasRecipe made; no gate chain legitimately passes through it.
		if outcome.expanded["EXPLOSIVES"] || outcome.terminal["EXPLOSIVES"] {
			t.Errorf("walk from %s descended into EXPLOSIVES — that only happens by expanding a "+
				"mineable raw material, and it is the entry point to the recipe cycle", tc.gateMaterial)
		}
	}
}

// recipeWalkOutcome separates the two ways a walk can encounter a good, because "stopped at
// IRON_ORE" and "never reached IRON_ORE" are different results that a single visited set would
// conflate — and only the first one is the behaviour under test.
type recipeWalkOutcome struct {
	expanded map[string]bool // IsRaw said no: the good's inputs were pushed
	terminal map[string]bool // IsRaw said yes: the walk stopped here
	steps    int
}

// walkRecipeGraph descends from root using only the GateTopology seam, with no visited set and a
// hard step budget. Returns false when the budget is exhausted, which for this graph means the
// walk is not terminating rather than merely being slow.
func walkRecipeGraph(topo *GateTopology, root string, stepBudget int) (recipeWalkOutcome, bool) {
	outcome := recipeWalkOutcome{expanded: map[string]bool{}, terminal: map[string]bool{}}
	stack := []string{root}

	for len(stack) > 0 {
		outcome.steps++
		if outcome.steps > stepBudget {
			return outcome, false
		}
		good := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if topo.IsRaw(good) {
			outcome.terminal[good] = true
			continue
		}
		outcome.expanded[good] = true
		stack = append(stack, topo.Inputs(good)...)
	}
	return outcome, true
}

func containsGood(goodsList []string, want string) bool {
	for _, good := range goodsList {
		if good == want {
			return true
		}
	}
	return false
}

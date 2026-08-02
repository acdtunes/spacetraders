package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// TestLegPlanBasis pins which plan basis each leg's price-drift observation is attributed to.
//
// This label is the difference between a drift figure that tests the market model and one
// that does not (sp-fpgl2). A solver leg's ExpectedUnitPrice is the planner's own projection.
// A look-back leg's is the manifest's CACHED SourceAsk, and the buy is gated to a tolerance
// band around that very number, so a fresh cache largely reproduces itself: look-back legs
// measured a median absolute error of EXACTLY 0.000% over 1423 production rows against the
// solver's 0.518%. Averaged into one series they read as a number describing neither, which
// is how the sp-1ek0 graduation gate came to report 0.309% for a model that actually drifts
// 0.518%. Misattributing a leg here silently re-merges the two populations.
// It is now keyed on the engine the executing path DECLARES rather than on the leg index it
// happens to pass, so the basis label and the row's engine column cannot disagree.
func TestLegPlanBasis(t *testing.T) {
	// The look-back manifest buy is the ONLY engine carrying a cached ask.
	if got := legPlanBasis(trading.LegEngineLookback); got != metrics.PlanBasisLookback {
		t.Errorf("legPlanBasis(lookback) = %q, want %q — a cached-ask leg counted as solver "+
			"re-merges the two plan bases and re-creates the diluted gate figure", got, metrics.PlanBasisLookback)
	}

	// The solver engine is the one that tests the market model.
	if got := legPlanBasis(trading.LegEngineSolver); got != metrics.PlanBasisSolver {
		t.Errorf("legPlanBasis(solver) = %q, want %q — a solver leg is the one that tests the market model",
			got, metrics.PlanBasisSolver)
	}

	// A liquidation now reports its OWN basis rather than borrowing the solver's. The prior
	// version of this test pinned it to solver and said so only because a liquidation carries
	// no basis, asking explicitly that a future change "force a decision about which
	// population it belongs to rather than letting it drift into solver unnoticed" — this is
	// that decision. Nothing observable moves: a non-positive basis returns before any counter
	// is touched, so the label never materialises. What changes is that the emitter can no
	// longer file a non-solver leg under solver.
	if got := legPlanBasis(trading.LegEngineLiquidation); got != metrics.PlanBasisLiquidation {
		t.Errorf("legPlanBasis(liquidation) = %q, want %q — a leg with no plan behind it must not "+
			"be labelled as solver evidence", got, metrics.PlanBasisLiquidation)
	}

	// An engine nobody taught this function about falls back to solver, which is the only
	// safe-looking wrong answer available — so the fallback is pinned to make it visible
	// rather than left as an accident of the switch's default arm.
	if got := legPlanBasis(trading.LegEngine("some-future-engine")); got != metrics.PlanBasisSolver {
		t.Errorf("legPlanBasis(unknown) = %q, want %q (documented fallback)", got, metrics.PlanBasisSolver)
	}
}

// TestLegPlanBasisMatchesLegIndexClass ties the DECLARED engine back to the leg-index sentinel
// each path stamps, so the two classifications cannot silently diverge.
//
// This is the cross-check that makes explicit stamping safe. The engine column is authoritative
// and the index stays the visualizer's ordering contract; if a call site ever declared one
// engine while stamping another's sentinel, the row would sort as one kind of leg and be
// attributed as another, and nothing else in the suite would notice.
func TestLegPlanBasisMatchesLegIndexClass(t *testing.T) {
	cases := []struct {
		name   string
		legIdx int
		engine trading.LegEngine
	}{
		{"lookback sentinel", lookbackLegIndex, trading.LegEngineLookback},
		{"first plan leg", 0, trading.LegEngineSolver},
		{"later plan leg", 7, trading.LegEngineSolver},
		{"liquidation base", liquidationLegIndexBase, trading.LegEngineLiquidation},
		{"liquidation second sink", liquidationLegIndexBase + 1, trading.LegEngineLiquidation},
		{"just below liquidation base", liquidationLegIndexBase - 1, trading.LegEngineSolver},
	}
	for _, tc := range cases {
		if got := trading.EngineForLegIndex(tc.legIdx); got != tc.engine {
			t.Errorf("%s: EngineForLegIndex(%d) = %q, want %q", tc.name, tc.legIdx, got, tc.engine)
		}
		if got, want := legPlanBasis(tc.engine), legPlanBasis(trading.EngineForLegIndex(tc.legIdx)); got != want {
			t.Errorf("%s: basis from declared engine (%q) disagrees with basis from leg index (%q)", tc.name, got, want)
		}
	}
}

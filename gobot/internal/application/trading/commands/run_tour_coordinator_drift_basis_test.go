package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
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
func TestLegPlanBasis(t *testing.T) {
	// The synthetic look-back manifest leg is the ONLY one carrying a cached ask, and
	// lookbackLegIndex is already how that leg is marked everywhere else.
	if got := legPlanBasis(lookbackLegIndex); got != metrics.PlanBasisLookback {
		t.Errorf("legPlanBasis(lookbackLegIndex) = %q, want %q — a cached-ask leg counted as solver "+
			"re-merges the two plan bases and re-creates the diluted gate figure", got, metrics.PlanBasisLookback)
	}

	// Every solver-planned position, including the first. Leg 0 matters on its own: an
	// off-by-one that tested legIdx <= 0 would misfile the tour's opening buy.
	for _, legIdx := range []int{0, 1, 2, 7} {
		if got := legPlanBasis(legIdx); got != metrics.PlanBasisSolver {
			t.Errorf("legPlanBasis(%d) = %q, want %q — a solver leg is the one that tests the market model",
				legIdx, got, metrics.PlanBasisSolver)
		}
	}

	// A distress liquidation sorts at or above liquidationLegIndexBase and classifies as
	// solver, which is harmless ONLY because it deliberately carries no plan basis: a
	// non-positive basis is skipped before any drift series moves. Pinned so that if a
	// liquidation ever gains a real basis, this assertion is the thing that forces a
	// decision about which population it belongs to rather than letting it drift into
	// solver unnoticed.
	if got := legPlanBasis(liquidationLegIndexBase); got != metrics.PlanBasisSolver {
		t.Errorf("legPlanBasis(liquidationLegIndexBase) = %q, want %q", got, metrics.PlanBasisSolver)
	}
}

package absorption_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
)

// The sink-depth crush prior. Every path that cannot positively confirm a sink's
// breadth must return the uniform prior (1.0), because the uniform prior is the
// conservative one: it charges a sale the full claim on the sink's depth.
func TestSinkDepthScaling_UnconfirmedBreadthKeepsTheUniformPrior(t *testing.T) {
	armed := absorption.SinkDepthScaling{Enabled: true, ThinListings: 2, MinCrushScale: 0.1}

	cases := []struct {
		name     string
		policy   absorption.SinkDepthScaling
		listings int
	}{
		{"disabled policy", absorption.SinkDepthScaling{ThinListings: 2, MinCrushScale: 0.1}, 40},
		{"zero-value policy", absorption.SinkDepthScaling{}, 40},
		{"unreadable breadth", armed, 0},
		{"negative breadth", armed, -3},
		{"thin threshold unset", absorption.SinkDepthScaling{Enabled: true, MinCrushScale: 0.1}, 40},
		{"floor unset", absorption.SinkDepthScaling{Enabled: true, ThinListings: 2}, 40},
		{"floor above unity", absorption.SinkDepthScaling{Enabled: true, ThinListings: 2, MinCrushScale: 1.5}, 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, 1.0, tc.policy.CrushScale(tc.listings))
		})
	}
}

// A market at or under the thin threshold is a micro-market: one tranche can take its
// bid off the board, so it keeps the full prior even with the policy armed.
func TestSinkDepthScaling_ThinMarketKeepsTheFullPrior(t *testing.T) {
	policy := absorption.SinkDepthScaling{Enabled: true, ThinListings: 2, MinCrushScale: 0.1}

	require.Equal(t, 1.0, policy.CrushScale(1), "a single-listing micro-market is the class the prior was fitted on")
	require.Equal(t, 1.0, policy.CrushScale(2))
	require.Less(t, policy.CrushScale(3), 1.0, "past the threshold the discount starts")
}

// The discount is proportional to breadth and monotone in it — a broader market never
// pays MORE for the same sale than a narrower one.
func TestSinkDepthScaling_DiscountsInProportionToBreadth(t *testing.T) {
	policy := absorption.SinkDepthScaling{Enabled: true, ThinListings: 2, MinCrushScale: 0.1}

	require.InDelta(t, 0.5, policy.CrushScale(4), 1e-9)
	require.InDelta(t, 0.2, policy.CrushScale(10), 1e-9)

	prev := 1.0
	for listings := 1; listings <= 60; listings++ {
		got := policy.CrushScale(listings)
		require.LessOrEqual(t, got, prev, "breadth %d must not be charged more than %d", listings, listings-1)
		require.Greater(t, got, 0.0)
		require.LessOrEqual(t, got, 1.0)
		prev = got
	}
}

// No market is bottomless. The floor bounds how far breadth may discount a sale's
// claim, so an arbitrarily broad hub still carries a fraction of it.
func TestSinkDepthScaling_FloorBoundsTheDiscount(t *testing.T) {
	policy := absorption.SinkDepthScaling{Enabled: true, ThinListings: 2, MinCrushScale: 0.1}

	require.Equal(t, 0.1, policy.CrushScale(1000))
	require.Equal(t, 0.1, policy.CrushScale(20), "the floor binds at a breadth a real hub reaches")
}

// The documented fallback: the uniform model stays expressible through the same knobs,
// so a captain can revert the refit without a rebuild.
func TestSinkDepthScaling_UniformModelStaysExpressible(t *testing.T) {
	uniform := absorption.SinkDepthScaling{Enabled: true, ThinListings: 2, MinCrushScale: 1.0}

	for _, listings := range []int{1, 2, 5, 40, 400} {
		require.Equal(t, 1.0, uniform.CrushScale(listings))
	}
}

// The defaults are the shipped fit, and they must stay inside the shape's own bounds.
func TestSinkDepthScaling_DefaultsAreWellFormed(t *testing.T) {
	require.Positive(t, absorption.DefaultThinListings)
	require.Greater(t, absorption.DefaultMinCrushScale, 0.0)
	require.Less(t, absorption.DefaultMinCrushScale, 1.0)
}

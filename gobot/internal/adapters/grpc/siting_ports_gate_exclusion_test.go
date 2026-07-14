package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// sp-vh1s §5.3 — the siting profit portfolio must EXCLUDE gate materials under the unified gate-fill
// toggle, so the general-economy portfolio never spins up a competing HARVESTING chain for a good the
// construction gate run owns (the sibling-harvester collision of §4). The exclusion is a SCORE-time
// veto: a gate good is dropped before it can ever be launched.

// The pure exclusion decision: a good is excluded from siting iff the toggle is ON *and* the good is a
// configured gate material. OFF (or a non-gate good) is never excluded — byte-identical to today.
func TestGateMaterialExcludedFromSiting(t *testing.T) {
	gate := map[string]bool{"FAB_MATS": true, "ADVANCED_CIRCUITRY": true}

	cases := []struct {
		name    string
		good    string
		unified bool
		want    bool
	}{
		{name: "gate good under toggle ON is excluded", good: "FAB_MATS", unified: true, want: true},
		{name: "gate good under toggle OFF is NOT excluded", good: "FAB_MATS", unified: false, want: false},
		{name: "non-gate good under toggle ON is NOT excluded", good: "COPPER_ORE", unified: true, want: false},
		{name: "non-gate good under toggle OFF is NOT excluded", good: "COPPER_ORE", unified: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, gateMaterialExcludedFromSiting(tc.good, gate, tc.unified))
		})
	}
}

// The projector VETOES (Proceed=false) a gate material under the toggle, dropping the candidate at
// SCORE time (zero cost) so it never reaches Launch — the resolver/guard are never even consulted for
// an excluded good.
func TestSitingChainProjector_UnifiedGateFill_VetoesGateMaterial(t *testing.T) {
	projector := &sitingChainProjector{
		unifiedGateFill: true,
		gateMaterials:   map[string]bool{"FAB_MATS": true},
	}

	proj, err := projector.Project(context.Background(), "FAB_MATS", "X1-HOME", 1)
	require.NoError(t, err)
	require.False(t, proj.Proceed, "a gate material must be vetoed from the siting portfolio under unified gate-fill")
}

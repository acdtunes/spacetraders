package grpc

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/stretchr/testify/require"
)

func growthBounds(t *testing.T) map[string]TuneBound {
	t.Helper()
	bounds, ok := tunableKnobsByContainerType()[string(container.ContainerTypeFleetGrowth)]
	require.True(t, ok, "the fleet-growth coordinator must be registered as tunable")
	return bounds
}

// THE BOUND IS THE MACHINE-READABLE HALF, AND IT MUST NOT CONTRADICT ITS OWN SENTENCE. Min/Max
// cross the wire on `tune --show` and are what anything validating a proposed value reads; the
// Description is what a human reads. `tune <key> 0` is the fleet-wide revert-to-default VERB — the
// writer skips the bound check for 0 and DELETES the key — so 0 reaches none of these knobs as a
// value. A Min of 0 therefore advertises a setting the writer refuses, and it advertises it to the
// consumer that cannot read the prose saying otherwise.
func TestGrowthTune_NoKnobAdvertisesZeroAsASettableValue(t *testing.T) {
	for key, bound := range growthBounds(t) {
		require.Equal(t, 1, bound.Min,
			"%s: 0 is the revert verb, never a value — a Min of 0 offers a setting the writer refuses", key)
		require.GreaterOrEqual(t, bound.Max, bound.Min, "%s bounds must be ordered", key)
		require.GreaterOrEqual(t, bound.Default, bound.Min, "%s default is below its own minimum", key)
	}
}

// The two knobs whose descriptions make the claim explicitly: the bound must say the same thing.
// heavy_cap's hold-at-zero route is the master switch, not a cap of 0, and the runway only ever
// ADDS to the immutable reserve floor — so neither may read as "0 is available here".
func TestGrowthTune_HeavyCapAndRunwayBoundsMatchTheirOwnDescriptions(t *testing.T) {
	bounds := growthBounds(t)

	heavyCap := bounds["heavy_cap"]
	require.Equal(t, 1, heavyCap.Min)
	require.Contains(t, heavyCap.Description, "ZERO IS NOT EXPRESSIBLE HERE")
	require.Contains(t, heavyCap.Description, "growth_enabled 2",
		"the description must name the hold an operator can actually reach")

	runway := bounds["growth_runway_milli_hours"]
	require.Equal(t, 1, runway.Min)
	require.Contains(t, runway.Description, "the smallest hold is 1, not 0")
}

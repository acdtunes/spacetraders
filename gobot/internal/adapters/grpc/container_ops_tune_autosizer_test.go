package grpc

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/stretchr/testify/require"
)

func autosizerBounds(t *testing.T) map[string]TuneBound {
	t.Helper()
	bounds, ok := tunableKnobsByContainerType()[string(container.ContainerTypeFleetAutosizer)]
	require.True(t, ok, "the fleet autosizer must be registered as tunable")
	return bounds
}

// sp-k4wdd — sizing_enabled is bounded [1,2] and its description STATES the encoding.
//
// The bound is what makes the encoding discoverable: `tune sizing_enabled 0` is the fleet-wide
// revert verb (MutateContainerConfigKey skips the bound check for 0 and DELETES the key), so a 0/1
// flag could not express "off" at all. An operator reading `tune --show` has only the description
// to tell them that 2 is off rather than an out-of-range mistake.
func TestAutosizerTune_SizingEnabledEncodingIsDocumented(t *testing.T) {
	bound := autosizerBounds(t)["sizing_enabled"]

	require.Equal(t, 1, bound.Min)
	require.Equal(t, 2, bound.Max)
	require.Equal(t, 1, bound.Default, "the autosizer ships ARMED")
	require.Contains(t, bound.Description, "1=on")
	require.Contains(t, bound.Description, "2=off")
}

// The switch's whole value is that it stops the READS, so the description must say so.
//
// This is not documentation pedantry. expansion_enabled — the knob an operator will pattern-match
// this one against — explicitly does the OPPOSITE ("Off does NOT stop the free work"). An operator
// who assumes the same semantics here would believe the autosizer was still discovering yards while
// merely not buying, and would not understand why their API budget recovered. The one thing this
// description must never be is silently interchangeable with its sibling's.
func TestAutosizerTune_SizingEnabledSaysItStopsTheReadsNotJustTheBuying(t *testing.T) {
	description := autosizerBounds(t)["sizing_enabled"].Description

	require.Contains(t, description, "STOPS THE READS",
		"the description must state that OFF stops the reads — the sibling knob expansion_enabled pauses "+
			"spending while free work CONTINUES, and an operator who assumes that here is misled")
	require.Contains(t, description, "no restart",
		"the description must state the tune applies live, like its siblings")
}

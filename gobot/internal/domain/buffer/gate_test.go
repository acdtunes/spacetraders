package buffer_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/buffer"
)

// admittedGood is a good that clears ALL THREE sp-rxrg gates: it is contracted to the hub, the
// hub does not produce it locally, and its nearest external source is comfortably beyond the
// distance floor. Every mutation test starts from this admitted baseline and flips exactly one
// input, so a re-admission proves the flipped gate was the sole reason for exclusion.
func admittedGood() buffer.Facts {
	return buffer.Facts{
		Good:                        "ASSAULT_RIFLES",
		HubContractFrequency:        3,
		HubProducesLocally:          false,
		ExternalSourceDistance:      300,
		ExternalSourceDistanceKnown: true,
	}
}

func TestGate_AdmitsAContractedRemotelySourcedNonLocalGood(t *testing.T) {
	gate := buffer.Gate{MinExternalSourceDistance: 25}
	require.True(t, gate.Admits(admittedGood()),
		"a hub contract good that the hub does not produce and whose source is far must be buffered")
}

// TestGate_EachGateIsIndependentlyLoadBearing is the mutation proof: starting from an admitted
// good, disabling any ONE gate's passing condition must exclude it — so removing that gate would
// re-admit exactly the good it exists to exclude.
func TestGate_EachGateIsIndependentlyLoadBearing(t *testing.T) {
	gate := buffer.Gate{MinExternalSourceDistance: 25}
	cases := []struct {
		name   string
		mutate func(*buffer.Facts)
	}{
		{"gate 1 contract-membership: not contracted to this hub", func(f *buffer.Facts) { f.HubContractFrequency = 0 }},
		{"gate 2 local-production: the hub's own market makes it", func(f *buffer.Facts) { f.HubProducesLocally = true }},
		{"gate 3 source-distance: nearest source is co-located", func(f *buffer.Facts) { f.ExternalSourceDistance = 0 }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			facts := admittedGood()
			require.True(t, gate.Admits(facts), "precondition: the un-mutated good is admitted")
			testCase.mutate(&facts)
			require.False(t, gate.Admits(facts), testCase.name)
		})
	}
}

// TestGate_SourceDistanceExactlyAtTheFloorIsExcluded pins the boundary: the floor is inclusive
// (<=), so a source exactly at the threshold is treated as too-near.
func TestGate_SourceDistanceExactlyAtTheFloorIsExcluded(t *testing.T) {
	gate := buffer.Gate{MinExternalSourceDistance: 25}
	facts := admittedGood()
	facts.ExternalSourceDistance = 25
	require.False(t, gate.Admits(facts), "a source exactly at the distance floor is too near to buffer")
}

// TestGate_FailsOpenWhenSourceDistanceUnknown proves gate 3 never excludes on missing distance
// data: an uncached far good keeps its buffer eligibility (no false-positive gating).
func TestGate_FailsOpenWhenSourceDistanceUnknown(t *testing.T) {
	gate := buffer.Gate{MinExternalSourceDistance: 25}
	facts := admittedGood()
	facts.ExternalSourceDistanceKnown = false
	facts.ExternalSourceDistance = 0 // ignored: a zero distance that is not KNOWN must not exclude
	require.True(t, gate.Admits(facts),
		"gate 3 fails open on an unknown source distance so a far good with uncached coords is not dropped")
}

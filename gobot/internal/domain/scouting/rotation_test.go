package scouting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testWaitLow  = 50 * time.Millisecond
	testWaitHigh = 1 * time.Second
)

// At or below waitLow the fleet has headroom: full scanning AND discovery may
// spend it.
func TestActiveShare_AtOrBelowWaitLow_FullShareWithDiscovery(t *testing.T) {
	for _, wait := range []time.Duration{0, 10 * time.Millisecond, testWaitLow} {
		share, discovery := ActiveShare(wait, testWaitLow, testWaitHigh)
		require.Equal(t, 1.0, share, "wait %v", wait)
		require.True(t, discovery, "wait %v", wait)
	}
}

// Discovery sheds FIRST: the moment wait exceeds waitLow, discovery stops while
// steady scanning continues at full share.
func TestActiveShare_BetweenLowAndHigh_DiscoveryShedsFirst(t *testing.T) {
	for _, wait := range []time.Duration{testWaitLow + 1, 500 * time.Millisecond, testWaitHigh - 1} {
		share, discovery := ActiveShare(wait, testWaitLow, testWaitHigh)
		require.Equal(t, 1.0, share, "wait %v: scanning holds while only discovery sheds", wait)
		require.False(t, discovery, "wait %v", wait)
	}
}

// From waitHigh the share scales linearly 1.0 → 0.5 over [waitHigh, 4×waitHigh].
func TestActiveShare_LinearRampFromWaitHigh(t *testing.T) {
	share, discovery := ActiveShare(testWaitHigh, testWaitLow, testWaitHigh)
	require.Equal(t, 1.0, share, "the ramp starts at 1.0 exactly at waitHigh")
	require.False(t, discovery)

	// Midpoint of the ramp: 2.5×waitHigh is half way through [waitHigh, 4×waitHigh].
	share, _ = ActiveShare(2500*time.Millisecond, testWaitLow, testWaitHigh)
	require.InDelta(t, 0.75, share, 1e-9)

	share, _ = ActiveShare(4*testWaitHigh, testWaitLow, testWaitHigh)
	require.InDelta(t, 0.5, share, 1e-9, "the ramp bottoms out at 0.5 at 4×waitHigh")
}

// Beyond 4×waitHigh the share floors at 0.5: degradation is bounded, never
// open-ended — half the fleet always scans.
func TestActiveShare_BeyondRampFloorsAtHalf(t *testing.T) {
	for _, wait := range []time.Duration{5 * testWaitHigh, 100 * testWaitHigh} {
		share, discovery := ActiveShare(wait, testWaitLow, testWaitHigh)
		require.Equal(t, 0.5, share, "wait %v", wait)
		require.False(t, discovery, "wait %v", wait)
	}
}

// Share 1.0 means nothing goes dormant.
func TestRotateDormant_FullShareNothingDormant(t *testing.T) {
	dormant, next := RotateDormant([]string{"X1-BB", "X1-AA", "X1-CC"}, 1.0, 0)
	require.Empty(t, dormant)
	require.GreaterOrEqual(t, next, 0)
}

// Share 0.5 over four systems parks exactly two per call, and over four
// consecutive calls every system is active at least twice: round-robin
// starvation-freedom, asserted explicitly.
func TestRotateDormant_HalfShareRotatesWithoutStarvation(t *testing.T) {
	inScope := []string{"X1-DD", "X1-BB", "X1-AA", "X1-CC"} // unsorted on purpose
	activeCounts := map[string]int{}
	cursor := 0
	for call := 0; call < 4; call++ {
		dormant, next := RotateDormant(inScope, 0.5, cursor)
		require.Len(t, dormant, 2, "call %d: half of four systems are dormant", call)
		for _, system := range inScope {
			if !dormant[system] {
				activeCounts[system]++
			}
		}
		cursor = next
	}
	for _, system := range inScope {
		require.GreaterOrEqualf(t, activeCounts[system], 2, "system %s starved by rotation", system)
	}
}

// Equal inputs produce equal outputs: the rotation is a pure function of
// (inScope, share, cursor) — input order must not matter.
func TestRotateDormant_DeterministicForEqualInputs(t *testing.T) {
	a, nextA := RotateDormant([]string{"X1-CC", "X1-AA", "X1-BB", "X1-DD"}, 0.5, 1)
	b, nextB := RotateDormant([]string{"X1-DD", "X1-BB", "X1-CC", "X1-AA"}, 0.5, 1)
	require.Equal(t, a, b)
	require.Equal(t, nextA, nextB)
}

// The active window is contiguous over the SORTED list starting at cursor, and
// the cursor advances by the active count — so consecutive calls walk the ring.
func TestRotateDormant_CursorWalksTheSortedRing(t *testing.T) {
	inScope := []string{"X1-DD", "X1-CC", "X1-BB", "X1-AA"}

	dormant, next := RotateDormant(inScope, 0.5, 0)
	require.Equal(t, map[string]bool{"X1-CC": true, "X1-DD": true}, dormant,
		"cursor 0 activates the first two of the sorted ring (AA, BB)")
	require.Equal(t, 2, next)

	dormant, next = RotateDormant(inScope, 0.5, next)
	require.Equal(t, map[string]bool{"X1-AA": true, "X1-BB": true}, dormant,
		"cursor 2 activates CC, DD")
	require.Equal(t, 0, next, "the cursor wraps around the ring")
}

// The active count is the CEILING of len×share, so a fractional share never
// rounds a system's slot away: 3 systems at share 0.5 keep 2 active.
func TestRotateDormant_CeilingActiveCount(t *testing.T) {
	dormant, next := RotateDormant([]string{"X1-AA", "X1-BB", "X1-CC"}, 0.5, 0)
	require.Len(t, dormant, 1)
	require.Equal(t, 2, next)
}

// An empty scope is a no-op.
func TestRotateDormant_EmptyScope(t *testing.T) {
	dormant, next := RotateDormant(nil, 0.5, 3)
	require.Empty(t, dormant)
	require.Equal(t, 3, next)
}

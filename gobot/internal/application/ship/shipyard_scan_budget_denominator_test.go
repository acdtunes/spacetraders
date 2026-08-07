package ship

// The denominator's own invariant: a counter hiccup may not widen the budget.
//
// refreshChartedCount's contract is that a failed reading leaves the previous map
// size in place, because the map size is what lengthens every yard's interval —
// lose it and the rotation shortens, the anti-starvation escape fires against a
// map orders of magnitude smaller than the real one, and the allowance this type
// exists to enforce stops being enforced. An answer of zero has to be treated as
// the same kind of failure as an error, because the count reaches the budget
// through a scope predicate that can degrade to a clause no row matches — it
// arrives as a confident zero rather than as an error.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
)

func TestChartedCount_AZeroReadingLeavesTheDenominatorInPlaceRatherThanCollapsingIt(t *testing.T) {
	b, now := newTestYardBudget(t, 0)
	counter := &countingYardCounter{count: 1000}
	b.SetChartedYardCounter(counter)
	ctx := context.Background()

	b.Admit(ctx, testPlayerID, "X1-AA-Y1", time.Time{}, false, marketscan.Discretionary)
	require.Equal(t, 1000, b.Snapshot().YardsKnown, "the wired counter should set the denominator")

	// A confident zero, with no error beside it: the shape an unresolvable scope
	// produces, since it still answers — from a clause that matches nothing.
	counter.count = 0
	*now = now.Add(yardCountTTL + time.Second)
	b.Admit(ctx, testPlayerID, "X1-AA-Y2", time.Time{}, false, marketscan.Discretionary)

	require.Equal(t, 1000, b.Snapshot().YardsKnown,
		"a zero reading must leave the previous denominator standing, not fall back to the handful of yards this process has been asked about")
}

// A zero reading must not shorten the anti-starvation bound either. This is the
// term that admits unconditionally, above the token cap and above the value bar,
// so a collapsed denominator here converts a paced budget into an unpaced one:
// every yard reads as past its bound and every read is forced through.
func TestChartedCount_AZeroReadingDoesNotShortenTheAntiStarvationBound(t *testing.T) {
	b, now := newTestYardBudget(t, 0)
	counter := &countingYardCounter{count: 1000}
	b.SetChartedYardCounter(counter)
	ctx := context.Background()

	b.Admit(ctx, testPlayerID, "X1-AA-Y1", time.Time{}, false, marketscan.Discretionary)
	bound := b.Snapshot().WorstCaseStaleness

	counter.count = 0
	*now = now.Add(yardCountTTL + time.Second)
	b.Admit(ctx, testPlayerID, "X1-AA-Y2", time.Time{}, false, marketscan.Discretionary)

	require.Equal(t, bound, b.Snapshot().WorstCaseStaleness,
		"the bound every unconditional admission is measured against must not move because a counter answered zero")
}

// The counter still has to be allowed to report a SMALLER map. Refusing zero is
// about an unreadable count, not about pinning the denominator to its high-water
// mark — an era transition genuinely shrinks the charted map, and a budget that
// could only ever grow its denominator would pace the new era against the old
// one's size forever.
func TestChartedCount_AShrinkingMapIsStillAccepted(t *testing.T) {
	b, now := newTestYardBudget(t, 0)
	counter := &countingYardCounter{count: 1000}
	b.SetChartedYardCounter(counter)
	ctx := context.Background()

	b.Admit(ctx, testPlayerID, "X1-AA-Y1", time.Time{}, false, marketscan.Discretionary)
	require.Equal(t, 1000, b.Snapshot().YardsKnown)

	counter.count = 40
	*now = now.Add(yardCountTTL + time.Second)
	b.Admit(ctx, testPlayerID, "X1-AA-Y2", time.Time{}, false, marketscan.Discretionary)

	require.Equal(t, 40, b.Snapshot().YardsKnown,
		"a real, smaller count is a map that shrank and must be adopted")
}

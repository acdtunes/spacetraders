package queries

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// staleGateEdges is gateEdges with every row past its freshness window — the shape Adjacency
// reports for a system whose gates were charted and have since aged out.
func staleGateEdges(neighbours ...string) []domainSystem.GateEdge {
	edges := gateEdges(neighbours...)
	for i := range edges {
		edges[i].Stale = true
	}
	return edges
}

// reversedCrossSystemFuelSurface is crossSystemFuelSurface with the two systems' roles swapped:
// the exporters sit in BB and the rich importers in AA, so the same four lanes run BB -> AA.
func reversedCrossSystemFuelSurface(t *testing.T) *fakeLaneMarketReader {
	t.Helper()
	return &fakeLaneMarketReader{
		systems: map[string][]string{
			"X1-AA": {"X1-AA-1", "X1-AA-2"},
			"X1-BB": {"X1-BB-1", "X1-BB-2"},
		},
		markets: map[string]*market.Market{
			"X1-BB-1": mkt(t, "X1-BB-1", good(t, "FUEL", 50, 100, 50, market.TradeTypeExport)),
			"X1-BB-2": mkt(t, "X1-BB-2", good(t, "FUEL", 60, 120, 50, market.TradeTypeExport)),
			"X1-AA-1": mkt(t, "X1-AA-1", good(t, "FUEL", 2000, 3000, 50, market.TradeTypeImport)),
			"X1-AA-2": mkt(t, "X1-AA-2", good(t, "FUEL", 1800, 2500, 50, market.TradeTypeImport)),
		},
	}
}

// THE CENSUS BILLS A ROUND TRIP, SO IT MUST PROVE ONE. Reach is DIRECTED, and every other fixture
// in this package wires a symmetric graph under which one lookup answers for both legs — so nothing
// here measured whether the return the price assumes was ever asked for.
//
// The stored graph is asymmetric in two ordinary states: one end charted and the other not, and one
// end's rows aged out (arriving AT an unverified system resolves, expanding THROUGH one does not).
// A hull dedicated to such a lane flies out with a cargo and cannot be proven able to come back and
// re-buy, yet the trip value it was counted on charged for that return — half a proof at the full
// price, which RULINGS #4 does not allow. Asking only the OTHER end is the sibling defect: the lane
// then borrows its neighbour's proof and counts outright.
//
// Both surfaces below are therefore zero, and the symmetric calibration is what stops that pair of
// zeros from passing for a census that has simply stopped counting.
func TestCountProfitableLanes_RealGateGraph_ProvesBothLegsOfTheCircuitItPrices(t *testing.T) {
	for _, c := range []struct {
		name      string
		adjacency map[string][]domainSystem.GateEdge
	}{
		{name: "the far end's gate was never charted", adjacency: map[string][]domainSystem.GateEdge{
			"X1-AA": gateEdges("X1-BB"),
		}},
		{name: "the far end's charted gates have aged out", adjacency: map[string][]domainSystem.GateEdge{
			"X1-AA": gateEdges("X1-BB"),
			"X1-BB": staleGateEdges("X1-AA"),
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := &laneGateStore{adjacency: c.adjacency}
			pair := []string{"X1-AA", "X1-BB"}
			svc := gategraph.NewService(store, nil, nil, nil)

			forward, err := svc.StoredHopDistances(context.Background(), "X1-AA", pair, laneSpanHops)
			require.NoError(t, err)
			require.Equal(t, 1, forward["X1-BB"], "calibration: leaving AA the crossing IS proven")
			reverse, err := svc.StoredHopDistances(context.Background(), "X1-BB", pair, laneSpanHops)
			require.NoError(t, err)
			require.NotContains(t, reverse, "X1-AA",
				"calibration: leaving BB the same crossing is unproven, so the fixture is genuinely directed")

			count, readable, err := countOverRealGateGraph(t, crossSystemFuelSurface(t), store, pair...)
			require.NoError(t, err)
			require.True(t, readable)
			require.Zero(t, count,
				"these four lanes run AA -> BB, and nothing proves the return leg they were charged for")

			count, readable, err = countOverRealGateGraph(t, reversedCrossSystemFuelSurface(t), store, pair...)
			require.NoError(t, err)
			require.True(t, readable)
			require.Zero(t, count,
				"the same four lanes priced BB -> AA leave an end nothing proves, and must not size a hull")

			symmetric := &laneGateStore{adjacency: gatedChain("X1-AA", "X1-BB")}
			count, _, err = countOverRealGateGraph(t, crossSystemFuelSurface(t), symmetric, pair...)
			require.NoError(t, err)
			require.Equal(t, 4, count,
				"calibration: the same market surface over a graph that proves BOTH legs is four lanes")
		})
	}
}

// A refused lane must not truncate the census. Each guard skips the lane it refuses and keeps
// counting; one that stopped the walk would collapse the count toward zero the moment a bad lane
// sorted first — the under-count this plan exists to undo, arriving by a different route.
//
// EVERY CASE PUTS ITS REFUSAL AND ITS SURVIVOR ON ONE GOOD, and lists the refused sink first. A
// good's pairs are enumerated together, source-index-major over the pooled listings, so the refusal
// is reached BEFORE the survivor no matter what order the goods themselves come in — an ordering
// this test cannot afford to inherit from anywhere else. The expected 1 is therefore two assertions
// at once: the guard refuses, or the answer is 2; and the walk continues past it, or the answer is 0.
func TestCountProfitableLanes_ARefusedLaneDoesNotTruncateTheCensus(t *testing.T) {
	t.Run("a sub-floor lane enumerated before the survivor", func(t *testing.T) {
		markets := &fakeLaneMarketReader{
			systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2", "X1-AA-3"}},
			markets: map[string]*market.Market{
				"X1-AA-1": mkt(t, "X1-AA-1", good(t, "FUEL", 50, 100, 50, market.TradeTypeExport)),
				// Spread 900, under MinBidMargin. Listed first, so it is the first pair judged.
				"X1-AA-2": mkt(t, "X1-AA-2", good(t, "FUEL", 1000, 1500, 50, market.TradeTypeImport)),
				"X1-AA-3": mkt(t, "X1-AA-3", good(t, "FUEL", 2000, 3000, 50, market.TradeTypeImport)),
			},
		}

		count, readable, err := countOverRealGateGraph(t, markets,
			&laneGateStore{adjacency: map[string][]domainSystem.GateEdge{}}, "X1-AA")
		require.NoError(t, err)
		require.True(t, readable)
		require.Equal(t, 1, count, "the sub-floor sink is skipped and the deeper one behind it still counts")
	})

	t.Run("an unreachable lane enumerated before the survivor", func(t *testing.T) {
		markets := &fakeLaneMarketReader{
			systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2"}, "X1-BB": {"X1-BB-1"}},
			markets: map[string]*market.Market{
				"X1-BB-1": mkt(t, "X1-BB-1", good(t, "FUEL", 2000, 3000, 50, market.TradeTypeImport)),
				"X1-AA-1": mkt(t, "X1-AA-1", good(t, "FUEL", 50, 100, 50, market.TradeTypeExport)),
				"X1-AA-2": mkt(t, "X1-AA-2", good(t, "FUEL", 2000, 3000, 50, market.TradeTypeImport)),
			},
		}

		// X1-BB is scanned first, so its sink is the first the exporter is paired with.
		count, readable, err := countOverRealGateGraph(t, markets,
			&laneGateStore{adjacency: map[string][]domainSystem.GateEdge{}}, "X1-BB", "X1-AA")
		require.NoError(t, err)
		require.True(t, readable)
		require.Equal(t, 1, count, "the ungated crossing is skipped and the within-system sink still counts")
	})

	t.Run("a lane that cannot pay for its crossing, enumerated before the survivor", func(t *testing.T) {
		require.Less(t, int64(1000*5), 2*domainSensing.DefaultGateFeeCredits,
			"calibration: the crossing trip must earn less than the two gates it pays")
		markets := &fakeLaneMarketReader{
			systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2"}, "X1-BB": {"X1-BB-1"}},
			markets: map[string]*market.Market{
				"X1-BB-1": mkt(t, "X1-BB-1", good(t, "FUEL", 1100, 1600, 5, market.TradeTypeImport)),
				"X1-AA-1": mkt(t, "X1-AA-1", good(t, "FUEL", 50, 100, 5, market.TradeTypeExport)),
				"X1-AA-2": mkt(t, "X1-AA-2", good(t, "FUEL", 1100, 1600, 4, market.TradeTypeImport)),
			},
		}

		count, readable, err := countOverRealGateGraph(t, markets,
			&laneGateStore{adjacency: gatedChain("X1-AA", "X1-MID", "X1-BB")}, "X1-BB", "X1-AA")
		require.NoError(t, err)
		require.True(t, readable)
		require.Equal(t, 1, count, "the two-gate lane is skipped as a loss and the gate-free one still counts")
	})
}

// An origin the distance walk was never run from is UNPROVEN, not next door. Today's market reads
// cannot produce one — every listing comes from a scanned system — so this is precisely the kind of
// fail-closed branch that rots unwatched: flipped to "reachable at zero hops" nothing else fails,
// and every pooled pairing then counts against a crossing nobody priced.
func TestCountProfitableLanes_ALaneFromAnUnwalkedOriginIsNotCounted(t *testing.T) {
	exp, imp := profitablePair(t, "FUEL")
	markets := &fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {"X1-ZZ-1", "X1-ZZ-2"}},
		markets: map[string]*market.Market{
			"X1-ZZ-1": mkt(t, "X1-ZZ-1", exp),
			"X1-ZZ-2": mkt(t, "X1-ZZ-2", imp),
		},
	}
	store := &laneGateStore{adjacency: gatedChain("X1-AA", "X1-ZZ")}

	count, readable, err := countOverRealGateGraph(t, markets, store, "X1-AA")
	require.NoError(t, err)
	require.True(t, readable)
	require.Zero(t, count, "no walk started at X1-ZZ, so nothing about a lane leaving it is proven")

	count, readable, err = countOverRealGateGraph(t, markets, store, "X1-AA", "X1-ZZ")
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 1, count, "calibration: walked from X1-ZZ the same pair IS a lane")
}

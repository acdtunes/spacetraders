package expansion

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// ---- pure selection cores (mutation-verified) --------------------------------------

// pickReusableProbe picks the FEWEST-hop probe that is within maxHops AND sits in a system whose
// trade-value is strictly BELOW the ceiling — the depth-vs-freshness guard that NEVER strips a probe
// off a high-value core market. These rows pin each guard independently.
func TestPickReusableProbe_NearestUnderCeilingWithinReach(t *testing.T) {
	cases := []struct {
		name       string
		candidates []reusableProbeCandidate
		maxHops    int
		ceiling    int
		wantSymbol string
		wantOK     bool
	}{
		{
			name: "picks the fewest-hop candidate under the ceiling",
			candidates: []reusableProbeCandidate{
				{shipSymbol: "P-FAR", fromSystem: "X1-A", systemValue: 10000, hops: 3},
				{shipSymbol: "P-NEAR", fromSystem: "X1-B", systemValue: 10000, hops: 1},
			},
			maxHops: 3, ceiling: 50000, wantSymbol: "P-NEAR", wantOK: true,
		},
		{
			name: "NEVER strips a probe off a system AT/ABOVE the ceiling (high-value core protected)",
			candidates: []reusableProbeCandidate{
				{shipSymbol: "P-CORE", fromSystem: "X1-CORE", systemValue: 500000, hops: 1}, // rich core — protected
				{shipSymbol: "P-EDGE", fromSystem: "X1-EDGE", systemValue: 5000, hops: 2},   // low-value edge — borrowable
			},
			maxHops: 3, ceiling: 50000, wantSymbol: "P-EDGE", wantOK: true,
		},
		{
			name: "excludes candidates beyond the relay reach",
			candidates: []reusableProbeCandidate{
				{shipSymbol: "P-TOOFAR", fromSystem: "X1-A", systemValue: 1000, hops: 5},
			},
			maxHops: 3, ceiling: 50000, wantOK: false,
		},
		{
			name: "ceiling 0 (disabled) disqualifies EVERY candidate — borrow off no system (safe default)",
			candidates: []reusableProbeCandidate{
				{shipSymbol: "P-CHEAP", fromSystem: "X1-A", systemValue: 1, hops: 1},
			},
			maxHops: 3, ceiling: 0, wantOK: false,
		},
		{
			name: "hop tie breaks on the lowest ship symbol (deterministic)",
			candidates: []reusableProbeCandidate{
				{shipSymbol: "P-Z", fromSystem: "X1-A", systemValue: 1000, hops: 1},
				{shipSymbol: "P-A", fromSystem: "X1-B", systemValue: 1000, hops: 1},
			},
			maxHops: 3, ceiling: 50000, wantSymbol: "P-A", wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			best, ok := pickReusableProbe(tc.candidates, tc.maxHops, tc.ceiling)
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Equal(t, tc.wantSymbol, best.shipSymbol)
			}
		})
	}
}

// unchartedNeighborsFrom returns a system's gate-adjacent systems that are NOT charted — the next
// ring the snowball walk enqueues. A system with no adjacency entry (still virgin) yields none;
// under-construction and already-charted neighbors are excluded.
func TestUnchartedNeighborsFrom_ReturnsOnlyUnchartedReachableNeighbors(t *testing.T) {
	adj := map[string][]system.GateEdge{
		"X1-S": {
			{ConnectedSystem: "X1-CHARTED", GateWaypoint: "X1-S-G"},
			{ConnectedSystem: "X1-VIRGIN1", GateWaypoint: "X1-S-G"},
			{ConnectedSystem: "X1-VIRGIN2", GateWaypoint: "X1-S-G"},
			{ConnectedSystem: "X1-BUILDING", GateWaypoint: "X1-S-G", UnderConstruction: true},
		},
		"X1-CHARTED": {{ConnectedSystem: "X1-S", GateWaypoint: "X1-C-G"}}, // has edges -> charted
	}
	charted := func(sys string) bool {
		_, has := adj[sys]
		return has
	}

	got := unchartedNeighborsFrom(adj, "X1-S", charted)
	require.ElementsMatch(t, []string{"X1-VIRGIN1", "X1-VIRGIN2"}, got,
		"only uncharted, non-under-construction neighbors are enqueued")

	require.Empty(t, unchartedNeighborsFrom(adj, "X1-VIRGIN1", charted),
		"a still-virgin system has no adjacency entry -> no walk (self-gated)")
}

// ---- MarketSystemValueReader: the ASK is the valuation leg (sp-en5h7) --------

// fakeValueMarketReader serves one good per waypoint; a waypoint absent from goods
// stands in for an unreadable market (contributes 0 rather than failing the read).
type fakeValueMarketReader struct {
	waypoints []string
	goods     map[string]market.TradeGood
}

func (f *fakeValueMarketReader) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	return f.waypoints, nil
}

func (f *fakeValueMarketReader) GetMarketData(_ context.Context, waypointSymbol string, _ int) (*market.Market, error) {
	g, ok := f.goods[waypointSymbol]
	if !ok {
		return nil, nil
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{g}, time.Now())
}

// SystemTradeValue values a system's depth at the ASK (TradeGood.PurchasePrice, the
// LARGER of the pair — sp-en5h7), never at the bid. This is load-bearing for
// RULINGS #4: pickReusableProbe borrows a probe only when systemValue < ceiling, so
// valuing at the smaller bid would shrink every total by the market's rake and
// LOOSEN the depth-vs-freshness guard into cannibalizing markets it had protected.
func TestSystemTradeValue_ValuesAtTheAskNotTheBid(t *testing.T) {
	const ask, bid, volume = 500, 100, 10
	g, err := market.NewTradeGood("FUEL", nil, nil, ask, bid, volume, market.TradeType("EXCHANGE"))
	require.NoError(t, err)

	reader := NewMarketSystemValueReader(&fakeValueMarketReader{
		waypoints: []string{"X1-A-1", "X1-A-UNREADABLE"},
		goods:     map[string]market.TradeGood{"X1-A-1": *g},
	})

	got, err := reader.SystemTradeValue(context.Background(), "X1-A", 1)
	require.NoError(t, err)
	require.Equal(t, ask*volume, got,
		"depth is valued at the ask (PurchasePrice) × volume; the unreadable market adds 0")
	require.Greater(t, got, bid*volume,
		"the ask-based total must EXCEED the bid-based one, so the reuse ceiling stays stricter, never looser")
}

package queries

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// The census is bounded by the REAL gategraph service here, not by a stand-in for it.
//
// Every other test in this package hands the reader a fake whose contract was written from the
// service's documentation, which makes those tests a check on the census and not on the agreement
// between the two. The agreement is the thing the bound rests on: the census must count exactly the
// pairings the planner will later route, and a fake cannot drift when the service does. Two of the
// properties relied on here — the origin answering at zero hops, and refusal expressed by ABSENCE
// rather than a large number — are read straight out of the returned map with no ceremony, so
// either could change under the census without a compile error.
//
// The tests below therefore drive gategraph.Service over a stored adjacency, which is also the
// closest a unit test gets to production: the daemon wires this same service into the reader.

// laneGateStore serves a fixed stored gate adjacency and refuses every other read. The refusals are
// assertions: proving reach for a census must cost the store one read and the API nothing, so a
// resolver that reached for per-system Edges (the fetch-through path) fails here rather than
// quietly turning a fleet-wide census into an API storm.
type laneGateStore struct {
	adjacency map[string][]domainSystem.GateEdge
	adjErr    error
}

func (s *laneGateStore) Adjacency(ctx context.Context) (map[string][]domainSystem.GateEdge, error) {
	return s.adjacency, s.adjErr
}

func (s *laneGateStore) Edges(ctx context.Context, systemSymbol string) ([]domainSystem.GateEdge, bool, error) {
	return nil, false, errors.New("the lane census must not read per-system Edges (that is the fetch-through path)")
}

func (s *laneGateStore) GateWaypointOf(ctx context.Context, systemSymbol string) (string, bool, error) {
	return "", false, errors.New("the lane census must not resolve gate waypoints")
}

func (s *laneGateStore) Replace(ctx context.Context, systemSymbol string, edges []domainSystem.GateEdge) error {
	return errors.New("the lane census is read-only and must never write the gate cache")
}

func (s *laneGateStore) UnreadableState(ctx context.Context, systemSymbol string) (int, time.Time, bool, error) {
	return 0, time.Time{}, false, errors.New("the lane census must not consult the unreadable-gate backoff")
}

func (s *laneGateStore) MarkUnreadable(ctx context.Context, systemSymbol, gateWaypoint string, now time.Time) (int, error) {
	return 0, errors.New("the lane census is read-only and must never mark a gate unreadable")
}

// gateEdges builds a system's stored, built, fresh edge set.
func gateEdges(neighbours ...string) []domainSystem.GateEdge {
	edges := make([]domainSystem.GateEdge, 0, len(neighbours))
	for _, n := range neighbours {
		edges = append(edges, domainSystem.GateEdge{ConnectedSystem: n, GateWaypoint: n + "-GATE"})
	}
	return edges
}

// gatedChain links a...z in a line, both ways, as the store would hold it.
func gatedChain(systems ...string) map[string][]domainSystem.GateEdge {
	adjacency := map[string][]domainSystem.GateEdge{}
	for i := 0; i+1 < len(systems); i++ {
		a, b := systems[i], systems[i+1]
		adjacency[a] = append(adjacency[a], gateEdges(b)...)
		adjacency[b] = append(adjacency[b], gateEdges(a)...)
	}
	return adjacency
}

// countOverRealGateGraph runs the census with a real gategraph.Service over the given stored
// adjacency. The nil API client is load-bearing: a resolver that tried to fetch would panic rather
// than pass.
func countOverRealGateGraph(t *testing.T, markets *fakeLaneMarketReader, store *laneGateStore, systems ...string) (int, bool, error) {
	t.Helper()
	reader := NewProfitableLaneReader(markets, gategraph.NewService(store, nil, nil, nil))
	return reader.countProfitable(context.Background(), 1, systems)
}

// A crossing the stored graph actually holds is counted, and the same market surface with the gate
// removed is not. The pair is the whole point: one number alone could be a broken fixture.
func TestCountProfitableLanes_RealGateGraph_CountsOnlyAGatedCrossing(t *testing.T) {
	gated, readable, err := countOverRealGateGraph(t, crossSystemFuelSurface(t),
		&laneGateStore{adjacency: gatedChain("X1-AA", "X1-BB")}, "X1-AA", "X1-BB")
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 4, gated, "both AA exporters pair with both BB importers across one built gate")

	ungated, readable, err := countOverRealGateGraph(t, crossSystemFuelSurface(t),
		&laneGateStore{adjacency: map[string][]domainSystem.GateEdge{}}, "X1-AA", "X1-BB")
	require.NoError(t, err)
	require.True(t, readable, "an unreachable pairing is a readable ZERO, not an outage")
	require.Zero(t, ungated, "the same four lanes with no gate between them are not work any hull can reach")
}

// The census's same-system answer, end to end. A fleet standing in a gateless system still has its
// own lanes, and they are counted — the reader reads a same-system pair as ZERO crossings, which is
// true only because the service says a system is nought hops from itself. Nothing else in the repo
// pins that; with it removed, the census silently counts no within-system lane at all, which is
// every lane the fleet could see before this plan widened the scan.
func TestCountProfitableLanes_RealGateGraph_CountsWithinSystemLanesWithNoGatesAtAll(t *testing.T) {
	exp, imp := profitablePair(t, "FUEL")
	markets := &fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2"}},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", exp),
			"X1-AA-2": mkt(t, "X1-AA-2", imp),
		},
	}

	count, readable, err := countOverRealGateGraph(t, markets,
		&laneGateStore{adjacency: map[string][]domainSystem.GateEdge{}}, "X1-AA")
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 1, count, "a pair inside one system crosses no gate and needs none to be counted")
}

// RULINGS #4: the census inherits the gate graph's passability rule, not merely its arithmetic. A
// route whose only intermediate gate is still being built cannot be flown, so it is not demand.
func TestCountProfitableLanes_RealGateGraph_RefusesARouteThroughAnUnbuiltGate(t *testing.T) {
	built := gatedChain("X1-AA", "X1-MID", "X1-BB")
	unbuilt := gatedChain("X1-AA", "X1-MID", "X1-BB")
	for i := range unbuilt["X1-MID"] {
		unbuilt["X1-MID"][i].UnderConstruction = true
	}

	count, _, err := countOverRealGateGraph(t, crossSystemFuelSurface(t), &laneGateStore{adjacency: built}, "X1-AA", "X1-BB")
	require.NoError(t, err)
	require.Equal(t, 4, count, "calibration: over two built gates the same lanes count")

	count, readable, err := countOverRealGateGraph(t, crossSystemFuelSurface(t), &laneGateStore{adjacency: unbuilt}, "X1-AA", "X1-BB")
	require.NoError(t, err)
	require.True(t, readable)
	require.Zero(t, count, "a hull cannot jump into a gate that is still being built")
}

// THE BOUND IS THE EXECUTOR'S, NOT THE FERRY'S. A hull ranks lanes over the system it stands in
// plus that system's directly-gated neighbours, so the widest pair any pool can hold is one hop in
// and one hop out. A sink past that is work no hull the count sizes can ever select, and counting it
// asks for a heavy anyway (RULINGS #4). Held instead to the pre-buy Routable reach — which answers
// whether a bought hull can be FERRIED somewhere, a different question — the census counts lanes out
// to five hops, and the assertion below at the ferry bound is what fails.
//
// The bound is what refuses here: the same fixture one hop nearer counts.
func TestCountProfitableLanes_RealGateGraph_RefusesASinkPastTheLanePoolSpan(t *testing.T) {
	chain := func(hops int) map[string][]domainSystem.GateEdge {
		path := []string{"X1-AA"}
		for i := 1; i < hops; i++ {
			path = append(path, fmt.Sprintf("X1-MID%d", i))
		}
		return gatedChain(append(path, "X1-BB")...)
	}

	// A lane at the bound pays 2×laneSpanHops crossings, so a shallow fixture would be refused by the
	// FEE and never consult the bound at all — the second leg's zero would prove nothing.
	const deep = 200
	headroom := int64((shallowestCrossSystemSpread - trading.MinBidMargin) * deep)
	require.Greater(t, headroom, int64(2*laneSpanHops)*domainSensing.DefaultGateFeeCredits,
		"calibration: at the bound even the thinnest of these lanes out-earns its round trip")

	count, _, err := countOverRealGateGraph(t, crossSystemFuelSurfaceOfDepth(t, deep),
		&laneGateStore{adjacency: chain(laneSpanHops)}, "X1-AA", "X1-BB")
	require.NoError(t, err)
	require.Equal(t, 4, count, "calibration: a sink exactly at the span is still work")

	count, readable, err := countOverRealGateGraph(t, crossSystemFuelSurfaceOfDepth(t, deep),
		&laneGateStore{adjacency: chain(laneSpanHops + 1)}, "X1-AA", "X1-BB")
	require.NoError(t, err)
	require.True(t, readable)
	require.Zero(t, count, "one hop past the widest pool, no scan holds both ends and the lane is not counted")

	// The ferry bound is where this test used to sit. The same lanes are still gated, still deep
	// enough, and still refused — so the tightening is the bound moving, not the fixture running out.
	count, readable, err = countOverRealGateGraph(t, crossSystemFuelSurfaceOfDepth(t, deep),
		&laneGateStore{adjacency: chain(gategraph.MaxJumpPath)}, "X1-AA", "X1-BB")
	require.NoError(t, err)
	require.True(t, readable)
	require.Zero(t, count, "reachable for a ferry is not selectable for a circuit")
}

// RULINGS #4: an unreadable gate store is not evidence of reachability. The real service surfaces
// it as an error and the census fails the whole count CLOSED.
func TestCountProfitableLanes_RealGateGraph_StoreFailureFailsClosed(t *testing.T) {
	count, readable, err := countOverRealGateGraph(t, crossSystemFuelSurface(t),
		&laneGateStore{adjErr: errors.New("gate store down")}, "X1-AA", "X1-BB")
	require.Error(t, err)
	require.False(t, readable)
	require.Zero(t, count)
}

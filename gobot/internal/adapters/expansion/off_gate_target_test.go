package expansion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// fakeUniverseProvider serves a fixed universe roster to the selector.
type fakeUniverseProvider struct {
	systems []system.SystemAPIData
	err     error
}

func (f *fakeUniverseProvider) AllSystems(context.Context, int) ([]system.SystemAPIData, error) {
	return f.systems, f.err
}

// fakeAdjacency serves a fixed gate adjacency — its key set (plus edge targets) is the
// gate-connected graph the off-gate enumeration subtracts from the universe.
type fakeAdjacency struct {
	adj map[string][]system.GateEdge
	err error
}

func (f *fakeAdjacency) Adjacency(context.Context) (map[string][]system.GateEdge, error) {
	return f.adj, f.err
}

func selectParams() expansionCmd.OffGateSelectionParams {
	return expansionCmd.OffGateSelectionParams{WarpRangeFuel: 100, ValueWeight: 10, FuelWeight: 1}
}

// TestOffGateSelect_EnumeratesOffGateSystemsOnly pins the sp-k645 off-gate enumeration
// (test 1): a universe system NOT in the gate-connected graph is a candidate; a
// gate-connected system NEVER is — even when it sits nearer the frontier edge. The gate
// graph's key set plus its edge targets are the on-network systems; everything else in the
// universe roster is off-gate.
func TestOffGateSelect_EnumeratesOffGateSystemsOnly(t *testing.T) {
	universe := &fakeUniverseProvider{systems: []system.SystemAPIData{
		{Symbol: "X1-EDGE", Type: "BLUE_STAR", X: 0, Y: 0},        // gate-connected (frontier edge, nearest to X1-OFF)
		{Symbol: "X1-INGRAPH", Type: "BLUE_STAR", X: 100, Y: 100}, // gate-connected (edge target), far — must never be picked
		{Symbol: "X1-OFF", Type: "BLACK_HOLE", X: 3, Y: 4},        // off-gate — the only candidate
	}}
	gate := &fakeAdjacency{adj: map[string][]system.GateEdge{
		"X1-EDGE": {{ConnectedSystem: "X1-INGRAPH"}}, // both X1-EDGE and X1-INGRAPH are on the gate network
	}}
	sel := NewOffGateWarpTargetSelector(universe, gate)

	target, found, err := sel.SelectTarget(context.Background(), 1, selectParams())
	require.NoError(t, err)
	require.True(t, found, "the off-gate system is a warp candidate")
	require.Equal(t, "X1-OFF", target.SystemSymbol, "only the OFF-gate system is selected")
	require.Equal(t, "X1-EDGE", target.FromSystem, "the warp launches from the nearest gate-connected frontier edge")
	require.Equal(t, 5, target.WarpFuelCost, "warp fuel is slice A's CRUISE cost of the 3-4-5 leg")
}

// TestOffGateSelect_PicksNearestHighestValueWithinRange pins target selection (test 2): among
// off-gate candidates within warp range, the score trades exploration value against warp-fuel
// distance from the frontier edge — a nearer system beats a farther one at equal value, and a
// promising-type system beats a barren-type one at equal distance.
func TestOffGateSelect_PicksNearestHighestValueWithinRange(t *testing.T) {
	gate := &fakeAdjacency{adj: map[string][]system.GateEdge{"X1-EDGE": {{ConnectedSystem: "X1-NBR"}}}}

	t.Run("nearer wins at equal value", func(t *testing.T) {
		universe := &fakeUniverseProvider{systems: []system.SystemAPIData{
			{Symbol: "X1-EDGE", Type: "BLUE_STAR", X: 0, Y: 0},
			{Symbol: "X1-NEAR", Type: "BLACK_HOLE", X: 3, Y: 4}, // fuel 5
			{Symbol: "X1-FAR", Type: "BLACK_HOLE", X: 6, Y: 8},  // fuel 10 — same (barren) type
		}}
		sel := NewOffGateWarpTargetSelector(universe, gate)
		target, found, err := sel.SelectTarget(context.Background(), 1, selectParams())
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "X1-NEAR", target.SystemSymbol, "nearer off-gate system wins at equal exploration value")
	})

	t.Run("promising type wins at equal distance", func(t *testing.T) {
		universe := &fakeUniverseProvider{systems: []system.SystemAPIData{
			{Symbol: "X1-EDGE", Type: "BLUE_STAR", X: 0, Y: 0},
			{Symbol: "X1-STAR", Type: "ORANGE_STAR", X: 3, Y: 4}, // fuel 5, promising
			{Symbol: "X1-HOLE", Type: "BLACK_HOLE", X: 0, Y: 5},  // fuel 5, barren — same distance
		}}
		sel := NewOffGateWarpTargetSelector(universe, gate)
		target, found, err := sel.SelectTarget(context.Background(), 1, selectParams())
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "X1-STAR", target.SystemSymbol, "promising-type off-gate system wins at equal warp distance")
	})
}

// TestOffGateSelect_ExcludesOutOfWarpRange pins the warp-range bound: an off-gate system whose
// nearest-edge warp leg costs more fuel than the range is excluded; a nearer in-range one is
// still selected, and when EVERY off-gate system is out of range no target is found.
func TestOffGateSelect_ExcludesOutOfWarpRange(t *testing.T) {
	gate := &fakeAdjacency{adj: map[string][]system.GateEdge{"X1-EDGE": {{ConnectedSystem: "X1-NBR"}}}}
	params := expansionCmd.OffGateSelectionParams{WarpRangeFuel: 6, ValueWeight: 10, FuelWeight: 1}

	t.Run("out-of-range candidate excluded, in-range selected", func(t *testing.T) {
		universe := &fakeUniverseProvider{systems: []system.SystemAPIData{
			{Symbol: "X1-EDGE", Type: "BLUE_STAR", X: 0, Y: 0},
			{Symbol: "X1-INRANGE", Type: "BLACK_HOLE", X: 3, Y: 4}, // fuel 5 <= 6
			{Symbol: "X1-OUT", Type: "ORANGE_STAR", X: 30, Y: 40},  // fuel 50 > 6 — excluded despite promising
		}}
		sel := NewOffGateWarpTargetSelector(universe, gate)
		target, found, err := sel.SelectTarget(context.Background(), 1, params)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "X1-INRANGE", target.SystemSymbol, "the out-of-range promising system is excluded")
	})

	t.Run("all out of range → no target", func(t *testing.T) {
		universe := &fakeUniverseProvider{systems: []system.SystemAPIData{
			{Symbol: "X1-EDGE", Type: "BLUE_STAR", X: 0, Y: 0},
			{Symbol: "X1-OUT", Type: "ORANGE_STAR", X: 30, Y: 40}, // fuel 50 > 6
		}}
		sel := NewOffGateWarpTargetSelector(universe, gate)
		_, found, err := sel.SelectTarget(context.Background(), 1, params)
		require.NoError(t, err)
		require.False(t, found, "no off-gate system within warp range → no target")
	})
}

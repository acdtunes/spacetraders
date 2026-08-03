package expansion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
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

func selectParams() parkedsensing.OffGateSelectionParams {
	return parkedsensing.OffGateSelectionParams{WarpRangeFuel: 100, ValueWeight: 10, FuelWeight: 1}
}

// TestOffGateSelect_EnumeratesOffGateSystemsOnly pins the off-gate enumeration
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

// TestOffGateSelect_AnUnbuiltGateConnectsNothing pins the classification that made the whole
// off-gate slice blind. GateEdge.UnderConstruction means the NEIGHBOUR's own gate is still being
// built, and the domain type says outright that a route "must never traverse INTO such an edge".
// Counting that neighbour as on-network is exactly backwards: a system whose only inbound gate can
// never be used is the single most valuable warp target there is. Measured live, every one of the
// 53 exits from this fleet's pocket is an under-construction edge, so the entire ring beyond the
// wall read as "already connected" and the selector could not see the systems it exists to reach.
//
// The four rows are the complete edge state space, because the answer turns on TWO flags and the
// interesting cases are the mixed ones. STALE means UnderConstruction is UNVERIFIED, so an unbuilt
// verdict cannot be trusted and the neighbour stays on-network — the same refusal the escape
// reader, the chosen-path verify and the distance walk all make with a stale row. The open-gate row
// is the no-regression guard: a neighbour across a gate that EXISTS must stay excluded, or the
// fleet warps to somewhere it could have walked.
//
// The fixture is one key and one neighbour across a single edge and nothing else, so the ONLY
// thing that can decide the verdict is that edge's build state.
func TestOffGateSelect_AnUnbuiltGateConnectsNothing(t *testing.T) {
	universe := &fakeUniverseProvider{systems: []system.SystemAPIData{
		{Symbol: "X1-KEY", Type: "BLUE_STAR", X: 0, Y: 0},   // our side of the wall
		{Symbol: "X1-WALL", Type: "BLACK_HOLE", X: 3, Y: 4}, // the far side, reachable only through that edge
	}}

	cases := []struct {
		name              string
		underConstruction bool
		stale             bool
		selectable        bool
		why               string
	}{
		{
			name:       "an open gate keeps its neighbour on-network",
			selectable: false,
			why:        "a system reachable through a gate that EXISTS is never a warp target — warp must not compete with a walk",
		},
		{
			name:              "a VERIFIED under-construction gate leaves its neighbour off-gate",
			underConstruction: true,
			selectable:        true,
			why:               "an unbuilt gate connects nothing, so the system behind it is off-gate and the best target there is",
		},
		{
			name:              "a STALE under-construction gate is not a verified verdict",
			underConstruction: true,
			stale:             true,
			selectable:        false,
			why:               "stale means the unbuilt verdict is UNVERIFIED, and warping on an unverified verdict is the expensive way to be wrong",
		},
		{
			name:       "a stale open gate is unverified in the same way",
			stale:      true,
			selectable: false,
			why:        "a stale row is never an authoritative verdict anywhere in this codebase, and it is not one here",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := &fakeAdjacency{adj: map[string][]system.GateEdge{
				"X1-KEY": {{
					ConnectedSystem:   "X1-WALL",
					GateWaypoint:      "X1-WALL-I51",
					UnderConstruction: tc.underConstruction,
					Stale:             tc.stale,
				}},
			}}
			sel := NewOffGateWarpTargetSelector(universe, gate)

			target, found, err := sel.SelectTarget(context.Background(), 1, selectParams())
			require.NoError(t, err)
			require.Equal(t, tc.selectable, found, tc.why)
			if tc.selectable {
				require.Equal(t, "X1-WALL", target.SystemSymbol)
				require.Equal(t, "X1-KEY", target.FromSystem, "the warp launches from the frontier key on our side of the wall")
			}
		})
	}
}

// TestOffGateSelect_AnAdjacencyKeyStaysOnNetworkBehindItsOwnUnbuiltGates pins the other half of the
// classification. An adjacency KEY is on our network however unbuilt its ONWARD gates are — the key
// exists because we successfully read that system's own jump gate, which is the same fact the
// gate-walking machinery treats as membership.
//
// X1-WALLED is that case: a key whose every edge is verified under construction. It also sits
// NEAREST the frontier and is cheap to reach, so if the key rule were dropped it would outscore the
// genuine off-gate candidate and the fleet would spend a warp on a system it can already walk to.
// That is what makes this fixture able to fail rather than merely able to pass.
func TestOffGateSelect_AnAdjacencyKeyStaysOnNetworkBehindItsOwnUnbuiltGates(t *testing.T) {
	universe := &fakeUniverseProvider{systems: []system.SystemAPIData{
		{Symbol: "X1-HOME", Type: "BLACK_HOLE", X: 0, Y: 0},      // key, open gate onward
		{Symbol: "X1-NBR", Type: "BLUE_STAR", X: 0, Y: 2},        // on-network across that open gate
		{Symbol: "X1-WALLED", Type: "BLACK_HOLE", X: 0, Y: 1},    // key; every onward gate unbuilt; NEAREST of all
		{Symbol: "X1-OFF", Type: "BLACK_HOLE", X: 0, Y: 10},      // the one genuinely off-network system
		{Symbol: "X1-BEYOND", Type: "ORANGE_STAR", X: 0, Y: 500}, // past the wall, out of warp range
	}}
	gate := &fakeAdjacency{adj: map[string][]system.GateEdge{
		"X1-HOME":   {{ConnectedSystem: "X1-NBR", GateWaypoint: "X1-NBR-I51"}},
		"X1-WALLED": {{ConnectedSystem: "X1-BEYOND", GateWaypoint: "X1-BEYOND-I51", UnderConstruction: true}},
	}}
	sel := NewOffGateWarpTargetSelector(universe, gate)

	target, found, err := sel.SelectTarget(context.Background(), 1, selectParams())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "X1-OFF", target.SystemSymbol,
		"a walled-in KEY is still on our network; only the genuinely off-network system is a warp target")
}

// TestOffGateSelect_ExcludesOutOfWarpRange pins the warp-range bound: an off-gate system whose
// nearest-edge warp leg costs more fuel than the range is excluded; a nearer in-range one is
// still selected, and when EVERY off-gate system is out of range no target is found.
func TestOffGateSelect_ExcludesOutOfWarpRange(t *testing.T) {
	gate := &fakeAdjacency{adj: map[string][]system.GateEdge{"X1-EDGE": {{ConnectedSystem: "X1-NBR"}}}}
	params := parkedsensing.OffGateSelectionParams{WarpRangeFuel: 6, ValueWeight: 10, FuelWeight: 1}

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

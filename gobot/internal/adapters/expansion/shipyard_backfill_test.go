package expansion

// Unit tests for the backfill charted-shipyard enumerator: it INTERSECTS the
// era-agnostic SHIPYARD-trait set with the current gate-reachable frontier, so it
// surfaces exactly the known-shipyard systems a probe could be relayed to, each tagged
// with its hop depth (the deeper-first key). Doubles at both driven ports (the frontier
// candidate reach + the waypoint trait lister).

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeCandidateLister (the gate-reachable frontier reach) is shared with
// shipyard_coverage_test.go in this package.

type fakeShipyardWaypointLister struct {
	waypoints []*shared.Waypoint
}

func (f *fakeShipyardWaypointLister) ListWithTrait(context.Context, string) ([]*shared.Waypoint, error) {
	return f.waypoints, nil
}

func yardWaypoint(symbol string) *shared.Waypoint {
	return &shared.Waypoint{Symbol: symbol, SystemSymbol: shared.ExtractSystemSymbol(symbol), Traits: []string{"SHIPYARD"}}
}

// GIVEN three known-shipyard systems, one of which is NOT currently gate-reachable, plus a
// reachable system with NO shipyard
// WHEN the enumerator lists charted shipyards
// THEN it returns only the reachable known-shipyard systems, each carrying its hop depth and
// a representative shipyard waypoint — the unreachable yard and the non-shipyard are omitted.
func TestChartedShipyardEnumerator_IntersectsReachableWithShipyardTrait(t *testing.T) {
	waypoints := []*shared.Waypoint{
		yardWaypoint("X1-NEAR-Y1"),
		yardWaypoint("X1-FAR-Y1"),
		yardWaypoint("X1-GONE-Y1"), // known shipyard, but not in the current reachable set
	}
	candidates := []expansionCmd.ExpansionCandidate{
		{SystemSymbol: "X1-NEAR", Hops: 2},
		{SystemSymbol: "X1-FAR", Hops: 7},
		{SystemSymbol: "X1-NOYARD", Hops: 3}, // reachable but holds no shipyard
	}
	enum := NewChartedShipyardEnumerator(
		&fakeCandidateLister{candidates: candidates},
		&fakeShipyardWaypointLister{waypoints: waypoints},
	)

	got, err := enum.ChartedShipyardSystems(context.Background(), 1, 12)
	require.NoError(t, err)

	bySystem := map[string]int{}
	yardBySystem := map[string]string{}
	for _, g := range got {
		bySystem[g.SystemSymbol] = g.Hops
		yardBySystem[g.SystemSymbol] = g.ShipyardWaypoint
	}
	require.Len(t, got, 2, "only reachable known-shipyard systems are enumerated")
	require.Equal(t, 2, bySystem["X1-NEAR"], "hops must be carried through from the reachable frontier")
	require.Equal(t, 7, bySystem["X1-FAR"])
	require.Equal(t, "X1-FAR-Y1", yardBySystem["X1-FAR"], "the representative shipyard waypoint must be surfaced")
	require.NotContains(t, bySystem, "X1-GONE", "a known shipyard not in the current reachable set is omitted (dead/unreachable)")
	require.NotContains(t, bySystem, "X1-NOYARD", "a reachable system with no shipyard is not a backfill target")
}

// Several shipyards in ONE system collapse to a single deterministic representative waypoint
// (the smallest symbol), so the enumeration is stable and never double-dispatches a system.
func TestChartedShipyardEnumerator_OneRepresentativeYardPerSystem(t *testing.T) {
	waypoints := []*shared.Waypoint{
		yardWaypoint("X1-MULTI-Y3"),
		yardWaypoint("X1-MULTI-Y1"),
		yardWaypoint("X1-MULTI-Y2"),
	}
	enum := NewChartedShipyardEnumerator(
		&fakeCandidateLister{candidates: []expansionCmd.ExpansionCandidate{{SystemSymbol: "X1-MULTI", Hops: 4}}},
		&fakeShipyardWaypointLister{waypoints: waypoints},
	)

	got, err := enum.ChartedShipyardSystems(context.Background(), 1, 12)
	require.NoError(t, err)
	require.Len(t, got, 1, "a system with several shipyards is one backfill target")
	require.Equal(t, "X1-MULTI-Y1", got[0].ShipyardWaypoint, "the deterministic representative is the smallest waypoint symbol")
}

// The enumerator honors the caller-supplied REACH — it does not bake in a shallow
// bound. A CHARTED shipyard sitting DEEP in the gate graph (hop depth 5-20, past the old ~3
// reposition bound) is in-graph + relay-reachable, so a WIDE reach must enumerate it; a shallow
// reach drops it. This is the exact blind spot: the deep in-graph charted yards were invisible
// because the reach was too small, not because they were unreachable.
func TestChartedShipyardEnumerator_HonorsCallerReach_DeepInGraphYardsAtWideReachOnly(t *testing.T) {
	waypoints := []*shared.Waypoint{
		yardWaypoint("X1-SHALLOW-Y1"),
		yardWaypoint("X1-DEEP5-Y1"),
		yardWaypoint("X1-DEEP8-Y1"),
		yardWaypoint("X1-DEEP20-Y1"),
	}
	candidates := []expansionCmd.ExpansionCandidate{
		{SystemSymbol: "X1-SHALLOW", Hops: 2},
		{SystemSymbol: "X1-DEEP5", Hops: 5},
		{SystemSymbol: "X1-DEEP8", Hops: 8},
		{SystemSymbol: "X1-DEEP20", Hops: 20},
	}
	scanner := &fakeCandidateLister{candidates: candidates}
	enum := NewChartedShipyardEnumerator(scanner, &fakeShipyardWaypointLister{waypoints: waypoints})

	// WIDE reach (full-graph default): every in-graph charted shipyard, however deep, enumerated.
	wide, err := enum.ChartedShipyardSystems(context.Background(), 1, 1000)
	require.NoError(t, err)
	require.Equal(t, 1000, scanner.gotMaxHops, "the caller's reach is passed straight through to the frontier scanner")
	require.ElementsMatch(t,
		[]string{"X1-SHALLOW", "X1-DEEP5", "X1-DEEP8", "X1-DEEP20"}, enumeratedSystems(wide),
		"a wide reach enumerates every in-graph charted shipyard, including the deep ones")

	// SHALLOW reach (the old ~3 bound): the deep in-graph yards are dropped.
	shallow, err := enum.ChartedShipyardSystems(context.Background(), 1, 3)
	require.NoError(t, err)
	require.Equal(t, []string{"X1-SHALLOW"}, enumeratedSystems(shallow),
		"a shallow reach drops the deep in-graph charted shipyards — the reach, not reachability, was the gap")
}

func enumeratedSystems(systems []scoutingCmd.ChartedShipyardSystem) []string {
	out := make([]string, 0, len(systems))
	for _, s := range systems {
		out = append(out, s.SystemSymbol)
	}
	return out
}

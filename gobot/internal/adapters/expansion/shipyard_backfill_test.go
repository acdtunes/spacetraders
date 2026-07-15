package expansion

// Unit tests for the sp-rhju backfill charted-shipyard enumerator: it INTERSECTS the
// era-agnostic SHIPYARD-trait set with the current gate-reachable frontier, so it
// surfaces exactly the known-shipyard systems a probe could be relayed to, each tagged
// with its hop depth (the deeper-first key). Doubles at both driven ports (the frontier
// candidate reach + the waypoint trait lister).

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
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
		12,
	)

	got, err := enum.ChartedShipyardSystems(context.Background(), 1)
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
		12,
	)

	got, err := enum.ChartedShipyardSystems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, got, 1, "a system with several shipyards is one backfill target")
	require.Equal(t, "X1-MULTI-Y1", got[0].ShipyardWaypoint, "the deterministic representative is the smallest waypoint symbol")
}

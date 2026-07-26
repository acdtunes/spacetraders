package commands

// Stage-2 stamping at the coordinator: every standing post carries the sorted
// hot set — the waypoints dealing in ≥1 whitelisted good — derived from the
// same census rows that scoped the post, and rewritten ONLY when the set
// changes (the delta-write discipline: a converged tick writes zero rows).

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// hotWaypointsFor mirrors richRows' waypoint naming: the sorted hot set the
// coordinator derives for a system whose census is richRows(system, n).
func hotWaypointsFor(system string, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("%s-W%d", system, i))
	}
	sort.Strings(out)
	return out
}

// hotSensingPost is sensingPost with a census-true hot set: the fixture for a
// CONVERGED standing post under richRows(system, hotN).
func hotSensingPost(system string, hulls, hotN int) *domainScouting.ScoutPost {
	post := sensingPost(system, hulls)
	post.HotWaypoints = hotWaypointsFor(system, hotN)
	return post
}

// The first declaration carries the hot set immediately (the census already
// knows it), sorted ascending regardless of census row order.
func TestSensing_DeclarationStampsSortedHotWaypoints(t *testing.T) {
	rows := []domainScouting.MarketDepthRow{
		depthRow("X1-AA1", "X1-AA1-W2", "CLOTHING", 60, 40_000),
		depthRow("X1-AA1", "X1-AA1-W0", "CLOTHING", 60, 40_000),
		depthRow("X1-AA1", "X1-AA1-W1", "CLOTHING", 60, 40_000),
	}
	dr := &fakeDepthReader{rows: rows}
	pr := newSensingPostRepo()
	h, _ := newSensingHandler(dr, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Len(t, pr.upserts, 1)
	require.Equal(t, []string{"X1-AA1-W0", "X1-AA1-W1", "X1-AA1-W2"}, pr.upserts[0].HotWaypoints,
		"declared sorted asc whatever order the census served the rows")
}

// The hot set obeys the delta-write discipline: once a post carries the
// census-current set, a converged tick writes NOTHING — steady state costs
// zero rows.
func TestSensing_HotSetSteadyStateWritesNothing(t *testing.T) {
	dr := &fakeDepthReader{rows: richRows("X1-AA1", 3)}
	pr := newSensingPostRepo()
	h, _ := newSensingHandler(dr, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))
	require.Len(t, pr.upserts, 1, "tick 1 declares the post with its hot set")
	require.Equal(t, hotWaypointsFor("X1-AA1", 3), pr.upserts[0].HotWaypoints)

	pr.upserts = nil
	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))
	require.Empty(t, pr.upserts, "tick 2 is converged — an unchanged hot set writes zero rows")
}

// A hot-set CHANGE alone — hulls and dormancy untouched — is a delta and
// writes exactly one row carrying the new sorted set.
func TestSensing_HotSetChangeWritesExactlyOneDelta(t *testing.T) {
	post := sensingPost("X1-AA1", 1)
	post.HotWaypoints = hotWaypointsFor("X1-AA1", 2)
	pr := newSensingPostRepo(post)
	dr := &fakeDepthReader{rows: richRows("X1-AA1", 3)} // a third market went hot
	h, _ := newSensingHandler(dr, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Empty(t, pr.upserts, "a hot-set delta is a narrow write, never a full-row Upsert")
	require.Len(t, pr.stateWrites, 1, "only the hot set changed — exactly one delta write")
	require.Equal(t, hotWaypointsFor("X1-AA1", 3), pr.stateWrites[0].hotWaypoints)
	require.Equal(t, 1, pr.stateWrites[0].hulls, "the delta carries the unchanged hull budget")
	require.False(t, pr.stateWrites[0].dormant)
}

// The stage-2 safety property end-to-end: a garbled observation (zero volume,
// zero price) of a whitelisted good still stamps its waypoint hot — selection
// is by what the market DEALS IN, never what it is currently worth.
func TestSensing_GarbledPriceRowStaysInStampedHotSet(t *testing.T) {
	rows := append(richRows("X1-AA1", 1), depthRow("X1-AA1", "X1-AA1-W9", "CLOTHING", 0, 0))
	dr := &fakeDepthReader{rows: rows}
	pr := newSensingPostRepo()
	h, _ := newSensingHandler(dr, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Len(t, pr.upserts, 1)
	require.Equal(t, []string{"X1-AA1-W0", "X1-AA1-W9"}, pr.upserts[0].HotWaypoints,
		"the crushed/garbled market still deals CLOTHING — no price term may drop it from the circuit")
}

// A MinHulls-floored post that fell out of sensing scope is KEPT (never
// removed) and its hot set stays census-true: a stale restriction on the home
// post would blind exactly the markets stage 2 exists to watch.
func TestSensing_FlooredOutOfScopePostHotSetRefreshed(t *testing.T) {
	t.Run("stale set is rewritten to the census", func(t *testing.T) {
		post := sensingPost("X1-HOME", 2)
		post.MinHulls = 2
		post.HotWaypoints = []string{"X1-HOME-W9"} // stale: that market no longer deals whitelisted goods
		pr := newSensingPostRepo(post)
		dr := &fakeDepthReader{rows: thinRows("X1-HOME")} // below the depth floor ⇒ out of scope
		h, _ := newSensingHandler(dr, pr, calmFleet(t), &fakePressure{})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Empty(t, pr.removed, "a floored post is never removed")
		require.Empty(t, pr.upserts, "the refresh is a narrow delta, never a full-row Upsert")
		require.Len(t, pr.stateWrites, 1)
		require.Equal(t, []string{"X1-HOME-W0"}, pr.stateWrites[0].hotWaypoints)
		require.Equal(t, 2, pr.stateWrites[0].hulls, "the refresh preserves the hull budget")
		require.False(t, pr.stateWrites[0].dormant)
		require.Equal(t, 2, pr.find("X1-HOME").MinHulls, "the narrow delta never touches the floor column")
	})

	t.Run("census-true set writes nothing", func(t *testing.T) {
		post := sensingPost("X1-HOME", 2)
		post.MinHulls = 2
		post.HotWaypoints = []string{"X1-HOME-W0"}
		pr := newSensingPostRepo(post)
		dr := &fakeDepthReader{rows: thinRows("X1-HOME")}
		h, _ := newSensingHandler(dr, pr, calmFleet(t), &fakePressure{})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Empty(t, pr.removed)
		require.Empty(t, pr.upserts, "converged floored post ⇒ zero writes")
		require.Empty(t, pr.stateWrites)
	})
}

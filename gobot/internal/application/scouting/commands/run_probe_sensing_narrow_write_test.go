package commands

// The narrow-seam invariant (I1): every LIVE-post delta the sensing coordinator
// writes — resize, dormancy flip, hot-set stamp, freshness refresh — must go
// through the four-column UpdateSensingState, never a full-row Upsert of the
// tick-start snapshot. Under saturation RotateDormant flips every in-scope post's dormant
// bit EVERY tick, so a full-row write is a per-tick clobber surface over the
// manning columns the scout reconciler writes concurrently and the min_hulls
// floor bootstrap flips exactly once behind an in-memory latch (a revert it
// would never repair). Each test emulates the race with the afterList seam: the
// concurrent write lands between the coordinator's read and its delta write.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A hot-set delta on an in-scope post must not clobber a manning assignment the
// scout reconciler wrote after the coordinator's snapshot.
func TestSensing_LiveDeltaPreservesConcurrentManningWrite(t *testing.T) {
	post := hotSensingPost("X1-AA1", 1, 2) // stale hot set: census now has 3 hot markets
	pr := newSensingPostRepo(post)
	pr.afterList = func() {
		manned := pr.find("X1-AA1")
		manned.AssignedHull = "PROBE-A"
		manned.TourContainerID = "tour-11"
	}
	h, _ := newSensingHandler(&fakeDepthReader{rows: richRows("X1-AA1", 3)}, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	got := pr.find("X1-AA1")
	require.Equal(t, "PROBE-A", got.AssignedHull,
		"the reconciler's manning write must survive the sensing delta (narrow columns only)")
	require.Equal(t, "tour-11", got.TourContainerID)
	require.Equal(t, hotWaypointsFor("X1-AA1", 3), got.HotWaypoints, "the delta itself must land")
	require.Empty(t, pr.upsertsFor("X1-AA1"), "a live-post delta is never a full-row Upsert")
	writes := pr.stateWritesFor("X1-AA1")
	require.Len(t, writes, 1, "exactly one narrow delta write")
	require.Equal(t, 1, writes[0].hulls)
	require.False(t, writes[0].dormant)
	require.Equal(t, time.Hour, writes[0].freshnessTarget, "every live-post delta re-stamps the config target ")
}

// The T9 revert scenario: bootstrap's hand-off drops the home post's MinHulls
// 3→1 exactly once (in-memory once-latch — never re-stamped). A sensing delta
// written from a pre-flip snapshot must not revert the floor.
func TestSensing_LiveDeltaPreservesMinHullsFlip(t *testing.T) {
	home := sensingPost("X1-HOME", 3)
	home.MinHulls = 3
	home.HotWaypoints = hotWaypointsFor("X1-HOME", 2) // stale: a third market went hot
	pr := newSensingPostRepo(home)
	pr.afterList = func() {
		pr.find("X1-HOME").MinHulls = 1 // the hand-off releases the reinforcement
	}
	h, _ := newSensingHandler(&fakeDepthReader{rows: richRows("X1-HOME", 3)}, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	got := pr.find("X1-HOME")
	require.Equal(t, 1, got.MinHulls,
		"the once-latched MinHulls flip must survive the sensing delta — a revert would re-pin the home reinforcement forever")
	require.Equal(t, hotWaypointsFor("X1-HOME", 3), got.HotWaypoints)
	require.Empty(t, pr.upsertsFor("X1-HOME"))
}

// The floored-keep path (an out-of-scope MinHulls-floored post, woken and
// hot-refreshed) is a live-post delta too: same narrow seam, same preservation.
func TestSensing_FlooredKeepPathPreservesConcurrentWrites(t *testing.T) {
	home := sensingPost("X1-HOME", 2)
	home.MinHulls = 2
	home.Dormant = true
	home.HotWaypoints = []string{"X1-HOME-W9"} // stale restriction
	pr := newSensingPostRepo(home)
	pr.afterList = func() {
		woken := pr.find("X1-HOME")
		woken.AssignedHull = "PROBE-H"
		woken.TourContainerID = "tour-77"
	}
	// HOME is census-visible but below the depth floor ⇒ out of scope ⇒ kept.
	h, _ := newSensingHandler(&fakeDepthReader{rows: thinRows("X1-HOME")}, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	got := pr.find("X1-HOME")
	require.Equal(t, "PROBE-H", got.AssignedHull, "the wake write must not clobber the concurrent manning")
	require.Equal(t, "tour-77", got.TourContainerID)
	require.False(t, got.Dormant, "the kept post is woken")
	require.Equal(t, []string{"X1-HOME-W0"}, got.HotWaypoints, "the kept post's hot set is held census-true")
	require.Equal(t, 2, got.MinHulls)
	require.Empty(t, pr.upsertsFor("X1-HOME"), "the wake write is a narrow delta, never a full-row Upsert")
	writes := pr.stateWritesFor("X1-HOME")
	require.Len(t, writes, 1)
	require.Equal(t, 2, writes[0].hulls, "the wake write never shrinks the post")
}

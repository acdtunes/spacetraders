package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE COLD-START TOUR GROWS TO THE RAMP. The probe fleet reaches probe_target over several
// ticks, so the post is first cut on whatever probes exist and must then take in the rest.
// These pin the two live reads that decide it — which probes count as untoured, and which
// hulls the re-cut is allowed to partition.

// homeProbe builds an idle satellite parked at a home waypoint.
func homeProbe(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	return homeReaderShip(t, symbol, waypoint, "SATELLITE", "")
}

// --- THE DISCRIMINATOR: grown fleet vs ended tour ---

// A ramp and a partial loss are the SAME numbers at fleet level (probes at target, one
// flying): what separates them is whether the parked hulls have ever been out. Only the
// probe that never has is grown to.
func TestCountUntouredHomeProbes_SeparatesAGrownFleetFromAnEndedTour(t *testing.T) {
	bought := homeProbe(t, "TORWIND-3", "X1-HQ-A1")   // the ramp just bought it
	returned := homeProbe(t, "TORWIND-4", "X1-HQ-B2") // its tour ended; it is parked
	flying := homeProbe(t, "TORWIND-2", "X1-HQ-C3")   // still out
	require.NoError(t, flying.AssignToContainer("scout-tour-TORWIND-2", shared.NewRealClock()))

	toured := map[string]bool{"TORWIND-2": true, "TORWIND-4": true}

	got := countUntouredHomeProbes([]*navigation.Ship{bought, returned, flying}, "X1-HQ", toured)
	require.Equal(t, 1, got, "only the hull the fleet GAINED counts; a hull whose tour ENDED is deliberately not re-manned")
}

// TERMINATION. Acting on this count re-partitions live tours, so it must only ever count
// hulls the tour can actually claim. A captain reservation, another op's claim, or a probe
// parked in a foreign system would never be handed a circuit — counting one would re-cut the
// post on every tick for the rest of cold start.
func TestCountUntouredHomeProbes_IgnoresHullsTheTourCanNeverClaim(t *testing.T) {
	reserved := homeProbe(t, "TORWIND-5", "X1-HQ-A1")
	require.NoError(t, reserved.ReserveByCaptain("manual errand", shared.NewRealClock()))
	borrowed := homeProbe(t, "TORWIND-6", "X1-HQ-A1")
	require.NoError(t, borrowed.AssignToContainer("some-other-op", shared.NewRealClock()))
	away := homeProbe(t, "TORWIND-7", "X1-FAR-A1")
	frigate := homeReaderShip(t, "TORWIND-1", "X1-HQ-A1", commandRole, "")

	ships := []*navigation.Ship{reserved, borrowed, away, frigate}
	require.Zero(t, countUntouredHomeProbes(ships, "X1-HQ", nil),
		"a hull the tour will never be given is not a hull the post is short of")
}

// The signal must survive the observation it is derived from: no history at all (a cold
// container table, or a restart that re-reads it) reads every home probe as untoured, which
// is what makes the first cut and the ramp re-cut the same code path.
func TestCountUntouredHomeProbes_NoHistoryCountsEveryIdleHomeProbe(t *testing.T) {
	ships := []*navigation.Ship{
		homeProbe(t, "TORWIND-2", "X1-HQ-A1"),
		homeProbe(t, "TORWIND-3", "X1-HQ-B2"),
		homeProbe(t, "TORWIND-4", "X1-HQ-C3"),
	}
	require.Equal(t, 3, countUntouredHomeProbes(ships, "X1-HQ", map[string]bool{}))
}

// --- THE PARTITION: which hulls the re-cut may take back ---

// THE INCUMBENT IS INCLUDED. ScoutMarkets re-partitions exactly the hulls it is handed, so a
// re-cut that named only the idle probes would leave the first probe flying the whole market
// set beside a partition of that same set across the newcomers — three overlapping copies
// instead of three disjoint thirds, which is the shape the partition exists to prevent.
func TestSelectHomeTourHulls_TakesBackTheHullsAlreadyOnItsOwnTour(t *testing.T) {
	flying := homeProbe(t, "TORWIND-2", "X1-HQ-A1")
	require.NoError(t, flying.AssignToContainer("scout-tour-TORWIND-2-x", shared.NewRealClock()))
	idleA := homeProbe(t, "TORWIND-3", "X1-HQ-B2")
	idleB := homeProbe(t, "TORWIND-4", "X1-HQ-C3")

	ourTours := map[string]bool{"scout-tour-TORWIND-2-x": true}

	got := selectHomeTourHulls([]*navigation.Ship{flying, idleA, idleB}, "X1-HQ", ourTours)
	require.Equal(t, []string{"TORWIND-2", "TORWIND-3", "TORWIND-4"}, got,
		"the whole home probe fleet must be partitioned in one cut, or the circuits overlap")
}

// RULINGS #7. Widening to the incumbent must not widen to anyone else's hull: a captain
// reservation carries no container id at all, and another op's claim is not this tour's to
// stop. Both are left where they are.
func TestSelectHomeTourHulls_NeverTakesAHullThatIsNotItsOwn(t *testing.T) {
	reserved := homeProbe(t, "TORWIND-5", "X1-HQ-A1")
	require.NoError(t, reserved.ReserveByCaptain("gate-supply errand", shared.NewRealClock()))
	borrowed := homeProbe(t, "TORWIND-6", "X1-HQ-A1")
	require.NoError(t, borrowed.AssignToContainer("sensing-worker-9", shared.NewRealClock()))
	away := homeProbe(t, "TORWIND-7", "X1-FAR-A1")
	frigate := homeReaderShip(t, "TORWIND-1", "X1-HQ-A1", commandRole, "")
	idle := homeProbe(t, "TORWIND-3", "X1-HQ-B2")

	ourTours := map[string]bool{"scout-tour-TORWIND-2-x": true}

	got := selectHomeTourHulls([]*navigation.Ship{reserved, borrowed, away, frigate, idle}, "X1-HQ", ourTours)
	require.Equal(t, []string{"TORWIND-3"}, got)
}

// --- THE YARD SENTINEL must never be swept into scouting ---
//
// The sentinel is protected via the SAME claim/assignment axis the test above already pins for ANY
// captain reservation — selectHomeTourHulls needs no code change at all. This test pins the EXACT
// reason string bootstrap's own buy+reserve uses (bootstrapCmd.YardSentinelReservationReason), so a
// future rename on either side (the buy in bootstrap_yard_sentinel.go, or the read here) is caught
// immediately instead of silently reopening the scouting-rotation leak this test exists to close.
func TestSelectHomeTourHulls_NeverTakesTheYardSentinel(t *testing.T) {
	sentinel := homeProbe(t, "SENTINEL-1", "X1-HQ-YARD")
	require.NoError(t, sentinel.ReserveByCaptain(bootstrapCmd.YardSentinelReservationReason, shared.NewRealClock()))
	idle := homeProbe(t, "TORWIND-3", "X1-HQ-B2")

	got := selectHomeTourHulls([]*navigation.Ship{sentinel, idle}, "X1-HQ", nil)
	require.Equal(t, []string{"TORWIND-3"}, got,
		"the yard sentinel must never be swept into the scouting rotation while it stands captain-reserved at the home shipyard")
}

// --- The two reads meet at the reconciler's guard ---

// END TO END over the observation the guard actually sees: the ramp's parked hulls make the
// post undersized, and the SAME fleet with its history intact does not. This is the pair the
// bug got wrong — it read the second answer for the first situation.
func TestBootstrapObservation_RampReadsUndersizedAndAnEndedTourDoesNot(t *testing.T) {
	satellite := homeProbe(t, "TORWIND-2", "X1-HQ-A1")
	require.NoError(t, satellite.AssignToContainer("scout-tour-TORWIND-2-x", shared.NewRealClock()))
	ships := []*navigation.Ship{
		satellite,
		homeProbe(t, "TORWIND-3", "X1-HQ-B2"),
		homeProbe(t, "TORWIND-4", "X1-HQ-C3"),
	}

	ramp := bootstrapCmd.Observation{HomeSystem: "X1-HQ", MarketsTotal: 27}
	observeFleetShape(ships, &ramp)
	ramp.ProbesUntoured = countUntouredHomeProbes(ships, "X1-HQ", map[string]bool{"TORWIND-2": true})
	require.Equal(t, 3, ramp.ProbeCount)
	require.Equal(t, 1, ramp.ProbesScouting)
	require.Equal(t, 2, ramp.ProbesUntoured, "the two hulls the ramp bought have never been out")

	ended := bootstrapCmd.Observation{HomeSystem: "X1-HQ", MarketsTotal: 27}
	observeFleetShape(ships, &ended)
	ended.ProbesUntoured = countUntouredHomeProbes(ships, "X1-HQ",
		map[string]bool{"TORWIND-2": true, "TORWIND-3": true, "TORWIND-4": true})
	require.Equal(t, ramp.ProbeCount, ended.ProbeCount, "the two situations are indistinguishable at fleet level...")
	require.Equal(t, ramp.ProbesScouting, ended.ProbesScouting, "...which is exactly why the history read is needed")
	require.Zero(t, ended.ProbesUntoured, "every hull has already had its circuit; it is not re-manned")
}

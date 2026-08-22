package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- THE ARITHMETIC INVARIANT ---
//
// observeFleetShape counts EVERY IsScoutType() hull toward obs.ProbeCount/ProbesScouting — the
// counter acquireProbesToTarget's own `need := probeTarget - obs.ProbeCount` loop reads. A yard
// sentinel bought under that SAME shared counter would silently steal one of the 3-probe scouting
// seed's own slots (buy the sentinel first, and the ramp then buys only 2 more "to target"), and once
// bought would mask a REAL scout lost later (2 real + 1 sentinel still reads "3, at target") from the
// existing replace-on-loss buy. These pin that neither happens: the sentinel is counted ONLY as
// obs.YardSentinelSymbol, never as a probe.

// yardSentinelShip builds an idle scout-type hull at waypoint, then reserves it for the captain with
// the sentinel's own reason — exactly the shape bootstrap's BuyAndReserve leaves behind.
func yardSentinelShip(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	ship := homeProbe(t, symbol, waypoint)
	require.NoError(t, ship.ReserveByCaptain(bootstrapCmd.YardSentinelReservationReason, shared.NewRealClock()))
	return ship
}

func TestObserveFleetShape_ExcludesYardSentinelFromProbeCount(t *testing.T) {
	ships := []*navigation.Ship{
		yardSentinelShip(t, "SENTINEL-1", "X1-HQ-YARD"),
		homeProbe(t, "TORWIND-2", "X1-HQ-A1"),
		homeProbe(t, "TORWIND-3", "X1-HQ-B2"),
		homeProbe(t, "TORWIND-4", "X1-HQ-C3"),
	}

	obs := bootstrapCmd.Observation{}
	observeFleetShape(ships, &obs)

	require.Equal(t, 3, obs.ProbeCount,
		"the sentinel must never count toward the 3-probe scouting seed's own target arithmetic")
	require.Equal(t, "SENTINEL-1", obs.YardSentinelSymbol)
}

// THE REPLACE-ON-LOSS SAFETY NET. acquireProbesToTarget re-triggers whenever obs.ProbeCount <
// probeTarget on ANY tick, including after steady state — that is how a lost real scout gets replaced.
// A shared counter that folded the sentinel in would read "2 real + 1 sentinel" as "3, still at
// target" and never re-buy the lost hull for the rest of cold start.
func TestObserveFleetShape_LostRealScoutStillReadsUnderTarget_EvenWithSentinelPresent(t *testing.T) {
	ships := []*navigation.Ship{
		yardSentinelShip(t, "SENTINEL-1", "X1-HQ-YARD"),
		homeProbe(t, "TORWIND-2", "X1-HQ-A1"),
		homeProbe(t, "TORWIND-3", "X1-HQ-B2"),
		// TORWIND-4 lost — only 2 real scouts remain, alongside the sentinel.
	}

	obs := bootstrapCmd.Observation{}
	observeFleetShape(ships, &obs)

	require.Equal(t, 2, obs.ProbeCount,
		"2 real scouts + 1 sentinel must read as 2, not 3 — the sentinel's presence must never mask a lost real scout from the replace-on-loss buy")
}

// A captain reservation for some OTHER reason (an operator's manual `ship reserve`, or a future
// unrelated errand) must NOT be mistaken for the yard sentinel — only the sentinel's own reason string
// identifies it, so an ordinary reservation still counts toward ProbeCount like any other non-idle
// scout (it is not scouting, but it is still a probe the fleet owns).
func TestObserveFleetShape_OrdinaryCaptainReservation_IsNotMistakenForTheSentinel(t *testing.T) {
	reserved := homeProbe(t, "TORWIND-5", "X1-HQ-A1")
	require.NoError(t, reserved.ReserveByCaptain("some other errand", shared.NewRealClock()))
	ships := []*navigation.Ship{reserved}

	obs := bootstrapCmd.Observation{}
	observeFleetShape(ships, &obs)

	require.Equal(t, 1, obs.ProbeCount, "an ordinary reservation is still a probe the fleet owns")
	require.Empty(t, obs.YardSentinelSymbol, "only the sentinel's OWN reason string may identify it")
}

// No sentinel bought yet this era ⇒ the zero value, never a stale/guessed symbol.
func TestObserveFleetShape_NoSentinelBought_YardSentinelSymbolEmpty(t *testing.T) {
	ships := []*navigation.Ship{
		homeProbe(t, "TORWIND-2", "X1-HQ-A1"),
		homeProbe(t, "TORWIND-3", "X1-HQ-B2"),
		homeProbe(t, "TORWIND-4", "X1-HQ-C3"),
	}

	obs := bootstrapCmd.Observation{}
	observeFleetShape(ships, &obs)

	require.Equal(t, 3, obs.ProbeCount)
	require.Empty(t, obs.YardSentinelSymbol)
}

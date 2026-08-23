package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
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

// --- EnsureParked: shares EnsureShipyardReadable's candidate selection, and re-evaluates every call ---
//
// EnsureParked had the same type-blind candidate pick as EnsureShipyardReadable, plus a second defect:
// it is a one-time purchase that parked permanently at whatever yard it first bought at and never
// reconsidered even once bootstrap's CURRENT purchasing need moved on to a type a different yard sells.
// These pin the fix: selectCandidateYard/yardCandidates now back EnsureParked too, and it is safe to call
// every tick (never a one-shot latch) so a docked hull can be redirected once its yard stops serving
// the current need.

// TestEnsureParked_RepositionsWhenDockedAtAYardConfirmedWrongForTheCurrentType: the sentinel is DOCKED
// exactly where it was bought — a yard that sells one type — while the current need has moved on to a
// type a DIFFERENT known yard sells. EnsureParked must redirect it, not read "docked at a shipyard" as
// "nothing to do."
func TestEnsureParked_RepositionsWhenDockedAtAYardConfirmedWrongForTheCurrentType(t *testing.T) {
	saved := &fakeSavedYards{rows: []domainShipyard.ShipTypeAvailability{
		{WaypointSymbol: "X1-MG48-C41", ShipType: "SHIP_PROBE", PurchasePrice: 40_000},
		{WaypointSymbol: "X1-MG48-A2", ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: 160_000},
	}}
	sentinel := shipyardHull(t, "TORWIND-5", "X1-MG48-C41", "", "SATELLITE", navigation.NavStatusDocked)
	med := &recordingBuyMediator{}
	c41, err := shared.NewWaypoint("X1-MG48-C41", 0, 0)
	require.NoError(t, err)
	a2, err := shared.NewWaypoint("X1-MG48-A2", 0, 0)
	require.NoError(t, err)
	acq := &bootstrapYardSentinelAcquirer{bootstrapAcquirer: &bootstrapAcquirer{
		med:          med,
		shipRepo:     &fakeAskShipRepo{all: []*navigation.Ship{sentinel}},
		waypointRepo: &fakeYardWaypoints{yards: []*shared.Waypoint{c41, a2}},
		savedYards:   saved,
	}}

	docked, err := acq.EnsureParked(context.Background(), 1, "X1-MG48", "SHIP_LIGHT_HAULER", "TORWIND-5")

	require.NoError(t, err)
	require.False(t, docked, "a redirect just issued a navigate — it is not YET docked at the new yard")
	require.Equal(t, []string{"X1-MG48-A2"}, navigateDestinations(med.sent),
		"STALE PLACEMENT: a sentinel docked at a yard confirmed wrong for the current need must be redirected, not left standing")
}

// TestEnsureParked_AlreadyAtAYardServingTheCurrentTypeIsANoOp is the control: once genuinely correctly
// positioned for the current need, re-checking every tick must cost nothing — no navigate, no dock.
func TestEnsureParked_AlreadyAtAYardServingTheCurrentTypeIsANoOp(t *testing.T) {
	saved := &fakeSavedYards{rows: []domainShipyard.ShipTypeAvailability{
		{WaypointSymbol: "X1-MG48-A2", ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: 160_000},
	}}
	sentinel := shipyardHull(t, "TORWIND-5", "X1-MG48-A2", "", "SATELLITE", navigation.NavStatusDocked)
	med := &recordingBuyMediator{}
	a2, err := shared.NewWaypoint("X1-MG48-A2", 0, 0)
	require.NoError(t, err)
	acq := &bootstrapYardSentinelAcquirer{bootstrapAcquirer: &bootstrapAcquirer{
		med:          med,
		shipRepo:     &fakeAskShipRepo{all: []*navigation.Ship{sentinel}},
		waypointRepo: &fakeYardWaypoints{yards: []*shared.Waypoint{a2}},
		savedYards:   saved,
	}}

	docked, err := acq.EnsureParked(context.Background(), 1, "X1-MG48", "SHIP_LIGHT_HAULER", "TORWIND-5")

	require.NoError(t, err)
	require.True(t, docked)
	require.Empty(t, navigateDestinations(med.sent), "already correctly positioned — nothing should move")
}

// TestEnsureParked_PrefersAConfirmedSellerOverTheFirstCandidateOnAFreshBuy pins the FIRST half of the
// defect (shared with EnsureShipyardReadable): on a fresh buy, still standing wherever the batch-purchase
// happened to leave it (docked at neither candidate), EnsureParked must not blindly navigate toward the
// first SHIPYARD waypoint — it must prefer the one confirmed to sell shipType.
func TestEnsureParked_PrefersAConfirmedSellerOverTheFirstCandidateOnAFreshBuy(t *testing.T) {
	saved := &fakeSavedYards{rows: []domainShipyard.ShipTypeAvailability{
		{WaypointSymbol: "X1-MG48-H55", ShipType: "SHIP_MINING_DRONE", PurchasePrice: 55_000},
		{WaypointSymbol: "X1-MG48-C41", ShipType: "SHIP_PROBE", PurchasePrice: 40_000},
	}}
	sentinel := shipyardHull(t, "TORWIND-5", "X1-MG48-BUY-SITE", "", "SATELLITE", navigation.NavStatusInOrbit)
	med := &recordingBuyMediator{}
	h55, err := shared.NewWaypoint("X1-MG48-H55", 0, 0)
	require.NoError(t, err)
	c41, err := shared.NewWaypoint("X1-MG48-C41", 0, 0)
	require.NoError(t, err)
	acq := &bootstrapYardSentinelAcquirer{bootstrapAcquirer: &bootstrapAcquirer{
		med:          med,
		shipRepo:     &fakeAskShipRepo{all: []*navigation.Ship{sentinel}},
		waypointRepo: &fakeYardWaypoints{yards: []*shared.Waypoint{h55, c41}}, // first candidate returned first
		savedYards:   saved,
	}}

	docked, err := acq.EnsureParked(context.Background(), 1, "X1-MG48", "SHIP_PROBE", "TORWIND-5")

	require.NoError(t, err)
	require.False(t, docked)
	require.Equal(t, []string{"X1-MG48-C41"}, navigateDestinations(med.sent),
		"must route the fresh sentinel toward the CONFIRMED seller, never blindly toward the first SHIPYARD waypoint")
}

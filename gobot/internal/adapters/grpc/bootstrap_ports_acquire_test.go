package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// sp-ltl75: EnsureShipyardReadable picked the FIRST SHIPYARD-trait waypoint in the home system with no
// regard for whether it actually sold the ship type bootstrap needed. Reproduced live in
// torwind-2026-08-23 (player 9, system X1-MG48): it picked X1-MG48-H55 (sells only SHIP_MINING_DRONE /
// SHIP_SURVEYOR) instead of X1-MG48-C41 (sells SHIP_PROBE), sent the command frigate there, and looped
// price_unreadable -> positioning_purchaser_at_shipyard -> price_unreadable every tick for 27+ minutes —
// a PERMANENT deadlock, because H55 will never sell a probe no matter how long a hull stands there.
//
// These pin the fix: the scanner now weighs the player's persisted shipyard-inventory record (every
// completed live GetShipyard scan on file) so a candidate CONFIRMED to sell the sought type is preferred,
// and one CONFIRMED not to is never sent to again for that type.

// navigateDestinations extracts the destination of every NavigateRouteCommand the mediator recorded, in
// call order, so a test can prove WHICH waypoint a dispatch actually targeted (or that none did).
func navigateDestinations(sent []common.Request) []string {
	var dests []string
	for _, r := range sent {
		if nav, ok := r.(*navCmd.NavigateRouteCommand); ok {
			dests = append(dests, nav.Destination)
		}
	}
	return dests
}

// mgYardWaypoints builds the two home-system SHIPYARD-trait candidates from the live incident, in the
// SAME order ListBySystemWithTrait returned them that night: the wrong yard (H55) first.
func mgYardWaypoints(t *testing.T) *fakeYardWaypoints {
	t.Helper()
	h55, err := shared.NewWaypoint("X1-MG48-H55", 0, 0)
	require.NoError(t, err)
	c41, err := shared.NewWaypoint("X1-MG48-C41", 0, 0)
	require.NoError(t, err)
	return &fakeYardWaypoints{yards: []*shared.Waypoint{h55, c41}}
}

// TestEnsureShipyardReadable_PrefersAConfirmedSellerOverAConfirmedWrongYard is the core regression pin:
// H55 is returned FIRST but is confirmed (via a scan on record) to sell only mining drones/surveyors;
// C41 is confirmed to sell probes. The fix must send toward C41, not blindly toward the first candidate.
func TestEnsureShipyardReadable_PrefersAConfirmedSellerOverAConfirmedWrongYard(t *testing.T) {
	saved := &fakeSavedYards{rows: []domainShipyard.ShipTypeAvailability{
		{WaypointSymbol: "X1-MG48-H55", ShipType: "SHIP_MINING_DRONE", PurchasePrice: 55_000},
		{WaypointSymbol: "X1-MG48-H55", ShipType: "SHIP_SURVEYOR", PurchasePrice: 78_000},
		{WaypointSymbol: "X1-MG48-C41", ShipType: "SHIP_PROBE", PurchasePrice: 40_000},
	}}
	freeHull := shipyardHull(t, "TORWIND-1", "X1-MG48-A1", "", commandRole, navigation.NavStatusInOrbit)
	med := &recordingBuyMediator{}
	scanner := &bootstrapShipyardScanner{
		med:          med,
		shipRepo:     &fakeAskShipRepo{all: []*navigation.Ship{freeHull}},
		waypointRepo: mgYardWaypoints(t),
		savedYards:   saved,
	}

	dispatched, exhausted, err := scanner.EnsureShipyardReadable(context.Background(), 1, "X1-MG48", "SHIP_PROBE", "", "")

	require.NoError(t, err)
	require.False(t, exhausted, "a viable candidate (C41) exists — this is not the exhausted dead end")
	require.True(t, dispatched, "a free hull exists and a confirmed seller exists — it must be sent")
	require.Equal(t, []string{"X1-MG48-C41"}, navigateDestinations(med.sent),
		"must route toward the CONFIRMED seller, never the yard proven not to sell the type")
}

// TestEnsureShipyardReadable_NeverRedispatchesToAConfirmedWrongYardEvenWhileAHullStandsThere reproduces
// the actual DEADLOCK shape: the named purchaser is already standing AT the wrong yard (H55), exactly
// where the live incident's stuck hull sat for 27+ minutes. The pre-fix code treated "standing at ANY
// SHIPYARD waypoint" as "in position, nothing to do" forever. The fix must recognise H55 is confirmed
// wrong and redirect the purchaser toward C41 instead of standing down.
func TestEnsureShipyardReadable_NeverRedispatchesToAConfirmedWrongYardEvenWhileAHullStandsThere(t *testing.T) {
	saved := &fakeSavedYards{rows: []domainShipyard.ShipTypeAvailability{
		{WaypointSymbol: "X1-MG48-H55", ShipType: "SHIP_MINING_DRONE", PurchasePrice: 55_000},
		{WaypointSymbol: "X1-MG48-H55", ShipType: "SHIP_SURVEYOR", PurchasePrice: 78_000},
		{WaypointSymbol: "X1-MG48-C41", ShipType: "SHIP_PROBE", PurchasePrice: 40_000},
	}}
	// The purchasing frigate is stuck exactly where the incident left it: docked at the wrong yard.
	stuckPurchaser := shipyardHull(t, "TORWIND-1", "X1-MG48-H55", navigation.PurchasingFleet, commandRole, navigation.NavStatusInOrbit)
	med := &recordingBuyMediator{}
	scanner := &bootstrapShipyardScanner{
		med:          med,
		shipRepo:     &fakeAskShipRepo{all: []*navigation.Ship{stuckPurchaser}},
		waypointRepo: mgYardWaypoints(t),
		savedYards:   saved,
	}

	dispatched, exhausted, err := scanner.EnsureShipyardReadable(context.Background(), 1, "X1-MG48", "SHIP_PROBE", "TORWIND-1", "")

	require.NoError(t, err)
	require.False(t, exhausted)
	require.True(t, dispatched, "DEADLOCK: a hull parked at a yard confirmed not to sell the type must be redirected, not read as already in position")
	require.Equal(t, []string{"X1-MG48-C41"}, navigateDestinations(med.sent))
}

// TestEnsureShipyardReadable_UnscannedCandidateStillEligibleWhenNothingIsKnownYet guards the ORIGINAL
// behavior on a fresh universe: with no shipyard-inventory record at all, the first candidate is still
// sent to discover it — the fix must not require prior knowledge before it ever moves a hull.
func TestEnsureShipyardReadable_UnscannedCandidateStillEligibleWhenNothingIsKnownYet(t *testing.T) {
	freeHull := shipyardHull(t, "TORWIND-1", "X1-MG48-A1", "", commandRole, navigation.NavStatusInOrbit)
	med := &recordingBuyMediator{}
	scanner := &bootstrapShipyardScanner{
		med:          med,
		shipRepo:     &fakeAskShipRepo{all: []*navigation.Ship{freeHull}},
		waypointRepo: mgYardWaypoints(t),
		savedYards:   &fakeSavedYards{}, // nothing on record yet
	}

	dispatched, exhausted, err := scanner.EnsureShipyardReadable(context.Background(), 1, "X1-MG48", "SHIP_PROBE", "", "")

	require.NoError(t, err)
	require.False(t, exhausted)
	require.True(t, dispatched)
	require.Equal(t, []string{"X1-MG48-H55"}, navigateDestinations(med.sent),
		"nothing is known yet — the original first-candidate discovery move is unchanged")
}

// TestEnsureShipyardReadable_ExhaustedWhenEveryCandidateConfirmedWrong is the distinct diagnosable dead
// end (sp-ltl75 acceptance #4): every known candidate has a scan on record and NONE of them sells the
// sought type. This must surface as its own state, never loop as a plain "still cold" wait, and it must
// dispatch nobody (there is nowhere useful left to send a hull).
func TestEnsureShipyardReadable_ExhaustedWhenEveryCandidateConfirmedWrong(t *testing.T) {
	saved := &fakeSavedYards{rows: []domainShipyard.ShipTypeAvailability{
		{WaypointSymbol: "X1-MG48-H55", ShipType: "SHIP_MINING_DRONE", PurchasePrice: 55_000},
		{WaypointSymbol: "X1-MG48-C41", ShipType: "SHIP_SURVEYOR", PurchasePrice: 60_000},
	}}
	freeHull := shipyardHull(t, "TORWIND-1", "X1-MG48-A1", "", commandRole, navigation.NavStatusInOrbit)
	med := &recordingBuyMediator{}
	scanner := &bootstrapShipyardScanner{
		med:          med,
		shipRepo:     &fakeAskShipRepo{all: []*navigation.Ship{freeHull}},
		waypointRepo: mgYardWaypoints(t),
		savedYards:   saved,
	}

	dispatched, exhausted, err := scanner.EnsureShipyardReadable(context.Background(), 1, "X1-MG48", "SHIP_PROBE", "", "")

	require.NoError(t, err)
	require.True(t, exhausted, "every candidate is confirmed wrong — this is the distinct dead end, not price_unreadable forever")
	require.False(t, dispatched)
	require.Empty(t, navigateDestinations(med.sent), "nothing may be sent when no candidate can ever sell the type")
}

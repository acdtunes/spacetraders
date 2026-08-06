package parkedsensing_test

// Integration tests (real GORM, no mocks) for the two reads behind the cold-start
// escape: which hull may SIGN for a purchase at a counter, and which hulls the
// engine may BORROW to put on one.
//
// THE SELECTION PREDICATE IS THE WHOLE SUBJECT, exactly as it is for
// DockedProbeAt, and it is stricter here because a non-probe hull has an owner.
// ShipRepository.ClaimShip refuses — inside its own row lock — a hull dedicated to
// another fleet, a hull a container already holds, and a hull the captain has
// reserved. Every one of those refusals is PERMANENT, so a hull offered here that
// cannot be claimed becomes a per-tick API drain: select, pay for a live shipyard
// price read, fail the claim, select the same hull again.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// hullRow builds a docked hull of the given role, free of every claim.
func hullRow(symbol, waypoint, role string) persistence.ShipModel {
	return persistence.ShipModel{
		ShipSymbol:       symbol,
		PlayerID:         testPlayerID,
		NavStatus:        string(navigation.NavStatusDocked),
		LocationSymbol:   waypoint,
		SystemSymbol:     "X1-AA",
		Role:             role,
		AssignmentStatus: "idle",
	}
}

func createHull(t *testing.T, db *gorm.DB, model persistence.ShipModel) {
	t.Helper()
	require.NoError(t, db.Create(&model).Error)
}

// --- DockedBuyerAt ------------------------------------------------------------

// THE HEADLINE. A hauler standing at the counter can sign for a purchase, which is
// the entire cold-start escape: the API sells a hull wherever one of ours is docked
// and does not care which.
func TestDockedBuyerAt_OffersAnIdleUndedicatedNonProbeHull(t *testing.T) {
	db := newShipPortsDB(t)
	createHull(t, db, hullRow("TORWIND-9", "X1-AA-Y1", "HAULER"))

	port := adapterSensing.NewShipPositionPort(db)
	ship, found, err := port.DockedBuyerAt(context.Background(), testPlayerID, "X1-AA-Y1")
	require.NoError(t, err)
	require.True(t, found, "a free hauler docked at the counter is exactly what the purchase needs")
	require.Equal(t, "TORWIND-9", ship)
}

// THE PERMANENT-REJECTION FILTER, one case per guard ClaimShip applies. Each of
// these hulls would be refused on every tick forever, so offering one converts a
// stalled placement into a standing API drain.
func TestDockedBuyerAt_SkipsEveryHullTheClaimPathWouldRefuse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*persistence.ShipModel)
		because string
	}{
		{
			name:    "dedicated to another fleet",
			mutate:  func(m *persistence.ShipModel) { m.DedicatedFleet = "contract" },
			because: "ClaimShip refuses a new claim on a hull whose fleet tag names somebody else",
		},
		{
			name: "already claimed by a container",
			mutate: func(m *persistence.ShipModel) {
				m.AssignmentStatus = "active"
				m.AssignmentOwner = string(navigation.AssignmentOwnerContainer)
			},
			because: "a live container claim belongs to whichever coordinator is driving the hull",
		},
		{
			name: "reserved by the captain",
			mutate: func(m *persistence.ShipModel) {
				m.AssignmentStatus = "active"
				m.AssignmentOwner = string(navigation.AssignmentOwnerCaptain)
			},
			because: "a captain reservation survives restarts precisely so no coordinator can take it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newShipPortsDB(t)
			model := hullRow("TORWIND-9", "X1-AA-Y1", "HAULER")
			tc.mutate(&model)
			createHull(t, db, model)

			port := adapterSensing.NewShipPositionPort(db)
			_, found, err := port.DockedBuyerAt(context.Background(), testPlayerID, "X1-AA-Y1")
			require.NoError(t, err)
			require.False(t, found, tc.because)
		})
	}
}

// A HULL THAT IS NOT AT THE COUNTER IS NOT A BUYER. Docked is the API's own
// precondition, so a hull in orbit above the yard, or standing somewhere else, or
// belonging to another player, is none of our business.
func TestDockedBuyerAt_SkipsHullsThatCannotSign(t *testing.T) {
	db := newShipPortsDB(t)
	orbiting := hullRow("TORWIND-ORBIT", "X1-AA-Y1", "HAULER")
	orbiting.NavStatus = string(navigation.NavStatusInOrbit)
	createHull(t, db, orbiting)
	createHull(t, db, hullRow("TORWIND-ELSEWHERE", "X1-AA-Y2", "HAULER"))
	theirs := hullRow("TORWIND-THEIRS", "X1-AA-Y1", "HAULER")
	theirs.PlayerID = testPlayerID + 1
	createHull(t, db, theirs)

	port := adapterSensing.NewShipPositionPort(db)
	_, found, err := port.DockedBuyerAt(context.Background(), testPlayerID, "X1-AA-Y1")
	require.NoError(t, err)
	require.False(t, found)
}

// THE PREFERENCE LADDER. A probe on station signs first, an ordinary hull next, and
// the command frigate LAST (RULINGS #7 — the flagship is drafted only when nothing
// else can do the job). The symbols are chosen so plain alphabetical order would
// give the opposite answer at every rung.
func TestDockedBuyerAt_PrefersProbeThenHaulerThenTheCommandFrigate(t *testing.T) {
	db := newShipPortsDB(t)
	createHull(t, db, hullRow("TORWIND-1", "X1-AA-Y1", "COMMAND")) // sorts first by symbol
	createHull(t, db, hullRow("TORWIND-8", "X1-AA-Y1", "HAULER"))
	port := adapterSensing.NewShipPositionPort(db)

	ship, found, err := port.DockedBuyerAt(context.Background(), testPlayerID, "X1-AA-Y1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "TORWIND-8", ship, "the command frigate was drafted while an ordinary hauler stood beside it")

	createHull(t, db, hullRow("TORWIND-Z", "X1-AA-Y1", "SATELLITE")) // sorts last by symbol
	ship, found, err = port.DockedBuyerAt(context.Background(), testPlayerID, "X1-AA-Y1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "TORWIND-Z", ship, "a probe of ours was on the counter and another coordinator's hull was taken anyway")
}

// OUR OWN SENSING FLEET IS ADMITTED, same as DockedProbeAt: ClaimShip accepts a hull
// whose tag matches the claiming operation, so a sensing-tagged hull is claimable.
func TestDockedBuyerAt_AdmitsOurOwnSensingFleet(t *testing.T) {
	db := newShipPortsDB(t)
	model := hullRow("TORWIND-9", "X1-AA-Y1", "HAULER")
	model.DedicatedFleet = appSensing.SensingParkedFleetTag
	createHull(t, db, model)

	port := adapterSensing.NewShipPositionPort(db)
	ship, found, err := port.DockedBuyerAt(context.Background(), testPlayerID, "X1-AA-Y1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "TORWIND-9", ship)
}

// --- LendableHulls ------------------------------------------------------------

// PROBES ARE NOT LENDABLE, and that is the point of the pass rather than an
// optimisation: the deadlock it serves is "no probe is free to put at a probe
// counter", so a probe answer would be either impossible or already served by the
// paths that move probes.
func TestLendableHulls_ExcludesProbes(t *testing.T) {
	db := newShipPortsDB(t)
	createHull(t, db, hullRow("TORWIND-PROBE", "X1-AA-A1", "SATELLITE"))
	createHull(t, db, hullRow("TORWIND-9", "X1-AA-A1", "HAULER"))

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8)
	require.NoError(t, err)
	require.Len(t, hulls, 1)
	require.Equal(t, "TORWIND-9", hulls[0].ShipSymbol)
	require.Equal(t, "X1-AA-A1", hulls[0].Waypoint)
	require.Equal(t, "X1-AA", hulls[0].System)
	require.False(t, hulls[0].InTransit)
}

// THE SAME CLAIM FILTER AS DockedBuyerAt. A hull the claim path would refuse is not
// a hull worth flying anywhere: the flight would be spent and the purchase would
// still fail.
func TestLendableHulls_ExcludesEveryHullTheClaimPathWouldRefuse(t *testing.T) {
	db := newShipPortsDB(t)
	dedicated := hullRow("TORWIND-A", "X1-AA-A1", "HAULER")
	dedicated.DedicatedFleet = "contract"
	createHull(t, db, dedicated)
	claimed := hullRow("TORWIND-B", "X1-AA-A1", "HAULER")
	claimed.AssignmentStatus = "active"
	claimed.AssignmentOwner = string(navigation.AssignmentOwnerContainer)
	createHull(t, db, claimed)
	reserved := hullRow("TORWIND-C", "X1-AA-A1", "HAULER")
	reserved.AssignmentStatus = "active"
	reserved.AssignmentOwner = string(navigation.AssignmentOwnerCaptain)
	createHull(t, db, reserved)

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8)
	require.NoError(t, err)
	require.Empty(t, hulls, "offered a hull that is dedicated, claimed or reserved — none of them is ours to lend")
}

// IN-TRANSIT HULLS ARE RETURNED AND FLAGGED. They are not borrowable, but a hull
// already flying to a counter is exactly what stops the next tick sending a second
// one there — dropping them would make the pass double-dispatch. The ships row
// records the DESTINATION while a hull is under way, which is why the flag alone is
// enough to identify the counter.
func TestLendableHulls_ReturnsInTransitHullsFlagged(t *testing.T) {
	db := newShipPortsDB(t)
	flying := hullRow("TORWIND-9", "X1-AA-Y1", "HAULER")
	flying.NavStatus = string(navigation.NavStatusInTransit)
	createHull(t, db, flying)

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8)
	require.NoError(t, err)
	require.Len(t, hulls, 1)
	require.True(t, hulls[0].InTransit, "a hull under way was reported as standing still and would be commanded again")
	require.Equal(t, "X1-AA-Y1", hulls[0].Waypoint, "the destination is what identifies the counter already being served")
}

// THE COMMAND FRIGATE SORTS LAST, so a tick that lends one hull lends the ordinary
// one (RULINGS #7).
func TestLendableHulls_RanksTheCommandFrigateLast(t *testing.T) {
	db := newShipPortsDB(t)
	createHull(t, db, hullRow("TORWIND-1", "X1-AA-A1", "COMMAND")) // sorts first by symbol
	createHull(t, db, hullRow("TORWIND-8", "X1-AA-A1", "HAULER"))

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8)
	require.NoError(t, err)
	require.Len(t, hulls, 2)
	require.Equal(t, "TORWIND-8", hulls[0].ShipSymbol, "the flagship was offered ahead of an ordinary hauler")
	require.Equal(t, "TORWIND-1", hulls[1].ShipSymbol)
}

// THE BOUND IS THE CONTRACT. A non-positive limit yields NOTHING rather than the
// whole fleet: defaulting an unset bound to "unbounded" is how a bounded read
// quietly becomes a fleet walk, which is the property ParkedShipReader protects.
func TestLendableHulls_HonoursItsBoundAndRefusesAnUnsetOne(t *testing.T) {
	db := newShipPortsDB(t)
	for _, symbol := range []string{"TORWIND-2", "TORWIND-3", "TORWIND-4"} {
		createHull(t, db, hullRow(symbol, "X1-AA-A1", "HAULER"))
	}

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 2)
	require.NoError(t, err)
	require.Len(t, hulls, 2)

	none, err := port.LendableHulls(context.Background(), testPlayerID, 0)
	require.NoError(t, err)
	require.Empty(t, none)
}

// CROSS-PLAYER ISOLATION, asserted rather than assumed: another agent's hulls are
// never ours to lend.
func TestLendableHulls_IsScopedToThePlayer(t *testing.T) {
	db := newShipPortsDB(t)
	theirs := hullRow("TORWIND-THEIRS", "X1-AA-A1", "HAULER")
	theirs.PlayerID = testPlayerID + 1
	createHull(t, db, theirs)

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8)
	require.NoError(t, err)
	require.Empty(t, hulls)
}

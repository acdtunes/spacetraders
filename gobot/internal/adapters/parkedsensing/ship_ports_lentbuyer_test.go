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
//
// A FOREIGN FLEET TAG IS NOT ON THIS LIST ANY MORE. The buy signs under the hull's
// own dedication, so ClaimShip admits a borrowed hull; what the tag costs is priced
// by the loaded-hold case below instead.
func TestDockedBuyerAt_SkipsEveryHullTheClaimPathWouldRefuse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*persistence.ShipModel)
		because string
	}{
		{
			name: "another fleet's hull with cargo aboard",
			mutate: func(m *persistence.ShipModel) {
				m.DedicatedFleet = navigation.TradeFleetMVT
				m.CargoUnits = 12
			},
			because: "a loaded hull is between the legs of a job, and signing for a purchase is not what it is standing there for",
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

// A BORROWED HULL MUST BE RECOGNISED AT THE COUNTER, or lending one buys nothing:
// staffedAt and the buy queue both ask this question, so a hull flown to a yard and
// then not seen there leaves the yard reading unstaffed on every later tick and the
// flight is spent for nothing.
func TestDockedBuyerAt_AdmitsAnIdleHullOnLoanFromAWorkingFleet(t *testing.T) {
	db := newShipPortsDB(t)
	model := hullRow("TORWIND-9", "X1-AA-Y1", "HAULER")
	model.DedicatedFleet = navigation.TradeFleetMVT
	createHull(t, db, model)

	port := adapterSensing.NewShipPositionPort(db)
	ship, found, err := port.DockedBuyerAt(context.Background(), testPlayerID, "X1-AA-Y1")
	require.NoError(t, err)
	require.True(t, found, "the lent hull is standing on the counter and nothing can be bought through it")
	require.Equal(t, "TORWIND-9", ship)
}

// THE SAME PREFERENCE, AT THE COUNTER. A free hull signs before one borrowed from a
// working fleet; the symbols are chosen so alphabetical order would answer otherwise.
func TestDockedBuyerAt_PrefersAFreeSignerOverABorrowedOne(t *testing.T) {
	db := newShipPortsDB(t)
	borrowed := hullRow("TORWIND-1", "X1-AA-Y1", "HAULER") // sorts first by symbol
	borrowed.DedicatedFleet = navigation.TradeFleetMVT
	createHull(t, db, borrowed)
	createHull(t, db, hullRow("TORWIND-8", "X1-AA-Y1", "HAULER"))

	port := adapterSensing.NewShipPositionPort(db)
	ship, found, err := port.DockedBuyerAt(context.Background(), testPlayerID, "X1-AA-Y1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "TORWIND-8", ship, "a working fleet's hull was signed for while a free one stood at the same counter")
}

// --- LendableHulls ------------------------------------------------------------

// PROBES ARE LENDABLE AND ARE OFFERED FIRST. A probe is the cheapest signer the
// fleet owns, so on a grown fleet the escape should never be waiting on a trade
// hull. Which probe is a LEDGER question this port cannot answer, so role is simply
// not the filter — the caller names the committed hulls instead.
func TestLendableHulls_AdmitsProbesAndOffersThemAheadOfTradeHulls(t *testing.T) {
	db := newShipPortsDB(t)
	createHull(t, db, hullRow("TORWIND-9", "X1-AA-A1", "HAULER")) // sorts first by symbol
	createHull(t, db, hullRow("TORWIND-PROBE", "X1-AA-A1", "SATELLITE"))

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
	require.NoError(t, err)
	require.Len(t, hulls, 2, "a probe standing idle is a candidate; excluding it left the escape waiting on a hauler")
	require.Equal(t, "TORWIND-PROBE", hulls[0].ShipSymbol, "a trade hull was offered while a probe stood beside it")
	require.Equal(t, "X1-AA-A1", hulls[0].Waypoint)
	require.Equal(t, "X1-AA", hulls[0].System)
	require.False(t, hulls[0].InTransit)
	require.Equal(t, "TORWIND-9", hulls[1].ShipSymbol)
}

// THE SKIP LIST IS APPLIED, which is what keeps the bounded page meaning "the best
// candidates" rather than "the first rows" on a fleet whose probes are nearly all
// committed. It is a paging aid, never the guard — the caller re-tests what it gets.
func TestLendableHulls_SkipsTheHullsTheCallerNamesAsEngaged(t *testing.T) {
	db := newShipPortsDB(t)
	createHull(t, db, hullRow("TORWIND-BUSY", "X1-AA-A1", "SATELLITE"))
	createHull(t, db, hullRow("TORWIND-FREE", "X1-AA-A1", "SATELLITE"))

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, []string{"TORWIND-BUSY"})
	require.NoError(t, err)
	require.Len(t, hulls, 1)
	require.Equal(t, "TORWIND-FREE", hulls[0].ShipSymbol)

	// An empty list is "skip nothing", never "skip everything" — a fleet with no
	// committed hull at all must still get its candidates.
	all, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
	require.NoError(t, err)
	require.Len(t, all, 2)
}

// THE SAME CLAIM FILTER AS DockedBuyerAt. A hull the claim path would refuse is not
// a hull worth flying anywhere: the flight would be spent and the purchase would
// still fail. A hull carrying cargo is refused for the other reason — it is working.
func TestLendableHulls_ExcludesEveryHullTheClaimPathWouldRefuse(t *testing.T) {
	db := newShipPortsDB(t)
	loaded := hullRow("TORWIND-A", "X1-AA-A1", "HAULER")
	loaded.DedicatedFleet = navigation.TradeFleetMVT
	loaded.CargoUnits = 12
	createHull(t, db, loaded)
	claimed := hullRow("TORWIND-B", "X1-AA-A1", "HAULER")
	claimed.AssignmentStatus = "active"
	claimed.AssignmentOwner = string(navigation.AssignmentOwnerContainer)
	createHull(t, db, claimed)
	reserved := hullRow("TORWIND-C", "X1-AA-A1", "HAULER")
	reserved.AssignmentStatus = "active"
	reserved.AssignmentOwner = string(navigation.AssignmentOwnerCaptain)
	createHull(t, db, reserved)

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
	require.NoError(t, err)
	require.Empty(t, hulls, "offered a hull that is loaded, claimed or reserved — none of them is ours to lend")
}

// THE HEADLINE FIX. Every hauler the fleet owns carries a trade tag by the time the
// bot reaches steady state, and the old filter admitted only undedicated hulls — so
// staffCounters was handed an empty list, returned at once, and the cold deadlock
// could never be broken however many dark systems were waiting.
func TestLendableHulls_LendsAnIdleHullFromADedicatedFleet(t *testing.T) {
	db := newShipPortsDB(t)
	for _, fleet := range []string{navigation.TradeFleet, navigation.TradeFleetMVT, navigation.TradeFleetLane, "contract"} {
		t.Run(fleet, func(t *testing.T) {
			model := hullRow("TORWIND-9", "X1-AA-A1", "HAULER")
			model.DedicatedFleet = fleet
			require.NoError(t, db.Save(&model).Error)

			port := adapterSensing.NewShipPositionPort(db)
			hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
			require.NoError(t, err)
			require.Len(t, hulls, 1, "a fully-dedicated fleet has no way out of the deadlock if its idle hulls are unlendable")
			require.Equal(t, "TORWIND-9", hulls[0].ShipSymbol)
		})
	}
}

// AN EMPTY HOLD IS WHAT REPLACES THE TAG AS THE GUARD. Cargo aboard means the hull is
// between the legs of a job with goods and money committed to it, so lending it could
// strand both — the one failure worse than the deadlock itself.
func TestLendableHulls_RefusesADedicatedHullWithCargoAboard(t *testing.T) {
	db := newShipPortsDB(t)
	loaded := hullRow("TORWIND-9", "X1-AA-A1", "HAULER")
	loaded.DedicatedFleet = navigation.TradeFleetMVT
	loaded.CargoUnits = 1
	createHull(t, db, loaded)

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
	require.NoError(t, err)
	require.Empty(t, hulls, "a single unit aboard is still a job in progress")
}

// AN ACTIVE ASSIGNMENT EXCLUDES A HULL WHATEVER ITS FLEET, including the general pool
// and this engine's own. It is the claim, not the tag, that says a coordinator is
// driving the hull right now — so widening the tag filter must not have widened this.
func TestLendableHulls_ExcludesAClaimedHullWhateverItsFleet(t *testing.T) {
	for _, fleet := range []string{"", appSensing.SensingParkedFleetTag, navigation.TradeFleetMVT, "contract"} {
		for _, owner := range []navigation.AssignmentOwner{navigation.AssignmentOwnerContainer, navigation.AssignmentOwnerCaptain} {
			t.Run(fleet+"/"+string(owner), func(t *testing.T) {
				db := newShipPortsDB(t)
				model := hullRow("TORWIND-9", "X1-AA-A1", "HAULER")
				model.DedicatedFleet = fleet
				model.AssignmentStatus = "active"
				model.AssignmentOwner = string(owner)
				createHull(t, db, model)

				port := adapterSensing.NewShipPositionPort(db)
				hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
				require.NoError(t, err)
				require.Empty(t, hulls, "a hull under a live claim is being driven by whoever holds it")
			})
		}
	}
}

// AN ACTIVE ASSIGNMENT STILL EXCLUDES A PROBE, whatever it is tagged. Dropping the
// role filter widened WHO may be considered and nothing else: a probe a container
// holds or the captain has reserved is still being driven by somebody.
func TestLendableHulls_ExcludesAClaimedProbeHoweverItIsTagged(t *testing.T) {
	for _, fleet := range []string{"", appSensing.SensingParkedFleetTag, "scout", navigation.TradeFleetMVT} {
		t.Run(fleet, func(t *testing.T) {
			db := newShipPortsDB(t)
			model := hullRow("TORWIND-PROBE", "X1-AA-A1", "SATELLITE")
			model.DedicatedFleet = fleet
			model.AssignmentStatus = "active"
			model.AssignmentOwner = string(navigation.AssignmentOwnerContainer)
			createHull(t, db, model)

			port := adapterSensing.NewShipPositionPort(db)
			hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
			require.NoError(t, err)
			require.Empty(t, hulls, "a probe under a live claim is being driven by whoever holds it")
		})
	}
}

// THE FLEET TAG IS NOT THE TEST, and this pins it against the tag the fleet happens
// to be wearing today. A probe is a candidate on the strength of standing idle and
// being named by no placement row — a rule that survives the tag being renamed.
func TestLendableHulls_AdmitsAnIdleProbeWhateverItIsTagged(t *testing.T) {
	for _, fleet := range []string{"", appSensing.SensingParkedFleetTag, "scout"} {
		t.Run(fleet, func(t *testing.T) {
			db := newShipPortsDB(t)
			model := hullRow("TORWIND-PROBE", "X1-AA-A1", "SATELLITE")
			model.DedicatedFleet = fleet
			createHull(t, db, model)

			port := adapterSensing.NewShipPositionPort(db)
			hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
			require.NoError(t, err)
			require.Len(t, hulls, 1)
			require.Equal(t, "TORWIND-PROBE", hulls[0].ShipSymbol)
		})
	}
}

// THE CHEAPEST SACRIFICE FIRST. Inside a role rung a hull no coordinator is counting
// on goes before one borrowed from a working fleet, so the trade fleet is disturbed
// only when nothing free can do the job. The symbols are chosen so plain alphabetical
// order would give the opposite answer.
func TestLendableHulls_PrefersAFreeHullOverOneBorrowedFromAWorkingFleet(t *testing.T) {
	db := newShipPortsDB(t)
	working := hullRow("TORWIND-1", "X1-AA-A1", "HAULER") // sorts first by symbol
	working.DedicatedFleet = navigation.TradeFleetMVT
	createHull(t, db, working)
	createHull(t, db, hullRow("TORWIND-8", "X1-AA-A1", "HAULER"))

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
	require.NoError(t, err)
	require.Len(t, hulls, 2)
	require.Equal(t, "TORWIND-8", hulls[0].ShipSymbol, "a working fleet's hull was offered ahead of a free one standing beside it")
	require.Equal(t, "TORWIND-1", hulls[1].ShipSymbol)
}

// RULINGS #7 STILL ORDERS THE RUNGS. The dedication tie-break sits INSIDE the role
// ladder, so a free flagship is still drafted after a borrowed ordinary hull rather
// than ahead of it.
func TestLendableHulls_StillRanksTheCommandFrigateBehindABorrowedHauler(t *testing.T) {
	db := newShipPortsDB(t)
	createHull(t, db, hullRow("TORWIND-1", "X1-AA-A1", "COMMAND"))
	borrowed := hullRow("TORWIND-8", "X1-AA-A1", "HAULER")
	borrowed.DedicatedFleet = navigation.TradeFleetMVT
	createHull(t, db, borrowed)

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
	require.NoError(t, err)
	require.Len(t, hulls, 2)
	require.Equal(t, "TORWIND-8", hulls[0].ShipSymbol, "the flagship was drafted while an ordinary hull could do the job")
	require.Equal(t, "TORWIND-1", hulls[1].ShipSymbol)
}

// THE IDEMPOTENCE KEY'S INPUT SURVIVES THE WIDENING. The pass strikes out a counter a
// hull is already flying to, and it builds that index from the in-transit flag and the
// destination this read reports. A borrowed hull under way must therefore still come
// back flagged, or the next tick sends a second hull to the same counter.
func TestLendableHulls_FlagsAnInTransitDedicatedHullSoItsCounterIsStruckOut(t *testing.T) {
	db := newShipPortsDB(t)
	flying := hullRow("TORWIND-9", "X1-AA-YARD", "HAULER")
	flying.DedicatedFleet = navigation.TradeFleetMVT
	flying.NavStatus = string(navigation.NavStatusInTransit)
	createHull(t, db, flying)

	port := adapterSensing.NewShipPositionPort(db)
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
	require.NoError(t, err)
	require.Len(t, hulls, 1)
	require.True(t, hulls[0].InTransit, "a borrowed hull under way was reported as standing still")
	require.Equal(t, "X1-AA-YARD", hulls[0].Waypoint, "the destination is what names the counter already being served")
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
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
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
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
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
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 2, nil)
	require.NoError(t, err)
	require.Len(t, hulls, 2)

	none, err := port.LendableHulls(context.Background(), testPlayerID, 0, nil)
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
	hulls, err := port.LendableHulls(context.Background(), testPlayerID, 8, nil)
	require.NoError(t, err)
	require.Empty(t, hulls)
}

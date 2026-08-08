package grpc

// Tests for the BUYER/YARD PAIRING on the hull buy path.
//
// The purchase must be signed by a hull ALREADY STANDING at the yard, so no flight is bought and no
// navigation step exists to fail. The fixtures below are the live shape: hulls stand on the cheap
// yards, but they belong to another fleet and the claim would refuse them, while the yard we can
// actually transact at is dearer. The target must be the dearer one, and the cheapest-KNOWN ask must
// keep naming the cheap one so the premium guard and ObserveHeavyPricePremium both judge honestly.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

const pairingHeavyType = "SHIP_HEAVY_FREIGHTER"

// sensingFleetTag is the parked-sensing dedication, taken from the application constant so a
// re-spelling here cannot make these fixtures agree with a rule the engine no longer applies.
const sensingFleetTag = parkedsensing.SensingParkedFleetTag

func buyerSymbols(buyers []purchaseBuyer) []string {
	out := make([]string, 0, len(buyers))
	for _, b := range buyers {
		out = append(out, b.ship.ShipSymbol())
	}
	return out
}

// --- fakes -------------------------------------------------------------------------------------

// fakeSystemYardLister lists SHIPYARD-trait waypoints PER SYSTEM, so a fixture can put yards in
// different systems rather than answering the same yards everywhere.
type fakeSystemYardLister struct{ bySystem map[string][]*shared.Waypoint }

func (f *fakeSystemYardLister) ListBySystemWithTrait(_ context.Context, systemSymbol, _ string) ([]*shared.Waypoint, error) {
	return f.bySystem[systemSymbol], nil
}

// fakeYardAskMediator prices ONE ship type per waypoint. An absent waypoint reads as a yard with no
// usable listing, exactly as an unpriced (presence-less) yard does.
type fakeYardAskMediator struct {
	common.Mediator
	askByWaypoint map[string]int
}

func (m *fakeYardAskMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	q, ok := request.(*shipyardQueries.GetShipyardListingsQuery)
	if !ok {
		return nil, errors.New("unexpected request")
	}
	ask, found := m.askByWaypoint[q.WaypointSymbol]
	if !found {
		return nil, errors.New("no priced listing at " + q.WaypointSymbol)
	}
	return &shipyardQueries.GetShipyardListingsResponse{
		Shipyard: domainShipyard.NewShipyard(q.WaypointSymbol, []string{pairingHeavyType},
			[]domainShipyard.ShipListing{{ShipType: pairingHeavyType, PurchasePrice: ask}}, 0),
	}, nil
}

// claimingShipRepo records the claim and the hand-back the borrowed signer must produce.
type claimingShipRepo struct {
	fakeReclaimShipRepo
	claimed   []claimCall
	released  []string
	claimErr  error
	claimedOK bool
}

type claimCall struct{ ship, container, operation string }

func (r *claimingShipRepo) ClaimShip(_ context.Context, ship, container string, _ shared.PlayerID, operation string) error {
	if r.claimErr != nil {
		return r.claimErr
	}
	r.claimed = append(r.claimed, claimCall{ship, container, operation})
	r.claimedOK = true
	return nil
}

func (r *claimingShipRepo) ReleaseContainerClaim(_ context.Context, ship string, _ shared.PlayerID, _ string) (string, error) {
	r.released = append(r.released, ship)
	return "", nil
}

// --- hull builders -----------------------------------------------------------------------------

// pairingHull builds an IDLE hull standing at waypoint with a dedicated_fleet tag. It exists
// alongside reclaimHull because reclaimHull parks every hull at one fixed waypoint, and WHERE a hull
// stands is the entire subject here.
func pairingHull(t *testing.T, symbol, waypoint, fleet string) *navigation.Ship {
	t.Helper()
	return pairingHullWithStatus(t, symbol, waypoint, fleet, navigation.NavStatusDocked)
}

func pairingHullWithStatus(t *testing.T, symbol, waypoint, fleet string, status navigation.NavStatus) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	wp, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), wp, fuel, 100, 40, cargo, 30,
		"FRAME_PROBE", "SATELLITE", nil, status)
	require.NoError(t, err)
	if fleet != "" {
		ship.SetDedicatedFleet(fleet)
	}
	return ship
}

// pairingBusyHull is a hull a container already holds — not idle, so not a buyer.
func pairingBusyHull(t *testing.T, symbol, waypoint, fleet string) *navigation.Ship {
	t.Helper()
	ship := pairingHull(t, symbol, waypoint, fleet)
	require.NoError(t, ship.AssignToContainer("sensing-1", shared.NewRealClock()))
	require.False(t, ship.IsIdle(), "the fixture must actually be busy, or it proves nothing")
	return ship
}

// liveFrameReader builds the reader over two priced heavy yards: a cheap one and a dearer one.
func liveFrameReader(t *testing.T, ships []*navigation.Ship) *fleetYardPriceReader {
	t.Helper()
	dear, err := shared.NewWaypoint("X1-KP46-A1", 0, 0)
	require.NoError(t, err)
	cheap, err := shared.NewWaypoint("X1-FH57-B10A", 0, 0)
	require.NoError(t, err)
	return &fleetYardPriceReader{
		med:      &fakeYardAskMediator{askByWaypoint: map[string]int{"X1-KP46-A1": 1_600_000, "X1-FH57-B10A": 1_200_000}},
		shipRepo: &fakeHeavyShipRepo{all: ships},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{
			"X1-KP46": {dear},
			"X1-FH57": {cheap},
		}},
	}
}

// --- the yard side -----------------------------------------------------------------------------

// THE PIVOTAL CASE. A hull stands on the cheap yard, but it belongs to the sensing fleet and the
// purchase claim would refuse it; the dearer yard has an undedicated hull standing on it. The target
// must be the dearer yard — the one we can transact at without moving anything — and the
// cheapest-KNOWN ask must still name the cheap one so the premium the fleet pays for presence stays
// visible to the guard and to ObserveHeavyPricePremium.
func TestYardPrice_TargetsTheYardWeAlreadyStandOn(t *testing.T) {
	r := liveFrameReader(t, []*navigation.Ship{
		pairingHull(t, "TORWINDSTG-9", "X1-KP46-A1", ""),
		pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag),
	})

	price, cheapest, yard, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, false)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, "X1-KP46-A1", yard, "the target must be a yard a CLAIMABLE hull already stands on")
	require.Equal(t, int64(1_600_000), price, "the reservation must save toward the yard we can actually transact at")
	require.Equal(t, int64(1_200_000), cheapest, "cheapest-KNOWN keeps naming the true minimum — the premium guard and metric judge against it")
}

// A hull standing on the cheap yard that the claim WOULD admit makes that yard targetable again, so
// the fleet is never made to overpay for nothing. The mirror of the pivotal case, and the proof that
// claimability is what filters.
func TestYardPrice_AClaimableHullOnTheCheapYardWinsIt(t *testing.T) {
	r := liveFrameReader(t, []*navigation.Ship{
		pairingHull(t, "TORWINDSTG-9", "X1-KP46-A1", ""),
		pairingHull(t, "TORWINDSTG-2", "X1-FH57-B10A", ""),
	})

	price, _, yard, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, false)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, "X1-FH57-B10A", yard)
	require.Equal(t, int64(1_200_000), price)
}

// STANDING IS WAYPOINT-EXACT. The purchase path skips its navigation step only on an exact waypoint
// match, so a hull elsewhere in the yard's own system is still a flight and must not qualify it.
func TestYardPrice_SameSystemIsNotStanding(t *testing.T) {
	r := liveFrameReader(t, []*navigation.Ship{
		pairingHull(t, "TORWINDSTG-1", "X1-KP46-A2", navigation.PurchasingFleet), // A2, not the yard A1
	})

	_, _, _, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, false)
	require.NoError(t, err)
	require.False(t, readable, "a hull one waypoint away would have to fly — that is not a yard we can buy at")
}

// THE LIVE STATE, and it must fail CLOSED: every priced yard is occupied only by hulls another fleet
// owns. No price is invented, so guardPrice refuses and the tick simply does not buy (RULINGS #4).
// The purchase does not silently fall back to flying a hull in.
func TestYardPrice_OnlyForeignFleetHullsStanding_StaysFailClosed(t *testing.T) {
	r := liveFrameReader(t, []*navigation.Ship{
		pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag),
		pairingHull(t, "TORWINDSTG-87", "X1-KP46-A1", sensingFleetTag),
	})

	price, _, yard, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, false)
	require.NoError(t, err)
	require.False(t, readable)
	require.Zero(t, price)
	require.Empty(t, yard)
}

// The scanned-yard fallback carries the same rule: a candidate we do not stand on is skipped, and
// cheapest still spans EVERY candidate so narrowing the target can never loosen the premium cap.
func TestYardPrice_ScannedFallback_SkipsCandidatesWeDoNotStandOn(t *testing.T) {
	scanned := &fakeScannedYards{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-FH57", WaypointSymbol: "X1-FH57-B10A", ShipType: pairingHeavyType, PurchasePrice: 1_200_000, Hops: 1},
		{SystemSymbol: "X1-NEAR", WaypointSymbol: "X1-NEAR-Y1", ShipType: pairingHeavyType, PurchasePrice: 1_700_000, Hops: 2},
	}}
	r := &fleetYardPriceReader{
		med: &fakeYardAskMediator{},
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{
			pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag), // present, not claimable
			pairingHull(t, "TORWINDSTG-2", "X1-NEAR-Y1", ""),                 // the one we can transact with
		}},
		waypointRepo: &fakeSystemYardLister{}, // no live in-system yard → the fallback runs
		scannedYards: scanned,
	}

	price, cheapest, yard, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, false)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, "X1-NEAR-Y1", yard, "the nearest candidate we STAND ON, not the nearest on the map")
	require.Equal(t, int64(1_700_000), price)
	require.Equal(t, int64(1_200_000), cheapest, "cheapest spans every candidate — narrowing the target never widens the premium cap")
	require.Equal(t, []string{"X1-FH57", "X1-NEAR"}, scanned.gotFrom, "the rank itself is still asked from the fleet's occupied systems")
}

// The live walk's own cheapest ask travels DOWN into the fallback. A priced yard nothing stands on
// is not a target, but it is still the cheapest ask KNOWN — dropping it here would leave the premium
// guard judging against a higher floor, which is a money guard silently loosened (RULINGS #4).
func TestYardPrice_FallbackKeepsTheLiveWalksCheapestKnownAsk(t *testing.T) {
	unoccupied, err := shared.NewWaypoint("X1-FH57-B10A", 0, 0)
	require.NoError(t, err)
	r := &fleetYardPriceReader{
		med: &fakeYardAskMediator{askByWaypoint: map[string]int{"X1-FH57-B10A": 1_100_000}},
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{
			pairingHull(t, "TORWINDSTG-86", "X1-FH57-A1", sensingFleetTag), // puts X1-FH57 in the walk
			pairingHull(t, "TORWINDSTG-2", "X1-NEAR-Y1", ""),
		}},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{"X1-FH57": {unoccupied}}},
		scannedYards: &fakeScannedYards{candidates: []shipyardQueries.YardCandidate{
			{SystemSymbol: "X1-NEAR", WaypointSymbol: "X1-NEAR-Y1", ShipType: pairingHeavyType, PurchasePrice: 1_700_000, Hops: 1},
		}},
	}

	price, cheapest, yard, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, false)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, "X1-NEAR-Y1", yard)
	require.Equal(t, int64(1_700_000), price)
	require.Equal(t, int64(1_100_000), cheapest, "the live walk's unoccupied ask is still the cheapest KNOWN one")
}

// --- the buyer side ----------------------------------------------------------------------------

// pairingBuyer wires the shared buy primitive over a fleet, reusing the SAME mediator and repository
// fakes the primitive's existing regression net drives it with.
func pairingBuyer(fleet []*navigation.Ship) (*fleetHullPurchaser, *fakeReclaimShipRepo, *recordingBuyMediator) {
	repo := &fakeReclaimShipRepo{all: fleet}
	med := &recordingBuyMediator{}
	return &fleetHullPurchaser{med: med, shipRepo: repo}, repo, med
}

// The hull standing at the ordered yard signs for it — not the `purchasing` frigate, which would
// have to fly. A probe can sign for a heavy freighter; the yard sells to whoever is docked.
func TestHullPurchaser_TheHullStandingAtTheYardSigns(t *testing.T) {
	p, repo, med := pairingBuyer([]*navigation.Ship{
		pairingHull(t, "TORWINDSTG-1", "X1-KP46-A2", navigation.PurchasingFleet),
		pairingHull(t, "TORWINDSTG-2", "X1-FH57-B10A", ""),
	})

	_, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{
		PlayerID: 1, Class: hullbuy.HullClassHeavy, ShipType: pairingHeavyType, Yard: "X1-FH57-B10A", ExpectedPrice: 1_200_000,
	})
	require.NoError(t, err)

	used, dispatched := purchasingHullUsed(med)
	require.True(t, dispatched)
	require.Equal(t, "TORWINDSTG-2", used, "the hull already at the counter buys; nothing flies")
	require.Equal(t, []assignFleetCall{{symbol: "TORWIND-99", fleet: "trade"}}, repo.assigned,
		"the dedicate-at-purchase stamp is unchanged")
}

// NOTHING STANDS THERE ⇒ REFUSE, and name it. The buy must never quietly fly a hull in: no purchase
// command is dispatched at all, so no travel is bought and no navigation can fail.
func TestHullPurchaser_NothingStandingAtTheYard_RefusesAndNamesIt(t *testing.T) {
	p, _, med := pairingBuyer([]*navigation.Ship{
		pairingHull(t, "TORWINDSTG-1", "X1-KP46-A2", navigation.PurchasingFleet),
	})

	_, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{
		PlayerID: 1, Class: hullbuy.HullClassHeavy, ShipType: pairingHeavyType, Yard: "X1-FH57-B10A",
	})
	require.ErrorContains(t, err, "no claimable hull of ours stands at X1-FH57-B10A")
	require.False(t, med.purchaseAttempted(), "a buy with nobody at the counter must never reach the money path")
}

// A hull standing there that another fleet owns is NOT a buyer: ClaimShip would refuse it, and the
// engine has no authority to release it (RULINGS #7).
func TestHullPurchaser_ForeignFleetHullAtTheYard_IsNotABuyer(t *testing.T) {
	// A claimable hull exists elsewhere, so the refusal below is about WHO stands at the yard rather
	// than about the fleet having no buyer at all.
	p, _, med := pairingBuyer([]*navigation.Ship{
		pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag),
		pairingHull(t, "TORWINDSTG-1", "X1-KP46-A2", navigation.PurchasingFleet),
	})

	_, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{
		PlayerID: 1, Class: hullbuy.HullClassHeavy, ShipType: pairingHeavyType, Yard: "X1-FH57-B10A",
	})
	require.ErrorContains(t, err, "no claimable hull of ours stands at X1-FH57-B10A")
	require.False(t, med.purchaseAttempted())
}

// --- the shared eligible-buyer rule ------------------------------------------------------------

// THE PREDICATE MIRRORS ClaimShip. A purchase container claims its buyer under the `purchasing`
// operation, and that claim admits only an undedicated hull or one already dedicated to that fleet;
// every other pin is a standing refusal the engine cannot lift. Selecting a hull the claim would
// reject buys nothing and risks taking a hull from the fleet that owns it.
func TestPurchaseBuyers_MirrorsTheClaimRule(t *testing.T) {
	inTransit := pairingHullWithStatus(t, "TORWINDSTG-7", "X1-FH57-B10A", "", navigation.NavStatusInTransit)
	require.True(t, inTransit.IsInTransit(), "the fixture must actually be flying, or it proves nothing")

	buyers := purchaseBuyers([]*navigation.Ship{
		pairingHull(t, "TORWINDSTG-9", "X1-KP46-A1", ""),
		pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag), // a SPARE probe — borrowed, last
		pairingHull(t, "TORWINDSTG-4", "X1-KP46-A1", "trade"),            // another fleet's — never
		pairingBusyHull(t, "TORWINDSTG-5", "X1-KP46-A1", ""),             // held by a container
		inTransit, // location is its DESTINATION
		pairingHull(t, "TORWINDSTG-1", "X1-KP46-A1", navigation.PurchasingFleet),
	}, nil, true)

	require.Equal(t, []string{"TORWINDSTG-1", "TORWINDSTG-9", "TORWINDSTG-86"}, buyerSymbols(buyers),
		"the exclusive purchasing hull, then undedicated idle hulls, then spare sensing probes; a trade hull is never a buyer")
	require.False(t, buyers[0].Borrowed)
	require.True(t, buyers[2].Borrowed, "a sensing probe is BORROWED — it needs a claim under its own fleet")
}

// IN TRANSIT IS NOT STANDING. A flying hull's location is its DESTINATION, so location alone reads a
// hull halfway across the map as already at the yard it is heading for.
func TestStandingBuyerAt_InTransitIsNotStanding(t *testing.T) {
	flying := pairingHullWithStatus(t, "TORWINDSTG-7", "X1-FH57-B10A", "", navigation.NavStatusInTransit)
	_, standing := standingBuyerAt(purchaseBuyers([]*navigation.Ship{flying}, nil, true), "X1-FH57-B10A")
	require.False(t, standing)
}

// --- the borrowed sensing probe ----------------------------------------------------------------

// THE TIER THAT MAKES THE ENGINE ABLE TO FIRE. Every hull standing on a priced heavy yard belongs to
// the sensing pool, so the yard is targetable only because a SPARE probe there may sign for it.
func TestYardPrice_ASpareSensingProbeMakesItsYardTargetable(t *testing.T) {
	r := liveFrameReader(t, []*navigation.Ship{
		pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag),
	})
	r.posts = emptyRoster()

	price, _, yard, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, false)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, "X1-FH57-B10A", yard)
	require.Equal(t, int64(1_200_000), price)
}

// AN UNREADABLE ROSTER REFUSES THE BORROW. Reported as "no post mans anything" it would read as
// every probe being spare, which is the one reading that empties the sensing fleet. The other tiers
// are untouched, so a roster outage degrades the buy rather than stopping it.
func TestYardPrice_UnreadableScoutPostRoster_RefusesTheBorrowedTier(t *testing.T) {
	r := liveFrameReader(t, []*navigation.Ship{
		pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag),
	})
	r.posts = &fakeScoutPostRoster{err: errors.New("roster unreadable")}

	_, _, _, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, false)
	require.NoError(t, err)
	require.False(t, readable, "an unreadable roster must never read as every probe being spare")
}

// A PROBE A LIVE SCOUT POST NAMES IS NOT SPARE, even standing idle on the yard between tours. This
// is the predicate that keeps a working scout on station, and it is the whole reason the borrow
// consults the roster rather than the ship row alone.
func TestYardPrice_AProbeManningAScoutPostIsNotBorrowable(t *testing.T) {
	r := liveFrameReader(t, []*navigation.Ship{
		pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag),
	})
	r.posts = &fakeScoutPostRoster{posts: []*domainScouting.ScoutPost{
		{SystemSymbol: "X1-FH57", Hulls: 1, AssignedHull: "TORWINDSTG-86"},
	}}

	_, _, _, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, false)
	require.NoError(t, err)
	require.False(t, readable, "a probe a live post names is that post's, idle or not")
}

// The same rule through the EXTRA manning slots: a multi-hull post's second probe is no more
// available than its first.
func TestPurchaseBuyers_AProbeInAPostsExtraSlotIsNotSpare(t *testing.T) {
	manned := map[string]bool{"TORWINDSTG-87": true}
	buyers := purchaseBuyers([]*navigation.Ship{
		pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag),
		pairingHull(t, "TORWINDSTG-87", "X1-FH57-B10A", sensingFleetTag),
	}, manned, true)

	require.Equal(t, []string{"TORWINDSTG-86"}, buyerSymbols(buyers))
}

// The buy CLAIMS a borrowed probe under its OWN dedication — the claim ClaimShip admits without any
// tag changing hands — and HANDS IT BACK when the purchase is done.
func TestHullPurchaser_BorrowedProbeIsClaimedUnderItsOwnFleetAndReturned(t *testing.T) {
	repo := &claimingShipRepo{fakeReclaimShipRepo: fakeReclaimShipRepo{all: []*navigation.Ship{
		pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag),
	}}}
	med := &recordingBuyMediator{}
	p := &fleetHullPurchaser{med: med, shipRepo: repo, posts: emptyRoster()}

	_, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{
		PlayerID: 1, Class: hullbuy.HullClassHeavy, ShipType: pairingHeavyType,
		Yard: "X1-FH57-B10A", ContainerID: "growth-1",
	})
	require.NoError(t, err)

	used, dispatched := purchasingHullUsed(med)
	require.True(t, dispatched)
	require.Equal(t, "TORWINDSTG-86", used, "the probe standing at the yard signs")
	require.Equal(t, []claimCall{{ship: "TORWINDSTG-86", container: "growth-1", operation: sensingFleetTag}}, repo.claimed,
		"the claim is under the hull's OWN dedication — nothing is poached and no tag changes")
	require.Equal(t, []string{"TORWINDSTG-86"}, repo.released, "the hull ends the tick as available to sensing as it began")
}

// NO OWNING CONTAINER ⇒ NO BORROW. The claim column carries a foreign key, so a made-up owner has no
// row to reference; refusing costs one tick, and a borrowed hull held without a claim is what lets
// the owning engine fly it out mid-purchase. Nothing is dispatched.
func TestHullPurchaser_BorrowedProbeWithoutAnOwningContainer_RefusesFailClosed(t *testing.T) {
	repo := &claimingShipRepo{fakeReclaimShipRepo: fakeReclaimShipRepo{all: []*navigation.Ship{
		pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag),
	}}}
	med := &recordingBuyMediator{}
	p := &fleetHullPurchaser{med: med, shipRepo: repo, posts: emptyRoster()}

	_, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{
		PlayerID: 1, Class: hullbuy.HullClassHeavy, ShipType: pairingHeavyType, Yard: "X1-FH57-B10A",
	})
	require.ErrorContains(t, err, "no owning container id")
	require.False(t, med.purchaseAttempted())
	require.Empty(t, repo.claimed)
}

// A LOST CLAIM RACE FAILS THE BUY CLOSED — never a second concurrent driver over one hull.
func TestHullPurchaser_BorrowedProbeClaimRace_FailsClosed(t *testing.T) {
	repo := &claimingShipRepo{
		fakeReclaimShipRepo: fakeReclaimShipRepo{all: []*navigation.Ship{
			pairingHull(t, "TORWINDSTG-86", "X1-FH57-B10A", sensingFleetTag),
		}},
		claimErr: errors.New("already assigned"),
	}
	med := &recordingBuyMediator{}
	p := &fleetHullPurchaser{med: med, shipRepo: repo, posts: emptyRoster()}

	_, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{
		PlayerID: 1, Class: hullbuy.HullClassHeavy, ShipType: pairingHeavyType,
		Yard: "X1-FH57-B10A", ContainerID: "growth-1",
	})
	require.ErrorContains(t, err, "claim failed")
	require.False(t, med.purchaseAttempted())
	require.Empty(t, repo.released, "a claim that never succeeded must not be released")
}

// AN UNDEDICATED SIGNER IS NOT BORROWED, so it takes no claim — the untouched pre-existing path.
func TestHullPurchaser_UndedicatedSignerTakesNoClaim(t *testing.T) {
	repo := &claimingShipRepo{fakeReclaimShipRepo: fakeReclaimShipRepo{all: []*navigation.Ship{
		pairingHull(t, "TORWINDSTG-2", "X1-FH57-B10A", ""),
	}}}
	p := &fleetHullPurchaser{med: &recordingBuyMediator{}, shipRepo: repo, posts: emptyRoster()}

	_, err := p.BuyAndDedicate(context.Background(), hullbuy.BuyOrder{
		PlayerID: 1, Class: hullbuy.HullClassHeavy, ShipType: pairingHeavyType, Yard: "X1-FH57-B10A",
	})
	require.NoError(t, err)
	require.Empty(t, repo.claimed)
}

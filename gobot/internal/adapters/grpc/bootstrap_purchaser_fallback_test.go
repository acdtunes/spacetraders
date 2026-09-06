package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE COLD-START DEADLOCK. The first-hauler pivot names the command frigate as purchaser, and the
// command frigate is also the fleet's only earner: the trade coordinator relaunches a tour on it
// within seconds, so by the time the buy executes the hull is claimed again. The named purchaser had
// no fallback, so 29 consecutive trade-seed buys died on the ownership refusal while a satellite sat
// docked at the target yard doing nothing, and the fleet could not grow.
//
// The refusal is right and stays: the buy path navigates + docks its purchaser through handlers that
// consult no claim, so flying a hull another writer owns is a silent write (RULINGS #3/#7). What was
// missing is the question AFTER it — is some OTHER hull free to do this buy? — which the anonymous
// path has always asked.

// dockedAtYard builds an unowned hull standing at a waypoint: no container claim, no captain
// reservation, nothing to interrupt.
func dockedAtYard(t *testing.T, symbol, yard, role string) *navigation.Ship {
	t.Helper()
	return shipyardHull(t, symbol, yard, "", role, navigation.NavStatusDocked)
}

// touringFrigate is the live shape: the command frigate, purchasing-dedicated by the pivot, already
// re-claimed by the tour the trade coordinator relaunched on it.
func touringFrigate(t *testing.T, symbol, container string) *navigation.Ship {
	t.Helper()
	frigate := newIdleTradeShip(t, symbol, 1)
	frigate.SetDedicatedFleet(navigation.PurchasingFleet)
	require.NoError(t, frigate.AssignToContainer(container, shared.NewRealClock()))
	return frigate
}

// THE LIVE SHAPE, FIXED: TORWIND-1 is running tour-run-TORWIND-1-37cb76b5 and TORWIND-5, a satellite,
// is docked at X1-UK80-A2 — the yard the buy won. The satellite buys, the frigate keeps its tour.
func TestBootstrapBuy_NamedPurchaserOwnedElsewhereFallsBackToTheHullStandingAtTheYard(t *testing.T) {
	frigate := touringFrigate(t, "TORWIND-1", "tour-run-TORWIND-1-37cb76b5")
	// A parked probe the buy would otherwise have to FLY to the yard, listed first so the pick cannot
	// be the incidental result of roster order.
	parkedElsewhere := dockedAtYard(t, "TORWIND-4", "X1-UK80-C9", "SATELLITE")
	satellite := dockedAtYard(t, "TORWIND-5", "X1-UK80-A2", "SATELLITE")

	med := &recordingBuyMediator{}
	logged := &recordingLogger{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{frigate, parkedElsewhere, satellite})

	result, err := acquirer.buyWith(loggerContext(logged), 1, "SHIP_LIGHT_HAULER", "X1-UK80-A2", "TORWIND-1")

	require.NoError(t, err, "a hull standing at the yard must be able to buy when the named purchaser is busy")
	require.Equal(t, "TORWIND-5", med.purchaserUsed(), "the satellite at the counter is the substitute")
	require.Equal(t, "TORWIND-99", result.ShipSymbol)
	require.Equal(t, "tour-run-TORWIND-1-37cb76b5", frigate.ContainerID(),
		"the running tour's claim must be left untouched — a fallback, never a claim override")

	fallbacks := logged.actions("bootstrap_purchaser_fallback")
	require.Len(t, fallbacks, 1, "a substituted purchaser must be visible in the log, not silent")
	require.Equal(t, "INFO", fallbacks[0]["level"])
	require.Equal(t, "TORWIND-1", fallbacks[0]["purchaser"])
	require.Equal(t, "tour-run-TORWIND-1-37cb76b5", fallbacks[0]["owner"])
	require.Equal(t, "TORWIND-5", fallbacks[0]["substitute"])
}

// FAIL CLOSED. With nothing standing at the yard and nothing idle, the cascade is empty and the
// ownership refusal is what the caller gets — the named hull is still never flown, and the error now
// says both which hull is contested and that no alternative was there.
func TestBootstrapBuy_NamedPurchaserOwnedElsewhereStillFailsWhenNoHullIsFree(t *testing.T) {
	frigate := touringFrigate(t, "TORWIND-1", "tour-run-TORWIND-1-37cb76b5")
	elsewhere := dockedAtYard(t, "TORWIND-6", "X1-UK80-B7", "SATELLITE")
	require.NoError(t, elsewhere.AssignToContainer("scout-tour-TORWIND-6", shared.NewRealClock()))

	med := &recordingBuyMediator{}
	logged := &recordingLogger{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{frigate, elsewhere})

	_, err := acquirer.buyWith(loggerContext(logged), 1, "SHIP_LIGHT_HAULER", "X1-UK80-A2", "TORWIND-1")

	require.Error(t, err)
	require.Contains(t, err.Error(), `purchaser TORWIND-1 is assigned to container "tour-run-TORWIND-1-37cb76b5"`,
		"the original ownership refusal must survive the fallback attempt")
	require.Contains(t, err.Error(), "X1-UK80-A2", "the error must name the yard nothing was standing at")
	require.False(t, med.purchaseAttempted(), "no purchase may be dispatched when the cascade is empty")
	require.Empty(t, logged.actions("bootstrap_purchaser_fallback"),
		"an empty cascade is no fallback — logging one would report a substitution that never happened")
}

// THE FALLBACK NEVER FLIES A THIRD HULL. The cascade's last step — any idle hull — is a DIVERSION:
// PurchaseShipCommand navigates whatever purchaser it is handed, and that step has no DedicatedFleet
// filter, so reaching it from the named path would send a probe on station or a hauler between legs to
// a shipyard. With a purchaser already named and nothing standing at the counter, the answer is the
// refusal and a retry next tick, not somebody else's working hull.
func TestBootstrapBuy_NamedFallbackNeverDivertsAnIdleHullAwayFromItsJob(t *testing.T) {
	frigate := touringFrigate(t, "TORWIND-1", "tour-run-TORWIND-1-37cb76b5")
	idleFarAway := dockedAtYard(t, "TORWIND-4", "X1-UK80-C9", "SATELLITE") // idle, on station, not at the yard

	med := &recordingBuyMediator{}
	logged := &recordingLogger{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{frigate, idleFarAway})

	_, err := acquirer.buyWith(loggerContext(logged), 1, "SHIP_LIGHT_HAULER", "X1-UK80-A2", "TORWIND-1")

	require.Error(t, err, "with nothing at the counter the named buy must refuse, not divert a working hull")
	require.Contains(t, err.Error(), `purchaser TORWIND-1 is assigned to container "tour-run-TORWIND-1-37cb76b5"`)
	require.Contains(t, err.Error(), "no unowned hull was standing at X1-UK80-A2 to buy in its place")
	require.False(t, med.purchaseAttempted(), "no hull may be flown to a yard to serve a fallback")
	require.Empty(t, logged.actions("bootstrap_purchaser_fallback"))
}

// THE SAME SHAPE, ANONYMOUS: unchanged. With no purchaser named there is no committed hull to fall
// back from, so the last step still sends the idle hull — this is the pre-existing probe-buy path and
// the fix must not narrow it.
func TestBootstrapBuy_AnonymousStillFliesAnIdleHullWhenNothingIsAtTheYard(t *testing.T) {
	idleFarAway := dockedAtYard(t, "TORWIND-4", "X1-UK80-C9", "SATELLITE")

	med := &recordingBuyMediator{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{idleFarAway})

	_, err := acquirer.buyWith(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-UK80-A2", "")

	require.NoError(t, err, "the anonymous cascade keeps all four steps")
	require.Equal(t, "TORWIND-4", med.purchaserUsed())
}

// A named purchaser that is FREE is used exactly as before: no cascade, no substitute, no log line.
func TestBootstrapBuy_FreeNamedPurchaserIsUsedWithNoFallback(t *testing.T) {
	frigate := newIdleTradeShip(t, "TORWIND-1", 1)
	frigate.SetDedicatedFleet(navigation.PurchasingFleet)
	satellite := dockedAtYard(t, "TORWIND-5", "X1-UK80-A2", "SATELLITE")

	med := &recordingBuyMediator{}
	logged := &recordingLogger{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{frigate, satellite})

	_, err := acquirer.buyWith(loggerContext(logged), 1, "SHIP_LIGHT_HAULER", "X1-UK80-A2", "TORWIND-1")

	require.NoError(t, err)
	require.Equal(t, "TORWIND-1", med.purchaserUsed(), "a free named purchaser is still THE purchaser")
	require.Empty(t, logged.actions("bootstrap_purchaser_fallback"))
}

// THE ANONYMOUS CASCADE IS UNCHANGED IN ORDER: a free exclusive purchasing ship still outranks the
// hull standing at the yard, so the deterministic buy ship is not displaced by the new widened match.
func TestBootstrapBuy_AnonymousCascadeStillPrefersThePurchasingFleetHullOverAYardHull(t *testing.T) {
	atYard := dockedAtYard(t, "TORWIND-5", "X1-UK80-A2", "SATELLITE")
	buyShip := newIdleTradeShip(t, "TORWIND-1", 1)
	buyShip.SetDedicatedFleet(navigation.PurchasingFleet)

	med := &recordingBuyMediator{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{atYard, buyShip})

	_, err := acquirer.buyWith(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-UK80-A2", "")

	require.NoError(t, err)
	require.Equal(t, "TORWIND-1", med.purchaserUsed(),
		"the exclusive purchasing ship remains the first choice — the yard match sits below it")
}

// The captain-reserved sentinel still outranks an ordinary hull at the same counter, so the sentinel's
// narrower exemption is not swallowed by the wider match placed after it.
func TestBootstrapBuy_AnonymousCascadePrefersTheSentinelOverAnOrdinaryYardHull(t *testing.T) {
	ordinary := dockedAtYard(t, "TORWIND-5", "X1-UK80-A2", "SATELLITE")
	sentinel := dockedYardSentinel(t, "SENTINEL-1", "X1-UK80-A2")

	med := &recordingBuyMediator{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{ordinary, sentinel})

	_, err := acquirer.buyWith(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-UK80-A2", "")

	require.NoError(t, err)
	require.Equal(t, "SENTINEL-1", med.purchaserUsed())
}

// The yard match outranks the generic idle search even when the idle hull is found first, because a
// hull at the counter buys with no flight and no fuel while an idle hull elsewhere must be sent.
func TestBootstrapBuy_AnonymousCascadePrefersAYardHullOverAnIdleHullElsewhere(t *testing.T) {
	idleElsewhere := newIdleTradeShip(t, "TORWIND-7", 1)
	atYard := dockedAtYard(t, "TORWIND-5", "X1-UK80-A2", "SATELLITE")

	med := &recordingBuyMediator{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{idleElsewhere, atYard})

	_, err := acquirer.buyWith(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-UK80-A2", "")

	require.NoError(t, err)
	require.Equal(t, "TORWIND-5", med.purchaserUsed())
}

// --- the widened yard match itself -------------------------------------------------------------

// SAME WAYPOINT, DOCKED, UNOWNED — the three bounds that keep this from ever flying or poaching a hull.
func TestUnassignedHullAtYard_MatchesOnlyAnUnownedHullDockedAtThatExactYard(t *testing.T) {
	docked := dockedAtYard(t, "TORWIND-5", "X1-UK80-A2", "SATELLITE")
	require.Equal(t, "TORWIND-5", unassignedHullAtYard([]*navigation.Ship{docked}, "X1-UK80-A2"))

	orbiting := shipyardHull(t, "TORWIND-5", "X1-UK80-A2", "", "SATELLITE", navigation.NavStatusInOrbit)
	require.Empty(t, unassignedHullAtYard([]*navigation.Ship{orbiting}, "X1-UK80-A2"),
		"in orbit is not standing at the counter — the buy would have to dock it first")

	transit := shipyardHull(t, "TORWIND-5", "X1-UK80-A2", "", "SATELLITE", navigation.NavStatusInTransit)
	require.Empty(t, unassignedHullAtYard([]*navigation.Ship{transit}, "X1-UK80-A2"),
		"a hull in transit is not there yet")

	away := dockedAtYard(t, "TORWIND-5", "X1-UK80-B7", "SATELLITE")
	require.Empty(t, unassignedHullAtYard([]*navigation.Ship{away}, "X1-UK80-A2"),
		"a hull at another waypoint would have to be FLOWN, which this path never does")

	require.Empty(t, unassignedHullAtYard([]*navigation.Ship{docked}, ""),
		"with no yard named there is nothing to match a position against")
}

// OWNERSHIP IS NOT WEAKENED BY THE WIDENING: a hull another container is running, and a hull held on
// any captain reservation (the sentinel's own included — it has its own earlier, narrower branch), are
// both invisible here.
func TestUnassignedHullAtYard_SkipsEveryHullAnotherWriterOwns(t *testing.T) {
	claimed := dockedAtYard(t, "TORWIND-5", "X1-UK80-A2", "SATELLITE")
	require.NoError(t, claimed.AssignToContainer("scout-tour-TORWIND-5", shared.NewRealClock()))
	require.Empty(t, unassignedHullAtYard([]*navigation.Ship{claimed}, "X1-UK80-A2"),
		"a hull a container is running must never be taken for a buy")

	reserved := dockedAtYard(t, "TORWIND-6", "X1-UK80-A2", "SATELLITE")
	require.NoError(t, reserved.ReserveByCaptain("operator errand", shared.NewRealClock()))
	require.Empty(t, unassignedHullAtYard([]*navigation.Ship{reserved}, "X1-UK80-A2"),
		"an operator's reservation is someone else's hull")

	sentinel := dockedYardSentinel(t, "SENTINEL-1", "X1-UK80-A2")
	require.Empty(t, unassignedHullAtYard([]*navigation.Ship{sentinel}, "X1-UK80-A2"),
		"the sentinel is matched by its own reason-scoped branch, not by this one")
}

// A SCOUT BEFORE AN EARNER. Both are standing at the counter and both are free, but taking the hauler
// costs the fleet a hull that could be trading; the satellite costs nothing.
func TestUnassignedHullAtYard_PrefersAScoutOverAnEarner(t *testing.T) {
	hauler := dockedAtYard(t, "TORWIND-4", "X1-UK80-A2", "HAULER")
	satellite := dockedAtYard(t, "TORWIND-5", "X1-UK80-A2", "SATELLITE")

	require.Equal(t, "TORWIND-5", unassignedHullAtYard([]*navigation.Ship{hauler, satellite}, "X1-UK80-A2"),
		"the satellite wins even when the hauler is found first")
	require.Equal(t, "TORWIND-4", unassignedHullAtYard([]*navigation.Ship{hauler}, "X1-UK80-A2"),
		"with no scout there the free earner at the counter still beats blocking the buy")
}

// AND AN UNTAGGED HULL BEFORE A DEDICATED ONE. The fleet tag is what marks a hull as belonging to a
// workstream, so among non-scouts at the counter the one no workstream has claimed goes first.
func TestUnassignedHullAtYard_PrefersAnUntaggedHullOverADedicatedOne(t *testing.T) {
	contract := dockedAtYard(t, "TORWIND-4", "X1-UK80-A2", "HAULER")
	contract.SetDedicatedFleet(contractFleetTag)
	spare := dockedAtYard(t, "TORWIND-7", "X1-UK80-A2", "HAULER")

	require.Equal(t, "TORWIND-7", unassignedHullAtYard([]*navigation.Ship{contract, spare}, "X1-UK80-A2"),
		"the untagged hull wins even when the contract hauler is found first")
	require.Equal(t, "TORWIND-4", unassignedHullAtYard([]*navigation.Ship{contract}, "X1-UK80-A2"),
		"a dedicated hull standing right there still beats blocking the buy — it is not flown, only used")
}

// The sentinel exemption is untouched by the fallback: a NAMED purchaser is still judged, and a
// contested one falls back onto the sentinel exactly as the anonymous path would.
func TestBootstrapBuy_FallbackCanLandOnTheYardSentinelWithoutReleasingIt(t *testing.T) {
	frigate := touringFrigate(t, "TORWIND-1", "tour-run-TORWIND-1-37cb76b5")
	sentinel := dockedYardSentinel(t, "SENTINEL-1", "X1-UK80-A2")

	med := &recordingBuyMediator{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{frigate, sentinel})

	_, err := acquirer.buyWith(loggerContext(&recordingLogger{}), 1, "SHIP_LIGHT_HAULER", "X1-UK80-A2", "TORWIND-1")

	require.NoError(t, err)
	require.Equal(t, "SENTINEL-1", med.purchaserUsed())
	require.True(t, sentinel.IsReservedByCaptain(), "the sentinel's watch must survive the buy")
	require.Equal(t, bootstrapCmd.YardSentinelReservationReason, sentinel.CaptainReservationReason())
}

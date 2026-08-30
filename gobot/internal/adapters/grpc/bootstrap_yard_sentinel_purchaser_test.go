package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE SENTINEL BUYS AT ITS OWN COUNTER, and only there. It is docked at the yard but held on
// bootstrap's own captain reservation, so it reads neither idle nor unowned — yet a purchase needs
// only a hull standing at a Shipyard-trait waypoint. Eligible for a buy at the yard it is docked at
// and nowhere else: never dispatched, no navigate, no dock, still on watch when the buy returns.

func dockedYardSentinel(t *testing.T, symbol, yard string) *navigation.Ship {
	t.Helper()
	ship := shipyardHull(t, symbol, yard, "", "SATELLITE", navigation.NavStatusDocked)
	require.NoError(t, ship.ReserveByCaptain(bootstrapCmd.YardSentinelReservationReason, shared.NewRealClock()))
	return ship
}

// purchaserUsed names the hull the buy was dispatched with, "" when none was.
func (m *recordingBuyMediator) purchaserUsed() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.sent {
		if b, ok := r.(*shipyardCmd.BatchPurchaseShipsCommand); ok {
			return b.PurchasingShipSymbol
		}
	}
	return ""
}

func TestBootstrapBuy_YardSentinelExecutesABuyAtItsOwnYard(t *testing.T) {
	sentinel := dockedYardSentinel(t, "SENTINEL-1", "X1-HQ-YARD")
	touring := newIdleTradeShip(t, "FRIGATE-1", 1)
	require.NoError(t, touring.AssignToContainer("trade_fleet_coordinator-FRIGATE-1", shared.NewRealClock()))

	med := &recordingBuyMediator{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{sentinel, touring})

	result, err := acquirer.buyWith(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-HQ-YARD", "")

	require.NoError(t, err, "the hull already standing at the yard must be able to execute the buy")
	require.Equal(t, "SENTINEL-1", med.purchaserUsed(),
		"the sentinel is the purchaser — no earning hull is diverted for a buy it is already parked for")
	require.Equal(t, "TORWIND-99", result.ShipSymbol)
}

// IT STAYS ON WATCH: still captain-reserved under bootstrap's own reason and still docked at the
// yard. The reservation is never released, so there is no window in which it is claimable.
func TestBootstrapBuy_YardSentinelIsStillStandingWatchAfterTheBuy(t *testing.T) {
	sentinel := dockedYardSentinel(t, "SENTINEL-1", "X1-HQ-YARD")

	med := &recordingBuyMediator{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{sentinel})

	_, err := acquirer.buyWith(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-HQ-YARD", "")
	require.NoError(t, err)

	require.True(t, sentinel.IsReservedByCaptain(), "the captain reservation must survive the buy untouched")
	require.Equal(t, bootstrapCmd.YardSentinelReservationReason, sentinel.CaptainReservationReason())
	require.True(t, sentinel.IsDocked(), "the sentinel must still be docked at its yard")
	require.Equal(t, "X1-HQ-YARD", sentinel.CurrentLocation().Symbol)
	require.Empty(t, sentinel.DedicatedFleet(), "buying must never give the sentinel a fleet tag")
}

// THE SAME-WAYPOINT BOUND. Dispatching the sentinel would abandon the watch, so a buy at another
// yard fails closed (no spend) with nothing else free, and the next tick re-derives.
func TestBootstrapBuy_YardSentinelIsNeverUsedForABuyAtAnotherYard(t *testing.T) {
	sentinel := dockedYardSentinel(t, "SENTINEL-1", "X1-HQ-YARD-A")

	med := &recordingBuyMediator{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{sentinel})

	_, err := acquirer.buyWith(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-HQ-YARD-B", "")

	require.Error(t, err, "the sentinel must never be dispatched to another yard to make a purchase")
	require.False(t, med.purchaseAttempted(), "no purchase may be dispatched with a hull that is not there")
	require.Equal(t, "X1-HQ-YARD-A", sentinel.CurrentLocation().Symbol, "the watch must be left where it was")
}

// An unresolved yard (auto-discovery) can make no same-waypoint claim.
func TestBootstrapBuy_YardSentinelIsNotUsedWhenTheYardIsUnresolved(t *testing.T) {
	sentinel := dockedYardSentinel(t, "SENTINEL-1", "X1-HQ-YARD")

	require.Empty(t, yardSentinelAtYard([]*navigation.Ship{sentinel}, ""),
		"with no yard named there is nothing to match the sentinel's position against")
}

// DOCKED, not merely orbiting: an orbiting sentinel waits a tick for parkYardSentinel to dock it.
func TestBootstrapBuy_YardSentinelInOrbitIsNotYetAPurchaser(t *testing.T) {
	orbiting := shipyardHull(t, "SENTINEL-1", "X1-HQ-YARD", "", "SATELLITE", navigation.NavStatusInOrbit)
	require.NoError(t, orbiting.ReserveByCaptain(bootstrapCmd.YardSentinelReservationReason, shared.NewRealClock()))

	require.Empty(t, yardSentinelAtYard([]*navigation.Ship{orbiting}, "X1-HQ-YARD"))
}

// THE EXEMPTION IS SCOPED BY THE REASON STRING: an operator's manual `ship reserve` on a probe at
// the same yard is still refused, so the ownership gate is weakened for nobody (RULINGS #3/#7).
func TestBootstrapBuy_AnOrdinaryReservedHullAtTheYardIsStillRefused(t *testing.T) {
	manual := shipyardHull(t, "PROBE-9", "X1-HQ-YARD", "", "SATELLITE", navigation.NavStatusDocked)
	require.NoError(t, manual.ReserveByCaptain("operator errand", shared.NewRealClock()))

	require.Empty(t, yardSentinelAtYard([]*navigation.Ship{manual}, "X1-HQ-YARD"),
		"only the sentinel's own reason string identifies it — a manual reservation is someone else's hull")

	med := &recordingBuyMediator{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{manual})

	_, err := acquirer.buyWith(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-HQ-YARD", "PROBE-9")

	require.Error(t, err, "a hull reserved for something else must never be flown by the buy path")
	require.False(t, med.purchaseAttempted())
}

// PRECEDENCE. A free exclusive purchasing ship still wins; below it the sentinel outranks an
// incidentally-idle hull, buying with no flight, no fuel and no tour interrupted.
func TestBootstrapBuy_PurchaserPrecedence_PurchasingShipThenSentinelThenAnyIdleHull(t *testing.T) {
	sentinel := dockedYardSentinel(t, "SENTINEL-1", "X1-HQ-YARD")
	stray := newIdleTradeShip(t, "STRAY-1", 1) // idle, parked far from the yard

	medWithFrigate := &recordingBuyMediator{}
	buyShip := newIdleTradeShip(t, "FRIGATE-1", 1)
	buyShip.SetDedicatedFleet(navigation.PurchasingFleet)
	acq := newBuyAcquirer(medWithFrigate, []*navigation.Ship{sentinel, buyShip, stray})
	_, err := acq.buyWith(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-HQ-YARD", "")
	require.NoError(t, err)
	require.Equal(t, "FRIGATE-1", medWithFrigate.purchaserUsed(),
		"a free exclusive purchasing ship remains the deterministic buyer")

	medNoFrigate := &recordingBuyMediator{}
	acq2 := newBuyAcquirer(medNoFrigate, []*navigation.Ship{sentinel, stray})
	_, err = acq2.buyWith(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-HQ-YARD", "")
	require.NoError(t, err)
	require.Equal(t, "SENTINEL-1", medNoFrigate.purchaserUsed(),
		"the hull already at the yard buys before one is flown there")
}

// THE COUNTER EXCLUSIONS ARE UNDISTURBED: a sentinel folded into ProbeCount would under-shoot the
// scouting seed's `need := probeTarget - obs.ProbeCount` and mask a real scout lost later.
func TestObserveFleetShape_SentinelDockedAtAYardIsStillNoProbe(t *testing.T) {
	scoutA := homeProbe(t, "TORWIND-2", "X1-HQ-A1")
	require.NoError(t, scoutA.AssignToContainer("scout-tour-TORWIND-2", shared.NewRealClock()))
	scoutB := homeProbe(t, "TORWIND-3", "X1-HQ-B2")
	require.NoError(t, scoutB.AssignToContainer("scout-tour-TORWIND-3", shared.NewRealClock()))
	ships := []*navigation.Ship{dockedYardSentinel(t, "SENTINEL-1", "X1-HQ-YARD"), scoutA, scoutB}

	obs := bootstrapCmd.Observation{}
	observeFleetShape(ships, &obs)

	require.Equal(t, 2, obs.ProbeCount, "the purchaser-capable sentinel must still be no member of the scouting seed")
	require.Equal(t, 2, obs.ProbesScouting, "the sentinel must not be counted as a scout on tour either")
	require.Equal(t, "SENTINEL-1", obs.YardSentinelSymbol)
	require.False(t, obs.HasIdlePurchaser,
		"a captain-reserved sentinel is not an idle hull, and must not be reported as one — it buys on its own narrower path")
}

// --- the observation the readiness gates read -------------------------------------------------

func yardSet(yards ...string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, y := range yards {
		set[y] = struct{}{}
	}
	return set
}

// The observation must carry the WAYPOINT, not just the fact of being parked: it is what a buy's
// winning yard is matched against.
func TestSentinelDockedYard_RecordsTheYardItStandsAt(t *testing.T) {
	sentinel := dockedYardSentinel(t, "SENTINEL-1", "X1-HQ-YARD")

	got := sentinelDockedYard([]*navigation.Ship{sentinel}, "SENTINEL-1", yardSet("X1-HQ-YARD", "X1-HQ-YARD-2"))

	require.Equal(t, "X1-HQ-YARD", got)
}

// Every other shape reads as no yard, so the readiness gates block rather than pass on a hull that
// cannot buy — fail-safe, and never a spend either way.
func TestSentinelDockedYard_EmptyForEveryShapeThatCannotBuy(t *testing.T) {
	orbiting := shipyardHull(t, "SENTINEL-1", "X1-HQ-YARD", "", "SATELLITE", navigation.NavStatusInOrbit)
	require.NoError(t, orbiting.ReserveByCaptain(bootstrapCmd.YardSentinelReservationReason, shared.NewRealClock()))
	require.Empty(t, sentinelDockedYard([]*navigation.Ship{orbiting}, "SENTINEL-1", yardSet("X1-HQ-YARD")),
		"in orbit is not standing at the counter")

	elsewhere := dockedYardSentinel(t, "SENTINEL-1", "X1-HQ-A1")
	require.Empty(t, sentinelDockedYard([]*navigation.Ship{elsewhere}, "SENTINEL-1", yardSet("X1-HQ-YARD")),
		"docked at a non-shipyard waypoint is not parked at a yard")

	require.Empty(t, sentinelDockedYard(nil, "SENTINEL-1", yardSet("X1-HQ-YARD")),
		"a sentinel absent from the roster cannot be shown to be anywhere")
}

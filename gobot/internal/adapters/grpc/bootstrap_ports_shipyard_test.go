package grpc

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/stretchr/testify/require"
)

// hullToSend is the whole decision behind EnsureShipyardReadable: which hull (if any) flies to the home
// yard to make its presence-gated listing price. It answers two different questions depending on whether
// the caller has already committed a purchaser, and both answers are load-bearing — one keeps the
// first-hauler pivot moving, the other keeps bootstrap from poaching another controller's ship.

func shipyardHull(t *testing.T, symbol, waypoint, dedicatedFleet, role string, status navigation.NavStatus) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), loc, fuel, 100, 40, cargo, 30,
		"FRAME_FRIGATE", role, nil, status,
	)
	require.NoError(t, err)
	if dedicatedFleet != "" {
		ship.SetDedicatedFleet(dedicatedFleet)
	}
	return ship
}

func homeYards() map[string]struct{} { return map[string]struct{}{"X1-HQ-YARD": {}} }

// A NAMED purchaser is already committed to the buy, so only its OWN position excuses the trip: it goes
// even while purchasing-dedicated (the free-hull search would refuse it) and even while another hull
// happens to be standing at the yard (the buy needs THIS hull there). It stands down only when it is
// already at a yard, or already flying somewhere from an earlier tick — the idempotency that stops a
// re-navigation on every unreadable tick.
func TestHullToSend_NamedPurchaserGoesUnlessItsOwnPositionExcusesTheTrip(t *testing.T) {
	bystanderAtYard := shipyardHull(t, "PROBE-2", "X1-HQ-YARD", "", "SATELLITE", navigation.NavStatusInOrbit)

	cases := []struct {
		name      string
		purchaser *navigation.Ship
		others    []*navigation.Ship
		wantSend  string
	}{
		{
			name:      "purchasing-dedicated and elsewhere: goes",
			purchaser: shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", navigation.PurchasingFleet, commandRole, navigation.NavStatusDocked),
			wantSend:  "FRIGATE-1",
		},
		{
			name:      "another hull already at the yard: still goes (the buy needs THIS hull there)",
			purchaser: shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", navigation.PurchasingFleet, commandRole, navigation.NavStatusDocked),
			others:    []*navigation.Ship{bystanderAtYard},
			wantSend:  "FRIGATE-1",
		},
		{
			name:      "already standing at the yard: nothing moves (the price reads next tick)",
			purchaser: shipyardHull(t, "FRIGATE-1", "X1-HQ-YARD", navigation.PurchasingFleet, commandRole, navigation.NavStatusInOrbit),
		},
		{
			name:      "already flying from an earlier tick: nothing moves (no second nav)",
			purchaser: shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", navigation.PurchasingFleet, commandRole, navigation.NavStatusInTransit),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			send, ok := hullToSend(append([]*navigation.Ship{tc.purchaser}, tc.others...), homeYards(), "FRIGATE-1", "")
			require.Equal(t, tc.wantSend != "", ok)
			require.Equal(t, tc.wantSend, send)
		})
	}
}

// With no purchaser named, the search takes only a genuinely FREE hull — undedicated and not mid-flight —
// so a hull another controller owns is never poached (RULINGS #7), and it prefers the command frigate, the
// natural cold-start buyer. Any hull already standing at a yard stands the whole search down: the price
// reads next tick, so nothing needs to move.
func TestHullToSend_FreeHullSearchNeverPoachesAndStandsDownOnAHullAtTheYard(t *testing.T) {
	cases := []struct {
		name     string
		ships    []*navigation.Ship
		wantSend string
	}{
		{
			name: "prefers the command frigate over another free hull",
			ships: []*navigation.Ship{
				shipyardHull(t, "PROBE-2", "X1-HQ-A1", "", "SATELLITE", navigation.NavStatusInOrbit),
				shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", "", commandRole, navigation.NavStatusInOrbit),
			},
			wantSend: "FRIGATE-1",
		},
		{
			name: "a dedicated hull is never poached, however idle",
			ships: []*navigation.Ship{
				shipyardHull(t, "HAULER-3", "X1-HQ-A1", contractFleetTag, "HAULER", navigation.NavStatusInOrbit),
				shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", navigation.PurchasingFleet, commandRole, navigation.NavStatusInOrbit),
			},
		},
		{
			name: "a hull already flying is not a candidate — its trip is under way",
			ships: []*navigation.Ship{
				shipyardHull(t, "PROBE-2", "X1-HQ-A1", "", "SATELLITE", navigation.NavStatusInTransit),
			},
		},
		{
			name: "a hull standing at the yard stands the search down",
			ships: []*navigation.Ship{
				shipyardHull(t, "PROBE-2", "X1-HQ-YARD", "", "SATELLITE", navigation.NavStatusInOrbit),
				shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", "", commandRole, navigation.NavStatusInOrbit),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			send, ok := hullToSend(tc.ships, homeYards(), "", "")
			require.Equal(t, tc.wantSend != "", ok)
			require.Equal(t, tc.wantSend, send)
		})
	}
}

// Once every hull carries a fleet tag the free-hull search is empty by construction, and the yard can
// never be warmed again. A BORROW is the caller's last resort for exactly that, and it flies only while
// the LIVE roster still shows the lent hull free — so a tour that resumed since the caller looked is
// never interrupted (PLAYBOOK §9), a claim is never flown out from under its writer, and a hull that
// need not be lent is not.
func TestHullToSend_BorrowedHullFliesOnlyWhileItIsStillFree(t *testing.T) {
	idleFrigate := func() *navigation.Ship {
		return shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", tradeFleetTag, commandRole, navigation.NavStatusInOrbit)
	}
	touringProbe := shipyardHull(t, "PROBE-2", "X1-HQ-A1", tradeFleetTag, "SATELLITE", navigation.NavStatusInOrbit)
	claimedFrigate := idleFrigate()
	require.NoError(t, claimedFrigate.AssignToContainer("trade_fleet_coordinator-1", shared.NewRealClock()))

	cases := []struct {
		name     string
		ships    []*navigation.Ship
		borrow   string
		wantSend string
	}{
		{
			name:     "every hull tagged and the lent frigate idle between tours: it goes",
			ships:    []*navigation.Ship{touringProbe, idleFrigate()},
			borrow:   "FRIGATE-1",
			wantSend: "FRIGATE-1",
		},
		{
			name:   "mid-tour: never redirected",
			ships:  []*navigation.Ship{shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", tradeFleetTag, commandRole, navigation.NavStatusInTransit)},
			borrow: "FRIGATE-1",
		},
		{
			name:   "a live container claim holds it: never flown out from under that writer",
			ships:  []*navigation.Ship{claimedFrigate},
			borrow: "FRIGATE-1",
		},
		{
			name:   "not on the roster: fails closed",
			ships:  []*navigation.Ship{touringProbe},
			borrow: "FRIGATE-1",
		},
		{
			name:   "a hull already at the yard still stands the whole search down",
			ships:  []*navigation.Ship{shipyardHull(t, "PROBE-2", "X1-HQ-YARD", tradeFleetTag, "SATELLITE", navigation.NavStatusInOrbit), idleFrigate()},
			borrow: "FRIGATE-1",
		},
		{
			name:     "a genuinely free hull outranks the borrow — nothing is lent that need not be",
			ships:    []*navigation.Ship{shipyardHull(t, "PROBE-2", "X1-HQ-A1", "", "SATELLITE", navigation.NavStatusInOrbit), idleFrigate()},
			borrow:   "FRIGATE-1",
			wantSend: "PROBE-2",
		},
		{
			name:  "none offered: the wait stands rather than poach a tagged hull",
			ships: []*navigation.Ship{idleFrigate()},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			send, ok := hullToSend(tc.ships, homeYards(), "", tc.borrow)
			require.Equal(t, tc.wantSend != "", ok)
			require.Equal(t, tc.wantSend, send)
		})
	}
}

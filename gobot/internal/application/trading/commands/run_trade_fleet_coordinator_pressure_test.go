package commands

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The fleet-wide inventory-pressure governor (sp-tgll8 item 1): govern buy-rate by
// sell-rate. When the trade fleet is saturated with hulls sitting FULL of cargo they cannot
// offload, the coordinator PAUSES relaunching EMPTY idle hulls into NEW buying tours this
// tick (stop buying what the fleet cannot sell) — but ALWAYS relaunches LADEN idle hulls so
// they can drain their cargo (never block a sell, RULINGS #4). Because full hulls are laden,
// a saturated fleet keeps selling and the FULL fraction drains, un-throttling the governor.
// Conservative default (65% FULL) so a healthy fleet is byte-identical.

// tradeHullWithCargo builds an idle (never-toured, so no cooldown anchor) trade hull holding
// `units` of cargo against a capacity — the sp-tgll8 pressure/laden/empty signal source.
func tradeHullWithCargo(t *testing.T, symbol string, capacity, units int) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-TR-A1", 1, 1)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	var inv []*shared.CargoItem
	if units > 0 {
		item, err := shared.NewCargoItem("G1", "G1", "", units)
		require.NoError(t, err)
		inv = []*shared.CargoItem{item}
	}
	cargo, err := shared.NewCargo(capacity, units, inv)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, capacity, cargo, 30, "FRAME_FRIGATE", "HAULER", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	ship.SetDedicatedFleet(tradeFleet)
	return ship
}

// runningFullTradeHull models a trade hull mid-tour (a live container claim) that is FULL —
// a saturation-pressure contributor that is not itself a relaunch candidate.
func runningFullTradeHull(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	ship := tradeHullWithCargo(t, symbol, 40, 40)
	require.NoError(t, ship.AssignToContainer("tour-live-"+symbol, clockAt(0)))
	return ship
}

// OVER THRESHOLD → pause EMPTY idle hulls (stop new buying), still relaunch LADEN idle hulls
// (let them sell). 6 running FULL + 1 idle EMPTY + 1 idle LADEN = 8 hulls, 6/8 = 75% FULL >
// 65%: the governor engages. The empty hull would start a NEW buying tour into a saturated
// fleet — paused; the laden hull carries cargo to sell — relaunched (never blocked).
func TestTradePressure_OverThreshold_PausesEmptyRelaunchesLaden(t *testing.T) {
	ships := []*navigation.Ship{
		tradeHullWithCargo(t, "TR-EMPTY", 40, 0),  // empty idle → paused under saturation
		tradeHullWithCargo(t, "TR-LADEN", 40, 20), // laden idle → still relaunched to sell
	}
	for i := 0; i < 6; i++ {
		ships = append(ships, runningFullTradeHull(t, fmt.Sprintf("TR-FULL-%d", i)))
	}
	repo := &fakeTradeShipRepo{ships: ships}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)

	require.Equal(t, 1, launched, "only the laden idle hull relaunches under saturation")
	require.Equal(t, []string{"TR-LADEN"}, launcher.launchedSymbols(),
		"the EMPTY idle hull is paused (no new buying); the LADEN idle hull still relaunches to sell")
	require.True(t, logger.loggedContaining("inventory pressure", "TR-EMPTY"),
		"the paused empty relaunch is logged with its reason")
}

// UNDER THRESHOLD → byte-identical: every idle hull relaunches as today. 1 running FULL + 3
// EMPTY idle = 4 hulls, 1/4 = 25% FULL < the 65% default: the governor stays dormant, so the
// empty idle hulls all relaunch into new tours exactly as before sp-tgll8.
func TestTradePressure_UnderThreshold_ByteIdentical(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		tradeHullWithCargo(t, "TR-A", 40, 0),
		tradeHullWithCargo(t, "TR-B", 40, 0),
		tradeHullWithCargo(t, "TR-C", 40, 0),
		runningFullTradeHull(t, "TR-RUN"),
	}}
	launcher := &fakeTourLauncher{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), tradeCmd())
	require.NoError(t, err)

	require.Equal(t, 3, launched, "below the pressure threshold the governor is dormant — all idle hulls relaunch")
	require.Equal(t, []string{"TR-A", "TR-B", "TR-C"}, launcher.launchedSymbols())
}

// ANTI-DEADLOCK (no wedge): a FULLY saturated fleet still drains. All 4 hulls are idle and
// FULL — 100% FULL, well over threshold — yet a full hull is LADEN, so the governor pauses
// NONE of them: all 4 relaunch to sell their cargo. As they sell, the FULL fraction falls and
// the governor un-throttles. This proves the governor never wedges the fleet (RULINGS #4).
func TestTradePressure_FullySaturated_LadenHullsStillDrain(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		tradeHullWithCargo(t, "TR-1", 40, 40),
		tradeHullWithCargo(t, "TR-2", 40, 40),
		tradeHullWithCargo(t, "TR-3", 40, 40),
		tradeHullWithCargo(t, "TR-4", 40, 40),
	}}
	launcher := &fakeTourLauncher{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), tradeCmd())
	require.NoError(t, err)

	require.Equal(t, 4, launched, "a fully-saturated fleet relaunches every LADEN hull to drain — never deadlocks")
	require.Equal(t, []string{"TR-1", "TR-2", "TR-3", "TR-4"}, launcher.launchedSymbols())
}

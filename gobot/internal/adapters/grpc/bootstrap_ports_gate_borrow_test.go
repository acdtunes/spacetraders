package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// A MATURED fleet has no undedicated hull left: the haulers are on contracts, the seed and the frigate
// on trade. hullToSend's free-hull search demands DedicatedFleet()=="" so it is empty by CONSTRUCTION
// there, which is why the GATE ramp needs a borrow candidate to warm a cold yard at all. These pin the
// observer's borrow pick and its composition with that search.

// maturedFleet is that shape: every haul-capable hull tagged, the frigate away on a tour.
func maturedFleet(t *testing.T) []*navigation.Ship {
	t.Helper()
	return []*navigation.Ship{
		shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", tradeFleetTag, commandRole, navigation.NavStatusInTransit),
		shipyardHull(t, "HAULER-2", "X1-HQ-A1", contractFleetTag, "HAULER", navigation.NavStatusInOrbit),
		shipyardHull(t, "HAULER-3", "X1-HQ-A1", contractFleetTag, "HAULER", navigation.NavStatusInOrbit),
		shipyardHull(t, "TRADER-4", "X1-HQ-A1", tradeFleetTag, "HAULER", navigation.NavStatusInOrbit),
	}
}

func observedBorrow(t *testing.T, ships []*navigation.Ship) string {
	t.Helper()
	var obs bootstrapCmd.Observation
	observeFleetShape(ships, &obs)
	return obs.BorrowableHull
}

// THE LIVE STALL. Nothing is undedicated, so the free-hull search finds nobody and the yard stays cold
// forever — unless the observer names a dedicated-but-idle hull to lend. The lent hull keeps its tag.
func TestObserveFleetShape_LendsADedicatedIdleHullWhenNothingIsFree(t *testing.T) {
	ships := maturedFleet(t)
	borrow := observedBorrow(t, ships)

	require.NotEmpty(t, borrow, "a matured fleet must still offer a hull to lend, or the cold yard never reads")
	require.NotEqual(t, "FRIGATE-1", borrow, "a hull mid-flight is not free to lend")

	send, ok := hullToSend(ships, homeYards(), "", borrow)
	require.True(t, ok, "the free-hull search is empty here — the borrow is the only way the yard warms")
	require.Equal(t, borrow, send)
}

// A GENUINELY FREE HULL STILL OUTRANKS THE LEND. Borrowing is the fallback, never the replacement: an
// undedicated hull carries no other controller's work, so nothing tagged is disturbed while one exists.
func TestObserveFleetShape_BorrowNeverDisplacesTheFreeHullSearch(t *testing.T) {
	ships := append(maturedFleet(t), shipyardHull(t, "SPARE-9", "X1-HQ-A1", "", "HAULER", navigation.NavStatusInOrbit))

	send, ok := hullToSend(ships, homeYards(), "", observedBorrow(t, ships))

	require.True(t, ok)
	require.Equal(t, "SPARE-9", send, "an undedicated hull must be taken before anything tagged is lent")
}

// BUSY MEANS BUSY. A hull mid-flight or mid-claim is another writer's (RULINGS #3/#7), so a fleet with
// nothing genuinely free lends NOBODY and the ramp stays blocked rather than dispatching a hull it
// cannot honor.
func TestObserveFleetShape_LendsNobodyWhenEveryCargoHullIsBusy(t *testing.T) {
	claimed := shipyardHull(t, "HAULER-2", "X1-HQ-A1", contractFleetTag, "HAULER", navigation.NavStatusInOrbit)
	require.NoError(t, claimed.AssignToContainer("contract_fleet_coordinator-1", shared.NewRealClock()))
	ships := []*navigation.Ship{
		shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", tradeFleetTag, commandRole, navigation.NavStatusInTransit),
		claimed,
		shipyardHull(t, "HAULER-3", "X1-HQ-A1", contractFleetTag, "HAULER", navigation.NavStatusInTransit),
	}

	borrow := observedBorrow(t, ships)

	require.Empty(t, borrow, "nothing here is free — lending anyway would fly another writer's hull")
	_, ok := hullToSend(ships, homeYards(), "", borrow)
	require.False(t, ok, "no candidate ⇒ nothing moves this tick")
}

// The lend pool is NARROWER than the free-hull search: a 0-cargo probe is fuel-poor and belongs to
// scouting, so it is never volunteered out of someone's fleet. The free search's own right to take an
// UNDEDICATED probe is untouched — that hull is nobody's.
func TestObserveFleetShape_NeverLendsAZeroCargoProbe(t *testing.T) {
	ships := []*navigation.Ship{
		shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", tradeFleetTag, commandRole, navigation.NavStatusInTransit),
		reclaimHull(t, "PROBE-5", 0, "", navigation.NavStatusInOrbit),
	}

	require.Empty(t, observedBorrow(t, ships), "a probe is not lent")

	send, ok := hullToSend(ships, homeYards(), "", "")
	require.True(t, ok)
	require.Equal(t, "PROBE-5", send, "the free-hull search still takes an undedicated probe on its own account")
}

// THE LEND'S SAFE POINT (RULINGS #3). An idle hull still HOLDING cargo is one its own coordinator
// tracks BY SYMBOL and deterministically comes back for, so flying it to a yard strands that load
// mid-campaign — the one interleave here that costs progress rather than fuel. Only an EMPTY hull goes.
func TestObserveFleetShape_NeverLendsAHullHoldingCargo(t *testing.T) {
	laden := shipyardHull(t, "HAULER-2", "X1-HQ-A1", contractFleetTag, "HAULER", navigation.NavStatusInOrbit)
	item, err := shared.NewCargoItem("IRON_ORE", "Iron Ore", "", 10)
	require.NoError(t, err)
	held, err := shared.NewCargo(40, 10, []*shared.CargoItem{item})
	require.NoError(t, err)
	laden.SetCargo(held)
	ships := []*navigation.Ship{
		shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", tradeFleetTag, commandRole, navigation.NavStatusInTransit),
		laden,
	}

	require.Empty(t, observedBorrow(t, ships), "a laden hull is mid-campaign — its load is not moved to a shipyard")

	empty := shipyardHull(t, "HAULER-3", "X1-HQ-A1", contractFleetTag, "HAULER", navigation.NavStatusInOrbit)
	require.Equal(t, "HAULER-3", observedBorrow(t, append(ships, empty)), "the empty hull beside it is lendable")
}

// THE CLAIM RACE (RULINGS #3). The contract coordinator can ClaimShip a hull between the observation
// that offered it and the navigate that would fly it. borrowedHull re-reads the LIVE roster, so the
// claim that landed first wins and the lend simply stands down — the yard trip retries a later tick.
func TestBorrowedHull_StandsDownWhenTheClaimLandedFirst(t *testing.T) {
	ships := maturedFleet(t)
	borrow := observedBorrow(t, ships)

	for _, sh := range ships {
		if sh.ShipSymbol() == borrow {
			require.NoError(t, sh.AssignToContainer("contract_fleet_coordinator-1", shared.NewRealClock()))
		}
	}

	_, ok := hullToSend(ships, homeYards(), "", borrow)
	require.False(t, ok, "a hull claimed since the observation is that writer's — it is never redirected")
}

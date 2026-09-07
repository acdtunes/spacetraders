package commands

// run_probe_sensing_bulk_buy_test.go covers the shape a bulk probe buy leaves on
// the fleet, and the one adoption could not absorb: many idle hulls at ONE waypoint
// that is already watched. Every yard carries the MARKET placement of the probe
// scanning it, so the occupancy guard refused the whole wave on every tick, for
// ever — outside the probe cap, while the cap under-read and re-bought.

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// THE HEADLINE CASE. Ten orphans at one yard that already carries a MARKET
// placement: every one of them is adopted, and the placement is untouched.
// MUTANT THIS KILLS: restoring the occupancy refusal. With it, adopted is zero
// and ten paid-for hulls stay invisible to the probe cap.
//
// MUTANT IT ALSO KILLS: writing the reserves as SPARE PLACEMENT rows. One placement
// per (waypoint, kind) means the tenth write re-points the row nine times over and
// nine hulls end up tagged with no row — the under-count RULINGS #4 forbids. The
// market assertions catch a write that lands on the placement table at all.
func TestAdoption_TenOrphansAtOneWatchedYard_AreAllAdoptedAndTheMarketSlotIsUntouched(t *testing.T) {
	const yard = "X1-KP23-A2"
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{yard, parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: yard, System: "X1-KP23", Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateParked, AssignedShip: "PROBE-WATCHER",
	}
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "PROBE-WATCHER", yard, parkedsensing.SensingParkedFleetTag),
	}
	bought := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		hull := fmt.Sprintf("PROBE-BULK-%02d", i)
		bought = append(bought, hull)
		world.fleet.ships = append(world.fleet.ships, scoutProbe(t, hull, yard))
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	for _, hull := range bought {
		require.True(t, reservedAt(world, hull, yard),
			"%s stands at a watched yard and is still ours — it belongs on the books", hull)
	}
	require.Equal(t, 10, logger.payload("parked_sensing_cycle")["adopted_stranded"])

	watched := world.ledger.slots[psSlotKey{yard, parkedsensing.SlotKindMarket}]
	require.Equal(t, "PROBE-WATCHER", watched.AssignedShip,
		"the MARKET placement still names its own hull — a spare must never displace one")
	require.Equal(t, parkedsensing.SlotKindMarket, watched.Kind,
		"and is still a MARKET slot, not rewritten to SPARE and dropped out of the scan rotation")
	require.Equal(t, parkedsensing.SlotStateParked, watched.State)

	count, err := world.ledger.CountOwnedProbes(world.ctx, testPlayerID)
	require.NoError(t, err)
	require.EqualValues(t, 11, count,
		"the probe cap sees the watcher and all ten adopted hulls; an under-count here re-buys probes we own")
}

// A SECOND RESERVE AT ONE WAYPOINT DOES NOT EVICT THE FIRST.
//
// MUTANT THIS KILLS: keying the reserve on the waypoint instead of the hull —
// exactly what the placement table does, and exactly why the reserve could not
// live in it. A waypoint-keyed pool returns one row here and the first hull is
// gone from the count.
func TestAdoption_ASecondReserveAtOneWaypointDoesNotEvictTheFirst(t *testing.T) {
	const yard = "X1-KP23-A2"
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{yard, parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: yard, System: "X1-KP23", Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateParked, AssignedShip: "PROBE-WATCHER",
	}
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "PROBE-WATCHER", yard, parkedsensing.SensingParkedFleetTag),
		scoutProbe(t, "PROBE-FIRST", yard),
	}
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.True(t, reservedAt(world, "PROBE-FIRST", yard), "precondition: the first is in the pool")

	world.fleet.ships = append(world.fleet.ships, scoutProbe(t, "PROBE-SECOND", yard))
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.True(t, reservedAt(world, "PROBE-FIRST", yard),
		"adopting the second must not overwrite the first — a hull dropped here is re-bought")
	require.True(t, reservedAt(world, "PROBE-SECOND", yard))
	require.Len(t, world.ledger.spareHulls, 2, "two reserves, one waypoint, two rows")
}

// THE BURST IS BOUNDED, and the bound is reported on the cycle line.
//
// MUTANT THIS KILLS: ignoring the cap. Without it the first tick adopts the whole
// backlog and `adopt used/limit` never reports a bound pass.
func TestAdoption_BurstIsBoundedAndReportedOnTheCycleLine(t *testing.T) {
	const yard = "X1-KP23-A2"
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	pinAdoptBudget(world)
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{yard, parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: yard, System: "X1-KP23", Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateParked, AssignedShip: "PROBE-WATCHER",
	}
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "PROBE-WATCHER", yard, parkedsensing.SensingParkedFleetTag),
	}
	for i := 0; i < DefaultMaxAdoptions+4; i++ {
		world.fleet.ships = append(world.fleet.ships, scoutProbe(t, fmt.Sprintf("PROBE-BULK-%02d", i), yard))
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Len(t, world.ledger.spareHulls, DefaultMaxAdoptions,
		"the first tick adopts exactly its budget; the rest are still orphaned and still first in line")
	require.Equal(t, DefaultMaxAdoptions, logger.payload("parked_sensing_cycle")["adopted_stranded"])

	rows, ok := logger.payload("parked_sensing_cycle")["pass_budgets"].([]map[string]interface{})
	require.True(t, ok)
	require.Contains(t, rows,
		map[string]interface{}{"pass": passBudgetAdopt, "used": DefaultMaxAdoptions, "limit": DefaultMaxAdoptions},
		"the cycle line names adoption as a bound pass, beside every other budget")

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Len(t, world.ledger.spareHulls, DefaultMaxAdoptions+4,
		"and the next tick picks up the remainder — nothing is lost, only paced")
}

// THE CUTOVER'S RESERVE ARM IS BOUNDED AND DEDUPED, like its standing sibling. It
// fires once per era, and the placement writes beside it are one per free waypoint;
// this arm can take every co-located hull at once, which on a bulk-bought fleet is
// hundreds of row locks on the one irreversible tick.
func TestCutover_ReserveAdoptionIsBoundedAndSkipsHullsAlreadyOnTheBooks(t *testing.T) {
	const yard = testHomeSystem + "-A1"
	world := newCutoverWorld(t)
	world.ledger.slots[psSlotKey{yard, parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: yard, System: testHomeSystem, Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateParked, AssignedShip: "PROBE-WATCHER",
	}
	// One hull already in the pool: it must cost no write at all.
	world.ledger.spareHulls = map[string]parkedsensing.SpareHull{
		"PROBE-KNOWN": {Ship: "PROBE-KNOWN", Waypoint: yard, System: testHomeSystem},
	}
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "PROBE-WATCHER", yard, parkedsensing.SensingParkedFleetTag),
		scoutProbe(t, "PROBE-KNOWN", yard),
	}
	for i := 0; i < DefaultMaxAdoptions+5; i++ {
		world.fleet.ships = append(world.fleet.ships, scoutProbe(t, fmt.Sprintf("PROBE-BULK-%02d", i), yard))
	}

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Len(t, world.ledger.spareHulls, DefaultMaxAdoptions+1,
		"the cap holds the burst down, and the hull already recorded cost no write")
	require.Contains(t, world.ledger.spareHulls, "PROBE-KNOWN")
	require.NotContains(t, world.tagger.tagged, "PROBE-KNOWN",
		"a hull already on the books is skipped outright, not re-recorded and re-tagged")
}

// THE REFUSALS ARE UNCHANGED. Widening the occupancy skip may only ADD adoptions,
// and each of these hulls is somebody else's or unplaceable. Run at a waypoint
// that IS occupied, which is precisely where the old guard used to do this job:
// with it gone, nothing but these filters stands between the pass and a hull it
// must not take.
func TestAdoption_EveryOtherRefusalStillRefuses(t *testing.T) {
	const yard = "X1-KP23-A2"
	for _, tc := range []struct {
		name string
		ship func(t *testing.T) *navigation.Ship
		post bool
	}{
		{"not a probe frame", func(t *testing.T) *navigation.Ship {
			return nonProbeWithFleet(t, "HAULER-1", yard, "")
		}, false},
		{"dedicated to a fleet that is not ours", func(t *testing.T) *navigation.Ship {
			return probeWithFleet(t, "PROBE-FOREIGN", yard, "trade-runner")
		}, false},
		{"still manning a surviving scout post", func(t *testing.T) *navigation.Ship {
			return scoutProbe(t, "PROBE-MANNED", yard)
		}, true},
		{"location cannot be read", func(t *testing.T) *navigation.Ship {
			return flyingScoutProbe(t, "PROBE-FLYING")
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
			world.posts.posts = nil
			world.ledger.slots[psSlotKey{yard, parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
				Waypoint: yard, System: "X1-KP23", Kind: parkedsensing.SlotKindMarket,
				State: parkedsensing.SlotStateParked, AssignedShip: "PROBE-WATCHER",
			}
			ship := tc.ship(t)
			world.fleet.ships = []*navigation.Ship{
				probeWithFleet(t, "PROBE-WATCHER", yard, parkedsensing.SensingParkedFleetTag),
				ship,
			}
			if tc.post {
				world.posts.posts = []*domainScouting.ScoutPost{{
					PlayerID: testPlayerID, SystemSymbol: "X1-KP23",
					Kind: domainScouting.PostKindStanding, AssignedHull: ship.ShipSymbol(),
				}}
			}

			require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

			require.Empty(t, world.ledger.spareHulls,
				"a hull this pass must not take is still not taken — the change may only ADD adoptions")
			require.NotContains(t, world.tagger.tagged, ship.ShipSymbol())
		})
	}
}

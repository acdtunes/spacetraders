package commands

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE FIXTURES IN THIS FILE ARE THE LIVE FLEET, hull symbol for hull symbol.
//
// The bug class this pass closes shipped once already because adoption's tests tagged their probes
// `scout` while not one live hull carried that tag: the tests proved the code worked on a fleet that
// did not exist. So these fixtures are copied from the live rows rather than invented —
//
//	sensing_slots  X1-KP23-A2 PARKED/MARKET/TORWIND-E, C38 PARKED/MARKET/TORWIND-11, and eight
//	               hull-less WANTED/MARKET placements elsewhere in the same system
//	ships          three idle untagged orphans SHARING X1-KP23-A2 behind that incumbent row, a
//	               fourth behind C38's, three probes `active` on scout_tour containers, and four
//	               hulls the ledger already names
//
// A fixture where every probe stands at its own waypoint would pass against the old code and prove
// nothing: the whole reason these hulls cannot be adopted where they stand is that sensing_slots is
// keyed (player, waypoint) and they are stacked on ONE waypoint that is already spoken for
// (sp-dpfp8). The stacking is the test.

// idleOrphan builds a landed, idle, untagged probe — the live orphan shape.
func idleOrphan(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	return probeWithFleet(t, symbol, waypoint, "")
}

// touringProbe builds a probe an active scout_tour container is driving. Three live hulls are in
// exactly this state and they are NOT ours to take (RULINGS #3, single writer).
func touringProbe(t *testing.T, symbol, waypoint, container string) *navigation.Ship {
	t.Helper()
	ship := probeWithFleet(t, symbol, waypoint, "")
	require.NoError(t, ship.AssignToContainer(container, shared.NewRealClock()))
	return ship
}

// wantedAt declares a hull-less MARKET placement: the 74 open rows this pass exists to fill.
func wantedAt(waypoint string) parkedsensing.QueuedSlot {
	return parkedsensing.QueuedSlot{
		Waypoint: waypoint, System: shared.ExtractSystemSymbol(waypoint),
		Kind: parkedsensing.SlotKindMarket, State: parkedsensing.SlotStateWanted,
	}
}

// parkedAt declares a placement already filled by an incumbent — the row that BLOCKS every orphan
// standing on that waypoint from ever being adopted in place.
func parkedAt(waypoint, hull string) parkedsensing.QueuedSlot {
	return parkedsensing.QueuedSlot{
		Waypoint: waypoint, System: shared.ExtractSystemSymbol(waypoint),
		Kind: parkedsensing.SlotKindMarket, State: parkedsensing.SlotStateParked, AssignedShip: hull,
	}
}

// liveFleetWorld reproduces player 5's X1-KP23 as it stands: twelve probes, four of them recorded,
// three mid-tour, four idle orphans stacked behind two occupied rows, and eight open placements.
func liveFleetWorld(t *testing.T) *cutoverWorld {
	t.Helper()
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil

	for _, slot := range []parkedsensing.QueuedSlot{
		// The four rows that already name a hull.
		{Waypoint: "X1-KP23-A1", System: "X1-KP23", Kind: parkedsensing.SlotKindMarket,
			State: parkedsensing.SlotStateInTransit, AssignedShip: "TORWIND-2"},
		parkedAt("X1-KP23-A2", "TORWIND-E"),
		parkedAt("X1-KP23-A3", "TORWIND-D"),
		parkedAt("X1-KP23-C38", "TORWIND-11"),
		// A claim the drain has money riding on.
		{Waypoint: "X1-KP23-A4", System: "X1-KP23", Kind: parkedsensing.SlotKindMarket,
			State: parkedsensing.SlotStateQueued},
		// The open placements.
		wantedAt("X1-KP23-D40"), wantedAt("X1-KP23-D41"), wantedAt("X1-KP23-E43"),
		wantedAt("X1-KP23-F46"), wantedAt("X1-KP23-G48"), wantedAt("X1-KP23-H50"),
		wantedAt("X1-KP23-J56"), wantedAt("X1-KP23-K86"),
		// The expansion engine's charting-seed request. NOT a placement.
		{Waypoint: "X1-KP23-J55", System: "X1-KP23", Kind: parkedsensing.SlotKindSpare,
			State: parkedsensing.SlotStateWanted},
	} {
		world.ledger.slots[psSlotKey{slot.Waypoint, slot.Kind}] = slot
	}

	// Ships in the order the repository returns them, so the pairing below is reproducible.
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-2", "X1-KP23-A1", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
		idleOrphan(t, "TORWIND-15", "X1-KP23-A2"),
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-F", "X1-KP23-A2"),
		touringProbe(t, "TORWIND-13", "X1-KP23-A3", "scout_tour-TORWIND-13-b4bba4bb"),
		probeWithFleet(t, "TORWIND-D", "X1-KP23-A3", parkedsensing.SensingParkedFleetTag),
		probeWithFleet(t, "TORWIND-11", "X1-KP23-C38", parkedsensing.SensingParkedFleetTag),
		touringProbe(t, "TORWIND-12", "X1-KP23-C38", "scout_tour-TORWIND-12-6f2b031d"),
		idleOrphan(t, "TORWIND-4", "X1-KP23-C38"),
		idleOrphan(t, "TORWIND-3", "X1-KP23-E43"),
		touringProbe(t, "TORWIND-10", "X1-KP23-I54", "scout_tour-TORWIND-10-8d8e137b"),
	}

	// The ships TABLE, which is a different port from the fleet enumeration above: the placement
	// machine locates hulls through this one and refuses to command a hull it cannot find. Every
	// probe is landed and docked, as the live rows have them.
	for _, ship := range world.fleet.ships {
		world.shipPos.at[ship.ShipSymbol()] = parkedsensing.ShipPos{
			Waypoint: ship.CurrentLocation().Symbol, NavStatus: navigation.NavStatusDocked,
		}
	}
	return world
}

// dispatchedCount reads the tick's orphan-dispatch tally off the heartbeat.
func dispatchedCount(t *testing.T, logger *capturingLogger) int {
	t.Helper()
	return logger.payload("parked_sensing_cycle")["dispatched_orphans"].(int)
}

// THE HEADLINE. Four idle hulls we already paid for are stacked behind two occupied rows and cannot
// be adopted where they stand. Each is sent to an open placement ELSEWHERE in its own system, at
// zero credits, and the placement machine flies it — which is the whole point: the fleet paid
// 245,316 credits for these hulls and was using none of them while the buy floor (correctly)
// refused to buy more.
func TestOrphanDispatch_LiveFleetShape_PutsEveryStackedIdleOrphanToWork(t *testing.T) {
	world := liveFleetWorld(t)
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 4, dispatchedCount(t, logger),
		"the four idle orphans stacked behind X1-KP23-A2 and -C38 are the ones only this pass can reach")

	// Targets are consumed in waypoint order, hulls in fleet order.
	for hull, waypoint := range map[string]string{
		"TORWIND-14": "X1-KP23-D40",
		"TORWIND-15": "X1-KP23-D41",
		"TORWIND-F":  "X1-KP23-F46",
		"TORWIND-4":  "X1-KP23-G48",
	} {
		row := world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}]
		require.Equal(t, hull, row.AssignedShip, "%s answers the placement at %s", hull, waypoint)
		require.Equal(t, parkedsensing.SlotStateInTransit, row.State,
			"the hull is already ours: straight to IN_TRANSIT, with no BOUGHT state and nothing bought")
		require.Equal(t, parkedsensing.SlotKindMarket, row.Kind,
			"kind is never rewritten — a MARKET placement that became SPARE leaves the scan rotation")
	}

	// End to end: the placement machine's dispatchClaim branch actually flew them. Without it the
	// rows would read in-flight forever while the hulls stood still.
	for _, hull := range []string{"TORWIND-14", "TORWIND-15", "TORWIND-F", "TORWIND-4"} {
		require.Contains(t, world.mover.moves, hull+"→"+world.ledger.slots[psSlotKey{dispatchTargetOf(world, hull), parkedsensing.SlotKindMarket}].Waypoint,
			"%s was commanded to fly to its placement", hull)
	}
}

// dispatchTargetOf finds the waypoint whose row now names hull.
func dispatchTargetOf(world *cutoverWorld, hull string) string {
	for key, slot := range world.ledger.slots {
		if slot.AssignedShip == hull {
			return key.waypoint
		}
	}
	return ""
}

// THE SCOUT STACK IS LIVE COLD-START INFRASTRUCTURE. Three probes sit `active` on scout_tour
// containers, and a container is a single writer that is not us. Taking one would fly a hull
// out from under a tour that still believes it owns it.
//
// The `manned` map does NOT cover this — it reads scout POSTS, and a tour is not a post — which is
// exactly why adoption carries its own !IsIdle guard and why this pass must carry it too.
func TestOrphanDispatch_NeverTakesAHullAnActiveContainerIsDriving(t *testing.T) {
	world := liveFleetWorld(t)
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	for _, touring := range []string{"TORWIND-13", "TORWIND-12", "TORWIND-10"} {
		require.Empty(t, dispatchTargetOf(world, touring),
			"%s is mid-tour: no placement may name it", touring)
		require.NotContains(t, world.mover.moves, touring, "and it is never commanded to move")
	}
}

// The OTHER half of "never take a busy hull": a probe still manning a surviving scout post. Read
// permissively an unreadable post list is an empty one, which reads as "nobody is manned" — so this
// guard and the fail-closed read below are the same guard seen from two sides.
func TestOrphanDispatch_NeverTakesAHullManningAScoutPost(t *testing.T) {
	world := liveFleetWorld(t)
	world.posts.posts = []*domainScouting.ScoutPost{{
		PlayerID: testPlayerID, SystemSymbol: "X1-KP23", AssignedHull: "TORWIND-14",
	}}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Empty(t, dispatchTargetOf(world, "TORWIND-14"),
		"a hull still manning a post belongs to that post")
}

// FAIL CLOSED. An unreadable post list must never read as "no hull is manned" — that is the exact
// shape that authorises taking a hull out from under the scout coordinator. The pass does nothing
// at all and says why.
func TestOrphanDispatch_UnreadablePostsDispatchNothing(t *testing.T) {
	world := liveFleetWorld(t)
	world.posts.listErr = errors.New("posts table unavailable")
	logger := &capturingLogger{}

	err := world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd)

	require.Error(t, err, "the failure is surfaced, not swallowed")
	require.Equal(t, 0, dispatchedCount(t, logger),
		"an unreadable post list is not an empty one")
	for _, hull := range []string{"TORWIND-14", "TORWIND-15", "TORWIND-F", "TORWIND-4"} {
		require.Empty(t, dispatchTargetOf(world, hull))
	}
}

// A hull the ledger ALREADY names is not an orphan. Re-pointing it would hand its placement away
// and, for the instant between the writes, leave one hull answering two rows.
func TestOrphanDispatch_NeverRepointsAHullTheLedgerAlreadyRecords(t *testing.T) {
	world := liveFleetWorld(t)
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, "TORWIND-E", world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}].AssignedShip,
		"the incumbent keeps its row")
	require.Equal(t, "TORWIND-11", world.ledger.slots[psSlotKey{"X1-KP23-C38", parkedsensing.SlotKindMarket}].AssignedShip)
	for hull, waypoint := range map[string]string{"TORWIND-E": "X1-KP23-A2", "TORWIND-11": "X1-KP23-C38",
		"TORWIND-D": "X1-KP23-A3", "TORWIND-2": "X1-KP23-A1"} {
		require.Equal(t, waypoint, dispatchTargetOf(world, hull),
			"%s is recorded at %s and must not be moved to a second row", hull, waypoint)
	}
}

// RULINGS #7. Widening eligibility must not become a licence to poach: a probe dedicated to a live
// foreign fleet already has a writer, and taking it would give one hull two.
func TestOrphanDispatch_NeverPoachesAHullDedicatedToALiveForeignFleet(t *testing.T) {
	for _, fleet := range []string{"contract", "trade", "manufacturing", "long-haul", "some-future-fleet"} {
		t.Run(fleet, func(t *testing.T) {
			world := liveFleetWorld(t)
			world.fleet.ships = append(world.fleet.ships,
				probeWithFleet(t, "PROBE-FOREIGN", "X1-KP23-A2", fleet))
			logger := &capturingLogger{}

			require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

			require.Empty(t, dispatchTargetOf(world, "PROBE-FOREIGN"),
				"a hull dedicated to %q belongs to that fleet", fleet)
		})
	}
}

// EVERY HULL GETS ITS OWN PLACEMENT — the property the allocator's consume-as-you-go is actually
// responsible for, isolated here because a mutation probe proved the obvious test does not pin it.
//
// Read the pool without consuming it and all three hulls are offered the SAME first placement: one
// wins, the other two lose the race and are skipped, and the other open rows are never offered to
// anybody. The outcome is still SAFE — the ledger's guarded transition sees to that — but only one
// hull moves per system per tick, which on the live fleet means one dispatch instead of four while
// 74 placements stand empty. Safety and throughput are different properties and this is the one that
// needs its own test.
func TestOrphanDispatch_EachHullGetsItsOwnDistinctPlacement(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
	for _, waypoint := range []string{"X1-KP23-D40", "X1-KP23-D41", "X1-KP23-F46"} {
		world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}] = wantedAt(waypoint)
	}
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
		idleOrphan(t, "TORWIND-15", "X1-KP23-A2"),
		idleOrphan(t, "TORWIND-F", "X1-KP23-A2"),
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 3, dispatchedCount(t, logger),
		"three hulls and three open placements: all three move, not just the first")
	require.Equal(t, "TORWIND-14", world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip)
	require.Equal(t, "TORWIND-15", world.ledger.slots[psSlotKey{"X1-KP23-D41", parkedsensing.SlotKindMarket}].AssignedShip)
	require.Equal(t, "TORWIND-F", world.ledger.slots[psSlotKey{"X1-KP23-F46", parkedsensing.SlotKindMarket}].AssignedShip)
}

// ONE PLACEMENT TAKES EXACTLY ONE HULL. Three idle orphans share X1-KP23-A2 and exactly one
// placement is open: two of them wait.
//
// This holds because the ledger's WANTED→IN_TRANSIT transition is guarded on the from-state, so a
// second hull's write returns ErrSlotClaimed and is skipped with nothing spent. That is worth
// pinning on its own — it is the backstop the whole pass leans on — but note that it is NOT what
// the allocator's consume-as-you-go provides; see the test above for that.
func TestOrphanDispatch_OnePlacementTakesExactlyOneHull(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
	world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}] = wantedAt("X1-KP23-D40")
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
		idleOrphan(t, "TORWIND-15", "X1-KP23-A2"),
		idleOrphan(t, "TORWIND-F", "X1-KP23-A2"),
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 1, dispatchedCount(t, logger), "one placement, one hull")
	require.Equal(t, "TORWIND-14", world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip)
	require.Empty(t, dispatchTargetOf(world, "TORWIND-15"), "the second hull waits for the next open placement")
	require.Empty(t, dispatchTargetOf(world, "TORWIND-F"))
}

// ONE HULL ANSWERS EXACTLY ONE PLACEMENT — the other half of the allocation discipline. With two
// placements open and one eligible hull, the second placement stays WANTED rather than being
// written with a hull that is already flying somewhere else.
func TestOrphanDispatch_OneHullAnswersExactlyOnePlacement(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
	world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}] = wantedAt("X1-KP23-D40")
	world.ledger.slots[psSlotKey{"X1-KP23-D41", parkedsensing.SlotKindMarket}] = wantedAt("X1-KP23-D41")
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 1, dispatchedCount(t, logger))
	require.Equal(t, "TORWIND-14", world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip)
	require.Empty(t, world.ledger.slots[psSlotKey{"X1-KP23-D41", parkedsensing.SlotKindMarket}].AssignedShip,
		"the second placement is not also written with a hull already spoken for")
}

// A SYSTEM THE GATE GRAPH CANNOT REACH IS NOT A TARGET, whatever is open in it.
//
// This test used to read "same system only", and that WAS the rule: RouteAcross refused outright
// (the sp-uwxwo stopgap, because routing a whole crossing blocked the tick), so a cross-gate errand
// was one nothing could perform. The walk now advances a step per tick, and the rule is the walk's
// reach — see run_probe_orphan_dispatch_xsys_test.go, where a hull is carried across one and two
// gates to a berth.
//
// What survives unchanged is the FAIL-CLOSED edge, which is what this fixture still pins: this world
// has no stored adjacency at all, so nothing can confirm X1-BT49 is reachable, and an unconfirmed
// reach sends nobody. Reading "we cannot see any gates" as "everything is reachable" is what leaves a
// hull IN_TRANSIT forever, holding a placement and counting against the probe cap while never
// arriving.
func TestOrphanDispatch_NeverDispatchesToASystemTheGateGraphCannotReach(t *testing.T) {
	world := steadyWorld(t, map[string]string{
		"X1-KP23": parkedsensing.VerdictInScope,
		"X1-BT49": parkedsensing.VerdictInScope,
	})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
	world.ledger.slots[psSlotKey{"X1-BT49-AA9E", parkedsensing.SlotKindMarket}] = wantedAt("X1-BT49-AA9E") // the biggest open system, and no edge to it
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 0, dispatchedCount(t, logger),
		"with no stored edge to confirm the crossing, the reach is empty and the hull stays put")
	require.Empty(t, world.ledger.slots[psSlotKey{"X1-BT49-AA9E", parkedsensing.SlotKindMarket}].AssignedShip)
}

// An orphan standing ON a hull-less placement is ADOPTION's, filled in place for free. Flying it
// somewhere else would burn a hop AND leave the placement under its feet still unfilled. TORWIND-3
// stands on X1-KP23-E43 in exactly this shape.
func TestOrphanDispatch_LeavesAnOrphanStandingOnItsOwnOpenPlacement(t *testing.T) {
	world := liveFleetWorld(t)
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, "X1-KP23-E43", dispatchTargetOf(world, "TORWIND-3"),
		"TORWIND-3 fills the placement it is already standing on rather than flying to another")
	require.Equal(t, parkedsensing.SlotStateParked, world.ledger.slots[psSlotKey{"X1-KP23-E43", parkedsensing.SlotKindMarket}].State,
		"adoption parks it in place — no flight, no IN_TRANSIT")
}

// And the corollary: a waypoint an idle orphan is standing on is never chosen as somebody ELSE's
// target, because adoption will fill it in place for nothing.
func TestOrphanDispatch_NeverTargetsAPlacementAnIdleOrphanIsAlreadyStandingOn(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
	world.ledger.slots[psSlotKey{"X1-KP23-E43", parkedsensing.SlotKindMarket}] = wantedAt("X1-KP23-E43") // TORWIND-3 stands here
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
		idleOrphan(t, "TORWIND-3", "X1-KP23-E43"),
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, "TORWIND-3", world.ledger.slots[psSlotKey{"X1-KP23-E43", parkedsensing.SlotKindMarket}].AssignedShip,
		"the hull already standing there gets it, not the one that would have to fly")
	require.Equal(t, 0, dispatchedCount(t, logger))
}

// THE OTHER HALF OF THE SAME EXCLUSION, and it needs adoption's budget spent to show at all — which
// is why it gets its own fixture rather than riding on the test above.
//
// In the ordinary case ordering hides it: adoption runs first and fills the placement under the
// standing hull's feet, so by the time this pass re-reads the ledger that row is PARKED and could
// not be a target anyway. Only a backlog past DefaultMaxAdoptions leaves such a row still WANTED —
// and then, without the exclusion, a DIFFERENT hull is flown to a waypoint that already has an idle
// probe standing on it. That is doubly wrong: the flight is wasted, and it takes the placement the
// standing hull was going to be adopted into for free, leaving that hull stuck for good.
func TestOrphanDispatch_NeverFliesAHullToAPlacementAnotherOrphanIsWaitingOn(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil

	// One dispatch-eligible orphan, stuck behind an incumbent's row.
	world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
	}
	// One more orphan-on-its-own-placement than adoption can absorb this tick, so the LAST of them
	// is still standing on a WANTED row when this pass runs. That row is the only one open.
	for i := 0; i <= DefaultMaxAdoptions; i++ {
		waypoint := fmt.Sprintf("X1-KP23-W%02d", i)
		world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}] = wantedAt(waypoint)
		world.fleet.ships = append(world.fleet.ships,
			idleOrphan(t, fmt.Sprintf("TORWIND-B%02d", i), waypoint))
	}
	stranded := fmt.Sprintf("X1-KP23-W%02d", DefaultMaxAdoptions)
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, parkedsensing.SlotStateWanted, world.ledger.slots[psSlotKey{stranded, parkedsensing.SlotKindMarket}].State,
		"precondition: adoption's budget ran out before this row, so it is still open")
	require.Equal(t, 0, dispatchedCount(t, logger),
		"the only open placement has an idle probe standing on it — nobody is flown to it")
	require.Empty(t, world.ledger.slots[psSlotKey{stranded, parkedsensing.SlotKindMarket}].AssignedShip)
	require.NotContains(t, world.mover.moves, "TORWIND-14→"+stranded)
}

// A SPARE/WANTED row is the expansion engine's charting-seed STAGING request, tracked in its own
// slot book. It is not a sensing placement and filling it here would desync that accounting.
func TestOrphanDispatch_NeverFillsACharteringSeedRequest(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
	world.ledger.slots[psSlotKey{"X1-KP23-J55", parkedsensing.SlotKindSpare}] = parkedsensing.QueuedSlot{
		Waypoint: "X1-KP23-J55", System: "X1-KP23",
		Kind: parkedsensing.SlotKindSpare, State: parkedsensing.SlotStateWanted,
	}
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 0, dispatchedCount(t, logger))
	require.Empty(t, world.ledger.slots[psSlotKey{"X1-KP23-J55", parkedsensing.SlotKindSpare}].AssignedShip,
		"a seed staging request is the expansion engine's, not a placement to fill")
}

// A QUEUED placement has a purchase claim on it and its TransitionSlot(QUEUED→BOUGHT) is guarded on
// that state. Filling it here would break a claim money is already riding on.
//
// THE OPEN PLACEMENT AT D40 IS WHAT MAKES THIS TEST BITE, and a mutation probe is why it is here.
// The ledger's guarded from-state already refuses the QUEUED write, so a fixture with ONLY the
// QUEUED row passes whether or not this pass filters on state — the write simply comes back
// ErrSlotClaimed. What the filter really prevents is the claim being drawn from the pool AT ALL: A4
// sorts before D40, so an unfiltered pool hands the hull the QUEUED row, the write fails, and the
// hull is skipped for the tick with the perfectly good placement at D40 left standing empty.
func TestOrphanDispatch_NeverFillsAQueuedPlacement(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
	world.ledger.slots[psSlotKey{"X1-KP23-A4", parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: "X1-KP23-A4", System: "X1-KP23",
		Kind: parkedsensing.SlotKindMarket, State: parkedsensing.SlotStateQueued,
	}
	world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}] = wantedAt("X1-KP23-D40") // sorts AFTER the claim
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, parkedsensing.SlotStateQueued, world.ledger.slots[psSlotKey{"X1-KP23-A4", parkedsensing.SlotKindMarket}].State,
		"the purchase claim is untouched")
	require.Empty(t, world.ledger.slots[psSlotKey{"X1-KP23-A4", parkedsensing.SlotKindMarket}].AssignedShip)
	require.Equal(t, 1, dispatchedCount(t, logger),
		"and the hull is not wasted on it: it goes to the placement that is genuinely open")
	require.Equal(t, "TORWIND-14", world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip)
}

// THE WRITE ORDER IS A MONEY GUARD, and this is what pins it.
//
// The orphan holds NO row, so there is no release leg — but there are still two writes, and their
// order decides which way a crash miscounts. The ROW is what CountOwnedProbes reads:
//
//   - row then tag (as built): a failure leaves the hull recorded and untagged. It counts against
//     the probe cap, and the placement machine re-tags it on first use. Recoverable.
//   - tag then row: a failure leaves the hull TAGGED sensing_parked with no row anywhere. It is
//     invisible to the cap forever, and the cap then authorises buying a replacement for a probe we
//     already own — the money-unsafe direction RULINGS #4 forbids, and the exact failure that cost
//     this fleet 245,316 credits.
//
// So: when the row write fails, the hull must NOT have been tagged.
func TestOrphanDispatch_AFailedRowWriteLeavesTheHullUntagged(t *testing.T) {
	world := liveFleetWorld(t)
	world.ledger.transitionErr = map[string]error{"X1-KP23-D40": errors.New("ledger unavailable")}
	logger := &capturingLogger{}

	err := world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd)

	require.Error(t, err, "a failed dispatch is surfaced, not swallowed")
	require.Empty(t, world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip, "the row was not written")
	require.NotContains(t, world.tagger.tagged, "TORWIND-14",
		"tagged-but-unrecorded is the shape that buys a probe we already own — the tag must come SECOND")
}

// Losing the race for a placement is routine contention, not a failure: another writer got there
// first, nothing was spent, and the hull is still an orphan first in line next tick.
func TestOrphanDispatch_LosingTheRaceForAPlacementIsNotAFailure(t *testing.T) {
	world := liveFleetWorld(t)
	world.ledger.transitionErr = map[string]error{"X1-KP23-D40": parkedsensing.ErrSlotClaimed}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd),
		"a lost race is not an error")
	require.Empty(t, world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip)
}

// IDEMPOTENT. A settled fleet costs reads and no writes, however many ticks run. Once dispatched a
// hull is named by its row, so the next tick reads it as recorded and leaves it alone — without
// that, every tick would re-point the same hulls at fresh placements forever.
func TestOrphanDispatch_IsIdempotentAcrossTicks(t *testing.T) {
	world := liveFleetWorld(t)

	first := &capturingLogger{}
	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, first), world.cmd))
	require.Equal(t, 4, dispatchedCount(t, first))

	second := &capturingLogger{}
	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, second), world.cmd))

	require.Equal(t, 0, dispatchedCount(t, second), "a settled fleet dispatches nothing")
	require.Equal(t, "TORWIND-14", world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip,
		"and the first tick's placements are untouched")
	require.Equal(t, "TORWIND-4", world.ledger.slots[psSlotKey{"X1-KP23-G48", parkedsensing.SlotKindMarket}].AssignedShip)
}

// BOUNDED PER TICK, in the class of DefaultMaxAdoptions and DefaultMaxPlacementActions: a plain
// constant that paces a burst of ledger writes and navigation commands, not an economic choice. The
// backlog is not lost — the hulls left over are still orphans and still first in line next tick.
func TestOrphanDispatch_BoundedPerTick(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
	}
	// Twice the bound in hulls, and a placement for every one of them.
	for i := 0; i < DefaultMaxOrphanDispatches*2; i++ {
		waypoint := fmt.Sprintf("X1-KP23-W%02d", i)
		world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}] = wantedAt(waypoint)
		world.fleet.ships = append(world.fleet.ships,
			idleOrphan(t, fmt.Sprintf("TORWIND-B%02d", i), "X1-KP23-A2"))
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, DefaultMaxOrphanDispatches, dispatchedCount(t, logger),
		"one tick dispatches at most the bound, however deep the backlog")
}

// The defensive half of the target filter. Every other test's refused shapes (PARKED incumbents,
// QUEUED claims) are already refused by the STATE check alone, so only a WANTED row that NAMES a
// hull isolates the AssignedShip half.
//
// ledgerHolds' own doc calls this combination out: no writer produces it today, and the alignment
// between the hull index and CountOwnedProbes holds only because of that. Which is exactly why it
// must be pinned — a convention has no test to fail when a future writer breaks it, and the failure
// mode here is flying a second hull at a placement already promised to one.
func TestOrphanDispatch_NeverTargetsAWantedPlacementThatAlreadyNamesAHull(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
	promised := wantedAt("X1-KP23-D40")
	promised.AssignedShip = "PROBE-PROMISED"
	world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}] = promised
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, "PROBE-PROMISED", world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip,
		"a row that already names a hull is never re-pointed, whatever state it is in")
	require.Equal(t, 0, dispatchedCount(t, logger))
}

// LEAVING A HULL FOR ADOPTION ONLY MATTERS WHEN ADOPTION DID NOT GET TO IT, and this is the shape
// where that happens: more orphans standing on their own open placements than adoption's per-tick
// budget can absorb. The leftovers must still not be flown anywhere.
//
// Ordering hides this in the ordinary case — adoption runs first and this pass re-reads the ledger,
// so an adopted hull is already recorded by the time dispatch looks. Only a backlog past
// DefaultMaxAdoptions exposes the guard, and without it those leftovers get flown to the open
// placements elsewhere in the system: a wasted hop to do what adoption does for free NEXT tick, and
// the placement under each hull's feet left open anyway.
func TestOrphanDispatch_LeavesTheOrphansAdoptionsBudgetCouldNotReach(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil

	// Two more orphans-on-their-own-placement than adoption can absorb in one tick.
	for i := 0; i < DefaultMaxAdoptions+2; i++ {
		waypoint := fmt.Sprintf("X1-KP23-W%02d", i)
		world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}] = wantedAt(waypoint)
		world.fleet.ships = append(world.fleet.ships,
			idleOrphan(t, fmt.Sprintf("TORWIND-B%02d", i), waypoint))
	}
	// Open placements nobody is standing on, which is where the leftovers would wrongly be sent.
	for i := 0; i < 5; i++ {
		waypoint := fmt.Sprintf("X1-KP23-Z%02d", i)
		world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}] = wantedAt(waypoint)
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, DefaultMaxAdoptions, logger.payload("parked_sensing_cycle")["adopted_stranded"],
		"adoption fills what its budget allows, in place")
	require.Equal(t, 0, dispatchedCount(t, logger),
		"the hulls adoption could not reach are still ADOPTION's — they are standing on open placements")
	for i := 0; i < 5; i++ {
		require.Empty(t, world.ledger.slots[psSlotKey{fmt.Sprintf("X1-KP23-Z%02d", i), parkedsensing.SlotKindMarket}].AssignedShip,
			"no leftover was flown to a placement it did not need to fly to")
	}
}

// buyableWorld is a world where the drain CAN genuinely buy: a probe-selling yard in-system with a
// hull of ours docked at it to buy through, and one open placement to buy for.
//
// Every other fixture in this file leaves the drain inert (no yard, no docked buyer), which is fine
// for testing this pass in isolation but useless for testing its ORDERING against the drain — hence
// the control arm below, which proves the fixture really does buy before the real arm asserts that
// it did not.
func buyableWorld(t *testing.T) *cutoverWorld {
	t.Helper()
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.catalog.yards["X1-KP23"] = []string{"X1-KP23-Y1"}
	world.shipPos.docked["X1-KP23-Y1"] = "TORWIND-BUYER"
	world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}] = wantedAt("X1-KP23-D40")
	return world
}

// THE ECONOMIC POINT OF THE WHOLE PASS, and the reason it runs BEFORE the drain.
//
// A placement filled by a hull we already own is a placement the drain does not buy a hull for. Run
// it after the drain instead and the tick spends real credits on a probe for a placement an idle
// probe was about to answer for nothing — which is the same argument that puts adoption ahead of the
// drain, except that here the hull is not merely counted but actually put to work.
func TestOrphanDispatch_RunsBeforeTheDrain_SoAFilledPlacementIsNeverAlsoBought(t *testing.T) {
	// CONTROL: with no idle orphan to answer it, the drain buys for this placement. Without this
	// arm the real arm below would pass against a fixture that simply never buys anything.
	t.Run("control — no orphan, so the drain buys", func(t *testing.T) {
		world := buyableWorld(t)
		logger := &capturingLogger{}

		require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

		require.Equal(t, 1, logger.payload("parked_sensing_cycle")["buy_bought"],
			"precondition: this fixture really can buy")
		require.Contains(t, world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip, "PROBE-BOUGHT")
	})

	t.Run("an idle orphan answers it first, and nothing is bought", func(t *testing.T) {
		world := buyableWorld(t)
		world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
		world.fleet.ships = []*navigation.Ship{
			probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
			idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
		}
		world.shipPos.at["TORWIND-14"] = parkedsensing.ShipPos{
			Waypoint: "X1-KP23-A2", NavStatus: navigation.NavStatusDocked,
		}
		logger := &capturingLogger{}

		require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

		require.Equal(t, "TORWIND-14", world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip,
			"the hull we already own answers the placement")
		require.Equal(t, 0, logger.payload("parked_sensing_cycle")["buy_bought"],
			"and the drain buys NOTHING: running this pass after it would spend credits on a probe "+
				"for a placement an idle probe was about to fill for free")
	})
}

// A hull with no location is IN TRANSIT, not an error: there is nowhere to dispatch it FROM, and
// commanding a hull we cannot place is how a ship ends up desynced. It is picked up next tick.
func TestOrphanDispatch_SkipsAHullWithNoRecordedLocation(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-KP23": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}] = wantedAt("X1-KP23-D40")
	world.fleet.ships = []*navigation.Ship{flyingScoutProbe(t, "TORWIND-14")}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 0, dispatchedCount(t, logger))
	require.Empty(t, world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip)
}

// THE OPERATOR'S QUESTION, ANSWERED AT THE COORDINATOR. "Expansion off = don't buy any
// probe, keep sensing with the existing ones." This is the same live fleet with the switch OFF: the
// four stacked idle hulls we already own still reach their placements, and nothing is bought.
//
// IT PASSES ON THE UNFIXED CODE TOO, and that is the point rather than a weakness. The dispatch has
// never read this knob — the defect was one layer up, in the pass that produces the placements it
// fills — so this pins the half of the operator's sentence that was ALREADY true, and would catch
// the plausible-looking future change that "consistently" gates the dispatch on expansion as well.
// The half that was broken is pinned in the domain, where the fix lives:
// parkedsensing.TestAdvanceExpansion_SpendPaused_StillMarksTheFrontier.
func TestOrphanDispatch_ExpansionOff_StillPlacesOwnedProbesAndBuysNothing(t *testing.T) {
	world := liveFleetWorld(t)
	world.cmd.ExpansionEnabled = 2 // the off-switch, as the operator sets it
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 4, dispatchedCount(t, logger),
		"the switch stops SPENDING, not the free placement of hulls already paid for")
	for hull, waypoint := range map[string]string{
		"TORWIND-14": "X1-KP23-D40",
		"TORWIND-15": "X1-KP23-D41",
		"TORWIND-F":  "X1-KP23-F46",
		"TORWIND-4":  "X1-KP23-G48",
	} {
		row := world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}]
		require.Equal(t, hull, row.AssignedShip, "%s still answers the placement at %s with expansion off", hull, waypoint)
		require.Equal(t, parkedsensing.SlotStateInTransit, row.State)
	}

	// RULINGS #4, asserted at the port that actually moves money rather than inferred from a
	// counter. Nothing was bought and nothing was even priced.
	require.Zero(t, world.calls.count("buy"), "expansion off must reach no purchase")
	require.Empty(t, world.purchaser.owners, "no probe purchase was claimed while expansion is off")
}

package commands

// run_probe_orphan_dispatch_xsys_test.go covers the one thing a same-system test structurally
// cannot: a hull LEAVING the system it is standing in.
//
// WHY THESE TESTS RUN SEVERAL TICKS. A crossing is not an action, it is a sequence of them — onto
// the gate, through it, once per gate, then one in-system hop, then a berth — and every one of those
// steps is issued by a LATER tick reading where the hull ended up. A single-tick test can only ever
// observe that a command was issued, and "issued the same command forever" is exactly what a broken
// walk looks like from there. So the fixtures below run consecutive ReconcileOnce calls against a
// ledger and a ships table that PERSIST between them, and assert on where the hull actually ends up.
//
// The live map these are cut from, measured from the gate_edges rows on the day this was written:
//
//	X1-KP23 (home, every placement filled)  ── X1-GF41, X1-MY3, X1-QG29, X1-AJ10, X1-XD91
//	X1-GF41 ── X1-KC84, X1-RX9, X1-UV2      X1-MY3 ── X1-BT49, X1-MC90      X1-AJ10 ── X1-BT49
//
// Every one of the eight systems holding an open placement — 66 of them, and not one in a system
// holding an idle hull — sits at one or two jumps. That is the whole reason the bound is the walk's
// reach rather than a single gate.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// crossingWorld is the live shape reduced to its bones: home is FULL (its one placement is taken by
// an incumbent, which is why the orphan behind it cannot be adopted in place), every open placement
// is in another system, and the mover really walks.
//
// homeExtra lets a caller add an open placement AT HOME, which is the control that proves a hull
// only ever crosses a gate because there was nothing nearer.
func crossingWorld(t *testing.T, systems ...string) *cutoverWorld {
	t.Helper()
	verdicts := map[string]string{"X1-KP23": parkedsensing.VerdictInScope}
	for _, system := range systems {
		verdicts[system] = parkedsensing.VerdictInScope
	}
	world := steadyWorld(t, verdicts)
	world.posts.posts = nil

	// The incumbent that blocks in-place adoption, and the orphan stacked behind it. This stacking
	// IS the bug class: sensing_slots is keyed (player, waypoint), so the second hull on a spoken-for
	// waypoint can never be recorded where it stands.
	world.ledger.slots[psSlotKey{"X1-KP23-A2", parkedsensing.SlotKindMarket}] = parkedAt("X1-KP23-A2", "TORWIND-E")
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "TORWIND-E", "X1-KP23-A2", parkedsensing.SensingParkedFleetTag),
		idleOrphan(t, "TORWIND-14", "X1-KP23-A2"),
	}
	for _, ship := range world.fleet.ships {
		world.shipPos.at[ship.ShipSymbol()] = parkedsensing.ShipPos{
			Waypoint: ship.CurrentLocation().Symbol, NavStatus: navigation.NavStatusDocked,
		}
	}

	world.mover.walk = &walkingShips{ships: world.shipPos, gates: world.gates}
	return world
}

// runTicks drives consecutive reconciles against the SAME world, which is what makes the ledger and
// the ships table persist across them — the property the whole walk depends on.
func runTicks(t *testing.T, world *cutoverWorld, ticks int) {
	t.Helper()
	for tick := 1; tick <= ticks; tick++ {
		require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, &capturingLogger{}), world.cmd),
			"tick %d must return — a tick that blocks on a crossing is the regression sp-uwxwo exists to prevent", tick)
	}
}

// systemOf reports which system the ships table currently has a hull in.
func systemOf(world *cutoverWorld, hull string) string {
	return shared.ExtractSystemSymbol(world.shipPos.at[hull].Waypoint)
}

// THE HEADLINE. An idle probe stacked behind an incumbent at home, with no placement at home it
// could ever fill, walks across a gate and BERTHS at a placement in the next system.
//
// This is the fix stated as an outcome rather than a predicate. Before it, every one of the 66 open
// placements was in a system holding no idle hull, so the same-system rule made this pass a no-op by
// construction — it could run every tick forever and dispatch nothing, which is exactly what it was
// doing on the live fleet.
func TestOrphanDispatch_WalksAnIdleProbeAcrossAGateAndBerthsIt(t *testing.T) {
	world := crossingWorld(t, "X1-GF41")
	world.gates.link("X1-KP23", "X1-GF41")
	world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}] = wantedAt("X1-GF41-M7")

	require.Equal(t, "X1-KP23", systemOf(world, "TORWIND-14"), "precondition: it starts at home")

	runTicks(t, world, 6)

	require.Equal(t, "X1-GF41", systemOf(world, "TORWIND-14"),
		"the hull actually LEFT its home system — the one thing a same-system test can never show")
	require.Equal(t, "X1-GF41-M7", world.shipPos.at["TORWIND-14"].Waypoint,
		"and arrived at the placement itself, not merely somewhere in that system")
	require.Equal(t, parkedsensing.SlotStateParked, world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}].State,
		"the placement is FILLED: the walk finished and the machine berthed the hull")
	require.Equal(t, "TORWIND-14", world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}].AssignedShip)
}

// TWO RINGS, so the walk carries the hull across TWO gates on consecutive ticks.
//
// This is the case the bound exists for. Naming X1-BT49 as a jump from home is a jump the API
// rejects outright, so the walk names the FIRST-RING system instead and re-derives after arriving —
// which means a hull may be sent anywhere the search reaches, not merely next door. Bounding this
// pass to adjacent systems instead would strand 32 of the 66 live placements (BT49 14, MC90 6,
// KC84 6, UV2 5, RX9 1) that the walk resolves perfectly well today.
func TestOrphanDispatch_CarriesAHullAcrossTwoGatesToAPlacementTwoRingsOut(t *testing.T) {
	world := crossingWorld(t, "X1-MY3", "X1-BT49")
	world.gates.link("X1-KP23", "X1-MY3")
	world.gates.link("X1-MY3", "X1-BT49") // BT49 is TWO jumps from home, exactly as on the live map
	world.ledger.slots[psSlotKey{"X1-BT49-AA9E", parkedsensing.SlotKindMarket}] = wantedAt("X1-BT49-AA9E")

	runTicks(t, world, 8)

	require.Equal(t, "X1-BT49", systemOf(world, "TORWIND-14"),
		"two gates crossed on consecutive ticks, each step decided from where the hull actually was")
	require.Equal(t, parkedsensing.SlotStateParked, world.ledger.slots[psSlotKey{"X1-BT49-AA9E", parkedsensing.SlotKindMarket}].State)
	require.Equal(t, "TORWIND-14", world.ledger.slots[psSlotKey{"X1-BT49-AA9E", parkedsensing.SlotKindMarket}].AssignedShip)
}

// AND NOT ONE SYSTEM FURTHER. A placement three jumps out is refused, because the walk cannot
// resolve it.
//
// Overshooting the bound does not fail loudly, which is exactly why this has to be refused HERE:
// nextHopToward would name no next system, the step would error, and the slot would sit IN_TRANSIT
// naming a hull that never arrives — while that hull went on counting against the probe cap, which
// is the over-count that stops the drain buying a replacement. A permanently stalled hull and a
// permanently held placement, silently.
func TestOrphanDispatch_RefusesAPlacementBeyondTheWalksReach(t *testing.T) {
	world := crossingWorld(t, "X1-MY3", "X1-BT49", "X1-FAR9")
	world.gates.link("X1-KP23", "X1-MY3")
	world.gates.link("X1-MY3", "X1-BT49")
	world.gates.link("X1-BT49", "X1-FAR9") // three jumps out: one ring past what the walk can name
	world.ledger.slots[psSlotKey{"X1-FAR9-M1", parkedsensing.SlotKindMarket}] = wantedAt("X1-FAR9-M1")

	runTicks(t, world, 4)

	require.Empty(t, world.ledger.slots[psSlotKey{"X1-FAR9-M1", parkedsensing.SlotKindMarket}].AssignedShip,
		"a placement the walk cannot route to is never handed a hull")
	require.Equal(t, parkedsensing.SlotStateWanted, world.ledger.slots[psSlotKey{"X1-FAR9-M1", parkedsensing.SlotKindMarket}].State,
		"and the placement stays openly WANTED rather than being locked IN_TRANSIT behind a hull that never arrives")
	require.Equal(t, "X1-KP23", systemOf(world, "TORWIND-14"), "the hull never left")
}

// THE BOUND IS THE WALK'S OWN, and this is what pins the two together.
//
// The reach this pass hands out and the reach the walk can resolve are the same number because they
// are the same CONSTANT — there is no second copy to drift. A test that hard-coded 2 would go on
// passing after somebody widened the walk and left this pass behind, so it asserts the relationship
// instead: a placement exactly at the bound is dispatched, one past it is not.
//
// TWO HULLS, AND THAT IS THE POINT OF THE FIXTURE. With one hull this test passes for the wrong
// reason — nearest-first hands it the in-bound placement whether or not the reach overshoots, so the
// out-of-bound row is never even offered and the bound is never consulted. A second idle hull is
// what forces the question: once the near placement is consumed, the only row left is the far one,
// and a reach that overshoots by a single ring will take it. Verified by mutation: widening this
// pass's search to MaxWalkRings+1 fails this test as well as RefusesAPlacementBeyondTheWalksReach,
// where before the second hull was added it passed straight through the mutation.
func TestOrphanDispatch_ReachIsExactlyTheWalksSharedRingBound(t *testing.T) {
	require.Equal(t, 2, parkedsensing.MaxWalkRings,
		"the live map's stranded systems sit at one and two jumps; if this bound changes the fixtures below need re-cutting")

	// A chain one system longer than the bound, so both edges of it are exercised on one map.
	atBound, pastBound := "X1-R2", "X1-R3"
	world := crossingWorld(t, "X1-R1", atBound, pastBound)
	world.gates.link("X1-KP23", "X1-R1")
	world.gates.link("X1-R1", atBound)
	world.gates.link(atBound, pastBound)
	world.ledger.slots[psSlotKey{pastBound + "-M1", parkedsensing.SlotKindMarket}] = wantedAt(pastBound + "-M1")
	world.ledger.slots[psSlotKey{atBound + "-M1", parkedsensing.SlotKindMarket}] = wantedAt(atBound + "-M1")
	world.fleet.ships = append(world.fleet.ships, idleOrphan(t, "TORWIND-15", "X1-KP23-A2"))
	world.shipPos.at["TORWIND-15"] = parkedsensing.ShipPos{
		Waypoint: "X1-KP23-A2", NavStatus: navigation.NavStatusDocked,
	}

	runTicks(t, world, 8)

	require.Equal(t, "TORWIND-14", world.ledger.slots[psSlotKey{atBound + "-M1", parkedsensing.SlotKindMarket}].AssignedShip,
		"a placement AT the walk's reach is dispatched")
	require.Empty(t, world.ledger.slots[psSlotKey{pastBound + "-M1", parkedsensing.SlotKindMarket}].AssignedShip,
		"a placement one ring PAST it is not, even with an idle hull left over and nothing nearer to give it")
	require.Equal(t, "X1-KP23", systemOf(world, "TORWIND-15"),
		"and that spare hull stays home rather than being walked toward a system the walk cannot route to")
}

// NEAREST FIRST. A hull never crosses a gate while its own system has an open placement, and never
// crosses two while a one-jump placement is open.
//
// The ordering is the allocation policy, not an accident of map iteration: the nearest placement is
// both the cheapest crossing and the soonest arrival, and presence in a NEAR system is what makes
// that system's shipyards buyable and stages the next charting seed.
func TestOrphanDispatch_PrefersTheNearestOpenPlacement(t *testing.T) {
	t.Run("its own system beats a neighbour", func(t *testing.T) {
		world := crossingWorld(t, "X1-GF41")
		world.gates.link("X1-KP23", "X1-GF41")
		world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}] = wantedAt("X1-GF41-M7")
		world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}] = wantedAt("X1-KP23-D40") // open, and no gate to reach it
		logger := &capturingLogger{}

		require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

		require.Equal(t, "TORWIND-14", world.ledger.slots[psSlotKey{"X1-KP23-D40", parkedsensing.SlotKindMarket}].AssignedShip,
			"the in-system placement is taken: a hop that crosses no gate is always the cheaper answer")
		require.Empty(t, world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}].AssignedShip)
	})

	t.Run("a neighbour beats a neighbour's neighbour", func(t *testing.T) {
		world := crossingWorld(t, "X1-MY3", "X1-BT49")
		world.gates.link("X1-KP23", "X1-MY3")
		world.gates.link("X1-MY3", "X1-BT49")
		world.ledger.slots[psSlotKey{"X1-BT49-AA9E", parkedsensing.SlotKindMarket}] = wantedAt("X1-BT49-AA9E") // two jumps
		world.ledger.slots[psSlotKey{"X1-MY3-M1", parkedsensing.SlotKindMarket}] = wantedAt("X1-MY3-M1")       // one jump
		logger := &capturingLogger{}

		require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

		require.Equal(t, "TORWIND-14", world.ledger.slots[psSlotKey{"X1-MY3-M1", parkedsensing.SlotKindMarket}].AssignedShip,
			"the one-jump placement is taken ahead of the two-jump one")
		require.Empty(t, world.ledger.slots[psSlotKey{"X1-BT49-AA9E", parkedsensing.SlotKindMarket}].AssignedShip)
	})
}

// AN UNREADABLE GATE GRAPH DISPATCHES NOBODY ACROSS IT, and says so.
//
// This is the fail-closed direction and it is the sharp one. "We could not read the topology" must
// never be taken as "everything is reachable" — that reading is precisely what sends a hull toward a
// system the walk cannot route to, where it sits IN_TRANSIT forever holding a placement and counting
// against the probe cap. An unreadable graph degrades this pass to same-system-only, which is the
// behaviour it had before the walk existed and costs nothing.
func TestOrphanDispatch_AnUnreadableGateGraphRefusesToCross(t *testing.T) {
	world := crossingWorld(t, "X1-GF41")
	world.gates.link("X1-KP23", "X1-GF41")
	world.gates.unreadable = map[string]bool{"X1-KP23": true}
	world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}] = wantedAt("X1-GF41-M7")
	logger := &capturingLogger{}

	err := world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd)

	require.Error(t, err,
		"a topology read that keeps failing must be visible: swallowed, it looks exactly like a map with no gates in it")
	require.Contains(t, err.Error(), "gate neighbours")
	require.Empty(t, world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}].AssignedShip,
		"and NOTHING is dispatched on a reach we could not confirm")
}

// A MAP WITH NO GATES AT ALL is not an error, and is not a crossing either.
//
// The distinction from the case above is the whole of fail-closed: "we know, and there are none"
// is a legitimate steady state (an unswept frontier, a system whose edges are all under
// construction) and must stay quiet, while "we do not know" must be loud. Both send the hull
// nowhere.
func TestOrphanDispatch_NoStoredAdjacencyIsQuietAndDispatchesNothingAcross(t *testing.T) {
	world := crossingWorld(t, "X1-GF41") // no link() call: the graph is empty, not broken
	world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}] = wantedAt("X1-GF41-M7")
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 0, dispatchedCount(t, logger))
	require.Empty(t, world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}].AssignedShip)
}

// THE TICK NEVER BLOCKS, and it is asserted on the crossing ticks specifically.
//
// sp-uwxwo is the regression this whole design is shaped around: RouteShipCommand resolves a gate
// path and FLIES it, waiting out every leg and every jump cooldown, and one such call inside a tick
// stalls the buy drain, the reaper, adoption and expansion behind it. runTicks fails by tick NUMBER
// if a reconcile does not return, and this test additionally pins the mechanism: the crossing is
// made of one-step commands, so the number of movement commands GROWS tick by tick instead of one
// call swallowing the journey.
func TestOrphanDispatch_ACrossingNeverBlocksATick(t *testing.T) {
	world := crossingWorld(t, "X1-GF41")
	world.gates.link("X1-KP23", "X1-GF41")
	world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}] = wantedAt("X1-GF41-M7")

	var perTick []int
	for tick := 1; tick <= 4; tick++ {
		require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, &capturingLogger{}), world.cmd),
			"tick %d returned an error rather than completing", tick)
		perTick = append(perTick, len(world.mover.moves))
	}

	require.Equal(t, []int{1, 2, 3, 3}, perTick,
		"one movement command per tick while the crossing runs, then none once the hull is there — "+
			"a blocking route would have flown the whole journey inside tick 1")
	require.Equal(t, "X1-GF41-M7", world.shipPos.at["TORWIND-14"].Waypoint)
}

// IDEMPOTENT ACROSS A CROSSING. The hull is dispatched ONCE, and the ticks that carry it across
// re-dispatch nobody — the row names it from the first write onward, so it is struck off the orphan
// list while it is still in flight.
//
// Without this the pass would re-enter a mid-flight hull every tick and hand it a second placement,
// leaving one hull answering two rows and a placement held by a hull that is walking away from it.
func TestOrphanDispatch_DispatchesAHullOnceEvenWhileItIsStillCrossing(t *testing.T) {
	world := crossingWorld(t, "X1-GF41", "X1-QG29")
	world.gates.link("X1-KP23", "X1-GF41")
	world.gates.link("X1-KP23", "X1-QG29")
	world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}] = wantedAt("X1-GF41-M7")
	world.ledger.slots[psSlotKey{"X1-QG29-M1", parkedsensing.SlotKindMarket}] = wantedAt("X1-QG29-M1") // a second open placement, still open

	dispatchedTotal := 0
	for tick := 1; tick <= 5; tick++ {
		logger := &capturingLogger{}
		require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))
		dispatchedTotal += dispatchedCount(t, logger)
	}

	require.Equal(t, 1, dispatchedTotal,
		"one hull, one dispatch — the ticks carrying it across must not offer it a second placement")

	named := 0
	for _, waypoint := range []string{"X1-GF41-M7", "X1-QG29-M1"} {
		if world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}].AssignedShip == "TORWIND-14" {
			named++
		}
	}
	require.Equal(t, 1, named, "TORWIND-14 answers exactly one placement, never two")
}

// TWO HULLS, TWO SYSTEMS, TWO PLACEMENTS — the consuming discipline holds across the gate too.
//
// Consuming is what buys THROUGHPUT here (the ledger's guarded transition is what buys safety): read
// without consuming, every hull in a system is offered the same first placement, one wins and the
// rest are skipped and never offered any of the others. That was one dispatch per system per tick,
// forever, with 66 placements standing empty.
func TestOrphanDispatch_TwoHullsTakeTwoDistinctCrossGatePlacements(t *testing.T) {
	world := crossingWorld(t, "X1-GF41")
	world.gates.link("X1-KP23", "X1-GF41")
	world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}] = wantedAt("X1-GF41-M7")
	world.ledger.slots[psSlotKey{"X1-GF41-M8", parkedsensing.SlotKindMarket}] = wantedAt("X1-GF41-M8")
	world.fleet.ships = append(world.fleet.ships, idleOrphan(t, "TORWIND-15", "X1-KP23-A2"))
	world.shipPos.at["TORWIND-15"] = parkedsensing.ShipPos{
		Waypoint: "X1-KP23-A2", NavStatus: navigation.NavStatusDocked,
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 2, dispatchedCount(t, logger), "both idle hulls are put to work in the same tick")
	require.NotEqual(t,
		world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}].AssignedShip,
		world.ledger.slots[psSlotKey{"X1-GF41-M8", parkedsensing.SlotKindMarket}].AssignedShip,
		"and onto DISTINCT placements — the same hull answering both is the bug consuming prevents")
}

// EVERY GUARD THAT KEEPS THIS PASS FROM TAKING A HULL IT DOES NOT OWN STILL HOLDS WHEN THE TARGET IS
// ACROSS A GATE. Widening WHERE a hull may be sent must not widen WHICH hulls may be sent, and each
// of these is verified load-bearing on the live fleet rather than theoretical.
func TestOrphanDispatch_OwnershipGuardsStillHoldForCrossGateTargets(t *testing.T) {
	guards := map[string]func(t *testing.T, world *cutoverWorld){
		// Three live probes sit `active` on scout_tour containers. Flying one mid-tour means two
		// writers on one hull and a ship stranded between two intentions (RULINGS #3).
		"a hull an active container is driving": func(t *testing.T, world *cutoverWorld) {
			world.fleet.ships = []*navigation.Ship{
				touringProbe(t, "TORWIND-14", "X1-KP23-A2", "scout_tour-TORWIND-14-b4bba4bb"),
			}
		},
		// The post list is read permissively, so an unreadable one must not read as "nobody is
		// manned" — that is the shape that flies the probe manning the surviving home post.
		"a hull manning a scout post": func(t *testing.T, world *cutoverWorld) {
			world.posts.posts = []*domainScouting.ScoutPost{{
				PlayerID: testPlayerID, SystemSymbol: "X1-KP23",
				Kind: domainScouting.PostKindStanding, AssignedHull: "TORWIND-14",
			}}
		},
		// Dedicated to a fleet that is not ours to take (RULINGS #7).
		"a hull dedicated to a live foreign fleet": func(t *testing.T, world *cutoverWorld) {
			world.fleet.ships = []*navigation.Ship{
				probeWithFleet(t, "TORWIND-14", "X1-KP23-A2", "mining_ops"),
			}
		},
	}

	for name, breakIt := range guards {
		t.Run(name, func(t *testing.T) {
			world := crossingWorld(t, "X1-GF41")
			world.gates.link("X1-KP23", "X1-GF41")
			world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}] = wantedAt("X1-GF41-M7")
			breakIt(t, world)
			logger := &capturingLogger{}

			require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

			require.Equal(t, 0, dispatchedCount(t, logger))
			require.Empty(t, world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}].AssignedShip,
				"an open placement across a gate is not a reason to take a hull that is not ours")
		})
	}
}

// THE WRITE ORDER IS STILL A MONEY GUARD when the target is across a gate.
//
// The row is what CountOwnedProbes reads, so it goes FIRST and the fleet tag follows best-effort. A
// crash between them leaves the hull recorded and untagged — it counts against the probe cap, and
// the placement machine re-tags it on first use. The other order leaves a hull TAGGED with no row
// anywhere: invisible to the cap for good, which then authorises re-buying a probe we already own.
// That is the exact shape that cost this fleet 245,316 credits (RULINGS #4).
func TestOrphanDispatch_AFailedCrossGateRowWriteLeavesTheHullUntagged(t *testing.T) {
	world := crossingWorld(t, "X1-GF41")
	world.gates.link("X1-KP23", "X1-GF41")
	world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}] = wantedAt("X1-GF41-M7")
	world.ledger.transitionErr = map[string]error{"X1-GF41-M7": errors.New("ledger unavailable")}
	logger := &capturingLogger{}

	require.Error(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.NotEqual(t, parkedsensing.SensingParkedFleetTag, world.tagger.tagged["TORWIND-14"],
		"the tag is never written when the row write failed: a tagged hull with no row is invisible "+
			"to the probe cap, which then authorises buying a replacement for a hull we already own")
}

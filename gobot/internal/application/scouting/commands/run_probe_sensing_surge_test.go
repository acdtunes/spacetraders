package commands

// run_probe_sensing_surge_test.go covers the SENSING SURGE (sp-zvywu Part 1): the
// pass that sends surplus probes we already own into the charted-but-unpriced map,
// several at a time.
//
// THE NUMBERS THESE FIXTURES ARE CUT FROM, measured live the night the pass was
// written and independently verified:
//
//	sensing_systems                      1,215
//	charted (waypoints, open era)        1,183
//	systems with ANY market_data           116   <- under 10% priced
//	CHARTED BUT UNPRICED                 1,067   <- the work pool
//	probes                                 686   (388 in parked slots, ~300 surplus)
//	discovery rate before this pass    1-2 systems/hr
//
// So every fixture below supplies MORE pool than the cap and MORE surplus hulls
// than the cap. That is not padding: a cap over a nearest-first allocator is never
// consulted unless the fixture saturates it, and a pool the size of the cap makes
// "dispatch up to the cap" and "dispatch the whole pool" indistinguishable — the
// test would pass against code that had no cap at all.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// surgeHome is where every surplus probe in these fixtures stands, and surgeStack is
// the one waypoint in it they are all stacked on — see surgeWorld.
const (
	surgeHome  = "X1-KP23"
	surgeStack = surgeHome + "-A2"
)

// surgeWorld is the live shape reduced to its bones: a home system whose own
// placements are all filled, a ring of charted-but-unpriced systems one gate out,
// and a pile of surplus probes standing at home with nothing to do.
//
// poolSize systems are put in the pool and surplus probes are parked at home. Both
// are passed rather than fixed so a test can state the saturation it needs.
func surgeWorld(t *testing.T, poolSize, surplus int) *cutoverWorld {
	t.Helper()
	world := steadyWorld(t, map[string]string{surgeHome: parkedsensing.VerdictInScope})
	world.posts.posts = nil

	// The pool: charted systems with a marketplace and no prices, all ONE gate from
	// home so the walk can carry a hull to any of them. Ranked identically (one
	// market, one waypoint each) unless a test says otherwise, so the count under
	// test is the CAP rather than the ranking.
	world.pool.pool = nil
	for i := 0; i < poolSize; i++ {
		system := fmt.Sprintf("X1-POOL%02d", i)
		world.gates.link(surgeHome, system)
		world.pool.pool = append(world.pool.pool, UnpricedSystem{
			System:          system,
			MarketWaypoints: []string{system + "-M1"},
			Waypoints:       1,
		})
	}

	// THE STACK, and it is the live shape rather than a convenience. Surplus probes sit
	// at the yard that sold them, BEHIND an incumbent's placement row: sensing_slots is
	// keyed (player, waypoint, kind), so a second hull on a spoken-for waypoint can
	// never be recorded where it stands. That is why these hulls are named by no row at
	// all, which is what makes them surplus.
	//
	// It is also what makes the counts below exact. A fixture whose orphans each stood
	// at their own free waypoint would have ADOPTION absorb them as parked spares before
	// this pass ever saw them — and every "dispatches N" assertion would then be
	// measuring adoption's per-tick budget instead of the surge's cap.
	world.ledger.slots[psSlotKey{surgeStack, parkedsensing.SlotKindMarket}] = parkedAt(surgeStack, "INCUMBENT")
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "INCUMBENT", surgeStack, parkedsensing.SensingParkedFleetTag),
	}
	for i := 0; i < surplus; i++ {
		hull := fmt.Sprintf("SURPLUS-%02d", i)
		// Idle, untagged, named by no slot row and manning no post: genuine surplus.
		world.fleet.ships = append(world.fleet.ships, idleOrphan(t, hull, surgeStack))
	}
	registerShipPositions(world)
	return world
}

// registerShipPositions mirrors the fleet into the ships-TABLE read, which is a
// different port from the whole-fleet enumeration — the placement machine refuses to
// command a hull it cannot locate there.
func registerShipPositions(world *cutoverWorld) {
	for _, ship := range world.fleet.ships {
		world.shipPos.at[ship.ShipSymbol()] = parkedsensing.ShipPos{
			Waypoint: ship.CurrentLocation().Symbol, NavStatus: navigation.NavStatusDocked,
		}
	}
}

// surgedRows is every placement the surge created, identified by its WRITE SHAPE
// rather than by the row it leaves behind: a MARKET placement UPSERTED in IN_TRANSIT
// already naming its hull.
//
// THE WRITE SHAPE IS THE ONLY HONEST DISCRIMINATOR, and reading the resulting rows
// instead is a fixture bug that already fired once here. The ORPHAN DISPATCH leaves
// byte-identical rows — MARKET, IN_TRANSIT, hull named — because it TRANSITIONS a
// placement the screen declared, and it runs immediately before the surge in the same
// tick. A row-shaped assertion therefore counted its berthings as surges, and the
// "leaves declared placements alone" test passed while the surge had in fact written
// nothing at all.
//
// No other pass produces this upsert: adoption upserts a PARKED SPARE, a finishing
// seed upserts a PARKED row, and the screen upserts WANTED metadata with no hull.
func surgedRows(world *cutoverWorld) []parkedsensing.SlotRecord {
	var out []parkedsensing.SlotRecord
	for _, record := range world.ledger.upserted {
		if record.Kind != parkedsensing.SlotKindMarket || record.State != parkedsensing.SlotStateInTransit {
			continue
		}
		if record.AssignedShip == "" {
			continue
		}
		out = append(out, record)
	}
	return out
}

func surgeTick(t *testing.T, world *cutoverWorld) {
	t.Helper()
	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, &capturingLogger{}), world.cmd))
}

// surgeTickLogged is surgeTick with the heartbeat KEPT, for the one assertion whose
// subject is the number an operator reads rather than the rows the tick wrote.
func surgeTickLogged(t *testing.T, world *cutoverWorld) *capturingLogger {
	t.Helper()
	logger := &capturingLogger{}
	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))
	return logger
}

// THE HEADLINE. A fleet with surplus probes and a large charted-but-unpriced pool
// dispatches up to the in-flight cap IN ONE TICK.
//
// This is the whole bead stated as an outcome. Before this pass the fleet priced 1-2
// systems an hour: the only routes into unpriced space were the expansion engine's
// charting seeds (one hull, one system, a whole tour) and the orphan dispatch, which
// structurally cannot reach any of these systems because they carry no placement row
// for it to fill. Twelve surplus hulls and twenty priceable systems produced ONE
// dispatch, and eleven hulls stood still.
func TestSensingSurge_DispatchesUpToTheInFlightCapInOneTick(t *testing.T) {
	world := surgeWorld(t, 20, 12) // pool and surplus both strictly greater than the cap

	logger := surgeTickLogged(t, world)

	rows := surgedRows(world)
	require.Len(t, rows, defaultSurgeInFlightCap,
		"one tick fills the whole in-flight cap: %d surplus hulls against %d priceable systems must not trickle",
		12, 20)

	// Eight DISTINCT hulls into eight DISTINCT systems. Either collapsing would make
	// the count above meaningless — one hull re-pointed eight times, or eight hulls
	// stacked on one market, both of which price exactly one system.
	hulls := map[string]bool{}
	systems := map[string]bool{}
	for _, row := range rows {
		hulls[row.AssignedShip] = true
		systems[row.System] = true
	}
	require.Len(t, hulls, defaultSurgeInFlightCap, "one hull per dispatch")
	require.Len(t, systems, defaultSurgeInFlightCap, "one system per dispatch — breadth is the entire return")

	// AND NO SURGED HULL ENDS THE TICK UNTAGGED. An untagged hull reads as idle and
	// undedicated to every other coordinator's ownership sweep — long enough to be
	// poached mid-flight — so the row and the hull have to tell the same story before the
	// tick ends.
	//
	// THIS PINS THE TICK, NOT THIS PASS'S OWN AssignFleet CALL, and the distinction is
	// worth stating because it looks like the latter: the placement machine re-tags every
	// hull it advances (placement.go), and it advances these rows in the same tick, so
	// deleting the tag call here alone leaves the assertion satisfied. That redundancy is
	// deliberate — this pass tags immediately so the probe cap counts the hull even if
	// the placement machine never reaches it — but the OUTCOME is what a test can hold.
	for _, row := range rows {
		require.Equal(t, parkedsensing.SensingParkedFleetTag, world.tagger.tagged[row.AssignedShip],
			"surged hull %s is dedicated to the sensing fleet before the tick ends", row.AssignedShip)
	}

	// AND THE HEARTBEAT CARRIES THE NUMBER. This is the coverage metric the bead is
	// measured by — systems that have never held a price now have a probe on the way —
	// and dispatched_orphans structurally cannot report it. A field the heartbeat fails
	// to carry is a number nobody ever sees.
	require.Equal(t, defaultSurgeInFlightCap, logger.payloads["parked_sensing_cycle"]["surged_unpriced"],
		"the tick reports what it surged, separately from the placements it merely filled")
}

// A FLIGHT INTO AN ALREADY-PRICED SYSTEM CONSUMES NO SURGE CAPACITY, and this is what
// stands between the cap and a pass that is inert in production.
//
// The screen declares MARKET placements in systems the fleet already prices and the
// placement machine flies hulls to them constantly, so a live ledger carries a standing
// population of IN_TRANSIT rows that has nothing whatever to do with this pass. If those
// counted against the cap they would consume the whole budget on every real tick — the
// surge would never dispatch again — while every fixture that put its in-flight rows in
// POOL systems stayed green. Hence inFlightSurge counts only rows in systems the pool
// still names.
//
// Sixteen such flights against a cap of eight: the surge must still fly its full eight.
func TestSensingSurge_AFlightIntoAnAlreadyPricedSystemConsumesNoSurgeCapacity(t *testing.T) {
	world := surgeWorld(t, 20, 12)
	// Sixteen hulls in flight toward systems that are NOT in the pool — the ordinary
	// shape of a fleet whose screen has been working, and twice the cap.
	for i := 0; i < 16; i++ {
		system := fmt.Sprintf("X1-PRICED%02d", i)
		waypoint := system + "-M1"
		world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
			Waypoint: waypoint, System: system, Kind: parkedsensing.SlotKindMarket,
			State: parkedsensing.SlotStateInTransit, AssignedShip: fmt.Sprintf("PRICED-FLYER-%02d", i),
		}
	}

	surgeTick(t, world)

	require.Len(t, surgedRows(world), defaultSurgeInFlightCap,
		"a flight toward a system the fleet already prices is not a surge flight and holds none of its capacity")
}

// THE TUNED CAP GOVERNS THE LIVE TICK, which is a strictly stronger claim than "the
// knob resolves". surge_inflight_cap is registered, bounded and documented, and none of
// that would show if the pass read the launch value and ignored the persisted one — the
// knob would be a dial wired to nothing, which is what a dormant feature looks like from
// the tune surface.
//
// AND ZERO IS NOT AN OFF SWITCH. `tune <key> 0` is the documented fleet-wide revert, so
// it restores the default 8 rather than stopping the pass: the surge ships ARMED and the
// cap is not a seam for turning it off.
func TestSensingSurge_HonoursTheTunedInFlightCap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tuned int
		want  int
	}{
		{"a tuned cap of three flies three", 3, 3},
		{"a tuned cap above the surplus is bounded by the hulls", 40, 12},
		{"zero is the documented revert, not an off switch", 0, defaultSurgeInFlightCap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Forty priceable systems against twelve surplus hulls, so the CAP is what
			// binds at 3 and the HULLS are what bind at 40 — both bounds observable.
			world := surgeWorld(t, 40, 12)
			world.handler.SetLiveConfigReader(staticLiveConfig{"surge_inflight_cap": tc.tuned})

			surgeTick(t, world)

			require.Len(t, surgedRows(world), tc.want,
				"the persisted knob governs this tick, not the launch default")
		})
	}
}

// BEFORE THE CUTOVER THE SURGE DOES NOT RUN AT ALL, and that is a single-writer rule
// (RULINGS #3) rather than an ordering preference. Until the cutover lands the scout
// posts still stand and the scout-post coordinator is actively drawing from its own idle
// pool, so a scout-tagged hull is not surplus — it is another live coordinator's working
// stock, and flying it would give one hull two writers and strand it between two
// intentions.
func TestSensingSurge_DoesNotRunBeforeTheCutover(t *testing.T) {
	world := surgeWorld(t, 20, 12)
	// An EMPTY sensing ledger with no cutover recorded IS the pending state — the same
	// trigger the cutover itself fires on. Everything else is left exactly as the
	// headline fixture has it: twelve surplus hulls and twenty priceable systems on offer.
	world.ledger.systems = map[string]parkedsensing.ExpandSystem{}

	surgeTick(t, world)

	require.Zero(t, world.pool.reads,
		"the pass does not even read its work list while the scout coordinator still owns the fleet")
	require.Empty(t, surgedRows(world), "and not one hull is taken")
}

// THE CAP IS CONSULTED, and the fixture is what makes that provable: nine dispatches
// are already in flight against a cap of eight and a pool of twenty, so a pass that
// ignored the standing population would fly eight more.
//
// The cap is on the IN-FLIGHT POPULATION, not on the per-tick burst, and that is the
// difference the two halves of this test pin. Six in flight leaves room for exactly
// two; nine in flight leaves room for none.
func TestSensingSurge_TopsUpToTheCapAndNoFurther(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    string
		inFlight int
		want     int
	}{
		{"six already flying leaves room for two", parkedsensing.SlotStateInTransit, 6, 2},
		{"the cap exactly reached leaves room for none", parkedsensing.SlotStateInTransit, defaultSurgeInFlightCap, 0},
		{"over the cap never dispatches, and never unwinds", parkedsensing.SlotStateInTransit, 9, 0},
		// BOUGHT counts alongside IN_TRANSIT. A probe the buy queue paid for and aimed at
		// an unpriced system is a flight the placement machine has to advance out of the
		// same per-tick budget, and it arrives to scan against the same API ceiling, so
		// counting only our own would let the two paths saturate it between them.
		{"a bought probe not yet moving counts too", parkedsensing.SlotStateBought, 6, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			world := surgeWorld(t, 20, 12)
			// The standing population, written as the durable rows the pass re-derives it
			// from: a probe already flying toward a pool system. They occupy the LAST pool
			// systems so the ranking still has untouched candidates at its head.
			for i := 0; i < tc.inFlight; i++ {
				system := fmt.Sprintf("X1-POOL%02d", 19-i)
				world.ledger.slots[psSlotKey{system + "-M1", parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
					Waypoint: system + "-M1", System: system, Kind: parkedsensing.SlotKindMarket,
					State: tc.state, AssignedShip: fmt.Sprintf("FLYING-%02d", i),
				}
			}

			surgeTick(t, world)

			require.Len(t, surgedRows(world), tc.want,
				"the tick tops the in-flight population up to the cap and stops (%d already flying, cap %d)",
				tc.inFlight, defaultSurgeInFlightCap)
		})
	}
}

// A DELIVERED-BUT-UNSCANNED PROBE DOES NOT CONSUME IN-FLIGHT CAPACITY, and this is
// the difference between a surge that keeps working and one that wedges after eight
// probes.
//
// A pool system stops being unpriced only when a scan LANDS, and the scan rotation is
// paced against whatever API headroom the rest of the fleet leaves — so the normal
// steady state is probes standing PARKED in systems that are still unpriced, waiting
// their turn. If a PARKED row counted as in flight, every delivered probe would hold
// its capacity slot until the rotation reached it, and the surge would stop for as
// long as the pacer lagged: the cap would silently become a lifetime total rather
// than a concurrency bound.
//
// Eight arrived probes here — the whole cap — and the surge must still fly eight more.
func TestSensingSurge_ADeliveredButUnscannedProbeDoesNotConsumeInFlightCapacity(t *testing.T) {
	world := surgeWorld(t, 20, 12)
	for i := 0; i < defaultSurgeInFlightCap; i++ {
		system := fmt.Sprintf("X1-POOL%02d", 19-i)
		waypoint := system + "-M1"
		hull := fmt.Sprintf("ARRIVED-%02d", i)
		// On station and scanning, in a system whose first scan has not landed yet — so it
		// is still in the pool the surge is handed.
		world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}] = parkedAt(waypoint, hull)
		world.fleet.ships = append(world.fleet.ships,
			probeWithFleet(t, hull, waypoint, parkedsensing.SensingParkedFleetTag))
	}
	registerShipPositions(world)

	surgeTick(t, world)

	require.Len(t, surgedRows(world), defaultSurgeInFlightCap,
		"arrived probes have finished their flight: they hold no in-flight capacity, whatever the rotation is doing")
}

// A PROBE STANDING A SENSING WATCH IS NEVER TAKEN. This is the standing owner rule,
// and it is the one guard whose failure would be catastrophic rather than merely
// wasteful: re-tasking a parked scanner takes its market out of the rotation, and the
// fleet's planner data goes dark where it was working.
//
// THE PREDICATE IS holds.hulls — every hull named by ANY sensing_slots row in ANY
// state. Here every probe is named by a PARKED MARKET row, which is exactly what a
// probe on station looks like, so the pool being wide open and richly ranked must
// still buy nothing.
func TestSensingSurge_NeverTakesAProbeServingASensingSlot(t *testing.T) {
	world := surgeWorld(t, 20, 0)

	// Eight probes, each parked on its own market and each NAMED BY ITS ROW. Idle in
	// every other sense — landed, readable, tagged as ours — so the row is the only
	// thing standing between them and the pool.
	for i := 0; i < 8; i++ {
		hull := fmt.Sprintf("ONSTATION-%02d", i)
		waypoint := fmt.Sprintf("%s-M%d", surgeHome, i)
		world.fleet.ships = append(world.fleet.ships,
			probeWithFleet(t, hull, waypoint, parkedsensing.SensingParkedFleetTag))
		world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}] = parkedAt(waypoint, hull)
	}
	registerShipPositions(world)

	surgeTick(t, world)

	require.Empty(t, surgedRows(world),
		"not one probe on station was re-tasked, however much unpriced space is on offer")
	// And their rows are untouched: still PARKED, still naming the same hull, still at
	// the same waypoint. A pass that moved a scanner would show up here even if it
	// happened to write no new row.
	for i := 0; i < 8; i++ {
		waypoint := fmt.Sprintf("%s-M%d", surgeHome, i)
		row := world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}]
		require.Equal(t, parkedsensing.SlotStateParked, row.State, "%s stays on station", waypoint)
		require.Equal(t, fmt.Sprintf("ONSTATION-%02d", i), row.AssignedShip, "%s keeps its hull", waypoint)
	}
}

// A HULL ON A CHARTING ERRAND IS NEVER TAKEN EITHER, and this is the hole the slot
// index structurally cannot see: claimSpares DELETES the spare's placement row when
// it hands the hull to a seed and records the errand in sensing_systems.seed_ship
// instead. So a seed hull is named by NO slot row and is idle between steps —
// indistinguishable from surplus to every other check.
//
// Taking one would give a hull two writers (RULINGS #3): the expansion engine would
// go on flying it toward its charting target while this pass flew it toward a market.
func TestSensingSurge_NeverTakesAHullOutOnAChartingErrand(t *testing.T) {
	world := surgeWorld(t, 20, 4) // four surplus hulls: two on live errands, one stood down, one free
	// Each surplus hull is actually a seed mid-tour. DISPATCHED and CHARTING are the
	// two ACTIVE states — the same pair expansion's own hasActiveSeed matches.
	world.ledger.systems["X1-DARK1"] = parkedsensing.ExpandSystem{
		System: "X1-DARK1", Verdict: parkedsensing.VerdictPending, CatalogKnown: true,
		SeedShip: "SURPLUS-00", SeedState: parkedsensing.SeedStateDispatched,
	}
	world.ledger.systems["X1-DARK2"] = parkedsensing.ExpandSystem{
		System: "X1-DARK2", Verdict: parkedsensing.VerdictPending, CatalogKnown: true,
		SeedShip: "SURPLUS-01", SeedState: parkedsensing.SeedStateCharting,
	}
	// DONE IS NOT AN ERRAND, and the state test is what says so. A stood-down seed has
	// been RELEASED by the expansion engine — nobody is flying it any more — so the surge
	// must be able to pick it up. A predicate that merely matched "seed_ship is set"
	// would strand every hull that ever charted anything for the rest of the era, and
	// nothing about the two live errands above would notice.
	world.ledger.systems["X1-DARK3"] = parkedsensing.ExpandSystem{
		System: "X1-DARK3", Verdict: parkedsensing.VerdictPending, CatalogKnown: true,
		SeedShip: "SURPLUS-03", SeedState: parkedsensing.SeedStateDone,
	}

	surgeTick(t, world)

	rows := surgedRows(world)
	require.Len(t, rows, 2,
		"the two hulls on LIVE errands stay put; the stood-down seed and the never-seeded hull both fly")
	flown := []string{}
	for _, row := range rows {
		flown = append(flown, row.AssignedShip)
	}
	require.ElementsMatch(t, []string{"SURPLUS-02", "SURPLUS-03"}, flown,
		"the two hulls the expansion engine still owns are left exactly where they are")
}

// WITH NO SURPLUS THE TICK DOES NOTHING — it does not steal, and it does not write.
//
// Every probe here belongs to somebody by a different route than a slot row: one mans
// the surviving home scout post, one is driven by a live scout_tour container. Both
// have a single writer already, and neither is ours (RULINGS #3, RULINGS #7).
//
// BOTH HULLS STAND IN surgeHome, which is gate-linked to every system in the pool, and
// that placement is load-bearing rather than tidy. A hull the pass declines has to be one
// it COULD otherwise have flown — the post-manned hull previously stood in an UNLINKED
// system, so the walk's reach was declining it and deleting the surge's own `manned` index
// changed nothing at all.
func TestSensingSurge_WithNoSurplusTheTickTakesNothing(t *testing.T) {
	world := surgeWorld(t, 20, 0)
	world.posts.posts = []*domainScouting.ScoutPost{{
		PlayerID: testPlayerID, SystemSymbol: surgeHome,
		Kind: domainScouting.PostKindStanding, AssignedHull: "PROBE-ON-POST",
	}}
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "PROBE-ON-POST", surgeHome+"-A1", ""),
		touringProbe(t, "PROBE-ON-TOUR", surgeHome+"-A3", "scout_tour-player-7"),
	}
	registerShipPositions(world)

	surgeTick(t, world)

	require.Empty(t, surgedRows(world), "a fleet with no surplus surges nothing")
	// And the pass DID run: the pool was read, twenty priceable systems were on offer,
	// and it declined all of them. Without this the test would also pass against a pass
	// that never executed — which is the vacuous version of every no-op assertion.
	require.Positive(t, world.pool.reads, "the surge ran and found the fleet had nothing spare")
}

// THE RANKING IS DETERMINISTIC AND STABLE, and it ranks on VALUE rather than on the
// order the store happened to return.
//
// The pool is deliberately handed back in the reverse of its value order, so a pass
// that simply consumed the slice would pick the worst eight. Marketplace count is the
// dominant key — it is the only input that scales what a delivered probe is worth
// permanently — then waypoint richness, then symbol for a total order.
// EVERY KEY IS SEPARATELY FALSIFIABLE, which is what this fixture is built for and
// what its first version got wrong. There, waypoint richness rose with marketplace
// count, so (markets, richness) and (richness) picked the same eight systems and the
// dominant key was untestable — a mutation that deleted it changed nothing.
//
// So the table below ANTI-CORRELATES them: the single-market systems are the richest
// in the pool by an order of magnitude, and within each marketplace tier the richness
// order is deliberately not the alphabetical one. Only (markets desc, richness desc,
// symbol asc) produces the sequence asserted, and RANK04 wins the last slot over
// RANK12 on the symbol key alone — those two tie on everything else.
func TestSensingSurge_RanksByValueDeterministicallyAcrossTicks(t *testing.T) {
	type candidate struct {
		system   string
		markets  int
		richness int
	}
	// Handed back WORST FIRST, so a pass that simply consumed the store's order would
	// take the four single-market systems before anything else.
	candidates := []candidate{
		{"X1-RANK00", 1, 90}, {"X1-RANK01", 1, 80}, {"X1-RANK02", 1, 70}, {"X1-RANK03", 1, 60},
		{"X1-RANK04", 2, 11}, {"X1-RANK05", 2, 14}, {"X1-RANK06", 2, 12}, {"X1-RANK07", 2, 13},
		{"X1-RANK08", 3, 21}, {"X1-RANK09", 3, 24}, {"X1-RANK10", 3, 22}, {"X1-RANK11", 3, 23},
		// RANK12 ties RANK04 on marketplace count AND on richness, so the last slot is
		// decided by the symbol key and nothing else.
		{"X1-RANK12", 2, 11},
	}

	build := func(order []candidate) *cutoverWorld {
		world := surgeWorld(t, 0, 12) // more surplus than the cap, so the cap is what stops it
		for _, c := range order {
			world.gates.link(surgeHome, c.system)
			markets := make([]string, 0, c.markets)
			for m := 1; m <= c.markets; m++ {
				markets = append(markets, fmt.Sprintf("%s-M%d", c.system, m))
			}
			world.pool.pool = append(world.pool.pool, UnpricedSystem{
				System: c.system, MarketWaypoints: markets, Waypoints: c.richness,
			})
		}
		return world
	}

	// IN WRITE ORDER, not sorted. The surge takes surplus hulls in fleet order and each
	// takes the best target left, so the write sequence IS the ranking — and asserting
	// the sequence rather than the set is what makes the second and third keys
	// falsifiable at all.
	chosen := func(world *cutoverWorld) []string {
		var systems []string
		for _, row := range surgedRows(world) {
			systems = append(systems, row.System)
		}
		return systems
	}

	want := []string{
		// Three markets, richest first: 24, 23, 22, 21.
		"X1-RANK09", "X1-RANK11", "X1-RANK10", "X1-RANK08",
		// Then two markets, richest first: 14, 13, 12, 11 — and RANK04 takes the last slot
		// from RANK12, which ties it on both value keys, on the symbol tie-break.
		"X1-RANK05", "X1-RANK07", "X1-RANK06", "X1-RANK04",
	}

	first := build(candidates)
	surgeTick(t, first)
	require.Equal(t, want, chosen(first),
		"marketplace count dominates, richness breaks its ties, and the symbol makes the order TOTAL — "+
			"not one of the four single-market systems is taken, though they are the richest and were offered first")

	// THE SECOND TICK IS HANDED THE SAME POOL IN THE OPPOSITE ORDER, and that is what
	// makes the symbol tie-break falsifiable. Re-running an IDENTICAL slice proves
	// nothing about totality: sort.Slice is unstable but deterministic, so a comparator
	// with a tie in it returns the same answer twice on the same input. It is a change of
	// INPUT ORDER that a non-total comparator cannot survive — and the store's row order
	// is not part of its contract, so this is the real production case.
	reversed := make([]candidate, 0, len(candidates))
	for i := len(candidates) - 1; i >= 0; i-- {
		reversed = append(reversed, candidates[i])
	}
	second := build(reversed)
	surgeTick(t, second)
	require.Equal(t, want, chosen(second),
		"the same pool ranks identically however the store ordered it — RANK04 still takes the last slot from RANK12")
}

// AMONG CANDIDATES OF EQUAL VALUE THE NEARER ONE WINS, AND VALUE STILL OUTRANKS
// DISTANCE. These are the two halves of the ranking's second key, and neither is
// observable in a fixture where every candidate sits one gate away.
//
// The economics: a gate hop costs 850-1360s measured (sp-smbgd) — tens of minutes,
// once — while a probe delivered to a system with more markets keeps paying for the
// rest of the era. So distance breaks ties and never outranks marketplace count.
func TestSensingSurge_PrefersTheNearerSystemButValueOutranksDistance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		nearMarket int
		farMarkets int
		want       string
	}{
		// The two are IDENTICAL in value, and the far one sorts FIRST alphabetically — so
		// the symbol key would pick the wrong one and only the hop count picks the right one.
		{"equal value: the nearer system wins", 1, 1, "X1-NEAR"},
		// One extra market two gates out beats one market next door.
		{"unequal value: the richer system wins even two gates out", 1, 2, "X1-FAR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			world := surgeWorld(t, 0, 1) // exactly ONE surplus hull, so the choice is forced
			// home ── X1-NEAR ── X1-FAR: one hop and two hops, both inside MaxWalkRings.
			world.gates.link(surgeHome, "X1-NEAR")
			world.gates.link("X1-NEAR", "X1-FAR")
			// X1-FAR is offered FIRST and sorts FIRST, so neither the store order nor the
			// symbol key can be what produces the expected answer.
			world.pool.pool = []UnpricedSystem{
				{System: "X1-FAR", MarketWaypoints: marketWaypoints("X1-FAR", tc.farMarkets), Waypoints: 5},
				{System: "X1-NEAR", MarketWaypoints: marketWaypoints("X1-NEAR", tc.nearMarket), Waypoints: 5},
			}

			surgeTick(t, world)

			rows := surgedRows(world)
			require.Len(t, rows, 1, "one surplus hull, one dispatch")
			require.Equal(t, tc.want, rows[0].System)
		})
	}
}

// marketWaypoints names n marketplace waypoints in a system.
func marketWaypoints(system string, n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("%s-M%d", system, i))
	}
	return out
}

// AN UNREADABLE POOL DISPATCHES NOTHING. The pool read is era-scoped and refuses
// rather than guessing when the open era cannot be resolved (persistence.OpenEraScope),
// and the pass has to honour that refusal: read permissively, an unresolvable era
// yields the pre-backfill rows and this pass would fly probes at systems from
// universes that no longer exist — sp-l0aqy, ~290 API failures an hour for ten hours,
// against an 85% ceiling.
//
// The failure is COLLECTED, not swallowed: the tick reports it so a persistently
// unreadable pool escalates instead of looking like a fleet with nothing to price.
func TestSensingSurge_AnUnreadablePoolDispatchesNothingAndIsReported(t *testing.T) {
	world := surgeWorld(t, 20, 12)
	world.pool.err = errors.New("failed to resolve the open universe era")

	err := world.handler.ReconcileOnce(common.WithLogger(world.ctx, &capturingLogger{}), world.cmd)

	require.Error(t, err, "the tick reports the refusal rather than presenting as idle")
	require.Contains(t, err.Error(), "charted-but-unpriced sensing pool")
	require.Empty(t, surgedRows(world), "and not one hull was sent anywhere")
}

// A WAYPOINT THAT ALREADY CARRIES A PLACEMENT IS NEVER SURGED ONTO, which is what
// keeps this pass strictly complementary to the two beside it.
//
// A hull-less WANTED row in an unpriced system is the ORPHAN DISPATCH's to fill and
// the buy queue's to fund. Writing over it here would ALSO make the surge's single
// write unsafe: the conflict branch of that write re-points an existing row at the new
// hull, which is how an incumbent gets evicted.
func TestSensingSurge_LeavesAlreadyDeclaredPlacementsToTheOtherPasses(t *testing.T) {
	// Sixteen surplus hulls: four go to the declared rows through the orphan dispatch,
	// which leaves twelve — still more than the cap, so the surge is not merely running
	// out of hulls when it declines those four waypoints.
	world := surgeWorld(t, 20, 16)
	// The four highest-ranked pool systems already have a declared placement — the
	// shape the screen leaves behind when it judged a system but nothing filled it.
	declared := map[string]bool{}
	for i := 0; i < 4; i++ {
		waypoint := fmt.Sprintf("X1-POOL%02d-M1", i)
		world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}] = wantedAt(waypoint)
		declared[waypoint] = true
	}

	surgeTick(t, world)

	rows := surgedRows(world)
	for _, row := range rows {
		require.False(t, declared[row.Waypoint],
			"%s was already declared: filling it belongs to the orphan dispatch, not here", row.Waypoint)
	}
	// FOUR, NOT EIGHT, and the shortfall is the in-flight budget working as designed.
	// The orphan dispatch has just sent four hulls to those declared rows, and those
	// rows are in POOL systems — so they are four probes flying toward unpriced systems,
	// and inFlightSurge counts them against the same cap. Both paths' flights are
	// advanced out of one placement-machine budget and arrive to scan against one API
	// ceiling, so counting only our own would let the two saturate it between them.
	// It can only ever dispatch FEWER probes, which is the direction RULINGS #4 wants.
	require.Len(t, rows, defaultSurgeInFlightCap-len(declared),
		"the orphan dispatch's own four flights into unpriced systems consume half the in-flight budget")
}

// AN UNREACHABLE POOL SYSTEM IS NEVER DISPATCHED TO. Overshooting the walk is not a
// loud failure: the router would name no next system, the step would error, and the
// slot would sit IN_TRANSIT naming a hull that never arrives and never stops counting
// against the probe cap — a permanently stalled hull and a permanently held placement,
// silently.
//
// Here the whole pool sits beyond the gate graph (nothing is linked to home), so the
// candidates are richly ranked and completely unreachable.
func TestSensingSurge_RefusesAPoolSystemBeyondTheWalksReach(t *testing.T) {
	world := surgeWorld(t, 20, 12)
	world.gates.edges = map[string][]string{} // the graph is empty, not broken

	surgeTick(t, world)

	require.Empty(t, surgedRows(world),
		"a hull is never sent toward a system the walk cannot resolve")
}

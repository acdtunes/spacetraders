package commands

// THE ADOPTION RETRY.
//
// The cutover adopts orphaned scout probes exactly once, and it skips any hull it
// cannot place — a probe IN TRANSIT at that moment has no location, so it is
// passed over. Because `markCutoverDone` latches, that hull was then lost for
// good: no ledger row, so invisible to CountOwnedProbes, so the probe cap
// under-reads and authorises buying a replacement for a hull we already own
// (RULINGS #4). It also keeps its `scout` tag and is driven by nobody.
//
// The fix is not to make the cutover fail on it — that would hold the whole
// engine hostage to a probe happening to be in flight. It is to run adoption
// EVERY tick, idempotently, so the hull is picked up the moment it lands.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// flyingScoutProbe is a scout-tagged probe with NO current location — the shape a
// hull has while it is in transit, and precisely what the cutover skips.
func flyingScoutProbe(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(0, 0)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(testPlayerID), nil, fuel, 0, 0, cargo, 30,
		"FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusInTransit)
	require.NoError(t, err)
	ship.SetDedicatedFleet(freshnessScoutFleetTag)
	return ship
}

// land gives a flying hull a location, as the ships table would once it arrives.
func land(t *testing.T, world *cutoverWorld, symbol, waypoint string) {
	t.Helper()
	for i, ship := range world.fleet.ships {
		if ship.ShipSymbol() == symbol {
			world.fleet.ships[i] = scoutProbe(t, symbol, waypoint)
			return
		}
	}
	t.Fatalf("no ship %q in the fixture fleet", symbol)
}

func slotFor(world *cutoverWorld, hull string) (parkedsensing.QueuedSlot, bool) {
	for _, slot := range world.ledger.slots {
		if slot.AssignedShip == hull {
			return slot, true
		}
	}
	return parkedsensing.QueuedSlot{}, false
}

// --- the leak this closes -------------------------------------------------------

// A probe in transit at cutover is skipped there — correctly, since a hull we
// cannot place is a hull we must not record — and then ADOPTED on a later tick,
// once the ships table can say where it is. Before this pass that hull was lost
// permanently, because adoption only ever ran inside the one-shot cutover.
func TestAdoption_InTransitHullIsAdoptedOnceItLands(t *testing.T) {
	world := newCutoverWorld(t)
	world.fleet.ships = append(world.fleet.ships, flyingScoutProbe(t, "PROBE-FLYING"))

	// Tick 1: the cutover runs and adopts what it can. The flying hull has no
	// location, so neither the cutover nor the retry can place it.
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	_, recorded := slotFor(world, "PROBE-FLYING")
	require.False(t, recorded, "a hull with no location must not be recorded anywhere")
	require.ElementsMatch(t, []string{"PROBE-ORPHAN-1", "PROBE-ORPHAN-2"}, adoptedHulls(world),
		"the hulls that DID have a location were adopted by the cutover")

	// The probe arrives.
	land(t, world, "PROBE-FLYING", "X1-FAR3-A1")
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	slot, recorded := slotFor(world, "PROBE-FLYING")
	require.True(t, recorded, "the landed hull is adopted on the next tick — the cutover never runs again")
	require.Equal(t, "X1-FAR3-A1", slot.Waypoint)
	require.Equal(t, parkedsensing.SlotKindSpare, slot.Kind)
	require.Equal(t, parkedsensing.SlotStateParked, slot.State)
	require.Equal(t, parkedsensing.SensingParkedFleetTag, world.tagger.tagged["PROBE-FLYING"],
		"and it is claimed into the sensing fleet")
}

// adoptedHulls lists every hull the ledger holds a slot row for.
func adoptedHulls(world *cutoverWorld) []string {
	var out []string
	for _, slot := range world.ledger.slots {
		if slot.AssignedShip != "" {
			out = append(out, slot.AssignedShip)
		}
	}
	return out
}

// THE MONEY-GUARD PIN (RULINGS #4). The probe cap counts ledger rows, so the
// whole point of adopting is that the count RISES — a hull the cap cannot see is
// a hull it will authorise re-buying.
func TestAdoption_ProbeCountRisesByExactlyTheAdopted(t *testing.T) {
	world := newCutoverWorld(t)
	world.fleet.ships = append(world.fleet.ships, flyingScoutProbe(t, "PROBE-FLYING"))
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	before, err := world.ledger.CountOwnedProbes(world.ctx, testPlayerID)
	require.NoError(t, err)

	land(t, world, "PROBE-FLYING", "X1-FAR3-A1")
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	after, err := world.ledger.CountOwnedProbes(world.ctx, testPlayerID)
	require.NoError(t, err)
	require.Equal(t, before+1, after, "the cap sees exactly the one hull that was adopted")
}

// --- idempotence ----------------------------------------------------------------

// Running every tick only works if a settled fleet costs nothing. Both
// already-adopted shapes are covered: the normal one (tagged into the sensing
// fleet) and the safe half-done one (recorded but still carrying the old tag,
// which is what a failed tag write leaves behind).
func TestAdoption_AlreadyAdoptedHullCostsNoWrite(t *testing.T) {
	world := newCutoverWorld(t)
	world.fleet.ships = append(world.fleet.ships, flyingScoutProbe(t, "PROBE-FLYING"))
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	land(t, world, "PROBE-FLYING", "X1-FAR3-A1")
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	// PROBE-ORPHAN-1 is the recorded-but-untagged shape: it holds a slot row and
	// still carries the scout tag, exactly as a failed tag write would leave it.
	for i, ship := range world.fleet.ships {
		if ship.ShipSymbol() == "PROBE-ORPHAN-1" {
			world.fleet.ships[i].SetDedicatedFleet(freshnessScoutFleetTag)
		}
	}
	settled := len(world.ledger.upserted)

	for i := 0; i < 3; i++ {
		require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	}

	require.Equal(t, settled, len(world.ledger.upserted),
		"a settled fleet costs no ledger writes, however many ticks run")
}

// --- eligibility ----------------------------------------------------------------

// A hull still manning a surviving scout post belongs to that post. Adopting it
// would hand one hull to two owners, and the sensing engine would fly it off the
// post the scout coordinator is still reconciling.
func TestAdoption_NeverStealsAMannedHull(t *testing.T) {
	world := newCutoverWorld(t)
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	for i := 0; i < 3; i++ {
		require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	}

	_, recorded := slotFor(world, "PROBE-HOME")
	require.False(t, recorded, "the hull manning the surviving home post is never adopted")
	require.NotEqual(t, parkedsensing.SensingParkedFleetTag, world.tagger.tagged["PROBE-HOME"],
		"and it is never re-tagged out from under the scout coordinator")
}

// --- fail-closed ----------------------------------------------------------------

// Every input fails ADVERSARIALLY — the fakes hand back the value that would do
// the most damage if the error were swallowed. An unreadable POST list is the
// sharpest: read as empty it means "no hull is manned", which would adopt the
// probe standing on the surviving home post right out from under the scout
// coordinator.
func TestAdoption_FailsClosedOnUnreadableInputs(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(*cutoverWorld)
	}{
		{"fleet unreadable", func(w *cutoverWorld) { w.fleet.err = errors.New("ships table down") }},
		{"post list unreadable", func(w *cutoverWorld) { w.posts.listErr = errors.New("posts table down") }},
		{"ledger unreadable", func(w *cutoverWorld) { w.ledger.slotsErr = errors.New("ledger down") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := newCutoverWorld(t)
			world.fleet.ships = append(world.fleet.ships, flyingScoutProbe(t, "PROBE-FLYING"))
			require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
			land(t, world, "PROBE-FLYING", "X1-FAR3-A1")

			settled := len(world.ledger.upserted)
			tc.break_(world)
			_ = world.handler.ReconcileOnce(world.ctx, world.cmd)

			require.Equal(t, settled, len(world.ledger.upserted),
				"an unverifiable input adopts nothing at all")
			_, recorded := slotFor(world, "PROBE-FLYING")
			require.False(t, recorded)
		})
	}
}

// --- the per-tick bound ---------------------------------------------------------

// The bound counts WRITES, not candidates examined. That distinction is the whole
// guard: a fleet is mostly ineligible hulls (haulers, manned probes, already-
// adopted spares), and fleet order is stable — so a budget spent on rows that were
// never going to be written would let the ineligible majority starve the eligible
// few on EVERY tick, forever. The reaper learned the same lesson.
func TestAdoption_BoundsWritesNotCandidates(t *testing.T) {
	world := newCutoverWorld(t)
	// A long run of ineligible hulls FIRST: already carrying the sensing tag, so
	// they are skipped without a write and must cost no budget.
	for i := 0; i < DefaultMaxAdoptions*2; i++ {
		settledHull := scoutProbe(t, fmt.Sprintf("PROBE-SETTLED-%02d", i), "X1-FAR9-A1")
		settledHull.SetDedicatedFleet(parkedsensing.SensingParkedFleetTag)
		world.fleet.ships = append(world.fleet.ships, settledHull)
	}
	world.fleet.ships = append(world.fleet.ships, flyingScoutProbe(t, "PROBE-FLYING"))
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	land(t, world, "PROBE-FLYING", "X1-FAR3-A1")
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	_, recorded := slotFor(world, "PROBE-FLYING")
	require.True(t, recorded,
		"the one eligible hull is adopted despite %d ineligible ones ahead of it", DefaultMaxAdoptions*2)
}

// A backlog larger than the budget is worked over several ticks rather than fired
// in one burst — and nothing is lost, because the leftovers are still orphaned and
// still first in line next tick.
//
// Driven from a POST-cutover world on purpose. The cutover's own adoption is
// unbounded (it runs once, so a single burst is an accepted one-time cost), and
// letting it fire here would adopt the whole backlog before this pass ever saw
// it — the test would then be measuring the wrong code.
func TestAdoption_BacklogIsSpreadAcrossTicks(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil // nothing manned, so every probe below is an orphan
	world.fleet.ships = nil
	for i := 0; i < DefaultMaxAdoptions+3; i++ {
		world.fleet.ships = append(world.fleet.ships,
			scoutProbe(t, fmt.Sprintf("PROBE-ORPHAN-%02d", i), fmt.Sprintf("X1-FAR%02d-A1", i)))
	}

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Len(t, adoptedHulls(world), DefaultMaxAdoptions,
		"the first tick adopts exactly the budget")

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Len(t, adoptedHulls(world), DefaultMaxAdoptions+3,
		"the next tick picks up the remainder")
}

// --- write ordering -------------------------------------------------------------

// RECORD BEFORE TAG, pinned by making the RECORD fail and checking that no tag
// went out. The two orders are not symmetric and only one is recoverable:
//
//   - Recorded-but-untagged (the correct order's failure mode): the probe cap
//     counts the row, and the placement machine re-asserts the tag the first time
//     the spare is used. Safe in both directions.
//   - Tagged-but-unrecorded (the inverted order's failure mode): the sensing tag
//     makes the hull fail this pass's scout-tag filter on EVERY later tick, so it
//     never gets a row, stays invisible to CountOwnedProbes forever, and
//     authorises buying a replacement for a probe we own (RULINGS #4).
//
// Driven from a post-cutover world so the failing ledger write meets THIS pass
// rather than the cutover's own adoption.
func TestAdoption_RecordsTheHullBeforeTaggingIt(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.fleet.ships = []*navigation.Ship{scoutProbe(t, "PROBE-STRANDED", "X1-FAR9-A1")}
	world.ledger.upsertErr = errors.New("ledger refusing writes")

	_ = world.handler.ReconcileOnce(world.ctx, world.cmd)

	_, recorded := slotFor(world, "PROBE-STRANDED")
	require.False(t, recorded, "the row did not land")
	require.NotContains(t, world.tagger.tagged, "PROBE-STRANDED",
		"and the hull must NOT be tagged: tagged-but-unrecorded makes it skip this pass forever, invisible to the probe cap")
}

// --- the cutover-pending guard --------------------------------------------------

// BEFORE THE CUTOVER, A SCOUT-TAGGED HULL IS NOT AN ORPHAN. The scout posts still
// exist and the scout-post coordinator is actively manning them and drawing from
// its idle pool. A retry pass running then would take hulls out from under a LIVE
// coordinator — turning a bookkeeping fix into a fleet-management fight.
//
// The cutover is precisely the event that converts "scout-tagged hull" into
// "orphan we failed to adopt", so it is the cutover, not the phase, that gates
// this pass. While the cutover is still pending — including while it is retrying
// after a failure — adoption is its job, and this pass stays out of the way.
//
// The idle hull below is the sharp case: it mans no post, so the manned-hull
// filter does NOT protect it. Only the cutover-pending guard does.
func TestAdoption_NothingIsAdoptedWhileTheCutoverIsStillPending(t *testing.T) {
	world := newCutoverWorld(t)
	world.fleet.ships = append(world.fleet.ships, scoutProbe(t, "PROBE-IDLE", "X1-POOL-A1"))

	// The cutover refuses at its very first step, so it retires nothing, adopts
	// nothing, and never latches done — it stays pending on every tick.
	base := world.handler.newPorts
	world.handler.SetEnginePortsFactory(func(playerID int) SensingEnginePorts {
		ports := base(playerID)
		ports.Home = &fakeHome{err: errAmnesia}
		return ports
	})

	for i := 0; i < 3; i++ {
		require.Error(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	}

	require.Empty(t, adoptedHulls(world),
		"nothing is adopted while the cutover is pending — the scout world still owns these hulls")
	require.Empty(t, world.tagger.tagged,
		"and nothing is re-tagged out from under the scout-post coordinator")
	require.Empty(t, world.posts.removed, "the posts are all still standing")
}

// The counterpart: once the cutover COMPLETES, the same idle hull is exactly the
// orphan this pass exists for, and it is adopted on the next tick.
func TestAdoption_ResumesOnceTheCutoverCompletes(t *testing.T) {
	world := newCutoverWorld(t)
	world.fleet.ships = append(world.fleet.ships, scoutProbe(t, "PROBE-IDLE", "X1-POOL-A1"))
	home := &fakeHome{err: errAmnesia}
	base := world.handler.newPorts
	world.handler.SetEnginePortsFactory(func(playerID int) SensingEnginePorts {
		ports := base(playerID)
		ports.Home = home
		return ports
	})

	require.Error(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Empty(t, adoptedHulls(world))

	// The home read recovers, so the cutover completes on the next tick.
	home.err, home.system = nil, testHomeSystem
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.NotEmpty(t, world.posts.removed, "the cutover ran")

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	_, recorded := slotFor(world, "PROBE-IDLE")
	require.True(t, recorded, "with the cutover done, the idle scout hull is an orphan this pass adopts")
}

// THE GUARD IS THE CUTOVER'S TRIGGER, NOT ITS IN-MEMORY LATCH, and this is the
// case that forces the difference.
//
// cutoverDone is a per-container map on the handler, so a daemon restart clears
// it. After a restart the ledger is already populated, so the cutover's trigger
// (`len(systems) == 0 && !done`) never fires again and the latch is never re-set
// — it stays false for the life of the process. A guard written as
// `if !h.cutoverAlreadyDone(...)` would therefore switch this pass OFF
// permanently after the first restart, silently, exactly when a long-lived fleet
// needs it most.
//
// Gating on the trigger instead reads the DURABLE fact: a populated ledger means
// the cutover is not pending, whoever's process is running.
func TestAdoption_SurvivesADaemonRestart(t *testing.T) {
	// A fresh handler with a populated ledger IS the post-restart state: the
	// in-memory done-latch is false, and the cutover will never run again.
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.fleet.ships = []*navigation.Ship{scoutProbe(t, "PROBE-ORPHAN-X", "X1-FAR9-A1")}
	require.False(t, world.handler.cutoverAlreadyDone(world.cmd.ContainerID),
		"the in-memory latch is unset, as it is after every restart")

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	_, recorded := slotFor(world, "PROBE-ORPHAN-X")
	require.True(t, recorded,
		"adoption still runs after a restart — the populated ledger, not the lost latch, is what says the cutover is done")
}

// --- waypoint collisions --------------------------------------------------------
//
// THE SLOT TABLE IS KEYED ON THE WAYPOINT, NOT THE HULL, and UpsertSpareSlot's
// conflict set includes slot_kind, state AND assigned_ship. So a second write at
// a waypoint does not fail — it silently REPLACES which hull the row names. The
// hull-level `recorded` set cannot see that: it is keyed on ship symbol and read
// once, before the loop.
//
// Both tests below assert on `adopted_stranded` as well as the ledger, because
// the failure mode is one where every write succeeds and the heartbeat reports
// success while a hull is quietly lost.

// (a) TWO ORPHANS AT ONE WAYPOINT. Co-located scout probes are ordinary. Without
// a waypoint guard the second write replaces the first, leaving hull A tagged
// `sensing_parked` with NO row — which makes it fail this pass's own scout-tag
// filter forever, invisible to CountOwnedProbes, authorising a re-buy of a probe
// we own (RULINGS #4). That is the tagged-but-unrecorded state the write ordering
// exists to prevent, reached here with nothing failing at all.
func TestAdoption_TwoOrphansAtOneWaypoint_AdoptsExactlyOne(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.fleet.ships = []*navigation.Ship{
		scoutProbe(t, "PROBE-A", "X1-POOL-A1"),
		scoutProbe(t, "PROBE-B", "X1-POOL-A1"), // same waypoint
	}
	logger := &capturingLogger{}
	ctx := common.WithLogger(world.ctx, logger)

	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))

	_, aRecorded := slotFor(world, "PROBE-A")
	_, bRecorded := slotFor(world, "PROBE-B")
	require.NotEqual(t, aRecorded, bRecorded, "exactly one of the two co-located hulls is recorded")
	require.Len(t, adoptedHulls(world), 1, "one waypoint holds one row, naming one hull")

	// The hull that did NOT get the row must not be tagged either — tagged
	// without a row is the unrecoverable direction.
	loser := "PROBE-A"
	if aRecorded {
		loser = "PROBE-B"
	}
	require.NotContains(t, world.tagger.tagged, loser,
		"the hull that lost the waypoint is left untouched, so it is still an orphan this pass retries")

	require.Equal(t, 1, logger.payload("parked_sensing_cycle")["adopted_stranded"],
		"and the heartbeat reports one adoption, not two")
}

// (b) AN ORPHAN STANDING ON A LIVE PLACEMENT'S WAYPOINT. This one is new to this
// pass rather than inherited: the cutover's copy only ever runs against an EMPTY
// ledger, while this runs every tick against a populated one.
//
// Without the guard the write evicts the incumbent — the row stops naming the
// working probe (which drops out of the cap) and its slot_kind/state are
// rewritten to SPARE/PARKED, so a live MARKET placement silently leaves the scan
// rotation. Two failures for the price of one, both silent.
func TestAdoption_OrphanOnALivePlacement_LeavesTheIncumbentAlone(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-IN1-M1", parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M1", System: "X1-IN1", Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateParked, AssignedShip: "PROBE-INCUMBENT",
	}
	// The orphan happens to be standing on the incumbent's waypoint.
	world.fleet.ships = []*navigation.Ship{scoutProbe(t, "PROBE-ORPHAN", "X1-IN1-M1")}
	logger := &capturingLogger{}
	ctx := common.WithLogger(world.ctx, logger)

	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))

	incumbent := world.ledger.slots[psSlotKey{"X1-IN1-M1", parkedsensing.SlotKindMarket}]
	require.Equal(t, "PROBE-INCUMBENT", incumbent.AssignedShip,
		"the live placement still names its own hull — evicting it drops a paid-for probe out of the cap")
	require.Equal(t, parkedsensing.SlotKindMarket, incumbent.Kind,
		"and it is still a MARKET slot, not rewritten to SPARE")
	require.Equal(t, parkedsensing.SlotStateParked, incumbent.State)

	_, recorded := slotFor(world, "PROBE-ORPHAN")
	require.False(t, recorded,
		"the orphan is skipped rather than evicting the incumbent; it stays untagged and recoverable, and uncounted meanwhile")
	require.NotContains(t, world.tagger.tagged, "PROBE-ORPHAN")
	require.Equal(t, 0, logger.payload("parked_sensing_cycle")["adopted_stranded"])
}

// A ROW WITHOUT A HULL IS STILL A ROW. UpsertSpareSlot's conflict set rewrites
// slot_kind and state REGARDLESS of whether the row names a ship, so an
// occupancy index built from hull-bearing rows only leaves a second door open:
// an orphan standing on an unfilled placement converts that row to SPARE/PARKED
// and names itself, and a live placement leaves the scan rotation exactly as in
// the incumbent case. Orphan scouts park at markets, so this is reachable.
//
// Two shapes, both hull-less: a screen-declared WANTED placement, and a QUEUED
// claim (whose later TransitionSlot(QUEUED→BOUGHT) would then fail with
// ErrSlotClaimed, having lost its claim to a write that was never about it).
// sp-0eufi NARROWED THIS. The WANTED case moved to
// TestAdoption_FillsAHullLessWantedPlacementTheOrphanIsStandingOn: a hull standing on a placement
// that is asking for a hull is now used to FILL it rather than skipped, which is what took the pass
// from absorbing one live orphan to absorbing the ones that were stranding 245k of paid-for hulls.
//
// What this test was protecting is NOT weakened, and the distinction is exact: the danger was
// UpsertSpareSlot's conflict set rewriting a MARKET placement's slot_kind to SPARE and dropping it
// out of the scan rotation. The fill path never touches kind and goes through the guarded
// WANTED->PARKED transition instead, so that assertion still holds — it just holds in the new test.
//
// QUEUED remains here, unchanged and still a hard skip: the drain has claimed that row for purchase
// and its TransitionSlot(QUEUED->BOUGHT) is guarded on the state, so money is already riding on it.
func TestAdoption_OrphanOnAQueuedPlacement_LeavesTheRowAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
	}{
		{"a placement already claimed for purchase", parkedsensing.SlotStateQueued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
			world.posts.posts = nil
			world.ledger.slots[psSlotKey{"X1-IN1-M1", parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
				Waypoint: "X1-IN1-M1", System: "X1-IN1", Kind: parkedsensing.SlotKindMarket,
				State: tc.state, // no AssignedShip — the row names no hull
			}
			world.fleet.ships = []*navigation.Ship{scoutProbe(t, "PROBE-ORPHAN", "X1-IN1-M1")}
			logger := &capturingLogger{}
			ctx := common.WithLogger(world.ctx, logger)

			require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))

			row := world.ledger.slots[psSlotKey{"X1-IN1-M1", parkedsensing.SlotKindMarket}]
			require.Equal(t, parkedsensing.SlotKindMarket, row.Kind,
				"the placement is still a MARKET slot — rewriting it to SPARE drops it out of the scan rotation")
			require.Equal(t, tc.state, row.State, "and still in its own state")
			require.Empty(t, row.AssignedShip, "and still names no hull")

			require.NotContains(t, world.tagger.tagged, "PROBE-ORPHAN")
			require.Equal(t, 0, logger.payload("parked_sensing_cycle")["adopted_stranded"])
		})
	}
}

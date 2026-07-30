package commands

// run_probe_sensing_cutover_test.go pins the one-time migration from the touring
// sensing model to the parked one.
//
// The cutover happens exactly once, on the first reconcile that finds an empty
// ledger, and it is not undoable: it deletes scout posts. So the properties
// asserted here are the ones that make it safe to run unattended — it keeps the
// home post, it accounts for every hull it orphans, and it rediscovers nothing
// through the API that the database already knows.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	testHomeSystem = "X1-HOME"
	testPlayerID   = 7
)

// cutoverWorld is the old world as the first parked-probe tick finds it.
//
// Every fake is held here and handed to the ports factory BY REFERENCE, so the
// instance a test reaches for is the same one the handler drives. A factory that
// allocated fresh fakes per call would let a test set up a fake the coordinator
// never sees and then assert against it — passing or failing for reasons that
// have nothing to do with the code.
type cutoverWorld struct {
	handler *RunProbeSensingCoordinatorHandler
	cmd     *RunProbeSensingCoordinatorCommand
	// ctx is cancelled by t.Cleanup. Every test drives the coordinator through
	// it, because a reconcile now STARTS the scan pacer — a Background context
	// would leak that goroutine for the life of the test binary and let it call
	// the fakes concurrently with the assertions.
	ctx      context.Context
	calls    *callCounter
	ledger   *psLedger
	posts    *fakePostRepo
	fleet    *fakeFleet
	tagger   *fakeFleetTagger
	depth    *fakeDepthReader
	catalog  *fakeCatalog
	goods    *fakeMarketGoods
	remote   *fakeRemoteMarket
	seeds    *fakeSeedCommander
	mover    *fakeMover
	recorder *fakeRecorder
	// gates is the stored gate adjacency. It starts EMPTY, which is the
	// fail-closed reading — a map with no known edges reaches nowhere — so every
	// fixture that does not wire topology gets same-system behaviour, and a test
	// that means to exercise a crossing has to say so.
	gates *fakeGates
	// yards is the shipyard blind spot as the store holds it: which yards we have
	// never asked about, and which ones a tick actually read.
	yards *fakeYardCatalog
	// ports is the COMPLETE engine surface this world wires, held so a test can
	// remove one port from a copy and re-wire the handler with it.
	ports SensingEnginePorts
	// purchaser is the probe-buy port, exposed so a test can assert WHAT the
	// coordinator hands it — specifically the claim owner, which must be this
	// tick's real container id (see the ships.container_id foreign key).
	purchaser *psPurchaser
	// shipPos is the ships-TABLE read the placement machine locates hulls
	// through, which is a different port from `fleet` (the whole-fleet
	// enumeration adoption and the orphan dispatch use). A hull present in one
	// and absent from the other is a legitimate state, so any test asserting a
	// hull was actually FLOWN has to populate this one too — the placement
	// machine refuses to command a hull it cannot locate.
	shipPos *fakeShipPositions
}

// newCutoverWorld builds the old-world fixture the brief describes: three scout
// posts (home plus two the touring model declared), two orphaned scout probes,
// and market rows for four systems, one of which deals only in ore.
func newCutoverWorld(t *testing.T) *cutoverWorld {
	t.Helper()
	calls := newCallCounter()

	// Four systems with market rows. The three sensing systems each hold a market
	// dealing in a whitelisted good; the ore system holds one that does not.
	catalog := newFakeCatalog()
	goods := newFakeMarketGoods()
	var rows []domainScouting.MarketDepthRow
	inScope := map[string][]string{
		"X1-AA1": {"FOOD"},
		"X1-BB2": {"ELECTRONICS", "MACHINERY"},
		"X1-CC3": {"CLOTHING"},
	}
	for system, list := range inScope {
		waypoint := system + "-M1"
		catalog.markets[system] = []string{waypoint}
		catalog.known[system] = true
		goods.goods[waypoint] = list
		for _, good := range list {
			rows = append(rows, domainScouting.MarketDepthRow{
				System: system, Waypoint: waypoint, Good: good, TradeVolume: 40, MidPrice: 900,
			})
		}
	}
	// The all-ore system: fully charted, every market resolved, nothing wanted.
	catalog.markets["X1-ORE"] = []string{"X1-ORE-M1"}
	catalog.known["X1-ORE"] = true
	goods.goods["X1-ORE-M1"] = []string{"IRON_ORE", "COPPER_ORE"}
	rows = append(rows, domainScouting.MarketDepthRow{
		System: "X1-ORE", Waypoint: "X1-ORE-M1", Good: "IRON_ORE", TradeVolume: 10, MidPrice: 50,
	})

	posts := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: testPlayerID, SystemSymbol: testHomeSystem, Kind: domainScouting.PostKindStanding, AssignedHull: "PROBE-HOME"},
		{PlayerID: testPlayerID, SystemSymbol: "X1-AA1", Kind: domainScouting.PostKindStanding, AssignedHull: "PROBE-ORPHAN-1"},
		{PlayerID: testPlayerID, SystemSymbol: "X1-BB2", Kind: domainScouting.PostKindStanding, AssignedHull: "PROBE-ORPHAN-2"},
	}}

	fleet := &fakeFleet{ships: []*navigation.Ship{
		scoutProbe(t, "PROBE-HOME", testHomeSystem+"-A1"),
		// The orphans sit in systems with no market rows, so nothing in this tick
		// competes for them — the cutover's own accounting is what is under test.
		scoutProbe(t, "PROBE-ORPHAN-1", "X1-FAR1-A1"),
		scoutProbe(t, "PROBE-ORPHAN-2", "X1-FAR2-A1"),
	}}

	depth := &fakeDepthReader{rows: rows}
	remote := &fakeRemoteMarket{calls: calls, goods: map[string][]string{}}
	tagger := newFakeFleetTagger()
	ledger := newPSLedger()
	recorder := newFakeRecorder()
	seeds := &fakeSeedCommander{calls: calls, hasMkt: map[string]bool{}}
	mover := &fakeMover{calls: calls}
	treasury := &psTreasury{calls: calls, credits: 5_000_000}
	purchaser := &psPurchaser{calls: calls, price: 40_000}
	ships := &fakeShipPositions{at: map[string]parkedsensing.ShipPos{}, docked: map[string]string{}}
	budget := &fakeBudget{ceiling: 2.0}
	gates := &fakeGates{edges: map[string][]string{}}
	// The shipyard reads. Empty outstanding set by default: a fixture that means to
	// exercise the catalogue sweep says so, and every other test sees a pass that
	// enumerates nothing and reads nothing — which is also the steady state in
	// production once the blind spot has drained.
	yards := &fakeYardCatalog{calls: calls}

	handler := NewRunProbeSensingCoordinatorHandler(
		depth, posts, fleet, &fakePressure{}, &fakePhase{inExpansion: true}, &shared.MockClock{CurrentTime: time.Now()},
	)
	handler.SetMetricsRecorder(recorder)
	// Held as a value rather than built inside the closure so a test can take the
	// COMPLETE surface, remove exactly one port and re-wire it — which is the only
	// way to prove a specific port is genuinely required rather than merely present.
	enginePorts := SensingEnginePorts{
		Ledger:       ledger,
		Waypoints:    catalog,
		Uncharted:    catalog,
		MarketGoods:  goods,
		SpreadOf:     goods,
		RemoteMarket: remote,
		Treasury:     treasury,
		CargoSpend:   &fakeCargoSpend{},
		Purchaser:    purchaser,
		Ships:        ships,
		Fleet:        tagger,
		Mover:        mover,
		Gates:        gates,
		SeedShip:     seeds,
		Scan:         &fakeScanRunner{calls: calls},
		YardCatalog:  yards,
		YardRead:     yards,
		YardScan:     yards,
		Home:         &fakeHome{system: testHomeSystem},
		Budget:       budget,
	}
	handler.SetEnginePortsFactory(func(int) SensingEnginePorts { return enginePorts })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return &cutoverWorld{
		handler: handler, cmd: sensingTestCmd(), ctx: ctx, calls: calls, ledger: ledger,
		posts: posts, fleet: fleet, tagger: tagger, depth: depth,
		catalog: catalog, goods: goods, remote: remote, seeds: seeds,
		mover: mover, recorder: recorder, shipPos: ships, purchaser: purchaser, gates: gates,
		yards: yards, ports: enginePorts,
	}
}

func sensingTestCmd() *RunProbeSensingCoordinatorCommand {
	return &RunProbeSensingCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(testPlayerID),
		ContainerID: "probe_sensing_coordinator-player-7",
	}
}

// scoutProbe is a SATELLITE hull dedicated to the touring model's "scout" fleet —
// exactly the shape the cutover has to recognise and adopt.
func scoutProbe(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	location.SystemSymbol = shared.ExtractSystemSymbol(waypoint)
	fuel, err := shared.NewFuel(0, 0)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(testPlayerID), location, fuel, 0, 0, cargo, 30,
		"FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusDocked)
	require.NoError(t, err)
	ship.SetDedicatedFleet(freshnessScoutFleetTag)
	return ship
}

// The whole cutover, in one tick.
func TestCutover_RetiresTheTouringModelInOneTick(t *testing.T) {
	world := newCutoverWorld(t)

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	// The two touring posts are retired; HOME is kept, because the scout-post
	// coordinator still mans it and bootstrap floor-protects it.
	require.ElementsMatch(t, []string{"X1-AA1", "X1-BB2"}, world.posts.removed,
		"every scout post except home is retired")
	require.NotContains(t, world.posts.removed, testHomeSystem, "the home post survives the cutover")

	// Every system the market cache already knew about now carries a verdict.
	verdicts := map[string]string{}
	for _, system := range world.ledger.systems {
		verdicts[system.System] = system.Verdict
	}
	require.Equal(t, map[string]string{
		"X1-AA1": parkedsensing.VerdictInScope,
		"X1-BB2": parkedsensing.VerdictInScope,
		"X1-CC3": parkedsensing.VerdictInScope,
		"X1-ORE": parkedsensing.VerdictNoWhitelist,
	}, verdicts, "three systems deal in whitelisted goods; the ore system is screened and rejected")

	// The in-scope systems got placements; the rejected one did not.
	wanted := map[string]bool{}
	for _, slot := range world.ledger.slots {
		if slot.Kind == parkedsensing.SlotKindMarket {
			wanted[slot.Waypoint] = true
		}
	}
	require.Equal(t, map[string]bool{"X1-AA1-M1": true, "X1-BB2-M1": true, "X1-CC3-M1": true}, wanted,
		"a MARKET placement per in-scope market, and none in the rejected system")

	// The orphaned hulls are accounted for: re-tagged, and recorded as parked
	// spares where they stand. An unrecorded probe is invisible to the probe cap,
	// which would authorise buying a replacement for a hull we already own.
	require.Equal(t, parkedsensing.SensingParkedFleetTag, world.tagger.tagged["PROBE-ORPHAN-1"])
	require.Equal(t, parkedsensing.SensingParkedFleetTag, world.tagger.tagged["PROBE-ORPHAN-2"])
	require.NotContains(t, world.tagger.tagged, "PROBE-HOME",
		"the hull still manning the surviving home post is left alone")

	spares := map[string]string{}
	for _, slot := range world.ledger.slots {
		if slot.Kind == parkedsensing.SlotKindSpare {
			spares[slot.Waypoint] = slot.AssignedShip
			require.Equal(t, parkedsensing.SlotStateParked, slot.State,
				"an adopted hull is already on station, so its slot is PARKED not WANTED")
		}
	}
	require.Equal(t, map[string]string{
		"X1-FAR1-A1": "PROBE-ORPHAN-1",
		"X1-FAR2-A1": "PROBE-ORPHAN-2",
	}, spares, "each orphan is adopted as a spare at the waypoint it already occupies")
}

// The cutover screens from the database and NOTHING else. The map is already in
// the local cache; paying the API to rediscover it would cost a full sweep's
// worth of calls for information we already hold — and would do it on the very
// first tick after a deploy.
func TestCutover_MakesNoRemoteCall(t *testing.T) {
	world := newCutoverWorld(t)

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Zero(t, world.calls.count("remote_market_fetch"),
		"the offline screen must never reach the API for a market the cache can answer for")
	require.Zero(t, world.calls.count("sync_waypoints"), "no catalog sweep: every system is already known")
	require.Zero(t, world.calls.count("chart")+world.calls.count("jump")+world.calls.count("seed_navigate"),
		"no charting seed is commanded during the cutover")
	require.Zero(t, world.calls.count("scan"), "the pacer is not running; the reconcile issues no scans itself")
	require.Zero(t, world.calls.count("navigate")+world.calls.count("route")+world.calls.count("dock"),
		"no hull is moved: every adopted probe is recorded where it already stands")
	require.Zero(t, world.calls.count("quote")+world.calls.count("buy"),
		"nothing is bought — no system in the fixture has a probe yard to buy through")

	// The ONE network read this tick legitimately makes is the buy queue's own
	// treasury check, which runs on every tick regardless of the cutover and is
	// the gate in front of spending rather than a cost of migrating. Asserting it
	// exactly keeps the negative assertions above honest: if a future change adds
	// another network read here, this line fails rather than the total silently
	// drifting upward.
	require.Equal(t, 1, world.calls.total(),
		"the treasury check is the only network read: got %v", world.calls.names())
	require.Equal(t, 1, world.calls.count("treasury"))
}

// The cutover runs ONCE. A ledger that legitimately reads empty later — a fresh
// era, a wiped table — must not re-run a scout-post retirement it has no rows to
// justify.
func TestCutover_RunsOnlyOnce(t *testing.T) {
	world := newCutoverWorld(t)
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	removedFirst := len(world.posts.removed)
	require.Equal(t, 2, removedFirst)

	// Wipe the ledger, which is what a fresh era looks like from here.
	world.ledger.systems = map[string]parkedsensing.ExpandSystem{}
	world.ledger.slots = map[psSlotKey]parkedsensing.QueuedSlot{}
	censusReadsBefore := world.depth.calls

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Len(t, world.posts.removed, removedFirst, "no second retirement sweep")
	require.Equal(t, censusReadsBefore, world.depth.calls, "the cutover census is not re-read")
}

// Without the home system every post looks retirable — including the one the
// scout-post coordinator is actively manning. An unreadable home system must
// refuse the whole cutover rather than proceed on a guess: the deletion is not
// undoable, and the next tick can simply retry.
func TestCutover_UnknownHomeSystem_RetiresNothing(t *testing.T) {
	world := newCutoverWorld(t)
	// Wrap the fixture's factory rather than replacing it, so every other port
	// stays exactly as the passing tests wire it and the home read is the single
	// difference under test.
	base := world.handler.newPorts
	world.handler.SetEnginePortsFactory(func(playerID int) SensingEnginePorts {
		ports := base(playerID)
		ports.Home = &fakeHome{err: errAmnesia}
		return ports
	})

	err := world.handler.ReconcileOnce(world.ctx, world.cmd)

	require.Error(t, err, "an unreadable home system is surfaced, never swallowed")
	require.Empty(t, world.posts.removed, "not one post is retired without knowing which to keep")
	require.Empty(t, world.ledger.systems, "and nothing is screened either — the cutover is refused whole")
}

var errAmnesia = errTest("player row unreadable")

type errTest string

func (e errTest) Error() string { return string(e) }

// THE RETRY PROPERTY. A cutover that fails partway must be re-run in full by the
// next tick, and must not double-apply anything it already committed.
//
// This is the ordering bug the screen-last sequence exists to prevent. Screening
// WRITES sensing_systems (every verdict, PENDING included), and a non-empty
// sensing_systems is exactly what stops the cutover re-firing — so a screen that
// ran before the hulls were accounted for would disarm the retry while scout
// probes still belonged to no post and no slot. Those hulls are invisible to the
// probe cap, so the engine would buy replacements for probes it already owns,
// permanently and with no path back.
//
// The injected failure is an unreadable posts table on the first tick, which is
// the earliest irreversible step and therefore the strictest case: NOTHING may
// have been committed, and everything must still happen exactly once.
func TestCutover_FailedFirstTick_IsFullyRetriedByTheNext(t *testing.T) {
	world := newCutoverWorld(t)
	world.posts.listErr = errAmnesia

	err := world.handler.ReconcileOnce(world.ctx, world.cmd)

	require.Error(t, err, "the failure is surfaced")
	require.Empty(t, world.posts.removed, "no post was retired")
	require.Empty(t, world.ledger.slots, "no hull was adopted")
	require.Empty(t, world.ledger.systems,
		"and CRUCIALLY nothing was screened — an empty sensing ledger is what re-arms the retry")

	// The posts table recovers; the next tick must do the whole job.
	world.posts.listErr = nil
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.ElementsMatch(t, []string{"X1-AA1", "X1-BB2"}, world.posts.removed,
		"the retry retires the posts the failed tick never reached")
	require.Equal(t, parkedsensing.SensingParkedFleetTag, world.tagger.tagged["PROBE-ORPHAN-1"])
	require.Equal(t, parkedsensing.SensingParkedFleetTag, world.tagger.tagged["PROBE-ORPHAN-2"])
	require.Len(t, world.ledger.systems, 4, "and screens every known system")

	// Exactly once each: a third tick is a no-op, because the ledger the retry
	// filled is now the thing that says the cutover is done.
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Len(t, world.posts.removed, 2, "no post is retired twice")

	spares := 0
	for _, slot := range world.ledger.slots {
		if slot.Kind == parkedsensing.SlotKindSpare {
			spares++
		}
	}
	require.Equal(t, 2, spares, "each orphan is adopted exactly once")
}

// The two steps that now run before the screen must be safe to REPEAT, or
// "retried in full" would mean "applied twice". Driven here by failing the
// adoption's ledger write on the first tick, which leaves the posts already
// retired and the hulls not yet recorded — the exact half-done state a retry
// has to land on.
func TestCutover_RetryIsIdempotentOverAPartialFirstTick(t *testing.T) {
	world := newCutoverWorld(t)
	world.ledger.upsertErr = errAmnesia

	require.Error(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.ElementsMatch(t, []string{"X1-AA1", "X1-BB2"}, world.posts.removed,
		"the posts were retired before the adoption failed")
	require.Empty(t, world.ledger.systems, "the screen was held back, so the retry is still armed")

	world.ledger.upsertErr = nil
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	// Re-listing after a partial removal returns only what survived, and removing
	// an absent post is not an error — so the repeat costs nothing and adds no
	// duplicate.
	require.Len(t, world.posts.removed, 2, "already-retired posts are not retired again")
	require.Equal(t, parkedsensing.SensingParkedFleetTag, world.tagger.tagged["PROBE-ORPHAN-1"])
	require.Equal(t, parkedsensing.SensingParkedFleetTag, world.tagger.tagged["PROBE-ORPHAN-2"])
	require.Len(t, world.ledger.systems, 4)
}

// A hull is RECORDED before it is TAGGED, for the reason recordPurchase gives in
// the buy queue: the two writes are ordered by what a failure between them costs.
//
// Tagged-but-unrecorded is the unrecoverable direction — the sensing tag makes
// the hull skip the adoption filter on every subsequent retry, so it would stay
// invisible to the probe cap forever and authorise buying a replacement for a
// probe we already own. Recorded-but-untagged is merely untidy: the cap counts
// it, and the placement machine re-asserts the tag the first time the spare is
// used.
//
// So a failed tag is a WARNING, not a failed cutover. Escalating it would re-run
// the whole cutover on every tick for as long as the tag write kept failing,
// over work that is already correctly recorded.
func TestCutover_AdoptionRecordsTheHullBeforeTaggingIt(t *testing.T) {
	world := newCutoverWorld(t)
	world.tagger.err = errAmnesia
	logger := &capturingLogger{}
	ctx := common.WithLogger(world.ctx, logger)

	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd),
		"a tag that will not stick does not fail the cutover")

	spares := map[string]string{}
	for _, slot := range world.ledger.slots {
		if slot.Kind == parkedsensing.SlotKindSpare {
			spares[slot.Waypoint] = slot.AssignedShip
		}
	}
	require.Equal(t, map[string]string{
		"X1-FAR1-A1": "PROBE-ORPHAN-1",
		"X1-FAR2-A1": "PROBE-ORPHAN-2",
	}, spares, "both hulls are recorded even though the tag write failed — the cap can see them")
	require.Empty(t, world.tagger.tagged, "and neither carries the sensing tag yet")
	require.True(t, logger.sawAction("parked_sensing_adopt_tag_failed"),
		"the untagged hull is named, so it is visible rather than merely tolerated")

	// The cutover COMPLETED: it screened, and it does not run again.
	require.Len(t, world.ledger.systems, 4)
	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))
	require.Len(t, world.posts.removed, 2, "no repeat retirement — the cutover is done, not stuck")
}

// A failure in the SCREEN stage must not latch the done-mark either.
//
// The mark is in memory, so latching it over an empty ledger strands the engine
// until a daemon restart: the cutover will not re-fire, and the steady-state
// sweep re-screens only systems that already carry a PENDING row — which a
// failed census never wrote. This is the last hole in the failure matrix, and
// it is the one that leaves the fleet quietly doing nothing.
func TestCutover_ScreenStageFailure_DoesNotLatchDone(t *testing.T) {
	world := newCutoverWorld(t)
	world.depth.err = errAmnesia

	require.Error(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Empty(t, world.ledger.systems, "nothing was screened")

	// The irreversible half DID run and stands — that is what makes the retry
	// safe rather than merely possible.
	require.ElementsMatch(t, []string{"X1-AA1", "X1-BB2"}, world.posts.removed)

	world.depth.err = nil
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Len(t, world.ledger.systems, 4, "the retry completes the screen the failed tick could not")
	require.Len(t, world.posts.removed, 2, "and repeats nothing")
}

// An EMPTY census is not a failure. A fleet that has scanned nothing yet screens
// zero systems and is still done cutting over — the expansion frontier is what
// grows the map from there, and re-running the retirement sweep every tick
// waiting for market rows would be wrong.
func TestCutover_EmptyCensusStillCompletes(t *testing.T) {
	world := newCutoverWorld(t)
	world.depth.rows = nil

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.ElementsMatch(t, []string{"X1-AA1", "X1-BB2"}, world.posts.removed)
	require.Empty(t, world.ledger.systems, "nothing to screen")

	// Done: a second tick does not retire anything again.
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Len(t, world.posts.removed, 2)
	require.Equal(t, 1, world.depth.calls, "the cutover census is read exactly once")
}

// --- partial-screen recovery (sp-x665h) ------------------------------------------

// errUnreadable is a genuine fault, not the offline screen refusing a fetch. The
// offline path yields PENDING VERDICTS rather than errors, so only a real read
// failure produces the shape this section is about: a system that leaves no row.
var errUnreadable = errors.New("market waypoint list unreadable")

// failOneScreen makes exactly one system's screen error while its siblings
// succeed, and returns the world.
func failOneScreen(t *testing.T, system string) *cutoverWorld {
	t.Helper()
	world := newCutoverWorld(t)
	world.catalog.marketsErr = map[string]error{system: errUnreadable}
	return world
}

// A system whose cutover screen FAILS must still leave a row, or it becomes
// invisible to everything.
//
// ScreenSystem writes its sensing_systems row last and only on full success, so
// a mid-census failure leaves that system with no row at all while its siblings
// have theirs. That is not merely untidy: the cutover's re-fire trigger is an
// EMPTY ledger, and the siblings' rows disarm it, while the steady-state sweep
// re-screens only systems that ALREADY carry a PENDING row. The system falls
// through both, and is re-screened only if frontier propagation happens to reach
// it — which depends on the map's topology rather than on anything we control.
func TestCutover_ScreenError_LeavesAPendingRowForTheSweep(t *testing.T) {
	world := failOneScreen(t, "X1-CC3")

	require.Error(t, world.handler.ReconcileOnce(world.ctx, world.cmd),
		"the screen failure is still surfaced")

	require.Equal(t, parkedsensing.VerdictPending, world.ledger.systems["X1-CC3"].Verdict,
		"the system we could not read is recorded as awaiting screening")
	require.Equal(t, parkedsensing.VerdictInScope, world.ledger.systems["X1-AA1"].Verdict,
		"its siblings keep the real verdicts they earned")
	require.Equal(t, parkedsensing.VerdictNoWhitelist, world.ledger.systems["X1-ORE"].Verdict)

	// The done-mark must NOT latch on a failed screen. This is load-bearing and
	// independently pinned by TestCutover_ScreenStageFailure_DoesNotLatchDone;
	// asserted here too because the fallback write is exactly the kind of change
	// that could accidentally turn the failure into a success.
	require.False(t, world.handler.cutoverAlreadyDone(world.cmd.ContainerID),
		"a screen that failed does not latch the cutover done")
}

// The bare row asserts NOTHING it does not know. A system we failed to read is a
// system we know nothing about, so the write carries the symbol and the PENDING
// verdict and no third thing: no depth, no uncharted count, no catalog claim.
//
// The write set is asserted directly rather than through its effect, mirroring
// the Task 9 column-ownership pins — and it goes through UpsertSystem, whose
// column list structurally excludes seed_ship and seed_state, so a charting
// errand can never be cleared by this path.
func TestCutover_ScreenError_PendingRowAssertsNothingFalse(t *testing.T) {
	world := failOneScreen(t, "X1-CC3")

	require.Error(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	var fallback []parkedsensing.SystemRecord
	for _, rec := range world.ledger.systemUpserts {
		if rec.System == "X1-CC3" {
			fallback = append(fallback, rec)
		}
	}
	require.Len(t, fallback, 1, "exactly one row is written for the failed system")
	require.Equal(t, parkedsensing.SystemRecord{
		System:  "X1-CC3",
		Verdict: parkedsensing.VerdictPending,
	}, fallback[0], "the record carries the symbol and the verdict and nothing else")

	row := world.ledger.systems["X1-CC3"]
	require.Zero(t, row.UnchartedCount, "no measurement is invented")
	require.False(t, row.CatalogKnown, "and no claim is made about a catalog we never read")
	require.Empty(t, row.SeedShip, "no charting errand is asserted")
	require.Empty(t, row.SeedState)
}

// The row is not bookkeeping for its own sake — it is what puts the system back
// in front of the steady-state sweep, which re-screens PENDING rows with the
// REAL remote fetcher wired (the cutover's is deliberately offline). This is the
// recovery that did not exist before, and the reason the gate comment can now
// make the stronger claim.
func TestCutover_ScreenError_NextSweepAdoptsTheSystem(t *testing.T) {
	world := failOneScreen(t, "X1-CC3")

	require.Error(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Equal(t, parkedsensing.VerdictPending, world.ledger.systems["X1-CC3"].Verdict)

	// The fault clears. No cutover re-fires — the ledger is no longer empty — so
	// the recovery here is the sweep's alone.
	world.catalog.marketsErr = nil
	censusReads := world.depth.calls
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Equal(t, parkedsensing.VerdictInScope, world.ledger.systems["X1-CC3"].Verdict,
		"the sweep picked the system up off its PENDING row and screened it properly")
	require.Equal(t, censusReads, world.depth.calls,
		"and did it without re-running the cutover census")
}

// If the fallback write ALSO fails there is nothing more to be done, but it must
// be said out loud and it must not take the rest of the census down with it: the
// systems after this one in the loop are still perfectly screenable.
func TestCutover_FallbackWriteAlsoFails_BothCollectedAndLoopContinues(t *testing.T) {
	world := failOneScreen(t, "X1-AA1") // sorts first, so siblings follow it
	world.ledger.systemUpsertErr = map[string]error{"X1-AA1": errors.New("ledger refusing writes")}

	err := world.handler.ReconcileOnce(world.ctx, world.cmd)

	require.Error(t, err)
	require.Contains(t, err.Error(), errUnreadable.Error(), "the screen failure is reported")
	require.Contains(t, err.Error(), "ledger refusing writes", "and so is the failed fallback — neither masks the other")

	require.NotContains(t, world.ledger.systems, "X1-AA1", "nothing could be recorded for it")
	require.Equal(t, parkedsensing.VerdictInScope, world.ledger.systems["X1-BB2"].Verdict,
		"the systems after the failure are still screened")
	require.Equal(t, parkedsensing.VerdictInScope, world.ledger.systems["X1-CC3"].Verdict)
	require.False(t, world.handler.cutoverAlreadyDone(world.cmd.ContainerID))
}

// THE ALL-FAIL CASE CHANGES SHAPE, and the change is the point. Before the
// fallback row existed, a census where EVERY screen failed left an empty ledger,
// so the cutover re-fired next tick and re-ran the whole offline census. Now the
// failures leave PENDING rows, the ledger is not empty, and the cutover does NOT
// re-fire — the sweep recovers the systems instead, with the real remote fetcher
// wired.
//
// That is strictly better (the sweep can resolve markets the offline screen
// cannot) but it is a genuine behavioural change, so it is pinned rather than
// left to be rediscovered. The empty-ledger rescue still matters for the case it
// was written for: a census READ failure, which returns before any system is
// iterated and therefore still writes nothing — see
// TestCutover_ScreenStageFailure_DoesNotLatchDone.
func TestCutover_EveryScreenFails_RecoversThroughTheSweepNotARefire(t *testing.T) {
	world := newCutoverWorld(t)
	world.catalog.marketsErr = map[string]error{}
	for _, system := range []string{"X1-AA1", "X1-BB2", "X1-CC3", "X1-ORE"} {
		world.catalog.marketsErr[system] = errUnreadable
	}

	require.Error(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Len(t, world.ledger.systems, 4, "every failed system still leaves a row")
	for system, row := range world.ledger.systems {
		require.Equal(t, parkedsensing.VerdictPending, row.Verdict, "%s awaits screening", system)
	}
	require.ElementsMatch(t, []string{"X1-AA1", "X1-BB2"}, world.posts.removed,
		"the irreversible half committed and stands")

	world.catalog.marketsErr = nil
	censusReads := world.depth.calls
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Equal(t, censusReads, world.depth.calls,
		"the cutover does NOT re-fire — the ledger is no longer empty")
	require.Equal(t, parkedsensing.VerdictInScope, world.ledger.systems["X1-AA1"].Verdict)
	require.Equal(t, parkedsensing.VerdictInScope, world.ledger.systems["X1-BB2"].Verdict)
	require.Equal(t, parkedsensing.VerdictInScope, world.ledger.systems["X1-CC3"].Verdict)
	require.Equal(t, parkedsensing.VerdictNoWhitelist, world.ledger.systems["X1-ORE"].Verdict,
		"the sweep screened all four off their PENDING rows")
	require.Len(t, world.posts.removed, 2, "and retired nothing a second time")
}

// --- the cutover's own waypoint collision (sp-0gp21 extension review C1) --------

// THE SAME COLLISION THE ADOPTION RETRY CLOSES, in the cutover's copy. It was
// left open when the retry pass was fixed, because "the cutover's adoption" was
// treated as one thing when it is two: the write ORDER (tracked separately as
// sp-p84c6) and the waypoint guard, which is this.
//
// sensing_slots is keyed on (player, waypoint) and UpsertSpareSlot's conflict set
// carries assigned_ship, so the second of two co-located orphans silently
// re-points the row at itself. The first hull then holds the sensing tag with NO
// row anywhere, and that is UNRECOVERABLE: both adoption passes skip
// sensing_parked-tagged hulls, reuseSpareHull and the seed claim read FROM the
// ledger, and DockedProbeAt will use the hull as a purchasing buyer but never
// writes a row naming it. So it is invisible to CountOwnedProbes forever, the cap
// under-reads, and a replacement is bought for a probe we own — with no error, no
// parked_sensing_adopt_tag_failed (the tag SUCCEEDED), and a healthy heartbeat.
//
// It fires on the irreversible first EXPANSION tick, and several idle probes at
// the home shipyard is an ordinary fleet shape.
func TestCutover_TwoOrphansAtOneWaypoint_AdoptsExactlyOne(t *testing.T) {
	world := newCutoverWorld(t)
	// Three ships: the home post's hull, and two orphans standing together.
	world.fleet.ships = []*navigation.Ship{
		scoutProbe(t, "PROBE-HOME", testHomeSystem+"-A1"),
		scoutProbe(t, "PROBE-ORPHAN-1", "X1-FAR1-A1"),
		scoutProbe(t, "PROBE-ORPHAN-2", "X1-FAR1-A1"), // same waypoint
	}
	logger := &capturingLogger{}
	ctx := common.WithLogger(world.ctx, logger)

	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))

	// Both counts are RECOMPUTED at each checkpoint rather than captured once —
	// re-asserting a local int after a second tick would pass no matter what that
	// tick did, which is the "certifies nothing" shape this whole wave closed.
	rowsWithHulls := func() int {
		n := 0
		for _, slot := range world.ledger.slots {
			if slot.AssignedShip != "" {
				n++
			}
		}
		return n
	}
	taggedOrphans := func() int {
		n := 0
		for _, hull := range []string{"PROBE-ORPHAN-1", "PROBE-ORPHAN-2"} {
			if world.tagger.tagged[hull] == parkedsensing.SensingParkedFleetTag {
				n++
			}
		}
		return n
	}

	require.Equal(t, 1, rowsWithHulls(), "one waypoint holds one row, so exactly one hull is recorded")
	require.Equal(t, 1, logger.payload("parked_sensing_cutover")["probes_adopted"],
		"and the count reports one — not two, with one of them silently overwritten")

	// The hull that did NOT get the row must be left UNTAGGED. Tagged-with-no-row
	// is the unrecoverable state: it would fail every later adoption filter.
	require.Equal(t, 1, taggedOrphans(),
		"only the recorded hull is tagged; tagging the other would strand it beyond recovery")

	// A later tick must not make it worse either. The standing adoption retry runs
	// on this tick and sees the skipped hull — its own occupancy guard is what
	// keeps it skipped rather than letting it overwrite the row that now exists.
	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))
	require.Equal(t, 1, rowsWithHulls(), "the later tick did not overwrite the row either")
	require.Equal(t, 1, taggedOrphans(), "and no later tick tags the hull it could not record")
}

// --- the shipyard blind spot, from the live tick ---------------------------------

// CLOSED IS NOT ARMED. The free catalogue sweep is worth nothing if the reconcile
// never calls it, and a discovery pass that silently does not run looks exactly
// like a fleet that has already read everything — the heartbeat would report zero
// outstanding either way.
//
// So this drives the REAL ReconcileOnce and asserts the blind spot actually
// shrank: yards outstanding at the start of the tick, read by the end of it, and
// reported on the one line the wake model reads.
func TestReconcile_ReadsTheOutstandingShipyardCatalogues(t *testing.T) {
	world := newCutoverWorld(t)
	world.yards.outstanding = []parkedsensing.OutstandingYard{
		{Waypoint: "X1-QR78-AE4F", System: "X1-QR78", Frontier: 1},
		{Waypoint: "X1-QR78-FE8C", System: "X1-QR78", Frontier: 1},
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.ElementsMatch(t, []string{"X1-QR78-AE4F", "X1-QR78-FE8C"}, world.yards.yardsRead(),
		"the tick must read the shipyards nobody has ever asked about")
	require.Empty(t, world.yards.yardsOutstanding(), "and the blind spot must actually shrink")

	payload := logger.payload("parked_sensing_cycle")
	require.Equal(t, 2, payload["yards_read"])
	require.Equal(t, 2, payload["yards_outstanding"],
		"outstanding reports the backlog the tick STARTED with — the number an operator watches fall")
}

// THE SHIPYARD READS ARE REQUIRED, NOT OPTIONAL-INJECTION.
//
// The distinction is the whole difference between a shipped feature and a dormant
// one. A nil-tolerant yard read would leave the coordinator ticking along
// reporting healthy while never learning what a single shipyard sells — which is
// indistinguishable, from the heartbeat, from a fleet that has already read them
// all. So the engine is held fail-closed and LOUD instead, exactly as it is for
// every other port it cannot work without.
//
// Every OTHER port is present here, so this fails only if the clause under test is
// gone from wired().
func TestSensing_MissingRequiredPorts_HoldsTheTickInert(t *testing.T) {
	for _, missing := range []struct {
		name string
		drop func(*SensingEnginePorts)
	}{
		{"the outstanding-yard enumeration", func(p *SensingEnginePorts) { p.YardCatalog = nil }},
		{"the free catalogue read", func(p *SensingEnginePorts) { p.YardRead = nil }},
		{"the parked-probe yard read", func(p *SensingEnginePorts) { p.YardScan = nil }},
	} {
		t.Run(missing.name, func(t *testing.T) {
			world := newCutoverWorld(t)
			ports := world.ports
			missing.drop(&ports)
			world.handler.SetEnginePortsFactory(func(int) SensingEnginePorts { return ports })

			require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

			require.Empty(t, world.posts.removed, "an engine that cannot read a shipyard retires nothing")
			require.Empty(t, world.ledger.systems, "and screens nothing")
		})
	}
}

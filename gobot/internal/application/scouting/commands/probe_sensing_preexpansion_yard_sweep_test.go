package commands

// probe_sensing_preexpansion_yard_sweep_test.go covers the FREE shipyard-catalogue
// sweep on a tick the EXPANSION gate holds closed (sp-z7u71).
//
// THE TEST THAT WOULD HAVE CAUGHT IT, and the shape that would not have. The
// sweep's own comment claimed it was "run on EVERY tick and gated on nothing",
// and every existing test of it drove a tick with the phase gate OPEN — where
// that claim is true. The gate returns before the call, so the entire
// DATA/INCOME/GATE period was spent holding no shipyard catalogue at all and
// EXPANSION began cold on heavy-hull-yard discovery every era. So every
// pre-EXPANSION test here sets the phase reader to NOT in expansion and reads the
// yards back out of the store the sweep writes to.
//
// The post-EXPANSION regression at the bottom is the other half, and it is the
// one that matters most. The fix adds a SECOND call rather than hoisting the
// existing one, precisely because the EXPANSION call's position — after the
// screen, before the drain — is a contract in both directions. That position is
// pinned here through the ledger's own call sequence, the same mechanism the
// reaper's ordering test uses, plus a purchase that is only reachable if the
// sweep's writes land before the drain reads them.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// --- shared fixture helpers ------------------------------------------------------

// preExpansionWorld is the steady fixture with the jump gate still under
// construction: bootstrap owns probes, and the only thing this coordinator may do
// is read.
func preExpansionWorld(t *testing.T) *cutoverWorld {
	t.Helper()
	world := newCutoverWorld(t)
	world.handler.phase = &fakePhase{inExpansion: false}
	// The cutover census is a post-EXPANSION read; leaving its rows in place would
	// let a gate regression present as "the fixture was busy anyway".
	world.depth.rows = nil
	return world
}

// outstandingYards fills the blind spot with n never-read shipyards, ranked so
// the bounded pick is reproducible: the frontier ranks descend with the index, so
// the first yards read are Y1, Y2, ... and the survivors of a bounded tick are
// the tail. An equal-rank fixture would make WHICH yards a bounded tick reads
// depend on the tie-break alone and prove nothing about the ordering.
func outstandingYards(n int) []parkedsensing.OutstandingYard {
	out := make([]parkedsensing.OutstandingYard, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, parkedsensing.OutstandingYard{
			Waypoint: fmt.Sprintf("X1-DARK-Y%d", i+1),
			System:   "X1-DARK",
			Frontier: n - i,
		})
	}
	return out
}

// loggedUnder returns the message and payload logged under an action, and whether
// it was logged at all. The message is asserted alongside the payload because the
// payload is not what an operator reads at 4am.
func loggedUnder(log *messageLogger, action string) (string, map[string]interface{}, bool) {
	log.mu.Lock()
	defer log.mu.Unlock()
	for i, fields := range log.fields {
		if got, ok := fields["action"].(string); ok && got == action {
			return log.messages[i], fields, true
		}
	}
	return "", nil, false
}

// The structured actions the sweep reports under. Assembled from consts rather
// than spelled out at each site so a rename cannot leave a NEGATIVE assertion
// passing by never matching anything.
const (
	yardSweepAction        = "parked_sensing_yard_catalog_sweep"
	yardSweepFailedAction  = "parked_sensing_yard_catalog_sweep_failed"
	yardSweepUnwiredAction = "parked_sensing_yard_catalog_unwired"
)

// --- the pre-EXPANSION sweep -----------------------------------------------------

// THE HEADLINE. A tick the EXPANSION gate holds closed still learns what the
// known shipyards sell — and the gate still holds everything that costs anything.
func TestSensing_PreExpansion_ReadsTheShipyardCatalogues(t *testing.T) {
	world := preExpansionWorld(t)
	world.yards.outstanding = outstandingYards(3)
	log := &messageLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd))

	require.ElementsMatch(t, []string{"X1-DARK-Y1", "X1-DARK-Y2", "X1-DARK-Y3"}, world.yards.yardsRead(),
		"a pre-EXPANSION tick must read the shipyards nobody has ever asked about")
	require.Empty(t, world.yards.yardsOutstanding(),
		"and the blind spot must actually shrink before EXPANSION, not after it")

	// THE GATE IS UNCHANGED. The fix widens exactly one free read; everything the
	// gate exists to withhold must still be withheld, or this is not the fix.
	require.Empty(t, world.posts.removed, "no scout post is retired before the hand-off")
	require.Empty(t, world.ledger.systems, "nothing is screened")
	require.Empty(t, world.purchaser.owners, "no probe is bought")
	require.Zero(t, world.fleet.calls, "no fleet read")
	require.Zero(t, world.depth.calls, "no census read")
	require.Equal(t, 3, world.calls.total(),
		"the tick's ONLY outbound calls are the three catalogue reads")
}

// The report is logged, because on this path there is nothing else to log it. The
// post-EXPANSION tick carries these numbers on the cycle heartbeat; a
// pre-EXPANSION tick emits no heartbeat at all, so without this line a yard map
// building for hours is indistinguishable from a sweep that never fired.
func TestSensing_PreExpansion_LogsTheCatalogueReport(t *testing.T) {
	world := preExpansionWorld(t)
	world.yards.outstanding = outstandingYards(2)
	log := &messageLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd))

	msg, fields, logged := loggedUnder(log, yardSweepAction)
	require.True(t, logged, "the pre-EXPANSION sweep must report what it read; it is the only evidence it ran")
	require.Equal(t, 2, fields["yards_outstanding"], "the backlog at the start of the pass")
	require.Equal(t, 2, fields["yards_read"], "the catalogues recorded this tick")
	require.Equal(t, 0, fields["yards_failed"])
	require.Equal(t, world.cmd.ContainerID, fields["container_id"],
		"the line must name the container, or it cannot be filtered to one player's coordinator")
	require.Contains(t, msg, "read 2 of 2 outstanding, 0 failed",
		"the rendered line carries the numbers, not just the payload")
}

// EMITTED ON THE ZERO TICK TOO. A drained backlog and a sweep that never fired
// produce the same silence otherwise, and the drained state is the steady state —
// so the line that proves the pass is alive is the one nobody would think to log.
func TestSensing_PreExpansion_LogsTheReportWhenNothingIsOutstanding(t *testing.T) {
	world := preExpansionWorld(t) // no outstanding yards
	log := &messageLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd))

	_, fields, logged := loggedUnder(log, yardSweepAction)
	require.True(t, logged, "an empty backlog is a finding, not a reason to stay silent")
	require.Equal(t, 0, fields["yards_outstanding"])
	require.Equal(t, 0, fields["yards_read"])
	require.Zero(t, world.calls.total(), "and it costs nothing to say so")
}

// The pre-EXPANSION pass is the SAME pass, bound included: no separate cap, no
// separate pacing. Ten dark yards drain over two ticks at eight a tick, which is
// also what makes the pass self-quiescing rather than a permanent per-tick cost.
func TestSensing_PreExpansion_HonoursTheSharedPerTickBound(t *testing.T) {
	world := preExpansionWorld(t)
	world.yards.outstanding = outstandingYards(parkedsensing.MaxYardCatalogReads + 2)
	log := &messageLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd))

	require.Len(t, world.yards.yardsRead(), parkedsensing.MaxYardCatalogReads,
		"one tick reads at most the shared bound, pre-EXPANSION as anywhere else")
	require.Len(t, world.yards.yardsOutstanding(), 2, "the tail stays outstanding")

	// The second tick finishes the job and the pass then goes quiet on its own.
	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd))
	require.Len(t, world.yards.yardsRead(), parkedsensing.MaxYardCatalogReads+2)
	require.Empty(t, world.yards.yardsOutstanding())
}

// --- the pre-EXPANSION sweep's failure modes -------------------------------------

// The two reads are checked INDIVIDUALLY, and this is what that buys. ports.wired()
// would have held this free pass hostage to the purchaser, the treasury and the
// mover — none of which it is handed — so a boot that has not wired the paid
// engine yet, which is exactly the pre-EXPANSION boot, would learn no catalogue at
// all. A missing yard port skips the sweep and says so; it never fails the tick.
func TestSensing_PreExpansion_UnwiredYardPortsSkipTheSweep(t *testing.T) {
	cases := []struct {
		name string
		// rewire installs the broken surface on a world whose sweep would
		// otherwise have three yards to read.
		rewire func(world *cutoverWorld)
	}{
		{"the frontier is missing", func(world *cutoverWorld) {
			ports := world.ports
			ports.YardCatalog = nil
			world.handler.SetEnginePortsFactory(func(int) SensingEnginePorts { return ports })
		}},
		{"the reader is missing", func(world *cutoverWorld) {
			ports := world.ports
			ports.YardRead = nil
			world.handler.SetEnginePortsFactory(func(int) SensingEnginePorts { return ports })
		}},
		{"no engine surface at all", func(world *cutoverWorld) {
			world.handler.SetEnginePortsFactory(nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := preExpansionWorld(t)
			world.yards.outstanding = outstandingYards(3)
			tc.rewire(world)
			log := &messageLogger{}

			require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd),
				"an unwired free read is inert, never fatal: the tick is correctly idle either way")

			require.Empty(t, world.yards.yardsRead(), "no catalogue can be read through a missing port")
			require.Len(t, world.yards.yardsOutstanding(), 3, "so the blind spot is untouched")
			_, _, logged := loggedUnder(log, yardSweepUnwiredAction)
			require.True(t, logged, "a silently skipped sweep is indistinguishable from a drained one")
			_, _, reported := loggedUnder(log, yardSweepAction)
			require.False(t, reported, "and it must not report a pass it never made")
		})
	}
}

// THE REASON THE GUARD IS NOT ports.wired(), stated as a test. That check
// requires the entire PAID engine surface — the purchaser, the treasury, the
// mover, seventeen ports — and this pass is handed none of them and could not
// reach them. A sweep gated on it would go inert on exactly the partially-wired
// boot the fix exists to serve, which is a bug that looks identical to the one
// being fixed. So: paid surface stripped, two free reads present, sweep runs.
//
// This test fails the moment the guard is "simplified" to the shared one.
func TestSensing_PreExpansion_SweepRunsWithThePaidSurfaceUnwired(t *testing.T) {
	world := preExpansionWorld(t)
	world.yards.outstanding = outstandingYards(3)

	ports := world.ports
	ports.Purchaser = nil
	ports.Treasury = nil
	ports.Mover = nil
	ports.Budget = nil
	ports.UnpricedPool = nil
	require.False(t, ports.wired(), "the fixture must be a surface ports.wired() REFUSES, or it proves nothing")
	world.handler.SetEnginePortsFactory(func(int) SensingEnginePorts { return ports })

	log := &messageLogger{}
	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd))

	require.Len(t, world.yards.yardsRead(), 3,
		"the free sweep needs the frontier and the reader, and nothing else: a boot with no purchaser must still learn what its shipyards sell")
	_, fields, logged := loggedUnder(log, yardSweepAction)
	require.True(t, logged)
	require.Equal(t, 3, fields["yards_read"])
}

// A frontier that cannot be enumerated is the pass being unable to see its own
// work — fatal to the PASS, and deliberately not to the tick. It is logged and
// swallowed: a catalogue-read hiccup must never turn into a sensing-coordinator
// failure on a tick whose whole job is to stand correctly idle.
func TestSensing_PreExpansion_UnreadableFrontierIsLoggedAndNotFatal(t *testing.T) {
	world := preExpansionWorld(t)
	world.yards.outstanding = outstandingYards(3)
	world.yards.listErr = errors.New("the waypoints table is unreadable")
	log := &messageLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd),
		"the free sweep's failure must not propagate out of a pre-EXPANSION tick")

	require.Empty(t, world.yards.yardsRead(), "an unreadable work list reads nothing")
	msg, fields, logged := loggedUnder(log, yardSweepFailedAction)
	require.True(t, logged, "a swallowed error that is not logged is an invisible one")
	require.Contains(t, msg, "the waypoints table is unreadable",
		"the line must name the cause, not merely that something failed")
	require.Contains(t, fields["error"], "the waypoints table is unreadable")
	_, _, reported := loggedUnder(log, yardSweepAction)
	require.False(t, reported, "a pass that could not enumerate must not report an empty backlog")
}

// ONE yard the API refuses is not the tick's problem, and not the other yards'
// either. It is counted, named one layer down, and the pass reports the failure
// rather than swallowing it into a zero.
func TestSensing_PreExpansion_ARefusedYardIsCountedNotFatal(t *testing.T) {
	world := preExpansionWorld(t)
	world.yards.outstanding = outstandingYards(2)
	world.yards.readErr = errors.New("shipyard 404")
	log := &messageLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd))

	require.Empty(t, world.yards.yardsRead(), "neither read was recorded")
	require.Len(t, world.yards.yardsOutstanding(), 2, "so both yards stay outstanding")
	msg, fields, logged := loggedUnder(log, yardSweepAction)
	require.True(t, logged)
	require.Equal(t, 2, fields["yards_failed"], "the refusals are counted, not hidden")
	require.Equal(t, 0, fields["yards_read"])
	require.Contains(t, msg, "2 failed")
}

// The phase gate's verdict is UNMOVED by the sweep. A tick that read eight
// catalogues still placed nothing, so it is still idle — and idle is what keeps a
// stall streak from surviving the phase change and escalating a correctly-gated
// coordinator.
func TestSensing_PreExpansion_SweepDoesNotDisturbTheIdleVerdict(t *testing.T) {
	world := preExpansionWorld(t)
	world.yards.outstanding = outstandingYards(20) // more than any tick can drain, so every tick sweeps
	spy := &sensingStallSpy{}
	world.handler.SetStallObserver(spy)

	for i := 0; i < health.StallEscalationTicks*2; i++ {
		require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	}

	require.NotEmpty(t, world.yards.yardsRead(),
		"the sweep ran on these ticks, so the verdict below is about a tick that did work")
	got := spy.forCoordinator(sensingStallCoordinator)
	require.Len(t, got, health.StallEscalationTicks*2, "every gated tick still reports a verdict")
	for i, outcome := range got {
		require.Equal(t, health.StallIdle, outcome.Outcome,
			"tick %d reported %s (%q): a pre-EXPANSION hold is correct however many catalogues it read, and a streak must not survive the phase change",
			i, outcome.Outcome, outcome.Reason)
	}
}

// --- the post-EXPANSION regression -----------------------------------------------

// yardSweepEvent marks the free sweep in the ledger's own call sequence, so its
// position can be compared against the stages that are already pinned there.
const yardSweepEvent = "YardCatalogSweep"

// recordStage appends one externally-observed marker to the ledger's tick
// sequence. It exists so a NON-ledger stage can be ordered against the ledger
// events the reaper's test already identifies the screen and the drain by.
func (f *psLedger) recordStage(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(event)
}

// countStage is how many times a marker appears — the difference between "the
// sweep still runs" and "the sweep now runs twice", which is exactly the
// regression a hoisted call would introduce.
func (f *psLedger) countStage(event string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, got := range f.order {
		if got == event {
			n++
		}
	}
	return n
}

// orderedYardPorts is the sweep's two ports with the tick's own bookkeeping
// wrapped around them: it stamps the ledger sequence when the pass enumerates,
// and it PUBLISHES a read yard into the waypoint catalog.
//
// The publication is the point, not decoration. Recording a catalogue is what
// makes a yard visible to the drain's yard lookup in production, so a fixture
// where the drain can only find its yard if the sweep has already run turns the
// documented ordering into a behaviour a test can fail on.
type orderedYardPorts struct {
	*fakeYardCatalog
	ledger  *psLedger
	publish func(waypoint string)
}

func (y *orderedYardPorts) OutstandingYards(ctx context.Context, playerID int) ([]parkedsensing.OutstandingYard, error) {
	y.ledger.recordStage(yardSweepEvent)
	return y.fakeYardCatalog.OutstandingYards(ctx, playerID)
}

func (y *orderedYardPorts) ReadCatalog(ctx context.Context, playerID int, waypoint string) error {
	if err := y.fakeYardCatalog.ReadCatalog(ctx, playerID, waypoint); err != nil {
		return err
	}
	y.publish(waypoint)
	return nil
}

// THE POST-EXPANSION CALL IS UNMOVED, and both edges of its position are the
// contract:
//
//   - AFTER the screen, so a system charted by THIS tick's sweep already has its
//     waypoint rows and its yards are enumerable on this same tick.
//   - BEFORE the drain, because the drain's yard lookup reads the very rows this
//     pass writes.
//
// The fix adds a second call on the pre-EXPANSION branch rather than hoisting
// this one precisely because a hoisted call could honour neither edge. This is
// the regression that would make that trade silently worthless.
func TestSensing_PostExpansion_YardSweepStillRunsBetweenTheScreenAndTheDrain(t *testing.T) {
	world := steadyWorld(t, map[string]string{
		"X1-IN1":  parkedsensing.VerdictInScope,
		"X1-PEND": parkedsensing.VerdictPending,
	})
	// A PENDING system the screen decides this tick, so the screen genuinely runs
	// ahead of the sweep rather than being merely earlier in the file.
	world.goods.goods["X1-PEND-M1"] = []string{"FOOD"}

	// The placement a probe is bought FOR, and the hull that can do the buying.
	world.ledger.slots[psSlotKey{"X1-IN1-M1", parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M1", System: "X1-IN1",
		Kind: parkedsensing.SlotKindMarket, State: parkedsensing.SlotStateWanted,
	}
	world.shipPos.docked["X1-IN1-Y1"] = "PROBE-BUYER"

	// The yard is DARK at the top of the tick: nothing knows it sells probes until
	// the sweep records its catalogue. If the sweep ran after the drain, the drain
	// would find no yard and buy nothing.
	world.yards.outstanding = []parkedsensing.OutstandingYard{
		{Waypoint: "X1-IN1-Y1", System: "X1-IN1", Frontier: 1},
	}
	require.Empty(t, world.catalog.yards["X1-IN1"], "the yard must start dark, or this proves nothing")

	ordered := &orderedYardPorts{fakeYardCatalog: world.yards, ledger: world.ledger,
		publish: func(waypoint string) {
			world.catalog.yards["X1-IN1"] = append(world.catalog.yards["X1-IN1"], waypoint)
		}}
	ports := world.ports
	ports.YardCatalog = ordered
	ports.YardRead = ordered
	// YardScan is left on the RAW fake: it is the pacer's port, driven from another
	// goroutine, and marking it would put that goroutine's reads into the sequence
	// this test orders stages by.
	world.handler.SetEnginePortsFactory(func(int) SensingEnginePorts { return ports })

	log := &capturingLogger{}
	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd))

	screen := world.ledger.indexOf("SystemsByVerdict:" + parkedsensing.VerdictPending)
	sweep := world.ledger.indexOf(yardSweepEvent)
	drain := world.ledger.indexOf(drainReadEvent())

	require.NotEqual(t, -1, screen, "the screening sweep ran")
	require.NotEqual(t, -1, sweep, "the shipyard-catalogue sweep still runs on a post-EXPANSION tick")
	require.NotEqual(t, -1, drain, "the buy queue ran")

	require.Greater(t, sweep, screen,
		"the catalogue sweep runs AFTER the screen, so a system charted this tick has enumerable waypoint rows")
	require.Less(t, sweep, drain,
		"and BEFORE the drain, whose yard lookup reads the rows this pass writes")
	require.Equal(t, 1, world.ledger.countStage(yardSweepEvent),
		"exactly one sweep per tick — the pre-EXPANSION branch must not add a second pass to an EXPANSION tick")

	// The ordering stated as behaviour: the probe is bought at a yard that was dark
	// when this tick started.
	require.Equal(t, []string{"X1-IN1-Y1"}, world.yards.yardsRead(), "the dark yard's catalogue was recorded")
	require.NotEmpty(t, world.purchaser.owners,
		"the drain bought at a yard the sweep published EARLIER IN THE SAME TICK; a sweep running after the drain would find no yard at all")

	// The report still reaches the cycle heartbeat — the post-EXPANSION channel,
	// which the pre-EXPANSION line does not replace.
	cycle := log.payload("parked_sensing_cycle")
	require.NotNil(t, cycle, "the post-EXPANSION tick still emits its heartbeat")
	require.Equal(t, 1, cycle["yards_read"], "carrying the numbers from THIS tick's sweep")

	// And the pre-EXPANSION line is exclusively pre-EXPANSION: an EXPANSION tick
	// that emitted it would mean the new branch is running where it must not.
	require.False(t, log.sawAction(yardSweepAction),
		"the pre-EXPANSION report belongs to the gated branch alone")
	require.False(t, log.sawAction(yardSweepUnwiredAction))
}

// The phase FLIPS. The yard map built while the gate was under construction is
// still there on the first EXPANSION tick — which is the entire point of the fix:
// EXPANSION starts warm on shipyard knowledge instead of cold.
func TestSensing_PhaseFlips_TheYardMapBuiltPreExpansionSurvives(t *testing.T) {
	world := preExpansionWorld(t)
	phase := world.handler.phase.(*fakePhase)
	world.yards.outstanding = outstandingYards(3)

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	preExpansionReads := world.yards.yardsRead()
	require.Len(t, preExpansionReads, 3, "the gated ticks built the map")

	phase.inExpansion = true
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Equal(t, preExpansionReads, world.yards.yardsRead(),
		"the first EXPANSION tick re-reads nothing: the catalogues are already held, so the pass is silent on its own")
	require.Empty(t, world.yards.yardsOutstanding(),
		"EXPANSION begins with the yard map already drained, not with the whole backlog ahead of it")
}

// A guard against the fixtures above passing for the wrong reason: the action
// names asserted on are the ones the code actually emits. A rename that broke
// every negative assertion in this file silently would be caught here.
func TestSensing_PreExpansion_SweepActionNamesAreTheOnesEmitted(t *testing.T) {
	world := preExpansionWorld(t)
	world.yards.outstanding = outstandingYards(1)
	log := &messageLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd))

	log.mu.Lock()
	defer log.mu.Unlock()
	var actions []string
	for _, fields := range log.fields {
		if action, ok := fields["action"].(string); ok {
			actions = append(actions, action)
		}
	}
	require.Contains(t, actions, yardSweepAction,
		"the emitted action name must match the constant every assertion in this file keys on; got %s",
		strings.Join(actions, ", "))
}

package commands

// run_probe_sensing_coordinator_test.go covers the composition itself: the gate
// in front of the tick, the order and budget the engines are driven with, the
// bounded screening sweep, and how the knobs resolve.
//
// The engines' own behaviour is tested in internal/application/parkedsensing.
// What is under test here is the wiring between them — which is where a mistake
// is silent, because every engine still reports success while the fleet does the
// wrong thing.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- the EXPANSION gate --------------------------------------------------------

// Pre-EXPANSION the world is still building its jump gate: bootstrap owns probe
// provisioning and the scout-post coordinator mans what it bought. The parked
// model buys hulls and retires posts, so a tick before the hand-off does NOTHING
// — not even the cutover, which would delete the posts the old world is still
// running on.
func TestSensing_PreExpansion_TakesNoAction(t *testing.T) {
	world := newCutoverWorld(t)
	phase := &fakePhase{inExpansion: false}
	world.handler.phase = phase

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Equal(t, 1, phase.calls, "the phase is read once per tick")
	require.Zero(t, world.depth.calls, "no census read — the gate is checked before any work")
	require.Zero(t, world.fleet.calls, "no fleet read")
	require.Empty(t, world.posts.removed, "no scout post is retired before the hand-off")
	require.Empty(t, world.ledger.systems, "nothing is screened")
	require.Zero(t, world.calls.total(), "not one outbound call")
}

// A phase that cannot be verified is not a licence to run: an unreadable world
// and an unwired reader both hold sensing inert, exactly as the probe buyer's
// gate does. The fake reports IN EXPANSION alongside its error, so a gate that
// read the bool and swallowed the error would run the whole tick and fail these
// assertions rather than being masked by a convenient false.
func TestSensing_PhaseUnverifiable_FailsClosed(t *testing.T) {
	cases := []struct {
		name  string
		phase expansionPhaseReader
	}{
		{"phase read fails", &fakePhase{inExpansion: true, err: errors.New("construction site unreachable")}},
		{"no reader wired", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := newCutoverWorld(t)
			world.handler.phase = tc.phase

			require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

			require.Zero(t, world.depth.calls, "no census read on an unverifiable phase")
			require.Empty(t, world.posts.removed, "no post retired on an unverifiable phase")
			require.Empty(t, world.ledger.systems)
			require.Zero(t, world.calls.total())
		})
	}
}

// The phase FLIPS mid-run — the deploy lands before the gate is built, the gate
// completes, and the coordinator keeps ticking through the transition. The
// cutover must fire on the first EXPANSION tick and NEVER again, including on
// the many ticks that follow.
//
// Phase-transition edges have bitten this fleet before, and the failure here
// would be a second retirement sweep against posts that legitimately exist
// again. The trigger is an empty ledger, and the first cutover fills it, so
// re-firing should be structurally impossible — this pins that it actually is.
func TestSensing_PhaseFlipsMidRun_CutsOverExactlyOnce(t *testing.T) {
	world := newCutoverWorld(t)
	phase := &fakePhase{inExpansion: false}
	world.handler.phase = phase
	ctx := world.ctx

	// Three pre-EXPANSION ticks: the world is untouched.
	for i := 0; i < 3; i++ {
		require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))
	}
	require.Empty(t, world.posts.removed, "nothing retired while the gate is still being built")

	// The gate completes.
	phase.inExpansion = true
	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))

	require.ElementsMatch(t, []string{"X1-AA1", "X1-BB2"}, world.posts.removed,
		"the first EXPANSION tick performs the cutover")
	censusReads := world.depth.calls
	require.Equal(t, 1, censusReads, "the cutover census is read exactly once")

	// Five more EXPANSION ticks: the cutover never runs again.
	for i := 0; i < 5; i++ {
		require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))
	}
	require.Len(t, world.posts.removed, 2, "no second retirement sweep on any later tick")
	require.Equal(t, censusReads, world.depth.calls, "and the census is never re-read")
}

// A half-wired engine is a wedge, not a degraded mode: it would plan placements
// forever and fill none. The tick refuses whole and says so.
func TestSensing_UnwiredPorts_HoldsTheTickInert(t *testing.T) {
	world := newCutoverWorld(t)
	world.handler.SetEnginePortsFactory(func(int) SensingEnginePorts {
		return SensingEnginePorts{Ledger: world.ledger} // everything else nil
	})

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Empty(t, world.posts.removed, "a half-wired engine retires nothing")
	require.Empty(t, world.ledger.systems, "and screens nothing")
	require.Zero(t, world.calls.total())
}

// --- the screening sweep -------------------------------------------------------

// steadyWorld is a post-cutover world: the ledger already holds systems, so the
// tick goes straight to the steady-state path.
func steadyWorld(t *testing.T, systems map[string]string) *cutoverWorld {
	t.Helper()
	world := newCutoverWorld(t)
	world.depth.rows = nil // the census is a cutover-only read
	for system, verdict := range systems {
		world.ledger.systems[system] = parkedsensing.ExpandSystem{
			System: system, Verdict: verdict, CatalogKnown: true,
		}
		world.catalog.known[system] = true
		world.catalog.markets[system] = []string{system + "-M1"}
	}
	return world
}

// Only PENDING systems are re-screened, and at most five per tick.
//
// Both halves are cost properties. Re-screening a decided system would put the
// sweep's cost on the size of the KNOWN map rather than the frontier; the batch
// bound is what keeps a large PENDING backlog from firing a burst of remote
// fetches and paginated catalog sweeps in one tick.
func TestScreenSweep_PendingOnly_AndBounded(t *testing.T) {
	verdicts := map[string]string{
		"X1-IN1": parkedsensing.VerdictInScope,
		"X1-IN2": parkedsensing.VerdictInScope,
		"X1-NO1": parkedsensing.VerdictNoWhitelist,
	}
	// Seven PENDING systems, so the batch bound is genuinely exercised.
	for _, s := range []string{"X1-P1", "X1-P2", "X1-P3", "X1-P4", "X1-P5", "X1-P6", "X1-P7"} {
		verdicts[s] = parkedsensing.VerdictPending
	}
	world := steadyWorld(t, verdicts)

	// Give every system a market the cache can answer for, so screening is free
	// and what the test measures is WHICH systems were looked at.
	for system := range verdicts {
		world.goods.goods[system+"-M1"] = []string{"FOOD"}
	}

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	rescreened := 0
	for system, verdict := range verdicts {
		if verdict != parkedsensing.VerdictPending {
			require.Equal(t, verdict, world.ledger.systems[system].Verdict,
				"%s was already decided and must not be re-screened", system)
			continue
		}
		if world.ledger.systems[system].Verdict != parkedsensing.VerdictPending {
			rescreened++
		}
	}
	require.Equal(t, screenSweepBatch, rescreened,
		"exactly the batch bound of PENDING systems is screened, not all seven")
}

// A PENDING system whose waypoint CATALOG has never been swept is swept FIRST,
// in-band, before it is screened.
//
// Without that, the screen reads an unswept system's empty waypoint list as a
// fully-examined barren one — the same reading, opposite meaning — and the
// NO_WHITELIST it would record is durable AND makes the system a frontier
// propagation origin, so one wrong write-off walks outward across the map.
func TestScreenSweep_CatalogUnknown_SweepsBeforeScreening(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-DARK": parkedsensing.VerdictPending})
	world.catalog.known["X1-DARK"] = false
	world.catalog.markets["X1-DARK"] = nil

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Equal(t, []string{"X1-DARK"}, world.seeds.synced,
		"the unswept system's catalog is fetched before any verdict is recorded")
	require.Equal(t, parkedsensing.VerdictPending, world.ledger.systems["X1-DARK"].Verdict,
		"an unswept system stays PENDING rather than being written off on absent evidence")
}

// A repeating catalog-sweep failure is otherwise invisible: the system simply
// stays PENDING forever while the sweep burns calls. It must not abort the tick
// either — the rest of the reconcile is unaffected by one dark system.
func TestScreenSweep_CatalogSweepFails_HoldsPendingAndContinues(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-DARK": parkedsensing.VerdictPending})
	world.catalog.known["X1-DARK"] = false
	world.seeds.syncErr = errors.New("api down")

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd),
		"one dark system does not fail the tick")
	require.Equal(t, parkedsensing.VerdictPending, world.ledger.systems["X1-DARK"].Verdict)
}

// --- the budget ----------------------------------------------------------------

// The two rates are NOT interchangeable and the composition must not swap them.
//
// Expansion gates on the SENSING residual, which the emergency brake can drive
// BELOW the minimum scan rate; the pacer runs at the FLOORED rate so market data
// never goes fully dark. Gating expansion on the pacer rate would make the brake
// invisible to it and leave it charting at full tilt through a rate-limit storm.
func TestBudget_ExpansionGetsTheResidual_PacerGetsTheFlooredRate(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	// Pressure well above the high-water mark, so the brake bites every tick.
	world.handler.pressure = &fakePressure{wait: 5 * time.Second}
	base := world.handler.newPorts
	world.handler.SetEnginePortsFactory(func(playerID int) SensingEnginePorts {
		ports := base(playerID)
		ports.Budget = &fakeBudget{ceiling: 2.0, nonSensing: 1.8, charting: 0.05}
		return ports
	})

	cfg := resolveSensingConfig(context.Background(), world.cmd, nil)
	ports, _ := world.handler.portsFor(testPlayerID)

	// Drive the brake down over several ticks, exactly as the loop would.
	var budget domainSensing.BudgetInputs
	for i := 0; i < 6; i++ {
		budget = world.handler.budgetInputs(world.cmd, cfg, ports)
	}

	sensingRate := domainSensing.SensingRate(budget)
	pacerRate := domainSensing.PacerRate(budget)
	floor := float64(cfg.MinScanRateMilli) / 1000.0

	require.Less(t, budget.BrakeFactor, 1.0, "sustained pressure engages the emergency brake")
	require.Less(t, sensingRate, floor,
		"the residual is allowed BELOW the floor — that sub-floor value is what expansion yields on")
	require.GreaterOrEqual(t, pacerRate, floor,
		"the pacer re-imposes the floor, so parked market data never goes fully dark")
}

// The brake is advanced exactly ONCE per tick. It is multiplicative, so a second
// read in the same tick would halve it twice and shed twice as hard as the
// observed pressure justifies.
func TestBudget_BrakeAdvancesOncePerTick(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.handler.pressure = &fakePressure{wait: 5 * time.Second}
	cfg := resolveSensingConfig(context.Background(), world.cmd, nil)
	ports, _ := world.handler.portsFor(testPlayerID)

	first := world.handler.budgetInputs(world.cmd, cfg, ports)
	second := world.handler.budgetInputs(world.cmd, cfg, ports)

	require.InDelta(t, first.BrakeFactor*0.5, second.BrakeFactor, 1e-9,
		"each call halves the brake exactly once")
}

// --- knob resolution -----------------------------------------------------------

// fakeLiveConfig is the per-tick view of the persisted container config.
type fakeLiveConfig struct {
	snapshot liveconfig.Snapshot
	err      error
}

func (f *fakeLiveConfig) Snapshot(context.Context, string, int) (liveconfig.Snapshot, error) {
	return f.snapshot, f.err
}

// expansion_enabled is encoded 1=on / 2=off rather than 0/1, because
// `tune <key> 0` means revert-to-default fleet-wide — a 0/1 encoding would make
// "off" unexpressible.
func TestKnobs_ExpansionEnabledEncoding(t *testing.T) {
	cases := []struct {
		name  string
		value int
		want  bool
	}{
		{"absent means the default, which is ON", 0, true},
		{"1 is ON", 1, true},
		{"2 is OFF", 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := sensingTestCmd()
			cmd.ExpansionEnabled = tc.value
			cfg := resolveSensingConfig(context.Background(), cmd, nil)
			require.Equal(t, tc.want, cfg.Expansion)
		})
	}
}

// A live tune takes effect on the NEXT tick, without a rebuild — and a launch
// value stays in force until one is set.
func TestKnobs_LiveConfigOverridesLaunch(t *testing.T) {
	cmd := sensingTestCmd()
	cmd.ProbeCap = 40
	cmd.ExpansionEnabled = 1

	launched := resolveSensingConfig(context.Background(), cmd, nil)
	require.Equal(t, 40, launched.ProbeCap, "with no live value the launch config governs")
	require.True(t, launched.Expansion)

	tuned := resolveSensingConfig(context.Background(), cmd, liveconfig.Snapshot{
		"probe_cap":         float64(120), // float64: the JSON-recovery shape
		"expansion_enabled": 2,
	})
	require.Equal(t, 120, tuned.ProbeCap, "a live value wins over the launch config")
	require.False(t, tuned.Expansion, "and so does a live off-switch")
}

// A failed snapshot runs the tick on the LAUNCH command rather than on an empty
// config — never a half-applied one.
func TestKnobs_UnreadableSnapshotFallsBackToLaunch(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.handler.SetLiveConfigReader(&fakeLiveConfig{err: errors.New("container row gone")})
	cmd := sensingTestCmd()
	cmd.ProbeCap = 55

	snapshot := world.handler.liveSnapshot(context.Background(), cmd)
	require.Nil(t, snapshot, "an unreadable snapshot is nil, not a partial map")
	require.Equal(t, 55, resolveSensingConfig(context.Background(), cmd, snapshot).ProbeCap)
}

// Zero is the documented revert and resolves silently. A NEGATIVE is a miswrite
// that can only come from a hand-edited config row, and two of them are silently
// destructive if absorbed: a negative min_scan_rate_milli flows through to a
// negative sensing rate, and a clamp below 1 collapses the weighting's
// optimistic prior. Both take the default instead.
func TestKnobs_NegativesTakeTheDefault(t *testing.T) {
	cmd := sensingTestCmd()
	cmd.MinScanRateMilli = -250
	cmd.ValueClampR = -3
	cmd.CapexReserveCredits = -1

	cfg := resolveSensingConfig(context.Background(), cmd, nil)

	require.Equal(t, defaultMinScanRateMilli, cfg.MinScanRateMilli)
	require.Equal(t, defaultValueClampR, cfg.ClampR)
	require.Equal(t, int64(defaultCapexReserveCredits), cfg.CapexReserveCredits)

	// And the resolved values are actually SAFE, which is the property the
	// clamping exists for: a negative floor would otherwise invert the pacer.
	require.Positive(t, domainSensing.PacerRate(domainSensing.BudgetInputs{
		CeilingReqPerSec: 2.0, TargetUtilPct: cfg.TargetUtilPct,
		MinScanRateMilli: cfg.MinScanRateMilli, NonSensingRate: 10.0, BrakeFactor: 1,
	}), "the pacer rate stays positive however starved the residual is")
}

// Every knob falls back to its documented const when the launch config is empty
// (RULINGS #5), which is the shape every boot-standing launch actually has.
func TestKnobs_EmptyLaunchResolvesToDocumentedDefaults(t *testing.T) {
	cfg := resolveSensingConfig(context.Background(), sensingTestCmd(), nil)

	require.Equal(t, defaultSensingTickSeconds*time.Second, cfg.Tick)
	require.Equal(t, defaultParkedProbeCap, cfg.ProbeCap)
	require.True(t, cfg.Expansion)
	require.Equal(t, defaultTargetUtilPct, cfg.TargetUtilPct)
	require.Equal(t, defaultMinScanRateMilli, cfg.MinScanRateMilli)
	require.Equal(t, defaultValueClampR, cfg.ClampR)
	require.Equal(t, defaultInflightCap, cfg.InflightCap)
	require.Equal(t, defaultCapitalMultiplierKMilli, cfg.CapitalMultiplierKMilli)
	require.Equal(t, int64(defaultCapexReserveCredits), cfg.CapexReserveCredits)
	require.Equal(t, defaultQuartermasterCadenceSecs*time.Second, cfg.QuartermasterCadence)
	require.Len(t, cfg.Whitelist, len(defaultSensingWhitelist()))
}

// The touring model's knobs are retained on the command and IGNORED. A container
// persisted by the old core still carries them, and it must come up on the new
// core's defaults rather than on a stale value that means nothing here.
func TestKnobs_RetiredTouringKnobsAreInert(t *testing.T) {
	cmd := sensingTestCmd()
	cmd.ProbeBudget = 150
	cmd.DepthFloor = 2_000_000
	cmd.SecondProbeThreshold = 12
	cmd.FreshnessTargetSecs = 10_800
	cmd.MaxSpendPerCycle = 1
	cmd.SpendWindowSecs = 1
	cmd.PurchaseCooldownSecs = 1
	cmd.DiscoveryDeclaresPerTick = 99

	cfg := resolveSensingConfig(context.Background(), cmd, nil)

	require.Equal(t, resolveSensingConfig(context.Background(), sensingTestCmd(), nil), cfg,
		"a config full of retired knobs resolves identically to an empty one")
}

// --- the scan rotation ---------------------------------------------------------

// The yard cadence is a KNOB, so the coordinator stamps it onto the rotation
// rather than the adapter inventing one. Market slots never carry it: their
// pacing is the spread weighting's job.
func TestScanRotation_YardCadenceIsStampedFromConfig(t *testing.T) {
	world := steadyWorld(t, nil)
	cfg := resolveSensingConfig(context.Background(), world.cmd, liveconfig.Snapshot{
		"quartermaster_cadence_secs": 900,
	})

	stamped := world.handler.stampCadence([]parkedsensing.SensingSlotView{
		{Waypoint: "X1-A-Y1", Kind: parkedsensing.SlotKindYard},
		{Waypoint: "X1-A-M1", Kind: parkedsensing.SlotKindMarket},
	}, cfg)

	require.Equal(t, 900*time.Second, stamped[0].YardCadence, "a yard carries the tuned cadence floor")
	require.Zero(t, stamped[1].YardCadence, "a market's pacing is the weighting's, not a cadence")
}

// The rotation is refreshed from the ledger every tick, at the floored pacer
// rate — that call is what keeps a restarted pacer scanning.
func TestReconcile_RefreshesTheScanRotation(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.ledger.slots[psSlotKey{"X1-IN1-M1", parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M1", System: "X1-IN1",
		Kind: parkedsensing.SlotKindMarket, State: parkedsensing.SlotStateParked,
		AssignedShip: "PROBE-1",
	}
	world.ledger.goods["X1-IN1-M1"] = []string{"FOOD"}

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	scanner := world.handler.scanners[world.cmd.ContainerID]
	require.NotNil(t, scanner, "the reconcile builds the container's rotation on first use")
	members, rate := scanner.RotationSize()
	require.Equal(t, 1, members, "the parked placement joins the rotation")
	require.Positive(t, rate, "and the rotation is paced at the rate this tick computed")
}

// --- metrics --------------------------------------------------------------------

// The gauges are published every tick, and the slot census reports every state
// INCLUDING the empty ones — a gauge that stopped reporting a drained state
// would leave its last non-zero value standing until the series went stale.
func TestMetrics_PublishesRateStalenessAndSlots(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	now := time.Now()
	world.handler.clock = &shared.MockClock{CurrentTime: now}
	for i, age := range []time.Duration{time.Minute, 10 * time.Minute, time.Hour} {
		waypoint := "X1-IN1-M" + string(rune('1'+i))
		world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
			Waypoint: waypoint, System: "X1-IN1",
			Kind: parkedsensing.SlotKindMarket, State: parkedsensing.SlotStateParked,
			AssignedShip: "PROBE-" + string(rune('1'+i)),
		}
		world.ledger.views[waypoint] = parkedsensing.SensingSlotView{LastScan: now.Add(-age)}
	}

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Len(t, world.recorder.rate, 1, "the pacer rate is published once per tick")
	require.InDelta(t, 60.0, world.recorder.staleness[stalenessTierHot], 1, "p10 is the freshest slot")
	require.InDelta(t, 3600.0, world.recorder.staleness[stalenessTierCold], 1, "p90 is the stalest")
	require.Equal(t, 3, world.recorder.slots[parkedsensing.SlotStateParked])
	require.Equal(t, 0, world.recorder.slots[parkedsensing.SlotStateQueued],
		"an empty state is republished as zero, never left to go stale")
}

// A slot that has never been scanned has no meaningful age. Folding it in as an
// unbounded one would peg the cold tier at the process's uptime and make the
// gauge unreadable for exactly as long as the rotation is warming up.
func TestMetrics_NeverScannedSlotsAreExcludedNotClamped(t *testing.T) {
	now := time.Now()
	hot, median, cold, ok := stalenessPercentiles([]parkedsensing.SensingSlotView{
		{Waypoint: "A", LastScan: now.Add(-30 * time.Second)},
		{Waypoint: "B"}, // never scanned
		{Waypoint: "C", LastScan: now.Add(-90 * time.Second)},
	}, now)

	require.True(t, ok)
	require.InDelta(t, 30.0, hot, 1)
	require.InDelta(t, 30.0, median, 1)
	require.InDelta(t, 90.0, cold, 1, "the never-scanned slot did not become the tail")

	_, _, _, none := stalenessPercentiles([]parkedsensing.SensingSlotView{{Waypoint: "A"}}, now)
	require.False(t, none, "with nothing measured the tiers are not published at all")
}

// --- error handling -------------------------------------------------------------

// One failing stage does not abort the tick. A reconcile that could not read the
// treasury must still advance the hulls already flying and still refresh the
// scan rotation — aborting on the first error would let one unreadable port
// dark the whole fleet's market data.
func TestReconcile_OneFailingStageDoesNotDarkTheRest(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.ledger.slots[psSlotKey{"X1-IN1-M1", parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M1", System: "X1-IN1",
		Kind: parkedsensing.SlotKindMarket, State: parkedsensing.SlotStateWanted,
	}
	world.ledger.slots[psSlotKey{"X1-IN1-M2", parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M2", System: "X1-IN1",
		Kind: parkedsensing.SlotKindMarket, State: parkedsensing.SlotStateParked,
		AssignedShip: "PROBE-1",
	}
	base := world.handler.newPorts
	world.handler.SetEnginePortsFactory(func(playerID int) SensingEnginePorts {
		ports := base(playerID)
		ports.Treasury = &psTreasury{calls: world.calls, err: errors.New("credits unreadable")}
		return ports
	})

	err := world.handler.ReconcileOnce(world.ctx, world.cmd)

	require.Error(t, err, "the failure is surfaced, never swallowed")
	require.Contains(t, err.Error(), "treasury unreadable")
	scanner := world.handler.scanners[world.cmd.ContainerID]
	require.NotNil(t, scanner)
	members, _ := scanner.RotationSize()
	require.Equal(t, 1, members,
		"the rotation was still refreshed despite the buy queue failing — and holds only the PARKED slot")
	require.Len(t, world.recorder.rate, 1, "and the gauges were still published")
}

// An unreadable ledger is different: every stage below reads from it, so there
// is nothing to salvage and the tick stops at the top.
func TestReconcile_UnreadableLedgerStopsTheTick(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.ledger.systemsErr = errors.New("database down")

	err := world.handler.ReconcileOnce(world.ctx, world.cmd)

	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to read the sensing ledger")
	require.Empty(t, world.recorder.rate, "nothing is published from a tick that never ran")
}

// The heartbeat is emitted on every tick, including one that did nothing, so a
// quiet fleet is visibly quiet rather than silently dead.
func TestReconcile_EmitsTheCycleHeartbeat(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	recorder := &capturingLogger{}
	ctx := common.WithLogger(world.ctx, recorder)

	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))

	require.True(t, recorder.sawAction("parked_sensing_cycle"),
		"every tick reports what it did under the parked_sensing_cycle action")
}

// capturingLogger records the structured actions a tick logged, and the payload
// each one carried.
//
// Mutex-guarded because the pacer goroutine logs through the same logger the
// test asserts on — its death notice comes from off the reconcile path, which is
// the whole point of that line.
type capturingLogger struct {
	mu      sync.Mutex
	actions []string
	// payloads holds the last structured payload logged under each action. The
	// heartbeat is what the wake model actually reads, so a field it fails to
	// carry is a number nobody ever sees.
	payloads map[string]map[string]interface{}
}

func (c *capturingLogger) Log(_ string, _ string, fields map[string]interface{}) {
	if action, ok := fields["action"].(string); ok {
		c.mu.Lock()
		c.actions = append(c.actions, action)
		if c.payloads == nil {
			c.payloads = map[string]map[string]interface{}{}
		}
		c.payloads[action] = fields
		c.mu.Unlock()
	}
}

// payload returns the last payload logged under an action, or nil.
func (c *capturingLogger) payload(action string) map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.payloads[action]
}

func (c *capturingLogger) sawAction(want string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, action := range c.actions {
		if action == want {
			return true
		}
	}
	return false
}

// --- pacer lifecycle -------------------------------------------------------------

// countingPacer stands in for Scanner.RunPacer, which in production returns only
// on context cancellation and so cannot be made to exit by a test.
type countingPacer struct {
	mu      sync.Mutex
	release chan struct{} // closed/sent to make a running pacer return
	// starts signals each launch AS IT HAPPENS. Counting alone is not enough:
	// the launch is a goroutine, so a bare count read straight after a reconcile
	// races the scheduler and can report 0 for a pacer that is about to run —
	// a false RED. Receiving proves a launch happened; a receive that TIMES OUT
	// proves one did not.
	starts chan struct{}
}

func newCountingPacer() *countingPacer {
	return &countingPacer{release: make(chan struct{}), starts: make(chan struct{}, 8)}
}

func (c *countingPacer) run(ctx context.Context, _ *parkedsensing.Scanner) {
	c.starts <- struct{}{}
	c.mu.Lock()
	release := c.release
	c.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-release:
	}
}

// awaitStart blocks until one pacer launches, failing the test if none does.
func (c *countingPacer) awaitStart(t *testing.T) {
	t.Helper()
	select {
	case <-c.starts:
	case <-time.After(2 * time.Second):
		t.Fatal("no pacer was launched")
	}
}

// requireNoFurtherStart proves no ADDITIONAL pacer launches within the window —
// the negative half, which a counter read cannot establish.
func (c *countingPacer) requireNoFurtherStart(t *testing.T) {
	t.Helper()
	select {
	case <-c.starts:
		t.Fatal("a second pacer was launched onto the same rotation")
	case <-time.After(200 * time.Millisecond):
	}
}

// rearm replaces the release channel so a relaunched pacer blocks again.
func (c *countingPacer) rearm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.release = make(chan struct{})
}

// ONE pacer per container, however many times the coordinator is entered.
//
// The container runner re-sends the SAME command — same container id, same
// uncancelled context — after an error or a panic, up to MaxRestartAttempts. A
// pacer launched unconditionally would therefore be launched again on every
// retry, and two pacers popping one heap issue scans at twice the rate the
// budget arithmetic computed. Nothing would report it: the heartbeat publishes
// the rate HANDED to the rotation, not the rate being spent, so the fleet would
// quietly overrun its share of the rate limiter.
func TestPacer_RepeatedEntryStartsExactlyOne(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	pacer := newCountingPacer()
	world.handler.runPacer = pacer.run

	// Four reconciles for one container id — the restart ceiling plus the
	// original entry.
	for i := 0; i < 4; i++ {
		require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	}

	pacer.awaitStart(t)
	pacer.requireNoFurtherStart(t)
	require.True(t, world.handler.pacerLive(world.cmd.ContainerID),
		"a re-entered coordinator holds exactly one pacer on its rotation")
}

// A pacer that DIES is relaunched by the next tick, loudly.
//
// The panic guard around the pacer suppresses and returns rather than
// restarting, so without this a single panic stops all parked-market scanning
// for the life of the container — while every heartbeat still reports a healthy
// computed rate. The failure would surface only as market data ageing without
// bound, hours later, on the staleness gauge.
func TestPacer_DeathIsRelaunchedNextTickAndReportedLoudly(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	pacer := newCountingPacer()
	world.handler.runPacer = pacer.run
	logger := &capturingLogger{}
	ctx := common.WithLogger(world.ctx, logger)

	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))
	pacer.awaitStart(t)

	// The pacer returns while the coordinator is still meant to be running.
	//
	// Waited on the LOG rather than the slot: the dying goroutine releases its
	// slot first and logs second, so waiting on the slot can outrun the line this
	// test is actually about. Once the log is observed the release has provably
	// already happened, which makes the second assertion ordered rather than racy.
	close(pacer.release)
	require.Eventually(t, func() bool { return logger.sawAction("parked_sensing_pacer_died") },
		2*time.Second, 5*time.Millisecond,
		"a pacer dying under a live context is reported, not inferred from a staleness gauge hours later")
	require.False(t, world.handler.pacerLive(world.cmd.ContainerID), "the dead pacer released its slot")

	// The next tick brings it back — a one-tick outage rather than a permanent one.
	pacer.rearm()
	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))
	pacer.awaitStart(t)
	require.True(t, world.handler.pacerLive(world.cmd.ContainerID), "the next reconcile relaunches the pacer")
}

// An ordinary shutdown is silent. The pacer stopping because its context was
// cancelled is correct behaviour, and reporting it would train the reader to
// ignore the line that matters.
func TestPacer_ShutdownIsNotReportedAsDeath(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	pacer := newCountingPacer()
	world.handler.runPacer = pacer.run
	logger := &capturingLogger{}
	ctx, cancel := context.WithCancel(world.ctx)
	ctx = common.WithLogger(ctx, logger)

	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))
	pacer.awaitStart(t)
	cancel()

	require.Eventually(t, func() bool { return !world.handler.pacerLive(world.cmd.ContainerID) },
		2*time.Second, 5*time.Millisecond)
	require.False(t, logger.sawAction("parked_sensing_pacer_died"),
		"a cancelled context is a shutdown, not a failure")
}

// --- the stranded-claim reaper --------------------------------------------------

// strandedClaim records one MARKET placement left QUEUED in the given system.
func strandedClaim(world *cutoverWorld, system string) string {
	waypoint := system + "-M1"
	world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: waypoint, System: system, Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateQueued, PurchaseYard: system + "-Y1",
	}
	return waypoint
}

// The ledger events that identify each stage, assembled from the state constants
// rather than spelled out. Spelling them would be worse here than anywhere else
// in the suite: one of the assertions below is NEGATIVE, and a key that quietly
// stopped matching would make "the reaper left this claim alone" pass by never
// matching anything at all.
func reapWriteEvent(waypoint string) string {
	return "TransitionSlot:" + waypoint + ":" +
		parkedsensing.SlotStateQueued + "→" + parkedsensing.SlotStateWanted
}

func reapReadEvent() string { return "SlotsByState:" + parkedsensing.SlotStateQueued }

func drainReadEvent() string {
	return "SlotsByState:" + parkedsensing.SlotStateWanted + "," + parkedsensing.SlotStateQueued
}

// THE REAPER'S POSITION IN THE TICK IS THE CONTRACT, not an implementation
// detail, and both edges of it are load-bearing:
//
//   - AFTER the screening sweep, so a verdict written by THIS tick is honoured by
//     THIS tick. Running first would read a stale map and revert claims for
//     systems the sweep is about to restore to IN_SCOPE.
//   - BEFORE the drain, so a claim released this tick is a WANTED placement the
//     drain can work immediately rather than one that waits a full tick.
//
// Pinned through the ledger's own call sequence: SlotsByState is issued by four
// stages and only its state list tells them apart, so the single-state QUEUED
// read is unambiguously the reaper's and the WANTED+QUEUED read is the drain's.
func TestReconcile_ReapRunsBetweenTheSweepAndTheDrain(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-GONE": parkedsensing.VerdictNoWhitelist})
	waypoint := strandedClaim(world, "X1-GONE")

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	sweep := world.ledger.indexOf("SystemsByVerdict:" + parkedsensing.VerdictPending)
	reapRead := world.ledger.indexOf(reapReadEvent())
	reapWrite := world.ledger.indexOf(reapWriteEvent(waypoint))
	drainRead := world.ledger.indexOf(drainReadEvent())

	require.NotEqual(t, -1, sweep, "the screening sweep ran")
	require.NotEqual(t, -1, reapRead, "the reaper read the claimed placements")
	require.NotEqual(t, -1, reapWrite, "the stranded claim was released")
	require.NotEqual(t, -1, drainRead, "the buy queue ran")

	require.Greater(t, reapRead, sweep, "the reaper runs AFTER the screening sweep, on this tick's verdicts")
	require.Less(t, reapWrite, drainRead, "the reaper runs BEFORE the drain, so a released claim is workable this tick")

	slot := world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}]
	require.Equal(t, parkedsensing.SlotStateWanted, slot.State, "the claim is handed back")
	require.Empty(t, slot.PurchaseYard, "and the yard chosen for a system we no longer watch goes with it")
}

// A system the sweep RESTORES to IN_SCOPE on this very tick keeps its claim. This
// is the ordering above stated as behaviour: the reaper reads the verdict map
// after the sweep has written to it, so a placement whose system just came back
// is left for the drain — which is already reading QUEUED rows in IN_SCOPE
// systems and will simply retry the purchase.
func TestReconcile_VerdictRestoredThisTickIsNotReaped(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-BACK": parkedsensing.VerdictPending})
	world.goods.goods["X1-BACK-M1"] = []string{"FOOD"}
	waypoint := strandedClaim(world, "X1-BACK")

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Equal(t, parkedsensing.VerdictInScope, world.ledger.systems["X1-BACK"].Verdict,
		"the sweep restored the system on this tick")
	// The reaper RAN — otherwise "the claim survived" would be true for the
	// uninteresting reason that nothing ever looked at it.
	require.NotEqual(t, -1, world.ledger.indexOf(reapReadEvent()),
		"the reaper read the claimed placements, so declining to reap is a decision")
	require.Equal(t, -1, world.ledger.indexOf(reapWriteEvent(waypoint)),
		"a claim whose system came back IN_SCOPE this tick is the drain's to work, not the reaper's")
	require.Equal(t, parkedsensing.SlotStateQueued, world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindMarket}].State)
}

// Nobody watches this loop run, so a count the heartbeat does not carry is a
// number nobody ever sees. The reap counts sit with the buy_* fields because
// what they measure is the buy queue's own claims being handed back — buy_queued
// going up and buy_reaped bringing it down is one story, told in one place.
func TestReconcile_HeartbeatCarriesTheReapCounts(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-GONE": parkedsensing.VerdictNoWhitelist})
	strandedClaim(world, "X1-GONE")
	logger := &capturingLogger{}
	ctx := common.WithLogger(world.ctx, logger)

	require.NoError(t, world.handler.ReconcileOnce(ctx, world.cmd))

	payload := logger.payload("parked_sensing_cycle")
	require.NotNil(t, payload, "the tick emitted its cycle heartbeat")
	require.Equal(t, 1, payload["buy_reaped"], "the released claim is reported")
	require.Equal(t, 0, payload["buy_reap_skipped"], "and so is the contention count, at zero")
}

// --- the operator rescreen's engine-side effect (sp-j2efq) -----------------------

// WHY THE RESCREEN VERB HAS TO EXIST, and that re-opening the verdict is enough
// to change the answer.
//
// A verdict is stamped with the goods whitelist in force when it was written, and
// NO_WHITELIST is durable — the sweep re-screens PENDING and nothing else. So an
// operator who widens the whitelist gets no effect at all on the existing map:
// the systems the new list would accept are exactly the ones that will never be
// looked at again.
//
// The rescreen's write (sensing_systems.verdict → PENDING; pinned at the
// persistence and daemon layers) is applied here directly, because what is under
// test is the CONSEQUENCE: that the sweep then re-judges the system and can reach
// a DIFFERENT verdict than the one it holds.
func TestReconcile_RescreenLetsAWidenedWhitelistReopenASystem(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-ORE": parkedsensing.VerdictNoWhitelist})
	// The market deals in URANITE, which the whitelist in force did not want.
	world.goods.goods["X1-ORE-M1"] = []string{"URANITE"}
	world.cmd.GoodsWhitelist = []string{"FOOD"}

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Equal(t, parkedsensing.VerdictNoWhitelist, world.ledger.systems["X1-ORE"].Verdict,
		"a decided system is not re-screened, which is the whole point of the durable verdict")

	// The operator widens the whitelist in config.yaml — and on its own that
	// changes NOTHING about the map already judged.
	world.cmd.GoodsWhitelist = []string{"FOOD", "URANITE"}
	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Equal(t, parkedsensing.VerdictNoWhitelist, world.ledger.systems["X1-ORE"].Verdict,
		"editing the whitelist alone does not re-open a system already written off")

	// The rescreen verb's write, applied directly.
	reopened := world.ledger.systems["X1-ORE"]
	reopened.Verdict = parkedsensing.VerdictPending
	world.ledger.systems["X1-ORE"] = reopened

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	require.Equal(t, parkedsensing.VerdictInScope, world.ledger.systems["X1-ORE"].Verdict,
		"re-opened, the sweep re-judges it under the CURRENT whitelist and reaches a different verdict")
}

// The other direction, which is the one that can strand hulls: a rotated
// whitelist that no longer wants what a system deals in must be able to reach
// NO_WHITELIST on a re-screen, rather than keeping a stale IN_SCOPE forever.
func TestReconcile_RescreenLetsARotatedWhitelistCloseASystem(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-CC3": parkedsensing.VerdictInScope})
	world.goods.goods["X1-CC3-M1"] = []string{"CLOTHING"}
	world.cmd.GoodsWhitelist = []string{"FOOD"} // CLOTHING has been rotated out

	reopened := world.ledger.systems["X1-CC3"]
	reopened.Verdict = parkedsensing.VerdictPending
	world.ledger.systems["X1-CC3"] = reopened

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Equal(t, parkedsensing.VerdictNoWhitelist, world.ledger.systems["X1-CC3"].Verdict,
		"a system whose goods are no longer wanted is closed on the re-screen")
}

package commands

// The probe-sensing coordinator is the fleet's ONE budgeted sensing loop. Its
// model is PARKED probes: a hull is bought for a waypoint, flown there once, and
// then stands still forever, scanning its own market on a rotation the
// coordinator paces against whatever API headroom the rest of the fleet leaves.
// Nothing tours. Steady-state sensing therefore costs navigation nothing at all
// — the only recurring spend is the scans themselves, which is the quantity this
// loop actually sizes.
//
// The coordinator owns no algorithm of its own. Every decision belongs to an
// engine in internal/application/parkedsensing, and this file is the composition
// root that gives each engine its ports, orders them within a tick, and reports
// what they did:
//
//	screen      — is this system worth watching, and which waypoints in it?
//	buy queue   — can we afford a hull for that placement, and buy it
//	placements  — fly the bought hulls out and stand them down on station
//	expansion   — push the frontier outward, and run charting seeds
//	scanner     — the single fleet-wide pacer that spends the scan budget
//
// The loop is idempotent and restart-safe (RULINGS #2): every decision is
// re-derived each tick from the durable sensing ledger (sensing_systems,
// sensing_slots) and the ships table. The only in-memory state is the scan
// rotation, which a restart rebuilds from the ledger on the first tick, and the
// emergency brake, which a restart resets to fully-released and which re-derives
// within a few ticks from live limiter pressure.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ParkedSensingRecorder is the metrics seam. Pure OBSERVATION (RULINGS #4): a
// recording miss must never touch a decision path, so every implementation is
// nil-safe and best-effort.
type ParkedSensingRecorder interface {
	RecordRate(playerID int, reqPerSec float64)
	RecordStaleness(playerID int, tier string, seconds float64)
	RecordSlots(playerID int, state string, count int)

	// The shipyard blind spot's three independently-failing halves. They are
	// published from the heartbeat rather than from publish() because publish()
	// sits inside the rotation-view branch and is skipped whenever that read
	// fails — and a tick that could not read the rotation is exactly the tick an
	// operator most needs the yard numbers for.
	RecordYardCatalogue(playerID int, state string, count int)
	RecordYardPresence(playerID int, outcome string, count int)
	RecordYardSlots(playerID int, stage string, count int)
}

// RunProbeSensingCoordinatorCommand launches the standing coordinator for a
// player. All knobs are launch-config keys (RULINGS #5); the zero value falls
// back to the documented default.
//
// The retired touring model's knobs are RETAINED as fields and IGNORED. They are
// not dead weight: restart recovery rebuilds this command from the persisted
// container config (RULINGS #2), and a config written by the old core still
// carries them. Removing the fields would not fail loudly — configReader.OptionalInt
// simply ignores keys it is not asked for — but keeping them documents that the
// old keys are known-and-inert rather than accidentally dropped.
type RunProbeSensingCoordinatorCommand struct {
	PlayerID    shared.PlayerID
	ContainerID string

	GoodsWhitelist []string
	TickSecs       int
	WaitLowMs      int
	WaitHighMs     int

	// ProbeCap is the hard ceiling on probe hulls the engine may own.
	ProbeCap int
	// ExpansionEnabled switches the expansion engine on or off, encoded
	// 1=on / 2=off. See defaultExpansionEnabled for why it is not 0/1.
	ExpansionEnabled int
	// TargetUtilPct is the share of the rate-limiter ceiling the fleet aims at.
	TargetUtilPct int
	// MinScanRateMilli is the pacer's floor, in thousandths of a request/sec.
	MinScanRateMilli int
	// ValueClampR bounds how much more attention the hottest market may earn
	// than the baseline. 1 flattens the weighting entirely.
	ValueClampR int
	// InflightCap bounds concurrent scans.
	InflightCap int
	// CapitalMultiplierKMilli is how many MILLI-hours of cargo runway the buy
	// floor holds. 2000 = 2h, 400 = 0.4h.
	CapitalMultiplierKMilli int
	// CapexReserveCredits is the committed-capex reserve the buy floor adds.
	CapexReserveCredits int
	// QuartermasterCadence is a yard slot's re-read floor, in seconds.
	QuartermasterCadence int
	// SurgeInFlightCap bounds how many surplus probes may be flying toward
	// charted-but-unpriced systems at once (sp-zvywu). See defaultSurgeInFlightCap.
	SurgeInFlightCap int

	// --- retired: read by the old touring core, ignored by this one -----------

	DepthFloor               int64
	ProbeBudget              int
	SecondProbeThreshold     int
	PurchaseCooldownSecs     int
	FreshnessTargetSecs      int
	MaxSpendPerCycle         int
	SpendWindowSecs          int
	DiscoveryDeclaresPerTick int
}

// RunProbeSensingCoordinatorResponse reports reconcile progress. Because the
// loop is infinite it is only observed on context cancellation (shutdown).
type RunProbeSensingCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunProbeSensingCoordinatorHandler composes the parked-probe engines into one
// reconcile loop. It is a registered singleton (one instance serves every
// player's ticks), so the per-container scan rotation and emergency brake are
// held in maps behind a mutex.
type RunProbeSensingCoordinatorHandler struct {
	depthReader MarketDepthReader
	postRepo    SensingPostRepository
	fleetRepo   FleetReader
	pressure    domainScouting.PressureReader
	phase       expansionPhaseReader
	clock       shared.Clock

	// newPorts builds the engine's outbound surface for one player, wired as
	// one unit after construction (the codebase's optional-injection idiom).
	// An unwired coordinator holds every tick inert and says so.
	newPorts SensingEnginePortsFactory

	// liveConfig gives each tick a fresh view of the persisted container config,
	// so `spacetraders tune` takes effect on the NEXT tick rather than at the
	// next rebuild. A nil reader (or a failed snapshot) runs the tick on the
	// launch command — never a half-applied config.
	liveConfig liveconfig.Reader

	// captainEvents emits the coordinator error-loop event when a reconcile
	// pass fails with the identical error for DefaultStreakThreshold
	// consecutive ticks — under the wake model the captain event IS the
	// standing failure sensor. Optional-injection.
	captainEvents captain.EventRecorder

	// recorder publishes the sensing gauges. Optional; nil means metrics are off.
	recorder ParkedSensingRecorder

	// stall is the WRITE-ONLY stall-escalation seam (health.StallObserver): each tick reports
	// PROGRESS / IDLE / BLOCKED(reason) for the sensing pass and for the off-gate/expansion pass
	// separately, so a wedge stops looking identical to a quiet fleet. Its single method returns
	// nothing, so no sensing decision can read the streak it accumulates (RULINGS #2 — see
	// internal/application/health/stall.go).
	stall health.StallObserver

	// mu guards the per-container state against the singleton-handler
	// concurrency (many containers' ticks share one handler).
	mu sync.Mutex
	// scanners holds each container's scan rotation. Memory-only by design: the
	// rotation is rebuilt from the ledger by the first SyncMembership, and every
	// slot's last_scan_at survives in the ledger, so a restart resumes with its
	// pacing intact.
	scanners map[string]*parkedsensing.Scanner
	// brakes holds each container's emergency throttle. A restart resets it to
	// fully released, which re-derives within a few ticks from live pressure.
	brakes map[string]float64
	// cutoverDone latches the one-time cutover per container, so a ledger that
	// legitimately reads empty later (a fresh era) does not re-run the scout-post
	// retirement it has no rows to justify.
	cutoverDone map[string]bool
	// portsByPlayer memoises each player's engine surface.
	portsByPlayer map[int]SensingEnginePorts
	// pacersRunning records which containers currently have a live scan pacer.
	// It is what makes starting one IDEMPOTENT — see ensurePacer.
	pacersRunning map[string]bool

	// runPacer is the pacer's entry point, a seam only for tests: production
	// runs the scanner's own loop, and a test substitutes one it can make exit
	// on demand (the real loop returns only on ctx cancellation).
	runPacer func(ctx context.Context, scanner *parkedsensing.Scanner)
}

// NewRunProbeSensingCoordinatorHandler wires the coordinator. clock defaults to
// the real clock when nil (production). The engine ports are injected separately
// via SetEnginePortsFactory. phase is the EXPANSION gate — a REQUIRED guard,
// deliberately a constructor parameter rather than an optional setter: a nil
// reader holds the whole coordinator inert (surfaced loudly every tick), never
// silently open.
func NewRunProbeSensingCoordinatorHandler(
	depthReader MarketDepthReader,
	postRepo SensingPostRepository,
	fleetRepo FleetReader,
	pressure domainScouting.PressureReader,
	phase expansionPhaseReader,
	clock shared.Clock,
) *RunProbeSensingCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunProbeSensingCoordinatorHandler{
		depthReader:   depthReader,
		postRepo:      postRepo,
		fleetRepo:     fleetRepo,
		pressure:      pressure,
		phase:         phase,
		clock:         clock,
		scanners:      map[string]*parkedsensing.Scanner{},
		brakes:        map[string]float64{},
		cutoverDone:   map[string]bool{},
		portsByPlayer: map[int]SensingEnginePorts{},
		pacersRunning: map[string]bool{},
		runPacer:      func(ctx context.Context, scanner *parkedsensing.Scanner) { scanner.RunPacer(ctx) },
	}
}

// SetEnginePortsFactory wires the parked-probe engine's outbound surface, as a
// per-player factory. Leaving it unset holds every tick inert.
func (h *RunProbeSensingCoordinatorHandler) SetEnginePortsFactory(f SensingEnginePortsFactory) {
	h.newPorts = f
}

// portsFor resolves one player's engine surface, memoised so a tick does not
// rebuild a dozen adapters it built last tick. The factory is called at most
// once per player for the life of the process.
func (h *RunProbeSensingCoordinatorHandler) portsFor(playerID int) (SensingEnginePorts, bool) {
	if h.newPorts == nil {
		return SensingEnginePorts{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if ports, ok := h.portsByPlayer[playerID]; ok {
		return ports, true
	}
	ports := h.newPorts(playerID)
	h.portsByPlayer[playerID] = ports
	return ports, true
}

// SetLiveConfigReader wires the per-tick live view of the persisted container
// config. Leaving it unset keeps the loop launch-frozen.
func (h *RunProbeSensingCoordinatorHandler) SetLiveConfigReader(r liveconfig.Reader) {
	h.liveConfig = r
}

// SetEventRecorder wires the captain outbox for the reconcile error-loop event.
func (h *RunProbeSensingCoordinatorHandler) SetEventRecorder(rec captain.EventRecorder) {
	h.captainEvents = rec
}

// SetMetricsRecorder wires the sensing gauges. Observation only.
func (h *RunProbeSensingCoordinatorHandler) SetMetricsRecorder(rec ParkedSensingRecorder) {
	h.recorder = rec
}

// SetStallObserver wires the coordinator-stall escalation seam. Optional and nil-safe. The seam
// is write-only by type (its one method returns nothing), so wiring it cannot give any sensing
// decision something new to branch on.
func (h *RunProbeSensingCoordinatorHandler) SetStallObserver(o health.StallObserver) { h.stall = o }

// noteReconcile records one reconcile pass at the streak checkpoint: a nil err
// resets the streak; a non-nil err repeating identically for
// DefaultStreakThreshold passes emits the coordinator error-loop captain event.
// Edge-triggered and nil-safe on the recorder.
func (h *RunProbeSensingCoordinatorHandler) noteReconcile(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, errMon *health.Monitor, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if streak, crossed := errMon.Note("reconcile", msg); crossed {
		health.RecordErrorLoop(h.captainEvents, common.LoggerFromContext(ctx), cmd.ContainerID, cmd.PlayerID.Value(), "reconcile", err, streak)
	}
}

// Handle runs the reconcile loop until the context is cancelled, with the scan
// pacer running alongside it.
//
// The pacer is started ONCE, before the first tick, and lives for the whole
// container: it is a single long-lived rotation whose membership the reconcile
// refreshes, not a per-tick job. Cancelling ctx stops both — the pacer drains
// its in-flight workers on the way out.
func (h *RunProbeSensingCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunProbeSensingCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	cfg := resolveSensingConfig(ctx, cmd, h.liveSnapshot(ctx, cmd))
	result := &RunProbeSensingCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Parked-probe sensing coordinator starting (tick %s, probe cap %d, expansion spending %v — frontier discovery runs either way)", cfg.Tick, cfg.ProbeCap, cfg.ExpansionSpend), map[string]interface{}{
		"action":       "probe_sensing_start",
		"container_id": cmd.ContainerID,
	})

	// The pacer is NOT started here. It is started from the tick path
	// (ensurePacer), which is what makes it both single and self-healing — see
	// that function; the container runner can re-enter this Handle several times
	// for one container, and a pacer that dies takes the fleet's scanning with it.

	errMon := health.NewMonitor(health.DefaultStreakThreshold)

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		err := h.ReconcileOnce(ctx, cmd)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Parked-probe sensing reconcile failed: %v", err), nil)
		}
		h.noteReconcile(ctx, cmd, errMon, err)
		result.Ticks++

		// Re-resolved every tick so a tuned cadence takes effect on the next
		// sleep rather than at the next rebuild.
		cfg = resolveSensingConfig(ctx, cmd, h.liveSnapshot(ctx, cmd))
		select {
		case <-time.After(cfg.Tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// sensingCycle carries one reconcile tick's inputs: the command, the knobs resolved
// for this tick, and the per-player engine ports.
type sensingCycle struct {
	cmd   *RunProbeSensingCoordinatorCommand
	cfg   sensingConfig
	ports SensingEnginePorts
}

// ReconcileOnce is one reconcile pass — the unit the tests drive directly.
//
// The stages run in dependency order (screen plans placements, the queue fills
// them, the placement machine flies them, expansion finds the next system to
// screen) and every stage's failure is COLLECTED rather than fatal. A tick that
// cannot read the treasury must still advance the hulls already flying and must
// still refresh the scan rotation — aborting on the first error would let one
// unreadable port dark the whole fleet's market data.
func (h *RunProbeSensingCoordinatorHandler) ReconcileOnce(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand) error {
	// EXPANSION GATE. Parked sensing buys hulls and sizes its footprint against
	// the fleet's trading reach; before the home jump gate is built there is no
	// such reach, and bootstrap owns probe provisioning. Checked FIRST so a
	// pre-EXPANSION tick does nothing at all — no ledger read, no cutover, no buy.
	if !h.expansionReached(ctx, cmd) {
		// Correctly gated, not wedged: bootstrap owns probes before the home gate is built.
		// Reported as IDLE so this stays silent for as many ticks as it takes, and so a stall
		// streak from before the gate does not survive across the phase change.
		h.observeStall(ctx, cmd, sensingStallCoordinator, health.TickIdle())
		return nil
	}

	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID.Value()
	ports, built := h.portsFor(playerID)
	if !built || !ports.wired() {
		logger.Log("WARNING", "Parked-probe sensing held (fail-closed): the engine ports are not wired — a half-wired engine plans placements it can never fill", map[string]interface{}{
			"action": "parked_sensing_unwired",
		})
		// A permanent wedge that produces the same silence as a quiet fleet. The expansion key
		// is deliberately left untouched: the pass did not run, so this tick is no evidence
		// about it either way, and a frozen streak neither escalates nor falsely clears.
		h.observeStall(ctx, cmd, sensingStallCoordinator, health.TickBlocked(stallReasonPortsUnwired,
			"the parked-sensing engine surface is incomplete — the tick holds fail-closed and can never fill a placement"))
		return nil
	}

	cfg := resolveSensingConfig(ctx, cmd, h.liveSnapshot(ctx, cmd))
	cyc := sensingCycle{cmd: cmd, cfg: cfg, ports: ports}

	// Started here rather than in Handle, and re-checked on every tick: the
	// container runner can enter Handle several times for one container, and a
	// pacer that dies would otherwise take all parked-market scanning with it
	// while the heartbeat still looked healthy.
	h.ensurePacer(ctx, cyc)

	// The budget is recomputed FIRST: expansion gates on the sensing residual and
	// the pacer runs at the floored rate, so both engines below need this tick's
	// numbers rather than the last tick's.
	budget := h.budgetInputs(cyc)
	sensingRate := domainSensing.SensingRate(budget)
	pacerRate := domainSensing.PacerRate(budget)

	systems, err := ports.Ledger.Systems(ctx, playerID)
	if err != nil {
		// The tick cannot even see its own world. Reported before the return so an unreadable
		// ledger escalates instead of presenting as a fleet with nothing to do.
		h.observeStall(ctx, cmd, sensingStallCoordinator, health.TickBlocked(stallReasonLedgerUnreadable, err.Error()))
		return fmt.Errorf("failed to read the sensing ledger: %w", err)
	}

	var failures []error
	cutover, rotation := 0, 0
	// cutoverPending is the cutover's own trigger, named because the adoption
	// retry below gates on it too: while the cutover has not finished, adopting
	// orphans is still ITS job.
	cutoverPending := len(systems) == 0 && !h.cutoverAlreadyDone(cmd.ContainerID)
	if cutoverPending {
		count, cerr := h.cutover(ctx, cyc)
		cutover = count
		if cerr != nil {
			// A partial cutover is durable and resumable — every write it made
			// stands — so the tick continues and the next one finishes the job.
			failures = append(failures, cerr)
		}
	}

	screened, serr := h.screenSweep(ctx, cyc)
	if serr != nil {
		failures = append(failures, serr)
	}

	// The free shipyard-catalogue sweep, run on EVERY tick and gated on nothing.
	//
	// It sits outside the screen sweep deliberately. Screening only ever revisits
	// PENDING systems (an IN_SCOPE one is never re-screened, by design), so a yard
	// in a system we have already judged would never be reached from there — and
	// that is most of the map. This pass asks about yards, not systems, and its
	// work list shrinks to nothing on its own as the reads land.
	//
	// AFTER the screen so a system charted by this tick's sweep already has its
	// waypoint rows, and its yards are enumerable on this same tick rather than the
	// next one. BEFORE the drain because the drain's yard lookup reads the very
	// rows this pass writes.
	yardRep, yerr := parkedsensing.ReadYardCatalogues(ctx, ports.yardCatalogPorts(), playerID)
	if yerr != nil {
		failures = append(failures, yerr)
	}

	// The paid half of the same discovery, and it runs IMMEDIATELY AFTER the free
	// one for a reason the two passes' contract makes exact. A yard only becomes a
	// presence candidate once its catalogue shows it sells something wanted
	// (yardscan.WantsPresence refuses an Unknown yard), so a catalogue landing on
	// this tick makes its yard eligible on this same tick rather than the next.
	// The reverse order would work too, one tick slower, and there is no reason to
	// pay that.
	//
	// BEFORE the reaper and the drain, so a market this pass releases back to
	// WANTED is a placement they see on the tick it is released — the reaper does
	// not treat it as stranded and the drain may re-cover it immediately.
	presenceRep, perr := parkedsensing.DispatchYardPresence(ctx, ports.yardPresencePorts(h.postRepo), playerID)
	if perr != nil {
		failures = append(failures, perr)
	}

	// BETWEEN THE SWEEP AND THE DRAIN, and both edges are the contract.
	//
	// After the sweep, so a verdict written by THIS tick is honoured by it: a
	// system the sweep has just restored to IN_SCOPE keeps its claim, which the
	// drain then re-works. Running the reaper first would read a stale map and
	// revert claims the sweep was about to justify.
	//
	// Before the drain, so a claim released this tick is a WANTED placement the
	// drain can fill immediately rather than one that waits a whole tick.
	//
	// The reaper re-reads the system table itself, and that RE-READ IS THE POINT.
	// Do not pass it the `systems` slice loaded at the top of this tick, however
	// much that looks like an obvious de-dup of a full-table read: that read
	// happens BEFORE the screening sweep, so reusing it would hand the reaper a
	// pre-sweep verdict map and silently restore exactly the stale-verdict
	// reaping the ordering above exists to prevent.
	reapRep, rerr := parkedsensing.ReapStrandedClaims(ctx, ports.reapPorts(), playerID, 0)
	if rerr != nil {
		failures = append(failures, rerr)
	}

	adopted, dispatchedOrphans, surged := h.reclaimIdleProbes(ctx, cyc, systems, cutoverPending, &failures)

	buyRep, berr := parkedsensing.DrainBuyQueue(ctx, ports.buyPorts(cmd.ContainerID, h.postRepo), playerID, buyKnobs(cfg), h.clock)
	if berr != nil {
		failures = append(failures, berr)
	}

	placeRep, perr := parkedsensing.AdvancePlacements(ctx, ports.placementPorts(), playerID, 0)
	if perr != nil {
		failures = append(failures, perr)
	}

	// Expansion is gated on the SENSING residual, never the pacer rate: the
	// emergency brake can legitimately drive the residual below the minimum scan
	// rate, and the pacer re-imposes that floor — so gating on the pacer rate
	// would make the brake invisible here and leave expansion charting away at
	// full tilt through a rate-limit storm (budget.go:82-116).
	expandRep, eerr := parkedsensing.AdvanceExpansion(ctx, ports.expandPorts(playerID, cfg.Whitelist), playerID, parkedsensing.ExpandKnobs{
		SpendEnabled:  cfg.ExpansionSpend,
		MinBudgetRate: float64(cfg.MinScanRateMilli) / 1000.0,
		Whitelist:     cfg.Whitelist,
	}, sensingRate)
	if eerr != nil {
		failures = append(failures, eerr)
	}

	rotation = h.syncScanRotation(ctx, cyc, pacerRate, &failures)

	h.heartbeat(ctx, cmd, cfg, heartbeat{
		sensingRate: sensingRate,
		pacerRate:   pacerRate,
		brake:       budget.BrakeFactor,
		cutover:     cutover,
		screened:    screened,
		adopted:     adopted,
		dispatched:  dispatchedOrphans,
		surged:      surged,
		reap:        reapRep,
		buy:         buyRep,
		place:       placeRep,
		expand:      expandRep,
		rotation:    rotation,
		yard:        yardRep,
		presence:    presenceRep,
	})

	// The tick's three-way verdict, on the two keys that stall independently. Reported ONCE per
	// tick each — the streak IS the tick count — and derived purely from tallies this tick
	// already produced, so nothing here can influence what the tick did.
	h.observeStall(ctx, cmd, sensingStallCoordinator, sensingTickVerdict(sensingTickTally{
		cutover:    cutover,
		screened:   screened,
		adopted:    adopted,
		dispatched: dispatchedOrphans,
		surged:     surged,
		rotation:   rotation,
		reap:       reapRep,
		buy:        buyRep,
		place:      placeRep,
		expand:     expandRep,
		failures:   len(failures),
	}))
	h.observeStall(ctx, cmd, expansionStallCoordinator, expansionStallVerdict(expandRep, eerr))

	return errors.Join(failures...)
}

// reclaimIdleProbes puts hulls we already own to work — adopt, dispatch, surge — before the
// drain buys any. Each pass is the reaper's sibling and lands BEFORE the drain for the same
// economic reason: a placement filled from an owned hull is a placement the drain does NOT
// buy for (RULINGS #4).
//
// GATED ON THE CUTOVER, NOT THE PHASE. Before the cutover a scout-tagged hull is not an
// orphan at all — the scout posts still stand and the scout-post coordinator is actively
// manning them from its idle pool — so a pass running then would take hulls out from under a
// LIVE coordinator. The cutover is precisely the event that turns "scout-tagged hull" into
// "orphan we failed to adopt", and while it is still pending (including while it retries
// after a failure) adoption remains its job.
func (h *RunProbeSensingCoordinatorHandler) reclaimIdleProbes(ctx context.Context, cyc sensingCycle, systems []parkedsensing.ExpandSystem, cutoverPending bool, failures *[]error) (adopted, dispatched, surged int) {
	if cutoverPending {
		return 0, 0, 0
	}
	adopted = h.adoptStrandedProbes(ctx, cyc, failures)
	// AFTER ADOPTION: adoption's in-place fill costs a row write and no movement, so a hull it
	// can absorb WHERE IT STANDS is absorbed there rather than flown somewhere. The dispatch
	// re-reads the ledger, so everything adoption just did is visible to it.
	dispatched = h.dispatchIdleOrphans(ctx, cyc, failures)
	// THE SENSING SURGE (sp-zvywu), and its position is three separate arguments.
	//
	// AFTER THE ORPHAN DISPATCH, because that pass fills placements THE SCREEN ALREADY DECLARED
	// and this one declares new ones. A hull the dispatch can berth at an existing WANTED row
	// needs no new placement written for it, so surging first would spend it on a fresh row and
	// leave a declared placement standing empty.
	//
	// A surplus probe is named by NO slot row, which is exactly why the probe cap cannot see it
	// — an UNDER-count, the direction that authorises buying a replacement for a hull we
	// already own. The surge's write names it, so the cap counts it, so the drain buys FEWER.
	//
	// `systems` is the tick's own ledger read, passed rather than re-read for ONE narrow use:
	// which hulls are out on a charting errand. That is safe where reusing it for a VERDICT
	// would not be (see the reaper's note) — SetSeed and UpsertSystem own disjoint column sets
	// in the persistence layer, so the screening sweep between the read and here cannot have
	// touched a seed column.
	surged = h.surgeToUnpricedSystems(ctx, cyc, systems, failures)
	return adopted, dispatched, surged
}

// syncScanRotation refreshes the pacer's membership from this tick's slot views and reports
// the resulting rotation size. The pacer runs at the FLOORED rate, so market data never goes
// fully dark however hard the brake bites.
func (h *RunProbeSensingCoordinatorHandler) syncScanRotation(ctx context.Context, cyc sensingCycle, pacerRate float64, failures *[]error) int {
	playerID := cyc.cmd.PlayerID.Value()
	views, err := cyc.ports.Ledger.ParkedSlotViews(ctx, playerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to read the parked scan rotation: %w", err))
		return 0
	}
	scanner := h.scannerFor(cyc)
	scanner.SyncMembership(h.stampCadence(views, cyc.cfg), pacerRate)
	rotation, _ := scanner.RotationSize()
	h.publish(ctx, playerID, pacerRate, views, cyc.ports)
	return rotation
}

// budgetInputs takes this tick's reading of the shared API budget and advances
// the emergency brake from live limiter pressure.
//
// The brake is per-container and multiplicative, so it is advanced exactly once
// per tick — reading it twice would double-brake, and the scan pacer deliberately
// has no pressure port of its own for the same reason (scanner.go's ScanPorts).
func (h *RunProbeSensingCoordinatorHandler) budgetInputs(cyc sensingCycle) domainSensing.BudgetInputs {
	wait := time.Duration(0)
	if h.pressure != nil {
		wait = h.pressure.Current(h.clock.Now())
	}

	h.mu.Lock()
	brake := domainSensing.ApplyBrake(h.brakes[cyc.cmd.ContainerID], wait, cyc.cfg.WaitLow, cyc.cfg.WaitHigh)
	h.brakes[cyc.cmd.ContainerID] = brake
	h.mu.Unlock()

	return domainSensing.BudgetInputs{
		CeilingReqPerSec: cyc.ports.Budget.CeilingReqPerSec(),
		TargetUtilPct:    cyc.cfg.TargetUtilPct,
		MinScanRateMilli: cyc.cfg.MinScanRateMilli,
		NonSensingRate:   cyc.ports.Budget.NonSensingRate(budgetWindow),
		ChartingRate:     cyc.ports.Budget.ChartingRate(budgetWindow),
		BrakeFactor:      brake,
	}
}

// adoptOrphanProbes takes ownership of the probes the retired posts were
// manning, as parked SPARE slots where each hull already stands.
//
// A scout-tagged hull that no surviving post names is otherwise stranded: no
// coordinator drives it, and — the part that costs money — the probe cap counts
// hulls through the ledger, so an unrecorded probe makes the fleet read SMALLER
// than it is and authorises buying a replacement for a hull we already own.
// Adopting as SPARE also puts them straight to work: the buy queue prefers
// re-tasking a spare over buying, and expansion claims them as charting seeds.
func (h *RunProbeSensingCoordinatorHandler) adoptOrphanProbes(ctx context.Context, cyc sensingCycle, failures *[]error) int {
	logger := common.LoggerFromContext(ctx)
	playerID := cyc.cmd.PlayerID.Value()
	ships, err := h.fleetRepo.FindAllByPlayer(ctx, cyc.cmd.PlayerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to list the fleet for probe adoption: %w", err))
		return 0
	}

	posts, err := h.postRepo.ListActive(ctx, playerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to re-list scout posts for probe adoption: %w", err))
		return 0
	}
	manned := primaryMannedHulls(posts)

	// The SAME occupancy index the standing adoption retry uses
	// (probe_sensing_adoption.go), for the same reason and against the same
	// hazard — see the guard inside the loop. Seeded from the LEDGER rather than
	// built empty: this pass fires on a ledger the era scope reports as empty,
	// but rows can legitimately already exist within it, and a loop-local set
	// would only ever see what THIS loop wrote.
	holds, herr := ledgerHoldings(ctx, cyc.ports, playerID)
	if herr != nil {
		*failures = append(*failures, herr)
		return 0
	}

	adopted := 0
	for _, ship := range ships {
		if !ship.IsScoutType() || ship.DedicatedFleet() != freshnessScoutFleetTag {
			continue
		}
		hull := ship.ShipSymbol()
		if manned[hull] {
			continue // still manning the home post the cutover kept
		}
		location := ship.CurrentLocation()
		if location == nil || location.Symbol == "" {
			continue // a hull we cannot place is a hull we must not record
		}
		// THE OCCUPANCY GUARD. UpsertSpareSlot's conflict set carries
		// assigned_ship, so a write at a waypoint that already holds a SPARE row
		// does not fail — it silently re-points that row at this hull. The hull it
		// displaced then holds the sensing tag with NO row anywhere, which is
		// UNRECOVERABLE: every adoption filter skips sensing_parked-tagged hulls,
		// the spare re-task and the seed claim both read FROM the ledger, and
		// DockedProbeAt will use such a hull as a purchasing buyer but never writes
		// a row naming it. It is invisible to CountOwnedProbes for good, so the cap
		// under-reads and buys a replacement for a probe we own (RULINGS #4) — with
		// no error and a healthy heartbeat. Two co-located idle probes at the home
		// shipyard is all it takes, on the one irreversible tick this pass ever
		// runs.
		//
		// Skipping is the right answer: the displaced hull would be lost, while a
		// skipped one stays untagged, unrecorded and therefore still recoverable.
		//
		// STILL KIND-BLIND after the key widened (sp-dpfp8), and deliberately: this
		// is the ONE irreversible tick, so it keeps the widest guard available and
		// leaves every judgement call to the passes that retry. See occupiedAt.
		//
		// sp-0eufi softened one clause above: adoption's filter is now an ALLOWLIST that INCLUDES
		// sensing_parked, so a tagged-but-unrecorded hull is no longer unrecoverable — the standing
		// pass picks it up. The displacement is still not worth risking here, and this remains a
		// plain skip: this is the ONE irreversible tick, while the standing pass retries every tick
		// and, unlike this path, knows how to fill a hull-less placement in place.
		if holds.occupiedAt(location.Symbol) {
			continue
		}

		// RECORD BEFORE TAGGING, for the reason recordPurchase gives in the buy
		// queue: the two writes are ordered by what a failure between them costs.
		// Recorded-but-untagged leaves a hull the probe cap COUNTS (it just is
		// not yet claimed by this fleet), and the placement machine re-asserts
		// the tag idempotently the first time the spare is used. Tagged-but-
		// unrecorded is the opposite and is unrecoverable here: the tag makes the
		// hull skip the adoption filter on every retry, so it would stay
		// invisible to the cap forever and authorise buying a replacement for a
		// probe we already own.
		if uerr := cyc.ports.Ledger.UpsertSpareSlot(ctx, playerID, parkedsensing.SlotRecord{
			Waypoint:     location.Symbol,
			System:       location.SystemSymbol,
			Kind:         parkedsensing.SlotKindSpare,
			State:        parkedsensing.SlotStateParked,
			AssignedShip: hull,
		}); uerr != nil {
			*failures = append(*failures, fmt.Errorf("failed to adopt probe %s as a spare: %w", hull, uerr))
			continue
		}
		// The waypoint holds a SPARE row now, so a later co-located hull in this
		// same sweep is skipped rather than overwriting what was just written.
		// APPENDED, not assigned: any MARKET placement already indexed here is
		// still on the books and must stay visible to the rest of the sweep.
		holds.rows[location.Symbol] = append(holds.rows[location.Symbol], parkedsensing.QueuedSlot{
			Waypoint:     location.Symbol,
			System:       location.SystemSymbol,
			Kind:         parkedsensing.SlotKindSpare,
			State:        parkedsensing.SlotStateParked,
			AssignedShip: hull,
		})

		// The tag write is BEST-EFFORT, and deliberately not a failure of the
		// cutover. It is the one write here that is not money-load-bearing: the
		// probe cap counts ledger ROWS, and the row is already written, so a
		// missing tag costs nothing the cap can see. The placement machine
		// re-asserts it idempotently the first time the spare is used — the same
		// reasoning recordPurchase gives for warning on its own tag failure
		// rather than failing the purchase over it.
		//
		// Escalating it would be actively worse now that a failed cutover
		// re-runs from the top: a persistently failing tag write would re-run
		// the whole cutover on every tick, forever, over work that is already
		// correctly recorded.
		if terr := cyc.ports.Fleet.AssignFleet(ctx, playerID, hull, parkedsensing.SensingParkedFleetTag); terr != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"Adopted probe %s is recorded as a sensing spare but keeps its old fleet tag (the probe cap counts it; the placement machine re-tags it on first use): %v",
				hull, terr), map[string]interface{}{
				"action":      "parked_sensing_adopt_tag_failed",
				"ship_symbol": hull,
			})
		}
		adopted++
	}
	return adopted
}

// expansionReached reports whether the bootstrap-derived phase is EXPANSION,
// naming the honest no-work reason when it is not (never a silent stall).
// FAIL-CLOSED on every unverifiable input (RULINGS #4): an unwired reader
// (mis-wire) and a read error both hold the coordinator inert, each with its own
// loud line so the wedge is visible on the first look.
func (h *RunProbeSensingCoordinatorHandler) expansionReached(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand) bool {
	logger := common.LoggerFromContext(ctx)
	if h.phase == nil {
		logger.Log("WARNING", "Probe sensing held (fail-closed): no bootstrap-phase reader wired — the EXPANSION phase cannot be verified, and a gate never defaults open", map[string]interface{}{
			"action": "probe_sensing_phase_unreadable",
		})
		return false
	}
	inExpansion, err := h.phase.InExpansion(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Probe sensing held (fail-closed): bootstrap phase unreadable — no sensing on an unknown phase: %v", err), map[string]interface{}{
			"action": "probe_sensing_phase_unreadable",
			"error":  err.Error(),
		})
		return false
	}
	if !inExpansion {
		logger.Log("INFO", "Probe sensing deferred: bootstrap phase pre-EXPANSION (jump-gate construction incomplete — the world is still in DATA/INCOME/GATE); parked-probe sensing runs only in EXPANSION, bootstrap provisions probes until then", map[string]interface{}{
			"action": "probe_sensing_phase_deferred",
		})
		return false
	}
	return true
}

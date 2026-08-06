package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// BootstrapTunableDefaults maps every LIVE-tunable bootstrap knob to its documented
// default — the value that applies when the persisted config column carries no positive
// override. The daemon's tune bounds registry reads THIS map, so the default-of-record stays
// in this file next to the const it mirrors. The map's KEY SET is also the contract for which
// BARE keys resolveBootstrapConfig live-overlays.
//
// The cadence is the ONE thing an operator retunes at runtime: every other cold-start value is the
// shape of the seed itself, fixed in code. This key is the SEPARATE bare family — distinct from the
// config.yaml-authoritative prefixed bootstrap_* launch keys — so a tune is never cleared by the
// launch-config rebuild and survives a daemon bounce (RULINGS #2).
// bootstrapOperationType is the ledger label for everything a bootstrap tick spends.
const bootstrapOperationType = "bootstrap"

func BootstrapTunableDefaults() map[string]int {
	return map[string]int{
		"tick_secs": defaultBootstrapTickSeconds,
	}
}

// bootstrapRunConfig is the launch command with its cadence default resolved, so the reconcile logic
// never repeats the "<= 0 → default" fallback (the autosizer resolveConfig idiom).
type bootstrapRunConfig struct {
	Disabled bool
	DryRun   bool
	Tick     time.Duration
}

func resolveBootstrapConfig(cmd *RunBootstrapCoordinatorCommand, live liveconfig.Snapshot) bootstrapRunConfig {
	c := bootstrapRunConfig{
		Disabled: cmd.Disabled,
		Tick:     time.Duration(cmd.TickIntervalSecs) * time.Second,
	}

	// Live overlay: a `tune` writes a BARE positive key to the persisted config
	// column; the per-tick snapshot overlays it here so the change lands on the NEXT tick with no
	// restart. Only-when-present (NOT snapshot-authoritative like the freshsizer): bootstrap's
	// launch keys are the SEPARATE prefixed bootstrap_* family, so an untuned bare key is genuinely
	// absent and must not zero the launch value.
	if live != nil {
		if v := live.PositiveIntOrZero("tick_secs"); v > 0 {
			c.Tick = time.Duration(v) * time.Second
		}
	}

	if c.Tick <= 0 {
		c.Tick = defaultBootstrapTickSeconds * time.Second
	}
	return c
}

// reconcileResult tallies one tick's effect for the heartbeat and the tests.
type reconcileResult struct {
	Phase            Phase
	Purchased        int    // probes actually bought this tick (the scanning workstream)
	HomePostDeclared bool   // the home scout-post coverage target was ensured this tick
	Blocker          string // the one guard that blocked the highest-priority action (for the heartbeat)

	// Contract-workstream tallies (Slice 2).
	HaulersBought      int  // contract haulers actually bought this tick (staged: at most 1)
	FrigateRetired     bool // the command frigate was retired from contract work this tick
	ContractRun        bool // batch-contract was launched this tick
	FrigateLoopStarted bool // the command frigate's continuous contract loop was started this tick
	FrigatePivoted     bool // the first-hauler pivot fired this tick: frigate loop STOPPED + dedicated the exclusive purchasing ship. With a readable yard price the buy also runs this tick; on a COLD price it is a SEPARATE later tick once the freed frigate is positioned (fault-2)
	PurchaserReleased  bool // the pivot's purchasing dedication was cleared this tick, handing a stranded frigate back to earning (the buy it was freed for had moved out of reach)
	TradeHullSeeded    bool // the cold-start hull-routing trade-seed fired this tick: acquisition #2 bought + dedicated to the trade fleet + the trade coordinator ensured
	PlacementSlots     int  // fixed delivery slots this era resolves — where the ramp spreads its hulls (for the heartbeat)

	// GATE tallies.
	ConstructionStartRan bool // `construction start` ran this tick (created/resumed the pipeline)
	MfgEnsured           bool // the manufacturing coordinator (executor) was ensured-running this tick
	MfgBounced           bool // the executor was bounced for pipeline adoption this tick (captain L57)
	WorkersReleased      int  // gate workers un-dedicated to the idle pool this tick (surplus re-balance; the repurpose seam is dormant)
	GateWorkersBought    int  // gate-worker hulls actually bought this tick (staged: at most 1)
	DesiredWorkers       int  // the tick's gate-worker sizing target (for the heartbeat)

	// COMPLETE tallies.
	HandoffLaunched          bool // the autosizer + standing coordinators were launched this tick (the hand-off)
	ConstructionHullsToTrade int  // gate construction hulls re-dedicated to the TRADE fleet this tick: the gate is built, so its workers stop earning until they are put back to work
	Done                     bool // terminal: COMPLETE reached and handed off — the reconcile loop may exit

	// The fleet autosizer was launched EARLY this tick (armed cold-start scaling). Test-only
	// observability — deliberately NOT in the heartbeat delta (keeping the flag-off log byte-identical);
	// the early launch surfaces its own INFO line, mirroring how the deferral does.
	AutosizerLaunchedEarly bool

	// The dedicated contract auto-scaler was ensured this tick (unconditional in the cold-start window).
	// Same test-only observability as AutosizerLaunchedEarly.
	ContractScalerLaunchedEarly bool
}

// probeBuyBridge closes the sync-lag window between a probe purchase and the ship-count observation
// reflecting it. The observed count LAGS a fresh buy — the count query does not see
// just-bought hulls until a later sync — so at a SHORT reconcile tick the next tick would read the
// stale low count and re-buy toward a target it already reached (over-buy → wasted capital). This
// tracks the probes THIS coordinator bought but the observation has not yet confirmed (pending), folds
// them into the effective count the buy gate reads, and DECAYS pending as the observation catches up so
// the effective count converges to the true count (a genuinely lost hull is still replaced — the bridge
// never wedges a legitimate re-buy). It bridges only the sync window; it is not a persisted progress
// cursor.
type probeBuyBridge struct {
	pending      int // probes bought that the observed count has not yet reflected
	lastObserved int // the raw observed ProbeCount at the previous tick — drives the decay
}

// effectiveProbeCount folds still-unobserved buys into the raw observed count and retires (decays) the
// pending tally by however many buys the observation has now absorbed since the last tick. Called once
// per readable tick, before the buy gate. A rise in the observed count only ever REDUCES pending; a
// drop (a lost hull) leaves pending untouched, so the effective count falls and the lost hull is bought
// again. When another actor also raised the count the bridge is only OVER-eager to decay (it would buy,
// not over-buy), which keeps the money-safety bias one-directional — never a re-buy past target.
func (b *probeBuyBridge) effectiveProbeCount(observed int) int {
	if observed > b.lastObserved {
		if absorbed := observed - b.lastObserved; absorbed >= b.pending {
			b.pending = 0
		} else {
			b.pending -= absorbed
		}
	}
	b.lastObserved = observed
	return observed + b.pending
}

// recordProbeBuys adds probes bought THIS tick to the pending tally, so the next tick counts them
// against target before the observation reflects them. A no-op for a zero/negative delta (GATE/EXPANSION
// ticks and dry-runs buy nothing).
func (b *probeBuyBridge) recordProbeBuys(n int) {
	if n > 0 {
		b.pending += n
	}
}

// probeBridge returns the per-container count-sync bridge, lazily created. Keyed by
// ContainerID because this handler is a REGISTERED SINGLETON serving every bootstrap container: a bare
// field would be shared and RACED across concurrent players. One container's ticks run sequentially
// (the Handle loop awaits each reconcile), so the returned *probeBuyBridge is only ever touched by a
// single goroutine — the mutex guards the map, not the returned struct.
func (h *RunBootstrapCoordinatorHandler) probeBridge(containerID string) *probeBuyBridge {
	h.buyBridgeMu.Lock()
	defer h.buyBridgeMu.Unlock()
	if h.buyBridges == nil {
		h.buyBridges = map[string]*probeBuyBridge{}
	}
	b := h.buyBridges[containerID]
	if b == nil {
		b = &probeBuyBridge{}
		h.buyBridges[containerID] = b
	}
	return b
}

// reconcileOnce runs one full pass: phantom-cache refresh → observe → derive phase → act on the
// delta → heartbeat. It is the unit the tests drive directly; Handle just calls it on the tick.
// Every side-effecting step is guarded "already done / in-flight?" and fails CLOSED on an
// unreadable input, so re-evaluation (including the first tick after a restart) never double-acts.
func (h *RunBootstrapCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunBootstrapCoordinatorCommand) (reconcileResult, error) {
	// Stamp the tick, not the individual flights inside it. The ports this drives are
	// registered singletons shared by every container and player, so they hold no identity of
	// their own to stamp with — the only place the running operation is known is here, and
	// everything the tick spends inherits it from this one point. Stamping the flights instead
	// would leave each new one to remember, which is how the fuel for a ferry came to be filed
	// as if nobody had asked for it.
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(cmd.ContainerID, bootstrapOperationType))

	// The tick runs entirely on the live-config snapshot taken here; a knob tuned mid-tick lands
	// on the next tick. A nil reader / read miss yields a nil snapshot, which
	// resolveBootstrapConfig treats as "run this tick on the launch command" (fail-safe launch).
	cfg := resolveBootstrapConfig(cmd, h.liveConfigSnapshot(ctx, cmd))
	logger := common.LoggerFromContext(ctx)
	res := reconcileResult{}

	// Master boot-gate (RULINGS #5): the container stays resident when disabled so a config flip +
	// restart re-arms it with no manual relaunch, but it takes no action while stood down.
	if cfg.Disabled {
		return res, nil
	}

	// Phantom-cache guard (captain L47): force a live ship re-read BEFORE any role/assignment
	// decision so a phantom-idle hull isn't misread. A refresh failure fails the tick CLOSED —
	// acting on a stale pool is exactly the desync this guards against.
	if h.refresher != nil {
		if err := h.refresher.RefreshFleet(ctx, cmd.PlayerID); err != nil {
			logger.Log("WARN", fmt.Sprintf("Bootstrap ship refresh failed — skipping tick (fail-closed): %v", err), map[string]interface{}{
				"action":       "bootstrap_refresh_failed",
				"container_id": cmd.ContainerID,
			})
			return res, nil
		}
	} else {
		logger.Log("WARN", "Bootstrap has no ship refresher wired — proceeding without the phantom-cache guard (captain L47)", map[string]interface{}{
			"action":       "bootstrap_no_refresher",
			"container_id": cmd.ContainerID,
		})
	}

	if h.observer == nil {
		logger.Log("ERROR", "Bootstrap has no world observer wired — cannot reconcile", map[string]interface{}{
			"action":       "bootstrap_no_observer",
			"container_id": cmd.ContainerID,
		})
		return res, nil
	}

	obs, err := h.observer.Observe(ctx, cmd.PlayerID)
	if err != nil {
		// An infra fault reading the world must not crash the loop — log and skip the tick.
		return res, fmt.Errorf("observe world: %w", err)
	}
	if !obs.Readable {
		// Fail closed: a missing signal never drives a spend or an assignment.
		res.Blocker = "world_unreadable"
		logger.Log("WARN", fmt.Sprintf("Bootstrap world unreadable this tick (fail-closed, no action): %s", obs.Reason), map[string]interface{}{
			"action":       "bootstrap_unreadable",
			"container_id": cmd.ContainerID,
			"reason":       obs.Reason,
		})
		h.emitHeartbeat(ctx, cmd, PhaseColdStart, obs, res)
		return res, nil
	}

	// Fresh-buy count-sync: fold probes this coordinator has bought but the ship-count
	// observation has not yet reflected into the count the tick reads. The observed count lags a fresh
	// buy (the count query does not see just-bought hulls until a later sync); at a SHORT tick that lag
	// spans the next tick, so without this the buy gate would re-buy toward a target already reached
	// (over-buy → wasted capital, the money-safety hole a short tick exposes). Applied here, before the
	// phase derivation and the switch, so the whole tick — buy gate, scout guard, heartbeat — reads one
	// consistent effective count. It only ADJUSTS the count; the money guard (the flat
	// common.ImmutableReserveFloor cushion) is untouched. The bridge decays to zero as the
	// observation catches up (see probeBuyBridge).
	bridge := h.probeBridge(cmd.ContainerID)
	obs.ProbeCount = bridge.effectiveProbeCount(obs.ProbeCount)

	// Derive the phase from the observation — NEVER from a persisted enum (spec §Architecture).
	phase := derivePhase(obs)
	// Escape hatch (UNCONDITIONALLY ON): a GATE that latched under-scaled with ~no construction
	// re-derives COLDSTART (so the op can re-scale out of the death spiral) after an anti-thrash hysteresis
	// streak. A legitimately-funded fresh GATE and a genuinely-building GATE are untouched — the re-derive
	// only releases a sticky, starved latch (see reDeriveUnderScaledGate).
	phase = h.reDeriveUnderScaledGate(cmd.ContainerID, phase, obs)
	res.Phase = phase
	if h.metrics != nil {
		h.metrics.RecordPhase(string(phase))
		// Construction progress is 0 pre-GATE and rises through GATE to 100 at COMPLETE — set each tick
		// so the gauge always reflects the live world (pure observation, nil-safe).
		h.metrics.RecordConstructionPct(obs.ConstructionPercent)
	}

	switch phase {
	case PhaseColdStart:
		// Scanning and contract income run TOGETHER, not in sequence. actData drives
		// probes→target + the home scout post + shipyard readability; actIncome starts the contract
		// engine at HOUR-0 and stages haulers as their source markets appear (the contract engine holds
		// an accepted-but-unsourceable contract gracefully — verified — and claims no ship until a
		// market is known, so it cannot steal the idle hull bootstrap needs to buy probes). Contracts
		// therefore run from hour 0 (RULINGS #1), never waiting on scanning.
		h.actData(ctx, cmd, obs, &res)
		scanningBlocker := res.Blocker
		h.actIncome(ctx, cmd, obs, &res)
		// The scanning blocker is the higher-signal heartbeat line (it is the critical path to markets),
		// so it outranks the contract one; income's shows only when scanning is unblocked.
		if scanningBlocker != "" {
			res.Blocker = scanningBlocker
		}
	case PhaseGate:
		h.actGate(ctx, cmd, obs, &res)
	case PhaseExpansion:
		h.actExpansion(ctx, cmd, cfg, obs, &res)
	}

	// During the cold-start SCALING window, launch the fleet autosizer EARLY so the
	// capacity reconciler's emitted contract-delivery demand finally has a buyer (steps 2-3 of the Admiral
	// cold-start sequence), and ensure the standing dedicated contract auto-scaler so it ramps the exclusive
	// contract fleet behind the 200000 cushion. Both are deliberately NOT launched in GATE/EXPANSION: GATE
	// repurposes haulers to construction (a running autosizer scaling the contract op would contend), and
	// EXPANSION performs the normal hand-off.
	if phase == PhaseColdStart {
		h.maybeLaunchAutosizerEarly(ctx, cmd, obs, &res)
		h.ensureContractScalerEarly(ctx, cmd, &res)
	}

	// Fold any probes bought this tick into the count-sync bridge, so the NEXT tick counts
	// them against target before the observation reflects them — the invariant that prevents the
	// short-tick cross-tick over-buy. Only the probe buy sets res.Purchased; other phases and
	// dry-runs record nothing.
	bridge.recordProbeBuys(res.Purchased)

	h.emitHeartbeat(ctx, cmd, phase, obs, res)
	return res, nil
}

// derivePhase reads the current phase from the observation alone (NEVER a persisted enum — spec
// §Architecture).
//
// The phase is decided ENTIRELY by the construction/funding signals: a built gate is EXPANSION, a
// building or funded gate is GATE, and everything before that is one COLDSTART, whose two workstreams
// (scanning and contracts) the tick dispatch runs together. Neither scan coverage nor realized $/hr is
// consulted — scanning is continuous background the freshness sizer keeps fresh, and contracts are the
// RULINGS #1 funding floor that runs from hour 0.
//
// The arc must be MONOTONE, so GATE is STICKY on obs.ConstructionStarted — once a construction pipeline
// exists the arc stays in GATE, never regressing (which would re-buy the just-repurposed haulers and
// thrash). The GATE-ENTRY decision itself is factored into gateFunded, which demands a genuinely SCALED
// AND FUNDED op (the full contract fleet has reached the auto-scaler's target AND the treasury holds a
// surplus), which is what makes the sticky latch above safe — construction can only start after a
// legitimate scaled+funded entry, so a lightly-scaled op can never latch GATE permanently (the
// death spiral).
// EXPANSION is terminal and monotone (a built gate stays built): the jump-gate construction is
// COMPLETE, so the world has entered the Admiral's steady-state-growth era — the ONE phase probe-buying
// belongs to. It is checked FIRST, before every other signal, and rides the same world-signal stickiness
// GATE does: the observer reports a BUILT home gate as ConstructionComplete every tick (including after a
// restart, when the pipeline is long gone), so no income dip, fleet churn, or restart-dropped in-memory
// window can ever pull the arc back into a buying phase. A restart at any point re-derives the
// true phase from these live signals — no persisted cursor, no double-advance.
func derivePhase(obs Observation) Phase {
	if obs.ConstructionComplete {
		return PhaseExpansion // sticky/terminal: the gate is built — steady-state growth, never regress
	}
	if obs.ConstructionStarted {
		return PhaseGate // sticky: stay in GATE even as repurposed haulers pull income under the bar
	}
	if gateFunded(obs) {
		return PhaseGate
	}
	return PhaseColdStart
}

// gateFunded reports whether the economic signals warrant entering GATE (jump-gate construction).
//
// GATE requires a genuinely SCALED AND FUNDED contract operation, not a lightly-scaled op that happened to
// book a good hour (the sp-gm7r death spiral: GATE entered on ~2 haulers, started a pipeline, and the
// ConstructionStarted sticky latch then held GATE forever while the op cannibalized its haulers). Both
// must hold together — coverage is NOT a gate here (scan-completeness is continuous background, never a
// phase gate; sourcing gate materials is construction's job), and neither is realized $/hr (a spiky
// signal one contract payout swings from net-negative to a false all-clear in a single tick):
//   - the FULL contract fleet (delivery obs.Haulers + depot obs.ContractDepotHullCount) has reached the
//     auto-scaler's live achievable target (obs.ContractScalerTarget) — the op is the size the scaler is
//     genuinely driving it toward, not a 2-hull blip. This is a HARD bar: an op that cannot
//     reach the target stays in cold start by design, and it FAILS CLOSED — a 0/unread target (no scaler
//     running) NEVER gates, so bootstrap never enters GATE on an unknown target; and
//   - a treasury SURPLUS over the immutable reserve floor clearing the gate-bill war chest
//     (gateSurplusFloor) — so GATE is EARNED from contract surplus, never raced on a thin treasury its
//     own material spend then crashes. Fail-closed: an unread/thin treasury yields a small or negative
//     surplus that does not gate.
//
// This is also WHY the ConstructionStarted sticky latch in derivePhase is safe: construction is started
// (by actGate) only AFTER derivePhase has returned GATE, which demands a legitimate scaled+funded entry —
// so a lightly-scaled op can never reach ConstructionStarted and latch GATE permanently. Gate entry only
// ever tightens, never loosens (RULINGS #4).
func gateFunded(obs Observation) bool {
	fullFleet := len(obs.Haulers) + obs.ContractDepotHullCount
	return obs.ContractScalerTarget > 0 &&
		fullFleet >= obs.ContractScalerTarget &&
		obs.Treasury-common.ImmutableReserveFloor >= gateSurplusFloor
}

// reDeriveUnderScaledGate is the escape hatch: it overrides a STICKY-LATCHED GATE back to COLDSTART when the
// op is under-scaled and construction has barely begun, so a cold start that latched GATE on a 2-hauler
// income blip (then cannibalized itself into the death spiral) can climb back out by re-scaling the contract
// op. It is the EXIT-side complement to gateFunded's stricter ENTRY: entry is now hard to reach under-scaled,
// and this releases an already-stuck latch (curing a live stuck-GATE the deploy inherits). UNCONDITIONALLY
// ON — consulted every tick, but it only ever touches a STICKY, starved latch.
//
// Anti-thrash HYSTERESIS: the under-scaled + low-progress condition must hold GateReentryStreakTicks
// CONSECUTIVE ticks before the phase flips; ANY tick that breaks it resets the streak. The direction is
// deliberately asymmetric — SLOW to leave GATE (N ticks), immediate to resume it once the op re-scales (a
// scaled tick resets the streak → derivePhase's sticky GATE stands) — so the phase strongly prefers GATE and
// only escapes a genuinely, persistently starved latch. The streak is in-memory per-container and fails SAFE
// on restart: a dropped streak just re-accrues from 0 (delays the re-derive one window), never double-acts,
// because the re-derive is a pure phase relabel (idempotent — no spend, no assignment; cold start re-scales via
// the already-guarded hauler buys, and the separate manufacturing executor keeps building meanwhile).
//
// Under-scaled is measured against gateMinHaulers (the escape hatch's starved-earner floor) so the escape
// fires only for a truly starved op — below 2 haulers — never merely because a decent op sits under the full
// scaler-target entry bar.
func (h *RunBootstrapCoordinatorHandler) reDeriveUnderScaledGate(containerID string, phase Phase, obs Observation) Phase {
	// Only a sticky-latched GATE (started, not complete) is a candidate; a legitimately-funded fresh GATE
	// (no pipeline yet) or a terminal COMPLETE is not. Anything else breaks the streak and stands.
	if phase != PhaseGate || !obs.ConstructionStarted || obs.ConstructionComplete {
		h.resetUnderScaledStreak(containerID)
		return phase
	}
	underScaled := len(obs.Haulers) < gateMinHaulers
	lowProgress := obs.ConstructionPercent < gateReentryConstructionPct
	if !underScaled || !lowProgress {
		h.resetUnderScaledStreak(containerID) // condition broke → the streak must be CONSECUTIVE
		return phase
	}
	// Under-scaled + barely-built: accrue the streak; only re-derive once it holds the full window.
	if h.bumpUnderScaledStreak(containerID) < gateReentryStreakTicks {
		return phase // not yet sustained — hold sticky GATE (anti-thrash)
	}
	return PhaseColdStart
}

// bumpUnderScaledStreak increments and returns the per-container under-scaled-GATE hysteresis counter. Keyed
// by ContainerID like the other per-container state (buyBridges) because this handler is a
// REGISTERED SINGLETON; the mutex guards the map, and one container's ticks are sequential so the count is
// only ever advanced by a single goroutine.
func (h *RunBootstrapCoordinatorHandler) bumpUnderScaledStreak(containerID string) int {
	h.underScaledStreakMu.Lock()
	defer h.underScaledStreakMu.Unlock()
	if h.underScaledStreaks == nil {
		h.underScaledStreaks = map[string]int{}
	}
	h.underScaledStreaks[containerID]++
	return h.underScaledStreaks[containerID]
}

// resetUnderScaledStreak clears the streak the instant the under-scaled+low-progress condition breaks, so
// the re-derive requires GateReentryStreakTicks CONSECUTIVE (not cumulative) ticks — the anti-thrash property.
func (h *RunBootstrapCoordinatorHandler) resetUnderScaledStreak(containerID string) {
	h.underScaledStreakMu.Lock()
	defer h.underScaledStreakMu.Unlock()
	if h.underScaledStreaks != nil {
		delete(h.underScaledStreaks, containerID)
	}
}

// actData runs the SCANNING workstream: (1) drive the probe fleet to probeTarget THIS tick —
// buying up to (target-count) probes in a capital-gated loop, or (when the home shipyard price is not
// yet readable) positioning a hull at the yard so the next tick's live read succeeds;
// (2) declare the home-system scout post as a coverage target — the boot-standing scout-post
// coordinator mans it by claiming an idle probe (bootstrap assigns NO probes itself). Both
// actions are independently guarded and idempotent, so re-evaluation never double-acts. It executes
// ALONGSIDE actIncome on every cold-start tick (the parallel model).
func (h *RunBootstrapCoordinatorHandler) actData(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	// (1) Capital-gated probe acquisition — buy to target in ONE pass (a fresh universe must
	// reach probeTarget fast, not one probe per 5-min tick). Guarded on the re-observed count, so a
	// mid-purchase restart that already incremented the count simply buys the remainder. The seed is
	// bootstrap's alone: it buys 0→probeTarget and nothing beyond, so the standing freshsizer owns
	// every probe above it with no runtime hand-off between the two.
	if obs.ProbeCount < probeTarget {
		h.acquireProbesToTarget(ctx, cmd, obs, res)
	}

	// (2) Declare the home-system scout post as a COVERAGE target. Bootstrap does NOT
	// assign probes to scout tours — a scout-all-markets sweep here HOLDS the probes and starves
	// the now-boot-standing scout-post coordinator. Instead it declares the desired-state
	// home post; the coordinator mans it by claiming an IDLE probe (→ VRP-partition → scan), seeding
	// the initial home scan → census → the freshsizer takes over declaring the rest. Idempotent (the
	// declarer skips a post that already exists), so re-declaring every tick is a no-op. Guarded
	// on a resolved home system only — declare the coverage target even before the first probe lands,
	// so manning starts the instant a probe is idle.
	if obs.HomeSystem != "" {
		h.declareHomeScoutPost(ctx, cmd, obs, res)
	}
}

// maybeLaunchAutosizerEarly launches the standing fleet autosizer DURING the cold-start scaling window
// so the capacity reconciler's emitted contract-delivery demand has a buyer that scales
// the contract operation (haulers/warehouse/stockers) — the Admiral's step 3. The caller has already
// checked we are in the cold-start window. It:
//   - is IDEMPOTENT: skips silently when the autosizer is already running (obs.AutosizerRunning) — the
//     steady state once launched, so no per-tick log spam and no double-launch;
//   - reuses the SAME hand-off launcher (LaunchAutosizer) the COMPLETE hand-off uses, so the early
//     autosizer is byte-identical to the handed-off one — it arms contract_delivery iff the
//     contract_delivery_hulls_enabled config knob is set (a SEPARATE arming, set at the coordinated arm);
//   - is a BACKGROUND launch: it never claims res.Blocker (the scaling workstream's own blocker is the
//     higher-signal heartbeat line), surfacing itself via its own INFO/ERROR log line instead;
//   - is nil-safe (no launcher wired ⇒ logged skip).
func (h *RunBootstrapCoordinatorHandler) maybeLaunchAutosizerEarly(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if obs.AutosizerRunning {
		return // already launched (this tick's earlier run, or an earlier tick) — idempotent no-op
	}

	if h.handoff == nil {
		logger.Log("WARN", "Bootstrap cold-start scaling is armed but no hand-off launcher wired — cannot launch the autosizer early (the reconciler's contract-delivery demand will have no buyer)", map[string]interface{}{
			"action":       "bootstrap_no_handoff_launcher",
			"container_id": cmd.ContainerID,
		})
		return
	}

	if err := h.handoff.LaunchAutosizer(ctx, cmd.PlayerID, cmd.AgentSymbol); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Bootstrap failed to launch the fleet autosizer early (cold-start scaling): %v", err), map[string]interface{}{
			"action":       "bootstrap_autosizer_early_launch_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.AutosizerLaunchedEarly = true
	logger.Log("INFO", "Bootstrap launched the fleet autosizer EARLY (cold-start contract scaling, sp-sjvv) — the capacity reconciler's emitted contract-delivery demand now has a guard-gated buyer", map[string]interface{}{
		"action":       "bootstrap_autosizer_launched_early",
		"container_id": cmd.ContainerID,
	})
}

// ensureContractScalerEarly ensures the standing dedicated contract auto-scaler is running DURING the
// cold-start scaling window so it ramps the exclusive contract fleet behind the 200000 cushion. The
// caller has already checked we are in the cold-start window. It mirrors maybeLaunchAutosizerEarly:
//   - IDEMPOTENCY lives in the LAUNCHER, which skips a coordinator already RUNNING/PENDING, so calling
//     it every cold-start tick never double-launches a second ramp loop;
//   - is nil-safe (no launcher wired ⇒ logged skip);
//   - is a BACKGROUND launch: it never claims res.Blocker, surfacing itself via its own INFO/ERROR line.
func (h *RunBootstrapCoordinatorHandler) ensureContractScalerEarly(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if h.handoff == nil {
		logger.Log("WARN", "Bootstrap has no hand-off launcher wired — cannot ensure the contract scaler", map[string]interface{}{
			"action":       "bootstrap_no_handoff_launcher",
			"container_id": cmd.ContainerID,
		})
		return
	}

	if err := h.handoff.LaunchContractScaler(ctx, cmd.PlayerID, cmd.AgentSymbol); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Bootstrap failed to ensure the contract auto-scaler: %v", err), map[string]interface{}{
			"action":       "bootstrap_contract_scaler_early_launch_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.ContractScalerLaunchedEarly = true
	logger.Log("INFO", "Bootstrap ensured the dedicated contract auto-scaler — it ramps the exclusive contract fleet to the live ceiling behind the 200000 cushion", map[string]interface{}{
		"action":       "bootstrap_contract_scaler_launched_early",
		"container_id": cmd.ContainerID,
	})
}

// acquireProbesToTarget drives the probe fleet to probeTarget in ONE tick, behind the
// readiness and capital gates, emitting the guardrail arithmetic per buy (RULINGS #4, fail closed).
// Caller has checked "needed" (ProbeCount < target).
//
// Two coupled cold-start mechanisms:
//   - READABILITY: the yard price is unreadable on a fresh universe because nothing has visited the home
//     shipyard (its live listing is presence-gated). Rather than fail closed forever, dispatch an idle
//     hull to the yard (h.scanner) so the NEXT tick's live read succeeds. The price guard is NOT weakened
//     — no buy fires this tick; we make the price readable, not bypass it.
//   - BUY-TO-TARGET: once readable, buy up to (target-count) probes in a loop, each iteration honoring the
//     flat common.ImmutableReserveFloor capital gate against the DECREMENTING treasury (the running spend
//     is subtracted so the guard reflects real remaining credits — never a stale-treasury overspend). The
//     yard ask is stable within a tick, so a single PriceCheck feeds the whole loop.
func (h *RunBootstrapCoordinatorHandler) acquireProbesToTarget(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	// Readiness gate, second half: unblocked? The batch-purchase path needs an idle hull to fly to
	// the yard. No idle hull ⇒ BLOCKED (not failed) — a later tick with a free hull retries.
	if !obs.HasIdlePurchaser {
		res.Blocker = "no_purchaser"
		logger.Log("WARN", fmt.Sprintf("Bootstrap probe needed (%d/%d) but BLOCKED: no idle hull to execute the purchase", obs.ProbeCount, probeTarget), map[string]interface{}{
			"action":       "bootstrap_buy_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_purchaser",
		})
		return
	}

	if h.acquirer == nil {
		res.Blocker = "no_acquirer"
		logger.Log("WARN", "Bootstrap probe needed but no acquirer wired — cannot price-check or buy", map[string]interface{}{
			"action":       "bootstrap_buy_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_acquirer",
		})
		return
	}

	// Price-check ONCE (the cheapest reachable yard's ask is stable within a tick, so it feeds the whole
	// buy loop). Unreadable price ⇒ do NOT buy this tick; instead make it readable by positioning a hull
	// at the yard. Still fails CLOSED (no spend) — a genuinely unreadable price buys nothing.
	price, yard, readable, err := h.acquirer.PriceCheck(ctx, cmd.PlayerID, probeShipType)
	if err != nil || !readable {
		h.awaitReadablePrice(ctx, cmd, obs, res, "", fmt.Sprintf("probe (%d/%d)", obs.ProbeCount, probeTarget), err)
		return
	}

	// A yard prices the hauler only while something is standing at it, and the probe buy has a hull there
	// right now. Take the hauler ask too, so the first-hauler pivot has evidence to weigh before it ever
	// commits the sole earner — the cold yard it meets later gives it none. Read-only (no spend, so the
	// price guard is untouched — RULINGS #4); a missed reading simply leaves the pivot with no evidence,
	// which it treats as no reason to hold.
	if h.haulAcquirer != nil {
		_, _, _, _ = h.haulAcquirer.PriceCheck(ctx, cmd.PlayerID, haulerShipType)
	}

	// Capital-gated buy LOOP: buy up to (target-count) probes THIS tick, decrementing the treasury each
	// iteration so the flat common.ImmutableReserveFloor gate reflects real remaining credits (sp-05glh:
	// an additive cushion — treasury−price ≥ floor — replacing the deleted proportional reserve_margin
	// gate).
	need := probeTarget - obs.ProbeCount
	var spent int64
	for i := 0; i < need; i++ {
		remaining := obs.Treasury - spent
		cushion := remaining - price
		affordable := cushion >= common.ImmutableReserveFloor
		logger.Log("INFO", fmt.Sprintf("Bootstrap probe buy decision (%d of %d needed): price=%d treasury=%d spent_so_far=%d remaining=%d cushion=(remaining-price)=%d floor=%d affordable=(cushion≥floor)=%v yard=%s — %s", i+1, need, price, obs.Treasury, spent, remaining, cushion, common.ImmutableReserveFloor, affordable, yard, buyBlockNote(affordable)), map[string]interface{}{
			"action":       "bootstrap_buy_decision",
			"container_id": cmd.ContainerID,
			"price":        price,
			"treasury":     obs.Treasury,
			"remaining":    remaining,
			"cushion":      cushion,
			"floor":        common.ImmutableReserveFloor,
			"affordable":   affordable,
			"yard":         yard,
		})
		if !affordable {
			// The capital gate caps the ramp: buy what fits this tick, the rest next tick as treasury grows.
			res.Blocker = "capital_gate"
			break
		}

		bought, berr := h.acquirer.Buy(ctx, cmd.PlayerID, probeShipType, yard)
		if berr != nil {
			res.Blocker = "purchase_error"
			logger.Log("ERROR", fmt.Sprintf("Bootstrap probe purchase failed: %v", berr), map[string]interface{}{
				"action":       "bootstrap_buy_error",
				"container_id": cmd.ContainerID,
			})
			break
		}
		res.Purchased++
		spent += price
		if h.metrics != nil {
			h.metrics.RecordProbePurchased()
		}
		logger.Log("INFO", fmt.Sprintf("Bootstrap bought probe %s at %s for %d (%d/%d)", bought.ShipSymbol, yard, bought.Price, obs.ProbeCount+res.Purchased, probeTarget), map[string]interface{}{
			"action":       "bootstrap_bought_probe",
			"container_id": cmd.ContainerID,
			"ship":         bought.ShipSymbol,
			"price":        bought.Price,
		})
	}
}

// awaitReadablePrice answers a cold home shipyard: the yard prices its hulls only while a ship is
// standing at it, so an unvisited yard reads unreadable and the buy would fail closed forever. Sending
// a hull is what turns the read into evidence — and it is not a way around the price guard (RULINGS
// #4): this tick still spends nothing either way, whichever branch it takes.
//
// purchaser names the committed buy ship when the caller has one, so a tick that sends nothing means
// it is already there or on its way (still positioning); with no purchaser named, nothing sent means
// the wait simply continues. subject names what the tick is blocked on, for the heartbeat log.
func (h *RunBootstrapCoordinatorHandler) awaitReadablePrice(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult, purchaser, subject string, priceErr error) {
	logger := common.LoggerFromContext(ctx)

	if h.scanner == nil {
		res.Blocker = "price_unreadable"
		logger.Log("WARN", fmt.Sprintf("Bootstrap %s price unreadable and no shipyard scanner wired — failing closed (no buy): err=%v", subject, priceErr), map[string]interface{}{
			"action":       "bootstrap_price_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "price_unreadable",
		})
		return
	}

	dispatched, serr := h.scanner.EnsureShipyardReadable(ctx, cmd.PlayerID, obs.HomeSystem, purchaser)
	if serr != nil {
		res.Blocker = "price_unreadable"
		logger.Log("WARN", fmt.Sprintf("Bootstrap %s price unreadable and sending a hull to the home shipyard failed — failing closed (no buy): %v", subject, serr), map[string]interface{}{
			"action":       "bootstrap_price_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "price_unreadable",
		})
		return
	}

	if dispatched || purchaser != "" {
		res.Blocker = "positioning_purchaser_at_shipyard"
		logger.Log("INFO", fmt.Sprintf("Bootstrap %s price unreadable (cold home shipyard) — %s; no buy this tick", subject, positioningNote(purchaser, dispatched)), map[string]interface{}{
			"action":       "bootstrap_positioning_purchaser",
			"container_id": cmd.ContainerID,
			"blocker":      "positioning_purchaser_at_shipyard",
			"ship":         purchaser,
		})
		return
	}

	// Nothing sent and no committed buy ship: a hull is already at or heading to the yard, or none is
	// free to go. Keep price_unreadable so the heartbeat shows we are still waiting on the read.
	res.Blocker = "price_unreadable"
	logger.Log("INFO", fmt.Sprintf("Bootstrap %s price unreadable — a hull is already at or heading to the home shipyard, or none is free to send; awaiting a readable price", subject), map[string]interface{}{
		"action":       "bootstrap_price_blocked",
		"container_id": cmd.ContainerID,
		"blocker":      "price_unreadable",
	})
}

// positioningNote says which hull the tick is waiting on and whether it had to be sent.
func positioningNote(purchaser string, dispatched bool) string {
	if purchaser == "" {
		return "sent a free hull to the home shipyard so the next tick's price read succeeds"
	}
	if dispatched {
		return fmt.Sprintf("sent the purchasing hull %s to the home shipyard so the next tick's price read succeeds", purchaser)
	}
	return fmt.Sprintf("the purchasing hull %s is already at or heading to the home shipyard", purchaser)
}

// buyBlockNote annotates the decision line with what would have blocked, so the one line carries
// the whole guardrail story.
func buyBlockNote(affordable bool) string {
	if affordable {
		return "clears the capital gate"
	}
	return "BLOCKED by the capital gate (would drop the cushion below the immutable reserve floor)"
}

// declareHomeScoutPost declares the STANDING home-system scout post as a coverage target:
// the desired state the boot-standing scout-post coordinator mans by claiming an IDLE
// probe. It does NOT assign or dedicate a probe — that is the coordinator's job — so bootstrap's
// probes stay idle and claimable. Idempotent: the declarer skips a post that already exists, so
// re-declaring every tick is a no-op. Caller has checked HomeSystem is resolved.
func (h *RunBootstrapCoordinatorHandler) declareHomeScoutPost(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if h.postDeclarer == nil {
		res.Blocker = "no_scout_post_declarer"
		logger.Log("WARN", "Bootstrap cannot declare the home scout post: no scout-post declarer wired — probes will be bought but no coverage target exists for the coordinator to man", map[string]interface{}{
			"action":       "bootstrap_scout_post_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_scout_post_declarer",
		})
		return
	}

	if err := h.postDeclarer.DeclareHomeScoutPost(ctx, cmd.PlayerID, obs.HomeSystem, probeTarget); err != nil {
		res.Blocker = "scout_post_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap home scout-post declaration failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_scout_post_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.HomePostDeclared = true
	logger.Log("INFO", fmt.Sprintf("Bootstrap ensured the home scout post %s (coverage target; the scout-post coordinator mans it by claiming an idle probe)", obs.HomeSystem), map[string]interface{}{
		"action":       "bootstrap_home_scout_post_declared",
		"container_id": cmd.ContainerID,
		"system":       obs.HomeSystem,
	})
}

// emitHeartbeat writes the per-tick progress line (phase · delta done · next action · blockers) so
// a wedged reconciler is visible, never a silent stall (captain L61, spec §Observability).
func (h *RunBootstrapCoordinatorHandler) emitHeartbeat(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, phase Phase, obs Observation, res reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	delta := fmt.Sprintf("bought=%d home_post=%v haulers_bought=%d trade_seeded=%v frigate_retired=%v batch_contract=%v frigate_loop=%v purchaser_released=%v construction_started=%v mfg_ensured=%v mfg_bounced=%v workers_released=%d gate_workers_bought=%d construction_hulls_to_trade=%d handoff=%v", res.Purchased, res.HomePostDeclared, res.HaulersBought, res.TradeHullSeeded, res.FrigateRetired, res.ContractRun, res.FrigateLoopStarted, res.PurchaserReleased, res.ConstructionStartRan, res.MfgEnsured, res.MfgBounced, res.WorkersReleased, res.GateWorkersBought, res.ConstructionHullsToTrade, res.HandoffLaunched)
	next := h.nextAction(phase, obs)
	blockers := res.Blocker
	if blockers == "" {
		blockers = "none"
	}

	logger.Log("INFO", fmt.Sprintf("Bootstrap heartbeat: phase=%s probes=%d/%d scouting=%d coverage=%d/%d (%.0f%%) haulers=%d/%d slots=%d income/hr=%.0f treasury=%d gate_site=%s construction=%.0f%% gate_workers=%d/%d · %s · next=%q · blockers=%s",
		phase, obs.ProbeCount, probeTarget, obs.ProbesScouting, obs.MarketsCovered, obs.MarketsTotal, obs.CoverageFraction()*100, len(obs.Haulers), haulerTarget, res.PlacementSlots, obs.IncomePerHour, obs.Treasury, gateSiteOrNone(obs.GateSite), obs.ConstructionPercent, obs.GateWorkers, res.DesiredWorkers, delta, next, blockers), map[string]interface{}{
		"action":             "bootstrap_heartbeat",
		"container_id":       cmd.ContainerID,
		"phase":              string(phase),
		"probes":             obs.ProbeCount,
		"probe_target":       probeTarget,
		"probes_scouting":    obs.ProbesScouting,
		"markets_covered":    obs.MarketsCovered,
		"markets_total":      obs.MarketsTotal,
		"haulers":            len(obs.Haulers),
		"hauler_target":      haulerTarget,
		"trade_hulls":        obs.TradeHullCount,
		"placement_slots":    res.PlacementSlots,
		"income_per_hour":    obs.IncomePerHour,
		"treasury":           obs.Treasury,
		"purchased":          res.Purchased,
		"haulers_bought":     res.HaulersBought,
		"trade_seeded":       res.TradeHullSeeded,
		"frigate_retired":    res.FrigateRetired,
		"batch_contract":     res.ContractRun,
		"frigate_loop":       res.FrigateLoopStarted,
		"purchaser_released": res.PurchaserReleased,
		"home_post_declared": res.HomePostDeclared,
		"gate_site":          obs.GateSite,
		"construction_pct":   obs.ConstructionPercent,
		"gate_workers":       obs.GateWorkers,
		"desired_workers":    res.DesiredWorkers,
		"workers_released":   res.WorkersReleased,
		"handoff":            res.HandoffLaunched,
		"blocker":            blockers,
	})
}

// nextAction names the single next thing the reconciler intends, for the heartbeat.
func (h *RunBootstrapCoordinatorHandler) nextAction(phase Phase, obs Observation) string {
	switch phase {
	case PhaseColdStart:
		// Scanning and contracts run together, so this walks both workstreams' outstanding
		// steps in one list — scanning first, because it is the critical path to markets.
		if obs.ProbeCount < probeTarget {
			return fmt.Sprintf("buy probes to target (%d/%d, capital-gated; positions a hull at the yard first if the price is cold)", obs.ProbeCount, probeTarget)
		}
		if obs.ProbeCount > 0 && obs.ProbesScouting < obs.ProbeCount {
			return "home scout post declared — awaiting the scout-post coordinator to man idle probe(s) (sp-pt7d)"
		}
		if obs.CommandFrigateOnContract {
			return "retire the command frigate from contract work"
		}
		if !obs.BatchContractRunning {
			return "launch batch-contract on the contract fleet"
		}
		if obs.CommandFrigateID != "" && !obs.FrigateContractLoopRunning && len(obs.Haulers) == 0 && !obs.CommandFrigatePurchasing {
			return "start the command frigate's continuous contract loop (pre-hauler sole earner)"
		}
		if len(obs.Haulers) < haulerTarget {
			return fmt.Sprintf("buy contract hauler %d/%d (staged, capital-gated, placed on a fixed delivery slot)", len(obs.Haulers)+1, haulerTarget)
		}
		return fmt.Sprintf("scan home system in parallel with contracts (coverage %.0f%%)", obs.CoverageFraction()*100)
	case PhaseGate:
		if obs.GateSite == "" {
			return "discover the jump-gate construction site"
		}
		if !obs.ConstructionStarted {
			return fmt.Sprintf("start construction pipeline on %s", obs.GateSite)
		}
		if !obs.ManufacturingRunning {
			return "ensure the manufacturing coordinator (executor) is running"
		}
		if !obs.ManufacturingAdopted {
			return "bounce the manufacturing coordinator so it adopts the pipeline (L57)"
		}
		plan := planGateWorkers(obs)
		if len(plan.ReleaseShips) > 0 {
			return fmt.Sprintf("repurpose %d surplus hauler(s) to gate construction", len(plan.ReleaseShips))
		}
		if plan.Buy > 0 {
			return fmt.Sprintf("buy 1 gate worker (staged, capital-gated; %d have/%d desired)", obs.GateWorkers, plan.DesiredWorkers)
		}
		return fmt.Sprintf("monitor construction to 100%% (%.0f%%)", obs.ConstructionPercent)
	case PhaseExpansion:
		if !obs.AutosizerRunning {
			return "launch the fleet-autosizer + standing coordinators (hand-off)"
		}
		return "EXPANSION — gate built, economy handed off; steady-state growth (probe-buying era), exiting"
	default:
		return fmt.Sprintf("phase %s unhandled", phase)
	}
}

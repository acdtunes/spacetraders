package commands

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// BootstrapTunableDefaults maps every LIVE-tunable bootstrap knob (sp-r6yq) to its documented
// default — the value that applies when the persisted config column carries no positive
// override. The daemon's tune bounds registry reads THIS map, so the defaults-of-record stay
// in this file next to the consts they mirror. The map's KEY SET is also the contract for which
// BARE keys resolveBootstrapConfig live-overlays.
//
// The tune mechanism is integer-only (liveconfig.PositiveInt), so the one fraction knob is
// expressed as an integer PERCENT (coverage_bar_percent) and income_bar as whole credits — the
// coordinator divides the percent by 100 on read. The two ship-type knobs
// (probe_ship_type, hauler_ship_type) are deliberately NOT tunable: a string asset is launch-config
// only across every coordinator (a hull type is not swapped mid-run). These keys are the SEPARATE
// bare family — distinct from the config.yaml-authoritative prefixed bootstrap_* launch keys — so a
// tune is never cleared by the launch-config rebuild and survives a daemon bounce (RULINGS #2).
func BootstrapTunableDefaults() map[string]int {
	return map[string]int{
		"probe_target":         defaultProbeTarget,
		"coverage_bar_percent": int(math.Round(defaultCoverageBar * 100)),
		"hauler_target":        defaultHaulerTarget,
		"income_bar":           int(math.Round(defaultIncomeBar)),
		"min_contract_earners": defaultMinContractEarners,
		"gate_worker_target":   defaultGateWorkerTarget,
		"tick_secs":            defaultBootstrapTickSeconds,
		// sp-tsn2 single-buyer arbitration flag (0=off default, 1=on). A tunable flag with no launch key.
		"defer_probe_to_freshsizer": defaultDeferProbeToFreshsizer,
		// The GATE-entry SUSTAINED $/hr bar (whole credits). Tunable-only. gate_min_haulers is the escape
		// hatch's starved-earner floor now (GATE ENTRY uses the scaler target, not a static hauler count).
		"gate_income_bar":  int(math.Round(defaultGateIncomeBar)),
		"gate_min_haulers": defaultGateMinHaulers,
		// Death-spiral cure (UNCONDITIONALLY ON, sp-gm7r removed the master flag): its calibration knobs (the
		// surplus floor is whole credits; the reentry construction ceiling is a whole percent). Tunable-only.
		"gate_surplus_floor":            int(defaultGateSurplusFloor),
		"gate_reentry_construction_pct": int(math.Round(defaultGateReentryConstructionPct)),
		"gate_reentry_streak_ticks":     defaultGateReentryStreakTicks,
	}
}

// bootstrapRunConfig is the launch command with every default resolved, so the reconcile logic
// never repeats the "<= 0 → default" fallback (RULINGS #5, the autosizer resolveConfig idiom).
type bootstrapRunConfig struct {
	Disabled bool
	DryRun   bool

	Tick          time.Duration
	ProbeTarget   int
	CoverageBar   float64
	ProbeShipType string

	// INCOME-phase knobs, each resolved to its documented default when unset.
	HaulerTarget       int
	IncomeBar          float64
	MinContractEarners int
	HaulerShipType     string

	// ContractWorkingCapitalFloor is the ABSOLUTE cash cushion (whole credits) the treasury must still
	// clear AFTER a staged INCOME hauler buy — the money-safety that keeps the contract operation's
	// goods+fuel working capital intact (sp-acv5). Its OWN dedicated parameter, a HIGHER absolute floor
	// than the base common.ImmutableReserveFloor the DATA probe buy gates on (sp-05glh: both are now flat
	// credit cushions — treasury−price ≥ floor — never a proportion of a growing treasury). Resolved ONLY
	// from the immutable defaultContractWorkingCapitalFloor constant — never the launch command,
	// config.yaml, or a live tune — per the Admiral's hard 50k working-capital floor (RULINGS #5 +
	// 2026-07-18 Amendment, "deliberately non-tunable per-run").
	ContractWorkingCapitalFloor int64

	// GATE-phase knob, resolved to its documented default when unset.
	GateWorkerTarget int

	// DeferProbeToFreshsizer arms the sp-tsn2 single-buyer arbitration: when true, bootstrap DEFERS
	// its DATA probe buy to the freshsizer once coverage>0 and a freshsizer coordinator is running, so
	// exactly one buyer grows the shared fleet during the conflict window. Default false
	// (byte-identical). A tunable flag (defer_probe_to_freshsizer) — armed live, no launch key.
	DeferProbeToFreshsizer bool

	// GATE-entry gate: GATE requires a genuinely SCALED AND FUNDED contract op (sp-gm7r). derivePhase enters
	// GATE only once the FULL contract fleet has reached the auto-scaler's live target (obs.ContractScalerTarget,
	// a HARD bar — an op that cannot reach it stays INCOME), a SUSTAINED $/hr ≥ GateIncomeBar, AND a treasury
	// surplus ≥ GateSurplusFloor — never the bare instantaneous income_bar and never a static hauler floor.
	GateIncomeBar float64 // SUSTAINED (rolling-window mean) net $/hr the fleet must clear for GATE entry.
	// GateMinHaulers is the escape hatch's STARVED-EARNER floor (sp-gm7r): reDeriveUnderScaledGate treats a
	// sticky GATE with fewer than this many haulers as under-scaled and re-derives INCOME. GATE ENTRY no
	// longer uses it (the scaler target is the entry bar) — it now scopes only the release of a stuck latch.
	GateMinHaulers int

	// Death-spiral cure (UNCONDITIONALLY ON, sp-gm7r removed the flag): (1) gateFunded's full-fleet-vs-scaler
	// -target bar + a sustained $/hr + a treasury surplus (see gateFunded); (2) planGateWorkers keeps the
	// WHOLE contract fleet earning (sp-cdxy2: the exclusive fleet is never repurposed — the gate BUYS its
	// workers); (3) reDeriveUnderScaledGate releases a sticky GATE that latched under-scaled with ~no
	// construction back to INCOME after GateReentryStreakTicks consecutive ticks. The calibration knobs
	// resolve to their documented defaults, so the struct stays deterministic. All tunable-only (no launch key).
	GateSurplusFloor           int64   // treasury surplus (over common.ImmutableReserveFloor) required to enter GATE — the gate-bill war chest.
	GateReentryConstructionPct float64 // construction % below which an under-scaled sticky GATE may re-derive INCOME (the escape hatch's scope).
	GateReentryStreakTicks     int     // consecutive under-scaled+low-progress ticks before the GATE→INCOME re-derive fires (anti-thrash hysteresis).
}

func resolveBootstrapConfig(cmd *RunBootstrapCoordinatorCommand, live liveconfig.Snapshot) bootstrapRunConfig {
	c := bootstrapRunConfig{
		Disabled:      cmd.Disabled,
		DryRun:        cmd.DryRun,
		Tick:          time.Duration(cmd.TickIntervalSecs) * time.Second,
		ProbeTarget:   cmd.ProbeTarget,
		CoverageBar:   cmd.CoverageBar,
		ProbeShipType: cmd.ProbeShipType,

		HaulerTarget:       cmd.HaulerTarget,
		IncomeBar:          cmd.IncomeBar,
		MinContractEarners: cmd.MinContractEarners,
		HaulerShipType:     cmd.HaulerShipType,

		GateWorkerTarget: cmd.GateWorkerTarget,
	}

	// Live overlay (sp-r6yq): a `tune` writes a BARE positive key to the persisted config
	// column; the per-tick snapshot overlays it here so the change lands on the NEXT tick with no
	// restart. Only-when-present (NOT snapshot-authoritative like the freshsizer): bootstrap's
	// launch keys are the SEPARATE prefixed bootstrap_* family, so an untuned bare key is genuinely
	// absent and must not zero the launch value — byte-identical when nothing is tuned. The one
	// fraction knob decodes from integer percent; income_bar is whole credits; the <=0 default
	// fallbacks below still apply to any knob left unset by both the launch command and the overlay.
	if live != nil {
		if v := live.PositiveIntOrZero("probe_target"); v > 0 {
			c.ProbeTarget = v
		}
		if v := live.PositiveIntOrZero("coverage_bar_percent"); v > 0 {
			c.CoverageBar = float64(v) / 100.0
		}
		if v := live.PositiveIntOrZero("hauler_target"); v > 0 {
			c.HaulerTarget = v
		}
		if v := live.PositiveIntOrZero("income_bar"); v > 0 {
			c.IncomeBar = float64(v)
		}
		if v := live.PositiveIntOrZero("min_contract_earners"); v > 0 {
			c.MinContractEarners = v
		}
		if v := live.PositiveIntOrZero("gate_worker_target"); v > 0 {
			c.GateWorkerTarget = v
		}
		if v := live.PositiveIntOrZero("tick_secs"); v > 0 {
			c.Tick = time.Duration(v) * time.Second
		}
		// sp-tsn2 arbitration flag: tunable-only (no launch key), default off. A positive value arms it;
		// absent/zeroed reverts to off (byte-identical). Reused straight off the sp-r6yq live-read seam.
		if v := live.PositiveIntOrZero("defer_probe_to_freshsizer"); v > 0 {
			c.DeferProbeToFreshsizer = true
		}
		// The scaled-GATE-entry gate is unconditionally on; these are its two always-consulted calibration
		// knobs, both tunable-only. Absent/zeroed ⇒ launch value; the <=0 fallbacks below fill their defaults.
		if v := live.PositiveIntOrZero("gate_income_bar"); v > 0 {
			c.GateIncomeBar = float64(v)
		}
		if v := live.PositiveIntOrZero("gate_min_haulers"); v > 0 {
			c.GateMinHaulers = v
		}
		// Death-spiral cure calibration knobs (UNCONDITIONALLY ON, sp-gm7r removed the master flag), all
		// tunable-only (no launch key). Absent/zeroed ⇒ the launch value; the <=0 fallbacks below fill each
		// knob's documented default so the struct is deterministic.
		if v := live.PositiveIntOrZero("gate_surplus_floor"); v > 0 {
			c.GateSurplusFloor = int64(v)
		}
		if v := live.PositiveIntOrZero("gate_reentry_construction_pct"); v > 0 {
			c.GateReentryConstructionPct = float64(v)
		}
		if v := live.PositiveIntOrZero("gate_reentry_streak_ticks"); v > 0 {
			c.GateReentryStreakTicks = v
		}
	}

	if c.Tick <= 0 {
		c.Tick = defaultBootstrapTickSeconds * time.Second
	}
	if c.ProbeTarget <= 0 {
		c.ProbeTarget = defaultProbeTarget
	}
	if c.CoverageBar <= 0 {
		c.CoverageBar = defaultCoverageBar
	}
	if c.ProbeShipType == "" {
		c.ProbeShipType = defaultProbeShipType
	}
	if c.HaulerTarget <= 0 {
		c.HaulerTarget = defaultHaulerTarget
	}
	if c.IncomeBar <= 0 {
		c.IncomeBar = defaultIncomeBar
	}
	if c.MinContractEarners <= 0 {
		c.MinContractEarners = defaultMinContractEarners
	}
	if c.HaulerShipType == "" {
		c.HaulerShipType = defaultHaulerShipType
	}
	if c.GateWorkerTarget <= 0 {
		c.GateWorkerTarget = defaultGateWorkerTarget
	}
	// The contract working-capital floor is the Admiral's IMMUTABLE hard floor (RULINGS #5): sourced ONLY
	// from the constant, never the launch command / config.yaml / a live tune. There is deliberately no
	// override seam above — a hard floor is not a per-run knob (sp-acv5).
	c.ContractWorkingCapitalFloor = defaultContractWorkingCapitalFloor
	// sp-fp3y: the scaled-GATE bars resolve to their documented defaults when neither launched nor tuned,
	// so bootstrapRunConfig stays deterministic. The scaled gate is unconditionally on, so these are always
	// consulted at GATE entry.
	if c.GateIncomeBar <= 0 {
		c.GateIncomeBar = defaultGateIncomeBar
	}
	if c.GateMinHaulers <= 0 {
		c.GateMinHaulers = defaultGateMinHaulers
	}
	// The death-spiral-cure calibration knobs resolve to their documented defaults when neither launched nor
	// tuned, so bootstrapRunConfig stays deterministic (they are always consulted now — sp-gm7r removed the flag).
	if c.GateSurplusFloor <= 0 {
		c.GateSurplusFloor = defaultGateSurplusFloor
	}
	if c.GateReentryConstructionPct <= 0 {
		c.GateReentryConstructionPct = defaultGateReentryConstructionPct
	}
	if c.GateReentryStreakTicks <= 0 {
		c.GateReentryStreakTicks = defaultGateReentryStreakTicks
	}
	return c
}

// reconcileResult tallies one tick's effect for the heartbeat and the tests.
type reconcileResult struct {
	Phase            Phase
	Purchased        int    // probes actually bought this tick (DATA)
	WouldBuy         int    // ships a dry-run WOULD have bought this tick (DATA probe or INCOME hauler)
	HomePostDeclared bool   // the home scout-post coverage target was ensured this tick (DATA, sp-pt7d)
	Blocker          string // the one guard that blocked the highest-priority action (for the heartbeat)

	// INCOME tallies (Slice 2).
	HaulersBought      int  // contract haulers actually bought this tick (staged: at most 1)
	FrigateRetired     bool // the command frigate was retired from contract work this tick
	ContractRun        bool // batch-contract was launched this tick
	FrigateLoopStarted bool // the command frigate's continuous contract loop was started this tick (sp-rype)
	FrigatePivoted     bool // the first-hauler pivot fired this tick: frigate loop STOPPED + dedicated the exclusive purchasing ship (sp-7r7w). With a readable yard price the buy also runs this tick; on a COLD price it is a SEPARATE later tick once the freed frigate is positioned (sp-5nd2 fault-2)
	TradeHullSeeded    bool // the INCOME hull-routing trade-seed fired this tick (sp-192k4): acquisition #2 bought + dedicated to the trade fleet + the trade coordinator ensured
	ViableHubs         int  // viable contract hubs the selector found (for the heartbeat)

	// GATE tallies.
	ConstructionStartRan bool // `construction start` ran this tick (created/resumed the pipeline)
	MfgEnsured           bool // the manufacturing coordinator (executor) was ensured-running this tick
	MfgBounced           bool // the executor was bounced for pipeline adoption this tick (captain L57)
	WorkersReleased      int  // gate workers un-dedicated to the idle pool this tick (sp-mxflh surplus re-balance; the sp-cdxy2 repurpose seam is dormant)
	GateWorkersBought    int  // gate-worker hulls actually bought this tick (staged: at most 1)
	DesiredWorkers       int  // the tick's gate-worker sizing target (for the heartbeat)

	// COMPLETE tallies.
	HandoffLaunched bool // the autosizer + standing coordinators were launched this tick (the hand-off)
	Done            bool // terminal: COMPLETE reached and handed off — the reconcile loop may exit

	// sp-sjvv: the fleet autosizer was launched EARLY this tick (armed cold-start scaling). Test-only
	// observability — deliberately NOT in the heartbeat delta (keeping the flag-off log byte-identical);
	// the early launch surfaces its own INFO line, mirroring how the sp-tsn2 deferral does.
	AutosizerLaunchedEarly bool

	// The dedicated contract auto-scaler was launched EARLY this tick (unconditional in the DATA/INCOME
	// window). Same test-only observability as AutosizerLaunchedEarly.
	ContractScalerLaunchedEarly bool
}

// probeBuyBridge closes the sync-lag window between a probe purchase and the ship-count observation
// reflecting it (sp-lgo3). The observed count LAGS a fresh buy — the count query does not see
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
// against target before the observation reflects them. A no-op for a zero/negative delta (non-DATA
// ticks and dry-runs buy nothing).
func (b *probeBuyBridge) recordProbeBuys(n int) {
	if n > 0 {
		b.pending += n
	}
}

// probeBridge returns the per-container count-sync bridge (sp-lgo3), lazily created. Keyed by
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

// incomeWindow is the sp-fp3y GATE-entry income smoother: a rolling window of the last
// gateIncomeWindowTicks realized-$/hr observations whose MEAN is the SUSTAINED $/hr the armed GATE gate
// reads. It exists because obs.IncomePerHour as observed is spiky — a single contract payout swings it
// from net-negative to well over the old 10000 income_bar in one tick (the ktio false trigger) — so the
// instantaneous value must NOT trip GATE. The mean over the window dilutes a lone spike against the
// surrounding (often net-negative) spend ticks; only a genuinely sustained rate clears the bar. Like the
// probeBuyBridge it is per-container handler state, not persisted progress: it is dropped on restart, and
// sustained() returns −inf until the window is FULL, so a spike on short history (the first ticks after
// arming or a restart) can never enter GATE — the arc simply keeps scaling the contract op until the
// income is genuinely sustained.
type incomeWindow struct {
	samples []float64
}

// sustained records this tick's realized $/hr and returns the rolling-window mean once the window holds a
// FULL gateIncomeWindowTicks samples; until then it returns −inf (below any positive bar), so the armed
// GATE gate requires the income to have been observed sustained across the whole window, never a single
// instantaneous spike. Called once per readable tick when the gate is armed (a single goroutine per
// container — see incomeWindowFor), so no per-window locking is needed.
func (w *incomeWindow) sustained(perHour float64) float64 {
	w.samples = append(w.samples, perHour)
	if len(w.samples) > gateIncomeWindowTicks {
		w.samples = w.samples[len(w.samples)-gateIncomeWindowTicks:]
	}
	if len(w.samples) < gateIncomeWindowTicks {
		return math.Inf(-1) // not yet sustained: a short history can never clear a positive bar
	}
	var sum float64
	for _, s := range w.samples {
		sum += s
	}
	return sum / float64(len(w.samples))
}

// incomeWindowFor returns the per-container GATE-entry income smoother (sp-fp3y), lazily created. Keyed by
// ContainerID for the same reason as probeBridge: this handler is a REGISTERED SINGLETON serving every
// bootstrap container, so a bare field would be shared and RACED across concurrent players. One container's
// ticks run sequentially (Handle awaits each reconcile), so the returned *incomeWindow is only ever touched
// by a single goroutine — the mutex guards the map, not the returned struct.
func (h *RunBootstrapCoordinatorHandler) incomeWindowFor(containerID string) *incomeWindow {
	h.incomeWindowMu.Lock()
	defer h.incomeWindowMu.Unlock()
	if h.incomeWindows == nil {
		h.incomeWindows = map[string]*incomeWindow{}
	}
	w := h.incomeWindows[containerID]
	if w == nil {
		w = &incomeWindow{}
		h.incomeWindows[containerID] = w
	}
	return w
}

// cachedHaulerPrice returns the last readable contract-hauler price cached for this container (sp-muc5x),
// or 0 when none has been observed yet. The caller treats 0 as "no evidence" and proceeds unchanged (the
// existing free+position path), so the guard only ever TIGHTENS behavior on a POSITIVE cache. Keyed by
// ContainerID like the other per-container state (this handler is a REGISTERED SINGLETON); the mutex guards
// the map, and one container's ticks are sequential so the value is only ever touched by a single goroutine.
func (h *RunBootstrapCoordinatorHandler) cachedHaulerPrice(containerID string) int64 {
	h.haulerPriceMu.Lock()
	defer h.haulerPriceMu.Unlock()
	return h.haulerPrices[containerID]
}

// cacheHaulerPrice records the last readable contract-hauler price for this container (sp-muc5x), so a later
// cold-price tick can test the first-hauler capital gate BEFORE freeing the frigate's earning loop. A
// non-positive price caches nothing (an unreadable read must not overwrite a good cache with 0).
func (h *RunBootstrapCoordinatorHandler) cacheHaulerPrice(containerID string, price int64) {
	if price <= 0 {
		return
	}
	h.haulerPriceMu.Lock()
	defer h.haulerPriceMu.Unlock()
	if h.haulerPrices == nil {
		h.haulerPrices = map[string]int64{}
	}
	h.haulerPrices[containerID] = price
}

// reconcileOnce runs one full pass: phantom-cache refresh → observe → derive phase → act on the
// delta → heartbeat. It is the unit the tests drive directly; Handle just calls it on the tick.
// Every side-effecting step is guarded "already done / in-flight?" and fails CLOSED on an
// unreadable input, so re-evaluation (including the first tick after a restart) never double-acts.
func (h *RunBootstrapCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunBootstrapCoordinatorCommand) (reconcileResult, error) {
	// The tick runs entirely on the live-config snapshot taken here; a knob tuned mid-tick lands
	// on the next tick (sp-r6yq). A nil reader / read miss yields a nil snapshot, which
	// resolveBootstrapConfig treats as "run this tick on the launch command" (fail-safe launch).
	cfg := resolveBootstrapConfig(cmd, h.liveConfigSnapshot(ctx, cmd))
	logger := common.LoggerFromContext(ctx)
	res := reconcileResult{}

	// Master boot-gate (RULINGS #5): the container stays resident when disabled so a config flip +
	// restart re-arms it with no manual relaunch, but it takes no action while stood down.
	if cfg.Disabled {
		return res, nil
	}

	// No-silent-dry-run (f5pr lesson): dry-run WARNs every tick — it is opt-in watch mode, not a
	// silent no-op.
	if cfg.DryRun {
		logger.Log("WARN", "Bootstrap in DRY-RUN — every decision is evaluated and logged but NOTHING is bought or assigned (set dry_run=false to arm)", map[string]interface{}{
			"action":       "bootstrap_dry_run",
			"container_id": cmd.ContainerID,
		})
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
		h.emitHeartbeat(ctx, cmd, cfg, PhaseData, obs, res)
		return res, nil
	}

	// Fresh-buy count-sync (sp-lgo3): fold probes this coordinator has bought but the ship-count
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

	// sp-fp3y: GATE entry must read a SUSTAINED $/hr (smoothed over a rolling window of recent ticks), so a
	// single instantaneous contract-payout spike cannot trip GATE with an unscaled op (the ktio deadlock).
	// The scaled gate is unconditionally on, so the smoothing is applied every tick. Substitute the window
	// mean into the observation the phase derivation reads — mirroring how the sp-lgo3 bridge substitutes
	// ProbeCount just above. The raw obs is left UNTOUCHED so the heartbeat still reports instantaneous
	// income; only phaseObs (the phase derivation's input) carries the smoothed value.
	phaseObs := obs
	phaseObs.IncomePerHour = h.incomeWindowFor(cmd.ContainerID).sustained(obs.IncomePerHour)

	// Derive the phase from the observation — NEVER from a persisted enum (spec §Architecture).
	phase := derivePhase(phaseObs, cfg)
	// Escape hatch (UNCONDITIONALLY ON, sp-gm7r): a GATE that latched under-scaled with ~no construction
	// re-derives INCOME (so the op can re-scale out of the death spiral) after an anti-thrash hysteresis
	// streak. A legitimately-funded fresh GATE and a genuinely-building GATE are untouched — the re-derive
	// only releases a sticky, starved latch (see reDeriveUnderScaledGate).
	phase = h.reDeriveUnderScaledGate(cmd.ContainerID, phase, phaseObs, cfg)
	res.Phase = phase
	if h.metrics != nil {
		h.metrics.RecordPhase(string(phase))
		// Construction progress is 0 pre-GATE and rises through GATE to 100 at COMPLETE — set each tick
		// so the gauge always reflects the live world (pure observation, nil-safe).
		h.metrics.RecordConstructionPct(obs.ConstructionPercent)
	}

	switch phase {
	case PhaseData:
		// COLD-START PARALLEL WINDOW (sp-t39j): scanning (DATA) and contract income (INCOME) run
		// TOGETHER, not in sequence. actData drives probes→target + scout assignment + shipyard
		// readability; actIncome starts the contract engine at HOUR-0 and stages haulers as their source
		// markets appear (the contract engine holds an accepted-but-unsourceable contract gracefully —
		// verified — and claims no ship until a market is known, so it cannot steal the idle hull
		// bootstrap needs to buy probes). Coverage no longer gates income (RULINGS #1: contracts from
		// hour 0).
		h.actData(ctx, cmd, cfg, obs, &res)
		dataBlocker := res.Blocker
		h.actIncome(ctx, cmd, cfg, obs, &res)
		// In the cold-start window the scanning-workstream blocker is the higher-signal one for the
		// heartbeat (it is the critical path to markets); keep it when set, else surface income's.
		if dataBlocker != "" {
			res.Blocker = dataBlocker
		}
	case PhaseIncome:
		h.actIncome(ctx, cmd, cfg, obs, &res)
	case PhaseGate:
		h.actGate(ctx, cmd, cfg, obs, &res)
	case PhaseExpansion:
		h.actExpansion(ctx, cmd, cfg, obs, &res)
	}

	// sp-sjvv (ktio-B): during the cold-start SCALING window (DATA/INCOME), launch the fleet autosizer
	// EARLY so the capacity reconciler's emitted contract-delivery demand finally has a buyer (steps 2-3
	// of the Admiral cold-start sequence). Unconditional in the DATA/INCOME window. Idempotent (skips when
	// already running). Deliberately NOT launched in GATE/EXPANSION: GATE repurposes haulers to construction
	// (a running autosizer scaling the contract op would contend), and EXPANSION performs the normal hand-off.
	if phase == PhaseData || phase == PhaseIncome {
		h.maybeLaunchAutosizerEarly(ctx, cmd, cfg, obs, &res)
	}

	// Launch the standing dedicated contract auto-scaler EARLY during the same DATA/INCOME scaling window so
	// it ramps the exclusive contract fleet behind the 200000 cushion. Idempotent (skips when already
	// running). Scoped to DATA/INCOME like the autosizer launch: GATE repurposes haulers to construction, and
	// the scaler is launched once then RUNS FOREVER via restart recovery. DELAY-LAUNCHED until the trade hull
	// exists (obs.TradeHullCount >= 1, sp-192k4): the scaler would otherwise grab acquisition #2 as a contract
	// hull, racing the INCOME trade-seed for that slot. Holding the scaler until the trade hull is seeded lets
	// the bootstrap own the #1-contract → #2-trade routing; the scaler then ramps #3… (it is cushion-gated, so
	// a slightly later launch never delays real buying). The trade hull existing is the observable, restart-safe
	// gate — no stored marker.
	if (phase == PhaseData || phase == PhaseIncome) && obs.TradeHullCount >= 1 {
		h.maybeLaunchContractScalerEarly(ctx, cmd, cfg, obs, &res)
	}

	// Fold any probes bought this tick into the count-sync bridge (sp-lgo3), so the NEXT tick counts
	// them against target before the observation reflects them — the invariant that prevents the
	// short-tick cross-tick over-buy. Only the DATA probe buy sets res.Purchased; other phases and
	// dry-runs record nothing.
	bridge.recordProbeBuys(res.Purchased)

	h.emitHeartbeat(ctx, cmd, cfg, phase, obs, res)
	return res, nil
}

// derivePhase reads the current phase from the observation alone (NEVER a persisted enum — spec
// §Architecture).
//
// PARALLEL MODEL (sp-t39j): DATA (scanning) and INCOME (contracts) are PARALLEL workstreams, NOT
// sequential phases. Coverage is NO LONGER a gate on income — contracts are the RULINGS #1 funding
// floor and must run from hour 0, not wait for scanning to ~complete. So the economic signals
// (construction, income_bar) are evaluated FIRST, regardless of coverage: a built gate is COMPLETE, a
// building/funded gate is GATE, no matter how much of the home system has been scanned. The DATA vs
// INCOME label below only chooses whether the SCANNING workstream is still ramping (probes under target,
// or bought but not yet scouting ⇒ DATA-labeled, still buying/assigning probes); the contract workstream
// runs in BOTH (the tick dispatch runs actIncome in the DATA phase too). Coverage is continuous
// background — the freshness sizer keeps the map fresh — and never gates the label.
//
// The arc must be MONOTONE, but realized income is NOT monotone across the INCOME→GATE boundary: GATE
// repurposes contract haulers to construction, which DROPS realized $/hr back under income_bar. So GATE
// is made STICKY on obs.ConstructionStarted — once a construction pipeline exists the arc stays in GATE
// regardless of income, never regressing (which would re-buy the just-repurposed haulers and thrash).
// The GATE-ENTRY decision itself is factored into gateFunded, which demands a genuinely SCALED AND FUNDED
// op (the full contract fleet has reached the auto-scaler's target, a SUSTAINED $/hr, AND a treasury
// surplus), which is what makes the sticky latch above safe — construction can only start after a
// legitimate scaled+funded entry, so a spurious income spike on a lightly-scaled op can never latch GATE
// permanently (the sp-gm7r death spiral).
// EXPANSION (sp-feiy7) is terminal and monotone (a built gate stays built): the jump-gate construction is
// COMPLETE, so the world has entered the Admiral's steady-state-growth era — the ONE phase probe-buying
// belongs to. It is checked FIRST, before every other signal, and rides the same world-signal stickiness
// GATE does: the observer reports a BUILT home gate as ConstructionComplete every tick (including after a
// restart, when the pipeline is long gone), so no income dip, fleet churn, or restart-dropped in-memory
// window can ever pull the arc back into a buying phase. A restart at any point re-derives the
// true phase from these live signals — no persisted cursor, no double-advance.
func derivePhase(obs Observation, cfg bootstrapRunConfig) Phase {
	if obs.ConstructionComplete {
		return PhaseExpansion // sticky/terminal: the gate is built — steady-state growth, never regress
	}
	if obs.ConstructionStarted {
		return PhaseGate // sticky: stay in GATE even as repurposed haulers pull income under the bar
	}
	if gateFunded(obs, cfg) {
		return PhaseGate
	}
	// Not yet funded/building. Label DATA while the scanning workstream is still ramping probes (under
	// target, or bought-but-not-yet-scouting), else INCOME — but the contract workstream runs regardless
	// of this label (see the tick dispatch). Coverage does NOT gate the label: once probes are at target
	// and scouting, actData is a no-op, so this flip only ever skips a no-op, never the probe ramp.
	if obs.ProbeCount >= cfg.ProbeTarget && obs.ProbesScouting >= obs.ProbeCount {
		return PhaseIncome
	}
	return PhaseData
}

// gateFunded reports whether the economic signals warrant entering GATE (jump-gate construction).
//
// GATE requires a genuinely SCALED AND FUNDED contract operation, not a lightly-scaled op on an income
// spike (the sp-gm7r death spiral: GATE entered on ~2 haulers + a spike, started a pipeline, and the
// ConstructionStarted sticky latch then held GATE forever while the op cannibalized its haulers). All
// three must hold together — coverage is NOT a gate here (scan-completeness is continuous background,
// never a phase gate; sourcing gate materials is construction's job):
//   - the FULL contract fleet (delivery obs.Haulers + depot obs.ContractDepotHullCount) has reached the
//     auto-scaler's live achievable target (obs.ContractScalerTarget) — the op is the size the scaler is
//     genuinely driving it toward, not a 2-hull blip. This is a HARD bar (sp-gm7r): an op that cannot
//     reach the target stays in INCOME by design, and it FAILS CLOSED — a 0/unread target (no scaler
//     running) NEVER gates, so bootstrap never enters GATE on an unknown target;
//   - a SUSTAINED $/hr ≥ gate_income_bar — obs.IncomePerHour here carries the rolling-window MEAN the
//     reconciler substitutes every tick (an instantaneous spike is diluted well under the bar; a
//     not-yet-full window reads −inf), so a fresh spike on short history can never trip GATE; and
//   - a treasury SURPLUS over the immutable reserve floor clearing the gate-bill war chest
//     (gate_surplus_floor) — so GATE is EARNED from contract surplus, never raced on a thin treasury its
//     own material spend then crashes. Fail-closed: an unread/thin treasury yields a small or negative
//     surplus that does not gate.
//
// This is also WHY the ConstructionStarted sticky latch in derivePhase is safe: construction is started
// (by actGate) only AFTER derivePhase has returned GATE, which demands a legitimate scaled+funded entry —
// so a spurious income spike on a lightly-scaled op can never reach ConstructionStarted and latch GATE
// permanently. Gate entry only ever tightens, never loosens (RULINGS #4).
func gateFunded(obs Observation, cfg bootstrapRunConfig) bool {
	fullFleet := len(obs.Haulers) + obs.ContractDepotHullCount
	return obs.ContractScalerTarget > 0 &&
		fullFleet >= obs.ContractScalerTarget &&
		obs.IncomePerHour >= cfg.GateIncomeBar &&
		obs.Treasury-common.ImmutableReserveFloor >= cfg.GateSurplusFloor
}

// reDeriveUnderScaledGate is the escape hatch: it overrides a STICKY-LATCHED GATE back to INCOME when the
// op is under-scaled and construction has barely begun, so a cold start that latched GATE on a 2-hauler
// income blip (then cannibalized itself into the death spiral) can climb back out by re-scaling the contract
// op. It is the EXIT-side complement to gateFunded's stricter ENTRY: entry is now hard to reach under-scaled,
// and this releases an already-stuck latch (curing a live stuck-GATE the deploy inherits). UNCONDITIONALLY
// ON (sp-gm7r removed the flag) — consulted every tick, but it only ever touches a STICKY, starved latch.
//
// Anti-thrash HYSTERESIS: the under-scaled + low-progress condition must hold GateReentryStreakTicks
// CONSECUTIVE ticks before the phase flips; ANY tick that breaks it resets the streak. The direction is
// deliberately asymmetric — SLOW to leave GATE (N ticks), immediate to resume it once the op re-scales (a
// scaled tick resets the streak → derivePhase's sticky GATE stands) — so the phase strongly prefers GATE and
// only escapes a genuinely, persistently starved latch. The streak is in-memory per-container and fails SAFE
// on restart: a dropped streak just re-accrues from 0 (delays the re-derive one window), never double-acts,
// because the re-derive is a pure phase relabel (idempotent — no spend, no assignment; INCOME re-scales via
// the already-guarded hauler buys, and the separate manufacturing executor keeps building meanwhile).
//
// Under-scaled is measured against gate_min_haulers (the escape hatch's starved-earner floor) so the escape
// fires only for a truly starved op — below 2 haulers OR below the sustained $/hr bar — never merely because
// a decent op sits under the full scaler-target entry bar. Low-progress uses the same sustained $/hr the
// entry gate reads (substituted by the caller), so a peak cannot mask a starved op.
func (h *RunBootstrapCoordinatorHandler) reDeriveUnderScaledGate(containerID string, phase Phase, obs Observation, cfg bootstrapRunConfig) Phase {
	// Only a sticky-latched GATE (started, not complete) is a candidate; a legitimately-funded fresh GATE
	// (no pipeline yet) or a terminal COMPLETE is not. Anything else breaks the streak and stands.
	if phase != PhaseGate || !obs.ConstructionStarted || obs.ConstructionComplete {
		h.resetUnderScaledStreak(containerID)
		return phase
	}
	underScaled := len(obs.Haulers) < cfg.GateMinHaulers || obs.IncomePerHour < cfg.GateIncomeBar
	lowProgress := obs.ConstructionPercent < cfg.GateReentryConstructionPct
	if !underScaled || !lowProgress {
		h.resetUnderScaledStreak(containerID) // condition broke → the streak must be CONSECUTIVE
		return phase
	}
	// Under-scaled + barely-built: accrue the streak; only re-derive once it holds the full window.
	if h.bumpUnderScaledStreak(containerID) < cfg.GateReentryStreakTicks {
		return phase // not yet sustained — hold sticky GATE (anti-thrash)
	}
	return PhaseIncome
}

// bumpUnderScaledStreak increments and returns the per-container under-scaled-GATE hysteresis counter. Keyed
// by ContainerID like the other per-container state (buyBridges/incomeWindows) because this handler is a
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

// actData runs the DATA (scanning) workstream: (1) drive the probe fleet to probe_target THIS tick —
// buying up to (target-count) probes in a capital-gated loop, or (when the home shipyard price is not
// yet readable) positioning a hull at the yard so the next tick's live read succeeds (sp-hh0h);
// (2) declare the home-system scout post as a coverage target (sp-pt7d) — the boot-standing scout-post
// coordinator (sp-9ujl) mans it by claiming an idle probe (bootstrap assigns NO probes itself). Both
// actions are independently guarded and idempotent, so re-evaluation never double-acts. It runs in the
// DATA phase, which — under the parallel model (sp-t39j) — executes ALONGSIDE actIncome during cold start.
func (h *RunBootstrapCoordinatorHandler) actData(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	// (1) Capital-gated probe acquisition — buy to target in ONE pass (sp-hh0h: a fresh universe must
	// reach probe_target fast, not one probe per 5-min tick). Guarded on the re-observed count, so a
	// mid-purchase restart that already incremented the count simply buys the remainder. sp-tsn2: when
	// the single-buyer arbitration is armed and the freshsizer has taken over (coverage>0 + freshsizer
	// running), DEFER the buy to it so the two coordinators never grow one shared fleet past the ceiling
	// (the era-3 multi-buyer lesson).
	if obs.ProbeCount < cfg.ProbeTarget && !h.deferProbeBuyToFreshsizer(ctx, cmd, cfg, obs, res) {
		h.acquireProbesToTarget(ctx, cmd, cfg, obs, res)
	}

	// (2) Declare the home-system scout post as a COVERAGE target (sp-pt7d). Bootstrap no longer
	// ASSIGNS probes to scout tours — the old scout-all-markets sweep HELD the probes and starved
	// the now-boot-standing scout-post coordinator (sp-9ujl). Instead it declares the desired-state
	// home post; the coordinator mans it by claiming an IDLE probe (→ VRP-partition → scan), seeding
	// the initial home scan → census → the freshsizer takes over declaring the rest. Idempotent (the
	// declarer skips a post that already exists), so re-declaring every DATA tick is a no-op. Guarded
	// on a resolved home system only — declare the coverage target even before the first probe lands,
	// so manning starts the instant a probe is idle.
	if obs.HomeSystem != "" {
		h.declareHomeScoutPost(ctx, cmd, cfg, obs, res)
	}
}

// deferProbeBuyToFreshsizer reports whether bootstrap should hand THIS tick's probe buy to the
// standing freshsizer (sp-tsn2 single-buyer arbitration). It engages ONLY when armed
// (defer_probe_to_freshsizer) AND the first market is covered (coverage>0, so the freshsizer has
// something to size against) AND a freshsizer coordinator is actually running to take over —
// bootstrap never defers into a vacuum, so a cold start cannot wedge if the freshsizer is down. It is
// BUY-ONLY: the caller still assigns scouting for the probes bootstrap already holds. Default off ⇒
// always false (byte-identical to today). A deferral is surfaced on the heartbeat, never silent.
func (h *RunBootstrapCoordinatorHandler) deferProbeBuyToFreshsizer(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) bool {
	if !cfg.DeferProbeToFreshsizer || obs.CoverageFraction() <= 0 || !obs.FreshsizerActive {
		return false
	}
	res.Blocker = "deferred_to_freshsizer"
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Bootstrap probe needed (%d/%d) but DEFERRING the buy to the running freshsizer (coverage %.0f%%>0) — single-buyer arbitration, one fleet-grower during the conflict window (sp-tsn2)", obs.ProbeCount, cfg.ProbeTarget, obs.CoverageFraction()*100), map[string]interface{}{
		"action":       "bootstrap_probe_deferred",
		"container_id": cmd.ContainerID,
		"blocker":      "deferred_to_freshsizer",
	})
	return true
}

// maybeLaunchAutosizerEarly launches the standing fleet autosizer DURING the cold-start scaling window
// (sp-sjvv, ktio-B) so the capacity reconciler's emitted contract-delivery demand has a buyer that scales
// the contract operation (haulers/warehouse/stockers) — the Admiral's step 3. The caller has already
// checked we are in the DATA/INCOME window. It:
//   - is IDEMPOTENT: skips silently when the autosizer is already running (obs.AutosizerRunning) — the
//     steady state once launched, so no per-tick log spam and no double-launch;
//   - reuses the SAME hand-off launcher (LaunchAutosizer) the COMPLETE hand-off uses, so the early
//     autosizer is byte-identical to the handed-off one — it arms contract_delivery iff sp-nkqn's own
//     contract_delivery_hulls_enabled config knob is set (a SEPARATE arming, set at the coordinated arm);
//   - is a BACKGROUND launch: it never claims res.Blocker (the scaling workstream's own blocker is the
//     higher-signal heartbeat line), surfacing itself via its own INFO/ERROR log line instead;
//   - is nil-safe (no launcher wired ⇒ logged skip) and dry-run-safe (WOULD-launch, no action).
func (h *RunBootstrapCoordinatorHandler) maybeLaunchAutosizerEarly(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if obs.AutosizerRunning {
		return // already launched (this tick's earlier run, or an earlier tick) — idempotent no-op
	}

	if cfg.DryRun {
		logger.Log("INFO", "Bootstrap DRY-RUN: WOULD launch the fleet autosizer EARLY (cold-start contract scaling armed) so the capacity reconciler's demand has a buyer (took no action)", map[string]interface{}{
			"action":       "bootstrap_would_launch_autosizer_early",
			"container_id": cmd.ContainerID,
		})
		return
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
	logger.Log("INFO", "Bootstrap launched the fleet autosizer EARLY (cold-start contract scaling armed, sp-sjvv) — the capacity reconciler's emitted contract-delivery demand now has a guard-gated buyer; bootstrap will DEFER its own contract-hauler buys to it (single-buyer arbitration)", map[string]interface{}{
		"action":       "bootstrap_autosizer_launched_early",
		"container_id": cmd.ContainerID,
	})
}

// maybeLaunchContractScalerEarly launches the standing dedicated contract auto-scaler DURING the
// cold-start scaling window so it ramps the exclusive contract fleet behind the 200000 cushion. The
// caller has already checked we are in the DATA/INCOME window. It mirrors maybeLaunchAutosizerEarly:
//   - IDEMPOTENT: skips silently when the scaler is already running (obs.ContractScalerRunning) — the
//     steady state once launched (launched once, then RUNS FOREVER via restart recovery);
//   - is nil-safe (no launcher wired ⇒ logged skip) and dry-run-safe (WOULD-launch, no action);
//   - is a BACKGROUND launch: it never claims res.Blocker, surfacing itself via its own INFO/ERROR line.
func (h *RunBootstrapCoordinatorHandler) maybeLaunchContractScalerEarly(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if obs.ContractScalerRunning {
		return // already launched (armed once) — idempotent no-op
	}

	if cfg.DryRun {
		logger.Log("INFO", "Bootstrap DRY-RUN: WOULD launch the dedicated contract auto-scaler EARLY to ramp the exclusive contract fleet (took no action)", map[string]interface{}{
			"action":       "bootstrap_would_launch_contract_scaler_early",
			"container_id": cmd.ContainerID,
		})
		return
	}

	if h.handoff == nil {
		logger.Log("WARN", "Bootstrap has no hand-off launcher wired — cannot launch the contract scaler early", map[string]interface{}{
			"action":       "bootstrap_no_handoff_launcher",
			"container_id": cmd.ContainerID,
		})
		return
	}

	if err := h.handoff.LaunchContractScaler(ctx, cmd.PlayerID, cmd.AgentSymbol); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Bootstrap failed to launch the contract auto-scaler early: %v", err), map[string]interface{}{
			"action":       "bootstrap_contract_scaler_early_launch_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.ContractScalerLaunchedEarly = true
	logger.Log("INFO", "Bootstrap launched the dedicated contract auto-scaler EARLY — it ramps the exclusive contract fleet to the live ceiling behind the 200000 cushion", map[string]interface{}{
		"action":       "bootstrap_contract_scaler_launched_early",
		"container_id": cmd.ContainerID,
	})
}

// acquireProbesToTarget drives the probe fleet to probe_target in ONE tick (sp-hh0h), behind the
// readiness and capital gates, emitting the guardrail arithmetic per buy (RULINGS #4, fail closed).
// Caller has checked "needed" (ProbeCount < target).
//
// Two coupled cold-start fixes vs the old one-per-tick buy:
//   - READABILITY: the yard price is unreadable on a fresh universe because nothing has visited the home
//     shipyard (its live listing is presence-gated). Rather than fail closed forever, dispatch an idle
//     hull to the yard (h.scanner) so the NEXT tick's live read succeeds. The price guard is NOT weakened
//     — no buy fires this tick; we make the price readable, not bypass it.
//   - BUY-TO-TARGET: once readable, buy up to (target-count) probes in a loop, each iteration honoring the
//     flat common.ImmutableReserveFloor capital gate against the DECREMENTING treasury (the running spend
//     is subtracted so the guard reflects real remaining credits — never a stale-treasury overspend). The
//     yard ask is stable within a tick, so a single PriceCheck feeds the whole loop.
func (h *RunBootstrapCoordinatorHandler) acquireProbesToTarget(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	// Readiness gate, second half: unblocked? The batch-purchase path needs an idle hull to fly to
	// the yard. No idle hull ⇒ BLOCKED (not failed) — a later tick with a free hull retries.
	if !obs.HasIdlePurchaser {
		res.Blocker = "no_purchaser"
		logger.Log("WARN", fmt.Sprintf("Bootstrap probe needed (%d/%d) but BLOCKED: no idle hull to execute the purchase", obs.ProbeCount, cfg.ProbeTarget), map[string]interface{}{
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
	// at the yard (sp-hh0h). Still fails CLOSED (no spend) — a genuinely unreadable price buys nothing.
	price, yard, readable, err := h.acquirer.PriceCheck(ctx, cmd.PlayerID, cfg.ProbeShipType)
	if err != nil || !readable {
		h.ensureShipyardReadable(ctx, cmd, cfg, obs, res, err)
		return
	}

	// sp-muc5x: a hull is at the yard NOW (the probe price just read), so the presence-gated hauler price is
	// readable at this same GetShipyard moment. SEED the last-hauler-price cache so INCOME knows the
	// first-hauler affordability BEFORE it ever frees the command frigate to position the buyer — the
	// cold-start deadlock this bead fixes (the frigate freed while permanently unaffordable, no earner left).
	// Read-only + best-effort (no spend — the price guard is untouched, RULINGS #4): a nil hauler acquirer or
	// an unreadable hauler listing caches nothing and the seed simply retries on the next readable DATA tick.
	h.seedHaulerPriceFromYard(ctx, cmd, cfg)

	// Capital-gated buy LOOP: buy up to (target-count) probes THIS tick, decrementing the treasury each
	// iteration so the flat common.ImmutableReserveFloor gate reflects real remaining credits (sp-05glh:
	// an additive cushion — treasury−price ≥ floor — replacing the deleted proportional reserve_margin
	// gate).
	need := cfg.ProbeTarget - obs.ProbeCount
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

		if cfg.DryRun {
			res.WouldBuy++
			spent += price // model the cumulative spend so the dry-run count respects the same gate
			logger.Log("INFO", fmt.Sprintf("Bootstrap DRY-RUN: WOULD buy %s #%d/%d at %s for %d (took no action)", cfg.ProbeShipType, obs.ProbeCount+i+1, cfg.ProbeTarget, yard, price), map[string]interface{}{
				"action":       "bootstrap_would_buy",
				"container_id": cmd.ContainerID,
			})
			continue
		}

		bought, berr := h.acquirer.Buy(ctx, cmd.PlayerID, cfg.ProbeShipType, yard)
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
		logger.Log("INFO", fmt.Sprintf("Bootstrap bought probe %s at %s for %d (%d/%d)", bought.ShipSymbol, yard, bought.Price, obs.ProbeCount+res.Purchased, cfg.ProbeTarget), map[string]interface{}{
			"action":       "bootstrap_bought_probe",
			"container_id": cmd.ContainerID,
			"ship":         bought.ShipSymbol,
			"price":        bought.Price,
		})
	}
}

// ensureShipyardReadable breaks the cold-start deadlock (sp-hh0h): the home shipyard price is unreadable
// because nothing has visited it yet, so — rather than fail closed forever — position an idle hull AT
// the yard so the NEXT tick's live PriceCheck returns prices. It NEVER buys and NEVER weakens the price
// guard: this tick still spends nothing. Nil-safe: with no scanner wired (or in dry-run) it preserves
// the pre-hh0h fail-closed behavior (blocker=price_unreadable, no repositioning) — byte-identical. The
// scanner is idempotent (a no-op when a hull is already positioned/en route), so calling it each
// unreadable tick never re-navigates.
func (h *RunBootstrapCoordinatorHandler) ensureShipyardReadable(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult, priceErr error) {
	logger := common.LoggerFromContext(ctx)

	if h.scanner == nil {
		res.Blocker = "price_unreadable"
		logger.Log("WARN", fmt.Sprintf("Bootstrap probe price unreadable and no shipyard scanner wired — failing closed (no buy): err=%v", priceErr), map[string]interface{}{
			"action":       "bootstrap_buy_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "price_unreadable",
		})
		return
	}

	if cfg.DryRun {
		res.Blocker = "price_unreadable"
		logger.Log("INFO", "Bootstrap DRY-RUN: probe price unreadable — WOULD position an idle hull at the home shipyard to make it readable (took no action)", map[string]interface{}{
			"action":       "bootstrap_would_position_purchaser",
			"container_id": cmd.ContainerID,
		})
		return
	}

	dispatched, serr := h.scanner.EnsureHomeShipyardReadable(ctx, cmd.PlayerID, obs.HomeSystem)
	if serr != nil {
		res.Blocker = "price_unreadable"
		logger.Log("WARN", fmt.Sprintf("Bootstrap probe price unreadable and shipyard positioning failed — failing closed (no buy): %v", serr), map[string]interface{}{
			"action":       "bootstrap_buy_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "price_unreadable",
		})
		return
	}
	if dispatched {
		res.Blocker = "positioning_purchaser_at_shipyard"
		logger.Log("INFO", fmt.Sprintf("Bootstrap probe price unreadable (cold home shipyard) — dispatched an idle hull to the home-system shipyard so the next tick's live price read succeeds (sp-hh0h); probes %d/%d", obs.ProbeCount, cfg.ProbeTarget), map[string]interface{}{
			"action":       "bootstrap_positioning_purchaser",
			"container_id": cmd.ContainerID,
			"blocker":      "positioning_purchaser_at_shipyard",
		})
		return
	}
	// Not dispatched: a hull is already present/en route at a shipyard (price should clear soon) or none
	// is free to send. Keep price_unreadable so the heartbeat shows we are still waiting on the read.
	res.Blocker = "price_unreadable"
	logger.Log("INFO", fmt.Sprintf("Bootstrap probe price unreadable — a hull is already at/en route to the home shipyard, or none is free; awaiting a readable price (probes %d/%d)", obs.ProbeCount, cfg.ProbeTarget), map[string]interface{}{
		"action":       "bootstrap_buy_blocked",
		"container_id": cmd.ContainerID,
		"blocker":      "price_unreadable",
	})
}

// buyBlockNote annotates the decision line with what would have blocked, so the one line carries
// the whole guardrail story.
func buyBlockNote(affordable bool) string {
	if affordable {
		return "clears the capital gate"
	}
	return "BLOCKED by the capital gate (would drop the cushion below the immutable reserve floor)"
}

// declareHomeScoutPost declares the STANDING home-system scout post as a coverage target (sp-pt7d):
// the desired state the boot-standing scout-post coordinator (sp-9ujl) mans by claiming an IDLE
// probe. It does NOT assign or dedicate a probe — that is the coordinator's job — so bootstrap's
// probes stay idle and claimable. Idempotent: the declarer skips a post that already exists, so
// re-declaring every DATA tick is a no-op. Caller has checked HomeSystem is resolved.
func (h *RunBootstrapCoordinatorHandler) declareHomeScoutPost(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", fmt.Sprintf("Bootstrap DRY-RUN: WOULD declare the home scout post %s for the scout-post coordinator to man (took no action)", obs.HomeSystem), map[string]interface{}{
			"action":       "bootstrap_would_declare_home_post",
			"container_id": cmd.ContainerID,
		})
		return
	}

	if h.postDeclarer == nil {
		res.Blocker = "no_scout_post_declarer"
		logger.Log("WARN", "Bootstrap cannot declare the home scout post: no scout-post declarer wired — probes will be bought but no coverage target exists for the coordinator to man", map[string]interface{}{
			"action":       "bootstrap_scout_post_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_scout_post_declarer",
		})
		return
	}

	if err := h.postDeclarer.DeclareHomeScoutPost(ctx, cmd.PlayerID, obs.HomeSystem, cfg.ProbeTarget); err != nil {
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
func (h *RunBootstrapCoordinatorHandler) emitHeartbeat(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, phase Phase, obs Observation, res reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	delta := fmt.Sprintf("bought=%d home_post=%v haulers_bought=%d trade_seeded=%v frigate_retired=%v batch_contract=%v frigate_loop=%v construction_started=%v mfg_ensured=%v mfg_bounced=%v workers_released=%d gate_workers_bought=%d handoff=%v", res.Purchased, res.HomePostDeclared, res.HaulersBought, res.TradeHullSeeded, res.FrigateRetired, res.ContractRun, res.FrigateLoopStarted, res.ConstructionStartRan, res.MfgEnsured, res.MfgBounced, res.WorkersReleased, res.GateWorkersBought, res.HandoffLaunched)
	if cfg.DryRun {
		delta = fmt.Sprintf("would_buy=%d (dry-run)", res.WouldBuy)
	}
	next := h.nextAction(cfg, phase, obs)
	blockers := res.Blocker
	if blockers == "" {
		blockers = "none"
	}

	logger.Log("INFO", fmt.Sprintf("Bootstrap heartbeat: phase=%s probes=%d/%d scouting=%d coverage=%d/%d (%.0f%%) haulers=%d/%d hubs=%d income/hr=%.0f/%.0f treasury=%d gate_site=%s construction=%.0f%% gate_workers=%d/%d · %s · next=%q · blockers=%s",
		phase, obs.ProbeCount, cfg.ProbeTarget, obs.ProbesScouting, obs.MarketsCovered, obs.MarketsTotal, obs.CoverageFraction()*100, len(obs.Haulers), cfg.HaulerTarget, res.ViableHubs, obs.IncomePerHour, cfg.IncomeBar, obs.Treasury, gateSiteOrNone(obs.GateSite), obs.ConstructionPercent, obs.GateWorkers, res.DesiredWorkers, delta, next, blockers), map[string]interface{}{
		"action":             "bootstrap_heartbeat",
		"container_id":       cmd.ContainerID,
		"phase":              string(phase),
		"probes":             obs.ProbeCount,
		"probe_target":       cfg.ProbeTarget,
		"probes_scouting":    obs.ProbesScouting,
		"markets_covered":    obs.MarketsCovered,
		"markets_total":      obs.MarketsTotal,
		"haulers":            len(obs.Haulers),
		"hauler_target":      cfg.HaulerTarget,
		"trade_hulls":        obs.TradeHullCount,
		"viable_hubs":        res.ViableHubs,
		"income_per_hour":    obs.IncomePerHour,
		"income_bar":         cfg.IncomeBar,
		"treasury":           obs.Treasury,
		"purchased":          res.Purchased,
		"haulers_bought":     res.HaulersBought,
		"trade_seeded":       res.TradeHullSeeded,
		"frigate_retired":    res.FrigateRetired,
		"batch_contract":     res.ContractRun,
		"frigate_loop":       res.FrigateLoopStarted,
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
func (h *RunBootstrapCoordinatorHandler) nextAction(cfg bootstrapRunConfig, phase Phase, obs Observation) string {
	switch phase {
	case PhaseData:
		// DATA runs in parallel with INCOME (contracts) at cold start (sp-t39j); this names the scanning
		// workstream's next step (the income workstream logs its own decision lines).
		if obs.ProbeCount < cfg.ProbeTarget {
			return fmt.Sprintf("buy probes to target (%d/%d, capital-gated; positions a hull at the yard first if the price is cold)", obs.ProbeCount, cfg.ProbeTarget)
		}
		if obs.ProbeCount > 0 && obs.ProbesScouting < obs.ProbeCount {
			return "home scout post declared — awaiting the scout-post coordinator to man idle probe(s) (sp-pt7d)"
		}
		return fmt.Sprintf("scan home system in parallel with contracts (coverage %.0f%%)", obs.CoverageFraction()*100)
	case PhaseIncome:
		if obs.CommandFrigateOnContract {
			return "retire the command frigate from contract work"
		}
		if !obs.BatchContractRunning {
			return "launch batch-contract on the contract fleet"
		}
		if obs.CommandFrigateID != "" && obs.ProbeCount >= cfg.ProbeTarget && !obs.FrigateContractLoopRunning && len(obs.Haulers) == 0 && !obs.CommandFrigatePurchasing {
			return "start the command frigate's continuous contract loop (pre-hauler sole earner)"
		}
		desired := len(selectContractHubs(obs.Markets, obs.ContractGoods))
		if desired > cfg.HaulerTarget {
			desired = cfg.HaulerTarget
		}
		if len(obs.Haulers) < desired {
			return fmt.Sprintf("buy contract hauler %d/%d (staged, capital-gated, hub-placed)", len(obs.Haulers)+1, desired)
		}
		return fmt.Sprintf("await realized $/hr ≥ bar (%.0f/%.0f)", obs.IncomePerHour, cfg.IncomeBar)
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
		plan := planGateWorkers(obs, cfg)
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

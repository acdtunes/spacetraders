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
// The tune mechanism is integer-only (liveconfig.PositiveInt), so the two fraction knobs are
// expressed as integer PERCENTS (coverage_bar_percent, reserve_margin_percent) and income_bar as
// whole credits — the coordinator divides the percents by 100 on read. The two ship-type knobs
// (probe_ship_type, hauler_ship_type) are deliberately NOT tunable: a string asset is launch-config
// only across every coordinator (a hull type is not swapped mid-run). These keys are the SEPARATE
// bare family — distinct from the config.yaml-authoritative prefixed bootstrap_* launch keys — so a
// tune is never cleared by the launch-config rebuild and survives a daemon bounce (RULINGS #2).
func BootstrapTunableDefaults() map[string]int {
	return map[string]int{
		"probe_target":           defaultProbeTarget,
		"coverage_bar_percent":   int(math.Round(defaultCoverageBar * 100)),
		"reserve_margin_percent": int(math.Round(defaultReserveMargin * 100)),
		"hauler_target":          defaultHaulerTarget,
		"income_bar":             int(math.Round(defaultIncomeBar)),
		"min_contract_earners":   defaultMinContractEarners,
		"gate_worker_target":     defaultGateWorkerTarget,
		"tick_secs":              defaultBootstrapTickSeconds,
		// sp-tsn2 single-buyer arbitration flag (0=off default, 1=on). A tunable flag with no launch key.
		"defer_probe_to_freshsizer": defaultDeferProbeToFreshsizer,
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
	ReserveMargin float64
	ProbeShipType string

	// INCOME-phase knobs (Slice 2), each resolved to its documented default when unset.
	HaulerTarget       int
	IncomeBar          float64
	MinContractEarners int
	HaulerShipType     string

	// GATE-phase knob (Slice 3), resolved to its documented default when unset.
	GateWorkerTarget int

	// DeferProbeToFreshsizer arms the sp-tsn2 single-buyer arbitration: when true, bootstrap DEFERS
	// its DATA probe buy to the freshsizer once coverage>0 and a freshsizer coordinator is running, so
	// exactly one buyer grows the shared fleet during the conflict window. Default false
	// (byte-identical). A tunable flag (defer_probe_to_freshsizer) — armed live, no launch key.
	DeferProbeToFreshsizer bool
}

func resolveBootstrapConfig(cmd *RunBootstrapCoordinatorCommand, live liveconfig.Snapshot) bootstrapRunConfig {
	c := bootstrapRunConfig{
		Disabled:      cmd.Disabled,
		DryRun:        cmd.DryRun,
		Tick:          time.Duration(cmd.TickIntervalSecs) * time.Second,
		ProbeTarget:   cmd.ProbeTarget,
		CoverageBar:   cmd.CoverageBar,
		ReserveMargin: cmd.ReserveMargin,
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
	// absent and must not zero the launch value — byte-identical when nothing is tuned. The two
	// fraction knobs decode from integer percent; income_bar is whole credits; the <=0 default
	// fallbacks below still apply to any knob left unset by both the launch command and the overlay.
	if live != nil {
		if v := live.PositiveIntOrZero("probe_target"); v > 0 {
			c.ProbeTarget = v
		}
		if v := live.PositiveIntOrZero("coverage_bar_percent"); v > 0 {
			c.CoverageBar = float64(v) / 100.0
		}
		if v := live.PositiveIntOrZero("reserve_margin_percent"); v > 0 {
			c.ReserveMargin = float64(v) / 100.0
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
	if c.ReserveMargin <= 0 {
		c.ReserveMargin = defaultReserveMargin
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
	return c
}

// reconcileResult tallies one tick's effect for the heartbeat and the tests.
type reconcileResult struct {
	Phase     Phase
	Purchased int    // probes actually bought this tick (DATA)
	WouldBuy  int    // ships a dry-run WOULD have bought this tick (DATA probe or INCOME hauler)
	Scouted   bool   // scout-all-markets assignment ran this tick (DATA)
	Blocker   string // the one guard that blocked the highest-priority action (for the heartbeat)

	// INCOME tallies (Slice 2).
	HaulersBought      int  // contract haulers actually bought this tick (staged: at most 1)
	FrigateRetired     bool // the command frigate was retired from contract work this tick
	ContractRun        bool // batch-contract was launched this tick
	FrigateLoopStarted bool // the command frigate's continuous contract loop was started this tick (sp-rype)
	ViableHubs         int  // viable contract hubs the selector found (for the heartbeat)

	// GATE tallies (Slice 3).
	ConstructionStartRan bool // `construction start` ran this tick (created/resumed the pipeline)
	MfgEnsured           bool // the manufacturing coordinator (executor) was ensured-running this tick
	MfgBounced           bool // the executor was bounced for pipeline adoption this tick (captain L57)
	WorkersReleased      int  // contract haulers released to construction this tick (repurpose-first)
	GateWorkersBought    int  // gate-worker hulls actually bought this tick (staged: at most 1)
	DesiredWorkers       int  // the tick's gate-worker sizing target (for the heartbeat)

	// COMPLETE tallies (Slice 3).
	HandoffLaunched bool // the autosizer + standing coordinators were launched this tick (the hand-off)
	Done            bool // terminal: COMPLETE reached and handed off — the reconcile loop may exit
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
	// consistent effective count. It only ADJUSTS the count; the money guard (reserve_margin) is
	// untouched. The bridge decays to zero as the observation catches up (see probeBuyBridge).
	bridge := h.probeBridge(cmd.ContainerID)
	obs.ProbeCount = bridge.effectiveProbeCount(obs.ProbeCount)

	// Derive the phase from the observation — NEVER from a persisted enum (spec §Architecture).
	phase := derivePhase(obs, cfg)
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
	case PhaseComplete:
		h.actComplete(ctx, cmd, cfg, obs, &res)
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
// INCOME label below only chooses whether the SCANNING workstream is still active (coverage under the
// bar ⇒ DATA-labeled, still buying/assigning probes); the contract workstream runs in BOTH (the tick
// dispatch runs actIncome in the DATA phase too). The MarketsTotal>0 guard keeps a cold agent from
// reading an empty world as "100% covered".
//
// The arc must be MONOTONE, but realized income is NOT monotone across the INCOME→GATE boundary: GATE
// repurposes contract haulers to construction, which DROPS realized $/hr back under income_bar. So GATE
// is made STICKY on obs.ConstructionStarted — once a construction pipeline exists the arc stays in GATE
// regardless of income, never regressing (which would re-buy the just-repurposed haulers and thrash).
// COMPLETE is terminal and monotone (a built gate stays built). A restart at any point re-derives the
// true phase from these live signals — no persisted cursor, no double-advance. A fleet PAST cold-start
// has coverage ≥ bar, so evaluating the economic signals first is byte-identical for it (the coverage
// check it used to pass first is satisfied anyway); only the cold-start window changes.
func derivePhase(obs Observation, cfg bootstrapRunConfig) Phase {
	if obs.ConstructionComplete {
		return PhaseComplete
	}
	if obs.ConstructionStarted {
		return PhaseGate // sticky: stay in GATE even as repurposed haulers pull income under the bar
	}
	if obs.IncomePerHour >= cfg.IncomeBar {
		return PhaseGate
	}
	// Not yet funded/building. Label DATA while the home system is still being scanned (below the bar),
	// else INCOME — but the contract workstream runs regardless of this label (see the tick dispatch).
	if obs.MarketsTotal > 0 && obs.CoverageFraction() >= cfg.CoverageBar {
		return PhaseIncome
	}
	return PhaseData
}

// actData runs the DATA (scanning) workstream: (1) drive the probe fleet to probe_target THIS tick —
// buying up to (target-count) probes in a capital-gated loop, or (when the home shipyard price is not
// yet readable) positioning a hull at the yard so the next tick's live read succeeds (sp-hh0h);
// (2) assign every probe to scout-all-markets when any probe is not yet scouting. Both actions are
// independently guarded and idempotent, so re-evaluation never double-acts. It runs in the DATA phase,
// which — under the parallel model (sp-t39j) — executes ALONGSIDE actIncome during cold start.
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

	// (2) Assign every probe to scout-all-markets. Idempotent: skip when every probe already
	// scouts (else the VRP re-optimizes across the current probe set each call).
	if obs.ProbeCount > 0 && obs.ProbesScouting < obs.ProbeCount {
		h.assignScouting(ctx, cmd, cfg, obs, res)
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
//     reserve_margin capital gate against the DECREMENTING treasury (the running spend is subtracted so the
//     guard reflects real remaining credits — never a stale-treasury overspend). The yard ask is stable
//     within a tick, so a single PriceCheck feeds the whole loop.
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

	// Capital-gated buy LOOP: buy up to (target-count) probes THIS tick, decrementing the treasury each
	// iteration so the reserve_margin gate reflects real remaining credits.
	need := cfg.ProbeTarget - obs.ProbeCount
	var spent int64
	for i := 0; i < need; i++ {
		remaining := obs.Treasury - spent
		capBudget := int64(float64(remaining) * cfg.ReserveMargin)
		affordable := price <= capBudget
		logger.Log("INFO", fmt.Sprintf("Bootstrap probe buy decision (%d of %d needed): price=%d treasury=%d spent_so_far=%d remaining=%d cap=(reserve_margin %.2f × remaining)=%d affordable=(price≤cap)=%v yard=%s — %s", i+1, need, price, obs.Treasury, spent, remaining, cfg.ReserveMargin, capBudget, affordable, yard, buyBlockNote(affordable)), map[string]interface{}{
			"action":         "bootstrap_buy_decision",
			"container_id":   cmd.ContainerID,
			"price":          price,
			"treasury":       obs.Treasury,
			"remaining":      remaining,
			"cap":            capBudget,
			"reserve_margin": cfg.ReserveMargin,
			"affordable":     affordable,
			"yard":           yard,
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
	return "BLOCKED by the capital gate (would exceed reserve_margin × treasury)"
}

// assignScouting assigns every probe to scout-all-markets (reuse the VRP fleet assignment). Caller
// has checked that at least one probe is not yet scouting.
func (h *RunBootstrapCoordinatorHandler) assignScouting(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if obs.HomeSystem == "" {
		res.Blocker = "no_home_system"
		logger.Log("WARN", "Bootstrap cannot assign scouting: home system unresolved", map[string]interface{}{
			"action":       "bootstrap_scout_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_home_system",
		})
		return
	}

	if cfg.DryRun {
		logger.Log("INFO", fmt.Sprintf("Bootstrap DRY-RUN: WOULD assign %d probe(s) to scout-all-markets in %s (%d already scouting) (took no action)", obs.ProbeCount, obs.HomeSystem, obs.ProbesScouting), map[string]interface{}{
			"action":       "bootstrap_would_scout",
			"container_id": cmd.ContainerID,
		})
		return
	}

	if h.scouter == nil {
		res.Blocker = "no_scouter"
		logger.Log("WARN", "Bootstrap has probes to scout but no scout assigner wired", map[string]interface{}{
			"action":       "bootstrap_scout_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_scouter",
		})
		return
	}

	if err := h.scouter.AssignAllMarkets(ctx, cmd.PlayerID, obs.HomeSystem); err != nil {
		res.Blocker = "scout_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap scout assignment failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_scout_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.Scouted = true
	logger.Log("INFO", fmt.Sprintf("Bootstrap assigned %d probe(s) to scout-all-markets in %s (%d were already scouting)", obs.ProbeCount, obs.HomeSystem, obs.ProbesScouting), map[string]interface{}{
		"action":       "bootstrap_scout_assigned",
		"container_id": cmd.ContainerID,
		"probes":       obs.ProbeCount,
		"system":       obs.HomeSystem,
	})
}

// emitHeartbeat writes the per-tick progress line (phase · delta done · next action · blockers) so
// a wedged reconciler is visible, never a silent stall (captain L61, spec §Observability).
func (h *RunBootstrapCoordinatorHandler) emitHeartbeat(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, phase Phase, obs Observation, res reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	delta := fmt.Sprintf("bought=%d scouted=%v haulers_bought=%d frigate_retired=%v batch_contract=%v frigate_loop=%v construction_started=%v mfg_ensured=%v mfg_bounced=%v workers_released=%d gate_workers_bought=%d handoff=%v", res.Purchased, res.Scouted, res.HaulersBought, res.FrigateRetired, res.ContractRun, res.FrigateLoopStarted, res.ConstructionStartRan, res.MfgEnsured, res.MfgBounced, res.WorkersReleased, res.GateWorkersBought, res.HandoffLaunched)
	if cfg.DryRun {
		delta = fmt.Sprintf("would_buy=%d (dry-run)", res.WouldBuy)
	}
	next := h.nextAction(cfg, phase, obs)
	blockers := res.Blocker
	if blockers == "" {
		blockers = "none"
	}

	logger.Log("INFO", fmt.Sprintf("Bootstrap heartbeat: phase=%s probes=%d/%d scouting=%d coverage=%d/%d (%.0f%%/%.0f%% bar) haulers=%d/%d hubs=%d income/hr=%.0f/%.0f treasury=%d gate_site=%s construction=%.0f%% gate_workers=%d/%d · %s · next=%q · blockers=%s",
		phase, obs.ProbeCount, cfg.ProbeTarget, obs.ProbesScouting, obs.MarketsCovered, obs.MarketsTotal, obs.CoverageFraction()*100, cfg.CoverageBar*100, len(obs.Haulers), cfg.HaulerTarget, res.ViableHubs, obs.IncomePerHour, cfg.IncomeBar, obs.Treasury, gateSiteOrNone(obs.GateSite), obs.ConstructionPercent, obs.GateWorkers, res.DesiredWorkers, delta, next, blockers), map[string]interface{}{
		"action":           "bootstrap_heartbeat",
		"container_id":     cmd.ContainerID,
		"phase":            string(phase),
		"probes":           obs.ProbeCount,
		"probe_target":     cfg.ProbeTarget,
		"probes_scouting":  obs.ProbesScouting,
		"markets_covered":  obs.MarketsCovered,
		"markets_total":    obs.MarketsTotal,
		"haulers":          len(obs.Haulers),
		"hauler_target":    cfg.HaulerTarget,
		"viable_hubs":      res.ViableHubs,
		"income_per_hour":  obs.IncomePerHour,
		"income_bar":       cfg.IncomeBar,
		"treasury":         obs.Treasury,
		"purchased":        res.Purchased,
		"haulers_bought":   res.HaulersBought,
		"frigate_retired":  res.FrigateRetired,
		"batch_contract":   res.ContractRun,
		"frigate_loop":     res.FrigateLoopStarted,
		"scouted":          res.Scouted,
		"gate_site":        obs.GateSite,
		"construction_pct": obs.ConstructionPercent,
		"gate_workers":     obs.GateWorkers,
		"desired_workers":  res.DesiredWorkers,
		"workers_released": res.WorkersReleased,
		"handoff":          res.HandoffLaunched,
		"blocker":          blockers,
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
			return "assign probes to scout-all-markets"
		}
		return fmt.Sprintf("scan to coverage bar in parallel with contracts (%.0f%%/%.0f%%)", obs.CoverageFraction()*100, cfg.CoverageBar*100)
	case PhaseIncome:
		if obs.CommandFrigateOnContract {
			return "retire the command frigate from contract work"
		}
		if !obs.BatchContractRunning {
			return "launch batch-contract on the contract fleet"
		}
		if obs.CommandFrigateID != "" && obs.ProbeCount >= cfg.ProbeTarget && !obs.FrigateContractLoopRunning {
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
	case PhaseComplete:
		if !obs.AutosizerRunning {
			return "launch the fleet-autosizer + standing coordinators (hand-off)"
		}
		return "COMPLETE — gate built, economy handed off, exiting"
	default:
		return fmt.Sprintf("phase %s unhandled", phase)
	}
}

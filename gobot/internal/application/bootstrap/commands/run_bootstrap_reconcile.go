package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

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
}

func resolveBootstrapConfig(cmd *RunBootstrapCoordinatorCommand) bootstrapRunConfig {
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
	HaulersBought  int  // contract haulers actually bought this tick (staged: at most 1)
	FrigateRetired bool // the command frigate was retired from contract work this tick
	ContractRun    bool // batch-contract was launched this tick
	ViableHubs     int  // viable contract hubs the selector found (for the heartbeat)

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

// reconcileOnce runs one full pass: phantom-cache refresh → observe → derive phase → act on the
// delta → heartbeat. It is the unit the tests drive directly; Handle just calls it on the tick.
// Every side-effecting step is guarded "already done / in-flight?" and fails CLOSED on an
// unreadable input, so re-evaluation (including the first tick after a restart) never double-acts.
func (h *RunBootstrapCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunBootstrapCoordinatorCommand) (reconcileResult, error) {
	cfg := resolveBootstrapConfig(cmd)
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
		h.actData(ctx, cmd, cfg, obs, &res)
	case PhaseIncome:
		h.actIncome(ctx, cmd, cfg, obs, &res)
	case PhaseGate:
		h.actGate(ctx, cmd, cfg, obs, &res)
	case PhaseComplete:
		h.actComplete(ctx, cmd, cfg, obs, &res)
	}

	h.emitHeartbeat(ctx, cmd, cfg, phase, obs, res)
	return res, nil
}

// derivePhase reads the current phase from the observation alone (NEVER a persisted enum — spec
// §Architecture). DATA is the cold-start default; once market coverage clears the bar the arc has
// passed DATA. Past DATA it is INCOME until the contract fleet's realized $/hr clears income_bar, then
// GATE, then COMPLETE once the gate is built. The MarketsTotal>0 guard keeps a cold agent (nothing
// scouted yet) in DATA rather than reading an empty world as "100% covered"; income_bar is positive by
// default, so a fresh INCOME entry (0 $/hr) never skips straight to GATE.
//
// The arc must be MONOTONE, but realized income is NOT monotone across the INCOME→GATE boundary: GATE
// repurposes contract haulers to construction, which DROPS realized $/hr back under income_bar. So GATE
// is made STICKY on obs.ConstructionStarted — once a construction pipeline exists the arc stays in GATE
// regardless of income, never regressing to INCOME (which would re-buy the just-repurposed haulers and
// thrash). COMPLETE is terminal and monotone (a built gate stays built). A restart at any point
// re-derives the true phase from these live signals — no persisted cursor, no double-advance.
func derivePhase(obs Observation, cfg bootstrapRunConfig) Phase {
	if !(obs.MarketsTotal > 0 && obs.CoverageFraction() >= cfg.CoverageBar) {
		return PhaseData
	}
	if obs.ConstructionComplete {
		return PhaseComplete
	}
	if obs.ConstructionStarted {
		return PhaseGate // sticky: stay in GATE even as repurposed haulers pull income under the bar
	}
	if obs.IncomePerHour >= cfg.IncomeBar {
		return PhaseGate
	}
	return PhaseIncome
}

// actData runs the DATA phase: (1) buy one probe if the fleet is short AND the buy clears both the
// readiness and capital gates; (2) assign every probe to scout-all-markets when any probe is not
// yet scouting. Both actions are independently guarded and idempotent, so re-evaluation never
// double-acts.
func (h *RunBootstrapCoordinatorHandler) actData(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	// (1) Staged, capital-gated probe acquisition — at most one buy per tick (never a blind
	// buy-all). Guarded on the re-observed count, so a mid-purchase restart that already
	// incremented the count simply skips.
	if obs.ProbeCount < cfg.ProbeTarget {
		h.maybeBuyProbe(ctx, cmd, cfg, obs, res)
	}

	// (2) Assign every probe to scout-all-markets. Idempotent: skip when every probe already
	// scouts (else the VRP re-optimizes across the current probe set each call).
	if obs.ProbeCount > 0 && obs.ProbesScouting < obs.ProbeCount {
		h.assignScouting(ctx, cmd, cfg, obs, res)
	}
}

// maybeBuyProbe evaluates and (unless dry-run) executes ONE staged probe buy behind the readiness
// and capital gates, emitting the guardrail arithmetic as a decision line (RULINGS #4, fail
// closed). Caller has already checked "needed" (ProbeCount < target).
func (h *RunBootstrapCoordinatorHandler) maybeBuyProbe(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
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

	// Price-check first (reuse shipyard list). Unreadable price ⇒ the capital gate fails CLOSED.
	price, yard, readable, err := h.acquirer.PriceCheck(ctx, cmd.PlayerID, cfg.ProbeShipType)
	if err != nil || !readable {
		res.Blocker = "price_unreadable"
		logger.Log("WARN", fmt.Sprintf("Bootstrap probe price unreadable — failing closed (no buy): err=%v", err), map[string]interface{}{
			"action":       "bootstrap_buy_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "price_unreadable",
		})
		return
	}

	// Capital gate: spend ≤ reserve_margin × treasury (leaves the rest as the working buffer, and
	// paces the ramp). Emit the full arithmetic so the captain retunes from evidence.
	capBudget := int64(float64(obs.Treasury) * cfg.ReserveMargin)
	affordable := price <= capBudget
	logger.Log("INFO", fmt.Sprintf("Bootstrap probe buy decision: price=%d treasury=%d cap=(reserve_margin %.2f × treasury)=%d affordable=(price≤cap)=%v yard=%s — %s", price, obs.Treasury, cfg.ReserveMargin, capBudget, affordable, yard, buyBlockNote(affordable)), map[string]interface{}{
		"action":         "bootstrap_buy_decision",
		"container_id":   cmd.ContainerID,
		"price":          price,
		"treasury":       obs.Treasury,
		"cap":            capBudget,
		"reserve_margin": cfg.ReserveMargin,
		"affordable":     affordable,
		"yard":           yard,
	})
	if !affordable {
		res.Blocker = "capital_gate"
		return
	}

	if cfg.DryRun {
		res.WouldBuy++
		logger.Log("INFO", fmt.Sprintf("Bootstrap DRY-RUN: WOULD buy 1 %s at %s for %d (took no action)", cfg.ProbeShipType, yard, price), map[string]interface{}{
			"action":       "bootstrap_would_buy",
			"container_id": cmd.ContainerID,
		})
		return
	}

	bought, err := h.acquirer.Buy(ctx, cmd.PlayerID, cfg.ProbeShipType, yard)
	if err != nil {
		res.Blocker = "purchase_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap probe purchase failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_buy_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.Purchased++
	if h.metrics != nil {
		h.metrics.RecordProbePurchased()
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap bought probe %s at %s for %d (%d/%d)", bought.ShipSymbol, yard, bought.Price, obs.ProbeCount+1, cfg.ProbeTarget), map[string]interface{}{
		"action":       "bootstrap_bought_probe",
		"container_id": cmd.ContainerID,
		"ship":         bought.ShipSymbol,
		"price":        bought.Price,
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

	delta := fmt.Sprintf("bought=%d scouted=%v haulers_bought=%d frigate_retired=%v batch_contract=%v construction_started=%v mfg_ensured=%v mfg_bounced=%v workers_released=%d gate_workers_bought=%d handoff=%v", res.Purchased, res.Scouted, res.HaulersBought, res.FrigateRetired, res.ContractRun, res.ConstructionStartRan, res.MfgEnsured, res.MfgBounced, res.WorkersReleased, res.GateWorkersBought, res.HandoffLaunched)
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
		if obs.ProbeCount < cfg.ProbeTarget {
			return fmt.Sprintf("buy probe %d/%d (staged, capital-gated)", obs.ProbeCount+1, cfg.ProbeTarget)
		}
		if obs.ProbeCount > 0 && obs.ProbesScouting < obs.ProbeCount {
			return "assign probes to scout-all-markets"
		}
		return fmt.Sprintf("await coverage ≥ bar (%.0f%%/%.0f%%)", obs.CoverageFraction()*100, cfg.CoverageBar*100)
	case PhaseIncome:
		if obs.CommandFrigateOnContract {
			return "retire the command frigate from contract work"
		}
		if !obs.BatchContractRunning {
			return "launch batch-contract on the contract fleet"
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

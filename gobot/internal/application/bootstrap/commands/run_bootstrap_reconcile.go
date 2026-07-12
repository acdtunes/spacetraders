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
	return c
}

// reconcileResult tallies one tick's effect for the heartbeat and the tests.
type reconcileResult struct {
	Phase     Phase
	Purchased int    // probes actually bought this tick
	WouldBuy  int    // probes a dry-run WOULD have bought
	Scouted   bool   // scout-all-markets assignment ran this tick
	Blocker   string // the one guard that blocked the highest-priority action (for the heartbeat)
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
	}

	switch phase {
	case PhaseData:
		h.actData(ctx, cmd, cfg, obs, &res)
	default:
		// Slice-1 terminal: the DATA exit is met but the later phases are not implemented yet. This
		// is a clean hold, not an error (spec §Slices: "add a phase" is the whole of Slice 2/3).
		logger.Log("INFO", fmt.Sprintf("Bootstrap phase %s not yet implemented — holding at DATA-complete (coverage %d/%d = %.0f%% ≥ bar %.0f%%)", phase, obs.MarketsCovered, obs.MarketsTotal, obs.CoverageFraction()*100, cfg.CoverageBar*100), map[string]interface{}{
			"action":       "bootstrap_phase_not_implemented",
			"container_id": cmd.ContainerID,
			"phase":        string(phase),
		})
	}

	h.emitHeartbeat(ctx, cmd, cfg, phase, obs, res)
	return res, nil
}

// derivePhase reads the current phase from the observation alone. DATA is the cold-start default;
// once market coverage clears the bar the arc has passed DATA (→ INCOME, the next phase). The
// MarketsTotal>0 guard keeps a cold agent (nothing scouted yet) in DATA rather than reading an
// empty world as "100% covered".
func derivePhase(obs Observation, cfg bootstrapRunConfig) Phase {
	if obs.MarketsTotal > 0 && obs.CoverageFraction() >= cfg.CoverageBar {
		return PhaseIncome
	}
	return PhaseData
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
	cap := int64(float64(obs.Treasury) * cfg.ReserveMargin)
	affordable := price <= cap
	logger.Log("INFO", fmt.Sprintf("Bootstrap probe buy decision: price=%d treasury=%d cap=(reserve_margin %.2f × treasury)=%d affordable=(price≤cap)=%v yard=%s — %s", price, obs.Treasury, cfg.ReserveMargin, cap, affordable, yard, buyBlockNote(affordable)), map[string]interface{}{
		"action":         "bootstrap_buy_decision",
		"container_id":   cmd.ContainerID,
		"price":          price,
		"treasury":       obs.Treasury,
		"cap":            cap,
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

	delta := fmt.Sprintf("bought=%d scouted=%v", res.Purchased, res.Scouted)
	if cfg.DryRun {
		delta = fmt.Sprintf("would_buy=%d (dry-run)", res.WouldBuy)
	}
	next := h.nextAction(cfg, phase, obs)
	blockers := res.Blocker
	if blockers == "" {
		blockers = "none"
	}

	logger.Log("INFO", fmt.Sprintf("Bootstrap heartbeat: phase=%s probes=%d/%d scouting=%d coverage=%d/%d (%.0f%%/%.0f%% bar) treasury=%d · %s · next=%q · blockers=%s",
		phase, obs.ProbeCount, cfg.ProbeTarget, obs.ProbesScouting, obs.MarketsCovered, obs.MarketsTotal, obs.CoverageFraction()*100, cfg.CoverageBar*100, obs.Treasury, delta, next, blockers), map[string]interface{}{
		"action":          "bootstrap_heartbeat",
		"container_id":    cmd.ContainerID,
		"phase":           string(phase),
		"probes":          obs.ProbeCount,
		"probe_target":    cfg.ProbeTarget,
		"probes_scouting": obs.ProbesScouting,
		"markets_covered": obs.MarketsCovered,
		"markets_total":   obs.MarketsTotal,
		"treasury":        obs.Treasury,
		"purchased":       res.Purchased,
		"scouted":         res.Scouted,
		"blocker":         blockers,
	})
}

// nextAction names the single next thing the reconciler intends, for the heartbeat.
func (h *RunBootstrapCoordinatorHandler) nextAction(cfg bootstrapRunConfig, phase Phase, obs Observation) string {
	if phase != PhaseData {
		return fmt.Sprintf("phase %s not yet implemented — holding at DATA-complete", phase)
	}
	if obs.ProbeCount < cfg.ProbeTarget {
		return fmt.Sprintf("buy probe %d/%d (staged, capital-gated)", obs.ProbeCount+1, cfg.ProbeTarget)
	}
	if obs.ProbeCount > 0 && obs.ProbesScouting < obs.ProbeCount {
		return "assign probes to scout-all-markets"
	}
	return fmt.Sprintf("await coverage ≥ bar (%.0f%%/%.0f%%)", obs.CoverageFraction()*100, cfg.CoverageBar*100)
}

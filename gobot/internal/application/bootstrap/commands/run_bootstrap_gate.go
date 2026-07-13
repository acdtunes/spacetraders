package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/twinreport"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// This file holds the GATE phase (Slice 3, sp-ysgb.2): the jump-gate construction drive and its
// deterministic worker sizing, plus the COMPLETE hand-off. It mirrors run_bootstrap_income.go's shape —
// independently-guarded, idempotent actions on the observed delta, each failing CLOSED — so a restart
// mid-GATE re-observes construction %, the executor's adoption, and the worker pool, and never
// double-starts, double-bounces, or double-buys.

// gateWorkerPlan is the deterministic worker-sizing decision for one GATE tick (autosizer stays OFF the
// whole bootstrap run). It is computed by planGateWorkers from the observation alone and then executed
// by actGate behind the readiness/solvency gates.
type gateWorkerPlan struct {
	// Buy is the staged gate-delivery-hauler buy this tick (0 or 1 — never a blind buy-all), non-zero
	// only once the pipeline reveals its chains AND existing gate workers fall short of its shape.
	// Option B: the ENTIRE gate-delivery fleet is BOUGHT from contract income — contract haulers are
	// never repurposed, so the whole contract fleet keeps earning through GATE.
	Buy int
	// DesiredWorkers is the sizing target (~one per active gate-material chain + a delivery hauler, capped
	// at gate_worker_target). 0 until the pipeline reveals its chains — so the buy holds until the shape is known.
	DesiredWorkers int
}

// planGateWorkers sizes the gate-construction (delivery-hauler) workforce deterministically from the
// observation (spec §Fleet scaling & hand-off), in the Option-B all-bought model:
//
//  1. SIZE TO THE PIPELINE'S SHAPE — once construction reveals its producing chains, target ~one worker
//     per active chain + a delivery hauler, capped at gate_worker_target.
//  2. BUY THE WHOLE FLEET FROM INCOME — stage the delta (one hull per tick) whenever existing gate
//     workers fall short of that shape. Contract haulers are NEVER repurposed (Option B): the entire
//     contract fleet keeps earning, and the gate-delivery fleet is funded by that income (each buy is
//     solvency-gated on the shared working-capital floor, so fleet and materials can't starve each other).
//
// It is pure and idempotent: a restart mid-GATE re-derives the same plan from the re-observed pool, so a
// bought hull (next tick a GateWorker) shrinks the deficit and the buy stops — never an over-buy.
func planGateWorkers(obs Observation, cfg bootstrapRunConfig) gateWorkerPlan {
	// (1) The sizing target, revealed only once the pipeline exposes its producing chains.
	desired := 0
	if obs.GateMaterialChains > 0 {
		desired = obs.GateMaterialChains + gateDeliveryHaulers
		if desired > cfg.GateWorkerTarget {
			desired = cfg.GateWorkerTarget
		}
	}

	// (2) Buy the staged delta (at most one per tick) whenever the executor's already-claimed workers
	// fall short of the shape. The whole gate-delivery fleet is bought (no repurpose seed), so the pool
	// is exactly GateWorkers; a bought hull becomes a GateWorker next tick, shrinking the deficit until
	// the buy stops — never an over-buy.
	buy := 0
	if desired > obs.GateWorkers {
		buy = 1
	}

	return gateWorkerPlan{
		Buy:            buy,
		DesiredWorkers: desired,
	}
}

// gateSiteOrNone renders the gate site for the heartbeat, or "none" before it is discovered.
func gateSiteOrNone(site string) string {
	if site == "" {
		return "none"
	}
	return site
}

// gateSolvencyNote annotates the gate-worker buy decision line with what would have blocked, so the one
// line carries the whole solvency story (mirrors buyBlockNote for the shared working-capital floor).
func gateSolvencyNote(affordable bool) string {
	if affordable {
		return "clears the shared working-capital floor"
	}
	return "BLOCKED by the solvency gate (would drop treasury below the shared working-capital reserve)"
}

// actGate runs the GATE phase (Slice 3): drive the jump gate to construction. Its steps are ordered and
// each independently guarded against the FRESH observation, so re-evaluation — including the first tick
// after a restart — never double-starts the pipeline, double-bounces the executor, or double-buys a worker:
//
//  1. Gate-site check — the observer discovers the under-construction JUMP_GATE waypoint; without it GATE
//     is BLOCKED (a later tick with waypoint data retries), never a spend on an unknown target.
//  2. Start the construction pipeline ONCE (`construction start`), guarded on obs.ConstructionStarted.
//     On the tick that creates it, that is ALL — the observation still reads !started, so adoption + worker
//     sizing begin next tick once the pipeline is real (this avoids bouncing the executor before a pipeline exists).
//  3. Ensure the executor has ADOPTED the pipeline (captain L57): if it is down, EnsureRunning starts it
//     (a fresh start adopts existing pipelines); if it is up but has not adopted the new pipeline, bounce
//     it so a restart adopts. Running-and-adopted ⇒ nothing.
//  4. Size the gate-delivery fleet (planGateWorkers): BUY the whole fleet from contract income, one hull
//     per tick, each solvency-gated on the shared working-capital floor (Option B — contract haulers are
//     never repurposed, so the whole contract fleet keeps earning through GATE).
//
// The monitor→COMPLETE transition is derivePhase's job (obs.ConstructionComplete), so GATE has no explicit
// "is it done?" branch — it just reconciles the construction drive each tick until the phase flips.
func (h *RunBootstrapCoordinatorHandler) actGate(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	// (1) Gate-site discovery is the observer's job; without a site GATE cannot act (fail-closed).
	if obs.GateSite == "" {
		res.Blocker = "no_gate_site"
		logger.Log("WARN", "Bootstrap GATE but no under-construction jump-gate site discovered yet — holding (fail-closed, retries when waypoint data lands)", map[string]interface{}{
			"action":       "bootstrap_gate_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_gate_site",
		})
		return
	}

	// (2) Start the pipeline once. On the creating tick, do nothing else — the observation still reads
	// !started, so adoption + sizing wait for next tick when the pipeline is real.
	if !obs.ConstructionStarted {
		h.startConstruction(ctx, cmd, cfg, obs, res)
		return
	}

	// (3) Ensure the executor is running AND has adopted the pipeline (the L57 adoption bounce).
	h.ensureExecutorAdopted(ctx, cmd, cfg, obs, res)

	// (4) Size the gate-delivery fleet: buy the whole fleet from contract income (staged, solvency-gated).
	h.sizeGateWorkers(ctx, cmd, cfg, obs, res)
}

// startConstruction drives `construction start <site>` once (idempotent at the adapter — it resumes an
// existing pipeline). Caller has checked obs.ConstructionStarted is false.
func (h *RunBootstrapCoordinatorHandler) startConstruction(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", fmt.Sprintf("Bootstrap DRY-RUN: WOULD start construction pipeline for gate site %s (took no action)", obs.GateSite), map[string]interface{}{
			"action":       "bootstrap_would_start_construction",
			"container_id": cmd.ContainerID,
			"site":         obs.GateSite,
		})
		return
	}
	if h.construction == nil {
		res.Blocker = "no_construction_manager"
		logger.Log("WARN", "Bootstrap GATE needs to start construction but no construction manager wired", map[string]interface{}{
			"action":       "bootstrap_gate_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_construction_manager",
		})
		return
	}
	if err := h.construction.Start(ctx, cmd.PlayerID, obs.GateSite); err != nil {
		res.Blocker = "construction_start_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap construction start failed for %s: %v", obs.GateSite, err), map[string]interface{}{
			"action":       "bootstrap_construction_start_error",
			"container_id": cmd.ContainerID,
			"site":         obs.GateSite,
		})
		return
	}
	res.ConstructionStartRan = true
	logger.Log("INFO", fmt.Sprintf("Bootstrap started the construction pipeline for jump-gate site %s — the manufacturing coordinator will adopt it (bounce next tick if already running)", obs.GateSite), map[string]interface{}{
		"action":       "bootstrap_construction_started",
		"container_id": cmd.ContainerID,
		"site":         obs.GateSite,
	})
	twinreport.Report("construction-start", nil) // test-gated: no /v2 call for the twin to observe
}

// ensureExecutorAdopted makes the construction executor (the manufacturing coordinator) both RUNNING and
// having ADOPTED the gate pipeline. A freshly-created pipeline is inert until the executor adopts it at
// startup (captain L57), so: not running ⇒ start it (a fresh start adopts); running-but-not-adopted ⇒
// bounce it (a restart adopts); running-and-adopted ⇒ nothing. Each branch is guarded on the observation,
// so a restart mid-GATE re-derives the right one and never double-acts. Caller has checked the pipeline exists.
func (h *RunBootstrapCoordinatorHandler) ensureExecutorAdopted(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	// Adoption short-circuits FIRST: if the pipeline is being worked (adopted), we are done regardless of
	// whether a separate executor container was detected — this keeps GATE quiet whether the executor is a
	// standing container or the daemon works the tasks directly, and never false-fires ensure/bounce on a
	// healthily-progressing pipeline.
	if obs.ManufacturingAdopted {
		return
	}

	if h.manufacturing == nil {
		res.Blocker = "no_manufacturing_controller"
		logger.Log("WARN", "Bootstrap GATE needs the manufacturing coordinator (construction executor) but none wired", map[string]interface{}{
			"action":       "bootstrap_gate_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_manufacturing_controller",
		})
		return
	}

	if !obs.ManufacturingRunning {
		if cfg.DryRun {
			logger.Log("INFO", "Bootstrap DRY-RUN: WOULD ensure the manufacturing coordinator (construction executor) is running — a fresh start adopts the pipeline (took no action)", map[string]interface{}{
				"action":       "bootstrap_would_ensure_manufacturing",
				"container_id": cmd.ContainerID,
			})
			return
		}
		if err := h.manufacturing.EnsureRunning(ctx, cmd.PlayerID); err != nil {
			res.Blocker = "manufacturing_ensure_error"
			logger.Log("ERROR", fmt.Sprintf("Bootstrap failed to ensure the manufacturing coordinator running: %v", err), map[string]interface{}{
				"action":       "bootstrap_manufacturing_ensure_error",
				"container_id": cmd.ContainerID,
			})
			return
		}
		res.MfgEnsured = true
		logger.Log("INFO", "Bootstrap started the manufacturing coordinator (construction executor) — a fresh start ADOPTS the gate pipeline at startup (captain L57)", map[string]interface{}{
			"action":       "bootstrap_manufacturing_ensured",
			"container_id": cmd.ContainerID,
		})
		return
	}

	// Running but not adopted ⇒ the L57 bounce: restart so it re-scans and adopts the fresh pipeline.
	if cfg.DryRun {
		logger.Log("INFO", "Bootstrap DRY-RUN: WOULD bounce the manufacturing coordinator so it ADOPTS the freshly-created gate pipeline (captain L57) (took no action)", map[string]interface{}{
			"action":       "bootstrap_would_bounce_manufacturing",
			"container_id": cmd.ContainerID,
		})
		return
	}
	if err := h.manufacturing.BounceForAdoption(ctx, cmd.PlayerID); err != nil {
		res.Blocker = "manufacturing_bounce_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap failed to bounce the manufacturing coordinator for adoption: %v", err), map[string]interface{}{
			"action":       "bootstrap_manufacturing_bounce_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.MfgBounced = true
	logger.Log("INFO", "Bootstrap bounced the manufacturing coordinator so it ADOPTS the freshly-created gate pipeline (captain L57: a new pipeline is inert until the executor adopts it at startup)", map[string]interface{}{
		"action":       "bootstrap_manufacturing_bounced",
		"container_id": cmd.ContainerID,
	})
	twinreport.Report("executor-bounce", nil) // test-gated: no /v2 call for the twin to observe
}

// sizeGateWorkers executes the deterministic worker plan: buy the staged gate-delivery hull when the
// executor's worker pool falls short of the pipeline's shape (Option B — the whole gate-delivery fleet is
// bought from contract income; contract haulers are never repurposed). The buy is guarded, so a failure
// this tick simply retries next tick.
func (h *RunBootstrapCoordinatorHandler) sizeGateWorkers(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	plan := planGateWorkers(obs, cfg)
	res.DesiredWorkers = plan.DesiredWorkers

	// Staged buy: purchase the delta (at most one hull) only when the pool is short of the shape.
	if plan.Buy > 0 {
		h.maybeBuyGateWorker(ctx, cmd, cfg, obs, plan, res)
	}
}

// maybeBuyGateWorker evaluates and (unless dry-run) executes ONE staged gate-delivery-hauler buy behind
// the readiness and SOLVENCY gates, emitting the guardrail arithmetic (RULINGS #4, fail closed). The
// solvency gate is the SAME shared working-capital floor the material engine enforces, so fleet and
// materials can't starve each other. Gate workers reuse the light-hauler asset (hauler_ship_type). Caller
// has checked the plan calls for a buy (pool short of the pipeline's shape).
func (h *RunBootstrapCoordinatorHandler) maybeBuyGateWorker(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, plan gateWorkerPlan, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	// In-flight guard (st-drm.6): gate workers reuse the hauler asset; don't dispatch another buy while
	// one this coordinator already launched is still on its way (its hull not yet counted as a worker).
	if h.acquisitionInFlight(ctx, cmd, res, cfg.HaulerShipType, "bootstrap_gate_blocked") {
		return
	}

	// Readiness gate: an idle hull must exist to fly to the yard and execute the buy. No idle hull ⇒
	// BLOCKED (not failed) — a later tick with a free hull retries.
	if !obs.HasIdlePurchaser {
		res.Blocker = "no_purchaser"
		logger.Log("WARN", fmt.Sprintf("Bootstrap gate worker needed (%d have, %d desired) but BLOCKED: no idle hull to execute the purchase", obs.GateWorkers, plan.DesiredWorkers), map[string]interface{}{
			"action":       "bootstrap_gate_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_purchaser",
		})
		return
	}

	if h.gateAcquirer == nil {
		res.Blocker = "no_gate_acquirer"
		logger.Log("WARN", "Bootstrap gate worker needed but no gate-worker acquirer wired — cannot price-check or buy", map[string]interface{}{
			"action":       "bootstrap_gate_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_gate_acquirer",
		})
		return
	}

	// Price-check first (reuse shipyard list). Unreadable price ⇒ the capital gate fails CLOSED.
	price, yard, readable, err := h.gateAcquirer.PriceCheck(ctx, cmd.PlayerID, cfg.HaulerShipType)
	if err != nil || !readable {
		res.Blocker = "price_unreadable"
		logger.Log("WARN", fmt.Sprintf("Bootstrap gate worker price unreadable — failing closed (no buy): err=%v", err), map[string]interface{}{
			"action":       "bootstrap_gate_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "price_unreadable",
		})
		return
	}

	// Solvency gate (Option B): the gate-delivery fleet is bought from contract income, and a buy must
	// leave live treasury AT OR ABOVE the SAME working-capital floor the material engine enforces at a
	// factory input buy — common.EffectiveReserveFloor(reserve, reserve_pct, treasury) =
	// max(50k, min(reserve, reserve_pct%×treasury)) (the sp-yqx4 counter-cyclical primitive that
	// production_executor.go's spendFloorBreached uses). Sharing ONE floor is what keeps fleet and
	// materials from starving each other — neither spends into the other's reserve. The floor itself is
	// the safety buffer (no separate margin). A buy that would breach it is BLOCKED and retries as contract
	// income refills the treasury — the same staging that paces the DATA/INCOME buys.
	floor := common.EffectiveReserveFloor(cfg.Reserve, cfg.ReservePct, obs.Treasury)
	affordable := obs.Treasury-price >= floor
	logger.Log("INFO", fmt.Sprintf("Bootstrap gate worker buy decision: price=%d treasury=%d reserve_floor=max(50k, min(%d, %d%%×treasury))=%d affordable=(treasury−price≥floor)=%v desired=%d have=%d yard=%s — %s", price, obs.Treasury, cfg.Reserve, cfg.ReservePct, floor, affordable, plan.DesiredWorkers, obs.GateWorkers, yard, gateSolvencyNote(affordable)), map[string]interface{}{
		"action":        "bootstrap_gate_worker_buy_decision",
		"container_id":  cmd.ContainerID,
		"price":         price,
		"treasury":      obs.Treasury,
		"reserve_floor": floor,
		"reserve":       cfg.Reserve,
		"reserve_pct":   cfg.ReservePct,
		"affordable":    affordable,
		"desired":       plan.DesiredWorkers,
		"have":          obs.GateWorkers,
		"yard":          yard,
	})
	if !affordable {
		res.Blocker = "gate_worker_capital_gate"
		return
	}

	if cfg.DryRun {
		res.WouldBuy++
		logger.Log("INFO", fmt.Sprintf("Bootstrap DRY-RUN: WOULD buy 1 %s at %s for %d as a gate-construction worker (took no action)", cfg.HaulerShipType, yard, price), map[string]interface{}{
			"action":       "bootstrap_would_buy_gate_worker",
			"container_id": cmd.ContainerID,
		})
		return
	}

	bought, err := h.gateAcquirer.BuyForConstruction(ctx, cmd.PlayerID, cfg.HaulerShipType, yard)
	if err != nil {
		res.Blocker = "purchase_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap gate worker purchase failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_gate_worker_buy_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.GateWorkersBought++
	if h.metrics != nil {
		h.metrics.RecordHaulerPurchased() // gate workers are light haulers — reuse the hull counter
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap bought gate-construction worker %s at %s for %d, dedicated to construction (%d have→%d, %d desired)", bought.ShipSymbol, yard, bought.Price, obs.GateWorkers, obs.GateWorkers+1, plan.DesiredWorkers), map[string]interface{}{
		"action":       "bootstrap_bought_gate_worker",
		"container_id": cmd.ContainerID,
		"ship":         bought.ShipSymbol,
		"price":        bought.Price,
	})
}

// actComplete runs the terminal COMPLETE phase (Slice 3): the gate is built, so bootstrap hands the fleet
// off to the mature demand-driven economy and exits. The hand-off launches the fleet-autosizer (OFF the
// whole bootstrap run so the two never bid against one treasury) and the other standing coordinators,
// exactly ONCE — guarded on obs.AutosizerRunning, so a restart post-COMPLETE re-observes the autosizer
// running and skips straight to exit (terminal idempotency, spec §Architecture). The loop exits only once
// the hand-off is confirmed (autosizer running or launched this tick); a blocked hand-off holds and retries.
func (h *RunBootstrapCoordinatorHandler) actComplete(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if !obs.AutosizerRunning {
		h.launchHandoff(ctx, cmd, cfg, res)
	}

	// Terminal exit: only once the standing economy is confirmed live (autosizer already running, or the
	// hand-off launched successfully this tick). A blocked/failed hand-off leaves Done false so the tick
	// retries — bootstrap never exits having left the fleet un-handed-off.
	if obs.AutosizerRunning || res.HandoffLaunched {
		res.Done = true
		logger.Log("INFO", "Bootstrap COMPLETE — the jump gate is built and the standing economy is handed off (fleet-autosizer + coordinators live); the bootstrap coordinator is exiting (its job is done)", map[string]interface{}{
			"action":       "bootstrap_complete",
			"container_id": cmd.ContainerID,
		})
	}
}

// launchHandoff launches the standing coordinators — the fleet-autosizer plus the rest — turning fleet
// scaling over to demand. Both launches must succeed to record the hand-off; a failure sets a blocker and
// leaves it for next tick (idempotent at the adapter, guarded on obs.AutosizerRunning by the caller).
func (h *RunBootstrapCoordinatorHandler) launchHandoff(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", "Bootstrap DRY-RUN: WOULD launch the fleet-autosizer + standing coordinators as the COMPLETE hand-off (took no action, and holds rather than exiting)", map[string]interface{}{
			"action":       "bootstrap_would_handoff",
			"container_id": cmd.ContainerID,
		})
		return
	}
	if h.handoff == nil {
		res.Blocker = "no_handoff_launcher"
		logger.Log("WARN", "Bootstrap COMPLETE but no hand-off launcher wired — cannot launch the standing economy (holding, not exiting)", map[string]interface{}{
			"action":       "bootstrap_complete_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_handoff_launcher",
		})
		return
	}
	if err := h.handoff.LaunchAutosizer(ctx, cmd.PlayerID, cmd.AgentSymbol); err != nil {
		res.Blocker = "autosizer_launch_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap hand-off failed to launch the fleet-autosizer: %v", err), map[string]interface{}{
			"action":       "bootstrap_autosizer_launch_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	if err := h.handoff.LaunchStandingCoordinators(ctx, cmd.PlayerID, cmd.AgentSymbol); err != nil {
		res.Blocker = "standing_launch_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap hand-off launched the autosizer but failed to launch the standing coordinators: %v", err), map[string]interface{}{
			"action":       "bootstrap_standing_launch_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.HandoffLaunched = true
	logger.Log("INFO", "Bootstrap launched the fleet-autosizer + standing coordinators — the hand-off to the mature demand-driven economy (the autosizer now owns all fleet scaling)", map[string]interface{}{
		"action":       "bootstrap_handoff_launched",
		"container_id": cmd.ContainerID,
	})
	// test-gated: the hand-off launches three standing coordinators in one op, none with a /v2 call.
	twinreport.Report("launch-autosizer", nil)
	twinreport.Report("launch-siting", nil)
	twinreport.Report("launch-worker-rebalancer", nil)
}

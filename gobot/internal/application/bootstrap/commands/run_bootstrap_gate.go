package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// This file holds the GATE phase: the jump-gate construction drive and its
// deterministic worker sizing, plus the EXPANSION hand-off. It mirrors run_bootstrap_income.go's shape —
// independently-guarded, idempotent actions on the observed delta, each failing CLOSED — so a restart
// mid-GATE re-observes construction %, the executor's adoption, and the worker pool, and never
// double-starts, double-bounces, or double-buys.

// gateWorkerPlan is the deterministic worker-sizing decision for one GATE tick (autosizer stays OFF the
// whole bootstrap run). It is computed by planGateWorkers from the observation alone and then executed
// by actGate behind the readiness/capital gates.
type gateWorkerPlan struct {
	// ReleaseShips are contract haulers to release into the executor's worker pool this tick. It is ALWAYS
	// EMPTY now (sp-cdxy2): the contract fleet is EXCLUSIVE (sp-9le3x) and is NEVER repurposed to gate
	// construction, so GATE never re-tags a "contract" hull "manufacturing" (the re-tag that dropped the
	// scaler's ContractHullCount and drove the buy→repurpose→buy churn). The field + the repurposer seam
	// it feeds are retained (dormant) rather than ripped out; sizeGateWorkers simply never iterates it.
	ReleaseShips []string
	// SurplusToUndedicate are the gate's OWN surplus IDLE manufacturing hulls to un-dedicate this tick (sp-mxflh):
	// when the executor holds MORE gate workers than the workforce target, the idle overage is
	// released to the UNDEDICATED idle pool so the contract scaler's reclaim-before-buy tier adopts it into the
	// contract fleet BEFORE buying — the zero-buy re-balance. Distinct from ReleaseShips (the dormant
	// re-dedicate-TO-construction seam): this un-dedicates AWAY from construction. Empty unless over-provisioned;
	// never a hull mid-task, never a contract hauler.
	SurplusToUndedicate []string
	// Buy is the staged ramp step: gate-worker hulls to BUY this tick (0 or 1 — never a blind buy-all),
	// non-zero while the executor's existing workers fall short of the workforce target. With the contract
	// fleet exclusive, the BUY (plus any idle non-dedicated hull the executor claims on its own) is the SOLE
	// source of the gate workforce — it sizes it from scratch.
	Buy int
	// DesiredWorkers is the sizing target: gate_worker_target, live from GATE entry.
	DesiredWorkers int
	// KeptOnContract is how many haulers stay on contracts through GATE — the WHOLE contract delivery fleet
	// now (sp-cdxy2), never a floored subset — carried for the decision log.
	KeptOnContract int
}

// planGateWorkers sizes the gate-construction workforce deterministically from the observation. The
// contract fleet is EXCLUSIVE (sp-9le3x): GATE builds its OWN construction fleet and never competes with
// contracts for hulls, so the plan is two independent decisions:
//
//  1. KEEP THE WHOLE CONTRACT FLEET — every "contract"-dedicated delivery hauler stays EARNING on
//     contracts through GATE (ReleaseShips is always empty). Repurposing them was the sp-cdxy2 churn: a
//     contract→manufacturing re-tag dropped the scaler's ContractHullCount below its delivery target, so
//     the scaler re-bought to refill and GATE repurposed again. Contracts fund the gate build at full
//     scale (RULINGS #1); the depot warehouse/stocker hulls are separately tagged and likewise untouched.
//  2. RAMP TO THE WORKFORCE TARGET FROM GATE ENTRY — target gate_worker_target workers as soon as the
//     pipeline exists and BUY the staged delta (one hull per tick) while the executor's workers fall short.
//     A bought hull is dedicated to the manufacturing fleet and shows up as a GateWorker next tick, so the
//     deficit shrinks one per tick and the buy stops at the target — never an over-buy.
//
// The target deliberately does NOT wait for, or track, the pipeline's revealed chain count: waiting would
// stall the whole construction ramp behind revelation, and tracking would make the target non-monotone
// inside GATE — a chain count that dropped would turn the hulls just bought into surplus and release them,
// the buy/release oscillation the bang-bang around desired exists to prevent. gate_worker_target is the
// single operator-reachable size, and the working-capital floor (not the shape) is what bounds the spend.
//
// It is pure and idempotent: a restart mid-GATE re-derives the same plan from the re-observed pool, so no
// ramp hull is double-bought.
func planGateWorkers(obs Observation, cfg bootstrapRunConfig) gateWorkerPlan {
	// (1) The exclusive contract fleet is never repurposed — release nothing, keep the whole delivery fleet.
	kept := len(obs.Haulers)

	// (2) The workforce target, live from the moment GATE has a pipeline to work.
	desired := cfg.GateWorkerTarget

	// Buy the staged delta (at most one per tick) while the executor's already-claimed workers fall short.
	// GateWorkers is the whole pool: a bought hull becomes a GateWorker next tick, so the deficit shrinks
	// and the buy stops at the target.
	buy := 0
	if desired > obs.GateWorkers {
		buy = 1
	}

	return gateWorkerPlan{
		ReleaseShips:        nil,
		SurplusToUndedicate: selectGateSurplus(obs, desired),
		Buy:                 buy,
		DesiredWorkers:      desired,
		KeptOnContract:      kept,
	}
}

// selectGateSurplus picks the IDLE manufacturing hulls to un-dedicate this tick (sp-mxflh) — the
// (GateWorkers − desired) overage, drawn ONLY from idle hulls (never one mid-task) in deterministic
// symbol order so the release is stable and reaches a fixed point (a released hull becomes a contract
// hull next tick, shrinking the surplus). It returns nil — releasing nothing — unless the executor holds
// more workers than the workforce target, which happens only when the executor has claimed idle hulls of
// its own beyond what the ramp bought. When the surplus exceeds the idle count it releases only the idle
// ones (fail-safe: the rest re-balance a later tick as they go idle). The buy path (desired > GateWorkers)
// and this release path (GateWorkers > desired) are mutually exclusive, so the sizing is a clean
// bang-bang around desired — never buying and releasing in the same tick.
func selectGateSurplus(obs Observation, desired int) []string {
	if desired <= 0 || obs.GateWorkers <= desired {
		return nil
	}
	idle := make([]string, 0, len(obs.GateWorkerHulls))
	for _, worker := range obs.GateWorkerHulls {
		if worker.Idle {
			idle = append(idle, worker.Symbol)
		}
	}
	sort.Strings(idle) // deterministic (lowest-symbol first) so the release is stable — no thrash
	surplus := obs.GateWorkers - desired
	if surplus < len(idle) {
		idle = idle[:surplus]
	}
	if len(idle) == 0 {
		return nil
	}
	return idle
}

// gateSiteOrNone renders the gate site for the heartbeat, or "none" before it is discovered.
func gateSiteOrNone(site string) string {
	if site == "" {
		return "none"
	}
	return site
}

// actGate runs the GATE phase: drive the jump gate to construction. Its steps are ordered and
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
//  4. Size the gate workforce (planGateWorkers): BUY the staged ramp delta while the executor's workers
//     fall short of the workforce target. The exclusive contract fleet is never repurposed (sp-cdxy2).
//
// The monitor→EXPANSION transition is derivePhase's job (obs.ConstructionComplete), so GATE has no explicit
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

	// (4) Size the gate workforce: buy the staged top-up if the executor's workers fall short.
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
}

// sizeGateWorkers executes the deterministic worker plan: buy the staged ramp delta while the executor's
// workers fall short of the workforce target. The release loop is retained but INERT — planGateWorkers keeps
// the whole exclusive contract fleet on contracts (sp-cdxy2), so plan.ReleaseShips is always empty and no
// hauler is ever repurposed; the loop stays only so a regression that reintroduced a release would still
// route through the guarded, idempotent repurposer rather than a raw re-tag. Each step is independently
// guarded, so a partial failure this tick simply retries next tick.
func (h *RunBootstrapCoordinatorHandler) sizeGateWorkers(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	plan := planGateWorkers(obs, cfg)
	res.DesiredWorkers = plan.DesiredWorkers

	// (1) INERT (always empty, sp-cdxy2): the exclusive contract fleet is never repurposed. Retained as the
	// guarded seam so a reintroduced release could never bypass the idempotent repurposer.
	for _, ship := range plan.ReleaseShips {
		h.repurposeHauler(ctx, cmd, cfg, ship, res)
	}

	// (1b) sp-mxflh: release the gate's OWN surplus IDLE manufacturing hulls to the UNDEDICATED idle pool so the
	// contract scaler's reclaim-before-buy tier adopts them into the contract fleet — the zero-buy re-balance
	// (which is also how the scaler's over-buying stops). Non-empty ONLY when over-provisioned; FREE (no spend).
	if len(plan.SurplusToUndedicate) > 0 {
		h.releaseGateSurplus(ctx, cmd, cfg, plan, res)
	}

	// (2) Staged ramp: buy the delta (at most one hull) only while the executor's workers are short of the target.
	if plan.Buy > 0 {
		h.maybeBuyGateWorker(ctx, cmd, cfg, obs, plan, res)
	}
}

// releaseGateSurplus un-dedicates the gate's surplus IDLE manufacturing hulls (planGateWorkers selected them —
// GateWorkers over the workforce target) back to the UNDEDICATED idle pool via the single-writer
// AssignFleet (fleet→"", RULINGS #3), from where the contract scaler's IdleHullReclaimer adopts them into the
// contract fleet before it buys — the zero-buy re-balance (sp-mxflh). FREE (un-dedicate spends nothing) ⇒ never
// cushion-gated. Best-effort + fail-closed: a nil releaser or a partial release just re-balances fewer this
// tick (retried next); the releaser re-guards each hull's idle status so one that started a task since the
// observation is never yanked mid-task. Skipped under dry-run (observe-only, like the buy/repurpose paths).
func (h *RunBootstrapCoordinatorHandler) releaseGateSurplus(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, plan gateWorkerPlan, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", fmt.Sprintf("Bootstrap DRY-RUN: WOULD release %d surplus idle gate worker(s) to the undedicated pool for the contract scaler to adopt: %v (took no action)", len(plan.SurplusToUndedicate), plan.SurplusToUndedicate), map[string]interface{}{
			"action":       "bootstrap_would_release_gate_surplus",
			"container_id": cmd.ContainerID,
		})
		return
	}
	if h.gateReleaser == nil {
		res.Blocker = "no_gate_releaser"
		logger.Log("WARN", "Bootstrap GATE has surplus gate workers to release but no gate-surplus releaser wired — holding the surplus", map[string]interface{}{
			"action":       "bootstrap_gate_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_gate_releaser",
		})
		return
	}
	released, err := h.gateReleaser.ReleaseSurplusGateWorkers(ctx, cmd.PlayerID, plan.SurplusToUndedicate)
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("Bootstrap GATE: releasing %d surplus gate worker(s) partially failed (%d released): %v", len(plan.SurplusToUndedicate), released, err), map[string]interface{}{
			"action":       "bootstrap_gate_surplus_release_error",
			"container_id": cmd.ContainerID,
		})
	}
	res.WorkersReleased += released
	if released > 0 {
		logger.Log("INFO", fmt.Sprintf("Bootstrap GATE released %d surplus idle gate worker(s) to the undedicated pool — the contract scaler now adopts them into the contract fleet (zero buys)", released), map[string]interface{}{
			"action":       "bootstrap_gate_surplus_released",
			"container_id": cmd.ContainerID,
		})
	}
}

// repurposeHauler releases ONE contract hauler back to the idle pool (reuse fleet unassign) so the
// manufacturing coordinator claims it as a gate worker. INERT under sp-cdxy2 (the exclusive contract fleet is
// never repurposed, so planGateWorkers hands it no ships) — retained as the guarded, idempotent seam. Idempotent
// at the adapter (clearing an already-clear tag is a no-op), so a re-release across a laggy observation is harmless.
func (h *RunBootstrapCoordinatorHandler) repurposeHauler(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, ship string, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", fmt.Sprintf("Bootstrap DRY-RUN: WOULD repurpose contract hauler %s to gate construction (took no action)", ship), map[string]interface{}{
			"action":       "bootstrap_would_repurpose",
			"container_id": cmd.ContainerID,
			"ship":         ship,
		})
		return
	}
	if h.repurposer == nil {
		res.Blocker = "no_repurposer"
		logger.Log("WARN", "Bootstrap GATE needs to repurpose a hauler to construction but no repurposer wired", map[string]interface{}{
			"action":       "bootstrap_gate_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_repurposer",
		})
		return
	}
	if err := h.repurposer.RepurposeToConstruction(ctx, cmd.PlayerID, ship); err != nil {
		res.Blocker = "repurpose_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap repurpose of hauler %s to construction failed: %v", ship, err), map[string]interface{}{
			"action":       "bootstrap_repurpose_error",
			"container_id": cmd.ContainerID,
			"ship":         ship,
		})
		return
	}
	res.WorkersReleased++
	logger.Log("INFO", fmt.Sprintf("Bootstrap released contract hauler %s to the manufacturing coordinator as a gate-construction worker", ship), map[string]interface{}{
		"action":       "bootstrap_repurposed_hauler",
		"container_id": cmd.ContainerID,
		"ship":         ship,
	})
}

// maybeBuyGateWorker evaluates and (unless dry-run) executes ONE staged gate-worker buy behind the
// readiness and capital gates, emitting the same guardrail arithmetic as the probe/hauler buys (RULINGS
// #4, fail closed). Gate workers reuse the light-hauler asset (hauler_ship_type). Caller has checked the
// plan calls for a buy (pool short of the workforce target).
func (h *RunBootstrapCoordinatorHandler) maybeBuyGateWorker(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, plan gateWorkerPlan, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

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

	// Capital gate (sp-bpdf): the gate-worker buy is bootstrap's GATE-phase construction spend, so it now
	// reserves the SAME absolute contract working-capital floor as the hauler buy (sp-acv5) — affordable ⇔
	// cushion=(treasury−price) ≥ contract_working_capital_floor — NOT the old proportional reserve_margin×
	// treasury cap. Gate construction therefore can never drive the treasury below the working-capital line
	// the fleet autosizer also honors (common.ImmutableReserveFloor; the two-buyer safety, ktio-B). A worker
	// that fails the gate this tick simply waits and re-checks (the whole contract fleet keeps earning
	// through GATE to grow the treasury). RULINGS #4 fail-closed: an unreadable price already returned above, and a
	// cushion below the floor does NOT buy — so after a permitted buy treasury ≥ floor by construction.
	// (The separate DATA-phase reserve_margin knob this once contrasted against is gone too — sp-05glh
	// scrapped it with the 40% rule; the probe buy now gates on this same common.ImmutableReserveFloor
	// cushion check, see run_bootstrap_reconcile.go.)
	cushion := obs.Treasury - price
	affordable := cushion >= cfg.ContractWorkingCapitalFloor
	floorNote := "clears the working-capital floor"
	if !affordable {
		floorNote = "BLOCKED by the working-capital floor (treasury−price below the contract working-capital floor)"
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap gate worker buy decision: price=%d treasury=%d floor=%d cushion=(treasury−price)=%d affordable=(cushion≥floor)=%v desired=%d have=%d yard=%s — %s", price, obs.Treasury, cfg.ContractWorkingCapitalFloor, cushion, affordable, plan.DesiredWorkers, obs.GateWorkers, yard, floorNote), map[string]interface{}{
		"action":       "bootstrap_gate_worker_buy_decision",
		"container_id": cmd.ContainerID,
		"price":        price,
		"treasury":     obs.Treasury,
		"floor":        cfg.ContractWorkingCapitalFloor,
		"cushion":      cushion,
		"affordable":   affordable,
		"desired":      plan.DesiredWorkers,
		"have":         obs.GateWorkers,
		"yard":         yard,
	})
	if !affordable {
		res.Blocker = "capital_gate"
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

// actExpansion runs the terminal EXPANSION phase (sp-feiy7 — formerly COMPLETE): the gate is built and
// steady-state growth begins, so bootstrap hands the fleet off to the mature demand-driven economy and
// exits (the standing coordinators — including the probe-buyer fleet, the Admiral's EXPANSION spender —
// own all growth from here). The hand-off launches the fleet-autosizer (OFF the whole bootstrap run so
// the two never bid against one treasury) and the other standing coordinators, exactly ONCE — guarded on
// obs.AutosizerRunning, so a restart post-gate re-observes the autosizer running and skips straight to
// exit (terminal idempotency, spec §Architecture).
//
// TERMINATION IS DRIVEN BY THE WORLD, NOT BY THE HAND-OFF'S OUTCOME. Reaching this phase already means the
// home jump gate reads BUILT — bootstrap's own terminal goal — so there is no cold-start work left whatever
// the launcher does. A confirmed hand-off exits immediately; an unconfirmed one is retried for
// expansionHandoffRetryTicks consecutive ticks and then exits anyway, because bootstrap is boot-standing
// and every launch is idempotent — the retry continues at the next daemon boot rather than every tick
// forever. That bound is load-bearing on a MATURE fleet: this coordinator's whole tick-cadence budget
// assumes it exits once the gate is built, and each tick it does not costs a fully-paginated fleet re-read
// against an account-wide request limit that fleet growth cannot raise.
//
// The distinction that keeps this safe is in the SIGNAL, not here: obs.ConstructionComplete is a positive
// live-API assertion, and every read miss on the gate-snapshot path leaves it false, so an unreadable or
// undiscovered world holds the arc in a cold-start phase and can never reach this function.
func (h *RunBootstrapCoordinatorHandler) actExpansion(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	// Cold-start scaling launches the fleet autosizer EARLY, so when it is already running
	// launchHandoff's autosizer-gated path (which ALSO launches the standing coordinators) is skipped and
	// the standing half still has to be ensured here. An autosizer-running-but-standing-absent state can
	// only arise under that early launch — the normal hand-off launches both in one call.
	handedOff := false
	if !obs.AutosizerRunning {
		h.launchHandoff(ctx, cmd, cfg, res)
		handedOff = res.HandoffLaunched
	} else {
		handedOff = h.ensureStandingHandoff(ctx, cmd, cfg, res)
	}

	if !handedOff {
		// Hold and retry while the fault could still be transient, so a fleet that has only just finished
		// its gate exits with the standing economy confirmed live rather than on the bounded path.
		if held := h.bumpExpansionHoldStreak(cmd.ContainerID); held < expansionHandoffRetryTicks {
			return
		}
		h.resetExpansionHoldStreak(cmd.ContainerID)
		res.Done = true
		logger.Log("WARN", fmt.Sprintf("Bootstrap EXPANSION — the jump gate is built, so bootstrap's own work is finished, but the hand-off could not be confirmed in %d consecutive ticks (blocker=%s); exiting anyway rather than holding a mature fleet in a per-tick full-fleet re-read. Bootstrap is boot-standing and the hand-off launches are idempotent, so the next daemon boot retries it", expansionHandoffRetryTicks, blockerOrNone(res.Blocker)), map[string]interface{}{
			"action":       "bootstrap_complete_unconfirmed_handoff",
			"container_id": cmd.ContainerID,
			"blocker":      res.Blocker,
		})
		return
	}

	h.resetExpansionHoldStreak(cmd.ContainerID)
	res.Done = true
	logger.Log("INFO", "Bootstrap EXPANSION — the jump gate is built and the standing economy is handed off (fleet-autosizer + coordinators live); steady-state growth (probe-buying era) begins and the bootstrap coordinator is exiting (its job is done)", map[string]interface{}{
		"action":       "bootstrap_complete",
		"container_id": cmd.ContainerID,
	})
}

// bumpExpansionHoldStreak increments and returns the per-container count of consecutive EXPANSION ticks
// whose hand-off could not be confirmed. Keyed by ContainerID like the other per-container state because
// this handler is a REGISTERED SINGLETON; the mutex guards the map, and one container's ticks are
// sequential so the count is only ever advanced by a single goroutine.
func (h *RunBootstrapCoordinatorHandler) bumpExpansionHoldStreak(containerID string) int {
	h.expansionHoldStreakMu.Lock()
	defer h.expansionHoldStreakMu.Unlock()
	if h.expansionHoldStreaks == nil {
		h.expansionHoldStreaks = map[string]int{}
	}
	h.expansionHoldStreaks[containerID]++
	return h.expansionHoldStreaks[containerID]
}

// resetExpansionHoldStreak clears the streak once the terminal exit is taken, so a container relaunched
// under the same ID starts its retry window fresh.
func (h *RunBootstrapCoordinatorHandler) resetExpansionHoldStreak(containerID string) {
	h.expansionHoldStreakMu.Lock()
	defer h.expansionHoldStreakMu.Unlock()
	if h.expansionHoldStreaks != nil {
		delete(h.expansionHoldStreaks, containerID)
	}
}

// blockerOrNone renders an empty blocker as "none" for the log line.
func blockerOrNone(blocker string) string {
	if blocker == "" {
		return "none"
	}
	return blocker
}

// ensureStandingHandoff finishes the EXPANSION hand-off for the sp-sjvv case where the fleet autosizer was
// launched EARLY (armed cold-start scaling) and is therefore already running — so launchHandoff's
// autosizer-gated path is skipped, but its SECOND half (the standing coordinators: siting +
// worker-rebalancer) still has to run. It reports whether the standing coordinators are confirmed up
// (launched this tick or already running). Idempotent at the adapter (each launch skips when the
// coordinator is already RUNNING/PENDING), dry-run-safe, and nil-safe. On success it sets
// res.HandoffLaunched so the caller's terminal-exit check passes and the EXPANSION line fires; on a
// blocked/failed launch it sets a blocker and returns false so the caller HOLDS (never exits
// half-handed-off). Mirrors launchHandoff's standing-coordinator portion.
func (h *RunBootstrapCoordinatorHandler) ensureStandingHandoff(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, res *reconcileResult) bool {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", "Bootstrap DRY-RUN: the autosizer was launched early — WOULD launch the standing coordinators (siting + worker-rebalancer) to finish the hand-off (took no action)", map[string]interface{}{
			"action":       "bootstrap_would_finish_handoff",
			"container_id": cmd.ContainerID,
		})
		return false
	}
	if h.handoff == nil {
		res.Blocker = "no_handoff_launcher"
		logger.Log("WARN", "Bootstrap EXPANSION (autosizer launched early) but no hand-off launcher wired — cannot launch the standing coordinators (holding, not exiting)", map[string]interface{}{
			"action":       "bootstrap_complete_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_handoff_launcher",
		})
		return false
	}
	if err := h.handoff.LaunchStandingCoordinators(ctx, cmd.PlayerID, cmd.AgentSymbol); err != nil {
		res.Blocker = "standing_launch_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap hand-off (autosizer already launched early) failed to launch the standing coordinators: %v", err), map[string]interface{}{
			"action":       "bootstrap_standing_launch_error",
			"container_id": cmd.ContainerID,
		})
		return false
	}
	res.HandoffLaunched = true
	logger.Log("INFO", "Bootstrap finished the hand-off — the fleet autosizer was launched early (cold-start scaling, sp-sjvv), and now the standing coordinators (siting + worker-rebalancer) are launched too; the mature demand-driven economy is fully live", map[string]interface{}{
		"action":       "bootstrap_handoff_launched",
		"container_id": cmd.ContainerID,
	})
	return true
}

// launchHandoff launches the standing coordinators — the fleet-autosizer plus the rest — turning fleet
// scaling over to demand. Both launches must succeed to record the hand-off; a failure sets a blocker and
// leaves it for next tick (idempotent at the adapter, guarded on obs.AutosizerRunning by the caller).
func (h *RunBootstrapCoordinatorHandler) launchHandoff(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", "Bootstrap DRY-RUN: WOULD launch the fleet-autosizer + standing coordinators as the EXPANSION hand-off (took no action)", map[string]interface{}{
			"action":       "bootstrap_would_handoff",
			"container_id": cmd.ContainerID,
		})
		return
	}
	if h.handoff == nil {
		res.Blocker = "no_handoff_launcher"
		logger.Log("WARN", "Bootstrap EXPANSION but no hand-off launcher wired — cannot launch the standing economy (holding, not exiting)", map[string]interface{}{
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
}

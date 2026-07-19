package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// This file holds the GATE phase: the jump-gate construction drive and its
// deterministic worker sizing, plus the COMPLETE hand-off. It mirrors run_bootstrap_income.go's shape —
// independently-guarded, idempotent actions on the observed delta, each failing CLOSED — so a restart
// mid-GATE re-observes construction %, the executor's adoption, and the worker pool, and never
// double-starts, double-bounces, or double-buys.

// gateWorkerPlan is the deterministic worker-sizing decision for one GATE tick (autosizer stays OFF the
// whole bootstrap run). It is computed by planGateWorkers from the observation alone and then executed
// by actGate behind the readiness/capital gates.
type gateWorkerPlan struct {
	// ReleaseShips are the contract haulers to release back to the idle pool this tick so the
	// manufacturing coordinator claims them as gate-construction workers — the "repurpose idle INCOME
	// haulers FIRST" seed. Everything beyond min_contract_earners, so the income fleet becomes the seed
	// workforce while a cash earner stays on contracts.
	ReleaseShips []string
	// Buy is the staged top-up: gate-worker hulls to BUY this tick (0 or 1 — never a blind buy-all),
	// non-zero only once the pipeline reveals its chains AND the repurposed pool + existing workers fall
	// short of the pipeline's shape.
	Buy int
	// DesiredWorkers is the sizing target (~one per active gate-material chain + a delivery hauler, capped
	// at gate_worker_target). 0 until the pipeline reveals its chains — the seed release doesn't wait on it.
	DesiredWorkers int
	// KeptOnContract is how many haulers are deliberately kept on contracts (min_contract_earners) to keep
	// funding material acquisition through GATE — carried for the decision log.
	KeptOnContract int
}

// planGateWorkers sizes the gate-construction workforce deterministically from the observation (spec
// §Fleet scaling & hand-off), in the spec's priority order:
//
//  1. REPURPOSE FIRST — release every contract hauler beyond min_contract_earners to the idle pool so
//     the manufacturing coordinator claims it. This is the seed workforce and it does NOT wait on the
//     pipeline's shape (contracts wind down as GATE begins). The count guard is len(Haulers) > keep, so
//     once the surplus is released a later tick re-observes fewer contract haulers and releases nothing.
//  2. TOP-UP TO THE PIPELINE'S SHAPE — once construction reveals its producing chains, target ~one
//     worker per active chain + a delivery hauler, capped at gate_worker_target, and BUY the staged
//     delta (one hull per tick) only if the repurposed pool + existing gate workers fall short.
//  3. KEEP A CASH EARNER — min_contract_earners haulers stay on contracts through GATE (never released).
//
// It is pure and idempotent: a restart mid-GATE re-derives the same plan from the re-observed pool, so
// no hauler is double-released and no top-up hull is double-bought.
func planGateWorkers(obs Observation, cfg bootstrapRunConfig) gateWorkerPlan {
	keep := cfg.MinContractEarners
	if keep < 0 {
		keep = 0
	}
	onContract := len(obs.Haulers)
	kept := keep
	if kept > onContract {
		kept = onContract
	}

	// (1) + (3): release the surplus beyond the kept earners; keep the first `keep` on contracts.
	var release []string
	if onContract > keep {
		for _, h := range obs.Haulers[keep:] {
			if h.Symbol != "" {
				release = append(release, h.Symbol)
			}
		}
	}

	// (2): the top-up target, revealed only once the pipeline exposes its producing chains.
	desired := 0
	if obs.GateMaterialChains > 0 {
		desired = obs.GateMaterialChains + gateDeliveryHaulers
		if desired > cfg.GateWorkerTarget {
			desired = cfg.GateWorkerTarget
		}
	}

	// Buy the staged delta (at most one per tick) only when the pool AFTER this tick's release still
	// falls short. The executor's already-claimed workers (GateWorkers) plus the surplus we hand it this
	// tick is the pool; a bought hull becomes a GateWorker next tick, so the deficit shrinks and the buy
	// stops — never an over-buy.
	buy := 0
	if desired > 0 {
		poolAfterRelease := obs.GateWorkers + len(release)
		if desired > poolAfterRelease {
			buy = 1
		}
	}

	return gateWorkerPlan{
		ReleaseShips:   release,
		Buy:            buy,
		DesiredWorkers: desired,
		KeptOnContract: kept,
	}
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
//  4. Size the gate workforce (planGateWorkers): repurpose surplus contract haulers to construction FIRST,
//     then buy the staged top-up delta only if the pool falls short of the pipeline's shape.
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

	// (4) Size the gate workforce: repurpose surplus haulers first, buy the staged top-up if short.
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

// sizeGateWorkers executes the deterministic worker plan: release surplus contract haulers to the
// executor (repurpose-first), then buy the staged top-up delta if the pool falls short. Each release and
// the buy is independently guarded, so a partial failure this tick simply retries next tick.
func (h *RunBootstrapCoordinatorHandler) sizeGateWorkers(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	plan := planGateWorkers(obs, cfg)
	res.DesiredWorkers = plan.DesiredWorkers

	// (1) Repurpose-first: release every surplus contract hauler to the executor's worker pool.
	for _, ship := range plan.ReleaseShips {
		h.repurposeHauler(ctx, cmd, cfg, ship, res)
	}

	// (2) Staged top-up: buy the delta (at most one hull) only when the pool is short of the shape.
	if plan.Buy > 0 {
		h.maybeBuyGateWorker(ctx, cmd, cfg, obs, plan, res)
	}
}

// repurposeHauler releases ONE contract hauler back to the idle pool (reuse fleet unassign) so the
// manufacturing coordinator claims it as a gate worker. Idempotent at the adapter (clearing an
// already-clear tag is a no-op), so a re-release across a laggy observation is harmless.
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
	logger.Log("INFO", fmt.Sprintf("Bootstrap released contract hauler %s to the manufacturing coordinator as a gate-construction worker (repurpose-first, keeping %d earner(s) on contracts)", ship, cfg.MinContractEarners), map[string]interface{}{
		"action":       "bootstrap_repurposed_hauler",
		"container_id": cmd.ContainerID,
		"ship":         ship,
	})
}

// maybeBuyGateWorker evaluates and (unless dry-run) executes ONE staged gate-worker buy behind the
// readiness and capital gates, emitting the same guardrail arithmetic as the probe/hauler buys (RULINGS
// #4, fail closed). Gate workers reuse the light-hauler asset (hauler_ship_type). Caller has checked the
// plan calls for a buy (pool short of the pipeline's shape).
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
	// that fails the gate this tick simply waits and re-checks (min_contract_earners keeps earning through
	// GATE to grow the treasury). RULINGS #4 fail-closed: an unreadable price already returned above, and a
	// cushion below the floor does NOT buy — so after a permitted buy treasury ≥ floor by construction.
	// reserve_margin is deliberately untouched (it still paces the DATA probe buy).
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

// actComplete runs the terminal COMPLETE phase: the gate is built, so bootstrap hands the fleet
// off to the mature demand-driven economy and exits. The hand-off launches the fleet-autosizer (OFF the
// whole bootstrap run so the two never bid against one treasury) and the other standing coordinators,
// exactly ONCE — guarded on obs.AutosizerRunning, so a restart post-COMPLETE re-observes the autosizer
// running and skips straight to exit (terminal idempotency, spec §Architecture). The loop exits only once
// the hand-off is confirmed (autosizer running or launched this tick); a blocked hand-off holds and retries.
func (h *RunBootstrapCoordinatorHandler) actComplete(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if !obs.AutosizerRunning {
		h.launchHandoff(ctx, cmd, cfg, res)
	} else if cfg.AutosizerEarlyScaling && !h.ensureStandingHandoff(ctx, cmd, cfg, res) {
		// sp-sjvv: cold-start scaling launched the fleet autosizer EARLY, so it is already running here
		// and launchHandoff's autosizer-gated path (which ALSO launches the standing coordinators) is
		// skipped — but siting + worker-rebalancer still have to be started (the early launch only
		// started the autosizer, and siting has no other launch path). Ensure them now; if they cannot
		// be confirmed this tick, HOLD (return without setting Done) and retry next tick, so bootstrap
		// never exits with the mature economy half-handed-off. This branch is UNREACHABLE when the flag
		// is off (byte-identical): with the feature disarmed the autosizer only ever runs by way of the
		// normal hand-off, which launches the standing coordinators in the same call — so an
		// autosizer-running-but-standing-coordinators-absent state can only arise under the early launch.
		return
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

// ensureStandingHandoff finishes the COMPLETE hand-off for the sp-sjvv case where the fleet autosizer was
// launched EARLY (armed cold-start scaling) and is therefore already running — so launchHandoff's
// autosizer-gated path is skipped, but its SECOND half (the standing coordinators: siting +
// worker-rebalancer) still has to run. It reports whether the standing coordinators are confirmed up
// (launched this tick or already running). Idempotent at the adapter (each launch skips when the
// coordinator is already RUNNING/PENDING), dry-run-safe, and nil-safe. On success it sets
// res.HandoffLaunched so the caller's terminal-exit check passes and the COMPLETE line fires; on a
// blocked/failed launch it sets a blocker and returns false so the caller HOLDS (never exits
// half-handed-off). Mirrors launchHandoff's standing-coordinator portion.
func (h *RunBootstrapCoordinatorHandler) ensureStandingHandoff(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, res *reconcileResult) bool {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", "Bootstrap DRY-RUN: the autosizer was launched early — WOULD launch the standing coordinators (siting + worker-rebalancer) to finish the hand-off (took no action, and holds rather than exiting)", map[string]interface{}{
			"action":       "bootstrap_would_finish_handoff",
			"container_id": cmd.ContainerID,
		})
		return false
	}
	if h.handoff == nil {
		res.Blocker = "no_handoff_launcher"
		logger.Log("WARN", "Bootstrap COMPLETE (autosizer launched early) but no hand-off launcher wired — cannot launch the standing coordinators (holding, not exiting)", map[string]interface{}{
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
}

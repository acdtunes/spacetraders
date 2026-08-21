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

// gateWorkerPlan is the deterministic worker-sizing decision for one GATE tick (fleet growth stays OFF the
// whole bootstrap run). It is computed by planGateWorkers from the observation alone and then executed
// by actGate behind the readiness/capital gates.
type gateWorkerPlan struct {
	// ReleaseShips are contract haulers to release into the executor's worker pool this tick. It is ALWAYS
	// EMPTY now: the contract fleet is EXCLUSIVE and is NEVER repurposed to gate
	// construction, so GATE never re-tags a "contract" hull "manufacturing" (the re-tag that dropped the
	// scaler's ContractHullCount and drove the buy→repurpose→buy churn). The field + the repurposer seam
	// it feeds are retained (dormant) rather than ripped out; sizeGateWorkers simply never iterates it.
	ReleaseShips []string
	// SurplusToUndedicate are the gate's OWN surplus IDLE manufacturing hulls to un-dedicate this tick:
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
	// DesiredWorkers is the sizing target: gateWorkerTarget, live from GATE entry.
	DesiredWorkers int
	// KeptOnContract is how many haulers stay on contracts through GATE — the WHOLE contract delivery fleet
	// now, never a floored subset — carried for the decision log.
	KeptOnContract int
}

// planGateWorkers sizes the gate-construction workforce deterministically from the observation. The
// contract fleet is EXCLUSIVE: GATE builds its OWN construction fleet and never competes with
// contracts for hulls, so the plan is two independent decisions:
//
//  1. KEEP THE WHOLE CONTRACT FLEET — every "contract"-dedicated hauler stays EARNING through GATE
//     (ReleaseShips is always empty). Repurposing them churns: a contract→manufacturing re-tag drops
//     the scaler's count below its target, the scaler re-buys, and GATE repurposes again. Contracts
//     fund the gate build at full scale (RULINGS #1).
//  2. RAMP TO THE WORKFORCE TARGET FROM GATE ENTRY — target gateWorkerTarget workers as soon as the
//     pipeline exists and BUY the staged delta (one hull per tick) while the executor's workers fall short.
//     A bought hull is dedicated to the manufacturing fleet and shows up as a GateWorker next tick, so the
//     deficit shrinks one per tick and the buy stops at the target — never an over-buy.
//
// The target deliberately does NOT track the pipeline's revealed chain count: that would make it
// non-monotone inside GATE, so a dropped chain count would turn hulls just bought into surplus and
// release them. gateWorkerTarget is the single operator-reachable size, and the working-capital floor
// — not the pipeline's shape — bounds the spend.
//
// It is pure and idempotent: a restart mid-GATE re-derives the same plan from the re-observed pool, so no
// ramp hull is double-bought.
func planGateWorkers(obs Observation) gateWorkerPlan {
	// (1) The exclusive contract fleet is never repurposed — release nothing, keep the whole delivery fleet.
	kept := len(obs.Haulers)

	// (2) The workforce target, applied from the moment GATE has a pipeline to work.
	desired := gateWorkerTarget

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

// selectGateSurplus picks the IDLE manufacturing hulls to un-dedicate this tick — the
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
//     fall short of the workforce target. The exclusive contract fleet is never repurposed.
//
// The monitor→EXPANSION transition is derivePhase's job (obs.ConstructionComplete), so GATE has no explicit
// "is it done?" branch — it just reconciles the construction drive each tick until the phase flips.
func (h *RunBootstrapCoordinatorHandler) actGate(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
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
		h.startConstruction(ctx, cmd, obs, res)
		return
	}

	// (3) Ensure the executor is running AND has adopted the pipeline (the L57 adoption bounce).
	h.ensureExecutorAdopted(ctx, cmd, obs, res)

	// (4) Size the gate workforce: buy the staged top-up if the executor's workers fall short.
	h.sizeGateWorkers(ctx, cmd, obs, res)
}

// startConstruction drives `construction start <site>` once (idempotent at the adapter — it resumes an
// existing pipeline). Caller has checked obs.ConstructionStarted is false.
func (h *RunBootstrapCoordinatorHandler) startConstruction(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

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
func (h *RunBootstrapCoordinatorHandler) ensureExecutorAdopted(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
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
// the whole exclusive contract fleet on contracts, so plan.ReleaseShips is always empty and no
// hauler is ever repurposed; the loop stays only so a regression that reintroduced a release would still
// route through the guarded, idempotent repurposer rather than a raw re-tag. Each step is independently
// guarded, so a partial failure this tick simply retries next tick.
func (h *RunBootstrapCoordinatorHandler) sizeGateWorkers(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	plan := planGateWorkers(obs)
	res.DesiredWorkers = plan.DesiredWorkers

	// (1) INERT (always empty): the exclusive contract fleet is never repurposed. Retained as the
	// guarded seam so a reintroduced release could never bypass the idempotent repurposer.
	for _, ship := range plan.ReleaseShips {
		h.repurposeHauler(ctx, cmd, ship, res)
	}

	// (1b) Release the gate's OWN surplus IDLE manufacturing hulls to the UNDEDICATED idle pool so the
	// contract scaler's reclaim-before-buy tier adopts them into the contract fleet — the zero-buy re-balance
	// (which is also how the scaler's over-buying stops). Non-empty ONLY when over-provisioned; FREE (no spend).
	if len(plan.SurplusToUndedicate) > 0 {
		h.releaseGateSurplus(ctx, cmd, plan, res)
	}

	// (2) Staged ramp: buy the delta (at most one hull) only while the executor's workers are short of the target.
	if plan.Buy > 0 {
		h.maybeBuyGateWorker(ctx, cmd, obs, plan, res)
	}
}

// releaseGateSurplus un-dedicates the gate's surplus IDLE manufacturing hulls (planGateWorkers selected them —
// GateWorkers over the workforce target) back to the UNDEDICATED idle pool via the single-writer
// AssignFleet (fleet→"", RULINGS #3), from where the contract scaler's IdleHullReclaimer adopts them into the
// contract fleet before it buys — the zero-buy re-balance. FREE (un-dedicate spends nothing) ⇒ never
// cushion-gated. Best-effort + fail-closed: a nil releaser or a partial release just re-balances fewer this
// tick (retried next); the releaser re-guards each hull's idle status so one that started a task since the
// observation is never yanked mid-task.
func (h *RunBootstrapCoordinatorHandler) releaseGateSurplus(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, plan gateWorkerPlan, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

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
// manufacturing coordinator claims it as a gate worker. INERT (the exclusive contract fleet is
// never repurposed, so planGateWorkers hands it no ships) — retained as the guarded, idempotent seam. Idempotent
// at the adapter (clearing an already-clear tag is a no-op), so a re-release across a laggy observation is harmless.
func (h *RunBootstrapCoordinatorHandler) repurposeHauler(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, ship string, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

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
// #4, fail closed). Gate workers reuse the light-hauler asset (haulerShipType). Caller has checked the
// plan calls for a buy (pool short of the workforce target).
func (h *RunBootstrapCoordinatorHandler) maybeBuyGateWorker(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, plan gateWorkerPlan, res *reconcileResult) {
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

	// Unreadable price ⇒ fail CLOSED (no spend) and SEND a hull: a yard prices only while one stands at it, and nothing else in GATE ever warms it. Named nobody — any idle hull buys here — plus a LEND, because GATE is reached on a matured fleet where every haul-capable hull is tagged and the undedicated-hull search has nobody left to pick.
	price, yard, readable, err := h.gateAcquirer.PriceCheck(ctx, cmd.PlayerID, haulerShipType)
	if err != nil || !readable {
		h.awaitReadablePrice(ctx, cmd, obs, res, "", obs.BorrowableHull, fmt.Sprintf("gate worker (%d/%d)", obs.GateWorkers, plan.DesiredWorkers), err)
		return
	}

	// Capital gate: the gate-worker buy is bootstrap's GATE-phase construction spend, so it
	// reserves the SAME absolute contract working-capital floor as the hauler buy — affordable ⇔
	// cushion=(treasury−price) ≥ the contract working-capital floor. Gate construction therefore can never
	// drive the treasury below the working-capital line
	// the fleet-growth coordinator also honors (common.ImmutableReserveFloor; the two-buyer safety). A worker
	// that fails the gate this tick simply waits and re-checks (the whole contract fleet keeps earning
	// through GATE to grow the treasury). RULINGS #4 fail-closed: an unreadable price already returned above, and a
	// cushion below the floor does NOT buy — so after a permitted buy treasury ≥ floor by construction. The probe
	// buy gates on the same shape against common.ImmutableReserveFloor (see run_bootstrap_reconcile.go).
	cushion := obs.Treasury - price
	affordable := cushion >= contractWorkingCapitalFloor
	floorNote := "clears the working-capital floor"
	if !affordable {
		floorNote = "BLOCKED by the working-capital floor (treasury−price below the contract working-capital floor)"
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap gate worker buy decision: price=%d treasury=%d floor=%d cushion=(treasury−price)=%d affordable=(cushion≥floor)=%v desired=%d have=%d yard=%s — %s", price, obs.Treasury, contractWorkingCapitalFloor, cushion, affordable, plan.DesiredWorkers, obs.GateWorkers, yard, floorNote), map[string]interface{}{
		"action":       "bootstrap_gate_worker_buy_decision",
		"container_id": cmd.ContainerID,
		"price":        price,
		"treasury":     obs.Treasury,
		"floor":        contractWorkingCapitalFloor,
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

	bought, err := h.gateAcquirer.BuyForConstruction(ctx, cmd.PlayerID, haulerShipType, yard)
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

// actExpansion runs the terminal EXPANSION phase (formerly COMPLETE): the gate is built and
// steady-state growth begins, so bootstrap hands the fleet off to the mature demand-driven economy and
// exits (the standing coordinators — including the fleet-growth coordinator, the EXPANSION spender that
// owns every heavy buy — owns all growth from here). The hand-off launches it exactly ONCE — guarded on
// obs.GrowthRunning, so a restart post-gate re-observes growth running and skips straight to exit
// (terminal idempotency, spec §Architecture).
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

	// ONE launch, ONE latch. Growth running IS the hand-off having happened — it is the only thing the
	// hand-off starts — so a restart post-gate re-observes it and exits without re-launching. A latch
	// naming anything the launch does not start needs a second repair path for the gap.
	handedOff := obs.GrowthRunning
	if !handedOff {
		h.launchHandoff(ctx, cmd, cfg, res)
		handedOff = res.HandoffLaunched
	}

	// The hand-off REDIRECTS the gate's construction hulls to trade, on the world signal and for the
	// reason: the gate reads BUILT, so its workers have no work left. Placed here with the home release
	// so both fire on every EXPANSION tick, confirmed hand-off or bounded-WARN alike. The destination is
	// ENSURED FIRST, so the hulls are handed into a fleet somebody is actually working.
	h.ensureTradeFleetCoordinator(ctx, cmd, cfg)
	h.redirectConstructionHullsToTrade(ctx, cmd, cfg, obs, res)

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
	logger.Log("INFO", "Bootstrap EXPANSION — the jump gate is built and the standing economy is handed off (fleet-growth coordinator live); steady-state growth (probe-buying era) begins and the bootstrap coordinator is exiting (its job is done)", map[string]interface{}{
		"action":       "bootstrap_complete",
		"container_id": cmd.ContainerID,
	})
}

// selectConstructionHullsForTrade picks the gate-construction hulls to hand to the trade fleet:
// every manufacturing-dedicated hull the observation reports as genuinely FREE — idle and not in transit
// (GateWorkerSnapshot.Idle is exactly that conjunction) — in deterministic symbol order so the hand-over
// is stable and logs read the same across ticks.
//
// A BUSY hull is deliberately left construction-dedicated: it is mid-delivery, and a hull yanked into
// trade with an unfinished construction leg is the strand this selection exists to prevent. It is not
// written off, either — the selection is re-derived from the fresh observation on every EXPANSION tick,
// so a hull that lands and goes idle is picked up by a later tick or by the next daemon boot (bootstrap
// is boot-standing and EXPANSION is monotone, so it re-observes and re-runs).
//
// Returning nil — nothing free to redirect — is the ordinary case on an already-redirected fleet, and
// the caller turns it into zero port calls.
func selectConstructionHullsForTrade(obs Observation) []string {
	free := make([]string, 0, len(obs.GateWorkerHulls))
	for _, worker := range obs.GateWorkerHulls {
		if worker.Idle {
			free = append(free, worker.Symbol)
		}
	}
	if len(free) == 0 {
		return nil
	}
	sort.Strings(free) // deterministic (lowest-symbol first), mirroring selectGateSurplus
	return free
}

// ensureTradeFleetCoordinator makes the standing trade-fleet coordinator RUNNING at EXPANSION, so the
// hulls the redirect hands over are actually worked instead of pinned to an unmanaged fleet.
//
// Nothing else guarantees this at EXPANSION. LaunchStandingCoordinators starts fleet growth and
// nothing else; the trade-fleet coordinator is not boot-standing; and the only other caller is the
// INCOME-phase trade-seed, which a MATURE fleet restarting into a built world never reaches — it
// derives EXPANSION on its first tick.
//
// It runs UNCONDITIONALLY, not only when there are hulls to redirect: on a fleet an earlier run
// already re-tagged there is nothing to hand over, and that restart is precisely the one that must
// confirm somebody is working them. Safe every tick because the launcher is idempotent.
//
// Best-effort, like everything else on this path: it never claims res.Blocker and never affects Done. A
// failed launch must NOT suppress the redirect either — a trade-tagged hull waiting for a coordinator is
// strictly better than a construction-tagged hull that idles forever — so the caller runs the redirect
// regardless and this reports on its own line.
func (h *RunBootstrapCoordinatorHandler) ensureTradeFleetCoordinator(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig) {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", "Bootstrap DRY-RUN: WOULD ensure the standing trade-fleet coordinator so the redirected gate hulls are worked (took no action)", map[string]interface{}{
			"action":       "bootstrap_would_ensure_trade_coordinator",
			"container_id": cmd.ContainerID,
		})
		return
	}
	if h.handoff == nil {
		logger.Log("WARN", "Bootstrap EXPANSION has no hand-off launcher wired — cannot ensure the trade-fleet coordinator, so redirected hulls wait for one to be launched", map[string]interface{}{
			"action":       "bootstrap_trade_coordinator_expansion_skipped",
			"container_id": cmd.ContainerID,
		})
		return
	}
	if err := h.handoff.LaunchTradeFleetCoordinator(ctx, cmd.PlayerID, cmd.AgentSymbol); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Bootstrap EXPANSION failed to ensure the trade-fleet coordinator (the redirect still runs; retried next tick/boot, and the exit is not held on it): %v", err), map[string]interface{}{
			"action":       "bootstrap_trade_coordinator_expansion_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	logger.Log("INFO", "Bootstrap EXPANSION ensured the standing trade-fleet coordinator — the gate hulls handed to trade are put on continuous tours", map[string]interface{}{
		"action":       "bootstrap_trade_coordinator_ensured_expansion",
		"container_id": cmd.ContainerID,
	})
}

// redirectConstructionHullsToTrade hands the gate's construction hulls to the TRADE fleet once the arc
// reaches EXPANSION. The gate is built, and gate construction is the SOLE consumer of the
// "manufacturing" dedication — the goods-factory coordinator that also drew on it was retired with the
// factories — so every hull still carrying that tag is a large-cargo hull polling a finished
// site. Trade is where it earns again.
//
// It ASSIGNS rather than un-dedicates, and that distinction is load-bearing. Clearing the tag to "" (what
// the GATE-phase surplus re-balance does) works during GATE only because the contract scaler's
// reclaim-before-buy tier is mid-ramp and adopts from the idle pool. At EXPANSION there is no such
// adopter for trade: the trade coordinator partitions on hulls ALREADY tagged "trade", the fleet
// the growth coordinator tags only hulls it BUYS, and the capacity reconciler that once auto-pinned idle hulls was
// deleted. An un-dedicated gate hull would simply idle — strictly worse than leaving it alone.
//
// IDEMPOTENCE IS DERIVED, NOT STORED. There is no "already done" flag: the selection comes from the live
// observation, and a redirected hull carries "trade", so the observer stops counting it as a gate worker
// and the next tick selects an empty set and calls nothing. That is restart-safe for free (a dropped
// in-memory flag cannot desync from the world) and strictly better than a latch, which would write off
// any hull that happened to be mid-delivery on the one tick it fired. The adapter re-guards each hull at
// write time as well, so even a stale observation performs zero writes.
//
// Best-effort, exactly like the home-floor release beside it: it NEVER sets res.Blocker and never affects
// Done. A mature fleet must not be pinned in the per-tick full-fleet re-read over a fleet re-tag, and a
// failure leaves the hulls construction-dedicated, so the next tick — or the next daemon boot — retries.
func (h *RunBootstrapCoordinatorHandler) redirectConstructionHullsToTrade(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	hulls := selectConstructionHullsForTrade(obs)
	if len(hulls) == 0 {
		return // nothing free to redirect (the steady state once the hand-over has happened)
	}
	if cfg.DryRun {
		logger.Log("INFO", fmt.Sprintf("Bootstrap DRY-RUN: WOULD redirect %d idle gate-construction hull(s) to the trade fleet now the gate is built: %v (took no action)", len(hulls), hulls), map[string]interface{}{
			"action":       "bootstrap_would_redirect_construction_hulls",
			"container_id": cmd.ContainerID,
			"ships":        hulls,
		})
		return
	}
	if h.gateReleaser == nil {
		logger.Log("WARN", fmt.Sprintf("Bootstrap EXPANSION could not redirect %d idle gate-construction hull(s) to trade: no gate releaser wired — they keep the construction dedication until the next boot", len(hulls)), map[string]interface{}{
			"action":       "bootstrap_construction_hulls_to_trade_skipped",
			"container_id": cmd.ContainerID,
			"ships":        hulls,
		})
		return
	}

	redirected, err := h.gateReleaser.ReleaseGateWorkersToTrade(ctx, cmd.PlayerID, hulls)
	if err != nil {
		// The count is untrustworthy alongside an error, so nothing is tallied from a failed call — the
		// hulls stay construction-dedicated and the next tick/boot re-derives and retries.
		logger.Log("ERROR", fmt.Sprintf("Bootstrap EXPANSION failed to redirect gate-construction hulls to trade (retried next tick/boot; the exit is not held on it): %v", err), map[string]interface{}{
			"action":       "bootstrap_construction_hulls_to_trade_error",
			"container_id": cmd.ContainerID,
			"ships":        hulls,
		})
		return
	}
	if redirected == 0 {
		return // every candidate was re-guarded away at write time — nothing landed, nothing to report
	}
	res.ConstructionHullsToTrade += redirected
	logger.Log("INFO", fmt.Sprintf("Bootstrap EXPANSION redirected %d of %d idle gate-construction hull(s) to the trade fleet — the gate is built, so its workers go back to earning instead of polling a finished site", redirected, len(hulls)), map[string]interface{}{
		"action":       "bootstrap_construction_hulls_to_trade",
		"container_id": cmd.ContainerID,
		"redirected":   redirected,
		"candidates":   len(hulls),
		"ships":        hulls,
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

// launchHandoff launches the EXPANSION hand-off — the standing fleet-growth coordinator, the fleet's
// only heavy buyer — turning fleet growth over to demand. The launch must succeed to record the
// hand-off; a failure sets a blocker and leaves it for next tick (idempotent at the adapter, guarded
// on obs.GrowthRunning by the caller).
func (h *RunBootstrapCoordinatorHandler) launchHandoff(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", "Bootstrap DRY-RUN: WOULD launch the standing fleet-growth coordinator (the fleet's only heavy buyer) as the EXPANSION hand-off (took no action)", map[string]interface{}{
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
	if err := h.handoff.LaunchStandingCoordinators(ctx, cmd.PlayerID, cmd.AgentSymbol); err != nil {
		res.Blocker = "standing_launch_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap hand-off failed to launch the standing fleet-growth coordinator — the fleet still has no heavy buyer: %v", err), map[string]interface{}{
			"action":       "bootstrap_standing_launch_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.HandoffLaunched = true
	logger.Log("INFO", "Bootstrap launched the standing fleet-growth coordinator — the hand-off to the mature demand-driven economy: the fleet's only heavy buyer has started, and growth is demand-driven from here", map[string]interface{}{
		"action":       "bootstrap_handoff_launched",
		"container_id": cmd.ContainerID,
	})
}

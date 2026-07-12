package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/twinreport"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// actIncome runs the INCOME phase (Slice 2): the contract-income ramp. Three independently-guarded,
// idempotent actions on the observed delta, ordered so the fleet earns from tick 1 and never deadlocks:
//
//  1. Retire the command frigate from contract work IF it still carries the "contract" tag (it is a
//     poor contract worker: low fuel/cargo). A stale-tag safety — clearing it keeps the frigate out of
//     the contract coordinator's dedicated pool. Skipped when already retired.
//  2. Run batch-contract (idempotent — skip if already running). The existing fleet then earns
//     immediately (the frigate via the contract coordinator's general pool, until dedicated haulers
//     put it in exclusive mode), growing treasury so the staged hauler buys become affordable — this
//     is what avoids the "retire everything, nothing earns" deadlock.
//  3. Staged, capital-gated hauler acquisition — one light hauler per viable contract hub, capped at
//     hauler_target. The COUNT guard (haulers < desired) is the double-buy protection; placement picks
//     the top-ranked hub no hauler yet serves. At most one buy per tick (never a blind buy-all).
//
// Each action is guarded "already done / in-flight?" against the FRESH observation, so re-evaluation —
// including the first tick after a restart — never double-acts or double-buys.
func (h *RunBootstrapCoordinatorHandler) actIncome(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	// (1) Retire the frigate from contract work — only if it still carries the tag (idempotent).
	if obs.CommandFrigateOnContract && obs.CommandFrigateID != "" {
		h.retireFrigate(ctx, cmd, cfg, obs, res)
	}

	// (2) Drive batch-contract so the fleet earns — only if not already running (idempotent).
	if !obs.BatchContractRunning {
		h.ensureBatchContract(ctx, cmd, cfg, res)
	}

	// (3) Staged hauler acquisition — one per viable hub, capped at hauler_target. Compute the viable
	// hubs (pure) and the desired count; the count guard is the double-buy protection.
	hubs := selectContractHubs(obs.Markets, obs.ContractGoods)
	res.ViableHubs = len(hubs)
	desired := len(hubs)
	if desired > cfg.HaulerTarget {
		desired = cfg.HaulerTarget
	}
	if len(obs.Haulers) < desired {
		h.maybeBuyHauler(ctx, cmd, cfg, obs, hubs, res)
	}
}

// retireFrigate clears the command frigate's contract-fleet dedication (reuses fleet unassign). The
// caller has checked the frigate still carries the tag, so this always has an effect. Dry-run logs the
// intent and takes no action.
func (h *RunBootstrapCoordinatorHandler) retireFrigate(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", fmt.Sprintf("Bootstrap DRY-RUN: WOULD retire command frigate %s from contract work (poor fuel/cargo) (took no action)", obs.CommandFrigateID), map[string]interface{}{
			"action":       "bootstrap_would_retire_frigate",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
		return
	}
	if h.retirer == nil {
		res.Blocker = "no_retirer"
		logger.Log("WARN", "Bootstrap needs to retire the frigate from contracts but no retirer wired", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_retirer",
		})
		return
	}
	if err := h.retirer.RetireFromContract(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
		res.Blocker = "retire_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap frigate retire failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_retire_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.FrigateRetired = true
	logger.Log("INFO", fmt.Sprintf("Bootstrap retired command frigate %s from contract work (a poor contract worker: low fuel/cargo) — the contract coordinator's dedicated pool now uses only haulers", obs.CommandFrigateID), map[string]interface{}{
		"action":       "bootstrap_retired_frigate",
		"container_id": cmd.ContainerID,
		"ship":         obs.CommandFrigateID,
	})
	twinreport.Report("fleet-unassign", nil) // test-gated: no /v2 call for the twin to observe
}

// ensureBatchContract launches the contract fleet coordinator (workflow batch-contract) so the fleet
// earns. The caller has checked it is not already running, so this is the idempotent launch. Dry-run
// logs the intent and takes no action.
func (h *RunBootstrapCoordinatorHandler) ensureBatchContract(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if cfg.DryRun {
		logger.Log("INFO", "Bootstrap DRY-RUN: WOULD launch batch-contract on the contract fleet (took no action)", map[string]interface{}{
			"action":       "bootstrap_would_run_batch_contract",
			"container_id": cmd.ContainerID,
		})
		return
	}
	if h.contractRun == nil {
		res.Blocker = "no_contract_runner"
		logger.Log("WARN", "Bootstrap needs to run batch-contract but no contract runner wired", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_contract_runner",
		})
		return
	}
	if err := h.contractRun.StartBatchContract(ctx, cmd.PlayerID); err != nil {
		res.Blocker = "batch_contract_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap batch-contract launch failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_batch_contract_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.ContractRun = true
	logger.Log("INFO", "Bootstrap launched batch-contract on the contract fleet — the fleet now earns while the hauler ramp stages", map[string]interface{}{
		"action":       "bootstrap_ran_batch_contract",
		"container_id": cmd.ContainerID,
	})
	twinreport.Report("batch-contract", nil) // test-gated: no /v2 call for the twin to observe
}

// maybeBuyHauler evaluates and (unless dry-run) executes ONE staged hauler buy behind the readiness
// and capital gates, placing it on the highest-ranked viable hub no hauler yet serves. It emits the
// same guardrail arithmetic as the probe buy (RULINGS #4, fail closed). Caller has checked "needed"
// (haulers < desired = min(viable hubs, hauler_target)).
func (h *RunBootstrapCoordinatorHandler) maybeBuyHauler(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, hubs []Hub, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	// In-flight guard (st-drm.6): don't dispatch another hauler buy while one this coordinator already
	// launched is still on its way (its hull not yet dedicated + counted in the observation).
	if h.acquisitionInFlight(ctx, cmd, res, cfg.HaulerShipType, "bootstrap_income_blocked") {
		return
	}

	// Placement: the top-ranked viable hub (within the cap) that no hauler already serves. Empty means
	// every capped hub is served — shouldn't happen given the caller's count guard, but fail-closed.
	hub := firstUnservedHub(hubs, obs.Haulers, cfg.HaulerTarget)
	if hub == "" {
		res.Blocker = "no_unserved_hub"
		logger.Log("WARN", fmt.Sprintf("Bootstrap hauler needed (%d/%d haulers) but every viable hub is already served — no placement target", len(obs.Haulers), cfg.HaulerTarget), map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_unserved_hub",
		})
		return
	}

	// Readiness gate: an idle hull must exist to fly to the yard and execute the buy. No idle hull ⇒
	// BLOCKED (not failed) — a later tick with a free hull retries.
	if !obs.HasIdlePurchaser {
		res.Blocker = "no_purchaser"
		logger.Log("WARN", fmt.Sprintf("Bootstrap hauler needed (%d/%d, hub %s) but BLOCKED: no idle hull to execute the purchase", len(obs.Haulers), cfg.HaulerTarget, hub), map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_purchaser",
		})
		return
	}

	if h.haulAcquirer == nil {
		res.Blocker = "no_hauler_acquirer"
		logger.Log("WARN", "Bootstrap hauler needed but no hauler acquirer wired — cannot price-check or buy", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_hauler_acquirer",
		})
		return
	}

	// Price-check first (reuse shipyard list). Unreadable price ⇒ the capital gate fails CLOSED.
	price, yard, readable, err := h.haulAcquirer.PriceCheck(ctx, cmd.PlayerID, cfg.HaulerShipType)
	if err != nil || !readable {
		res.Blocker = "price_unreadable"
		logger.Log("WARN", fmt.Sprintf("Bootstrap hauler price unreadable — failing closed (no buy): err=%v", err), map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "price_unreadable",
		})
		return
	}

	// Capital gate: spend ≤ reserve_margin × treasury (the money-guard + pacer). Emit the full
	// arithmetic so the captain retunes from evidence. A ~300k hauler simply waits until contracts have
	// grown treasury past ~2× its price — the staging that falls out of the ≤50% cap.
	capBudget := int64(float64(obs.Treasury) * cfg.ReserveMargin)
	affordable := price <= capBudget
	logger.Log("INFO", fmt.Sprintf("Bootstrap hauler buy decision: price=%d treasury=%d cap=(reserve_margin %.2f × treasury)=%d affordable=(price≤cap)=%v hub=%s yard=%s — %s", price, obs.Treasury, cfg.ReserveMargin, capBudget, affordable, hub, yard, buyBlockNote(affordable)), map[string]interface{}{
		"action":         "bootstrap_hauler_buy_decision",
		"container_id":   cmd.ContainerID,
		"price":          price,
		"treasury":       obs.Treasury,
		"cap":            capBudget,
		"reserve_margin": cfg.ReserveMargin,
		"affordable":     affordable,
		"hub":            hub,
		"yard":           yard,
	})
	if !affordable {
		res.Blocker = "capital_gate"
		return
	}

	if cfg.DryRun {
		res.WouldBuy++
		logger.Log("INFO", fmt.Sprintf("Bootstrap DRY-RUN: WOULD buy 1 %s at %s for %d and place it on hub %s (took no action)", cfg.HaulerShipType, yard, price, hub), map[string]interface{}{
			"action":       "bootstrap_would_buy_hauler",
			"container_id": cmd.ContainerID,
		})
		return
	}

	bought, err := h.haulAcquirer.BuyAndPlace(ctx, cmd.PlayerID, cfg.HaulerShipType, yard, hub)
	if err != nil {
		res.Blocker = "purchase_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap hauler purchase failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_hauler_buy_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.HaulersBought++
	if h.metrics != nil {
		h.metrics.RecordHaulerPurchased()
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap bought contract hauler %s at %s for %d, dedicated + placed on hub %s (%d/%d haulers, %d viable hubs)", bought.ShipSymbol, yard, bought.Price, hub, len(obs.Haulers)+1, cfg.HaulerTarget, res.ViableHubs), map[string]interface{}{
		"action":       "bootstrap_bought_hauler",
		"container_id": cmd.ContainerID,
		"ship":         bought.ShipSymbol,
		"price":        bought.Price,
		"hub":          hub,
	})
}

// firstUnservedHub returns the highest-ranked viable hub (within the hauler_target cap) that no
// existing hauler is placed on, or "" when all capped hubs are served. A hub is "served" when some
// hauler's Waypoint is on it (idle at, or heading to) — so a hauler bought last tick and still en
// route keeps its hub from being re-selected. The reconciler's count guard caps total buys regardless,
// so even a mis-placement (from a churned ranking) can never overshoot hauler_target.
func firstUnservedHub(hubs []Hub, haulers []HaulerSnapshot, cap int) string {
	served := make(map[string]struct{}, len(haulers))
	for _, hl := range haulers {
		if hl.Waypoint != "" {
			served[hl.Waypoint] = struct{}{}
		}
	}
	limit := len(hubs)
	if limit > cap {
		limit = cap
	}
	for i := 0; i < limit; i++ {
		if _, ok := served[hubs[i].Waypoint]; !ok {
			return hubs[i].Waypoint
		}
	}
	return ""
}

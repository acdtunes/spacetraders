package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// tradeFleetTag is the dedicated-fleet tag the standing trade-fleet coordinator selects on (matches the
// trading package's tradeFleet). The cold-start hull-routing trade-seed (sp-192k4) buys ONE hull and dedicates
// it to this fleet so acquisition #2 becomes a trade hull, decoupled from the contract op — the trade
// coordinator, contract coordinator, and contract scaler stay phase-BLIND (all the phase logic lives here).
const tradeFleetTag = "trade"

// actIncome runs the CONTRACT-INCOME workstream (Slice 2): the contract-income ramp. Four independently-guarded,
// idempotent actions on the observed delta, ordered so the fleet earns from tick 1 and never deadlocks:
//
//  1. Retire the command frigate from contract work IF it still carries the "contract" tag (it is a
//     poor contract worker: low fuel/cargo). A stale-tag safety — clearing it keeps the frigate out of
//     the contract coordinator's dedicated pool. Skipped when already retired.
//  2. Run batch-contract (idempotent — skip if already running). The existing fleet then earns
//     immediately (the frigate via the contract coordinator's general pool, until dedicated haulers
//     put it in exclusive mode), growing treasury so the staged hauler buys become affordable — this
//     is what avoids the "retire everything, nothing earns" deadlock.
//  3. Start the command frigate's OWN continuous single-hull contract loop once probe provisioning is
//     done (sp-rype, reusing the sp-ehg9 batch-contract --loop primitive). The frigate is the pre-hauler
//     sole earner but the contract fleet coordinator does not keep it earning (sp-ehg9), so without this
//     the frigate parks idle at the shipyard after its hour-0 probe buy and income never flows — the
//     stall this fixes. The loop and the coordinator are mutually exclusive at the daemon (per-player
//     single CONTRACT_WORKFLOW worker), so there is no double-claim (RULINGS #7).
//  4. Staged, capital-gated hull acquisition, ROUTED BY ORDER (sp-192k4): acquisition #1 → the contract
//     fleet, #2 → the TRADE fleet (the trade-seed, held until the first contract hull exists), #3… →
//     contract again. One light hauler per viable contract hub, capped at haulerTarget. The COUNT guard
//     (haulers < desired) is the double-buy protection; placement picks the top-ranked hub no hauler yet
//     serves. At most one buy per tick (never a blind buy-all). The trade hull is decoupled — it does not
//     count toward the contract scaler's ceiling, and all the phase logic lives HERE (the trade/contract
//     coordinators + scaler stay phase-blind).
//
// Each action is guarded "already done / in-flight?" against the FRESH observation, so re-evaluation —
// including the first tick after a restart — never double-acts or double-buys.
func (h *RunBootstrapCoordinatorHandler) actIncome(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	// CONTRACT GRADUATION (sp-difa.1): a graduated player has DURABLY retired contracts as the funding
	// floor (the operator's manual, era-scoped decision). This whole workstream is contract-income
	// — batch-contract, the frigate sole-earner loop, staged hauler buys — so when graduated it does
	// NOTHING: bootstrap never (re)starts or maintains a contract earner, even after a boot-standing
	// relaunch. Fail-OPEN (obs.ContractGraduated defaults false on a fresh era / read miss) ⇒ byte-identical
	// to today. Scanning (actData) and gate construction (actGate) run regardless — this gates ONLY
	// contract income, and never touches trade.
	if obs.ContractGraduated {
		res.Blocker = "contract_graduated"
		common.LoggerFromContext(ctx).Log("INFO", "Bootstrap contract workstream OFF: player is contract-graduated — not starting/maintaining any contract earner (durable, sp-difa.1); scanning + gate construction + trade unaffected", map[string]interface{}{
			"action":       "bootstrap_contract_graduated",
			"container_id": cmd.ContainerID,
		})
		return
	}

	// (1) Retire the frigate from contract work — only if it still carries the tag (idempotent).
	if obs.CommandFrigateOnContract && obs.CommandFrigateID != "" {
		h.retireFrigate(ctx, cmd, obs, res)
	}

	// (2) Drive batch-contract so the fleet earns — only if not already running (idempotent).
	if !obs.BatchContractRunning {
		h.ensureBatchContract(ctx, cmd, res)
	}

	// (3) Put the command frigate on its OWN continuous contract loop once provisioning is done, so it
	// EARNS as the pre-hauler sole earner instead of parking idle at the shipyard (sp-rype). Guarded on
	// probes≥target (the frigate juggles buy-probes-first, then earn — sp-t39j) AND the frigate not
	// already looping (obs.FrigateContractLoopRunning — the earner-signal, so it starts exactly once and
	// never double-claims). A resolved frigate is required (no guess). sp-7r7w: also gated on ZERO
	// haulers — the loop is the PRE-hauler earner ONLY, and the first-hauler pivot (step 4) STOPS it to
	// free the frigate as the purchaser; once a hauler exists the loop must NEVER (re)start (the hauler +
	// the scaled fleet earn, and the frigate is retired to the exclusive purchasing role), so this gate is
	// also what keeps the pivot durable across a restart.
	if obs.CommandFrigateID != "" && obs.ProbeCount >= probeTarget && !obs.FrigateContractLoopRunning && len(obs.Haulers) == 0 && !obs.CommandFrigatePurchasing {
		h.startFrigateContractLoop(ctx, cmd, obs, res)
	}

	// (4) Staged hull acquisition — one per viable hub, capped at haulerTarget. Compute the viable hubs
	// (pure) and the desired count; the count guard is the double-buy protection.
	hubs := selectContractHubs(obs.Markets, obs.ContractGoods)
	res.ViableHubs = len(hubs)
	desired := len(hubs)
	if desired > haulerTarget {
		desired = haulerTarget
	}
	contractHaulers := len(obs.Haulers)

	// (4a) HULL-ROUTING (sp-192k4): route cold-start light-hull acquisitions by order — #1 →
	// contract, #2 → TRADE, #3… → contract. Once the FIRST contract hull exists and no trade hull does yet,
	// seed ONE trade-dedicated hull + ensure the trade coordinator, then RETURN so THIS tick's acquisition
	// is the trade seed, not a 2nd contract hauler. The trade hull EXISTING (obs.TradeHullCount) is the
	// durable, observable "seeded" signal — idempotent by construction, re-derived each tick, restart-safe
	// (no stored flag). The trade hull is decoupled from the contract op: it does NOT count toward the
	// contract scaler's ceiling, and the trade/contract coordinators + scaler stay phase-BLIND.
	if contractHaulers >= 1 && obs.TradeHullCount == 0 {
		h.maybeSeedTradeHull(ctx, cmd, obs, res)
		return
	}

	// (4b) Staged contract-hauler acquisition — HELD at 1 contract hull until the trade hull is seeded
	// (the (contractHaulers == 0 || TradeHullCount >= 1) guard), so acquisition #2 is the trade seed above:
	// contractHaulers==0 still buys contract #1 unchanged; once a trade hull exists contract buying resumes
	// for #3…, capped at desired.
	if contractHaulers < desired && (contractHaulers == 0 || obs.TradeHullCount >= 1) {
		h.maybeBuyHauler(ctx, cmd, obs, hubs, res)
	}
}

// retireFrigate clears the command frigate's contract-fleet dedication (reuses fleet unassign). The
// caller has checked the frigate still carries the tag, so this always has an effect.
func (h *RunBootstrapCoordinatorHandler) retireFrigate(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

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
}

// ensureBatchContract launches the contract fleet coordinator (workflow batch-contract) so the fleet
// earns. The caller has checked it is not already running, so this is the idempotent launch.
func (h *RunBootstrapCoordinatorHandler) ensureBatchContract(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

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
}

// startFrigateContractLoop puts the command frigate on its continuous single-hull contract loop so it
// runs contracts as the pre-hauler sole earner (sp-rype, reusing the sp-ehg9 batch-contract --loop
// primitive: BatchContractWorkflow with iterations=-1). The caller has checked provisioning is done
// (probes≥target), the frigate is resolved, and no loop is already running — so this is the guarded,
// idempotent start. A nil starter degrades to a logged skip surfaced as a blocker (never a panic),
// matching the other contract collaborators' nil contract.
func (h *RunBootstrapCoordinatorHandler) startFrigateContractLoop(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if h.frigateLoop == nil {
		res.Blocker = "no_frigate_loop_starter"
		logger.Log("WARN", "Bootstrap needs to start the frigate contract loop but no starter wired — the frigate would park idle after the probe buy (sp-rype)", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_frigate_loop_starter",
		})
		return
	}
	if err := h.frigateLoop.StartLoop(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
		res.Blocker = "frigate_loop_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap frigate contract-loop start failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_frigate_loop_error",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
		return
	}
	res.FrigateLoopStarted = true
	logger.Log("INFO", fmt.Sprintf("Bootstrap started the command frigate %s on its continuous contract loop — it now EARNS as the pre-hauler sole earner (sp-ehg9 batch-contract --loop) instead of parking idle at the shipyard (sp-rype)", obs.CommandFrigateID), map[string]interface{}{
		"action":       "bootstrap_started_frigate_loop",
		"container_id": cmd.ContainerID,
		"ship":         obs.CommandFrigateID,
	})
}

// maybeBuyHauler evaluates and (unless dry-run) executes ONE staged hauler buy behind the readiness
// and capital gates, placing it on the highest-ranked viable hub no hauler yet serves. It emits the
// same guardrail arithmetic as the probe buy (RULINGS #4, fail closed). Caller has checked "needed"
// (haulers < desired = min(viable hubs, haulerTarget)).
func (h *RunBootstrapCoordinatorHandler) maybeBuyHauler(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, hubs []Hub, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	// Placement: the top-ranked viable hub (within the cap) that no hauler already serves. Empty means
	// every capped hub is served — shouldn't happen given the caller's count guard, but fail-closed.
	hub := firstUnservedHub(hubs, obs.Haulers, haulerTarget)
	if hub == "" {
		res.Blocker = "no_unserved_hub"
		logger.Log("WARN", fmt.Sprintf("Bootstrap hauler needed (%d/%d haulers) but every viable hub is already served — no placement target", len(obs.Haulers), haulerTarget), map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_unserved_hub",
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

	// Readiness / FIRST-HAULER PIVOT decision (sp-7r7w). Determined BEFORE the price-check so a genuine
	// no-purchaser blocks cheaply without a shipyard read — the pre-sp-7r7w behavior.
	//
	// The FIRST hauler is bought by freeing the command frigate from its pre-hauler sole-earner loop to
	// serve as THE purchaser (the documented first-hauler pivot) — the frigate is retired to contracts at
	// this pivot regardless of whether a stray hull happens to be idle, so the exclusive purchasing ship
	// is always established. The pivot fires ONLY for the cash-flow-critical FIRST hauler and only at a
	// SAFE point:
	//   - len(Haulers)==0 (the first hauler only — a later hauler buys off an incidentally-idle hull),
	//   - the frigate loop is what holds it (FrigateContractLoopRunning) and the frigate is resolved,
	//   - the frigate carries NO contract cargo (FrigateCargoEmpty) — stopping mid-delivery would lose
	//     cargo, so a loaded frigate defers the pivot a tick (the loop delivers + empties), and
	//   - the loop-stopper is wired.
	// The actual loop-STOP is deferred until AFTER the capital gate below, so the frigate only ever stops
	// earning once the buy is affordable AND warranted. When NOT pivoting, an existing idle hull buys as
	// before; with neither an idle hull nor a pivot available, BLOCK (no_purchaser) and retry.
	pivot := len(obs.Haulers) == 0 && obs.FrigateContractLoopRunning && obs.CommandFrigateID != "" && obs.FrigateCargoEmpty && h.frigateLoop != nil
	// committedPurchaser: a prior tick already FREED + DEDICATED the frigate as the exclusive buy ship
	// (sp-5nd2 fault-2 pivot), so it is THE purchaser even while it is still navigating to the shipyard for
	// the cold-price read — recognise it here so an en-route tick surfaces "positioning", not no_purchaser.
	committedPurchaser := len(obs.Haulers) == 0 && obs.CommandFrigatePurchasing && obs.CommandFrigateID != ""
	if !pivot && !committedPurchaser && !obs.HasIdlePurchaser {
		res.Blocker = "no_purchaser"
		logger.Log("WARN", fmt.Sprintf("Bootstrap hauler needed (%d/%d, hub %s) but BLOCKED: no idle hull to execute the purchase and the first-hauler pivot is unavailable (haulers=%d loop_running=%v cargo_empty=%v) — retry next tick", len(obs.Haulers), haulerTarget, hub, len(obs.Haulers), obs.FrigateContractLoopRunning, obs.FrigateCargoEmpty), map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_purchaser",
		})
		return
	}

	// Price-check (reuse shipyard list — a live, PRESENCE-GATED GetShipyard). Unreadable ⇒ the capital gate
	// cannot be evaluated, so nothing is bought this tick (RULINGS #4 fail-closed).
	price, yard, readable, err := h.haulAcquirer.PriceCheck(ctx, cmd.PlayerID, haulerShipType)
	if err != nil || !readable {
		// sp-5nd2 fault-2: on cold start the price is unreadable because NOTHING is at the home shipyard (the
		// frigate is on its loop, the probes are scouting), so the presence-gated live read returns no priced
		// listing. Rather than fail closed forever (the deadlock), FREE the command frigate at the inter-contract
		// window and get it to the yard so the NEXT tick's read succeeds and the buy — which itself navigates +
		// docks + reads + purchases (BatchPurchaseShips) — runs behind bootstrap's own working-capital floor,
		// which stays HERE and is evaluated on the real price once it reads.
		h.ensureHaulerPriceReadable(ctx, cmd, obs, res, pivot, err)
		return
	}

	// sp-muc5x: cache the just-read hauler price so a later COLD-price tick can test affordability BEFORE
	// freeing the frigate's earning loop (the deadlock this bead fixes). This refreshes the probe-buy seed with
	// the live price whenever the yard reads — it stores an already-read value (no extra API call).
	h.cacheHaulerPrice(cmd.ContainerID, price)

	// Capital gate (sp-acv5): buy as soon as the treasury AFTER the buy still clears the ABSOLUTE
	// contract working-capital floor — affordable ⇔ cushion=(treasury−price) ≥ contract_working_capital_floor.
	// The hauler exists to SCALE cash flow, so it is bought as soon as the buy leaves a safe goods+fuel
	// operating cushion (PLAYBOOK §3) — an absolute floor, not a fraction of a growing balance. The probe
	// buy gates on the same shape against common.ImmutableReserveFloor (see run_bootstrap_reconcile.go).
	// RULINGS #4 fail-closed: an unreadable price already returned above, and a cushion below the floor
	// does NOT buy — so after a permitted buy treasury ≥ floor by construction (the working-capital safety).
	cushion := obs.Treasury - price
	affordable := cushion >= contractWorkingCapitalFloor
	floorNote := "clears the working-capital floor"
	if !affordable {
		floorNote = "BLOCKED by the working-capital floor (treasury−price below the contract working-capital floor)"
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap hauler buy decision: price=%d treasury=%d floor=%d cushion=(treasury−price)=%d affordable=(cushion≥floor)=%v hub=%s yard=%s — %s", price, obs.Treasury, contractWorkingCapitalFloor, cushion, affordable, hub, yard, floorNote), map[string]interface{}{
		"action":       "bootstrap_hauler_buy_decision",
		"container_id": cmd.ContainerID,
		"price":        price,
		"treasury":     obs.Treasury,
		"floor":        contractWorkingCapitalFloor,
		"cushion":      cushion,
		"affordable":   affordable,
		"hub":          hub,
		"yard":         yard,
	})
	if !affordable {
		res.Blocker = "capital_gate"
		return
	}

	// Execute the pivot BEFORE the buy: stop the frigate loop (StopContainer releases its work-claim →
	// idle), then dedicate it the exclusive purchasing ship. Dedicating BEFORE the buy keeps the
	// invariant "the frigate is protected before it is used or left idle": a later buy failure leaves an
	// idle, purchasing-dedicated frigate (the loop cannot re-claim a foreign-dedicated hull), which the
	// next tick simply reuses as the purchaser. A stop/dedicate failure returns (retry next tick).
	if pivot {
		if err := h.frigateLoop.StopLoop(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
			res.Blocker = "frigate_loop_stop_error"
			logger.Log("ERROR", fmt.Sprintf("Bootstrap first-hauler pivot: stopping the command frigate %s contract loop failed — no purchaser this tick: %v", obs.CommandFrigateID, err), map[string]interface{}{
				"action":       "bootstrap_frigate_pivot_stop_error",
				"container_id": cmd.ContainerID,
				"ship":         obs.CommandFrigateID,
			})
			return
		}
		if h.retirer == nil {
			res.Blocker = "no_retirer"
			logger.Log("WARN", "Bootstrap first-hauler pivot: frigate loop stopped but no retirer wired to dedicate the purchasing ship", map[string]interface{}{
				"action":       "bootstrap_income_blocked",
				"container_id": cmd.ContainerID,
				"blocker":      "no_retirer",
			})
			return
		}
		if err := h.retirer.DedicateAsPurchaser(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
			res.Blocker = "frigate_dedicate_error"
			logger.Log("ERROR", fmt.Sprintf("Bootstrap first-hauler pivot: dedicating the command frigate %s as the exclusive purchasing ship failed — retry next tick: %v", obs.CommandFrigateID, err), map[string]interface{}{
				"action":       "bootstrap_frigate_pivot_dedicate_error",
				"container_id": cmd.ContainerID,
				"ship":         obs.CommandFrigateID,
			})
			return
		}
		res.FrigatePivoted = true
		logger.Log("INFO", fmt.Sprintf("Bootstrap first-hauler PIVOT: STOPPED the command frigate %s contract loop and dedicated it the EXCLUSIVE purchasing ship (protected, never re-drafted) — it now buys hauler #1 and stands by as the buy ship for every subsequent purchase (the documented first-hauler pivot; the hauler + scaled fleet take over earning)", obs.CommandFrigateID), map[string]interface{}{
			"action":       "bootstrap_frigate_pivot",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
	}

	// The purchaser is the command frigate whenever the pivot owns it — freed THIS tick (pivot) or already
	// freed+dedicated on a prior tick and now positioned at the yard (committedPurchaser). Otherwise "" lets
	// the acquirer use any idle hull (the direct-buy behavior for a subsequent hauler bootstrap buys itself).
	purchaser := ""
	if pivot || committedPurchaser {
		purchaser = obs.CommandFrigateID
	}
	bought, err := h.haulAcquirer.BuyAndPlace(ctx, cmd.PlayerID, haulerShipType, yard, hub, purchaser)
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
	logger.Log("INFO", fmt.Sprintf("Bootstrap bought contract hauler %s at %s for %d, dedicated + placed on hub %s (%d/%d haulers, %d viable hubs)", bought.ShipSymbol, yard, bought.Price, hub, len(obs.Haulers)+1, haulerTarget, res.ViableHubs), map[string]interface{}{
		"action":       "bootstrap_bought_hauler",
		"container_id": cmd.ContainerID,
		"ship":         bought.ShipSymbol,
		"price":        bought.Price,
		"hub":          hub,
	})
}

// maybeSeedTradeHull seeds acquisition #2 to the TRADE fleet (sp-192k4 hull-routing). The caller has
// checked the routing gate (one contract hull exists, no trade hull yet — obs.TradeHullCount==0, the durable
// observable "seeded" signal). It buys ONE hull, dedicates it to the trade fleet (NO hub — a trade hull runs
// continuous tours the coordinator assigns, not a fixed contract hub), then ensures the standing trade-fleet
// coordinator so the hull is put on a tour.
//
// MONEY GUARD (RULINGS #4): it carries the SAME price-check + working-capital gate as maybeBuyHauler because
// the trade hull occupies acquisition-slot #2 — the slot that as a contract hauler is capital-gated at
// contract_working_capital_floor — so re-routing #2 to trade must NOT weaken the guard on that spend (an
// unreadable price or a short cushion buys nothing this tick). No first-hauler pivot / cold-price dance is
// needed: by this point the command frigate is already the exclusive purchasing ship (established freeing it
// for contract #1) and idle at the yard, so it is the deterministic purchaser. Both collaborators are required
// UP FRONT (never buy a trade hull we cannot then manage) — a nil acquirer/launcher is a logged skip surfaced
// as a blocker (like the maybeBuyHauler nil guards), never a panic.
func (h *RunBootstrapCoordinatorHandler) maybeSeedTradeHull(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if h.haulAcquirer == nil {
		res.Blocker = "no_hauler_acquirer"
		logger.Log("WARN", "Bootstrap trade-seed needed but no hauler acquirer wired — cannot price-check or buy", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_hauler_acquirer",
		})
		return
	}
	// Require the launcher BEFORE buying — never strand a purchased trade hull with no coordinator to run it.
	if h.handoff == nil {
		res.Blocker = "no_handoff_launcher"
		logger.Log("WARN", "Bootstrap trade-seed needed but no hand-off launcher wired — cannot ensure the trade coordinator, so not buying a trade hull we could not then manage", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_handoff_launcher",
		})
		return
	}

	// Price-check (reuse shipyard list — a live, PRESENCE-GATED GetShipyard). Unreadable ⇒ the capital gate
	// cannot be evaluated, so nothing is bought this tick (RULINGS #4 fail-closed). By the trade-seed point the
	// purchasing frigate is at the yard from the contract-#1 buy, so the price normally reads.
	price, yard, readable, err := h.haulAcquirer.PriceCheck(ctx, cmd.PlayerID, haulerShipType)
	if err != nil || !readable {
		res.Blocker = "price_unreadable"
		logger.Log("WARN", fmt.Sprintf("Bootstrap trade-seed price unreadable — failing closed (no buy): err=%v", err), map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "price_unreadable",
		})
		return
	}

	// Capital gate (sp-acv5): affordable ⇔ cushion=(treasury−price) ≥ contract_working_capital_floor — the SAME
	// floor the contract-hauler buy this slot displaces uses, so re-routing #2 to trade never weakens the money
	// guard (RULINGS #4/#5). A cushion below the floor does NOT buy.
	cushion := obs.Treasury - price
	affordable := cushion >= contractWorkingCapitalFloor
	floorNote := "clears the working-capital floor"
	if !affordable {
		floorNote = "BLOCKED by the working-capital floor (treasury−price below the contract working-capital floor)"
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap trade-seed buy decision: price=%d treasury=%d floor=%d cushion=(treasury−price)=%d affordable=(cushion≥floor)=%v yard=%s — %s", price, obs.Treasury, contractWorkingCapitalFloor, cushion, affordable, yard, floorNote), map[string]interface{}{
		"action":       "bootstrap_trade_seed_buy_decision",
		"container_id": cmd.ContainerID,
		"price":        price,
		"treasury":     obs.Treasury,
		"floor":        contractWorkingCapitalFloor,
		"cushion":      cushion,
		"affordable":   affordable,
		"yard":         yard,
	})
	if !affordable {
		res.Blocker = "capital_gate"
		return
	}

	// Buy + dedicate to the trade fleet (no hub). The purchaser is the command frigate — the exclusive
	// purchasing ship established at the first-hauler pivot — so the buy is deterministic, not dependent on an
	// incidentally-idle hull.
	bought, err := h.haulAcquirer.BuyAndDedicate(ctx, cmd.PlayerID, haulerShipType, yard, tradeFleetTag, obs.CommandFrigateID)
	if err != nil {
		res.Blocker = "trade_seed_purchase_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap trade-seed purchase failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_trade_seed_buy_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.TradeHullSeeded = true
	logger.Log("INFO", fmt.Sprintf("Bootstrap seeding the 2nd cold-start hull to the TRADE fleet (hull-routing) + ensuring the trade coordinator: bought %s at %s for %d, dedicated %q (does NOT count toward the contract scaler ceiling)", bought.ShipSymbol, yard, bought.Price, tradeFleetTag), map[string]interface{}{
		"action":       "bootstrap_seeded_trade_hull",
		"container_id": cmd.ContainerID,
		"ship":         bought.ShipSymbol,
		"price":        bought.Price,
	})

	// Ensure the standing trade-fleet coordinator so the seeded hull is put on a continuous tour. Idempotent
	// (skips a running coordinator). Best-effort BACKGROUND launch: it surfaces a failure on its own ERROR line
	// and never claims res.Blocker (the seed itself succeeded), mirroring the early autosizer/contract-scaler
	// launches.
	if err := h.handoff.LaunchTradeFleetCoordinator(ctx, cmd.PlayerID, cmd.AgentSymbol); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Bootstrap trade-seed: launching the trade-fleet coordinator failed (the seeded hull will idle until it is launched): %v", err), map[string]interface{}{
			"action":       "bootstrap_trade_coordinator_launch_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	logger.Log("INFO", "Bootstrap ensured the trade-fleet coordinator (trade-seed) — the seeded trade hull is now managed on continuous tours", map[string]interface{}{
		"action":       "bootstrap_trade_coordinator_ensured",
		"container_id": cmd.ContainerID,
	})
}

// ensureHaulerPriceReadable breaks the sp-5nd2 fault-2 cold-start deadlock: the first-hauler buy needs the
// live LIGHT_SHUTTLE price, but the home shipyard listing is PRESENCE-GATED (GetShipyard returns a priced
// listing only with a hull AT the yard) and on cold start nothing is there — the frigate is on its contract
// loop, the probes are scouting. Rather than fail closed forever, it FREES the command frigate at the
// inter-contract SAFE POINT (StopLoop + DedicateAsPurchaser — command duty per RULINGS #7; cargo-empty so no
// in-flight contract is lost) and POSITIONS it at the home shipyard, so the NEXT tick's read succeeds and the
// buy runs behind the working-capital floor. The buy itself (BatchPurchaseShips) navigates + docks + reads +
// purchases; positioning just makes bootstrap's OWN pre-buy floor guard evaluable on the real price. It NEVER
// buys and NEVER weakens the price guard (RULINGS #4) — the tick spends nothing while unreadable — and it
// NEVER stops a frigate it cannot then position (no earner lost for nothing). With no scanner wired it
// keeps the pre-fix fail-closed behavior exactly.
func (h *RunBootstrapCoordinatorHandler) ensureHaulerPriceReadable(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult, pivot bool, priceErr error) {
	logger := common.LoggerFromContext(ctx)

	// Positioning needs the shipyard scanner. Without it, keep the pre-fix fail-closed behavior EXACTLY — and
	// never stop the frigate's earning loop when we could not then position it (no earner lost for nothing).
	if h.scanner == nil {
		res.Blocker = "price_unreadable"
		logger.Log("WARN", fmt.Sprintf("Bootstrap hauler price unreadable and no shipyard scanner wired — failing closed (no buy): err=%v", priceErr), map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "price_unreadable",
		})
		return
	}

	// FREE the frigate at the inter-contract window when the pivot is warranted but it is still on its loop
	// (it must idle before it can go to the yard). Once dedicated it is the committed purchaser, so a later
	// unreadable tick (still en route) re-positions idempotently WITHOUT re-freeing.
	frigateReady := obs.CommandFrigatePurchasing && obs.CommandFrigateID != ""
	if pivot {
		// sp-muc5x — NEVER free the frigate's earning loop while the hauler is UNAFFORDABLE. The live price is
		// unreadable here (cold yard), so gate the free on the CACHED last-hauler-price seeded at the probe buy. When a
		// cache exists AND the buy would breach the working-capital floor (treasury−price < floor), keep the
		// frigate ON its contract loop EARNING (blocker=capital_gate) and return — it is freed to position+buy
		// only once the treasury clears the floor. This restores the invariant the deadlock violated: the
		// frigate was freed while permanently unaffordable, so no earner remained and the treasury never grew
		// (permanent stall). No money guard is weakened — this is the SAME cushion≥floor test as the capital
		// gate on the readable path (RULINGS #4/#5); it only TIGHTENS *when* the frigate is freed. A 0/absent
		// cache (first-ever read, e.g. a fresh boot before that seed) proceeds to the existing free+position.
		if cached := h.cachedHaulerPrice(cmd.ContainerID); cached > 0 && obs.Treasury-cached < contractWorkingCapitalFloor {
			res.Blocker = "capital_gate"
			logger.Log("INFO", fmt.Sprintf("Bootstrap first-hauler pivot HELD (not freeing the earner): cached hauler price=%d treasury=%d cushion=(treasury−price)=%d floor=%d — keeping the command frigate %s on its contract loop EARNING until the treasury clears the working-capital floor (sp-muc5x: never stop the sole earner while the hauler is unaffordable)", cached, obs.Treasury, obs.Treasury-cached, contractWorkingCapitalFloor, obs.CommandFrigateID), map[string]interface{}{
				"action":       "bootstrap_pivot_held_unaffordable",
				"container_id": cmd.ContainerID,
				"blocker":      "capital_gate",
				"cached_price": cached,
				"treasury":     obs.Treasury,
				"floor":        contractWorkingCapitalFloor,
				"ship":         obs.CommandFrigateID,
			})
			return
		}
		if h.retirer == nil {
			res.Blocker = "price_unreadable"
			logger.Log("WARN", "Bootstrap hauler price unreadable and no retirer wired to free the command frigate — failing closed (no buy)", map[string]interface{}{
				"action":       "bootstrap_income_blocked",
				"container_id": cmd.ContainerID,
				"blocker":      "price_unreadable",
			})
			return
		}
		if err := h.frigateLoop.StopLoop(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
			res.Blocker = "frigate_loop_stop_error"
			logger.Log("ERROR", fmt.Sprintf("Bootstrap first-hauler pivot: stopping the command frigate %s contract loop to position it for the price read failed — retry next tick: %v", obs.CommandFrigateID, err), map[string]interface{}{
				"action":       "bootstrap_frigate_pivot_stop_error",
				"container_id": cmd.ContainerID,
				"ship":         obs.CommandFrigateID,
			})
			return
		}
		if err := h.retirer.DedicateAsPurchaser(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
			res.Blocker = "frigate_dedicate_error"
			logger.Log("ERROR", fmt.Sprintf("Bootstrap first-hauler pivot: dedicating the command frigate %s as the exclusive purchasing ship failed — retry next tick: %v", obs.CommandFrigateID, err), map[string]interface{}{
				"action":       "bootstrap_frigate_pivot_dedicate_error",
				"container_id": cmd.ContainerID,
				"ship":         obs.CommandFrigateID,
			})
			return
		}
		res.FrigatePivoted = true
		frigateReady = true
		logger.Log("INFO", fmt.Sprintf("Bootstrap first-hauler PIVOT: FREED the command frigate %s at the inter-contract window (loop stopped + dedicated the exclusive purchasing ship) — positioning it at the home shipyard so the presence-gated hauler price reads next tick (sp-5nd2 fault-2)", obs.CommandFrigateID), map[string]interface{}{
			"action":       "bootstrap_frigate_pivot",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
	}

	if !frigateReady {
		// No frigate to free/position (e.g. a subsequent buy resting on an incidentally-idle probe that is
		// not at the yard): reuse the sp-hh0h probe-path positioner for any idle undedicated hull.
		h.ensureShipyardReadable(ctx, cmd, obs, res, priceErr)
		return
	}

	// POSITION the freed+dedicated frigate at the home shipyard (idempotent: a no-op when it is already there
	// / en route). The buy stays blocked THIS tick (fail closed) and fires next tick once the frigate arrives
	// and the live price reads.
	dispatched, serr := h.scanner.PositionPurchaserAtShipyard(ctx, cmd.PlayerID, obs.CommandFrigateID, obs.HomeSystem)
	if serr != nil {
		res.Blocker = "price_unreadable"
		logger.Log("WARN", fmt.Sprintf("Bootstrap hauler price unreadable and positioning the purchasing frigate failed — failing closed (no buy): %v", serr), map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "price_unreadable",
		})
		return
	}
	res.Blocker = "positioning_purchaser_at_shipyard"
	positionNote := fmt.Sprintf("purchasing frigate %s already at/heading to the home shipyard — awaiting a readable price", obs.CommandFrigateID)
	if dispatched {
		positionNote = fmt.Sprintf("navigated the purchasing frigate %s to the home shipyard so the presence-gated hauler price reads next tick", obs.CommandFrigateID)
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap first-hauler pivot: %s (no buy this tick — sp-5nd2 fault-2)", positionNote), map[string]interface{}{
		"action":       "bootstrap_positioning_purchaser",
		"container_id": cmd.ContainerID,
		"blocker":      "positioning_purchaser_at_shipyard",
		"ship":         obs.CommandFrigateID,
	})
}

// seedHaulerPriceFromYard price-checks the contract-hauler ship type while a hull is already at the home
// shipyard (the probe-buy moment, run_bootstrap_reconcile.go) and caches the readable price (sp-muc5x).
// It exists so the first-hauler pivot can test the capital gate BEFORE freeing the command frigate's
// earning loop — the cold-start deadlock where the frigate is freed while the hauler is unaffordable, so no
// earner remains and the treasury never grows (permanent stall). It is READ-ONLY (a price-check, never a
// buy — the money guard is untouched, RULINGS #4) and best-effort: a nil hauler acquirer or an unreadable
// hauler listing caches nothing, and the guard then treats the cache as absent and preserves the existing
// free+position behavior (this fix only ever TIGHTENS, never loosens).
func (h *RunBootstrapCoordinatorHandler) seedHaulerPriceFromYard(ctx context.Context, cmd *RunBootstrapCoordinatorCommand) {
	if h.haulAcquirer == nil {
		return
	}
	price, _, readable, err := h.haulAcquirer.PriceCheck(ctx, cmd.PlayerID, haulerShipType)
	if err != nil || !readable {
		return
	}
	h.cacheHaulerPrice(cmd.ContainerID, price)
}

// firstUnservedHub returns the highest-ranked viable hub (within the haulerTarget cap) that no
// existing hauler is placed on, or "" when all capped hubs are served. A hub is "served" when some
// hauler's Waypoint is on it (idle at, or heading to) — so a hauler bought last tick and still en
// route keeps its hub from being re-selected. The reconciler's count guard caps total buys regardless,
// so even a mis-placement (from a churned ranking) can never overshoot haulerTarget.
func firstUnservedHub(hubs []Hub, haulers []HaulerSnapshot, hubCap int) string {
	served := make(map[string]struct{}, len(haulers))
	for _, hl := range haulers {
		if hl.Waypoint != "" {
			served[hl.Waypoint] = struct{}{}
		}
	}
	limit := len(hubs)
	if limit > hubCap {
		limit = hubCap
	}
	for i := 0; i < limit; i++ {
		if _, ok := served[hubs[i].Waypoint]; !ok {
			return hubs[i].Waypoint
		}
	}
	return ""
}

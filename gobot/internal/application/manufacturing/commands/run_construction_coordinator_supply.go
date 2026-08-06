package commands

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// supplyTask advances one construction material for the claimed hauler. It runs in two phases:
//
//	PHASE 1 (deliver-on-hand): if the hull ALREADY HOLDS units of the material the site still
//	needs, unload them to the site FIRST — UNCONDITIONALLY, before and independent of the
//	source-buy gate below. Delivering cargo already aboard has zero market impact and always
//	advances the gate; it is NOT a buy, so the fail-closed buy guard does not govern it. Without
//	this, a laden hull released mid-delivery (pipeline/coordinator restart) reaches ProduceGood
//	first and parks on a dry source WITHOUT ever unloading, stranding the gate material.
//
//	PHASE 2 (source-then-deliver): source the still-outstanding remainder via the shared engine
//	(the fail-closed buy path, UNCHANGED) and deliver it. Reached for the common empty-hull drain
//	(nothing on-hand) or when the hull's on-hand load did not cover the full bill.
//
// Returns true when the tick delivered anything for this task. A genuinely-unsourceable remainder
// is deferred (parked PENDING, source cleared) rather than failed — but on-hand cargo that was
// already delivered is never stranded: such a task advances (completed; the outstanding
// remainder re-stages via replenishment).
func (h *RunConstructionCoordinatorHandler) supplyTask(ctx context.Context, cmd *RunConstructionCoordinatorCommand, systemSymbol string, lot constructionLot, playerID shared.PlayerID) bool {
	if !h.claimTaskForSupply(ctx, lot.task, lot.ship) {
		return false
	}

	// A ROLE-TAGGED gate hull takes its role's own leg rather than the shared source-then-deliver
	// path. A DELIVERY hull buys terminal-factory output and hauls it to the gate; a FACTORY hull
	// feeds INPUTS into the factories that export those materials and never touches their output.
	// The condition lives in gateLegRole because the dispatch planner declines hulls on the
	// strength of what these legs can do and must never disagree with this routing.
	//
	// A SWITCH WITH A LOUD DEFAULT, not an if/else, and the default is the point. gate.Role is an
	// int enum and Go checks no switch for exhaustiveness, so an `else` here would silently hand a
	// THIRD role to the delivery leg — while dispatchableByHold, which asks `role != RoleDelivery`,
	// silently exempted the same hull from the wedged-hull decline. Two silent, opposite readings
	// of one new role, with no compiler help and no failing test. The default converts that into a
	// line an operator cannot miss.
	//
	// It falls through to the SHARED path rather than refusing the lot: the shared path is the
	// universal fallback every unroled hull already takes, so a mis-configured role still does
	// useful work instead of stranding an EXECUTING task. Unreachable today — ParseFleetTag only
	// ever yields the two roles — and deliberately kept as the alarm for the day it is not.
	if role, ok := h.gateLegRole(lot.claimIdentity); ok {
		switch role {
		case gate.RoleFactory:
			return h.feedGateLeg(ctx, cmd, systemSymbol, lot, playerID)
		case gate.RoleDelivery:
			return h.deliverGateLeg(ctx, cmd, systemSymbol, lot, playerID)
		default:
			common.LoggerFromContext(ctx).Log("ERROR", fmt.Sprintf("Gate role %q (%s) has no leg of its own, so %s is falling back to the shared source-then-deliver path. A role added to gate.roleTags MUST get a case here AND a decision in dispatchableByHold's wedged-hull decline — neither is checked by the compiler", lot.claimIdentity, role, lot.ship.ShipSymbol()), map[string]interface{}{
				"ship": lot.ship.ShipSymbol(), "claim_identity": lot.claimIdentity, "role": role.String(),
			})
		}
	}

	leg := &supplyLeg{lot: lot, ship: lot.ship}

	if settled, advanced := h.deliverOnHandCargo(ctx, leg, playerID); settled {
		return advanced
	}

	if h.warehouse.enabled() {
		if settled, advanced := h.supplyFromWarehouse(ctx, leg, systemSymbol, playerID); settled {
			return advanced
		}
	}

	return h.sourceAndDeliverRemainder(ctx, cmd, systemSymbol, leg, playerID)
}

// supplyLeg is one hull's in-flight supply of a construction material: the lot it was dispatched
// for, the EFFECTIVE hull, the pipeline its deliveries were recorded against, and the units it has
// already delivered this tick. ship is NOT lot.ship — a warehouse withdrawal reloads the hull, and
// every step after that must address the reloaded one.
type supplyLeg struct {
	lot       constructionLot
	ship      *navigation.Ship
	pipeline  *manufacturing.ManufacturingPipeline
	delivered int
}

func (l *supplyLeg) task() *manufacturing.ManufacturingTask { return l.lot.task }

func (l *supplyLeg) ephemeral() bool { return l.lot.ephemeral }

// claimTaskForSupply binds the claimed hauler to the task and moves it to EXECUTING.
func (h *RunConstructionCoordinatorHandler) claimTaskForSupply(ctx context.Context, task *manufacturing.ManufacturingTask, ship *navigation.Ship) bool {
	logger := common.LoggerFromContext(ctx)
	if err := task.AssignShip(ship.ShipSymbol()); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not assign hauler to construction task %s: %v", task.ID(), err), nil)
		return false
	}
	if err := task.StartExecution(); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not start construction task %s: %v", task.ID(), err), nil)
		return false
	}
	return true
}

// deliverOnHandCargo unloads units of the material the hull ALREADY HOLDS, before and independent
// of the source-buy gate: cargo already aboard has zero market impact and always advances the gate,
// so the fail-closed buy guard does not govern it. Without this a laden hull released mid-delivery
// by a restart reaches ProduceGood first and parks on a dry source, stranding the material.
//
// settled reports that the task reached a terminal answer this tick (returned in advanced);
// otherwise the caller sources the still-outstanding remainder.
func (h *RunConstructionCoordinatorHandler) deliverOnHandCargo(
	ctx context.Context,
	leg *supplyLeg,
	playerID shared.PlayerID,
) (settled, advanced bool) {
	task, ship := leg.task(), leg.ship
	if onHandUnits(ship, task.Good()) == 0 || h.remainingBill(ctx, task) <= 0 {
		return false, false
	}

	delivered, err := h.producer.DeliverToConstructionSite(ctx, ship.ShipSymbol(), task.Good(), task.ConstructionSite(), playerID)
	if err != nil {
		// A 4219 'ship has 0 units' means the on-hand cargo was PHANTOM (the cache was never
		// decremented after an earlier supply): resync the hull + defer, never fail/loop.
		if isPhantomCargoSupplyError(err) {
			return true, h.handlePhantomCargo(ctx, leg, playerID)
		}
		if !leg.ephemeral() {
			h.failTask(ctx, task, fmt.Sprintf("delivering on-hand %s to %s failed: %v", task.Good(), task.ConstructionSite(), err))
		}
		return true, false
	}
	if delivered == 0 {
		return false, false
	}

	leg.pipeline = h.recordDelivery(ctx, task, delivered)
	leg.delivered = delivered
	// The on-hand load met the site's remaining bill: complete now — never buy past demand.
	if h.remainingForGoodLocked(leg.pipeline, task.Good()) <= 0 {
		return true, h.completeSupply(ctx, leg, delivered)
	}
	return false, false
}

// supplyFromWarehouse WITHDRAWS the material from an in-system depot at ZERO cost before any market
// buy, so the depot's stocker is the sole buyer and the construction buy is only ever the residual
// (RULINGS #4, no double-buy). A withdrawal trip delivers what it drew and re-stages the remainder,
// so a covered unit is never also bought. The withdrawal cap mirrors the fan-out slice so
// concurrent lots cannot over-draw a material's remaining requirement.
//
// settled reports that the task reached a terminal answer this tick (returned in advanced).
func (h *RunConstructionCoordinatorHandler) supplyFromWarehouse(
	ctx context.Context,
	leg *supplyLeg,
	systemSymbol string,
	playerID shared.PlayerID,
) (settled, advanced bool) {
	logger := common.LoggerFromContext(ctx)
	task, ship := leg.task(), leg.ship

	need := leg.lot.cappedNeed(h.remainingBill(ctx, task))
	withdrew, reloaded, werr := h.trySourceFromWarehouse(ctx, task, ship, systemSymbol, playerID, need)
	if werr != nil {
		// Fail-open: a warehouse hiccup never starves the gate — log and fall through to the buy.
		logger.Log("WARNING", fmt.Sprintf("Construction warehouse-first sourcing of %s failed; falling back to market buy: %v", task.Good(), werr), map[string]interface{}{
			"ship": ship.ShipSymbol(), "good": task.Good(), "task": task.ID(),
		})
	}
	if withdrew == 0 {
		return false, false
	}

	leg.ship = reloaded
	delivered, err := h.producer.DeliverToConstructionSite(ctx, leg.ship.ShipSymbol(), task.Good(), task.ConstructionSite(), playerID)
	if err != nil {
		if isPhantomCargoSupplyError(err) {
			return true, h.handlePhantomCargo(ctx, leg, playerID)
		}
		return true, h.completeOrFail(ctx, leg, fmt.Sprintf("delivering withdrawn %s to %s failed: %v", task.Good(), task.ConstructionSite(), err))
	}
	leg.pipeline = h.recordDelivery(ctx, task, delivered)
	return true, h.completeSupply(ctx, leg, leg.delivered+delivered)
}

// sourceAndDeliverRemainder buys the still-outstanding remainder on the shared engine and delivers
// it. A genuinely-unsourceable remainder is DEFERRED (parked PENDING, source cleared) rather than
// failed, but on-hand cargo already delivered this tick is never stranded: such a task completes
// and replenishment re-stages what is left.
func (h *RunConstructionCoordinatorHandler) sourceAndDeliverRemainder(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	systemSymbol string,
	leg *supplyLeg,
	playerID shared.PlayerID,
) bool {
	task, ship := leg.task(), leg.ship

	// AN UNLOAD-ONLY LOT NEVER REACHES THE BUY. Its material has nothing left to purchase — every
	// outstanding unit is already paid for and in some hull's hold — so the lot exists only to
	// unload what it carries, which PHASE 1 has already done. Falling through would hand a zero
	// fill target to the executor, and a zero target means "fill to full capacity".
	if !leg.lot.mayBuy() {
		common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Construction drain: %s is not buying %s this trip — every outstanding unit is already paid for and in a hull's hold, so this lot only unloads what it carries", ship.ShipSymbol(), task.Good()), map[string]interface{}{
			"ship": ship.ShipSymbol(), "good": task.Good(), "task": task.ID(), "action": "skip_inflight_covered",
		})
		return h.completeOrDefer(ctx, leg)
	}

	ctx = mfgServices.WithHullFillTarget(ctx, h.hullFillTarget(ctx, leg.lot), 0)

	// Honor the planner's already-made buy-vs-produce decision recorded on the task: a direct BUY
	// of the final good, or a FABRICATION driven as an AcquisitionFabricate node so the engine buys
	// the inputs, feeds the factory, and harvests the output into the hauler — a buy-only drain
	// would explode the market bid and cannot fill the gate at scale.
	node := h.constructionSourcingNode(ctx, task, systemSymbol, cmd.PlayerID)

	result, err := h.producer.ProduceGood(h.gateSupplyContext(ctx, task), ship, node, systemSymbol, cmd.PlayerID, h.operationContext(cmd), false)
	if err != nil {
		return h.completeOrFail(ctx, leg, fmt.Sprintf("sourcing %s failed: %v", task.Good(), err))
	}
	if result == nil || result.QuantityAcquired == 0 {
		// Dry / no eligible source: the fail-closed park. A fan-out CLONE never defers — its
		// material's original ready task, dispatched alongside, owns the defer — so it just
		// abandons its empty trip.
		return h.completeOrDefer(ctx, leg)
	}

	delivered, err := h.producer.DeliverToConstructionSite(ctx, ship.ShipSymbol(), task.Good(), task.ConstructionSite(), playerID)
	if err != nil {
		// A 4219 on the sourced load is the same phantom-cargo signal: resync + recover without failing.
		// On-hand progress already recorded this tick is preserved by handlePhantomCargo (never stranded).
		if isPhantomCargoSupplyError(err) {
			return h.handlePhantomCargo(ctx, leg, playerID)
		}
		return h.completeOrFail(ctx, leg, fmt.Sprintf("delivering %s to %s failed: %v", task.Good(), task.ConstructionSite(), err))
	}

	leg.pipeline = h.recordDelivery(ctx, task, delivered)
	return h.completeSupply(ctx, leg, leg.delivered+delivered)
}

// completeOrFail resolves a leg that could not finish. On-hand cargo already delivered this tick is
// NEVER stranded: the task advanced the gate, so it completes and replenishment re-stages the
// remainder. Only a leg that delivered nothing fails.
func (h *RunConstructionCoordinatorHandler) completeOrFail(ctx context.Context, leg *supplyLeg, reason string) bool {
	if leg.delivered > 0 {
		return h.completeSupply(ctx, leg, leg.delivered)
	}
	if !leg.ephemeral() {
		h.failTask(ctx, leg.task(), reason)
	}
	return false
}

// completeOrDefer is completeOrFail for a recoverable condition: an empty-of-the-material hull
// parks PENDING for the SupplyMonitor to re-source rather than failing.
func (h *RunConstructionCoordinatorHandler) completeOrDefer(ctx context.Context, leg *supplyLeg) bool {
	if leg.delivered > 0 {
		return h.completeSupply(ctx, leg, leg.delivered)
	}
	if !leg.ephemeral() {
		h.deferTask(ctx, leg.task())
	}
	return false
}

// hullFillTarget sizes how far to fill the hauler before delivering: the material's outstanding
// bill, so a round-trip carries ~a hull rather than one ~trade-volume tranche. A 0 bill
// (pipeline/material unreadable) leaves the executor to fill to full capacity — a supply is never
// harmful. One of SEVERAL lots fanned onto the same material is capped to its hull-load SLICE so
// the concurrent lots together never buy past the remaining requirement; a lot with no
// authoritative cap fills toward the whole bill.
func (h *RunConstructionCoordinatorHandler) hullFillTarget(ctx context.Context, lot constructionLot) int {
	return lot.cappedNeed(h.remainingBill(ctx, lot.task))
}

// gateSupplyContext marks the run as construction supply carrying the gate waypoint, so the
// engine's RESALE-margin guards (chain-margin, crushed-sink) are scoped out through the whole tree:
// the harvested output is delivered to the gate, never resold. INPUT buys still pass the full
// money-guard stack.
//
// The construction-site DeliveryTarget stamped here is what makes IsUnifiedGateNode true for every
// node in the recursive tree below — this is the ONLY place gate mode is entered.
func (h *RunConstructionCoordinatorHandler) gateSupplyContext(ctx context.Context, task *manufacturing.ManufacturingTask) context.Context {
	produceCtx := shared.WithConstructionSupply(ctx)
	return mfgServices.WithDeliveryTarget(produceCtx, mfgServices.ConstructionSiteTarget(task.ConstructionSite()))
}

// supplyTaskBounded runs supplyTask under a per-task deadline so a single wedged task can NEVER hold
// its worker — and the slot that worker holds under max_workers — indefinitely. The task body runs on
// a child goroutine over a timeout ctx; the worker is reclaimed the instant the task finishes OR the
// deadline elapses, whichever comes first, so a slot always comes back. This is the hard safety net:
// even a downstream op that ignores ctx entirely cannot starve the drain of dispatch capacity
// — at worst its goroutine unwinds later while the drain keeps ticking, and because
// taskCtx is cancelled the money paths abort rather than spend. done is buffered so a late finish
// never blocks a possibly-orphaned child. Per-step enter/exit logging makes a slow/wedged task
// diagnosable rather than an undiagnosable hang.
func (h *RunConstructionCoordinatorHandler) supplyTaskBounded(ctx context.Context, cmd *RunConstructionCoordinatorCommand, systemSymbol string, lot constructionLot, playerID shared.PlayerID, timeout time.Duration) bool {
	logger := common.LoggerFromContext(ctx)
	task := lot.task
	ship := lot.ship
	taskCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logger.Log("INFO", fmt.Sprintf("Construction drain: START supply of %s via %s -> %s (timeout %s)", task.Good(), ship.ShipSymbol(), task.ConstructionSite(), timeout), map[string]interface{}{
		"ship": ship.ShipSymbol(), "good": task.Good(), "construction_site": task.ConstructionSite(), "task": task.ID(),
	})

	done := make(chan bool, 1)
	go func() {
		// The task body runs on its own goroutine, where a panic reaches no caller: unrecovered it
		// takes the daemon down, and even survived it would leave the worker on the select below until
		// the deadline with the hull idle in its hold. Report it as a supply that drained nothing.
		defer func() {
			failure := recover()
			if failure == nil {
				return
			}
			logger.Log("ERROR", fmt.Sprintf("Construction drain: supply of %s via %s panicked: %v\n%s", task.Good(), ship.ShipSymbol(), failure, debug.Stack()), map[string]interface{}{
				"ship": ship.ShipSymbol(), "good": task.Good(), "construction_site": task.ConstructionSite(), "task": task.ID(),
			})
			// Record it as the task failure it is, exactly as a sourcing or delivery error is
			// recorded, so the retry sweep re-stages the leg. Left EXECUTING it has nothing running
			// behind it and nothing that will ever pick it up again.
			if !lot.ephemeral {
				h.failTask(taskCtx, task, fmt.Sprintf("supplying %s panicked: %v", task.Good(), failure))
			}
			done <- false
		}()
		done <- h.supplyTask(taskCtx, cmd, systemSymbol, lot, playerID)
	}()

	select {
	case drained := <-done:
		logger.Log("INFO", fmt.Sprintf("Construction drain: END supply of %s via %s (drained=%t)", task.Good(), ship.ShipSymbol(), drained), map[string]interface{}{
			"ship": ship.ShipSymbol(), "good": task.Good(), "task": task.ID(), "drained": drained,
		})
		return drained
	case <-taskCtx.Done():
		// taskCtx derives from the tick's ctx, so a daemon stop lands here too — as CANCELLED, not
		// as the deadline. Reporting a deploy as "exceeded <timeout>" sends the next investigator
		// hunting a slow haul that never happened. The interrupted worker re-queues its own task
		// (requeueInterrupted) instead of failing it.
		if errors.Is(taskCtx.Err(), context.Canceled) {
			logger.Log("WARNING", fmt.Sprintf("Construction drain: supply of %s via %s INTERRUPTED by a stop — abandoning this tick; the task is re-queued for the next activation with no retry spent and the hull keeps its load: %v", task.Good(), ship.ShipSymbol(), taskCtx.Err()), map[string]interface{}{
				"ship": ship.ShipSymbol(), "good": task.Good(), "construction_site": task.ConstructionSite(), "task": task.ID(),
			})
			return false
		}
		logger.Log("ERROR", fmt.Sprintf("Construction drain: supply of %s via %s exceeded %s — ABANDONING this tick (hull released, task retried next tick; the coordinator keeps ticking, never hangs): %v", task.Good(), ship.ShipSymbol(), timeout, taskCtx.Err()), map[string]interface{}{
			"ship": ship.ShipSymbol(), "good": task.Good(), "construction_site": task.ConstructionSite(), "task": task.ID(), "timeout": timeout.String(),
		})
		return false
	}
}

// completeSupply finishes a task that delivered a load this tick: complete + persist it, enqueue the
// next single-load replenishment when the site's bill is not yet met, and log the supply. Returns
// true (the task drained). Shared by the deliver-on-hand path and the source-then-deliver path so
// the completion/refill logic cannot drift.
func (h *RunConstructionCoordinatorHandler) completeSupply(ctx context.Context, leg *supplyLeg, delivered int) bool {
	logger := common.LoggerFromContext(ctx)
	task, pipeline, ship, ephemeral := leg.task(), leg.pipeline, leg.ship, leg.ephemeral()
	// A fan-out CLONE has ALREADY done the real work — sourced, delivered, and recorded the
	// pipeline progress (recordDelivery, above) — so it must NOT persist a task status or enqueue a
	// replenishment: the material's ORIGINAL ready task (dispatched alongside every clone) owns the
	// task lifecycle and the single-per-material re-staging. Skipping both keeps the ready queue at the
	// planner's one-task-per-material and lets the fan-out re-derive parallelism from live hulls each
	// tick, with no clone-created task rows to orphan.
	if !ephemeral {
		if err := task.Complete(); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Could not complete construction task %s: %v", task.ID(), err), nil)
		}
		if err := h.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Could not persist completed construction task %s: %v", task.ID(), err), nil)
		}
		// One supplyTask delivers a single hauler load. If the site's bill for this material is not yet
		// met, enqueue the next delivery so the gate keeps filling without a manual re-plan — the drain
		// self-re-stages until each material's full bill is met, instead of stalling on the planner's
		// single per-material task.
		h.enqueueReplenishmentIfNeeded(ctx, task, pipeline)
	}
	logger.Log("INFO", fmt.Sprintf("Supplied %d %s to construction site %s", delivered, task.Good(), task.ConstructionSite()), map[string]interface{}{
		"good": task.Good(), "units": delivered, "construction_site": task.ConstructionSite(), "ship": ship.ShipSymbol(), "ephemeral": ephemeral,
	})
	return true
}

// onHandUnits reports how many units of good the ship currently holds (0 if the hold is empty or
// unset) — the nil-safe read the deliver-on-hand path uses to decide whether the claimed hull is
// already laden with the construction material.
func onHandUnits(ship *navigation.Ship, good string) int {
	cargo := ship.Cargo()
	if cargo == nil {
		return 0
	}
	return cargo.GetItemUnits(good)
}

// isPhantomCargoSupplyError reports whether err is the API's 4219 'ship cargo does not contain N
// units / ship has 0 units' — the signal that the hull does NOT actually hold the cargo the drain
// routed it to deliver (a PHANTOM left by a cache not written back after an earlier supply). It is
// NOT a site/bill failure and must NOT be treated as a generic delivery error: retrying re-routes the
// empty hull to re-deliver forever and, when the hull is already at the gate, wedges the drain.
// Matched on the API error-code substring, consistent with isTransientDockStateError's 4214/4244
// matching — the raw response body ({"error":{"code":4219,...}}) is wrapped through verbatim.
func isPhantomCargoSupplyError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "4219")
}

// handlePhantomCargo recovers from a 4219 phantom-cargo supply rejection instead of failing or
// re-looping. The hull did not actually hold the cargo the drain routed it to deliver, so: (1)
// RESYNC the hull from the server, clearing the desynced cache (reconciling even a phantom that
// arose some other way); then (2) advance without failing: if on-hand progress was already recorded
// this tick, complete it (never strand delivered units); otherwise DEFER the task for re-sourcing, so
// the NEXT tick sources into a hull with REAL cargo rather than re-routing this empty one to
// re-deliver. Returns whether the task counts as drained this tick.
func (h *RunConstructionCoordinatorHandler) handlePhantomCargo(ctx context.Context, leg *supplyLeg, playerID shared.PlayerID) bool {
	logger := common.LoggerFromContext(ctx)
	task, ship := leg.task(), leg.ship
	logger.Log("WARNING", fmt.Sprintf("Construction drain: hauler %s supply of %s rejected as PHANTOM cargo (API 4219, ship has 0 units) — resyncing hull and recovering the task (never re-routing/hanging) [sp-6zkg/sp-j09q]", ship.ShipSymbol(), task.Good()), map[string]interface{}{
		"ship": ship.ShipSymbol(), "good": task.Good(), "construction_site": task.ConstructionSite(), "task": task.ID(),
	})
	h.resyncShipCargo(ctx, ship.ShipSymbol(), playerID)
	if leg.delivered > 0 {
		// On-hand units already delivered + recorded this tick — complete (never strand them).
		return h.completeSupply(ctx, leg, leg.delivered)
	}
	// A fan-out CLONE never defers (its material's original ready task owns the defer); only a real task
	// parks for re-sourcing.
	if !leg.ephemeral() {
		h.deferTask(ctx, task)
	}
	return false
}

// resyncShipCargo forces the hull's cached state back to server truth after a phantom-cargo 4219.
// SyncShipFromAPI is the daemon's own GET-and-write-through reconcile — the same path a boot sync and
// `ship refresh` run — so this stays within the single-writer model (the daemon reconciling its OWN
// cache, not a CLI writer). Best-effort: a resync failure is logged, not fatal — the next tick
// re-polls and keeps the cache coherent on the happy path.
func (h *RunConstructionCoordinatorHandler) resyncShipCargo(ctx context.Context, shipSymbol string, playerID shared.PlayerID) {
	logger := common.LoggerFromContext(ctx)
	if _, err := h.shipRepo.SyncShipFromAPI(ctx, shipSymbol, playerID); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Construction drain: could not resync hull %s after phantom-cargo 4219 (cache reconciles on next sync): %v", shipSymbol, err), nil)
	}
}

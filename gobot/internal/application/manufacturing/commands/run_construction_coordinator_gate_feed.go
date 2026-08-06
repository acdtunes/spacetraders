package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// feedGateLeg runs one FACTORY hull's leg: flush, plan, resolve, buy, feed.
//
// ONE STEP PER LEG, and that is the bound on factory-fleet spend. There is no bill for an INPUT —
// the construction site's bill is denominated in gate materials, not in IRON_ORE — so nothing
// downstream caps how much feedstock a hull could buy. What caps it is: one hull-load per leg
// (BuyAtTerminalFactory stamps the trip allocation as the fill target, bounded by hull capacity),
// the market's per-transaction trade_volume, and the working-capital floor re-checked EVERY
// tranche and failing closed. Nothing here reads, moves or weakens a floor (RULINGS #4).
//
// EVERY exit funnels through the SAME completion machinery the rest of supplyTask uses. A leg
// that simply returns leaves its task EXECUTING forever: nothing re-stages the next load, the
// ready queue drains to nothing, and the drain goes quiet while still reporting RUNNING — a stall
// indistinguishable from a finished gate.
//
// EVERY EXIT ALSO LOGS, including the unwired one. This leg emits no metric — the drain has no
// metrics seam at all, its whole observability surface is the container log — so the log line IS
// the counter, and a path that leaves none makes "the leg was never invoked" and "the leg ran and
// stood every hull down" the same observation.
//
// Reports whether the leg advanced this task, which for the factory role means only the flush:
// feeding a factory supplies no units to the construction site.
func (h *RunConstructionCoordinatorHandler) feedGateLeg(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	systemSymbol string,
	lot constructionLot,
	playerID shared.PlayerID,
) bool {
	logger := common.LoggerFromContext(ctx)
	task := lot.task

	if !h.factory.enabled() {
		// Unwired: park rather than nil-panic. DEFENSIVE — this is NOT a live production path, and
		// the reverse claim (that it is the only reachable one) was true only while the leg had no
		// caller at all.
		//
		// The sole production caller is supplyTask, and it routes through gateLegRole, which gates
		// the FACTORY role on this SAME h.factory.enabled(). So an unwired handler never sends a lot
		// here: a factory-tagged hull falls through to the shared fabricate path and recovers there,
		// which is exactly what keeps the dispatch planner's decline honest (it must never be made
		// about a leg that is not going to run). The only things that reach this branch today are
		// the two tests that call the leg directly.
		//
		// It is kept, and it PARKS and LOGS rather than returning bare, because any caller that
		// does reach it is by definition one that forgot that check — and a bare return leaves the
		// task EXECUTING forever: nothing re-stages the next load, the ready queue drains to
		// nothing, and the drain reports RUNNING while doing nothing, with no line saying why.
		logger.Log("WARNING", fmt.Sprintf("Gate factory: %s was routed to the feeding leg but the factory collaborators are not wired — parking its task and feeding nothing", lot.ship.ShipSymbol()), map[string]interface{}{
			"ship": lot.ship.ShipSymbol(), "action": "factory_unwired",
		})
		return h.completeOrDefer(ctx, &supplyLeg{lot: lot, ship: lot.ship})
	}

	pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID())
	if err != nil || pipeline == nil {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: cannot read pipeline %s for %s — standing down this leg rather than feeding against an unknown bill: %v", task.PipelineID(), task.Good(), err), nil)
		return h.completeOrDefer(ctx, &supplyLeg{lot: lot, ship: lot.ship})
	}
	leg := &supplyLeg{lot: lot, ship: lot.ship, pipeline: pipeline}

	// FLUSH FIRST. A factory hull can hold a GATE MATERIAL — re-roled between ticks, or a restart
	// mid-leg. A terminal factory will not buy its own export (marketBuys refuses an EXPORT
	// listing), so that cargo would ride forever and the hold would never free. Unload it at the
	// SITE through the same path the delivery leg uses; cargo already aboard has zero market
	// impact and always advances the gate.
	freed := h.flushOnHandGateMaterials(ctx, leg, pipeline, playerID)

	billSource := pipeline
	if leg.pipeline != nil {
		billSource = leg.pipeline
	}

	// Size the buy against the free hold, including whatever the flush just released. The cached
	// *Ship is deliberately not updated by DeliverToConstructionSite (it writes the emptied hold
	// back through the repository), so the freed units are added explicitly rather than re-read.
	capacity := totalUnits(freed)
	if cargo := lot.ship.Cargo(); cargo != nil {
		capacity += cargo.Capacity - cargo.Units
	}

	// NO ROOM TO BUY MEANS DELIVER WHAT YOU ALREADY HAVE.
	//
	// THIS LEG BUYS BEFORE IT FEEDS, so a zero-capacity short-circuit here does not merely skip a
	// purchase — it skips FeedFactory too, and FeedFactory is the ONLY thing that empties a factory
	// hull. The state is not hypothetical and this leg mints it: the buy is sized to the WHOLE free
	// hold, so a successful buy followed by a failed or refused feed leaves the hull EXACTLY full
	// (both branches below say so — "the cargo stays aboard for the next leg"). From then on every
	// subsequent leg re-entered here and parked, with nothing in the system to unload it: the hull
	// was out of the fleet until a human intervened.
	//
	// It is checked BEFORE planGateFeed rather than after, and that order is load-bearing.
	// planGateFeed refuses any step whose INPUT SOURCE does not resolve, which is right for a leg
	// about to buy and wrong for one that is not: an era where nothing exports the good aboard is
	// exactly when the hull is stuck, and inheriting that requirement would rebuild the wedge under
	// a new name. Planning first would also park on !planned before the hold was ever considered.
	if capacity <= 0 {
		return h.feedGateLegFromHold(ctx, cmd, systemSymbol, leg, billSource)
	}

	// The SAME capacity the buy is sized with is what the plan prices its affordability against
	// (sp-9eor3). Passing the hold rather than letting the planner guess is what keeps the
	// prediction and the purchase talking about one quantity: a planner sizing against some other
	// number would decline steps the buy would have afforded, or admit ones it would not.
	step, input, target, planned := h.planGateFeed(ctx, cmd, systemSymbol, billSource, capacity)
	if !planned {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: %s found no feedable step this leg — every gate material is either satisfied, already ABUNDANT at its factory, has no resolvable source and destination, or costs more than the working-capital reserve allows", lot.ship.ShipSymbol()), map[string]interface{}{
			"ship": lot.ship.ShipSymbol(), "action": "no_feed_step",
		})
		return h.completeOrDefer(ctx, leg)
	}

	// THE SAME pinned buy the delivery fleet uses, with every money guard unchanged.
	result, berr := h.factory.buyer.BuyAtTerminalFactory(ctx, lot.ship, step.Input, input, capacity, systemSymbol, cmd.PlayerID, h.operationContext(cmd))
	if berr != nil {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: buying %s at %s for the %s factory failed: %v", step.Input, input.WaypointSymbol, step.Target, berr), map[string]interface{}{
			"good": step.Input, "target": step.Target, "source": input.WaypointSymbol,
		})
		return h.completeOrDefer(ctx, leg)
	}
	if result == nil || result.QuantityAcquired == 0 {
		// The money or price guards stopped the fill. Fail closed: do NOT fly an empty hull to a
		// factory. Recorded loudly — a refused buy and an idle hull must not look the same.
		logger.Log("WARNING", fmt.Sprintf("Gate factory: acquired nothing of %s at %s — the spend guards stopped the fill, so %s feeds no factory this leg", step.Input, input.WaypointSymbol, lot.ship.ShipSymbol()), map[string]interface{}{
			"good": step.Input, "source": input.WaypointSymbol, "ship": lot.ship.ShipSymbol(), "action": "buy_acquired_nothing",
		})
		return h.completeOrDefer(ctx, leg)
	}

	// THE INPUT LIST IS WHAT THIS LEG BOUGHT FOR THIS FACTORY — step.Input alone — and NOT the
	// hull's hold. The two are different subjects and both are load-bearing:
	//
	//   - The list is the sp-b27a2 guard's subject. ValidateFeedDestination refuses the NAVIGATE
	//     unless the destination imports EVERY good named, so naming unrelated cargo the hull
	//     merely happens to carry would refuse a trip that would have fed the factory correctly.
	//     That also reverses sp-w2qg5, whose ruling is that unsellable cargo aboard RIDES ON
	//     rather than vetoing the trip. This mirrors the fabricate path exactly, where the subject
	//     is run.haulingInputs() — "what this run acquired for the factory" — never the hold.
	//
	//   - deliverInputs, on arrival, offers the WHOLE HOLD, filtered good-by-good against the
	//     destination's own listing. So a factory can receive more than is named here, but only
	//     goods it actually imports; its own EXPORT is refused (which is why the flush above is
	//     not optional — a gate material would otherwise ride forever), and so is anything it does
	//     not list. Pinned in
	//     services.TestFeedFactory_OffersTheWholeHoldButTheDestinationsListingDecidesWhatLands.
	fed, ferr := h.factory.feeder.FeedFactory(ctx, lot.ship, target, []string{step.Input}, cmd.PlayerID, h.operationContext(cmd))
	if ferr != nil {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: feeding %d %s into the %s factory at %s failed; the cargo stays aboard for the next leg: %v", result.QuantityAcquired, step.Input, step.Target, target.WaypointSymbol, ferr), map[string]interface{}{
			"good": step.Input, "target": step.Target, "factory": target.WaypointSymbol,
		})
		return h.completeOrDefer(ctx, leg)
	}
	if fed != nil && fed.Refused {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: %s refused %s — %s keeps the cargo aboard rather than stranding it there (sp-b27a2)", target.WaypointSymbol, step.Input, lot.ship.ShipSymbol()), map[string]interface{}{
			"good": step.Input, "factory": target.WaypointSymbol, "action": "feed_refused",
		})
		return h.completeOrDefer(ctx, leg)
	}

	units := 0
	if fed != nil {
		units = fed.UnitsDelivered
	}
	logger.Log("INFO", fmt.Sprintf("Gate factory: %s fed %d %s into the %s factory at %s — the delivery fleet buys that factory's OUTPUT, never its inputs", lot.ship.ShipSymbol(), units, step.Input, step.Target, target.WaypointSymbol), map[string]interface{}{
		"ship": lot.ship.ShipSymbol(), "good": step.Input, "units": units,
		"target": step.Target, "factory": target.WaypointSymbol, "depth": step.Depth,
	})

	// The feed supplied no units to the CONSTRUCTION SITE, so leg.delivered reflects the flush
	// alone. completeOrDefer honours it: a leg that flushed COMPLETES, a leg that only fed PARKS
	// for the SupplyMonitor to re-activate. Parking is not a failure — it must never spend a
	// retry against a task the factory role was never going to close.
	if leg.delivered > 0 {
		return h.completeSupply(ctx, leg, leg.delivered)
	}
	return h.completeOrDefer(ctx, leg)
}

// feedGateLegFromHold runs the leg for a hull with NO FREE HOLD: it delivers what is already
// aboard instead of standing the hull down.
//
// IT ISSUES NO PURCHASE, and that is a property of the path, not a happy accident of the current
// call graph. Every unit it moves is already owned and already paid for, so there is no floor to
// read, no tranche to size and no treasury to reserve — this adds no second spend primitive and
// touches none of the existing one (RULINGS #4). The only reason the hull is here at all is that
// it has nowhere to put a purchase.
//
// EVERY EXIT FUNNELS THROUGH completeOrDefer, like every other exit in this file. A leg that
// simply returned would leave its task EXECUTING forever: nothing re-stages the next load, the
// ready queue drains to nothing, and the drain reports RUNNING while doing nothing.
//
// It reports no drain. Feeding a factory supplies no units to the CONSTRUCTION SITE, and on this
// path the flush necessarily moved nothing either — capacity is freed plus free hold, both
// non-negative, so a non-positive capacity means the flush freed zero.
func (h *RunConstructionCoordinatorHandler) feedGateLegFromHold(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	systemSymbol string,
	leg *supplyLeg,
	pipeline *manufacturing.ManufacturingPipeline,
) bool {
	logger := common.LoggerFromContext(ctx)
	ship := leg.ship

	step, target, found := h.planGateFeedFromHold(ctx, cmd, systemSymbol, pipeline, ship)
	if !found {
		// THE HONEST PARK: nothing aboard is an input of any factory in the chain, so there is
		// genuinely nowhere to put it, and dispatching a hull to a factory that will refuse its
		// cargo is precisely the sp-b27a2 incident. The HOLD IS NAMED because this park and an idle
		// tick are otherwise the same silence, and the cargo is the only clue to why the hull is
		// stuck.
		logger.Log("WARNING", fmt.Sprintf("Gate factory: %s has a FULL hold (%s) and no factory in this chain imports any of it — parking rather than dispatching cargo a factory would refuse (sp-b27a2)", ship.ShipSymbol(), describeHold(ship)), map[string]interface{}{
			"ship": ship.ShipSymbol(), "hold": describeHold(ship), "action": "full_hold_unfeedable",
		})
		return h.completeOrDefer(ctx, leg)
	}

	// The input list names the good ABOARD that this trip is for — the same subject contract the
	// buy path keeps, for the same reason. ValidateFeedDestination refuses the NAVIGATE unless the
	// destination imports EVERY good named, so naming the whole hold would refuse the very trip
	// that empties it (sp-w2qg5: unsellable cargo aboard rides on, it does not veto the trip).
	// deliverInputs still offers the WHOLE HOLD on arrival, filtered good-by-good against the
	// destination's own listing, so a hull carrying several feedable goods may empty more than one.
	aboard := onHandUnits(ship, step.Input)
	fed, ferr := h.factory.feeder.FeedFactory(ctx, ship, target, []string{step.Input}, cmd.PlayerID, h.operationContext(cmd))
	if ferr != nil {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: unloading the %d %s aboard %s into the %s factory at %s failed; the cargo stays aboard and the next leg retries: %v", aboard, step.Input, ship.ShipSymbol(), step.Target, target.WaypointSymbol, ferr), map[string]interface{}{
			"good": step.Input, "target": step.Target, "factory": target.WaypointSymbol,
			"ship": ship.ShipSymbol(), "action": "full_hold_feed_failed",
		})
		return h.completeOrDefer(ctx, leg)
	}
	if fed != nil && fed.Refused {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: %s refused the %d %s aboard %s — keeping the cargo rather than stranding it there (sp-b27a2)", target.WaypointSymbol, aboard, step.Input, ship.ShipSymbol()), map[string]interface{}{
			"good": step.Input, "factory": target.WaypointSymbol,
			"ship": ship.ShipSymbol(), "action": "full_hold_feed_refused",
		})
		return h.completeOrDefer(ctx, leg)
	}

	units := 0
	if fed != nil {
		units = fed.UnitsDelivered
	}

	// ZERO UNITS IS NOT A RECOVERY, and it must not be logged as one. The trip happened and the
	// destination refused nothing, yet the market took nothing: the hull is STILL FULL and will
	// re-enter this same path next leg. The likeliest cause is the sp-kdsrh fail-closed withhold —
	// an arrival listing that would not read offers the whole hold nothing — and a partial fill or
	// an exhausted trade volume land here too.
	//
	// This leg emits no metric; the log line IS the counter. Reporting "freed the hull" for a hull
	// that is still wedged would make the recovery and the wedge the SAME observation, which is the
	// exact blindness this operation is being rebuilt to remove. Separate tag, separate level.
	if units == 0 {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: %s had no free hold and reached the %s factory at %s, but the market took ZERO %s — the hull is STILL FULL and re-enters this path next leg; nothing was freed", ship.ShipSymbol(), step.Target, target.WaypointSymbol, step.Input), map[string]interface{}{
			"ship": ship.ShipSymbol(), "good": step.Input, "units": 0,
			"target": step.Target, "factory": target.WaypointSymbol, "depth": step.Depth,
			"action": "full_hold_fed_nothing",
		})
		return h.completeOrDefer(ctx, leg)
	}

	logger.Log("INFO", fmt.Sprintf("Gate factory: %s had no free hold, so it fed the %d %s already aboard into the %s factory at %s — freeing the hull rather than parking it", ship.ShipSymbol(), units, step.Input, step.Target, target.WaypointSymbol), map[string]interface{}{
		"ship": ship.ShipSymbol(), "good": step.Input, "units": units,
		"target": step.Target, "factory": target.WaypointSymbol, "depth": step.Depth,
		"action": "full_hold_fed",
	})
	return h.completeOrDefer(ctx, leg)
}

// planGateFeedFromHold picks the step this leg will feed OFF THE HULL'S OWN HOLD.
//
// It differs from planGateFeed in exactly two ways, and both are the point:
//
//   - IT SELECTS ON WHAT IS ABOARD, not on which gate material is neediest. Picking the neediest
//     material's step and hoping the hold matches is how a "fixed" leg flies to a factory that
//     imports nothing it carries, lands zero units, and parks politely instead of parking loudly —
//     the same wedge wearing a better log line. Selecting on the hold is what makes the named
//     input one the destination genuinely imports, so the sp-b27a2 guard passes and the cargo
//     actually lands.
//
//   - IT DOES NOT RESOLVE A SOURCE. planGateFeed must, because it is about to buy. This path buys
//     nothing, and an era where nothing exports the good aboard is exactly the era in which a hull
//     full of it is stuck — refusing there would rebuild the wedge.
//
// The ABUNDANT fail-safe ORDERS this choice but does not VETO it. It exists to stop a BUY into a
// full warehouse ("buying into a full warehouse burns treasury for nothing"), and no purchase
// happens here: honouring it as a veto would strand the hull permanently in exchange for no saving
// at all. So a starved factory is preferred, and an abundant one is still accepted rather than
// leaving the hull wedged.
func (h *RunConstructionCoordinatorHandler) planGateFeedFromHold(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	systemSymbol string,
	pipeline *manufacturing.ManufacturingPipeline,
	ship *navigation.Ship,
) (gate.FeedStep, *mfgServices.MarketLocatorResult, bool) {
	logger := common.LoggerFromContext(ctx)

	var abundantStep gate.FeedStep
	var abundantTarget *mfgServices.MarketLocatorResult
	haveAbundant := false

	for _, material := range gateMaterialsNeediestFirst(pipeline) {
		plan := gate.PlanFeed(material.TradeSymbol(), h.factory.topology, gate.DefaultFeedDepthCap)
		logger.Log("INFO", plan.LogLine(), map[string]interface{}{
			"root": plan.Root, "steps": len(plan.Steps), "stops": len(plan.Stops),
		})

		for _, step := range plan.Steps {
			// The hold is consulted BEFORE the market lookup, so a step this hull cannot serve
			// never prices a factory it is not going to visit.
			if onHandUnits(ship, step.Input) <= 0 {
				continue
			}
			target, terr := h.factory.topology.TerminalFactory(ctx, step.Target, systemSymbol, cmd.PlayerID)
			if terr != nil || target == nil {
				logger.Log("WARNING", fmt.Sprintf("Gate factory: %s is carrying %s but no factory in %s exports %s this era, so it cannot be unloaded there: %v", ship.ShipSymbol(), step.Input, systemSymbol, step.Target, terr), map[string]interface{}{
					"good": step.Input, "target": step.Target, "reason": "no_destination_factory",
				})
				continue
			}
			if shared.ParseSupplyLevel(target.Supply) == shared.SupplyLevelAbundant {
				if !haveAbundant {
					abundantStep, abundantTarget, haveAbundant = step, target, true
				}
				continue
			}
			return step, target, true
		}
	}

	if haveAbundant {
		logger.Log("INFO", fmt.Sprintf("Gate factory: the %s factory at %s is already ABUNDANT, but %s has no free hold and no starved factory takes what it carries — unloading its %s there anyway, since that check guards a PURCHASE and this leg makes none", abundantStep.Target, abundantTarget.WaypointSymbol, ship.ShipSymbol(), abundantStep.Input), map[string]interface{}{
			"good": abundantStep.Input, "target": abundantStep.Target, "factory": abundantTarget.WaypointSymbol,
			"ship": ship.ShipSymbol(), "action": "full_hold_abundant_accepted",
		})
		return abundantStep, abundantTarget, true
	}
	return gate.FeedStep{}, nil, false
}

// describeHold renders a hull's cargo for the log. The container-log renderer drops metadata maps,
// so a hold reported only in metadata is exactly as invisible as one not reported at all.
func describeHold(ship *navigation.Ship) string {
	cargo := ship.Cargo()
	if cargo == nil || len(cargo.Inventory) == 0 {
		return "empty"
	}
	parts := make([]string, 0, len(cargo.Inventory))
	for _, item := range cargo.Inventory {
		parts = append(parts, fmt.Sprintf("%d %s", item.Units, item.Symbol))
	}
	return strings.Join(parts, ", ")
}

// gateMaterialsNeediestFirst is the outstanding construction bill in feeding order: neediest gate
// material first, deterministic on a tie so a leg's choice is reproducible. A met bill needs no
// feeding and is dropped.
//
// SHARED by both planners on purpose. The two select different steps for different reasons, but
// they must agree on WHICH MATERIALS ARE IN PLAY — a second copy would be free to drift, and a
// hold-side walk considering a material the buy-side walk had already dropped is how a hull ends
// up feeding a factory whose bill is closed.
func gateMaterialsNeediestFirst(pipeline *manufacturing.ManufacturingPipeline) []*manufacturing.ConstructionMaterialTarget {
	materials := make([]*manufacturing.ConstructionMaterialTarget, 0, len(pipeline.Materials()))
	for _, target := range pipeline.Materials() {
		if target.RemainingQuantity() <= 0 {
			continue // a met bill needs no feeding
		}
		materials = append(materials, target)
	}
	sort.SliceStable(materials, func(i, j int) bool {
		if materials[i].RemainingQuantity() != materials[j].RemainingQuantity() {
			return materials[i].RemainingQuantity() > materials[j].RemainingQuantity()
		}
		return materials[i].TradeSymbol() < materials[j].TradeSymbol()
	})
	return materials
}

// gateFeedCandidate is one feed step that survived the cheap declines, carrying the scarcity the
// ranking sorts on. supply and known are kept alongside rank because the LOG needs the level's
// NAME and needs to distinguish "MODERATE" from "could not read it" — rank alone collapses those
// two into the same integer, which is precisely the confusion the ranking must not hide.
type gateFeedCandidate struct {
	step   gate.FeedStep
	target *mfgServices.MarketLocatorResult
	supply string
	known  bool
	rank   int
}

// scarcityRank orders inputs by how short the destination factory is of them: SCARCE(1) sorts
// ahead of ABUNDANT(5), so a lower rank is a more urgent feed.
//
// AN UNREADABLE SUPPLY RANKS AS MODERATE — the middle of the ladder — and that is a deliberate
// choice between two bad extremes, not a fallback nobody thought about. Ranking unknown FIRST
// prioritises exactly the factories we cannot see, letting one unscanned market capture every leg;
// ranking it LAST starves any factory whose listing happens to be stale. The neutral middle lets a
// genuinely SCARCE step outrank it and a genuinely ABUNDANT one lose to it, while a plan whose
// supplies are ALL unreadable ties throughout and keeps plan order exactly.
//
// It is expressed as MODERATE's own Order() rather than a literal 3 so it cannot drift if the
// ladder is ever renumbered.
func scarcityRank(supply string, known bool) int {
	if !known {
		return shared.SupplyLevelModerate.Order()
	}
	return shared.ParseSupplyLevel(supply).Order()
}

// logGateFeedRanking records the order the leg will actually try, and it only speaks when the
// ranking CHANGED something.
//
// A line per leg restating an unchanged plan order would be pure noise, and noise is how a real
// reordering stops being noticed. But a silent reordering is worse: the leg would feed a different
// input than the plan lists first, with nothing in the log saying why — rebuilding the "ordering
// encodes priority by accident" opacity from the other side.
//
// The numbers go in the MESSAGE: the container log renderer drops metadata maps.
func logGateFeedRanking(ctx context.Context, root string, candidates []gateFeedCandidate) {
	if len(candidates) < 2 {
		return
	}
	head := candidates[0]
	if !head.known {
		return // nothing was readable enough to reorder anything
	}
	reordered := false
	for _, candidate := range candidates {
		if candidate.rank > head.rank {
			reordered = true
			break
		}
	}
	if !reordered {
		return // every candidate is equally short; plan order stands and says so by staying quiet
	}
	order := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		level := candidate.supply
		if !candidate.known {
			level = "unreadable"
		}
		order = append(order, fmt.Sprintf("%s(%s)", candidate.step.Input, level))
	}
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Gate factory: feeding the %s chain by SCARCITY, not recipe order — %s is next because its factory is %s of it; full order %s", root, head.step.Input, head.supply, strings.Join(order, " > ")), map[string]interface{}{
		"root": root, "chosen": head.step.Input, "supply": head.supply,
		"order": strings.Join(order, ">"), "action": "feed_ranked_by_scarcity",
	})
}

// planGateFeed picks THIS leg's single feed step: walk each outstanding gate material neediest
// first and, within a material, take the step whose destination factory is SHORTEST of that input
// (sp-q9um6) among those whose input source AND destination factory both resolve AND whose first
// tranche this treasury can actually pay for.
//
// Ordering runs in two passes, and the split is what keeps the ranking from costing anything it
// does not have to: the two CHEAP declines (no destination factory, target ABUNDANT) are applied to
// every step first, only the survivors are ranked, and the EXPENSIVE work — resolving each input's
// source and pricing its tranche against treasury — happens in ranked order and stops at the first
// step that passes. So a leg still resolves roughly as many sources as before; what changed is
// WHICH step it tries first.
//
// Every declined step is logged with its reason. A walk that declines silently rebuilds the exact
// opacity this design exists to remove — a starved factory and a satisfied one would look the same.
//
// AFFORDABILITY IS A DECLINE, NOT AN ABORT (sp-9eor3), and that distinction is the whole bead. The
// walk RETURNS the first step that passes, so any condition it does not test as a `continue` is one
// that ends the leg from the head of the line: the step is selected, the buy is refused downstream,
// and feedGateLeg parks WITHOUT ever considering step 2. Live, that made the FAB_MATS plan
// (IRON->FAB_MATS, QUARTZ_SAND->FAB_MATS, IRON_ORE->IRON) unable to reach QUARTZ_SAND at all: IRON's
// source resolved, its destination resolved, and its destination was MODERATE rather than ABUNDANT
// so the fail-safe above did not fire — so IRON was selected on every leg, refused on every leg for
// ~209,000 credits against the reserve, and QUARTZ_SAND went unbought for seven hours while the
// factory that needed it read SCARCE. The failure did not self-clear: the only escape was IRON
// reaching ABUNDANT, which required buying the very IRON the guard was refusing.
//
// Being a fourth `continue` rather than a retry at the buy site is what keeps ONE BUY PER LEG — the
// only bound on factory-fleet spend, since an INPUT has no bill (see this file's header, and
// TestFeedGateLeg_BuysExactlyOnceEvenThoughThePlanHasSeveralSteps, which pins the call count).
// It also keeps this function's own contract: a declined step names its reason.
func (h *RunConstructionCoordinatorHandler) planGateFeed(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	systemSymbol string,
	pipeline *manufacturing.ManufacturingPipeline,
	units int,
) (gate.FeedStep, *mfgServices.MarketLocatorResult, *mfgServices.MarketLocatorResult, bool) {
	logger := common.LoggerFromContext(ctx)

	for _, material := range gateMaterialsNeediestFirst(pipeline) {
		plan := gate.PlanFeed(material.TradeSymbol(), h.factory.topology, gate.DefaultFeedDepthCap)
		logger.Log("INFO", plan.LogLine(), map[string]interface{}{
			"root": plan.Root, "steps": len(plan.Steps), "stops": len(plan.Stops),
		})

		// THE TWO CHEAP DECLINES RUN FIRST, ON EVERY STEP, and only the survivors get ranked. An
		// ABUNDANT factory needs no feedstock at ANY rank, so it must never become a candidate —
		// ranking it and then declining it would price an input the leg was never going to buy,
		// which is exactly the ordering the fail-safe's own comment protects.
		candidates := make([]gateFeedCandidate, 0, len(plan.Steps))
		for _, step := range plan.Steps {
			target, terr := h.factory.topology.TerminalFactory(ctx, step.Target, systemSymbol, cmd.PlayerID)
			if terr != nil || target == nil {
				logger.Log("WARNING", fmt.Sprintf("Gate factory: no factory in %s exports %s this era, so its %s feed is declined: %v", systemSymbol, step.Target, step.Input, terr), map[string]interface{}{
					"good": step.Input, "target": step.Target, "reason": "no_destination_factory",
				})
				continue
			}
			// THE ABUNDANT FAIL-SAFE. A factory already at the top of the supply ladder does not
			// need feedstock; buying into a full warehouse burns treasury for nothing. Deliberately
			// the ladder's TOP and nothing else — a threshold here would be a knob, and this phase
			// adds none.
			//
			// It precedes the SOURCE lookup, so a declined step never prices an input it will not
			// buy.
			if shared.ParseSupplyLevel(target.Supply) == shared.SupplyLevelAbundant {
				logger.Log("INFO", fmt.Sprintf("Gate factory: the %s factory at %s is already ABUNDANT — declining its %s feed rather than buying into a full warehouse", step.Target, target.WaypointSymbol, step.Input), map[string]interface{}{
					"good": step.Input, "target": step.Target, "factory": target.WaypointSymbol, "reason": "target_abundant",
				})
				continue
			}
			supply, known := h.factory.topology.ImportSupply(ctx, target.WaypointSymbol, step.Input, cmd.PlayerID)
			candidates = append(candidates, gateFeedCandidate{
				step: step, target: target, supply: supply, known: known,
				rank: scarcityRank(supply, known),
			})
		}

		// SCARCEST INPUT FIRST, PLAN ORDER TO BREAK EVERY TIE (sp-q9um6).
		//
		// A factory cannot produce while it is short of ANY input, so the input it is shortest of
		// is the one worth a hull-load — regardless of where the recipe happens to list it. Plan
		// order encoded priority purely by accident: F45 read IRON MODERATE and QUARTZ_SAND SCARCE,
		// and IRON won every leg for no better reason than appearing first in the recipe.
		//
		// THE SORT IS STABLE, AND THAT IS THE SAFETY PROPERTY, not a detail. Ties keep plan order,
		// and every step whose supply could not be read ties with every other — so when the market
		// data is absent this ordering degrades to EXACTLY the plan order it replaced. A ranking
		// that fell back to something else would be worse than no ranking at all, because plan
		// order is at least predictable. It also preserves shallowest-first among equally-short
		// steps, which plan order already encodes and which
		// TestFeedGateLeg_FeedsTheTerminalFactoryBeforeAnythingDeeper pins.
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].rank < candidates[j].rank })

		logGateFeedRanking(ctx, plan.Root, candidates)

		// THE EXPENSIVE DECLINES RUN IN RANKED ORDER, so the scarcest input is the one whose source
		// is resolved and whose cost is priced first — and a scarcest step that declines yields to
		// the next-scarcest rather than ending the leg (sp-9eor3).
		for _, candidate := range candidates {
			step, target := candidate.step, candidate.target
			source, serr := h.factory.topology.TerminalFactory(ctx, step.Input, systemSymbol, cmd.PlayerID)
			if serr != nil || source == nil {
				// A refusal, never a substitution: sending a hull to some other waypoint is how
				// cargo ends up somewhere that cannot accept it.
				logger.Log("WARNING", fmt.Sprintf("Gate factory: nothing in %s exports %s, so the %s factory cannot be fed it this leg: %v", systemSymbol, step.Input, step.Target, serr), map[string]interface{}{
					"good": step.Input, "target": step.Target, "reason": "no_input_source",
				})
				continue
			}
			// THE AFFORDABILITY DECLINE. Everything this needs is known here — the market, the good,
			// its cached ask, the per-transaction volume and the free hold — so a step the reserve
			// will refuse is passed over for one it will not, instead of being selected and taking
			// the leg down with it.
			//
			// THE SUBJECT IS THE FIRST TRANCHE, not the whole hull-load, and matching the guard's own
			// subject is what keeps this from over-declining. fillFromSource buys in tranches of
			// min(free hold, trade volume, trip target) and re-checks the floor before EACH one, so a
			// fill is refused OUTRIGHT — QuantityAcquired 0, the condition that ends the leg — only
			// when the FIRST tranche breaches. Pricing the full hold instead would decline steps that
			// would have partially filled and fed the factory perfectly well, which is a second way to
			// starve the walk wearing the fix's clothes.
			//
			// AN UNKNOWN PRICE IS NOT AN UNAFFORDABLE ONE. With no cached quote there is no prediction
			// to make, so the step proceeds and the commit-time guard decides on arrival as it always
			// has. Declining here instead would let a cold or unscanned price cache freeze the entire
			// feed — refusing on ABSENCE of evidence rather than on evidence, which deadlocks exactly
			// when the fleet is coldest.
			if source.Price > 0 && source.TradeVolume > 0 {
				tranche := min(units, source.TradeVolume)
				projected := tranche * source.Price
				if breached, reserve := h.factory.buyer.SpendFloorWouldBreach(ctx, cmd.PlayerID, projected); breached {
					// THE NUMBERS GO IN THE MESSAGE. The container log renderer drops metadata maps,
					// and this leg emits no metric — the log line IS the counter — so a decline whose
					// cost and reserve live only in metadata is a decline nobody can size.
					//
					// Distinct wording from the on-arrival "acquired nothing" refusal ON PURPOSE
					// (criterion 4): one says a hull flew and was refused, the other says no hull was
					// ever sent. Collapsing them into one phrase rebuilds the log archaeology this
					// bead was filed out of.
					logger.Log("WARNING", fmt.Sprintf("Gate factory: declining the %s feed for the %s factory — %d units at %s would cost about %d against a reserve of %d, so this leg takes the next affordable step instead of stalling on this one", step.Input, step.Target, tranche, source.WaypointSymbol, projected, reserve), map[string]interface{}{
						"good": step.Input, "target": step.Target, "source": source.WaypointSymbol,
						"units": tranche, "projected_cost": projected, "reserve": reserve,
						"reason": "unaffordable_input", "action": "step_skipped_unaffordable",
					})
					continue
				}
			}
			return step, source, target, true
		}
	}
	return gate.FeedStep{}, nil, nil, false
}

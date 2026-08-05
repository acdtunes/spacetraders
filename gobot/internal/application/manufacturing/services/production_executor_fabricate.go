package services

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// productionDwellWarnThreshold bounds how long PollForProduction can wait on a
// fabrication without escalating its logging. Below this, the existing sparse
// "every 5th attempt" INFO cadence is enough; past it, a WARNING fires on
// EVERY attempt so a factory holding docked hull claims for tens of minutes
// (sp-npyr: SHIP_PARTS held TORWIND-3/6 for 40+ min with only sparse logging,
// reading as a silent stall from the outside) has its wait reason visible in
// the logs at the true claim-holding site.
const productionDwellWarnThreshold = 5 * time.Minute

// productionPollLogEveryN is the sparse INFO cadence referenced by productionDwellWarnThreshold.
const productionPollLogEveryN = 5

// defaultProductionPollIntervals is the fallback fabrication poll schedule: a fast first
// poll to catch quick production, then a settled interval. Read-only; never mutated in place.
var defaultProductionPollIntervals = []time.Duration{30 * time.Second, 60 * time.Second}

// fabricationRun is one fabricate-a-good request: the hull doing the work, the supply-chain node
// being made, and the operation it is booked against.
type fabricationRun struct {
	ship         *navigation.Ship
	node         *goods.SupplyChainNode
	systemSymbol string
	playerID     int
	opContext    *shared.OperationContext // for transaction linking
	// inputsOnly applies to THIS node's output only; forChild always clears it, because an
	// intermediate input must be harvested before it can be delivered to the parent factory.
	inputsOnly bool
}

func (r fabricationRun) forChild(child *goods.SupplyChainNode) fabricationRun {
	r.node = child
	r.inputsOnly = false
	return r
}

// haulingInputs is the goods this run acquires and carries to the factory: the node's CHILDREN,
// which is exactly what produceInputsForFactory loops over to fill the hold.
//
// Deliberately not the recipe from goods.ExportToImportMap. The two agree in production (the tree
// is built from that same map) but the tree is the authority on what was actually produced, and
// where they diverge the recipe refuses runs that carry nothing: a childless FABRICATE node hauls
// no inputs at all — it delivers whatever is already aboard and harvests — and judging it against
// its recipe would park it over goods it was never carrying. Cargo aboard that this run did not
// acquire is likewise not a reason to refuse the trip; deliverInputs already holds such goods
// rather than dumping them (sp-w2qg5).
func (r fabricationRun) haulingInputs() []string {
	inputs := make([]string, 0, len(r.node.Children))
	for _, child := range r.node.Children {
		inputs = append(inputs, child.Good)
	}
	return inputs
}

// fabricateGood manufactures a good by producing inputs and delivering them to a manufacturing waypoint
func (e *ProductionExecutor) fabricateGood(ctx context.Context, run fabricationRun) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)
	node := run.node
	totalCost := 0

	// Step 0: Check if factory already has ABUNDANT supply - skip input production if so
	// This allows opportunistic collection when factory already has goods ready
	factoryMarket, err := e.marketLocator.FindExportMarket(ctx, node.Good, run.systemSymbol, run.playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find factory (export market) for %s: %w", node.Good, err)
	}

	playerIDValue := shared.MustNewPlayerID(run.playerID)
	stockedResult, err := e.collectExistingFactorySupply(ctx, run, factoryMarket, playerIDValue)
	if err != nil {
		return nil, err
	}
	if stockedResult != nil {
		return stockedResult, nil
	}

	if e.chainMarginParked(ctx, run) {
		return &ProductionResult{
			QuantityAcquired: 0,
			TotalCost:        0,
			WaypointSymbol:   factoryMarket.WaypointSymbol,
		}, nil
	}

	// A good that does not respond to feeding (EQUIPMENT/LAB_INSTRUMENTS/FOOD/MEDICINE) makes
	// hauling inputs to it wasted hull-hours: buy-or-skip. Step 0 already bought it if the factory
	// was abundantly stocked, so reaching here means not-stocked.
	feedCfg := defaultFeedingPolicy()
	if len(node.Children) > 0 && !feedCfg.isFeedResponsive(node.Good) {
		logger.Log("INFO", fmt.Sprintf("Feed-responsive gate: %s does not respond to feeding — buy-or-skip, not hauling inputs (sp-to2v)", node.Good), map[string]interface{}{
			"good": node.Good, "factory": factoryMarket.WaypointSymbol,
			"action": "feed_skipped", "reason": "non_responsive_good",
		})
		return &ProductionResult{QuantityAcquired: 0, TotalCost: 0, WaypointSymbol: factoryMarket.WaypointSymbol}, nil
	}

	inputCost, err := e.produceInputsForFactory(ctx, run, feedCfg)
	if err != nil {
		return nil, err
	}
	totalCost += inputCost

	// The factory EXPORTS the finished good (a low ASK) and IMPORTS the inputs — an import
	// market is a consumer that buys at a high price, which is the opposite of what we need.
	logger.Log("INFO", fmt.Sprintf("Found factory (export market) for %s at %s", node.Good, factoryMarket.WaypointSymbol), map[string]interface{}{
		"good":     node.Good,
		"waypoint": factoryMarket.WaypointSymbol,
		"ask":      factoryMarket.Price, // the factory's ASK — what we pay to harvest (sp-en5h7)
	})

	if refused := e.feedDestinationRefused(ctx, run, factoryMarket.WaypointSymbol, playerIDValue); refused {
		// The inputs already bought stay aboard and the run reports what it actually spent —
		// see feedDestinationRefused for why this cost is not zeroed like the earlier parks.
		return &ProductionResult{
			QuantityAcquired: 0,
			TotalCost:        totalCost,
			WaypointSymbol:   factoryMarket.WaypointSymbol,
		}, nil
	}

	updatedShip, err := e.NavigateAndDock(ctx, run.ship.ShipSymbol(), factoryMarket.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to factory: %w", err)
	}

	// The factory IMPORTS the inputs, so delivering them is a sell.
	deliveryRevenue, _ := e.deliverInputs(ctx, updatedShip, playerIDValue)
	totalCost -= deliveryRevenue

	logger.Log("INFO", "Delivered inputs to factory", map[string]interface{}{
		"good":             node.Good,
		"waypoint":         factoryMarket.WaypointSymbol,
		"delivery_revenue": deliveryRevenue,
	})

	// In inputs-only mode the poll still confirms the output was produced, but the harvest is
	// skipped so the good is left in factory stock.
	quantity, cost, err := e.PollForProduction(ctx, node.Good, factoryMarket.WaypointSymbol, updatedShip.ShipSymbol(), playerIDValue, run.opContext, run.inputsOnly, run.systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed during production polling: %w", err)
	}

	totalCost += cost

	return &ProductionResult{
		QuantityAcquired: quantity,
		TotalCost:        totalCost,
		WaypointSymbol:   factoryMarket.WaypointSymbol,
	}, nil
}

// gateTopology is the executor's role-resolving view of the era's topology. It is built on demand
// from what the executor already holds rather than injected, and that is deliberate: the recipe
// map is a game constant (the same goods.ExportToImportMap both production SupplyChainResolver
// sites are built from) and *MarketLocator is already the executor's market-role resolver, so
// there is nothing here a caller could meaningfully vary. Building it here also leaves every
// existing construction site untouched — including the struct-literal ProductionExecutors in the
// package's own fixtures, which a nil-able field would nil-panic through Inputs.
//
// Consequently the feed guard below is ARMED for every executor in the process; there is no
// wiring step that can be forgotten and no configuration that can leave it off.
func (e *ProductionExecutor) gateTopology() *GateTopology {
	return NewGateTopology(e.marketLocator, goods.ExportToImportMap)
}

// feedDestinationRefused reports whether the factory this run is about to be sent to will refuse
// the inputs it is carrying, and logs the reason when it does.
//
// fabricateGood navigates to the factory that EXPORTS the good, assuming that factory also IMPORTS
// that good's inputs. Within one chain it does; across chains it does not, and sp-b27a2 is exactly
// that case — IRON_ORE (FAB_MATS chain) carried to the ADVANCED_CIRCUITRY exporter, which imports
// nothing from that chain, leaving the hauler at 80/80 unable to deliver or dump. Checking before
// the navigate is the root-cause fix: deliverInputs already holds cargo the local market will not
// buy (sp-w2qg5), but by the time it runs the hull is already at the wrong waypoint.
//
// The subject is run.haulingInputs — the goods this run actually acquired for the factory — not
// the good's recipe. See haulingInputs for why the distinction matters.
//
// The guard itself lives in feedDestinationRefusedFor, which the gate FACTORY leg shares. This
// method is now only the fabricate path's binding of run -> input list.
func (e *ProductionExecutor) feedDestinationRefused(
	ctx context.Context,
	run fabricationRun,
	factoryWaypoint string,
	playerID shared.PlayerID,
) bool {
	return e.feedDestinationRefusedFor(ctx, run.haulingInputs(), factoryWaypoint, playerID)
}

// feedDestinationRefusedFor is the sp-b27a2 guard itself, over an explicit input list.
//
// TWO callers share this ONE copy, and that sharing is the whole point: the fabricate path passes
// run.haulingInputs(), the gate FACTORY leg passes the inputs it bought for a feed step. A second
// copy would be free to drift, and the failure mode of a drifted copy is a hull at 80/80 with
// cargo it can neither deliver nor dump.
//
// There is no fallback to another waypoint on refusal. Substituting one is precisely how cargo
// ends up somewhere that cannot accept it, which is the incident this guard exists to prevent.
func (e *ProductionExecutor) feedDestinationRefusedFor(
	ctx context.Context,
	inputs []string,
	factoryWaypoint string,
	playerID shared.PlayerID,
) bool {
	// An unreadable listing is passed through as nil, which ValidateFeedDestination refuses.
	// Reading the destination's OWN listing is what makes the check exact: a system can hold
	// several markets importing a good, so the best-bid importer is not the question here.
	destination, err := e.marketRepo.GetMarketData(ctx, factoryWaypoint, playerID.Value())
	if err != nil {
		destination = nil
	}

	refusal := e.gateTopology().ValidateFeedDestination(destination, factoryWaypoint, inputs)
	if refusal == nil {
		return false
	}

	// Cause in the MESSAGE: the container-log renderer drops metadata.
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Refusing to haul %v to %s: %v — the hull would arrive with cargo it can neither deliver nor dump (sp-b27a2)",
		inputs, factoryWaypoint, refusal,
	), map[string]interface{}{
		"inputs": inputs, "factory": factoryWaypoint,
		"action": "feed_refused", "reason": "destination_cannot_accept_inputs",
		"error": refusal.Error(),
	})
	return true
}

// chainMarginParked refuses, before ANY input is bought, a fabrication that is structurally
// underwater — summed input ask already above the output's resale bid — even when no single input
// trips the per-buy ceiling.
//
// Scoped to RESALE runs: a construction-supply or inputs-only run delivers its output to the gate
// and never resells it, so a resale-margin veto there would wrongly park the gate fill. Those runs'
// input buys still pass the full money-guard stack (RULINGS #4).
func (e *ProductionExecutor) chainMarginParked(ctx context.Context, run fabricationRun) bool {
	if run.inputsOnly || shared.ConstructionSupplyFromContext(ctx) || len(run.node.Children) == 0 {
		return false
	}
	return e.inputRoundMarginParked(ctx, run.node, run.systemSymbol, run.playerID)
}

// produceInputsForFactory recursively acquires every input this factory needs, scarcest (taproot)
// first and each capped at the balanced tranche sized to the limiting input, so an ample input is
// not piled on while the scarce one starves. Returns their summed cost.
func (e *ProductionExecutor) produceInputsForFactory(ctx context.Context, run fabricationRun, feedCfg feedingPolicyConfig) (int, error) {
	logger := common.LoggerFromContext(ctx)
	node := run.node
	logger.Log("INFO", fmt.Sprintf("Starting fabrication of %s (requires %d inputs)", node.Good, len(node.Children)), map[string]interface{}{
		"good":        node.Good,
		"input_count": len(node.Children),
	})

	childOrder := node.Children
	feedCap := 0
	if len(node.Children) > 0 {
		childOrder, feedCap = e.planBalancedFeed(ctx, node, run.systemSymbol, run.playerID, feedCfg)
	}

	totalCost := 0
	for _, child := range childOrder {
		// Children are inputs that must be harvested and delivered to THIS factory —
		// inputs-only never suppresses their acquisition, so forChild forces it off.
		childCtx := ctx
		if feedCap > 0 {
			// A FRESH per-child stamp: the child's own planner re-stamps a cap for its subtree, so
			// a parent's cap never leaks onto a grandchild.
			childCtx = WithInputFeedCap(ctx, feedCap)
		}
		childRun := run.forChild(child)
		result, err := e.ProduceGood(childCtx, childRun.ship, childRun.node, childRun.systemSymbol, childRun.playerID, childRun.opContext, childRun.inputsOnly)
		if err != nil {
			return 0, fmt.Errorf("failed to produce input %s: %w", child.Good, err)
		}
		totalCost += result.TotalCost
		logger.Log("INFO", fmt.Sprintf("Produced input: %d units of %s (cost: %d credits)", result.QuantityAcquired, child.Good, result.TotalCost), map[string]interface{}{
			"input_good": child.Good,
			"quantity":   result.QuantityAcquired,
			"cost":       result.TotalCost,
		})
	}
	return totalCost, nil
}

func (e *ProductionExecutor) collectExistingFactorySupply(
	ctx context.Context,
	run fabricationRun,
	factoryMarket *MarketLocatorResult,
	playerIDValue shared.PlayerID,
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)
	node := run.node

	marketData, err := e.marketRepo.GetMarketData(ctx, factoryMarket.WaypointSymbol, playerIDValue.Value())
	if err != nil || marketData == nil {
		return nil, nil
	}
	tradeGood := marketData.FindGood(node.Good)
	if tradeGood == nil || tradeGood.Supply() == nil {
		return nil, nil
	}
	supply := *tradeGood.Supply()
	if !isHighOrAbundant(supply) {
		return nil, nil
	}

	logger.Log("INFO", fmt.Sprintf("Factory already has %s supply of %s - skipping input production", supply, node.Good), map[string]interface{}{
		"good":    node.Good,
		"factory": factoryMarket.WaypointSymbol,
		"supply":  supply,
	})

	// Navigate directly to factory and purchase
	updatedShip, err := e.NavigateAndDock(ctx, run.ship.ShipSymbol(), factoryMarket.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to factory: %w", err)
	}

	// Purchase the goods directly (PollForProduction will find them immediately since supply is HIGH/ABUNDANT).
	// In inputs-only mode the harvest is skipped, so the already-abundant stock is left for construction to source.
	quantity, cost, err := e.PollForProduction(ctx, node.Good, factoryMarket.WaypointSymbol, updatedShip.ShipSymbol(), playerIDValue, run.opContext, run.inputsOnly, run.systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to purchase from factory: %w", err)
	}

	return &ProductionResult{
		QuantityAcquired: quantity,
		TotalCost:        cost,
		WaypointSymbol:   factoryMarket.WaypointSymbol,
	}, nil
}

// PollForProduction polls the market database until the output good appears in exports.
// Uses exponential backoff with NO timeout - polls indefinitely until good appears or context cancelled.
// Returns quantity purchased and cost.
func (e *ProductionExecutor) PollForProduction(
	ctx context.Context,
	good string,
	waypointSymbol string,
	shipSymbol string,
	playerID shared.PlayerID,
	opContext *shared.OperationContext, // Operation context for transaction linking
	inputsOnly bool, // when true, confirm production then LEAVE the output in factory stock (skip the harvest)
	systemSymbol string, // system to search for a resale sink when checking the crushed-sink guard (bp6f #3)
) (int, int, error) {
	logger := common.LoggerFromContext(ctx)
	run := harvestRun{
		good:           good,
		waypointSymbol: waypointSymbol,
		shipSymbol:     shipSymbol,
		systemSymbol:   systemSymbol,
		playerID:       playerID,
		inputsOnly:     inputsOnly,
	}

	intervals := e.pollingIntervals
	if len(intervals) == 0 {
		intervals = defaultProductionPollIntervals
	}

	attempt := 0
	pollStart := e.clock.Now()
	for {
		select {
		case <-ctx.Done():
			return 0, 0, fmt.Errorf("production polling cancelled: %w", ctx.Err())
		default:
			// Continue polling
		}

		// Query market data from database (kept fresh by scout tours)
		marketData, err := e.marketRepo.GetMarketData(ctx, waypointSymbol, playerID.Value())
		if err != nil {
			return 0, 0, fmt.Errorf("failed to get market data during polling: %w", err)
		}

		if tradeGood := marketData.FindGood(good); tradeGood != nil {
			logger.Log("INFO", fmt.Sprintf("Production complete: %s now available at %s (polled %d times)", good, waypointSymbol, attempt+1), map[string]interface{}{
				"good":          good,
				"waypoint":      waypointSymbol,
				"poll_attempts": attempt + 1,
				"ask":           tradeGood.PurchasePrice(),
			})
			return e.harvestProducedOutput(ctx, run, tradeGood)
		}

		waitDuration := intervals[min(attempt, len(intervals)-1)]
		e.logProductionWait(ctx, run, attempt, e.clock.Now().Sub(pollStart), waitDuration)

		timer := time.NewTimer(waitDuration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, 0, fmt.Errorf("production polling cancelled during wait: %w", ctx.Err())
		case <-timer.C:
			// Continue to next poll attempt
		}

		attempt++
	}
}

// harvestRun is one fabrication's harvest target: the good, the factory it was produced at, the
// hull collecting it, and the system a resale sink is searched in for the crushed-sink guard.
type harvestRun struct {
	good           string
	waypointSymbol string
	shipSymbol     string
	systemSymbol   string
	playerID       shared.PlayerID
	// inputsOnly confirms production but LEAVES the output in factory stock (skips the harvest).
	inputsOnly bool
}

// harvestProducedOutput decides what to do with a confirmed batch of finished goods sitting in the
// factory's export stock, and buys it into the hold unless a guard parks the harvest.
func (e *ProductionExecutor) harvestProducedOutput(
	ctx context.Context,
	run harvestRun,
	tradeGood *market.TradeGood,
) (int, int, error) {
	logger := common.LoggerFromContext(ctx)

	// Construction-support: leave the output for the construction pipeline to be the SOLE buyer.
	// Harvesting here is what starved the era-2 gate fill — the factory bought back its own output.
	if run.inputsOnly {
		logger.Log("INFO", fmt.Sprintf("inputs-only: %s produced and left in factory stock at %s — harvest skipped", run.good, run.waypointSymbol), map[string]interface{}{
			"good":     run.good,
			"waypoint": run.waypointSymbol,
		})
		return 0, 0, nil
	}

	if e.crushedSinkParked(ctx, run, tradeGood.PurchasePrice()) {
		return 0, 0, nil
	}

	return e.purchaseFabricatedOutput(ctx, run, tradeGood.TradeVolume(), tradeGood.PurchasePrice())
}

// crushedSinkParked refuses a harvest whose downstream resale bid has fallen below what the factory
// charges to harvest it — loss-making production on every cycle. Fails OPEN when no sink can be
// found: that is normal for a good with no direct resale market, not a signal to stop producing.
//
// A construction-supply or unified gate-fill harvest delivers to the jump-gate site rather than a
// resale sink, so the guard does not apply — those nodes are margin-blind by design and their input
// buys already passed the full money-guard stack (RULINGS #4).
func (e *ProductionExecutor) crushedSinkParked(ctx context.Context, run harvestRun, harvestCost int) bool {
	if shared.ConstructionSupplyFromContext(ctx) || IsUnifiedGateNode(ctx) {
		return false
	}
	sink, sinkErr := e.marketLocator.FindImportMarket(ctx, run.good, run.systemSymbol, run.playerID.Value())
	if sinkErr != nil || sink == nil || sink.Price >= harvestCost {
		return false
	}
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Parking %s at %s: crushed sink - resale bid %d at %s is below harvest cost %d, producing would lose money",
		run.good, run.waypointSymbol, sink.Price, sink.WaypointSymbol, harvestCost,
	), map[string]interface{}{
		"action":       "factory_parked",
		"reason":       "crushed_sink",
		"good":         run.good,
		"waypoint":     run.waypointSymbol,
		"sink":         sink.WaypointSymbol,
		"sink_bid":     sink.Price,
		"harvest_cost": harvestCost,
	})
	return true
}

// logProductionWait states the elapsed dwell in the message TEXT once a fabrication passes
// productionDwellWarnThreshold — the container-log renderer drops metadata — so a long wait reads
// as progress rather than a silent stall. Below the threshold the sparse INFO cadence is enough.
func (e *ProductionExecutor) logProductionWait(ctx context.Context, run harvestRun, attempt int, elapsed, nextWait time.Duration) {
	logger := common.LoggerFromContext(ctx)
	if elapsed >= productionDwellWarnThreshold {
		logger.Log("WARNING", fmt.Sprintf(
			"Still waiting on %s at %s after %s (ship %s, attempt %d) — fabrication in progress, not stalled",
			run.good, run.waypointSymbol, elapsed.Round(time.Second), run.shipSymbol, attempt+1,
		), map[string]interface{}{
			"good":          run.good,
			"waypoint":      run.waypointSymbol,
			"ship":          run.shipSymbol,
			"attempt":       attempt + 1,
			"elapsed_sec":   elapsed.Seconds(),
			"next_wait_sec": nextWait.Seconds(),
		})
		return
	}
	if attempt == 0 || attempt%productionPollLogEveryN == 0 {
		logger.Log("INFO", "Polling for production completion", map[string]interface{}{
			"good":          run.good,
			"waypoint":      run.waypointSymbol,
			"attempt":       attempt + 1,
			"next_wait_sec": nextWait.Seconds(),
		})
	}
}

func (e *ProductionExecutor) purchaseFabricatedOutput(
	ctx context.Context,
	run harvestRun,
	tradeVolume int,
	unitPrice int, // per-unit harvest cost (the factory's ask = tradeGood.PurchasePrice()) — the working-capital-floor basis
) (int, int, error) {
	logger := common.LoggerFromContext(ctx)
	good, waypointSymbol, shipSymbol, playerID := run.good, run.waypointSymbol, run.shipSymbol, run.playerID

	ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to reload ship: %w", err)
	}

	ship, roomFreed := e.makeRoomForOutputHarvest(ctx, ship, good, playerID)
	if !roomFreed {
		return 0, 0, nil
	}
	availableSpace := ship.Cargo().Capacity - ship.Cargo().Units

	purchaseQty := min(availableSpace, harvestTrancheCap(ctx, tradeVolume))
	if purchaseQty <= 0 {
		return 0, 0, fmt.Errorf("trade volume is zero for %s", good)
	}

	// The fabricated-OUTPUT buy is floored IDENTICALLY to the raw-input path, reusing the same
	// primitive — no second floor, no new knob (RULINGS #4/#5). Fail-CLOSED: a breach, or a blind
	// treasury read inside the guard, PARKS the harvest, which loses nothing because the good stays
	// in the factory's export stock for a later pass.
	projectedCost := purchaseQty * unitPrice
	if breached, enforcedFloor := e.spendFloorBreached(ctx, playerID.Value(), projectedCost); breached {
		logger.Log("WARNING", fmt.Sprintf("Parked fabricated-output harvest of %s at %s — would breach the working-capital reserve (projected cost %d, reserve %d)", good, waypointSymbol, projectedCost, enforcedFloor), map[string]interface{}{
			"good": good, "market": waypointSymbol, "projected_cost": projectedCost,
			"action": "factory_parked", "reason": "spend_floor",
		})
		return 0, 0, nil
	}

	reservationID, parked := e.reserveConcurrentSpendOrPark(ctx, playerID.Value(), projectedCost, waypointSymbol, good)
	if parked {
		return 0, 0, nil // concurrent cap (reserveConcurrentSpendOrPark logged the cause)
	}

	logger.Log("INFO", fmt.Sprintf("Purchasing %d units of fabricated %s (cargo: %d, trade_volume: %d)", purchaseQty, good, availableSpace, tradeVolume), nil)

	purchaseCmd := &shipCargo.PurchaseCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      purchaseQty,
		PlayerID:   playerID,
	}

	// Same dock-retry guard as the raw-buy path: a transient "must be docked" re-docks and retries
	// rather than crashing the container. The reservation is released on BOTH paths.
	response, err := e.purchaseWithDockRetry(ctx, purchaseCmd)
	e.releaseSpendReservation(ctx, playerID.Value(), reservationID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to purchase fabricated output: %w", err)
	}

	logger.Log("INFO", fmt.Sprintf("Purchased fabricated output: %d units of %s for %d credits", response.UnitsAdded, good, response.TotalCost), map[string]interface{}{
		"good":       good,
		"quantity":   response.UnitsAdded,
		"total_cost": response.TotalCost,
		"waypoint":   waypointSymbol,
	})

	return response.UnitsAdded, response.TotalCost, nil
}

// harvestTrancheCap bounds one output harvest: the market's own trade volume — the natural "buy
// only what's exported" limit that paces the drain — lowered to the balanced tranche when this
// output is a fabricated INPUT being harvested to feed a parent factory, so an ample intermediate
// is not over-harvested past its scarce sibling's flow. A ROOT output has no parent cap.
func harvestTrancheCap(ctx context.Context, tradeVolume int) int {
	if feedCap, capped := inputFeedCapFromContext(ctx); capped && feedCap < tradeVolume {
		return feedCap
	}
	return tradeVolume
}

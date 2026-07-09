package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// productionDockConfirmAttempts bounds how many times NavigateAndDock will reload
// and re-issue a dock while waiting for the ship to reach a confirmed DOCKED
// state (arrival + persisted dock). Bounded so a wedged ship can never spin
// forever.
const productionDockConfirmAttempts = 10

// productionDockRetryLimit bounds how many times a cargo transaction that fails
// with a transient "must be docked" signal is re-docked and retried before the
// error is surfaced. Bounded so a genuinely undockable ship can never infinite
// loop (sp-n7yp feeder crash #3).
const productionDockRetryLimit = 3

// productionEmptyTrancheRetryLimit bounds how many times an input buy that comes
// back empty ("partial failure: ... 0 units processed" / API 400 — a market drained
// between the scout read and the buy) is retried before the tranche is skipped so the
// feeder run can continue. Bounded so a structurally-empty market can never
// infinite-loop (sp-q02m feeder crash #4).
const productionEmptyTrancheRetryLimit = 3

// productionEmptyTrancheRetryDelay is the backoff between empty-tranche retries,
// giving the market a chance to refill. It runs on the injected clock, so it is a
// no-op under the test clock.
const productionEmptyTrancheRetryDelay = 2 * time.Second

// productionDwellWarnThreshold bounds how long PollForProduction can wait on a
// fabrication without escalating its logging. Below this, the existing sparse
// "every 5th attempt" INFO cadence is enough; past it, a WARNING fires on
// EVERY attempt so a factory holding docked hull claims for tens of minutes
// (sp-npyr: SHIP_PARTS held TORWIND-3/6 for 40+ min with only sparse logging,
// reading as a silent stall from the outside) has its wait reason visible in
// the logs at the true claim-holding site.
const productionDwellWarnThreshold = 5 * time.Minute

// ProductionExecutor orchestrates the production of goods by coordinating ship operations.
// It handles both purchasing goods from markets (BUY) and manufacturing them (FABRICATE).
type ProductionExecutor struct {
	mediator         common.Mediator
	shipRepo         navigation.ShipRepository
	marketRepo       market.MarketRepository
	marketLocator    *MarketLocator
	clock            shared.Clock
	pollingIntervals []time.Duration // Configurable polling intervals
}

// NewProductionExecutor creates a new production executor with default polling intervals
func NewProductionExecutor(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketLocator *MarketLocator,
	clock shared.Clock,
) *ProductionExecutor {
	return NewProductionExecutorWithConfig(
		mediator,
		shipRepo,
		marketRepo,
		marketLocator,
		clock,
		[]time.Duration{30 * time.Second, 60 * time.Second}, // Default intervals
	)
}

// NewProductionExecutorWithConfig creates a new production executor with custom polling intervals
func NewProductionExecutorWithConfig(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketLocator *MarketLocator,
	clock shared.Clock,
	pollingIntervals []time.Duration,
) *ProductionExecutor {
	return &ProductionExecutor{
		mediator:         mediator,
		shipRepo:         shipRepo,
		marketRepo:       marketRepo,
		marketLocator:    marketLocator,
		clock:            clock,
		pollingIntervals: pollingIntervals,
	}
}

// ProductionResult contains the outcome of a production operation
type ProductionResult struct {
	QuantityAcquired int
	TotalCost        int
	WaypointSymbol   string // Where the good was acquired
}

// ProduceGood orchestrates the production of a good using the given ship.
// For BUY nodes: finds market, navigates, purchases whatever is available.
// For FABRICATE nodes: recursively produces inputs, delivers them, polls for output, purchases output.
// Returns the quantity acquired and total cost.
//
// inputsOnly applies to the OUTPUT of this node only: when true and this node is
// fabricated, its finished output is left in factory stock instead of being
// harvested (sp-q02m). It never suppresses an input buy — the raw materials still
// have to be acquired and delivered — so buyGood ignores it, and fabricateGood
// forces it off when recursing into children (an intermediate fabricated input must
// be harvested so it can be delivered to the parent factory).
func (e *ProductionExecutor) ProduceGood(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
	opContext *shared.OperationContext, // Operation context for transaction linking
	inputsOnly bool,
) (*ProductionResult, error) {
	// Add operation context to Go context for transaction tagging
	if opContext != nil && opContext.IsValid() {
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	switch node.AcquisitionMethod {
	case goods.AcquisitionBuy:
		return e.buyGood(ctx, ship, node, systemSymbol, playerID, opContext)
	case goods.AcquisitionFabricate:
		return e.fabricateGood(ctx, ship, node, systemSymbol, playerID, opContext, inputsOnly)
	default:
		return nil, fmt.Errorf("unknown acquisition method: %s", node.AcquisitionMethod)
	}
}

// buyGood purchases a good from a market
func (e *ProductionExecutor) buyGood(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	// Find best market selling this good
	marketResult, err := e.marketLocator.FindExportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find market selling %s: %w", node.Good, err)
	}

	logger.Log("INFO", fmt.Sprintf("Found export market for %s purchase", node.Good), map[string]interface{}{
		"good":         node.Good,
		"market":       marketResult.WaypointSymbol,
		"price":        marketResult.Price,
		"activity":     marketResult.Activity,
		"supply":       marketResult.Supply,
		"trade_volume": marketResult.TradeVolume,
	})

	// Navigate to market and dock
	playerIDValue := shared.MustNewPlayerID(playerID)
	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), marketResult.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to market: %w", err)
	}

	// Calculate purchase quantity (capped by cargo space and trade volume)
	availableSpace := updatedShip.Cargo().Capacity - updatedShip.Cargo().Units
	if availableSpace <= 0 {
		// sp-mu6u: a full hold used to crash the feeder outright here. We're
		// already docked at this market, so try to sell whatever is onboard to
		// free space before giving up — a factory that didn't unload its last
		// output before buying the next input should recover, not die.
		freedShip, sellErr := e.freeCargoSpace(ctx, updatedShip, playerIDValue)
		if sellErr != nil {
			logger.Log("WARN", fmt.Sprintf("Hold full and could not unload existing cargo — skipping this input purchase of %s", node.Good), map[string]interface{}{
				"good":  node.Good,
				"ship":  updatedShip.ShipSymbol(),
				"error": sellErr.Error(),
			})
			return &ProductionResult{
				QuantityAcquired: 0,
				TotalCost:        0,
				WaypointSymbol:   marketResult.WaypointSymbol,
			}, nil
		}
		updatedShip = freedShip
		availableSpace = updatedShip.Cargo().Capacity - updatedShip.Cargo().Units
		if availableSpace <= 0 {
			logger.Log("WARN", fmt.Sprintf("Hold still full after unloading existing cargo — skipping this input purchase of %s", node.Good), map[string]interface{}{
				"good": node.Good,
				"ship": updatedShip.ShipSymbol(),
			})
			return &ProductionResult{
				QuantityAcquired: 0,
				TotalCost:        0,
				WaypointSymbol:   marketResult.WaypointSymbol,
			}, nil
		}
	}

	// Cap at trade volume to leave room for other inputs
	purchaseQty := min(availableSpace, marketResult.TradeVolume)
	if purchaseQty <= 0 {
		return nil, fmt.Errorf("trade volume is zero for %s", node.Good)
	}

	logger.Log("INFO", fmt.Sprintf("Purchasing %d units of %s (cargo: %d, trade_volume: %d)", purchaseQty, node.Good, availableSpace, marketResult.TradeVolume), nil)

	// Purchase cargo (capped by trade volume)
	purchaseCmd := &shipCargo.PurchaseCargoCommand{
		ShipSymbol: updatedShip.ShipSymbol(),
		GoodSymbol: node.Good,
		Units:      purchaseQty,
		PlayerID:   playerIDValue,
	}

	// Dispatch through the empty-tranche guard: a transient "must be docked" is still
	// re-docked and retried inside (sp-n7yp); an empty / zero-volume tranche
	// ("partial failure: ... 0 units processed" / API 400) is bounded-retried then
	// skipped so the feeder survives instead of crashing the container (sp-q02m crash #4).
	response, err := e.purchaseInputWithEmptyTrancheGuard(ctx, purchaseCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to purchase cargo: %w", err)
	}
	if response == nil {
		// Empty tranche persisted across the retry bound: skip this input with a
		// zero-unit result and let the run continue rather than crashing the container.
		logger.Log("WARN", fmt.Sprintf("Skipped empty tranche for %s at %s — market sold 0 units; feeder continues", node.Good, marketResult.WaypointSymbol), map[string]interface{}{
			"good":   node.Good,
			"market": marketResult.WaypointSymbol,
		})
		return &ProductionResult{
			QuantityAcquired: 0,
			TotalCost:        0,
			WaypointSymbol:   marketResult.WaypointSymbol,
		}, nil
	}

	logger.Log("INFO", fmt.Sprintf("Purchased %d units of %s for %d credits", response.UnitsAdded, node.Good, response.TotalCost), map[string]interface{}{
		"good":       node.Good,
		"quantity":   response.UnitsAdded,
		"total_cost": response.TotalCost,
		"market":     marketResult.WaypointSymbol,
	})

	return &ProductionResult{
		QuantityAcquired: response.UnitsAdded,
		TotalCost:        response.TotalCost,
		WaypointSymbol:   marketResult.WaypointSymbol,
	}, nil
}

// fabricateGood manufactures a good by producing inputs and delivering them to a manufacturing waypoint
func (e *ProductionExecutor) fabricateGood(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
	opContext *shared.OperationContext, // Operation context for transaction linking
	inputsOnly bool,
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)
	totalCost := 0

	// Step 0: Check if factory already has ABUNDANT supply - skip input production if so
	// This allows opportunistic collection when factory already has goods ready
	factoryMarket, err := e.marketLocator.FindExportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find factory (export market) for %s: %w", node.Good, err)
	}

	// Check current supply at factory
	playerIDValue := shared.MustNewPlayerID(playerID)
	stockedResult, err := e.collectExistingFactorySupply(ctx, ship, node, factoryMarket, playerIDValue, opContext, inputsOnly)
	if err != nil {
		return nil, err
	}
	if stockedResult != nil {
		return stockedResult, nil
	}

	// Step 1: Recursively produce all required inputs
	logger.Log("INFO", fmt.Sprintf("Starting fabrication of %s (requires %d inputs)", node.Good, len(node.Children)), map[string]interface{}{
		"good":        node.Good,
		"input_count": len(node.Children),
	})

	for _, child := range node.Children {
		// Children are inputs that must be harvested and delivered to THIS factory —
		// inputs-only never suppresses their acquisition, so force it off here.
		result, err := e.ProduceGood(ctx, ship, child, systemSymbol, playerID, opContext, false)
		if err != nil {
			return nil, fmt.Errorf("failed to produce input %s: %w", child.Good, err)
		}
		totalCost += result.TotalCost
		logger.Log("INFO", fmt.Sprintf("Produced input: %d units of %s (cost: %d credits)", result.QuantityAcquired, child.Good, result.TotalCost), map[string]interface{}{
			"input_good": child.Good,
			"quantity":   result.QuantityAcquired,
			"cost":       result.TotalCost,
		})
	}

	// Step 2: Navigate to factory (already found above in Step 0)
	// CRITICAL: We need an EXPORT market (factory that produces and sells cheap),
	// NOT an import market (consumer that buys at high price).
	// The factory EXPORTS the finished good (low sell price) and IMPORTS the inputs.

	logger.Log("INFO", fmt.Sprintf("Found factory (export market) for %s at %s", node.Good, factoryMarket.WaypointSymbol), map[string]interface{}{
		"good":       node.Good,
		"waypoint":   factoryMarket.WaypointSymbol,
		"sell_price": factoryMarket.Price, // Factory's sell price (what we pay to buy)
	})

	// Step 3: Navigate to factory and dock (playerIDValue already created in Step 0)
	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), factoryMarket.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to factory: %w", err)
	}

	// Step 4: Deliver all inputs by selling cargo to the factory
	// The factory IMPORTS the inputs (we sell to them)
	deliveryRevenue, err := e.deliverInputs(ctx, updatedShip, playerIDValue, opContext)
	if err != nil {
		return nil, fmt.Errorf("failed to deliver inputs: %w", err)
	}
	totalCost -= deliveryRevenue

	logger.Log("INFO", "Delivered inputs to factory", map[string]interface{}{
		"good":             node.Good,
		"waypoint":         factoryMarket.WaypointSymbol,
		"delivery_revenue": deliveryRevenue,
	})

	// Step 5: Poll for production until output good supply increases, then purchase
	// The factory EXPORTS the finished good (we buy from them at their sell price).
	// In inputs-only mode the poll still confirms the output was produced, but the
	// harvest is skipped so the good is left in factory stock (sp-q02m).
	quantity, cost, err := e.PollForProduction(ctx, node.Good, factoryMarket.WaypointSymbol, updatedShip.ShipSymbol(), playerIDValue, opContext, inputsOnly)
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

func (e *ProductionExecutor) collectExistingFactorySupply(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	factoryMarket *MarketLocatorResult,
	playerIDValue shared.PlayerID,
	opContext *shared.OperationContext,
	inputsOnly bool,
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

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
	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), factoryMarket.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to factory: %w", err)
	}

	// Purchase the goods directly (PollForProduction will find them immediately since supply is HIGH/ABUNDANT).
	// In inputs-only mode the harvest is skipped, so the already-abundant stock is left for construction to source.
	quantity, cost, err := e.PollForProduction(ctx, node.Good, factoryMarket.WaypointSymbol, updatedShip.ShipSymbol(), playerIDValue, opContext, inputsOnly)
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
) (int, int, error) {
	logger := common.LoggerFromContext(ctx)

	// Use configured polling intervals (or defaults if not set)
	intervals := e.pollingIntervals
	if len(intervals) == 0 {
		intervals = []time.Duration{
			30 * time.Second, // Initial poll - catch fast production
			60 * time.Second, // Settled interval
		}
	}

	attempt := 0
	pollStart := e.clock.Now()
	for {
		// Check for context cancellation (daemon stop, user command, etc.)
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

		// Check if good appears in exports
		tradeGood := marketData.FindGood(good)
		if tradeGood != nil {
			logger.Log("INFO", fmt.Sprintf("Production complete: %s now available at %s (polled %d times)", good, waypointSymbol, attempt+1), map[string]interface{}{
				"good":          good,
				"waypoint":      waypointSymbol,
				"poll_attempts": attempt + 1,
				"sell_price":    tradeGood.SellPrice(),
			})

			// Construction-support (inputs-only) mode: production is confirmed and the
			// output now sits in the factory's export stock. Do NOT harvest it — leave
			// it for the construction pipeline to be the sole buyer. Harvesting here is
			// exactly what starved the era-2 gate fill: the factory bought back its own
			// 149 FAB_MATS and froze the fill at 898/1600 for ~6h (sp-q02m).
			if inputsOnly {
				logger.Log("INFO", fmt.Sprintf("inputs-only: %s produced and left in factory stock at %s — harvest skipped", good, waypointSymbol), map[string]interface{}{
					"good":     good,
					"waypoint": waypointSymbol,
				})
				return 0, 0, nil
			}

			return e.purchaseFabricatedOutput(ctx, good, waypointSymbol, shipSymbol, playerID, tradeGood.TradeVolume())
		}

		// Log polling attempt. Past productionDwellWarnThreshold, escalate to a
		// WARNING on EVERY attempt with the elapsed dwell stated in the message
		// text itself (the container-log renderer prints only level+message and
		// drops metadata, sp-iqyq) so a long fabrication wait is observable
		// rather than reading as a silent stall (sp-npyr).
		elapsed := e.clock.Now().Sub(pollStart)
		if elapsed >= productionDwellWarnThreshold {
			logger.Log("WARNING", fmt.Sprintf(
				"Still waiting on %s at %s after %s (ship %s, attempt %d) — fabrication in progress, not stalled",
				good, waypointSymbol, elapsed.Round(time.Second), shipSymbol, attempt+1,
			), map[string]interface{}{
				"good":          good,
				"waypoint":      waypointSymbol,
				"ship":          shipSymbol,
				"attempt":       attempt + 1,
				"elapsed_sec":   elapsed.Seconds(),
				"next_wait_sec": intervals[min(attempt, len(intervals)-1)].Seconds(),
			})
		} else if attempt == 0 || attempt%5 == 0 { // Log every 5th attempt to reduce noise
			logger.Log("INFO", "Polling for production completion", map[string]interface{}{
				"good":          good,
				"waypoint":      waypointSymbol,
				"attempt":       attempt + 1,
				"next_wait_sec": intervals[min(attempt, len(intervals)-1)].Seconds(),
			})
		}

		// Calculate wait interval
		intervalIndex := attempt
		if intervalIndex >= len(intervals) {
			intervalIndex = len(intervals) - 1 // Use last interval for all subsequent attempts
		}
		waitDuration := intervals[intervalIndex]

		// Wait before next poll
		// Create a timer for the wait duration
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

func (e *ProductionExecutor) purchaseFabricatedOutput(
	ctx context.Context,
	good string,
	waypointSymbol string,
	shipSymbol string,
	playerID shared.PlayerID,
	tradeVolume int,
) (int, int, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to reload ship: %w", err)
	}

	availableSpace := ship.Cargo().Capacity - ship.Cargo().Units
	if availableSpace <= 0 {
		// sp-wwhu: sibling of sp-mu6u's crash, on the harvest side — a full hold
		// used to crash the container outright here. We're already docked at this
		// market to harvest, so try to sell whatever is onboard to free space
		// first. Unlike a skipped INPUT purchase, a skipped output harvest loses
		// nothing: the fabricated good stays in the factory's export stock and is
		// picked up on a later pass, so skip gracefully rather than die.
		freedShip, sellErr := e.freeCargoSpace(ctx, ship, playerID)
		if sellErr != nil {
			logger.Log("WARN", fmt.Sprintf("Hold full and could not unload existing cargo — skipping this output harvest of %s", good), map[string]interface{}{
				"good":  good,
				"ship":  ship.ShipSymbol(),
				"error": sellErr.Error(),
			})
			return 0, 0, nil
		}
		ship = freedShip
		availableSpace = ship.Cargo().Capacity - ship.Cargo().Units
		if availableSpace <= 0 {
			logger.Log("WARN", fmt.Sprintf("Hold still full after unloading existing cargo — skipping this output harvest of %s", good), map[string]interface{}{
				"good": good,
				"ship": ship.ShipSymbol(),
			})
			return 0, 0, nil
		}
	}

	purchaseQty := min(availableSpace, tradeVolume)
	if purchaseQty <= 0 {
		return 0, 0, fmt.Errorf("trade volume is zero for %s", good)
	}

	logger.Log("INFO", fmt.Sprintf("Purchasing %d units of fabricated %s (cargo: %d, trade_volume: %d)", purchaseQty, good, availableSpace, tradeVolume), nil)

	purchaseCmd := &shipCargo.PurchaseCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      purchaseQty,
		PlayerID:   playerID,
	}

	// Same dock-retry guard as the raw-buy path: a transient "must be docked"
	// re-docks and retries rather than crashing the container (sp-n7yp).
	response, err := e.purchaseWithDockRetry(ctx, purchaseCmd)
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

// NavigateAndDock navigates to a waypoint and returns the ship only once it is
// CONFIRMED docked — the dock is actually persisted via the API, not merely
// flipped to DOCKED in memory.
//
// The previous implementation pre-mutated the reloaded ship with EnsureDocked in
// its arrival poll, then handed that already-DOCKED ship to DockShipCommand. That
// made the dock a no-op: runStateTransition sees EnsureDocked report "no change"
// and short-circuits before calling the API, so the ship stayed IN_ORBIT in the
// DB while the code believed it was docked. The very next PurchaseCargoCommand
// reloaded IN_ORBIT and crashed the container with "ship must be docked"
// (sp-n7yp feeder crash #3). We now detect arrival WITHOUT mutating the ship and
// dock via a symbol-only command so the handler reloads the real IN_ORBIT state
// and the API dock actually fires, then re-read and assert DOCKED before
// returning.
func (e *ProductionExecutor) NavigateAndDock(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	navigateCmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    playerID,
	}
	if _, err := e.mediator.Send(ctx, navigateCmd); err != nil {
		return nil, fmt.Errorf("failed to navigate to %s: %w", destination, err)
	}

	return e.dockAndConfirm(ctx, shipSymbol, destination, playerID)
}

// dockAndConfirm waits for the ship to arrive, issues a real (API-backed) dock,
// and returns only after re-reading a persisted DOCKED state. Bounded by
// productionDockConfirmAttempts so a wedged ship can never spin forever.
//
// Critically, it never acts on a ship it mutated in memory: each attempt reloads
// a fresh ship, and the dock is issued via a symbol-only DockShipCommand so the
// handler loads the true (IN_ORBIT) state and EnsureDocked reports a real change
// — otherwise the dock short-circuits to a no-op and the buy races an unpersisted
// dock (sp-n7yp).
func (e *ProductionExecutor) dockAndConfirm(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	var ship *navigation.Ship
	for attempt := 0; attempt < productionDockConfirmAttempts; attempt++ {
		reloaded, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to reload ship after navigation: %w", err)
		}
		ship = reloaded

		if ship.IsDocked() {
			return ship, nil // confirmed: persisted DOCKED
		}

		if ship.IsInTransit() {
			// Still travelling — wait for arrival, then re-read.
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("dock wait cancelled: %w", ctx.Err())
			default:
				e.clock.Sleep(1 * time.Second)
			}
			continue
		}

		// Arrived and in orbit: issue a real dock. Pass ShipSymbol (nil Ship) so
		// DockShipHandler loads the true IN_ORBIT state, EnsureDocked reports a
		// change, and the API dock actually fires + persists.
		if _, err := e.mediator.Send(ctx, &shipTypes.DockShipCommand{
			ShipSymbol: shipSymbol,
			PlayerID:   playerID,
		}); err != nil {
			return nil, fmt.Errorf("failed to dock ship %s: %w", shipSymbol, err)
		}
		// Honor cancellation between issuing the dock and re-reading; loop back
		// immediately to confirm the persisted state (no mandatory sleep on the
		// happy path).
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("dock wait cancelled: %w", err)
		}
	}

	if ship != nil && ship.IsInTransit() {
		return nil, fmt.Errorf("ship %s still in transit after %d attempts", shipSymbol, productionDockConfirmAttempts)
	}
	return nil, fmt.Errorf("ship %s did not reach a confirmed DOCKED state at %s after %d attempts", shipSymbol, destination, productionDockConfirmAttempts)
}

// isTransientDockStateError reports whether err is the recoverable "ship must be
// docked" signal — the local precondition error (cargo_transaction.go) or the
// API's 4214/4244 codes — rather than a genuine failure (insufficient funds, no
// cargo space, ...). Only these are safe to retry after re-docking.
func isTransientDockStateError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "must be docked") ||
		strings.Contains(msg, "4214") ||
		strings.Contains(msg, "4244")
}

// isEmptyTrancheError reports whether err is the "bought nothing" signal from an
// input buy — the cargo handler's "partial failure: ... 0 units processed" wrapper
// (cargo_transaction.go), raised when the first tranche's API call fails because the
// market's supply was drained between the scout read and the buy (an empty /
// zero-volume tranche, surfaced by the API as a 400).
//
// A genuine funds shortfall also processes zero units, so it too carries that phrase
// — but it is NOT an empty tranche and must surface as a real failure (mirroring how
// this file treats insufficient funds elsewhere). We therefore explicitly exclude it,
// so only a truly empty/zero-volume tranche is eligible for retry-then-skip
// (sp-q02m feeder crash #4).
func isEmptyTrancheError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if !strings.Contains(msg, "0 units processed") {
		return false
	}
	if strings.Contains(msg, "insufficient") {
		return false // genuine funds failure — must surface, never be silently skipped
	}
	return true
}

// purchaseWithDockRetry dispatches a PurchaseCargoCommand and, if it fails with a
// transient dock-state signal, reconciles the ship from the API (clearing any
// stale DOCKED cache entry that would make a re-dock a no-op — the subtlety
// NegotiateContractHandler documents), re-docks, and retries. Bounded by
// productionDockRetryLimit. A transient dock state must never crash the container
// (sp-n7yp feeder crash #3); genuine failures surface immediately, unretried.
func (e *ProductionExecutor) purchaseWithDockRetry(
	ctx context.Context,
	cmd *shipCargo.PurchaseCargoCommand,
) (*shipCargo.PurchaseCargoResponse, error) {
	logger := common.LoggerFromContext(ctx)
	var lastErr error
	for attempt := 0; attempt <= productionDockRetryLimit; attempt++ {
		resp, err := e.mediator.Send(ctx, cmd)
		if err == nil {
			response, ok := resp.(*shipCargo.PurchaseCargoResponse)
			if !ok {
				return nil, fmt.Errorf("unexpected response type from purchase command")
			}
			return response, nil
		}

		if !isTransientDockStateError(err) {
			return nil, err // genuine failure — surface immediately
		}

		lastErr = err
		if attempt == productionDockRetryLimit {
			break
		}

		logger.Log("WARN", "Purchase hit a transient dock-state error; re-docking and retrying", map[string]interface{}{
			"ship":    cmd.ShipSymbol,
			"good":    cmd.GoodSymbol,
			"attempt": attempt + 1,
			"error":   err.Error(),
		})
		if rerr := e.redockFromAPI(ctx, cmd.ShipSymbol, cmd.PlayerID); rerr != nil {
			return nil, fmt.Errorf("failed to re-dock after transient dock error: %w", rerr)
		}
	}

	return nil, fmt.Errorf("purchase still failing after %d dock retries: %w", productionDockRetryLimit, lastErr)
}

// purchaseInputWithEmptyTrancheGuard dispatches an input buy and survives an empty /
// zero-volume tranche instead of crashing the container (sp-q02m feeder crash #4).
//
// Dock-state transients are still absorbed by the inner purchaseWithDockRetry. If the
// buy comes back empty ("partial failure: ... 0 units processed" / API 400 — the
// market drained between the scout read and the buy), we bounded-retry in case the
// supply refills, then report a SKIP so the caller can continue with a zero-unit
// result rather than dying unrecoverably. Genuine failures (insufficient funds,
// no cargo space, exhausted dock retries) surface immediately.
//
// Returns:
//   - (resp, nil): a successful buy
//   - (nil,  nil): the tranche stayed empty across the retry bound — SKIP and continue
//   - (nil,  err): a genuine failure
func (e *ProductionExecutor) purchaseInputWithEmptyTrancheGuard(
	ctx context.Context,
	cmd *shipCargo.PurchaseCargoCommand,
) (*shipCargo.PurchaseCargoResponse, error) {
	logger := common.LoggerFromContext(ctx)
	var lastErr error
	for attempt := 0; attempt <= productionEmptyTrancheRetryLimit; attempt++ {
		// Honour container shutdown between attempts.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, err := e.purchaseWithDockRetry(ctx, cmd)
		if err == nil {
			return resp, nil
		}
		if !isEmptyTrancheError(err) {
			return nil, err // genuine failure — surface immediately, unretried
		}

		lastErr = err
		if attempt == productionEmptyTrancheRetryLimit {
			break
		}

		logger.Log("WARN", "Input buy hit an empty/zero-volume tranche; retrying in case supply refills", map[string]interface{}{
			"ship":    cmd.ShipSymbol,
			"good":    cmd.GoodSymbol,
			"attempt": attempt + 1,
			"error":   err.Error(),
		})
		e.clock.Sleep(productionEmptyTrancheRetryDelay)
	}

	// The tranche stayed empty across the bound: report a skip so the feeder survives
	// (a permanently-empty market must not crash the container or infinite-loop).
	logger.Log("WARN", "Input tranche still empty after bounded retries — skipping to keep the feeder alive", map[string]interface{}{
		"ship":    cmd.ShipSymbol,
		"good":    cmd.GoodSymbol,
		"retries": productionEmptyTrancheRetryLimit,
		"error":   lastErr.Error(),
	})
	return nil, nil
}

// redockFromAPI reconciles the ship against the server (SyncShipFromAPI) so a
// stale DOCKED cache entry cannot make EnsureDocked a no-op, then issues a real
// dock via a symbol-only command. Mirrors the reactive re-dock in
// NegotiateContractHandler.
func (e *ProductionExecutor) redockFromAPI(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
) error {
	if _, err := e.shipRepo.SyncShipFromAPI(ctx, shipSymbol, playerID); err != nil {
		return fmt.Errorf("failed to refresh ship %s from API: %w", shipSymbol, err)
	}
	if _, err := e.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
	}); err != nil {
		return fmt.Errorf("failed to dock ship %s: %w", shipSymbol, err)
	}
	return nil
}

// deliverInputs sells all cargo (inputs) at the current location
func (e *ProductionExecutor) deliverInputs(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (int, error) {
	logger := common.LoggerFromContext(ctx)
	totalRevenue := 0

	// Sell each cargo item
	for _, item := range ship.Cargo().Inventory {
		sellCmd := &shipCargo.SellCargoCommand{
			ShipSymbol: ship.ShipSymbol(),
			GoodSymbol: item.Symbol,
			Units:      item.Units,
			PlayerID:   playerID,
		}

		sellResp, err := e.mediator.Send(ctx, sellCmd)
		if err != nil {
			return 0, fmt.Errorf("failed to sell %s: %w", item.Symbol, err)
		}

		response, ok := sellResp.(*shipCargo.SellCargoResponse)
		if !ok {
			return 0, fmt.Errorf("unexpected response type from sell command")
		}

		totalRevenue += response.TotalRevenue

		logger.Log("INFO", fmt.Sprintf("Delivered input: %d units of %s (revenue: %d credits)", response.UnitsSold, item.Symbol, response.TotalRevenue), map[string]interface{}{
			"input_good": item.Symbol,
			"units":      response.UnitsSold,
			"revenue":    response.TotalRevenue,
		})
	}

	return totalRevenue, nil
}

// freeCargoSpace sells whatever is currently in the ship's hold at its current
// docked market so a full hold does not block an input purchase (sp-mu6u).
// Unlike deliverInputs (which hard-fails on the first item this market won't
// buy), this is best-effort: an item this market doesn't import is skipped
// rather than aborting the whole attempt, since the goal here is only to make
// room, not to guarantee every item sells. Returns the reloaded ship
// reflecting whatever did sell.
func (e *ProductionExecutor) freeCargoSpace(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	logger := common.LoggerFromContext(ctx)

	if ship.Cargo().IsEmpty() {
		return nil, fmt.Errorf("hold reports full but carries no inventory (capacity %d) — nothing to unload", ship.Cargo().Capacity)
	}

	sold := 0
	for _, item := range ship.Cargo().Inventory {
		sellCmd := &shipCargo.SellCargoCommand{
			ShipSymbol: ship.ShipSymbol(),
			GoodSymbol: item.Symbol,
			Units:      item.Units,
			PlayerID:   playerID,
		}
		resp, err := e.mediator.Send(ctx, sellCmd)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Could not unload %s to free cargo space — market may not import it", item.Symbol), map[string]interface{}{
				"good":  item.Symbol,
				"ship":  ship.ShipSymbol(),
				"error": err.Error(),
			})
			continue
		}
		response, ok := resp.(*shipCargo.SellCargoResponse)
		if !ok {
			continue
		}
		sold += response.UnitsSold
		logger.Log("INFO", fmt.Sprintf("Unloaded %d units of %s to free cargo space", response.UnitsSold, item.Symbol), map[string]interface{}{
			"good":     item.Symbol,
			"quantity": response.UnitsSold,
			"revenue":  response.TotalRevenue,
		})
	}

	if sold == 0 {
		return nil, fmt.Errorf("market would not buy any of the %d onboard item(s)", len(ship.Cargo().Inventory))
	}

	reloaded, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship after unloading cargo: %w", err)
	}
	return reloaded, nil
}

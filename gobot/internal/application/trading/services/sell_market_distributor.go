package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// SellMarketDistributor selects sell markets in a distributed fashion to avoid flooding.
// When multiple ships collect the same good, this service ensures they sell to different
// markets (SCARCE/LIMITED supply) to prevent crashing prices at a single location.
//
// Distribution Strategy:
//  1. Find ALL eligible sell markets for the good (IMPORT type, SCARCE/LIMITED supply)
//  2. Count pending COLLECT_SELL tasks per market
//  3. Select the market with the fewest pending tasks
//  4. Tie-breaker: prefer SCARCE over LIMITED, then higher purchase price
type SellMarketDistributor struct {
	marketRepo market.MarketRepository
	taskRepo   manufacturing.TaskRepository
}

// NewSellMarketDistributor creates a new sell market distributor
func NewSellMarketDistributor(
	marketRepo market.MarketRepository,
	taskRepo manufacturing.TaskRepository,
) *SellMarketDistributor {
	return &SellMarketDistributor{
		marketRepo: marketRepo,
		taskRepo:   taskRepo,
	}
}

// EligibleMarket represents a potential sell market with its metrics
type EligibleMarket struct {
	WaypointSymbol string
	PurchasePrice  int    // What the market pays us
	Supply         string // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
	Activity       string // WEAK, GROWING, STRONG, RESTRICTED
	PendingTasks   int    // Number of pending COLLECT_SELL tasks for this market
}

// SelectSellMarket finds the best sell market for a good, distributing across multiple markets.
//
// Parameters:
//   - good: The good to sell (e.g., "SHIP_PARTS")
//   - factorySymbol: The factory collecting from (for logging)
//   - systemSymbol: The system to search in
//   - playerID: The player identifier
//   - fallbackMarket: Market to use if no eligible markets found (pipeline's original sell market)
//
// Returns the waypoint symbol of the selected sell market.
func (d *SellMarketDistributor) SelectSellMarket(
	ctx context.Context,
	good string,
	factorySymbol string,
	systemSymbol string,
	playerID int,
	fallbackMarket string,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	// Step 1: Find all eligible sell markets
	eligibleMarkets, err := d.findEligibleSellMarkets(ctx, good, systemSymbol, playerID)
	if err != nil {
		logger.Log("WARN", "Failed to find eligible sell markets, using fallback", map[string]interface{}{
			"good":     good,
			"fallback": fallbackMarket,
			"error":    err.Error(),
		})
		return fallbackMarket, nil
	}

	if len(eligibleMarkets) == 0 {
		logger.Log("DEBUG", "No eligible sell markets found, using fallback", map[string]interface{}{
			"good":     good,
			"fallback": fallbackMarket,
		})
		return fallbackMarket, nil
	}

	// Step 2: Count pending tasks per market
	if d.taskRepo != nil {
		d.countPendingTasksPerMarket(ctx, eligibleMarkets, good, playerID)
	}

	// Step 3: Select the best market (fewest pending tasks, then supply, then price)
	selectedMarket := d.selectBestMarket(eligibleMarkets)

	logger.Log("INFO", "Selected sell market for distribution", map[string]interface{}{
		"good":            good,
		"factory":         factorySymbol,
		"selected_market": selectedMarket.WaypointSymbol,
		"supply":          selectedMarket.Supply,
		"pending_tasks":   selectedMarket.PendingTasks,
		"purchase_price":  selectedMarket.PurchasePrice,
		"eligible_count":  len(eligibleMarkets),
	})

	return selectedMarket.WaypointSymbol, nil
}

// findEligibleSellMarkets finds all markets that are eligible sell destinations.
// Eligible markets: NOT EXPORT type (exclude factories), SCARCE or LIMITED supply, WEAK or RESTRICTED activity.
//
// Trade type logic:
//   - EXPORT = factory that produces the good (we buy from them, NOT sell)
//   - IMPORT = market that consumes the good (ideal sell destination)
//   - EXCHANGE = market that trades both ways (acceptable sell destination)
//
// For final goods (SHIP_PARTS, etc.), we allow IMPORT and EXCHANGE markets.
// We only exclude EXPORT markets (factories) since they produce the good and don't need more.
func (d *SellMarketDistributor) findEligibleSellMarkets(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) ([]*EligibleMarket, error) {
	// Get all markets in the system
	marketWaypoints, err := d.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets: %w", err)
	}

	var eligible []*EligibleMarket

	for _, waypointSymbol := range marketWaypoints {
		marketData, err := d.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}

		tradeGood := marketData.FindGood(good)
		if tradeGood == nil {
			continue
		}

		// Exclude EXPORT markets (factories that produce this good)
		// They already have supply and won't pay good prices
		// Allow IMPORT (consumers) and EXCHANGE (traders) markets
		if tradeGood.TradeType() == market.TradeTypeExport {
			continue
		}

		// Extract supply and activity
		supply := ""
		if tradeGood.Supply() != nil {
			supply = *tradeGood.Supply()
		}
		activity := ""
		if tradeGood.Activity() != nil {
			activity = *tradeGood.Activity()
		}

		// Filter: Only SCARCE or LIMITED supply (markets that need goods)
		if supply != "SCARCE" && supply != "LIMITED" {
			continue
		}

		// Filter: Only WEAK or RESTRICTED activity (stable prices)
		if activity != "WEAK" && activity != "RESTRICTED" {
			continue
		}

		eligible = append(eligible, &EligibleMarket{
			WaypointSymbol: waypointSymbol,
			PurchasePrice:  tradeGood.PurchasePrice(),
			Supply:         supply,
			Activity:       activity,
			PendingTasks:   0, // Will be filled in next step
		})
	}

	return eligible, nil
}

// countPendingTasksPerMarket queries the task repo and counts pending COLLECT_SELL tasks per market.
// Updates the PendingTasks field of each eligible market in-place.
func (d *SellMarketDistributor) countPendingTasksPerMarket(
	ctx context.Context,
	markets []*EligibleMarket,
	good string,
	playerID int,
) {
	// Get all incomplete tasks for the player
	tasks, err := d.taskRepo.FindIncomplete(ctx, playerID)
	if err != nil {
		return // Can't count, leave all at 0
	}

	// Build a map for quick lookup
	marketMap := make(map[string]*EligibleMarket)
	for _, m := range markets {
		marketMap[m.WaypointSymbol] = m
	}

	// Count COLLECT_SELL tasks per target market
	for _, task := range tasks {
		if task.TaskType() != manufacturing.TaskTypeCollectSell {
			continue
		}
		if task.Good() != good {
			continue
		}
		// Only count PENDING, READY, ASSIGNED, EXECUTING tasks (not COMPLETED/FAILED)
		status := task.Status()
		if status == manufacturing.TaskStatusCompleted || status == manufacturing.TaskStatusFailed {
			continue
		}

		targetMarket := task.TargetMarket()
		if m, exists := marketMap[targetMarket]; exists {
			m.PendingTasks++
		}
	}
}

// selectBestMarket selects the best market from eligible options.
// Priority: 1) Fewest pending tasks, 2) SCARCE > LIMITED, 3) Highest purchase price
func (d *SellMarketDistributor) selectBestMarket(markets []*EligibleMarket) *EligibleMarket {
	if len(markets) == 0 {
		return nil
	}

	best := markets[0]
	for _, m := range markets[1:] {
		// Primary: fewer pending tasks wins
		if m.PendingTasks < best.PendingTasks {
			best = m
			continue
		}
		if m.PendingTasks > best.PendingTasks {
			continue
		}

		// Secondary: SCARCE > LIMITED (SCARCE markets pay more)
		if m.Supply == "SCARCE" && best.Supply == "LIMITED" {
			best = m
			continue
		}
		if m.Supply == "LIMITED" && best.Supply == "SCARCE" {
			continue
		}

		// Tertiary: higher purchase price wins
		if m.PurchasePrice > best.PurchasePrice {
			best = m
		}
	}

	return best
}

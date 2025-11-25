package services

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// ArbitrageOpportunityFinder orchestrates market scanning and opportunity discovery.
// This is an application service that coordinates infrastructure (repositories) with domain logic (analyzer).
type ArbitrageOpportunityFinder struct {
	marketRepo       market.MarketRepository
	waypointProvider system.IWaypointProvider
	analyzer         *trading.ArbitrageAnalyzer
	blacklistedGoods map[string]bool // Goods to exclude from opportunities
}

// DefaultBlacklistedGoods contains goods that should not be traded
// Currently empty - the scoring algorithm should naturally filter bad opportunities.
// Previous blacklist (AMMUNITION, ELECTRONICS, GOLD) removed in favor of smarter
// scoring that uses supply/activity data to avoid high-risk trades.
var DefaultBlacklistedGoods = []string{
	// Empty - let the algorithm decide based on market conditions
}

// NewArbitrageOpportunityFinder creates a new opportunity finder service
func NewArbitrageOpportunityFinder(
	marketRepo market.MarketRepository,
	waypointProvider system.IWaypointProvider,
	analyzer *trading.ArbitrageAnalyzer,
) *ArbitrageOpportunityFinder {
	// Build blacklist map from defaults
	blacklist := make(map[string]bool)
	for _, good := range DefaultBlacklistedGoods {
		blacklist[good] = true
	}

	return &ArbitrageOpportunityFinder{
		marketRepo:       marketRepo,
		waypointProvider: waypointProvider,
		analyzer:         analyzer,
		blacklistedGoods: blacklist,
	}
}

// FindOpportunities scans all markets in a system for arbitrage opportunities.
//
// Algorithm:
//  1. Get all markets in the system
//  2. Load market data for each waypoint
//  3. Build index: good → {buyers, sellers}
//  4. Analyze all buy/sell pairs for each good
//  5. Filter by minimum margin
//  6. Score and sort by composite score
//  7. Return top N opportunities
//
// Parameters:
//   - ctx: Context for cancellation
//   - systemSymbol: System to scan (e.g., "X1-AU21")
//   - playerID: Player identifier
//   - cargoCapacity: Ship cargo capacity for profit calculations
//   - minMargin: Minimum profit margin threshold (e.g., 10.0 for 10%)
//   - limit: Maximum number of opportunities to return
//
// Returns:
//   - List of opportunities sorted by score (descending)
//   - Error if scanning fails
func (f *ArbitrageOpportunityFinder) FindOpportunities(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	cargoCapacity int,
	minMargin float64,
	limit int,
) ([]*trading.ArbitrageOpportunity, error) {
	// Validate inputs
	if systemSymbol == "" {
		return nil, fmt.Errorf("system symbol required")
	}
	if cargoCapacity <= 0 {
		return nil, trading.ErrInvalidCargoCapacity
	}
	if minMargin <= 0 {
		return nil, trading.ErrInvalidMarginThreshold
	}

	// Step 1: Get all markets in system
	marketWaypoints, err := f.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch markets: %w", err)
	}

	if len(marketWaypoints) == 0 {
		return nil, trading.ErrNoOpportunitiesFound
	}

	// Step 2: Load market data and waypoint data for all markets
	markets := make(map[string]*market.Market)
	waypoints := make(map[string]*shared.Waypoint)

	for _, waypointSymbol := range marketWaypoints {
		// Load market data
		m, err := f.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil {
			// Skip markets with errors (may be temporarily unavailable)
			continue
		}
		if m == nil {
			continue
		}
		markets[waypointSymbol] = m

		// Load waypoint data
		wp, err := f.waypointProvider.GetWaypoint(ctx, waypointSymbol, systemSymbol, playerID)
		if err != nil {
			// Skip waypoints with errors
			continue
		}
		waypoints[waypointSymbol] = wp
	}

	if len(markets) < 2 {
		return nil, trading.ErrNoOpportunitiesFound
	}

	// Step 3: Build good→markets index
	type marketPair struct {
		market   *market.Market
		waypoint *shared.Waypoint
	}

	goodsMap := make(map[string]struct {
		buyers  []marketPair // Markets buying this good (imports)
		sellers []marketPair // Markets selling this good (exports)
	})

	for waypointSymbol, m := range markets {
		wp := waypoints[waypointSymbol]
		if wp == nil || m == nil {
			continue
		}

		for _, tradeGood := range m.TradeGoods() {
			goodSymbol := tradeGood.Symbol()
			entry := goodsMap[goodSymbol]

			// Check if market sells this good (we can buy from it)
			// Markets that export goods have them available for sale
			if tradeGood.SellPrice() > 0 {
				entry.sellers = append(entry.sellers, marketPair{
					market:   m,
					waypoint: wp,
				})
			}

			// Check if market buys this good (we can sell to it)
			// Markets that import goods will purchase them
			if tradeGood.PurchasePrice() > 0 {
				entry.buyers = append(entry.buyers, marketPair{
					market:   m,
					waypoint: wp,
				})
			}

			goodsMap[goodSymbol] = entry
		}
	}

	// Step 4: Analyze all buy/sell pairs
	opportunities := []*trading.ArbitrageOpportunity{}

	for good, markets := range goodsMap {
		// Skip blacklisted goods
		if f.blacklistedGoods[good] {
			continue
		}
		// For each good, try all combinations of (buy, sell) pairs
		// Note: markets.sellers = markets we BUY from (they sell to us)
		//       markets.buyers = markets we SELL to (they buy from us)
		for _, buyMarket := range markets.sellers {
			for _, sellMarket := range markets.buyers {
				// Don't trade with same market
				if buyMarket.waypoint.Symbol == sellMarket.waypoint.Symbol {
					continue
				}

				// Get trade goods
				buyGood := buyMarket.market.FindGood(good)
				sellGood := sellMarket.market.FindGood(good)

				if buyGood == nil || sellGood == nil {
					continue
				}

				// Analyze pair using domain service
				opp, err := f.analyzer.AnalyzeMarketPair(
					good,
					buyMarket.market,
					buyGood,
					sellMarket.market,
					sellGood,
					buyMarket.waypoint,
					sellMarket.waypoint,
					cargoCapacity,
					minMargin,
				)

				if err != nil {
					// Skip non-viable opportunities
					continue
				}

				// Only include viable opportunities
				if opp.IsViable() {
					opportunities = append(opportunities, opp)
				}
			}
		}
	}

	// Step 5: Check if we found any opportunities
	if len(opportunities) == 0 {
		return nil, trading.ErrNoOpportunitiesFound
	}

	// Step 6: Sort by score descending (higher score = better opportunity)
	sort.Slice(opportunities, func(i, j int) bool {
		return opportunities[i].Score() > opportunities[j].Score()
	})

	// Step 7: Limit results
	if len(opportunities) > limit {
		opportunities = opportunities[:limit]
	}

	return opportunities, nil
}

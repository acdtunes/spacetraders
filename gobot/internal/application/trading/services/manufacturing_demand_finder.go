package services

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"

	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
)

// ManufacturingDemandFinder discovers high-demand goods that can be manufactured.
// It scans import markets for goods with high purchase prices and filters to
// only those that can be fabricated via the supply chain.
type ManufacturingDemandFinder struct {
	marketRepo       market.MarketRepository
	waypointProvider system.IWaypointProvider
	supplyChainMap   map[string][]string
	resolver         *goodsServices.SupplyChainResolver
}

// NewManufacturingDemandFinder creates a new demand finder service
func NewManufacturingDemandFinder(
	marketRepo market.MarketRepository,
	waypointProvider system.IWaypointProvider,
	supplyChainMap map[string][]string,
	resolver *goodsServices.SupplyChainResolver,
) *ManufacturingDemandFinder {
	return &ManufacturingDemandFinder{
		marketRepo:       marketRepo,
		waypointProvider: waypointProvider,
		supplyChainMap:   supplyChainMap,
		resolver:         resolver,
	}
}

// DemandFinderConfig contains configuration for demand discovery
type DemandFinderConfig struct {
	MinPurchasePrice int // Minimum price to consider (default: 1000)
	MaxOpportunities int // Max opportunities to return (default: 10)
}

// DefaultDemandFinderConfig returns sensible defaults
func DefaultDemandFinderConfig() DemandFinderConfig {
	return DemandFinderConfig{
		MinPurchasePrice: 1000,
		MaxOpportunities: 10,
	}
}

// FindHighDemandManufacturables scans all markets for high-demand goods that can be manufactured.
//
// Algorithm:
//  1. Get all markets in the system
//  2. For each market, check imported goods (high purchase prices = demand)
//  3. Filter: only goods that exist in supply chain map (manufacturable)
//  4. Filter: purchase price >= minimum threshold
//  5. Build dependency tree for each opportunity
//  6. Sort by purchase price (descending)
//  7. Return top N opportunities
//
// Parameters:
//   - ctx: Context for cancellation
//   - systemSymbol: System to scan (e.g., "X1-AU21")
//   - playerID: Player identifier
//   - config: Configuration for filtering and limits
//
// Returns:
//   - List of opportunities sorted by purchase price (descending)
//   - Error if scanning fails
func (f *ManufacturingDemandFinder) FindHighDemandManufacturables(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	config DemandFinderConfig,
) ([]*trading.ManufacturingOpportunity, error) {
	// Validate inputs
	if systemSymbol == "" {
		return nil, fmt.Errorf("system symbol required")
	}

	// Apply defaults if not set
	if config.MinPurchasePrice <= 0 {
		config.MinPurchasePrice = 1000
	}
	if config.MaxOpportunities <= 0 {
		config.MaxOpportunities = 10
	}

	// Step 1: Get all markets in system
	marketWaypoints, err := f.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch markets: %w", err)
	}

	if len(marketWaypoints) == 0 {
		return nil, nil // No markets, no opportunities
	}

	// Step 2: Build index of high-demand goods
	// Map: good -> best import opportunity (highest purchase price)
	type demandEntry struct {
		good           string
		waypointSymbol string
		purchasePrice  int
		activity       string
		supply         string
	}
	demandIndex := make(map[string]*demandEntry)

	for _, waypointSymbol := range marketWaypoints {
		marketData, err := f.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil {
			continue // Skip markets we can't access
		}

		// Check each trade good
		for _, tradeGood := range marketData.TradeGoods() {
			goodSymbol := tradeGood.Symbol()
			purchasePrice := tradeGood.PurchasePrice()

			// Skip if price below threshold
			if purchasePrice < config.MinPurchasePrice {
				continue
			}

			// Skip if not manufacturable
			if !f.isManufacturable(goodSymbol) {
				continue
			}

			// Extract activity and supply (may be nil)
			activity := ""
			if tradeGood.Activity() != nil {
				activity = *tradeGood.Activity()
			}
			supply := ""
			if tradeGood.Supply() != nil {
				supply = *tradeGood.Supply()
			}

			// Track best price for this good
			existing := demandIndex[goodSymbol]
			if existing == nil || purchasePrice > existing.purchasePrice {
				demandIndex[goodSymbol] = &demandEntry{
					good:           goodSymbol,
					waypointSymbol: waypointSymbol,
					purchasePrice:  purchasePrice,
					activity:       activity,
					supply:         supply,
				}
			}
		}
	}

	// Step 3: Build opportunities with dependency trees
	var opportunities []*trading.ManufacturingOpportunity

	for _, entry := range demandIndex {
		// Get waypoint for the sell market
		sellMarket, err := f.waypointProvider.GetWaypoint(ctx, entry.waypointSymbol, systemSymbol, playerID)
		if err != nil {
			continue // Skip if waypoint lookup fails
		}

		// Build dependency tree
		tree, err := f.resolver.BuildDependencyTree(ctx, entry.good, systemSymbol, playerID)
		if err != nil {
			continue // Skip if tree building fails (circular deps, unknown goods)
		}

		// Create opportunity
		opp, err := trading.NewManufacturingOpportunity(
			entry.good,
			sellMarket,
			entry.purchasePrice,
			entry.activity,
			entry.supply,
			tree,
		)
		if err != nil {
			continue // Skip invalid opportunities
		}

		opportunities = append(opportunities, opp)
	}

	// Step 4: Sort by composite score (descending)
	// Score factors: price (40%), activity (30%), supply (20%), depth (10%)
	sort.Slice(opportunities, func(i, j int) bool {
		return opportunities[i].Score() > opportunities[j].Score()
	})

	// Step 5: Limit results
	if len(opportunities) > config.MaxOpportunities {
		opportunities = opportunities[:config.MaxOpportunities]
	}

	return opportunities, nil
}

// isManufacturable checks if a good can be fabricated via the supply chain
func (f *ManufacturingDemandFinder) isManufacturable(good string) bool {
	_, exists := f.supplyChainMap[good]
	return exists
}

// GetManufacturableGoods returns all goods that can be manufactured
func (f *ManufacturingDemandFinder) GetManufacturableGoods() []string {
	result := make([]string, 0, len(f.supplyChainMap))
	for good := range f.supplyChainMap {
		result = append(result, good)
	}
	sort.Strings(result)
	return result
}

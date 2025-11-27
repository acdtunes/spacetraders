package services

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
	pipelineRepo     manufacturing.PipelineRepository
}

// NewManufacturingDemandFinder creates a new demand finder service
func NewManufacturingDemandFinder(
	marketRepo market.MarketRepository,
	waypointProvider system.IWaypointProvider,
	supplyChainMap map[string][]string,
	resolver *goodsServices.SupplyChainResolver,
	pipelineRepo manufacturing.PipelineRepository,
) *ManufacturingDemandFinder {
	return &ManufacturingDemandFinder{
		marketRepo:       marketRepo,
		waypointProvider: waypointProvider,
		supplyChainMap:   supplyChainMap,
		resolver:         resolver,
		pipelineRepo:     pipelineRepo,
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
	// Map: good -> ALL eligible import markets (we'll select based on distance later)
	type demandEntry struct {
		good           string
		waypointSymbol string
		purchasePrice  int
		activity       string
		supply         string
	}
	demandIndex := make(map[string][]*demandEntry) // Changed to slice to track ALL markets

	for _, waypointSymbol := range marketWaypoints {
		marketData, err := f.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil {
			continue // Skip markets we can't access
		}
		if marketData == nil {
			continue // No market data for this waypoint
		}

		// Check each trade good
		for _, tradeGood := range marketData.TradeGoods() {
			goodSymbol := tradeGood.Symbol()
			purchasePrice := tradeGood.PurchasePrice()

			// CRITICAL: Only consider IMPORT goods - these are markets that BUY/CONSUME
			// EXPORT markets sell goods (factories), EXCHANGE markets trade both ways
			// We want to sell to markets that NEED the goods (IMPORT type)
			if tradeGood.TradeType() != market.TradeTypeImport {
				continue // Skip export and exchange markets
			}

			// Skip if price below threshold
			if purchasePrice < config.MinPurchasePrice {
				continue
			}

			// NOTE: We no longer filter by isManufacturable here!
			// Direct arbitrage doesn't require manufacturing - just HIGH/ABUNDANT source
			// The supply chain resolver will handle both cases:
			// - HIGH/ABUNDANT source → AcquisitionBuy (direct arbitrage)
			// - Below HIGH → AcquisitionFabricate (needs manufacturing)

			// Extract activity and supply (may be nil)
			activity := ""
			if tradeGood.Activity() != nil {
				activity = *tradeGood.Activity()
			}
			supply := ""
			if tradeGood.Supply() != nil {
				supply = *tradeGood.Supply()
			}

			// CRITICAL: Only consider markets with WEAK or RESTRICTED activity
			// See docs/PARALLEL_MANUFACTURING_SYSTEM_DESIGN.md - Sell Market Selection
			// STRONG (20.3% drift) and GROWING (33.6% drift) are too volatile
			if activity != "WEAK" && activity != "RESTRICTED" {
				continue // Skip volatile markets
			}

			// CRITICAL: Only sell to markets with SCARCE or LIMITED supply
			// Symmetry with BUY logic (HIGH/ABUNDANT for buying)
			// Markets that need goods (low supply) pay higher prices
			if supply != "SCARCE" && supply != "LIMITED" {
				continue // Skip markets that already have enough supply
			}

			// Track ALL eligible markets for this good (we'll select by distance later)
			demandIndex[goodSymbol] = append(demandIndex[goodSymbol], &demandEntry{
				good:           goodSymbol,
				waypointSymbol: waypointSymbol,
				purchasePrice:  purchasePrice,
				activity:       activity,
				supply:         supply,
			})
		}
	}

	// Step 3: Build opportunities with dependency trees
	// For each good, select the CLOSEST sell market to minimize cycle time
	var opportunities []*trading.ManufacturingOpportunity

	for good, entries := range demandIndex {
		if len(entries) == 0 {
			continue
		}

		// CRITICAL: Skip goods that already have an active pipeline
		// This prevents duplicate pipelines for the same product
		if f.pipelineRepo != nil {
			existingPipeline, err := f.pipelineRepo.FindActiveForProduct(ctx, playerID, good)
			if err == nil && existingPipeline != nil {
				continue // Already have an active pipeline for this good
			}
		}

		// Build dependency tree first to find factory location
		tree, err := f.resolver.BuildDependencyTree(ctx, good, systemSymbol, playerID)
		if err != nil {
			continue // Skip if tree building fails (circular deps, unknown goods)
		}

		// Find factory waypoint (export market for this good)
		factoryWaypoint, err := f.findFactoryWaypoint(ctx, good, systemSymbol, playerID)
		if err != nil {
			continue // Skip if no factory found
		}

		// Select the closest sell market to minimize cycle time
		var bestEntry *demandEntry
		var bestDistance float64 = -1

		for _, entry := range entries {
			sellWaypoint, err := f.waypointProvider.GetWaypoint(ctx, entry.waypointSymbol, systemSymbol, playerID)
			if err != nil {
				continue
			}

			distance := factoryWaypoint.DistanceTo(sellWaypoint)

			// Select closest market (first one, or closer than current best)
			if bestDistance < 0 || distance < bestDistance {
				bestDistance = distance
				bestEntry = entry
			}
		}

		if bestEntry == nil {
			continue
		}

		// Get the selected sell market waypoint
		sellMarket, err := f.waypointProvider.GetWaypoint(ctx, bestEntry.waypointSymbol, systemSymbol, playerID)
		if err != nil {
			continue
		}

		// Create opportunity with the closest market
		opp, err := trading.NewManufacturingOpportunity(
			bestEntry.good,
			sellMarket,
			bestEntry.purchasePrice,
			bestEntry.activity,
			bestEntry.supply,
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

// findFactoryWaypoint finds the export market for a good (factory for manufacturing, source for arbitrage)
func (f *ManufacturingDemandFinder) findFactoryWaypoint(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*shared.Waypoint, error) {
	// Get all markets to find the export market for this good
	marketWaypoints, err := f.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get markets: %w", err)
	}

	for _, waypointSymbol := range marketWaypoints {
		marketData, err := f.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}

		for _, tradeGood := range marketData.TradeGoods() {
			if tradeGood.Symbol() == good && tradeGood.TradeType() == market.TradeTypeExport {
				// Found the export market (factory) for this good
				return f.waypointProvider.GetWaypoint(ctx, waypointSymbol, systemSymbol, playerID)
			}
		}
	}

	return nil, fmt.Errorf("no export market found for %s", good)
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

// SetStrategy configures the acquisition strategy for the supply chain resolver.
// This controls whether intermediates are bought (prefer-buy) or fabricated (prefer-fabricate).
//
// Strategies:
//   - prefer-buy: Always buy from markets if available (original behavior, may buy at SCARCE prices)
//   - prefer-fabricate: Fabricate intermediates unless supply is HIGH/ABUNDANT (recursive manufacturing)
//   - smart: Fabricate only when supply is SCARCE/LIMITED (adaptive)
func (f *ManufacturingDemandFinder) SetStrategy(strategy string) {
	f.resolver.SetStrategy(goodsServices.AcquisitionStrategy(strategy))
}

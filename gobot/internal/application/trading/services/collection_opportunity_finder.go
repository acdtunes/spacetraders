package services

import (
	"context"
	"fmt"
	"sort"

	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// CollectionOpportunity represents a profitable opportunity to collect goods from
// a factory with HIGH/ABUNDANT supply and sell to a market with SCARCE/LIMITED demand.
type CollectionOpportunity struct {
	Good               string // The good to collect
	FactorySymbol      string // EXPORT market with HIGH/ABUNDANT supply
	SellMarket         string // IMPORT market with SCARCE/LIMITED supply
	FactorySupply      string // Current factory supply level (HIGH/ABUNDANT)
	FactoryActivity    string // Factory activity level (WEAK preferred for buying)
	SellMarketSupply   string // Sell market supply level
	SellMarketActivity string // Sell market activity level (STRONG preferred for selling)
	SellPrice          int    // Expected sell price at demand market
	BuyPrice           int    // Expected buy price at factory
	ExpectedProfit     int    // Expected profit per unit
}

// Score returns a composite score for opportunity ranking.
// Higher is better.
//
// Activity-based scoring (based on data analysis of 30,493 market price records):
// - STRONG activity at IMPORT markets = highest prices (best for selling)
// - WEAK activity at EXPORT markets = lowest prices (best for buying)
func (o *CollectionOpportunity) Score() int {
	// Base score is expected profit
	score := o.ExpectedProfit

	// Bonus for ABUNDANT factory supply (more reliable source)
	if o.FactorySupply == "ABUNDANT" {
		score += 100
	}

	// Activity-based bonus for sell market (IMPORT)
	// STRONG activity = highest prices at IMPORT markets = best for selling
	switch o.SellMarketActivity {
	case "STRONG":
		score += 500
	case "GROWING":
		score += 300
	case "WEAK":
		score += 100
	case "RESTRICTED":
		score += 0
	}

	// Activity-based bonus for factory (EXPORT market)
	// WEAK activity = lowest prices at EXPORT markets = best for buying
	switch o.FactoryActivity {
	case "WEAK":
		score += 200 // Best for buying
	case "GROWING":
		score += 100
	case "STRONG":
		score += 50
	case "RESTRICTED":
		score += 0 // Worst for buying
	}

	return score
}

// CollectionOpportunityFinder discovers opportunities to collect goods from factories
// with HIGH/ABUNDANT supply and sell to markets with SCARCE/LIMITED demand.
// This enables collection pipelines that operate independently of fabrication pipelines.
//
// Also discovers storage-based opportunities: goods extracted by siphon operations
// (e.g., HYDROCARBON) that can be sold to import markets.
type CollectionOpportunityFinder struct {
	marketRepo    market.MarketRepository
	pipelineRepo  manufacturing.PipelineRepository
	storageOpRepo storage.StorageOperationRepository // Optional: enables storage-based opportunities
}

// NewCollectionOpportunityFinder creates a new collection opportunity finder
func NewCollectionOpportunityFinder(
	marketRepo market.MarketRepository,
	pipelineRepo manufacturing.PipelineRepository,
) *CollectionOpportunityFinder {
	return &CollectionOpportunityFinder{
		marketRepo:   marketRepo,
		pipelineRepo: pipelineRepo,
	}
}

// WithStorageRepo adds storage operation repository for storage-based opportunities.
// Call this to enable finding opportunities for goods produced by siphon operations.
func (f *CollectionOpportunityFinder) WithStorageRepo(repo storage.StorageOperationRepository) *CollectionOpportunityFinder {
	f.storageOpRepo = repo
	return f
}

// CollectionFinderConfig contains configuration for opportunity discovery
type CollectionFinderConfig struct {
	MinProfitMargin   float64 // Minimum profit margin to consider (default: 0.1 = 10%)
	MinExpectedProfit int     // Minimum absolute profit per unit (default: 200)
	MaxOpportunities  int     // Maximum opportunities to return (default: 10)
}

// DefaultCollectionFinderConfig returns sensible defaults
func DefaultCollectionFinderConfig() CollectionFinderConfig {
	return CollectionFinderConfig{
		MinProfitMargin:   0.1, // 10% minimum margin
		MinExpectedProfit: 200, // 200 credits minimum profit per unit
		MaxOpportunities:  10,
	}
}

// FindOpportunities discovers collection opportunities in the given system.
//
// Algorithm:
//  1. Query all markets in system from database
//  2. Find EXPORT markets with HIGH/ABUNDANT supply (factories with goods to collect)
//  3. Find ALL IMPORT markets (buyers) - scoring differentiates by activity
//  4. Match exports to imports, calculate expected profit with activity bonuses
//  5. Skip goods with active collection pipeline (prevent duplicates)
//  6. Return opportunities sorted by score (profit + activity bonuses)
//
// Parameters:
//   - ctx: Context for cancellation
//   - systemSymbol: System to scan (e.g., "X1-AU21")
//   - playerID: Player identifier
//   - config: Configuration for filtering and limits
//
// Returns:
//   - List of opportunities sorted by expected profit (descending)
//   - Error if scanning fails
func (f *CollectionOpportunityFinder) FindOpportunities(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	config CollectionFinderConfig,
) ([]*CollectionOpportunity, error) {
	if systemSymbol == "" {
		return nil, fmt.Errorf("system symbol required")
	}

	// Apply defaults
	if config.MinProfitMargin <= 0 {
		config.MinProfitMargin = 0.1
	}
	if config.MaxOpportunities <= 0 {
		config.MaxOpportunities = 10
	}

	// Step 1: Get all markets in system
	marketWaypoints, err := f.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch markets: %w", err)
	}

	fmt.Printf("[CollectionFinder] Found %d markets in system %s for player %d\n", len(marketWaypoints), systemSymbol, playerID)

	if len(marketWaypoints) == 0 {
		return nil, nil
	}

	// Step 2: Build index of factories (EXPORT with HIGH/ABUNDANT supply)
	// Map: good -> factories that export it with good supply
	type factoryEntry struct {
		waypointSymbol string
		supply         string
		activity       string // Activity level (WEAK preferred for buying)
		sellPrice      int    // Price we'd pay to buy from factory
	}
	factoryIndex := make(map[string][]*factoryEntry)

	// Step 3: Build index of buyers (ALL IMPORT markets)
	// Map: good -> all buyers that import it (scoring differentiates by activity)
	type buyerEntry struct {
		waypointSymbol string
		supply         string
		activity       string
		purchasePrice  int // Price buyer pays us
	}
	buyerIndex := make(map[string][]*buyerEntry)

	// Scan all markets
	for _, waypointSymbol := range marketWaypoints {
		marketData, err := f.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}

		for _, tradeGood := range marketData.TradeGoods() {
			goodSymbol := tradeGood.Symbol()
			supply := ""
			if tradeGood.Supply() != nil {
				supply = *tradeGood.Supply()
			}
			activity := ""
			if tradeGood.Activity() != nil {
				activity = *tradeGood.Activity()
			}

			// Check if this is a factory (EXPORT with HIGH/ABUNDANT supply)
			if tradeGood.TradeType() == market.TradeTypeExport {
				if supply == "ABUNDANT" || supply == "HIGH" {
					factoryIndex[goodSymbol] = append(factoryIndex[goodSymbol], &factoryEntry{
						waypointSymbol: waypointSymbol,
						supply:         supply,
						activity:       activity,
						sellPrice:      tradeGood.SellPrice(), // Price we pay
					})
				}
			}

			// Check if this is a buyer (IMPORT market)
			// Accept ALL import markets - activity-based scoring will differentiate
			// Data analysis shows: STRONG activity markets pay the highest prices
			if tradeGood.TradeType() == market.TradeTypeImport {
				buyerIndex[goodSymbol] = append(buyerIndex[goodSymbol], &buyerEntry{
					waypointSymbol: waypointSymbol,
					supply:         supply,
					activity:       activity,
					purchasePrice:  tradeGood.PurchasePrice(), // Price we receive
				})
			}
		}
	}

	// Debug: Print indices
	fmt.Printf("[CollectionFinder] Factory index has %d goods, Buyer index has %d goods\n", len(factoryIndex), len(buyerIndex))
	for good, factories := range factoryIndex {
		fmt.Printf("[CollectionFinder] Factory good %s: %d factories\n", good, len(factories))
	}
	for good, buyers := range buyerIndex {
		fmt.Printf("[CollectionFinder] Buyer good %s: %d buyers\n", good, len(buyers))
	}

	// Step 4: Match factories to buyers and calculate profit
	var opportunities []*CollectionOpportunity

	for good, factories := range factoryIndex {
		buyers, hasBuyers := buyerIndex[good]
		if !hasBuyers || len(buyers) == 0 {
			fmt.Printf("[CollectionFinder] %s: no buyers, skipping\n", good)
			continue // No buyer for this good
		}

		fmt.Printf("[CollectionFinder] %s: found %d factories and %d buyers, checking for existing pipeline\n", good, len(factories), len(buyers))

		// Skip if there's already an active collection pipeline for this good
		if f.pipelineRepo != nil {
			existingPipeline, err := f.pipelineRepo.FindActiveCollectionForProduct(ctx, playerID, good)
			if err == nil && existingPipeline != nil {
				fmt.Printf("[CollectionFinder] %s: already has active collection pipeline, skipping\n", good)
				continue
			}
		}

		// Find best factory-buyer pair for this good
		var bestOpp *CollectionOpportunity

		for _, factory := range factories {
			for _, buyer := range buyers {
				// Skip if same waypoint (can't buy and sell at same place)
				if factory.waypointSymbol == buyer.waypointSymbol {
					fmt.Printf("[CollectionFinder] %s: skipping same waypoint %s\n", good, factory.waypointSymbol)
					continue
				}

				// Calculate profit
				profit := buyer.purchasePrice - factory.sellPrice

				// Check minimum margin
				if factory.sellPrice > 0 {
					margin := float64(profit) / float64(factory.sellPrice)
					if margin < config.MinProfitMargin {
						fmt.Printf("[CollectionFinder] %s: margin %.2f%% < min %.2f%% (buy=%d, sell=%d), skipping\n",
							good, margin*100, config.MinProfitMargin*100, factory.sellPrice, buyer.purchasePrice)
						continue
					}
				}

				// Check minimum absolute profit
				if profit < config.MinExpectedProfit {
					fmt.Printf("[CollectionFinder] %s: profit %d < min %d, skipping\n",
						good, profit, config.MinExpectedProfit)
					continue
				}

				opp := &CollectionOpportunity{
					Good:               good,
					FactorySymbol:      factory.waypointSymbol,
					SellMarket:         buyer.waypointSymbol,
					FactorySupply:      factory.supply,
					FactoryActivity:    factory.activity,
					SellMarketSupply:   buyer.supply,
					SellMarketActivity: buyer.activity,
					SellPrice:          buyer.purchasePrice,
					BuyPrice:           factory.sellPrice,
					ExpectedProfit:     profit,
				}

				// Track best opportunity for this good
				if bestOpp == nil || opp.Score() > bestOpp.Score() {
					bestOpp = opp
				}
			}
		}

		if bestOpp != nil {
			fmt.Printf("[CollectionFinder] %s: found opportunity, profit=%d, factory=%s, buyer=%s\n",
				good, bestOpp.ExpectedProfit, bestOpp.FactorySymbol, bestOpp.SellMarket)
			opportunities = append(opportunities, bestOpp)
		} else {
			fmt.Printf("[CollectionFinder] %s: no valid opportunity found despite matches\n", good)
		}
	}

	// Step 5: Sort by score (descending)
	sort.Slice(opportunities, func(i, j int) bool {
		return opportunities[i].Score() > opportunities[j].Score()
	})

	// Step 6: Limit results
	if len(opportunities) > config.MaxOpportunities {
		opportunities = opportunities[:config.MaxOpportunities]
	}

	return opportunities, nil
}

// StorageCollectionOpportunity represents an opportunity to collect goods from
// a storage operation (e.g., gas siphoning) and sell to a market.
type StorageCollectionOpportunity struct {
	Good               string // The good to collect (e.g., HYDROCARBON)
	StorageOperationID string // Storage operation to collect from
	StorageWaypoint    string // Where storage ships are located
	SellMarket         string // IMPORT market to sell to
	SellPrice          int    // Expected sell price
	ExpectedProfit     int    // Expected profit per unit (sell price, since storage goods are "free")
}

// FindStorageOpportunities discovers opportunities to sell goods from storage operations.
// This finds byproducts like HYDROCARBON from gas siphoning that accumulate on storage ships
// and can be sold to import markets.
//
// Algorithm:
//  1. Find running storage operations for the player
//  2. Get the goods each operation produces
//  3. For each good, find IMPORT markets with demand
//  4. Create opportunities to sell storage goods to those markets
//  5. Skip goods already handled by an active collection pipeline
func (f *CollectionOpportunityFinder) FindStorageOpportunities(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) ([]*StorageCollectionOpportunity, error) {
	if f.storageOpRepo == nil {
		return nil, nil // No storage repo configured
	}

	// Find running storage operations
	operations, err := f.storageOpRepo.FindRunning(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find storage operations: %w", err)
	}

	if len(operations) == 0 {
		return nil, nil
	}

	// Get all markets in system for finding buyers
	marketWaypoints, err := f.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch markets: %w", err)
	}

	// Build buyer index: good -> import markets (with activity for tie-breaking)
	type buyerEntry struct {
		waypointSymbol string
		purchasePrice  int
		activity       string // STRONG preferred for selling (highest prices)
	}
	buyerIndex := make(map[string][]*buyerEntry)

	for _, waypointSymbol := range marketWaypoints {
		marketData, err := f.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}

		for _, tradeGood := range marketData.TradeGoods() {
			// Only look at IMPORT markets (buyers)
			if tradeGood.TradeType() != market.TradeTypeImport {
				continue
			}

			activity := ""
			if tradeGood.Activity() != nil {
				activity = *tradeGood.Activity()
			}

			goodSymbol := tradeGood.Symbol()
			buyerIndex[goodSymbol] = append(buyerIndex[goodSymbol], &buyerEntry{
				waypointSymbol: waypointSymbol,
				purchasePrice:  tradeGood.PurchasePrice(),
				activity:       activity,
			})
		}
	}

	var opportunities []*StorageCollectionOpportunity

	// Check each storage operation's goods
	for _, op := range operations {
		for _, good := range op.SupportedGoods() {
			// Skip if there's already an active collection pipeline for this good
			if f.pipelineRepo != nil {
				existingPipeline, err := f.pipelineRepo.FindActiveCollectionForProduct(ctx, playerID, good)
				if err == nil && existingPipeline != nil {
					fmt.Printf("[StorageOpportunityFinder] %s: already has active collection pipeline, skipping\n", good)
					continue
				}
			}

			// Find buyers for this good
			buyers, hasBuyers := buyerIndex[good]
			if !hasBuyers || len(buyers) == 0 {
				continue
			}

			// Find best buyer: highest price, with STRONG activity as tiebreaker
			var bestBuyer *buyerEntry
			for _, buyer := range buyers {
				if bestBuyer == nil {
					bestBuyer = buyer
					continue
				}

				// Primary: Highest price
				if buyer.purchasePrice > bestBuyer.purchasePrice {
					bestBuyer = buyer
					continue
				}

				// Secondary: STRONG activity preferred (prices likely to stay high)
				if buyer.purchasePrice == bestBuyer.purchasePrice {
					if goodsServices.ImportActivityScore(buyer.activity) > goodsServices.ImportActivityScore(bestBuyer.activity) {
						bestBuyer = buyer
					}
				}
			}

			if bestBuyer != nil && bestBuyer.purchasePrice > 0 {
				opportunities = append(opportunities, &StorageCollectionOpportunity{
					Good:               good,
					StorageOperationID: op.ID(),
					StorageWaypoint:    op.WaypointSymbol(),
					SellMarket:         bestBuyer.waypointSymbol,
					SellPrice:          bestBuyer.purchasePrice,
					ExpectedProfit:     bestBuyer.purchasePrice, // Storage goods are "free" (already extracted)
				})
				fmt.Printf("[StorageOpportunityFinder] Found opportunity for %s: sell at %s for %d credits\n",
					good, bestBuyer.waypointSymbol, bestBuyer.purchasePrice)
			}
		}
	}

	// Sort by profit
	sort.Slice(opportunities, func(i, j int) bool {
		return opportunities[i].ExpectedProfit > opportunities[j].ExpectedProfit
	})

	return opportunities, nil
}

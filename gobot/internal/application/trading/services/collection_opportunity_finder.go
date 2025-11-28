package services

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// CollectionOpportunity represents a profitable opportunity to collect goods from
// a factory with HIGH/ABUNDANT supply and sell to a market with SCARCE/LIMITED demand.
type CollectionOpportunity struct {
	Good           string // The good to collect
	FactorySymbol  string // EXPORT market with HIGH/ABUNDANT supply
	SellMarket     string // IMPORT market with SCARCE/LIMITED supply
	FactorySupply  string // Current factory supply level (HIGH/ABUNDANT)
	SellPrice      int    // Expected sell price at demand market
	BuyPrice       int    // Expected buy price at factory
	ExpectedProfit int    // Expected profit per unit
}

// Score returns a composite score for opportunity ranking.
// Higher is better.
func (o *CollectionOpportunity) Score() int {
	// Base score is expected profit
	score := o.ExpectedProfit

	// Bonus for ABUNDANT supply (more reliable)
	if o.FactorySupply == "ABUNDANT" {
		score += 100
	}

	return score
}

// CollectionOpportunityFinder discovers opportunities to collect goods from factories
// with HIGH/ABUNDANT supply and sell to markets with SCARCE/LIMITED demand.
// This enables collection pipelines that operate independently of fabrication pipelines.
type CollectionOpportunityFinder struct {
	marketRepo   market.MarketRepository
	pipelineRepo manufacturing.PipelineRepository
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
//  3. Find IMPORT markets with SCARCE/LIMITED supply + WEAK/RESTRICTED activity (buyers)
//  4. Match exports to imports, calculate expected profit
//  5. Skip goods with active collection pipeline (prevent duplicates)
//  6. Return opportunities sorted by profit
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
		sellPrice      int // Price we'd pay to buy from factory
	}
	factoryIndex := make(map[string][]*factoryEntry)

	// Step 3: Build index of buyers (IMPORT with SCARCE/LIMITED supply and WEAK/RESTRICTED activity)
	// Map: good -> buyers that import it with demand
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

			// Check if this is a factory (EXPORT with HIGH/ABUNDANT)
			if tradeGood.TradeType() == market.TradeTypeExport {
				if supply == "HIGH" || supply == "ABUNDANT" {
					factoryIndex[goodSymbol] = append(factoryIndex[goodSymbol], &factoryEntry{
						waypointSymbol: waypointSymbol,
						supply:         supply,
						sellPrice:      tradeGood.SellPrice(), // Price we pay
					})
				}
			}

			// Check if this is a buyer (IMPORT with SCARCE/LIMITED supply)
			// SCARCE supply always qualifies as high demand regardless of activity
			// LIMITED supply requires WEAK/RESTRICTED activity to indicate unmet demand
			if tradeGood.TradeType() == market.TradeTypeImport {
				isBuyer := false
				if supply == "SCARCE" {
					// SCARCE always indicates high demand - activity doesn't matter
					isBuyer = true
				} else if supply == "LIMITED" && (activity == "WEAK" || activity == "RESTRICTED") {
					// LIMITED with low activity indicates unmet demand
					isBuyer = true
				}
				if isBuyer {
					buyerIndex[goodSymbol] = append(buyerIndex[goodSymbol], &buyerEntry{
						waypointSymbol: waypointSymbol,
						supply:         supply,
						activity:       activity,
						purchasePrice:  tradeGood.PurchasePrice(), // Price we receive
					})
				}
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
					Good:           good,
					FactorySymbol:  factory.waypointSymbol,
					SellMarket:     buyer.waypointSymbol,
					FactorySupply:  factory.supply,
					SellPrice:      buyer.purchasePrice,
					BuyPrice:       factory.sellPrice,
					ExpectedProfit: profit,
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

// FindOpportunitiesForGood finds collection opportunities for a specific good.
// Useful when you know what good you want to collect but need to find markets.
func (f *CollectionOpportunityFinder) FindOpportunitiesForGood(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) ([]*CollectionOpportunity, error) {
	if good == "" || systemSymbol == "" {
		return nil, fmt.Errorf("good and system symbol required")
	}

	// Get all markets
	marketWaypoints, err := f.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch markets: %w", err)
	}

	var factories []*struct {
		waypointSymbol string
		supply         string
		sellPrice      int
	}
	var buyers []*struct {
		waypointSymbol string
		purchasePrice  int
	}

	// Find factories and buyers for this good
	for _, waypointSymbol := range marketWaypoints {
		marketData, err := f.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}

		tradeGood := marketData.FindGood(good)
		if tradeGood == nil {
			continue
		}

		supply := ""
		if tradeGood.Supply() != nil {
			supply = *tradeGood.Supply()
		}
		activity := ""
		if tradeGood.Activity() != nil {
			activity = *tradeGood.Activity()
		}

		// Factory check
		if tradeGood.TradeType() == market.TradeTypeExport {
			if supply == "HIGH" || supply == "ABUNDANT" {
				factories = append(factories, &struct {
					waypointSymbol string
					supply         string
					sellPrice      int
				}{
					waypointSymbol: waypointSymbol,
					supply:         supply,
					sellPrice:      tradeGood.SellPrice(),
				})
			}
		}

		// Buyer check - SCARCE always qualifies, LIMITED requires WEAK/RESTRICTED activity
		if tradeGood.TradeType() == market.TradeTypeImport {
			isBuyer := false
			if supply == "SCARCE" {
				isBuyer = true
			} else if supply == "LIMITED" && (activity == "WEAK" || activity == "RESTRICTED") {
				isBuyer = true
			}
			if isBuyer {
				buyers = append(buyers, &struct {
					waypointSymbol string
					purchasePrice  int
				}{
					waypointSymbol: waypointSymbol,
					purchasePrice:  tradeGood.PurchasePrice(),
				})
			}
		}
	}

	// Build opportunities
	var opportunities []*CollectionOpportunity

	for _, factory := range factories {
		for _, buyer := range buyers {
			if factory.waypointSymbol == buyer.waypointSymbol {
				continue
			}

			profit := buyer.purchasePrice - factory.sellPrice
			if profit <= 0 {
				continue
			}

			opportunities = append(opportunities, &CollectionOpportunity{
				Good:           good,
				FactorySymbol:  factory.waypointSymbol,
				SellMarket:     buyer.waypointSymbol,
				FactorySupply:  factory.supply,
				SellPrice:      buyer.purchasePrice,
				BuyPrice:       factory.sellPrice,
				ExpectedProfit: profit,
			})
		}
	}

	// Sort by profit
	sort.Slice(opportunities, func(i, j int) bool {
		return opportunities[i].ExpectedProfit > opportunities[j].ExpectedProfit
	})

	return opportunities, nil
}

package services

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// MarketLocator finds optimal markets for buying and selling goods.
// It ranks markets by activity and supply levels to guide production decisions.
// For ship-type goods, it also searches shipyards.
type MarketLocator struct {
	marketRepo   market.MarketRepository
	waypointRepo system.WaypointRepository
	playerRepo   player.PlayerRepository
	apiClient    ports.APIClient
}

// NewMarketLocator creates a new market locator service
func NewMarketLocator(
	marketRepo market.MarketRepository,
	waypointRepo system.WaypointRepository,
	playerRepo player.PlayerRepository,
	apiClient ports.APIClient,
) *MarketLocator {
	return &MarketLocator{
		marketRepo:   marketRepo,
		waypointRepo: waypointRepo,
		playerRepo:   playerRepo,
		apiClient:    apiClient,
	}
}

// MarketLocatorResult contains market information for a good
type MarketLocatorResult struct {
	WaypointSymbol string
	Activity       string // WEAK, GROWING, STRONG, RESTRICTED
	Supply         string // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
	Price          int    // sell_price (for exports) or purchase_price (for imports)
	TradeVolume    int    // Maximum units per transaction
}

// FindImportMarket finds a market that wants to buy a good (imports it).
// Returns the market with the highest purchase price, preferring STRONG activity.
func (l *MarketLocator) FindImportMarket(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Use the repository's FindBestMarketBuying method
	bestMarket, err := l.marketRepo.FindBestMarketBuying(ctx, good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find import market for %s: %w", good, err)
	}

	if bestMarket == nil {
		return nil, fmt.Errorf("no market found importing %s", good)
	}

	tradeGood, err := l.scannedTradeGood(ctx, bestMarket.WaypointSymbol, good, playerID)
	if err != nil {
		return nil, err
	}

	return &MarketLocatorResult{
		WaypointSymbol: bestMarket.WaypointSymbol,
		Activity:       activityOrEmpty(tradeGood),
		Supply:         bestMarket.Supply,
		Price:          bestMarket.PurchasePrice,
		TradeVolume:    tradeGood.TradeVolume(),
	}, nil
}

// findShipyardSellingShip finds a shipyard that sells a specific ship type.
// Returns the shipyard with the lowest purchase price.
func (l *MarketLocator) findShipyardSellingShip(
	ctx context.Context,
	shipType string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Get player to fetch API token
	playerEntity, err := l.playerRepo.FindByID(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %w", err)
	}

	// Find all shipyards in the system
	shipyards, err := l.waypointRepo.ListBySystemWithTrait(ctx, systemSymbol, "SHIPYARD")
	if err != nil {
		return nil, fmt.Errorf("failed to find shipyards: %w", err)
	}

	if len(shipyards) == 0 {
		return nil, fmt.Errorf("no shipyards found in system %s", systemSymbol)
	}

	// Search all shipyards for the ship type
	var bestShipyard *MarketLocatorResult
	var bestPrice int

	for _, waypoint := range shipyards {
		// Get shipyard data from API
		shipyardData, err := l.apiClient.GetShipyard(ctx, systemSymbol, waypoint.Symbol, playerEntity.Token)
		if err != nil {
			// Skip shipyards we can't access
			continue
		}

		// Find the ship in this shipyard's listings
		for _, listing := range shipyardData.Ships {
			if listing.Type == shipType {
				// Found the ship! Check if it's cheaper than current best
				if bestShipyard == nil || listing.PurchasePrice < bestPrice {
					bestPrice = listing.PurchasePrice
					bestShipyard = &MarketLocatorResult{
						WaypointSymbol: waypoint.Symbol,
						Activity:       "", // Shipyards don't have activity/supply metrics
						Supply:         "",
						Price:          listing.PurchasePrice,
					}
				}
			}
		}
	}

	if bestShipyard == nil {
		return nil, fmt.Errorf("no shipyard found selling %s in system %s", shipType, systemSymbol)
	}

	return bestShipyard, nil
}

// isShipType returns true if the good is an actual ship type (not ship components like SHIP_PARTS).
// Ship types are manufactured at shipyards, while ship components are sold at regular markets.
var shipComponents = map[string]bool{
	"SHIP_PARTS":   true,
	"SHIP_PLATING": true,
}

func isShipType(good string) bool {
	if shipComponents[good] {
		return false
	}
	return strings.HasPrefix(good, "SHIP_")
}

func (l *MarketLocator) scannedTradeGood(ctx context.Context, waypointSymbol string, good string, playerID int) (*market.TradeGood, error) {
	marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get market data: %w", err)
	}
	if marketData == nil {
		return nil, fmt.Errorf("no market data found for %s (market may not have been scanned)", waypointSymbol)
	}

	tradeGood := marketData.FindGood(good)
	if tradeGood == nil {
		return nil, fmt.Errorf("good %s not found in market %s", good, waypointSymbol)
	}
	return tradeGood, nil
}

// FindExportMarket finds a market that sells a good (exports it).
// For actual ship types (not ship components), searches shipyards.
// For regular goods and ship components, returns the market with the lowest sell price.
func (l *MarketLocator) FindExportMarket(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Check if this is an actual ship type - ships are manufactured at shipyards
	// Ship components (SHIP_PARTS, SHIP_PLATING) are regular market goods
	if isShipType(good) {
		return l.findShipyardSellingShip(ctx, good, systemSymbol, playerID)
	}

	// Use the repository's FindCheapestMarketSelling method for regular goods
	cheapestMarket, err := l.marketRepo.FindCheapestMarketSelling(ctx, good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find export market for %s: %w", good, err)
	}

	if cheapestMarket == nil {
		return nil, fmt.Errorf("no market found exporting %s", good)
	}

	tradeGood, err := l.scannedTradeGood(ctx, cheapestMarket.WaypointSymbol, good, playerID)
	if err != nil {
		return nil, err
	}

	return &MarketLocatorResult{
		WaypointSymbol: cheapestMarket.WaypointSymbol,
		Activity:       activityOrEmpty(tradeGood),
		Supply:         cheapestMarket.Supply,
		Price:          cheapestMarket.SellPrice,
		TradeVolume:    tradeGood.TradeVolume(),
	}, nil
}

// FindExportMarketBySupplyPriority finds the best market with acceptable supply level.
// Priority: Supply level (ABUNDANT > HIGH > MODERATE), then Activity (WEAK > GROWING > STRONG).
// SCARCE and LIMITED supply levels are skipped to avoid overpaying.
//
// Activity-based optimization: For EXPORT markets (buying), WEAK activity = lowest prices.
// Data analysis: WEAK + ABUNDANT = avg 43 credits, RESTRICTED + ABUNDANT = 6,863 credits.
//
// This is used for raw material acquisition in manufacturing pipelines.
// Example: LIQUID_NITROGEN at ABUNDANT G52 costs 18-28 credits, but SCARCE C44 costs 650+.
//
// Returns error if no market with MODERATE or better supply exists.
func (l *MarketLocator) FindExportMarketBySupplyPriority(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Ship types are handled by shipyards (no supply levels)
	if isShipType(good) {
		return l.findShipyardSellingShip(ctx, good, systemSymbol, playerID)
	}

	// Get all markets in the system to consider activity
	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets: %w", err)
	}

	// Collect all candidate markets with MODERATE+ supply
	type candidateMarket struct {
		waypointSymbol string
		supply         string
		activity       string
		price          int
		tradeVolume    int
		supplyScore    int // ABUNDANT=3, HIGH=2, MODERATE=1
		activityScore  int // WEAK=4, GROWING=3, STRONG=2, RESTRICTED=1
	}
	var candidates []candidateMarket

	for _, waypointSymbol := range marketWaypoints {
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}

		tradeGood := marketData.FindGood(good)
		if tradeGood == nil || tradeGood.TradeType() != market.TradeTypeExport {
			continue
		}

		supply := supplyOrEmpty(tradeGood)

		// Skip SCARCE and LIMITED - only accept MODERATE+
		supplyScore := manufacturing.SupplyLevel(supply).Order() - manufacturing.SupplyLevelLimited.Order()
		if supplyScore < 1 {
			continue
		}

		activity := activityOrEmpty(tradeGood)

		candidates = append(candidates, candidateMarket{
			waypointSymbol: waypointSymbol,
			supply:         supply,
			activity:       activity,
			price:          tradeGood.SellPrice(),
			tradeVolume:    tradeGood.TradeVolume(),
			supplyScore:    supplyScore,
			activityScore:  ExportActivityScore(activity),
		})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no market with MODERATE+ supply for %s (SCARCE/LIMITED markets skipped)", good)
	}

	// Sort by: Supply priority DESC, then Activity score DESC, then Price ASC
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].supplyScore != candidates[j].supplyScore {
			return candidates[i].supplyScore > candidates[j].supplyScore
		}
		if candidates[i].activityScore != candidates[j].activityScore {
			return candidates[i].activityScore > candidates[j].activityScore
		}
		return candidates[i].price < candidates[j].price
	})

	best := candidates[0]
	return &MarketLocatorResult{
		WaypointSymbol: best.waypointSymbol,
		Activity:       best.activity,
		Supply:         best.supply,
		Price:          best.price,
		TradeVolume:    best.tradeVolume,
	}, nil
}

// FindExportMarketWithGoodSupply finds a market that exports a good with HIGH or ABUNDANT supply.
// This is used for supply-gated acquisitions to ensure we only buy when prices are favorable.
// Returns nil if no market with good supply is available.
//
// Supply levels affect prices:
// - ABUNDANT: -20 to -10% (best prices for buying)
// - HIGH: -10 to 0% (good prices for buying)
// - MODERATE: 0-15% (average prices)
// - LIMITED: +15-30% (above average prices)
// - SCARCE: +30-70% (worst prices - NEVER BUY)
func (l *MarketLocator) FindExportMarketWithGoodSupply(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Check if this is an actual ship type - ships are manufactured at shipyards
	// Shipyards don't have supply levels, so they're always available
	if isShipType(good) {
		return l.findShipyardSellingShip(ctx, good, systemSymbol, playerID)
	}

	// Get all markets in the system
	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets in system: %w", err)
	}

	// Collect all markets with HIGH or ABUNDANT supply
	type candidateMarket struct {
		result *MarketLocatorResult
		supply string
		price  int
	}
	var candidates []candidateMarket

	for _, waypointSymbol := range marketWaypoints {
		// Get market data
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue // Skip markets we can't access
		}

		// Check if this market exports the good
		tradeGood := marketData.FindGood(good)
		if tradeGood == nil {
			continue // Market doesn't have this good
		}

		// Only consider EXPORT markets (selling to us)
		if tradeGood.TradeType() != market.TradeTypeExport {
			continue
		}

		// Check supply level - only HIGH or ABUNDANT
		supply := supplyOrEmpty(tradeGood)
		if !isHighOrAbundant(supply) {
			continue
		}

		activity := activityOrEmpty(tradeGood)

		candidates = append(candidates, candidateMarket{
			result: &MarketLocatorResult{
				WaypointSymbol: waypointSymbol,
				Activity:       activity,
				Supply:         supply,
				Price:          tradeGood.SellPrice(),
				TradeVolume:    tradeGood.TradeVolume(),
			},
			supply: supply,
			price:  tradeGood.SellPrice(),
		})
	}

	if len(candidates) == 0 {
		return nil, nil // No market with good supply - not an error, just unavailable
	}

	// Sort candidates: ABUNDANT > HIGH, then by price (lower is better)
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].supply != candidates[j].supply {
			return candidates[i].supply == supplyAbundant
		}
		return candidates[i].price < candidates[j].price
	})

	return candidates[0].result, nil
}

// FindBestExportMarket finds the best market for selling a good.
// It prefers markets with high activity and abundant supply.
// Ranking: STRONG + ABUNDANT/HIGH > GROWING + MODERATE/HIGH > Any + MODERATE > WEAK/SCARCE
func (l *MarketLocator) FindBestExportMarket(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Get all markets in the system
	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets in system: %w", err)
	}

	var bestMarket *MarketLocatorResult
	var bestScore int

	for _, waypointSymbol := range marketWaypoints {
		// Get market data
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue // Skip markets we can't access
		}

		// Check if this market sells the good
		tradeGood := marketData.FindGood(good)
		if tradeGood == nil {
			continue // Market doesn't sell this good
		}

		// Calculate market score based on activity and supply
		activity := activityOrEmpty(tradeGood)
		supply := supplyOrEmpty(tradeGood)

		score := calculateMarketScore(activity, supply)

		// Update best market if this one has a higher score
		if bestMarket == nil || score > bestScore {
			bestScore = score
			bestMarket = &MarketLocatorResult{
				WaypointSymbol: waypointSymbol,
				Activity:       activity,
				Supply:         supply,
				Price:          tradeGood.SellPrice(),
				TradeVolume:    tradeGood.TradeVolume(),
			}
		}
	}

	if bestMarket == nil {
		return nil, fmt.Errorf("no market found exporting %s", good)
	}

	return bestMarket, nil
}

// calculateMarketScore assigns a numeric score to a market based on activity and supply.
// Higher scores indicate better markets for selling goods.
// Scoring hierarchy:
// 1. STRONG activity + ABUNDANT/HIGH supply (90-100)
// 2. GROWING activity + MODERATE/HIGH supply (70-80)
// 3. Any activity + MODERATE supply (40-60)
// 4. WEAK activity or SCARCE/LIMITED supply (10-30)
func calculateMarketScore(activity, supply string) int {
	return sellMarketScoringPolicy.Score(activity, supply)
}

// ExportActivityScore returns a score for activity when BUYING from export markets.
// For EXPORT markets (buying), lower activity = lower prices = better for us.
// Data analysis: WEAK + ABUNDANT = avg 43 credits, RESTRICTED + ABUNDANT = 6,863 credits
func ExportActivityScore(activity string) int {
	return market.ActivityLevel(activity).BuyerActivityScore()
}

// ImportActivityScore returns a score for activity when SELLING to import markets.
// For IMPORT markets (selling), higher activity = higher prices = better for us.
// Data analysis: STRONG = avg 7,551 credits, RESTRICTED = 1,480 credits
func ImportActivityScore(activity string) int {
	return market.ActivityLevel(activity).SellerActivityScore()
}

// FindFactoryForProduction finds a waypoint that can produce outputGood
// AND accepts all inputGoods for delivery. This prevents the bug where
// a factory is selected that exports the output but doesn't have a market
// for the required inputs.
//
// Parameters:
//   - outputGood: The good to be produced (factory must EXPORT/SELL this)
//   - inputGoods: Goods that will be delivered (factory must IMPORT/BUY these)
//
// Returns the best factory waypoint that satisfies both conditions.
func (l *MarketLocator) FindFactoryForProduction(
	ctx context.Context,
	outputGood string,
	inputGoods []string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Get all markets in the system
	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets in system: %w", err)
	}

	var bestFactory *MarketLocatorResult
	var bestScore int

	for _, waypointSymbol := range marketWaypoints {
		// Get market data
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue // Skip markets we can't access
		}

		// Check if this market EXPORTS the output good (actually produces it)
		// CRITICAL: Must check trade_type = EXPORT, not just that the good exists!
		// A market can IMPORT a good (consume it) without producing it.
		outputTradeGood := marketData.FindGood(outputGood)
		if outputTradeGood == nil || outputTradeGood.TradeType() != market.TradeTypeExport {
			continue // Market doesn't produce (export) this good
		}

		// Check if this market IMPORTS all input goods (buys them)
		// A factory that produces a good should also accept its inputs
		allInputsAccepted := true
		for _, inputGood := range inputGoods {
			inputTradeGood := marketData.FindGood(inputGood)
			if inputTradeGood == nil {
				allInputsAccepted = false
				break
			}
		}

		if !allInputsAccepted {
			continue // Factory doesn't accept all required inputs
		}

		// Calculate score based on output good activity and supply
		activity := activityOrEmpty(outputTradeGood)
		supply := supplyOrEmpty(outputTradeGood)

		score := calculateMarketScore(activity, supply)

		// Update best factory if this one has a higher score
		if bestFactory == nil || score > bestScore {
			bestScore = score
			bestFactory = &MarketLocatorResult{
				WaypointSymbol: waypointSymbol,
				Activity:       activity,
				Supply:         supply,
				Price:          outputTradeGood.SellPrice(),
				TradeVolume:    outputTradeGood.TradeVolume(),
			}
		}
	}

	if bestFactory == nil {
		return nil, fmt.Errorf("no factory found that produces %s AND accepts inputs %v", outputGood, inputGoods)
	}

	return bestFactory, nil
}

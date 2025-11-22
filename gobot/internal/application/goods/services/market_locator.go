package services

import (
	"context"
	"fmt"
	"strings"

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

	// Get full market data to extract activity
	marketData, err := l.marketRepo.GetMarketData(ctx, bestMarket.WaypointSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get market data: %w", err)
	}

	// Extract trade good details
	tradeGood := marketData.FindGood(good)
	if tradeGood == nil {
		return nil, fmt.Errorf("good %s not found in market %s", good, bestMarket.WaypointSymbol)
	}

	result := &MarketLocatorResult{
		WaypointSymbol: bestMarket.WaypointSymbol,
		Activity:       "",
		Supply:         bestMarket.Supply,
		Price:          bestMarket.PurchasePrice,
	}

	// Extract activity if available
	if tradeGood.Activity() != nil {
		result.Activity = *tradeGood.Activity()
	}

	return result, nil
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
func isShipType(good string) bool {
	// Ship components (sold at markets, not shipyards)
	shipComponents := map[string]bool{
		"SHIP_PARTS":   true,
		"SHIP_PLATING": true,
	}

	// If it's a ship component, it's not a ship type
	if shipComponents[good] {
		return false
	}

	// Otherwise, if it starts with "SHIP_", it's a ship type
	return strings.HasPrefix(good, "SHIP_")
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

	// Get full market data to extract activity
	marketData, err := l.marketRepo.GetMarketData(ctx, cheapestMarket.WaypointSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get market data: %w", err)
	}

	// Extract trade good details
	tradeGood := marketData.FindGood(good)
	if tradeGood == nil {
		return nil, fmt.Errorf("good %s not found in market %s", good, cheapestMarket.WaypointSymbol)
	}

	result := &MarketLocatorResult{
		WaypointSymbol: cheapestMarket.WaypointSymbol,
		Activity:       "",
		Supply:         cheapestMarket.Supply,
		Price:          cheapestMarket.SellPrice,
	}

	// Extract activity if available
	if tradeGood.Activity() != nil {
		result.Activity = *tradeGood.Activity()
	}

	return result, nil
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
		if err != nil {
			continue // Skip markets we can't access
		}

		// Check if this market sells the good
		tradeGood := marketData.FindGood(good)
		if tradeGood == nil {
			continue // Market doesn't sell this good
		}

		// Calculate market score based on activity and supply
		activity := ""
		if tradeGood.Activity() != nil {
			activity = *tradeGood.Activity()
		}
		supply := ""
		if tradeGood.Supply() != nil {
			supply = *tradeGood.Supply()
		}

		score := calculateMarketScore(activity, supply)

		// Update best market if this one has a higher score
		if bestMarket == nil || score > bestScore {
			bestScore = score
			bestMarket = &MarketLocatorResult{
				WaypointSymbol: waypointSymbol,
				Activity:       activity,
				Supply:         supply,
				Price:          tradeGood.SellPrice(),
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
	activityScore := 0
	switch activity {
	case "STRONG":
		activityScore = 50
	case "GROWING":
		activityScore = 30
	case "WEAK":
		activityScore = 10
	case "RESTRICTED":
		activityScore = 5
	default:
		activityScore = 20 // Unknown/missing activity
	}

	supplyScore := 0
	switch supply {
	case "ABUNDANT":
		supplyScore = 50
	case "HIGH":
		supplyScore = 40
	case "MODERATE":
		supplyScore = 30
	case "LIMITED":
		supplyScore = 20
	case "SCARCE":
		supplyScore = 10
	default:
		supplyScore = 15 // Unknown/missing supply
	}

	return activityScore + supplyScore
}

package contract

import (
	"context"
	"fmt"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// FindPurchaseMarket finds the cheapest market for purchasing goods needed for a contract delivery.
//
// Parameters:
//   - contract: The contract containing delivery requirements
//   - marketRepo: Repository to query markets
//   - playerID: Player ID for market lookups
//
// Returns:
//   - waypointSymbol: The waypoint symbol of the cheapest market
//   - error: Any error encountered
//
// Note: This function finds the market for the FIRST delivery in the contract.
// For contracts with multiple deliveries requiring different goods, it returns
// the market for the first unfulfilled delivery.
func FindPurchaseMarket(
	ctx context.Context,
	contract *domainContract.Contract,
	marketRepo trading.MarketRepository,
	playerID int,
) (string, error) {
	deliveries := contract.Terms().Deliveries

	// Find the first unfulfilled delivery
	for _, delivery := range deliveries {
		unitsNeeded := delivery.UnitsRequired - delivery.UnitsFulfilled
		if unitsNeeded == 0 {
			continue // Already fulfilled, skip
		}

		// Extract system from destination (e.g., X1-GZ7-A1 -> X1-GZ7)
		system := extractSystem(delivery.DestinationSymbol)

		// Find cheapest market selling this good
		cheapestMarket, err := marketRepo.FindCheapestMarketSelling(
			ctx,
			delivery.TradeSymbol,
			system,
			playerID,
		)
		if err != nil {
			return "", fmt.Errorf("failed to find market for %s: %w", delivery.TradeSymbol, err)
		}
		if cheapestMarket == nil {
			return "", fmt.Errorf("no market found selling %s in system %s", delivery.TradeSymbol, system)
		}

		fmt.Printf("[MARKET_FINDER] Cheapest market for %s: %s (price: %d)\n",
			delivery.TradeSymbol, cheapestMarket.WaypointSymbol, cheapestMarket.SellPrice)

		return cheapestMarket.WaypointSymbol, nil
	}

	return "", fmt.Errorf("no unfulfilled deliveries found in contract")
}

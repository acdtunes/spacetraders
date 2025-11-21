package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
	marketRepo market.MarketRepository,
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
		system := shared.ExtractSystemSymbol(delivery.DestinationSymbol)

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

		logger := common.LoggerFromContext(ctx)
		logger.Log("INFO", "Cheapest market found", map[string]interface{}{
			"action":       "find_cheapest_market",
			"trade_symbol": delivery.TradeSymbol,
			"market":       cheapestMarket.WaypointSymbol,
			"sell_price":   cheapestMarket.SellPrice,
		})

		return cheapestMarket.WaypointSymbol, nil
	}

	return "", fmt.Errorf("no unfulfilled deliveries found in contract")
}

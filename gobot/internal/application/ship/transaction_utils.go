package ship

import (
	"context"
)

// getTransactionLimit retrieves the transaction limit for a trade good from market data.
//
// This function attempts to fetch market data for the specified waypoint and extract
// the transaction limit for the given trade good. If market data is unavailable or
// the good is not found at the market, it falls back to using the full requested units
// as the limit (single transaction mode).
//
// Fallback strategy:
//   - Market data unavailable → single transaction with all requested units
//   - Trade good not available at market → single transaction with all requested units
//   - Trade good has limit → use the market's transaction limit
//
// This ensures the handler can still operate even when market data is stale or missing.
//
// Parameters:
//   - ctx: Request context for cancellation
//   - marketRepo: Repository for fetching market data
//   - waypointSymbol: Symbol of the waypoint/market (e.g., "X1-C3-A1")
//   - goodSymbol: Symbol of the trade good (e.g., "IRON_ORE")
//   - playerID: Player ID for market data access
//   - requestedUnits: Number of units requested in the original command
//
// Returns:
//   - Transaction limit (units per API call). Either the market's limit or requestedUnits.
func getTransactionLimit(
	ctx context.Context,
	marketRepo MarketRepository,
	waypointSymbol string,
	goodSymbol string,
	playerID int,
	requestedUnits int,
) int {
	// Try to fetch market data
	marketData, err := marketRepo.GetMarketData(ctx, uint(playerID), waypointSymbol)
	if err != nil || marketData == nil {
		// Market data unavailable - use single transaction fallback
		return requestedUnits
	}

	limit := marketData.GetTransactionLimit(goodSymbol)
	if limit == 0 {
		// Good not available at market - use single transaction fallback
		return requestedUnits
	}

	return limit
}

// min returns the minimum of two integers.
//
// This utility is used in transaction splitting loops to determine how many units
// to process in each API call (capped by either remaining units or transaction limit).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

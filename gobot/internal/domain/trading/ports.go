package trading

import "context"

// ArbitrageOpportunityFinder defines the interface for discovering arbitrage opportunities.
// This is implemented in the application layer as a service.
type ArbitrageOpportunityFinder interface {
	// FindOpportunities scans all markets in a system for arbitrage opportunities.
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
	FindOpportunities(
		ctx context.Context,
		systemSymbol string,
		playerID int,
		cargoCapacity int,
		minMargin float64,
		limit int,
	) ([]*ArbitrageOpportunity, error)
}

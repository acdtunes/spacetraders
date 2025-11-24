package trading

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

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

// ArbitrageExecutionLogRepository manages execution logs for training data.
// This is implemented in the adapter layer (persistence).
type ArbitrageExecutionLogRepository interface {
	// Save persists a new execution log
	Save(ctx context.Context, log *ArbitrageExecutionLog) error

	// FindByPlayerID retrieves logs for ML training with pagination
	//
	// Parameters:
	//   - ctx: Context for cancellation
	//   - playerID: Player identifier
	//   - limit: Maximum number of logs to return (0 for all)
	//   - offset: Number of logs to skip for pagination
	//
	// Returns:
	//   - List of execution logs ordered by executed_at DESC
	//   - Error if query fails
	FindByPlayerID(
		ctx context.Context,
		playerID shared.PlayerID,
		limit int,
		offset int,
	) ([]*ArbitrageExecutionLog, error)

	// FindSuccessfulRuns retrieves only successful executions
	//
	// Parameters:
	//   - ctx: Context for cancellation
	//   - playerID: Player identifier
	//   - minExamples: Minimum number of examples to return (0 for all)
	//
	// Returns:
	//   - List of successful execution logs ordered by executed_at DESC
	//   - Error if query fails
	FindSuccessfulRuns(
		ctx context.Context,
		playerID shared.PlayerID,
		minExamples int,
	) ([]*ArbitrageExecutionLog, error)

	// CountByPlayerID returns total logged executions for a player
	//
	// Parameters:
	//   - ctx: Context for cancellation
	//   - playerID: Player identifier
	//
	// Returns:
	//   - Total count of execution logs
	//   - Error if query fails
	CountByPlayerID(ctx context.Context, playerID shared.PlayerID) (int, error)

	// ExportToCSV exports logs for ML training (Python consumption)
	//
	// Parameters:
	//   - ctx: Context for cancellation
	//   - playerID: Player identifier
	//   - outputPath: File path for CSV output
	//
	// Returns:
	//   - Error if export fails
	ExportToCSV(
		ctx context.Context,
		playerID shared.PlayerID,
		outputPath string,
	) error
}

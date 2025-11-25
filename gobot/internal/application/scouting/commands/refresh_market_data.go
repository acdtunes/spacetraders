package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MarketScanner defines the interface for scanning and saving market data.
// This interface breaks the import cycle between ship/commands and scouting/commands.
type MarketScanner interface {
	ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error
}

// RefreshMarketDataCommand triggers a market data refresh after a transaction
// This command fetches fresh market data from the API and updates the database
type RefreshMarketDataCommand struct {
	PlayerID       shared.PlayerID
	WaypointSymbol string
}

// RefreshMarketDataResponse contains the result of the refresh operation
type RefreshMarketDataResponse struct {
	Success bool
	Error   string
}

// RefreshMarketDataHandler handles market data refresh requests
type RefreshMarketDataHandler struct {
	marketScanner MarketScanner
}

// NewRefreshMarketDataHandler creates a new handler
func NewRefreshMarketDataHandler(marketScanner MarketScanner) *RefreshMarketDataHandler {
	return &RefreshMarketDataHandler{
		marketScanner: marketScanner,
	}
}

// Handle executes the refresh market data command
func (h *RefreshMarketDataHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RefreshMarketDataCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)

	// Scan and save market data
	err := h.marketScanner.ScanAndSaveMarket(ctx, uint(cmd.PlayerID.Value()), cmd.WaypointSymbol)
	if err != nil {
		logger.Log("WARN", "Failed to refresh market data after transaction", map[string]interface{}{
			"waypoint": cmd.WaypointSymbol,
			"error":    err.Error(),
		})
		return &RefreshMarketDataResponse{
			Success: false,
			Error:   err.Error(),
		}, nil // Don't fail the operation, just return error in response
	}

	logger.Log("DEBUG", "Market data refreshed after transaction", map[string]interface{}{
		"waypoint": cmd.WaypointSymbol,
	})

	return &RefreshMarketDataResponse{
		Success: true,
	}, nil
}

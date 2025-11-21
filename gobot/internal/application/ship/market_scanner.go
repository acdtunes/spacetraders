package ship

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	infraports "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// MarketScanner handles automatic market scanning and data persistence
type MarketScanner struct {
	apiClient  infraports.APIClient
	marketRepo MarketRepository
	playerRepo player.PlayerRepository
}

// NewMarketScanner creates a new market scanner service
func NewMarketScanner(
	apiClient infraports.APIClient,
	marketRepo MarketRepository,
	playerRepo player.PlayerRepository,
) *MarketScanner {
	return &MarketScanner{
		apiClient:  apiClient,
		marketRepo: marketRepo,
		playerRepo: playerRepo,
	}
}

// ScanAndSaveMarket scans a market at the given waypoint and saves the data to the database.
// This is a non-fatal operation - errors are logged but do not fail the caller's operation.
func (s *MarketScanner) ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error {
	logger := common.LoggerFromContext(ctx)

	// Get player token from context
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to get player token: %v", err), nil)
		return fmt.Errorf("failed to get player token: %w", err)
	}

	// Extract system symbol from waypoint (e.g., "X1-TEST-A1" -> "X1-TEST")
	// Find last hyphen to extract system symbol
	systemSymbol := waypointSymbol
	for i := len(waypointSymbol) - 1; i >= 0; i-- {
		if waypointSymbol[i] == '-' {
			systemSymbol = waypointSymbol[:i]
			break
		}
	}

	logger.Log("INFO", fmt.Sprintf("[MarketScanner] Scanning market at %s", waypointSymbol), nil)

	// Get market data from API
	marketData, err := s.apiClient.GetMarket(ctx, systemSymbol, waypointSymbol, token)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to get market data for %s: %v", waypointSymbol, err), nil)
		return fmt.Errorf("failed to get market data for %s: %w", waypointSymbol, err)
	}

	// Convert API DTOs to domain TradeGoods
	tradeGoods := make([]market.TradeGood, 0, len(marketData.TradeGoods))
	for _, apiGood := range marketData.TradeGoods {
		good, err := market.NewTradeGood(
			apiGood.Symbol,
			&apiGood.Supply,
			nil, // activity not provided in API response
			apiGood.SellPrice,
			apiGood.PurchasePrice,
			apiGood.TradeVolume,
		)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to create trade good: %v", err), nil)
			return fmt.Errorf("failed to create trade good: %w", err)
		}
		tradeGoods = append(tradeGoods, *good)
	}

	// Persist market data
	err = s.marketRepo.UpsertMarketData(ctx, playerID, waypointSymbol, tradeGoods, time.Now())
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to persist market data for %s: %v", waypointSymbol, err), nil)
		return fmt.Errorf("failed to persist market data: %w", err)
	}

	logger.Log("INFO", fmt.Sprintf("[MarketScanner] Successfully scanned and saved market data for %s (%d goods)", waypointSymbol, len(tradeGoods)), nil)

	return nil
}

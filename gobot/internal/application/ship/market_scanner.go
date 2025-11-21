package ship

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MarketScanner handles automatic market scanning and data persistence
type MarketScanner struct {
	apiClient  domainPorts.APIClient
	marketRepo scoutingQuery.MarketRepository
	playerRepo player.PlayerRepository
}

// NewMarketScanner creates a new market scanner service
func NewMarketScanner(
	apiClient domainPorts.APIClient,
	marketRepo scoutingQuery.MarketRepository,
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

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to get player token: %v", err), nil)
		return fmt.Errorf("failed to get player token: %w", err)
	}

	systemSymbol := shared.ExtractSystemSymbol(waypointSymbol)
	logger.Log("INFO", fmt.Sprintf("[MarketScanner] Scanning market at %s", waypointSymbol), nil)

	marketData, err := s.apiClient.GetMarket(ctx, systemSymbol, waypointSymbol, token)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to get market data for %s: %v", waypointSymbol, err), nil)
		return fmt.Errorf("failed to get market data for %s: %w", waypointSymbol, err)
	}

	tradeGoods, err := s.convertAPIGoodsToDomain(marketData.TradeGoods, logger)
	if err != nil {
		return err
	}

	err = s.marketRepo.UpsertMarketData(ctx, playerID, waypointSymbol, tradeGoods, time.Now())
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to persist market data for %s: %v", waypointSymbol, err), nil)
		return fmt.Errorf("failed to persist market data: %w", err)
	}

	logger.Log("INFO", fmt.Sprintf("[MarketScanner] Successfully scanned and saved market data for %s (%d goods)", waypointSymbol, len(tradeGoods)), nil)

	return nil
}

func (s *MarketScanner) convertAPIGoodsToDomain(apiGoods []domainPorts.TradeGoodData, logger common.ContainerLogger) ([]market.TradeGood, error) {
	tradeGoods := make([]market.TradeGood, 0, len(apiGoods))
	for _, apiGood := range apiGoods {
		good, err := market.NewTradeGood(
			apiGood.Symbol,
			&apiGood.Supply,
			nil,
			apiGood.SellPrice,
			apiGood.PurchasePrice,
			apiGood.TradeVolume,
		)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to create trade good: %v", err), nil)
			return nil, fmt.Errorf("failed to create trade good: %w", err)
		}
		tradeGoods = append(tradeGoods, *good)
	}
	return tradeGoods, nil
}

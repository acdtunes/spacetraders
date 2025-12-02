package ship

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MarketScanner handles automatic market scanning and data persistence
type MarketScanner struct {
	apiClient        domainPorts.APIClient
	marketRepo       scoutingQuery.MarketRepository
	playerRepo       player.PlayerRepository
	priceHistoryRepo market.MarketPriceHistoryRepository
}

// NewMarketScanner creates a new market scanner service
func NewMarketScanner(
	apiClient domainPorts.APIClient,
	marketRepo scoutingQuery.MarketRepository,
	playerRepo player.PlayerRepository,
	priceHistoryRepo market.MarketPriceHistoryRepository,
) *MarketScanner {
	return &MarketScanner{
		apiClient:        apiClient,
		marketRepo:       marketRepo,
		playerRepo:       playerRepo,
		priceHistoryRepo: priceHistoryRepo,
	}
}

// ScanAndSaveMarket scans a market at the given waypoint and saves the data to the database.
// This is a non-fatal operation - errors are logged but do not fail the caller's operation.
func (s *MarketScanner) ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error {
	logger := common.LoggerFromContext(ctx)

	// Start timing for metrics
	startTime := time.Now()

	// Get existing market data for price history comparison
	existingMarket, _ := s.marketRepo.GetMarketData(ctx, waypointSymbol, int(playerID))

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to get player token: %v", err), nil)
		// Record failed scan in metrics
		if collector := metrics.GetGlobalMarketCollector(); collector != nil {
			collector.RecordScan(int(playerID), waypointSymbol, time.Since(startTime), err)
		}
		return fmt.Errorf("failed to get player token: %w", err)
	}

	systemSymbol := shared.ExtractSystemSymbol(waypointSymbol)
	logger.Log("INFO", fmt.Sprintf("[MarketScanner] Scanning market at %s", waypointSymbol), nil)

	marketData, err := s.apiClient.GetMarket(ctx, systemSymbol, waypointSymbol, token)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to get market data for %s: %v", waypointSymbol, err), nil)
		// Record failed scan in metrics
		if collector := metrics.GetGlobalMarketCollector(); collector != nil {
			collector.RecordScan(int(playerID), waypointSymbol, time.Since(startTime), err)
		}
		return fmt.Errorf("failed to get market data for %s: %w", waypointSymbol, err)
	}

	tradeGoods, err := s.convertAPIGoodsToDomain(marketData.TradeGoods, logger)
	if err != nil {
		// Record failed scan in metrics
		if collector := metrics.GetGlobalMarketCollector(); collector != nil {
			collector.RecordScan(int(playerID), waypointSymbol, time.Since(startTime), err)
		}
		return err
	}

	err = s.marketRepo.UpsertMarketData(ctx, playerID, waypointSymbol, tradeGoods, time.Now())
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to persist market data for %s: %v", waypointSymbol, err), nil)
		// Record failed scan in metrics
		if collector := metrics.GetGlobalMarketCollector(); collector != nil {
			collector.RecordScan(int(playerID), waypointSymbol, time.Since(startTime), err)
		}
		return fmt.Errorf("failed to persist market data: %w", err)
	}

	// Record price changes in history if repository is available
	if s.priceHistoryRepo != nil && existingMarket != nil {
		s.recordPriceChanges(ctx, existingMarket, waypointSymbol, tradeGoods, int(playerID), logger)
	}

	logger.Log("INFO", fmt.Sprintf("[MarketScanner] Successfully scanned and saved market data for %s (%d goods)", waypointSymbol, len(tradeGoods)), nil)

	// Record successful scan in metrics
	if collector := metrics.GetGlobalMarketCollector(); collector != nil {
		collector.RecordScan(int(playerID), waypointSymbol, time.Since(startTime), nil)
	}

	return nil
}

func (s *MarketScanner) convertAPIGoodsToDomain(apiGoods []domainPorts.TradeGoodData, logger common.ContainerLogger) ([]market.TradeGood, error) {
	tradeGoods := make([]market.TradeGood, 0, len(apiGoods))
	for _, apiGood := range apiGoods {
		good, err := market.NewTradeGood(
			apiGood.Symbol,
			&apiGood.Supply,
			&apiGood.Activity,
			apiGood.SellPrice,
			apiGood.PurchasePrice,
			apiGood.TradeVolume,
			market.TradeType(apiGood.TradeType),
		)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to create trade good: %v", err), nil)
			return nil, fmt.Errorf("failed to create trade good: %w", err)
		}
		tradeGoods = append(tradeGoods, *good)
	}
	return tradeGoods, nil
}

// recordPriceChanges compares old and new market data and records changes to price history
func (s *MarketScanner) recordPriceChanges(
	ctx context.Context,
	existingMarket *market.Market,
	waypointSymbol string,
	newGoods []market.TradeGood,
	playerID int,
	logger common.ContainerLogger,
) {
	// Build map of old goods for quick lookup
	oldGoods := make(map[string]market.TradeGood)
	for _, good := range existingMarket.TradeGoods() {
		oldGoods[good.Symbol()] = good
	}

	// Compare each good in new market data
	for _, newGood := range newGoods {
		oldGood, exists := oldGoods[newGood.Symbol()]

		// Record if good is new or any relevant field changed
		if !exists || s.pricesChanged(&oldGood, &newGood) {
			playerIDObj, err := shared.NewPlayerID(playerID)
			if err != nil {
				logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Invalid player ID: %v", err), nil)
				continue
			}

			history, err := market.NewMarketPriceHistory(
				waypointSymbol,
				newGood.Symbol(),
				playerIDObj,
				newGood.PurchasePrice(),
				newGood.SellPrice(),
				newGood.Supply(),
				newGood.Activity(),
				newGood.TradeVolume(),
			)

			if err != nil {
				logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to create price history: %v", err), nil)
				continue
			}

			if err := s.priceHistoryRepo.RecordPriceChange(ctx, history); err != nil {
				logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to record price change: %v", err), nil)
				// Don't fail the scan if price history recording fails
			}
		}
	}
}

// pricesChanged checks if any relevant field changed between old and new trade goods
func (s *MarketScanner) pricesChanged(old, new *market.TradeGood) bool {
	if old.PurchasePrice() != new.PurchasePrice() {
		return true
	}
	if old.SellPrice() != new.SellPrice() {
		return true
	}
	if old.TradeVolume() != new.TradeVolume() {
		return true
	}

	// Compare supply (both could be nil)
	oldSupply := old.Supply()
	newSupply := new.Supply()
	if (oldSupply == nil) != (newSupply == nil) {
		return true
	}
	if oldSupply != nil && newSupply != nil && *oldSupply != *newSupply {
		return true
	}

	// Compare activity (both could be nil)
	oldActivity := old.Activity()
	newActivity := new.Activity()
	if (oldActivity == nil) != (newActivity == nil) {
		return true
	}
	if oldActivity != nil && newActivity != nil && *oldActivity != *newActivity {
		return true
	}

	return false
}

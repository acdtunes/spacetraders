package persistence

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// TradingMarketRepositoryAdapter adapts MarketRepositoryGORM to trading.MarketRepository interface
// This adapter is needed because the persistence layer and trading domain have different signatures
type TradingMarketRepositoryAdapter struct {
	marketRepo *MarketRepositoryGORM
}

// NewTradingMarketRepositoryAdapter creates a new adapter
func NewTradingMarketRepositoryAdapter(marketRepo *MarketRepositoryGORM) *TradingMarketRepositoryAdapter {
	return &TradingMarketRepositoryAdapter{
		marketRepo: marketRepo,
	}
}

// GetMarketData adapts the method signature from persistence to trading domain
func (a *TradingMarketRepositoryAdapter) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*trading.Market, error) {
	// Call the persistence layer with parameters in the correct order
	persistenceMarket, err := a.marketRepo.GetMarketData(ctx, uint(playerID), waypointSymbol)
	if err != nil {
		return nil, err
	}

	if persistenceMarket == nil {
		return nil, nil
	}

	// Convert from persistence Market to trading Market
	tradeGoods := make([]trading.TradeGood, len(persistenceMarket.TradeGoods()))
	for i, good := range persistenceMarket.TradeGoods() {
		supply := ""
		if good.Supply() != nil {
			supply = *good.Supply()
		}
		tradeGoods[i] = trading.TradeGood{
			Symbol:        good.Symbol(),
			Supply:        supply,
			SellPrice:     good.SellPrice(),
			PurchasePrice: good.PurchasePrice(),
			TradeVolume:   good.TradeVolume(),
		}
	}

	tradingMarket, err := trading.NewMarket(persistenceMarket.WaypointSymbol(), tradeGoods)
	if err != nil {
		return nil, err
	}
	return tradingMarket, nil
}

// FindCheapestMarketSelling adapts the method from persistence to trading domain
func (a *TradingMarketRepositoryAdapter) FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*trading.CheapestMarketResult, error) {
	// Call the persistence layer (signature: goodSymbol, systemSymbol, playerID)
	result, err := a.marketRepo.FindCheapestMarketSelling(ctx, goodSymbol, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}

	if result == nil {
		return nil, nil
	}

	// Convert result
	return &trading.CheapestMarketResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.TradeSymbol,
		SellPrice:      result.SellPrice,
		Supply:         result.Supply,
	}, nil
}

// FindBestMarketBuying adapts the method from persistence to trading domain
func (a *TradingMarketRepositoryAdapter) FindBestMarketBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*trading.BestMarketBuyingResult, error) {
	result, err := a.marketRepo.FindBestMarketBuying(ctx, goodSymbol, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}

	if result == nil {
		return nil, nil
	}

	return &trading.BestMarketBuyingResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.TradeSymbol,
		PurchasePrice:  result.PurchasePrice,
		Supply:         result.Supply,
	}, nil
}

// ListMarketsInSystem adapts the method from persistence to return market.Market slice
func (a *TradingMarketRepositoryAdapter) ListMarketsInSystem(ctx context.Context, playerID uint, systemSymbol string, maxAgeMinutes int) ([]market.Market, error) {
	return a.marketRepo.ListMarketsInSystem(ctx, playerID, systemSymbol, maxAgeMinutes)
}

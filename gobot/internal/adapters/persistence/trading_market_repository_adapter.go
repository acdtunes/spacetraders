package persistence

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// MarketRepositoryAdapter adapts MarketRepositoryGORM to market.MarketRepository interface
// This adapter handles parameter type conversions (int vs uint for playerID)
type MarketRepositoryAdapter struct {
	marketRepo *MarketRepositoryGORM
}

// NewMarketRepositoryAdapter creates a new adapter
func NewMarketRepositoryAdapter(marketRepo *MarketRepositoryGORM) *MarketRepositoryAdapter {
	return &MarketRepositoryAdapter{
		marketRepo: marketRepo,
	}
}

// GetMarketData adapts the method signature from persistence (uint playerID) to market domain (int playerID)
func (a *MarketRepositoryAdapter) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	// Call the persistence layer (now uses int directly, no conversion needed)
	return a.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
}

// FindCheapestMarketSelling passes through to the underlying repository
func (a *MarketRepositoryAdapter) FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.CheapestMarketResult, error) {
	return a.marketRepo.FindCheapestMarketSelling(ctx, goodSymbol, systemSymbol, playerID)
}

// FindCheapestMarketSellingWithSupply passes through to the underlying repository
func (a *MarketRepositoryAdapter) FindCheapestMarketSellingWithSupply(ctx context.Context, goodSymbol, systemSymbol string, playerID int, supplyLevel string) (*market.CheapestMarketResult, error) {
	return a.marketRepo.FindCheapestMarketSellingWithSupply(ctx, goodSymbol, systemSymbol, playerID, supplyLevel)
}

// FindBestMarketBuying passes through to the underlying repository
func (a *MarketRepositoryAdapter) FindBestMarketBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.BestMarketBuyingResult, error) {
	return a.marketRepo.FindBestMarketBuying(ctx, goodSymbol, systemSymbol, playerID)
}

// FindBestMarketForBuying passes through to the underlying repository
func (a *MarketRepositoryAdapter) FindBestMarketForBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.BestBuyingMarketResult, error) {
	return a.marketRepo.FindBestMarketForBuying(ctx, goodSymbol, systemSymbol, playerID)
}

// ListMarketsInSystem passes through to the underlying repository
func (a *MarketRepositoryAdapter) ListMarketsInSystem(ctx context.Context, playerID uint, systemSymbol string, maxAgeMinutes int) ([]market.Market, error) {
	return a.marketRepo.ListMarketsInSystem(ctx, playerID, systemSymbol, maxAgeMinutes)
}

// FindAllMarketsInSystem passes through to the underlying repository
func (a *MarketRepositoryAdapter) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return a.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
}

// FindFactoryForGood passes through to the underlying repository
func (a *MarketRepositoryAdapter) FindFactoryForGood(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.FactoryResult, error) {
	return a.marketRepo.FindFactoryForGood(ctx, goodSymbol, systemSymbol, playerID)
}

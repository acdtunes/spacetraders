package scouting

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// MarketRepository defines operations for market data persistence
type MarketRepository interface {
	GetMarketData(ctx context.Context, playerID uint, waypointSymbol string) (*market.Market, error)
	UpsertMarketData(ctx context.Context, playerID uint, waypointSymbol string, goods []market.TradeGood, timestamp time.Time) error
	ListMarketsInSystem(ctx context.Context, playerID uint, systemSymbol string, maxAgeMinutes int) ([]market.Market, error)
}

// GetMarketDataQuery - Query to retrieve market data for a waypoint
type GetMarketDataQuery struct {
	PlayerID       uint
	WaypointSymbol string
}

// GetMarketDataResponse - Response containing market data
type GetMarketDataResponse struct {
	Market *market.Market
}

// GetMarketDataHandler - Handles market data queries
type GetMarketDataHandler struct {
	marketRepo MarketRepository
}

// NewGetMarketDataHandler creates a new market data query handler
func NewGetMarketDataHandler(marketRepo MarketRepository) *GetMarketDataHandler {
	return &GetMarketDataHandler{
		marketRepo: marketRepo,
	}
}

// Handle executes the get market data query
func (h *GetMarketDataHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*GetMarketDataQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}
	// Get market data from repository
	marketData, err := h.marketRepo.GetMarketData(ctx, query.PlayerID, query.WaypointSymbol)
	if err != nil {
		return nil, err
	}

	return &GetMarketDataResponse{
		Market: marketData,
	}, nil
}

// ListMarketDataQuery - Query to retrieve all market data in a system
type ListMarketDataQuery struct {
	PlayerID      uint
	SystemSymbol  string
	MaxAgeMinutes int
}

// ListMarketDataResponse - Response containing list of markets
type ListMarketDataResponse struct {
	Markets []market.Market
}

// ListMarketDataHandler - Handles list market data queries
type ListMarketDataHandler struct {
	marketRepo MarketRepository
}

// NewListMarketDataHandler creates a new list market data query handler
func NewListMarketDataHandler(marketRepo MarketRepository) *ListMarketDataHandler {
	return &ListMarketDataHandler{
		marketRepo: marketRepo,
	}
}

// Handle executes the list market data query
func (h *ListMarketDataHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*ListMarketDataQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}
	// Get all markets in system from repository
	markets, err := h.marketRepo.ListMarketsInSystem(ctx, query.PlayerID, query.SystemSymbol, query.MaxAgeMinutes)
	if err != nil {
		return nil, err
	}

	return &ListMarketDataResponse{
		Markets: markets,
	}, nil
}

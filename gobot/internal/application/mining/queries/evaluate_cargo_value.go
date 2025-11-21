package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// CargoItemValue represents a cargo item with its market value
type CargoItemValue struct {
	Symbol string
	Units  int
	Price  int // Market purchase price (what market pays us)
}

// EvaluateCargoValueQuery - Query to evaluate cargo and determine what to keep/jettison
type EvaluateCargoValueQuery struct {
	CargoItems        []CargoItemValue // Items to evaluate
	MinPriceThreshold int              // Minimum price to keep (jettison if below)
	SystemSymbol      string           // System to check markets in
	PlayerID          int
}

// EvaluateCargoValueResponse - Response with items to keep and jettison
type EvaluateCargoValueResponse struct {
	KeepItems     []CargoItemValue // Top N by value
	JettisonItems []CargoItemValue // Rest to jettison
}

// EvaluateCargoValueHandler - Handles cargo value evaluation queries
type EvaluateCargoValueHandler struct {
	marketRepo market.MarketRepository
}

// NewEvaluateCargoValueHandler creates a new evaluate cargo value handler
func NewEvaluateCargoValueHandler(
	marketRepo market.MarketRepository,
) *EvaluateCargoValueHandler {
	return &EvaluateCargoValueHandler{
		marketRepo: marketRepo,
	}
}

// Handle executes the evaluate cargo value query
func (h *EvaluateCargoValueHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*EvaluateCargoValueQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)

	// If no items, return empty response
	if len(query.CargoItems) == 0 {
		return &EvaluateCargoValueResponse{
			KeepItems:     []CargoItemValue{},
			JettisonItems: []CargoItemValue{},
		}, nil
	}

	logger.Log("INFO", "Cargo value evaluation initiated", map[string]interface{}{
		"action":            "evaluate_cargo",
		"item_count":        len(query.CargoItems),
		"price_threshold":   query.MinPriceThreshold,
		"system_symbol":     query.SystemSymbol,
	})

	// 1. Fetch market prices for all cargo items
	itemValues := make([]CargoItemValue, len(query.CargoItems))
	for i, item := range query.CargoItems {
		// If price is already set, use it
		if item.Price > 0 {
			itemValues[i] = item
			continue
		}

		// Otherwise, look up the market price
		price, err := h.getBestMarketPrice(ctx, item.Symbol, query.SystemSymbol, query.PlayerID)
		if err != nil {
			// If we can't find market data, use 0 (item will be jettisoned)
			logger.Log("WARNING", "Market price lookup failed for cargo item", map[string]interface{}{
				"action":        "price_lookup_failed",
				"cargo_symbol":  item.Symbol,
				"system_symbol": query.SystemSymbol,
				"error":         err.Error(),
			})
			price = 0
		}

		itemValues[i] = CargoItemValue{
			Symbol: item.Symbol,
			Units:  item.Units,
			Price:  price,
		}
	}

	// 2. Split into keep (>= threshold) and jettison (< threshold)
	keepItems := []CargoItemValue{}
	jettisonItems := []CargoItemValue{}

	for _, item := range itemValues {
		if item.Price >= query.MinPriceThreshold {
			keepItems = append(keepItems, item)
		} else {
			jettisonItems = append(jettisonItems, item)
		}
	}

	logger.Log("INFO", "Cargo value evaluation completed", map[string]interface{}{
		"action":         "cargo_evaluation_complete",
		"keep_count":     len(keepItems),
		"jettison_count": len(jettisonItems),
		"threshold":      query.MinPriceThreshold,
	})

	return &EvaluateCargoValueResponse{
		KeepItems:     keepItems,
		JettisonItems: jettisonItems,
	}, nil
}

// getBestMarketPrice finds the best purchase price for a good in the system
func (h *EvaluateCargoValueHandler) getBestMarketPrice(ctx context.Context, symbol, systemSymbol string, playerID int) (int, error) {
	// Use FindBestMarketBuying to find the market that pays the most for this good
	result, err := h.marketRepo.FindBestMarketBuying(ctx, symbol, systemSymbol, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to find market: %w", err)
	}

	if result == nil {
		return 0, fmt.Errorf("no markets found for %s in system %s", symbol, systemSymbol)
	}

	return result.PurchasePrice, nil
}

// getSystemFromWaypoint extracts the system symbol from a waypoint symbol
// Finds the last hyphen and returns everything before it
func getSystemFromWaypoint(waypointSymbol string) string {
	for i := len(waypointSymbol) - 1; i >= 0; i-- {
		if waypointSymbol[i] == '-' {
			return waypointSymbol[:i]
		}
	}
	return waypointSymbol
}

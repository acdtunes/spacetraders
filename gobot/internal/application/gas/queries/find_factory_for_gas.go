package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FindFactoryForGasQuery finds a factory that imports a given gas type
type FindFactoryForGasQuery struct {
	GasSymbol    string // e.g., "LIQUID_HYDROGEN"
	SystemSymbol string // Prefer factories in same system
	PlayerID     int
	ShipLocation *shared.Waypoint // Current ship location for distance calculation (unused for now)
}

// FindFactoryForGasResponse contains the factory with lowest gas supply
type FindFactoryForGasResponse struct {
	FactoryWaypoint string
	SupplyLevel     string  // "SCARCE", "LIMITED", "MODERATE", "HIGH", "ABUNDANT"
	Distance        float64 // From current ship location (0 if not calculated)
	Found           bool
}

// FindFactoryForGasHandler handles the query for finding factories needing gas
type FindFactoryForGasHandler struct {
	marketRepo market.MarketRepository
}

// NewFindFactoryForGasHandler creates a new handler
func NewFindFactoryForGasHandler(marketRepo market.MarketRepository) *FindFactoryForGasHandler {
	return &FindFactoryForGasHandler{
		marketRepo: marketRepo,
	}
}

// Handle executes the query to find a factory with low gas supply
func (h *FindFactoryForGasHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*FindFactoryForGasQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	response := &FindFactoryForGasResponse{
		Found: false,
	}

	// 1. Get all market waypoints in the system
	marketWaypoints, err := h.marketRepo.FindAllMarketsInSystem(ctx, query.SystemSymbol, query.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets in system: %w", err)
	}

	// 2. Lower supply levels indicate higher demand
	supplyPriority := map[string]int{
		"SCARCE":   1, // Highest priority - factory urgently needs gas
		"LIMITED":  2,
		"MODERATE": 3,
		"HIGH":     4,
		"ABUNDANT": 5, // Lowest priority - factory is well stocked
	}

	var bestFactory string
	var bestSupply string
	bestPriority := 999

	// 3. Check each market for the gas import
	for _, waypointSymbol := range marketWaypoints {
		mkt, err := h.marketRepo.GetMarketData(ctx, waypointSymbol, query.PlayerID)
		if err != nil {
			continue // Skip markets we can't fetch
		}
		if mkt == nil {
			continue
		}

		// Check if this market imports (not exports) the gas
		good := mkt.FindGood(query.GasSymbol)
		if good == nil {
			continue
		}

		// Only consider markets that IMPORT the gas (factories)
		// Skip EXPORT markets (gas producers) and EXCHANGE markets
		if good.TradeType() != market.TradeTypeImport {
			continue
		}

		// Get supply level - supply is a pointer
		supply := ""
		if good.Supply() != nil {
			supply = *good.Supply()
		}

		// Get supply level priority
		priority, ok := supplyPriority[supply]
		if !ok {
			priority = 3 // Default to MODERATE if unknown
		}

		// Select factory with lowest supply (highest demand)
		if priority < bestPriority {
			bestFactory = mkt.WaypointSymbol()
			bestSupply = supply
			bestPriority = priority
		}
	}

	if bestFactory == "" {
		return response, nil // No factory found that imports this gas
	}

	response.FactoryWaypoint = bestFactory
	response.SupplyLevel = bestSupply
	response.Distance = 0 // Distance calculation would require waypoint coordinates
	response.Found = true

	return response, nil
}

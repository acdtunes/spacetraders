package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// EvaluateContractProfitabilityQuery is a query to evaluate contract profitability
type EvaluateContractProfitabilityQuery struct {
	Contract        *domainContract.Contract
	ShipSymbol      string
	PlayerID        int
	FuelCostPerTrip int // Fuel cost per round trip (for delivery and return)
}

// ProfitabilityResult contains the profitability evaluation results
type ProfitabilityResult struct {
	IsProfitable           bool
	NetProfit              int
	PurchaseCost           int
	TripsRequired          int
	CheapestMarketWaypoint string
	Reason                 string
}

// EvaluateContractProfitabilityHandler evaluates contract profitability
// This is a thin orchestrator that:
// 1. Fetches required data (ship, market prices)
// 2. Builds ProfitabilityContext
// 3. Delegates calculation to Contract.EvaluateProfitability()
type EvaluateContractProfitabilityHandler struct {
	shipRepo   navigation.ShipRepository
	marketRepo market.MarketRepository
}

// NewEvaluateContractProfitabilityHandler creates a new handler
func NewEvaluateContractProfitabilityHandler(
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
) *EvaluateContractProfitabilityHandler {
	return &EvaluateContractProfitabilityHandler{
		shipRepo:   shipRepo,
		marketRepo: marketRepo,
	}
}

// Handle executes the profitability evaluation query
func (h *EvaluateContractProfitabilityHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*EvaluateContractProfitabilityQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Fetch ship to get cargo capacity
	ship, err := h.shipRepo.FindBySymbol(ctx, query.ShipSymbol, query.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	// 2. Build market prices map by fetching cheapest markets for each delivery
	marketPrices := make(map[string]int)
	var cheapestMarketWaypoint string

	for _, delivery := range query.Contract.Terms().Deliveries {
		unitsNeeded := delivery.UnitsRequired - delivery.UnitsFulfilled
		if unitsNeeded == 0 {
			continue // Already fulfilled
		}

		// Extract system from destination (find last hyphen)
		systemSymbol := delivery.DestinationSymbol
		for i := len(delivery.DestinationSymbol) - 1; i >= 0; i-- {
			if delivery.DestinationSymbol[i] == '-' {
				systemSymbol = delivery.DestinationSymbol[:i]
				break
			}
		}

		// Find cheapest market selling this good
		cheapestMarket, err := h.marketRepo.FindCheapestMarketSelling(ctx, delivery.TradeSymbol, systemSymbol, query.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to find market for %s: %w", delivery.TradeSymbol, err)
		}
		if cheapestMarket == nil {
			return nil, fmt.Errorf("no market found selling %s in system %s", delivery.TradeSymbol, systemSymbol)
		}

		// Store price for this trade good
		marketPrices[delivery.TradeSymbol] = cheapestMarket.SellPrice

		// Store first cheapest market waypoint (primary purchasing location)
		if cheapestMarketWaypoint == "" {
			cheapestMarketWaypoint = cheapestMarket.WaypointSymbol
		}
	}

	// 3. Build profitability context
	profitabilityCtx := domainContract.ProfitabilityContext{
		MarketPrices:           marketPrices,
		CargoCapacity:          ship.Cargo().Capacity,
		FuelCostPerTrip:        query.FuelCostPerTrip,
		CheapestMarketWaypoint: cheapestMarketWaypoint,
	}

	// 4. Delegate calculation to domain entity
	evaluation, err := query.Contract.EvaluateProfitability(profitabilityCtx)
	if err != nil {
		return nil, fmt.Errorf("profitability evaluation failed: %w", err)
	}

	// 5. Convert domain result to application DTO
	return &ProfitabilityResult{
		IsProfitable:           evaluation.IsProfitable,
		NetProfit:              evaluation.NetProfit,
		PurchaseCost:           evaluation.PurchaseCost,
		TripsRequired:          evaluation.TripsRequired,
		CheapestMarketWaypoint: evaluation.CheapestMarketWaypoint,
		Reason:                 evaluation.Reason,
	}, nil
}

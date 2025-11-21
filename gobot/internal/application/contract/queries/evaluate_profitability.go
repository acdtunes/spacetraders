package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// EvaluateContractProfitabilityQuery is a query to evaluate contract profitability
type EvaluateContractProfitabilityQuery struct {
	Contract        *domainContract.Contract
	ShipSymbol      string
	PlayerID   shared.PlayerID
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

	ship, err := h.fetchShip(ctx, query.ShipSymbol, query.PlayerID)
	if err != nil {
		return nil, err
	}

	marketPrices, cheapestMarketWaypoint, err := h.buildMarketPricesMap(ctx, query)
	if err != nil {
		return nil, err
	}

	profitabilityCtx := h.buildProfitabilityContext(ship, marketPrices, cheapestMarketWaypoint, query.FuelCostPerTrip)

	evaluation, err := h.delegateCalculationToDomain(query.Contract, profitabilityCtx)
	if err != nil {
		return nil, err
	}

	return h.convertToApplicationDTO(evaluation), nil
}

func (h *EvaluateContractProfitabilityHandler) fetchShip(ctx context.Context, shipSymbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

func (h *EvaluateContractProfitabilityHandler) buildMarketPricesMap(ctx context.Context, query *EvaluateContractProfitabilityQuery) (map[string]int, string, error) {
	marketPrices := make(map[string]int)
	var cheapestMarketWaypoint string

	for _, delivery := range query.Contract.Terms().Deliveries {
		unitsNeeded := delivery.UnitsRequired - delivery.UnitsFulfilled
		if unitsNeeded == 0 {
			continue
		}

		systemSymbol := shared.ExtractSystemSymbol(delivery.DestinationSymbol)

		cheapestMarket, err := h.findCheapestMarket(ctx, delivery.TradeSymbol, systemSymbol, query.PlayerID.Value())
		if err != nil {
			return nil, "", err
		}

		marketPrices[delivery.TradeSymbol] = cheapestMarket.SellPrice

		if cheapestMarketWaypoint == "" {
			cheapestMarketWaypoint = cheapestMarket.WaypointSymbol
		}
	}

	return marketPrices, cheapestMarketWaypoint, nil
}

func (h *EvaluateContractProfitabilityHandler) findCheapestMarket(ctx context.Context, tradeSymbol string, systemSymbol string, playerID int) (*market.CheapestMarketResult, error) {
	cheapestMarket, err := h.marketRepo.FindCheapestMarketSelling(ctx, tradeSymbol, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find market for %s: %w", tradeSymbol, err)
	}
	if cheapestMarket == nil {
		return nil, fmt.Errorf("no market found selling %s in system %s", tradeSymbol, systemSymbol)
	}
	return cheapestMarket, nil
}

func (h *EvaluateContractProfitabilityHandler) buildProfitabilityContext(ship *navigation.Ship, marketPrices map[string]int, cheapestMarketWaypoint string, fuelCostPerTrip int) domainContract.ProfitabilityContext {
	return domainContract.ProfitabilityContext{
		MarketPrices:           marketPrices,
		CargoCapacity:          ship.Cargo().Capacity,
		FuelCostPerTrip:        fuelCostPerTrip,
		CheapestMarketWaypoint: cheapestMarketWaypoint,
	}
}

func (h *EvaluateContractProfitabilityHandler) delegateCalculationToDomain(contract *domainContract.Contract, profitabilityCtx domainContract.ProfitabilityContext) (*domainContract.ProfitabilityEvaluation, error) {
	evaluation, err := contract.EvaluateProfitability(profitabilityCtx)
	if err != nil {
		return nil, fmt.Errorf("profitability evaluation failed: %w", err)
	}
	return evaluation, nil
}

func (h *EvaluateContractProfitabilityHandler) convertToApplicationDTO(evaluation *domainContract.ProfitabilityEvaluation) *ProfitabilityResult {
	return &ProfitabilityResult{
		IsProfitable:           evaluation.IsProfitable,
		NetProfit:              evaluation.NetProfit,
		PurchaseCost:           evaluation.PurchaseCost,
		TripsRequired:          evaluation.TripsRequired,
		CheapestMarketWaypoint: evaluation.CheapestMarketWaypoint,
		Reason:                 evaluation.Reason,
	}
}

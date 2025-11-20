package contract

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

const (
	// MinProfitThreshold defines the minimum acceptable net profit for contract evaluation.
	// Contracts with profits >= -5000 are considered profitable (accepts losses up to 5000 credits).
	// This matches the Python implementation's min_profit_threshold = -5000.
	MinProfitThreshold = -5000
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
// Following the exact Python calculation formula:
//
// total_payment = contract.payment.on_accepted + contract.payment.on_fulfilled
// purchase_cost = sum(cheapest_market.sell_price * units_needed for each delivery)
// trips_required = ceil(total_units / cargo_capacity)
// fuel_cost = trips_required * fuel_cost_per_trip
// net_profit = total_payment - (purchase_cost + fuel_cost)
// is_profitable = net_profit >= -5000  (accepts losses up to 5000 credits)
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

	// 1. Load ship to get cargo capacity
	ship, err := h.shipRepo.FindBySymbol(ctx, query.ShipSymbol, query.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	// 2. Calculate total payment (Python: total_payment = contract.payment.on_accepted + contract.payment.on_fulfilled)
	totalPayment := query.Contract.Terms().Payment.OnAccepted + query.Contract.Terms().Payment.OnFulfilled

	// 3. Calculate purchase cost and total units needed
	// Python logic:
	//   for delivery in contract.deliveries:
	//       units_needed = delivery.units_required - delivery.units_fulfilled
	//       cheapest_market = find_cheapest_market_selling(delivery.trade_symbol, system)
	//       purchase_cost += cheapest_market.sell_price * units_needed
	purchaseCost := 0
	totalUnits := 0
	var cheapestMarketWaypoint string

	for _, delivery := range query.Contract.Terms().Deliveries {
		unitsNeeded := delivery.UnitsRequired - delivery.UnitsFulfilled
		if unitsNeeded == 0 {
			continue
		}

		// Extract system from destination (X1-GZ7-A1 -> X1-GZ7)
		system := extractSystem(delivery.DestinationSymbol)

		// Find cheapest market selling this good
		cheapestMarket, err := h.marketRepo.FindCheapestMarketSelling(ctx, delivery.TradeSymbol, system, query.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to find market for %s: %w", delivery.TradeSymbol, err)
		}
		if cheapestMarket == nil {
			return nil, fmt.Errorf("no market found selling %s in system %s", delivery.TradeSymbol, system)
		}

		// Store first cheapest market waypoint (primary market for purchasing)
		if cheapestMarketWaypoint == "" {
			cheapestMarketWaypoint = cheapestMarket.WaypointSymbol
		}

		purchaseCost += cheapestMarket.SellPrice * unitsNeeded
		totalUnits += unitsNeeded
	}

	// 4. Calculate trips required (Python: trips_required = ceil(total_units / cargo_capacity))
	cargoCapacity := ship.Cargo().Capacity
	tripsRequired := int(math.Ceil(float64(totalUnits) / float64(cargoCapacity)))

	// 5. Calculate fuel cost (Python: fuel_cost = trips_required * fuel_cost_per_trip)
	fuelCost := tripsRequired * query.FuelCostPerTrip

	// 6. Calculate net profit (Python: net_profit = total_payment - (purchase_cost + fuel_cost))
	netProfit := totalPayment - (purchaseCost + fuelCost)

	// 7. Determine profitability (Python: min_profit_threshold = -5000, is_profitable = net_profit >= min_profit_threshold)
	isProfitable := netProfit >= MinProfitThreshold

	// 8. Generate reason (Python logic)
	var reason string
	if netProfit > 0 {
		reason = "Profitable"
	} else if netProfit >= MinProfitThreshold {
		reason = "Acceptable small loss (avoids opportunity cost)"
	} else {
		reason = "Loss exceeds acceptable threshold"
	}

	return &ProfitabilityResult{
		IsProfitable:           isProfitable,
		NetProfit:              netProfit,
		PurchaseCost:           purchaseCost,
		TripsRequired:          tripsRequired,
		CheapestMarketWaypoint: cheapestMarketWaypoint,
		Reason:                 reason,
	}, nil
}

// extractSystem extracts system symbol from waypoint symbol
// Example: X1-GZ7-A1 -> X1-GZ7
func extractSystem(waypointSymbol string) string {
	parts := strings.Split(waypointSymbol, "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return waypointSymbol
}

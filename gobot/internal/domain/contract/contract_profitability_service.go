package contract

import "fmt"

// ContractProfitabilityService provides profitability analysis for contracts.
// This service separates financial analysis logic from the Contract entity,
// following the Single Responsibility Principle.
type ContractProfitabilityService struct{}

// NewContractProfitabilityService creates a new profitability service
func NewContractProfitabilityService() *ContractProfitabilityService {
	return &ContractProfitabilityService{}
}

// EvaluateProfitability calculates contract profitability given market conditions.
//
// Business Rules:
//   - total_payment = on_accepted + on_fulfilled
//   - purchase_cost = sum(market_price * units_needed for each delivery)
//   - trips_required = ceil(total_units / cargo_capacity)
//   - fuel_cost = trips_required * fuel_cost_per_trip
//   - net_profit = total_payment - (purchase_cost + fuel_cost)
//   - is_profitable = net_profit >= MinProfitThreshold (-5000)
//
// Parameters:
//   - contract: The contract to evaluate
//   - ctx: Market prices, cargo capacity, and fuel costs
//
// Returns:
//   - ProfitabilityEvaluation with all calculated metrics
//   - Error if market prices are missing for required goods
func (s *ContractProfitabilityService) EvaluateProfitability(
	contract *Contract,
	ctx ProfitabilityContext,
) (*ProfitabilityEvaluation, error) {
	// 1. Calculate total payment
	totalPayment := s.calculateTotalPayment(contract)

	// 2. Calculate purchase cost and total units needed
	purchaseCost, totalUnits, err := s.calculatePurchaseCost(contract, ctx)
	if err != nil {
		return nil, err
	}

	// 3. Calculate trips required (ceiling division)
	tripsRequired := s.calculateTripsRequired(totalUnits, ctx.CargoCapacity)

	// 4. Calculate fuel cost
	fuelCost := s.calculateFuelCost(tripsRequired, ctx.FuelCostPerTrip)

	// 5. Calculate net profit
	netProfit := s.calculateNetProfit(totalPayment, purchaseCost, fuelCost)

	// 6. Determine profitability
	isProfitable := netProfit >= MinProfitThreshold

	// 7. Generate reason
	reason := s.generateProfitabilityReason(netProfit)

	return &ProfitabilityEvaluation{
		IsProfitable:           isProfitable,
		NetProfit:              netProfit,
		TotalPayment:           totalPayment,
		PurchaseCost:           purchaseCost,
		FuelCost:               fuelCost,
		TripsRequired:          tripsRequired,
		CheapestMarketWaypoint: ctx.CheapestMarketWaypoint,
		Reason:                 reason,
	}, nil
}

// calculateTotalPayment computes the total payment from on_accepted + on_fulfilled
func (s *ContractProfitabilityService) calculateTotalPayment(contract *Contract) int {
	return contract.terms.Payment.OnAccepted + contract.terms.Payment.OnFulfilled
}

// calculatePurchaseCost computes the purchase cost and total units needed
func (s *ContractProfitabilityService) calculatePurchaseCost(
	contract *Contract,
	ctx ProfitabilityContext,
) (purchaseCost int, totalUnits int, err error) {
	for _, delivery := range contract.terms.Deliveries {
		unitsNeeded := delivery.UnitsRequired - delivery.UnitsFulfilled
		if unitsNeeded == 0 {
			continue // Delivery already fulfilled
		}

		// Look up market price for this trade good
		sellPrice, ok := ctx.MarketPrices[delivery.TradeSymbol]
		if !ok {
			return 0, 0, fmt.Errorf("missing market price for %s", delivery.TradeSymbol)
		}

		purchaseCost += sellPrice * unitsNeeded
		totalUnits += unitsNeeded
	}

	return purchaseCost, totalUnits, nil
}

// calculateTripsRequired computes the number of trips needed (ceiling division)
func (s *ContractProfitabilityService) calculateTripsRequired(totalUnits, cargoCapacity int) int {
	if cargoCapacity > 0 && totalUnits > 0 {
		return (totalUnits + cargoCapacity - 1) / cargoCapacity
	}
	return 0
}

// calculateFuelCost computes the total fuel cost
func (s *ContractProfitabilityService) calculateFuelCost(tripsRequired, fuelCostPerTrip int) int {
	return tripsRequired * fuelCostPerTrip
}

// calculateNetProfit computes the net profit
func (s *ContractProfitabilityService) calculateNetProfit(totalPayment, purchaseCost, fuelCost int) int {
	return totalPayment - (purchaseCost + fuelCost)
}

// generateProfitabilityReason creates a human-readable reason for the profitability decision
func (s *ContractProfitabilityService) generateProfitabilityReason(netProfit int) string {
	if netProfit > 0 {
		return "Profitable"
	}
	if netProfit >= MinProfitThreshold {
		return "Acceptable small loss (avoids opportunity cost)"
	}
	return "Loss exceeds acceptable threshold"
}

package contract

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type Payment struct {
	OnAccepted  int
	OnFulfilled int
}

type Delivery struct {
	TradeSymbol       string
	DestinationSymbol string
	UnitsRequired     int
	UnitsFulfilled    int
}

type Terms struct {
	Payment          Payment
	Deliveries       []Delivery
	DeadlineToAccept string
	Deadline         string
}

type Contract struct {
	contractID    string
	playerID      shared.PlayerID
	factionSymbol string
	contractType  string
	terms         Terms
	accepted      bool
	fulfilled     bool
	clock         shared.Clock
}

// NewContract creates a new contract
// The clock parameter is optional - if nil, defaults to RealClock for production use
func NewContract(contractID string, playerID shared.PlayerID, factionSymbol, contractType string, terms Terms, clock shared.Clock) (*Contract, error) {
	if contractID == "" {
		return nil, fmt.Errorf("contract ID cannot be empty")
	}
	if playerID.IsZero() {
		return nil, fmt.Errorf("invalid player ID")
	}
	if factionSymbol == "" {
		return nil, fmt.Errorf("faction symbol cannot be empty")
	}
	if len(terms.Deliveries) == 0 {
		return nil, fmt.Errorf("contract must have at least one delivery")
	}

	// Default to RealClock if not provided
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &Contract{
		contractID:    contractID,
		playerID:      playerID,
		factionSymbol: factionSymbol,
		contractType:  contractType,
		terms:         terms,
		accepted:      false,
		fulfilled:     false,
		clock:         clock,
	}, nil
}

func (c *Contract) ContractID() string        { return c.contractID }
func (c *Contract) PlayerID() shared.PlayerID { return c.playerID }
func (c *Contract) FactionSymbol() string     { return c.factionSymbol }
func (c *Contract) Type() string              { return c.contractType }
func (c *Contract) Terms() Terms              { return c.terms }
func (c *Contract) Accepted() bool            { return c.accepted }
func (c *Contract) Fulfilled() bool           { return c.fulfilled }

// Accept accepts the contract (MUTABLE - modifies in place)
func (c *Contract) Accept() error {
	if c.fulfilled {
		return fmt.Errorf("contract already fulfilled")
	}
	if c.accepted {
		return fmt.Errorf("contract already accepted")
	}
	c.accepted = true
	return nil
}

// DeliverCargo updates delivery progress (MUTABLE)
func (c *Contract) DeliverCargo(tradeSymbol string, units int) error {
	if !c.accepted {
		return fmt.Errorf("contract not accepted")
	}

	var delivery *Delivery
	for i := range c.terms.Deliveries {
		if c.terms.Deliveries[i].TradeSymbol == tradeSymbol {
			delivery = &c.terms.Deliveries[i]
			break
		}
	}

	if delivery == nil {
		return fmt.Errorf("trade symbol not in contract")
	}

	if delivery.UnitsFulfilled+units > delivery.UnitsRequired {
		return fmt.Errorf("units exceed required")
	}

	delivery.UnitsFulfilled += units
	return nil
}

// CanFulfill checks if all deliveries are complete
func (c *Contract) CanFulfill() bool {
	for _, delivery := range c.terms.Deliveries {
		if delivery.UnitsFulfilled < delivery.UnitsRequired {
			return false
		}
	}
	return true
}

// Fulfill marks contract as fulfilled (MUTABLE)
func (c *Contract) Fulfill() error {
	if !c.accepted {
		return fmt.Errorf("contract not accepted")
	}
	if !c.CanFulfill() {
		return fmt.Errorf("deliveries not complete")
	}
	c.fulfilled = true
	return nil
}

// IsExpired checks if contract is past deadline
func (c *Contract) IsExpired() bool {
	deadline, err := time.Parse(time.RFC3339, c.terms.Deadline)
	if err != nil {
		return false
	}
	return c.clock.Now().UTC().After(deadline)
}

// ProfitabilityContext contains market and ship data needed for profitability calculation
type ProfitabilityContext struct {
	// MarketPrices maps trade_symbol to sell_price at the cheapest market
	MarketPrices map[string]int
	// CargoCapacity is the ship's cargo hold capacity
	CargoCapacity int
	// FuelCostPerTrip is the fuel cost for a round trip (delivery + return)
	FuelCostPerTrip int
	// CheapestMarketWaypoint is the primary market waypoint for purchasing
	CheapestMarketWaypoint string
}

// ProfitabilityEvaluation contains the results of profitability calculation
type ProfitabilityEvaluation struct {
	IsProfitable           bool
	NetProfit              int
	TotalPayment           int
	PurchaseCost           int
	FuelCost               int
	TripsRequired          int
	CheapestMarketWaypoint string
	Reason                 string
}

const (
	// MinProfitThreshold defines the minimum acceptable net profit.
	// Contracts with profits >= -5000 are considered profitable (accepts losses up to 5000 credits).
	// This allows taking small losses to avoid opportunity cost of idle ships.
	MinProfitThreshold = -5000
)

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
//   - ctx: Market prices, cargo capacity, and fuel costs
//
// Returns:
//   - ProfitabilityEvaluation with all calculated metrics
//   - Error if market prices are missing for required goods
func (c *Contract) EvaluateProfitability(ctx ProfitabilityContext) (*ProfitabilityEvaluation, error) {
	// 1. Calculate total payment
	totalPayment := c.terms.Payment.OnAccepted + c.terms.Payment.OnFulfilled

	// 2. Calculate purchase cost and total units needed
	purchaseCost := 0
	totalUnits := 0

	for _, delivery := range c.terms.Deliveries {
		unitsNeeded := delivery.UnitsRequired - delivery.UnitsFulfilled
		if unitsNeeded == 0 {
			continue // Delivery already fulfilled
		}

		// Look up market price for this trade good
		sellPrice, ok := ctx.MarketPrices[delivery.TradeSymbol]
		if !ok {
			return nil, fmt.Errorf("missing market price for %s", delivery.TradeSymbol)
		}

		purchaseCost += sellPrice * unitsNeeded
		totalUnits += unitsNeeded
	}

	// 3. Calculate trips required (ceiling division)
	tripsRequired := 0
	if ctx.CargoCapacity > 0 && totalUnits > 0 {
		tripsRequired = (totalUnits + ctx.CargoCapacity - 1) / ctx.CargoCapacity
	}

	// 4. Calculate fuel cost
	fuelCost := tripsRequired * ctx.FuelCostPerTrip

	// 5. Calculate net profit
	netProfit := totalPayment - (purchaseCost + fuelCost)

	// 6. Determine profitability
	isProfitable := netProfit >= MinProfitThreshold

	// 7. Generate reason
	var reason string
	if netProfit > 0 {
		reason = "Profitable"
	} else if netProfit >= MinProfitThreshold {
		reason = "Acceptable small loss (avoids opportunity cost)"
	} else {
		reason = "Loss exceeds acceptable threshold"
	}

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

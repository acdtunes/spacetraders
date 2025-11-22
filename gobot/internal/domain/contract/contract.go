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

// EvaluateProfitability delegates profitability calculation to ContractProfitabilityService.
//
// This method provides a convenient API for contract profitability evaluation while
// maintaining proper separation of concerns. The actual business logic resides in
// ContractProfitabilityService.
//
// Parameters:
//   - ctx: Market prices, cargo capacity, and fuel costs
//
// Returns:
//   - ProfitabilityEvaluation with all calculated metrics
//   - Error if market prices are missing for required goods
func (c *Contract) EvaluateProfitability(ctx ProfitabilityContext) (*ProfitabilityEvaluation, error) {
	service := NewContractProfitabilityService()
	return service.EvaluateProfitability(c, ctx)
}

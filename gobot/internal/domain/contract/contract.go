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

// MarkDeliveryTermsMet reconciles a single delivery to fully fulfilled after the
// server has confirmed that good's terms are already met — a SpaceTraders 4509
// "delivery terms ... have been met" response — while the local row had lagged
// behind at a lower UnitsFulfilled. It sets UnitsFulfilled = UnitsRequired for
// ONLY the named good (server truth), so a good the server has NOT confirmed is
// never marked fulfilled and a partial over-fetch attempt does not under-record
// the true server count. Idempotent: re-marking an already-met delivery is a
// no-op. This heals the server-met/local-unmet divergence behind the sp-1pf0r
// contract-fleet wedge.
func (c *Contract) MarkDeliveryTermsMet(tradeSymbol string) error {
	if !c.accepted {
		return fmt.Errorf("contract not accepted")
	}
	for i := range c.terms.Deliveries {
		if c.terms.Deliveries[i].TradeSymbol == tradeSymbol {
			c.terms.Deliveries[i].UnitsFulfilled = c.terms.Deliveries[i].UnitsRequired
			return nil
		}
	}
	return fmt.Errorf("trade symbol not in contract")
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

// EvaluateProfitability delegates profitability calculation to ContractProfitabilityService.
//
// This method provides a convenient API for contract profitability evaluation while
// maintaining proper separation of concerns. The actual business logic resides in
// ContractProfitabilityService.
//
// ctx carries market prices, cargo capacity and fuel costs; it is not a
// context.Context. Errors when a required good has no market price.
func (c *Contract) EvaluateProfitability(ctx ProfitabilityContext) (*ProfitabilityEvaluation, error) {
	service := NewContractProfitabilityService()
	return service.EvaluateProfitability(c, ctx)
}

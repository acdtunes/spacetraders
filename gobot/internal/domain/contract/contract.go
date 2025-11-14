package contract

import (
	"fmt"
	"time"
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

type ContractTerms struct {
	Payment          Payment
	Deliveries       []Delivery
	DeadlineToAccept string
	Deadline         string
}

type Contract struct {
	contractID    string
	playerID      int
	factionSymbol string
	contractType  string
	terms         ContractTerms
	accepted      bool
	fulfilled     bool
}

// NewContract creates a new contract
func NewContract(contractID string, playerID int, factionSymbol, contractType string, terms ContractTerms) (*Contract, error) {
	if contractID == "" {
		return nil, fmt.Errorf("contract ID cannot be empty")
	}
	if playerID <= 0 {
		return nil, fmt.Errorf("invalid player ID")
	}
	if factionSymbol == "" {
		return nil, fmt.Errorf("faction symbol cannot be empty")
	}
	if len(terms.Deliveries) == 0 {
		return nil, fmt.Errorf("contract must have at least one delivery")
	}

	return &Contract{
		contractID:    contractID,
		playerID:      playerID,
		factionSymbol: factionSymbol,
		contractType:  contractType,
		terms:         terms,
		accepted:      false,
		fulfilled:     false,
	}, nil
}

func (c *Contract) ContractID() string    { return c.contractID }
func (c *Contract) PlayerID() int         { return c.playerID }
func (c *Contract) FactionSymbol() string { return c.factionSymbol }
func (c *Contract) Type() string          { return c.contractType }
func (c *Contract) Terms() ContractTerms  { return c.terms }
func (c *Contract) Accepted() bool        { return c.accepted }
func (c *Contract) Fulfilled() bool       { return c.fulfilled }

// Accept accepts the contract (MUTABLE - modifies in place)
func (c *Contract) Accept() error {
	if c.accepted {
		return fmt.Errorf("contract already accepted")
	}
	c.accepted = true
	return nil
}

// DeliverCargo updates delivery progress (MUTABLE)
func (c *Contract) DeliverCargo(tradeSymbol string, units int) error {
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
	return time.Now().UTC().After(deadline)
}

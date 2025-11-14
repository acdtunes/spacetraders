package contract

import "context"

// ContractRepository defines the interface for contract persistence operations
type ContractRepository interface {
	FindByID(ctx context.Context, contractID string, playerID int) (*Contract, error)
	FindActiveContracts(ctx context.Context, playerID int) ([]*Contract, error)
	Save(ctx context.Context, contract *Contract) error
}

type ContractData struct {
	ContractID    string
	PlayerID      int
	FactionSymbol string
	Type          string
	Terms         ContractTermsData
	Accepted      bool
	Fulfilled     bool
}

type ContractTermsData struct {
	Payment          PaymentData
	Deliveries       []DeliveryData
	DeadlineToAccept string
	Deadline         string
}

type PaymentData struct {
	OnAccepted  int
	OnFulfilled int
}

type DeliveryData struct {
	TradeSymbol       string
	DestinationSymbol string
	UnitsRequired     int
	UnitsFulfilled    int
}

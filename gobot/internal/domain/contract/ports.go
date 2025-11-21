package contract

import "context"

// ContractRepository defines the interface for contract persistence operations
type ContractRepository interface {
	FindByID(ctx context.Context, contractID string) (*Contract, error)
	FindActiveContracts(ctx context.Context, playerID int) ([]*Contract, error)
	Add(ctx context.Context, contract *Contract) error
}


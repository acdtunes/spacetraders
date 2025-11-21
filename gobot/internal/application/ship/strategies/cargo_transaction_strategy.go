package strategies

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// CargoTransactionStrategy defines the interface for different types of cargo transactions.
//
// This strategy pattern allows us to unify purchase and sell operations which share 90%
// of their logic, differing only in validation rules and the specific API call made.
//
// Implementations:
//   - PurchaseStrategy: Validates cargo space availability and purchases cargo
//   - SellStrategy: Validates cargo inventory and sells cargo
type CargoTransactionStrategy interface {
	// Execute performs the actual transaction API call
	Execute(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*TransactionResult, error)

	// ValidatePreconditions checks if the ship can perform this transaction
	// Returns error if preconditions are not met
	ValidatePreconditions(ship *navigation.Ship, goodSymbol string, units int) error

	// GetTransactionType returns a human-readable name for the transaction type
	GetTransactionType() string
}

// TransactionResult contains the accumulated results of cargo transactions.
//
// This unified result type allows both purchase and sell operations to use
// the same accumulation logic, simplifying the handler implementation.
type TransactionResult struct {
	TotalAmount      int // Total credits (cost for purchase, revenue for sell)
	UnitsProcessed   int // Total units (added for purchase, sold for sell)
	TransactionCount int // Number of API transactions executed
}

// PurchaseStrategy implements cargo purchase operations.
//
// Business rules enforced:
//   - Ship must have sufficient available cargo space
type PurchaseStrategy struct {
	apiClient infraPorts.APIClient
}

// NewPurchaseStrategy creates a new purchase strategy instance.
func NewPurchaseStrategy(apiClient infraPorts.APIClient) *PurchaseStrategy {
	return &PurchaseStrategy{
		apiClient: apiClient,
	}
}

// Execute performs a cargo purchase API call.
func (s *PurchaseStrategy) Execute(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*TransactionResult, error) {
	result, err := s.apiClient.PurchaseCargo(ctx, shipSymbol, goodSymbol, units, token)
	if err != nil {
		return nil, err
	}

	return &TransactionResult{
		TotalAmount:    result.TotalCost,
		UnitsProcessed: result.UnitsAdded,
	}, nil
}

// ValidatePreconditions checks if the ship has sufficient cargo space for purchase.
func (s *PurchaseStrategy) ValidatePreconditions(ship *navigation.Ship, goodSymbol string, units int) error {
	availableSpace := ship.AvailableCargoSpace()
	if availableSpace < units {
		return fmt.Errorf("insufficient cargo space: need %d, have %d", units, availableSpace)
	}
	return nil
}

// GetTransactionType returns "purchase" for logging and error messages.
func (s *PurchaseStrategy) GetTransactionType() string {
	return "purchase"
}

// SellStrategy implements cargo sale operations.
//
// Business rules enforced:
//   - Ship must have sufficient cargo of the specified type
type SellStrategy struct {
	apiClient infraPorts.APIClient
}

// NewSellStrategy creates a new sell strategy instance.
func NewSellStrategy(apiClient infraPorts.APIClient) *SellStrategy {
	return &SellStrategy{
		apiClient: apiClient,
	}
}

// Execute performs a cargo sale API call.
func (s *SellStrategy) Execute(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*TransactionResult, error) {
	result, err := s.apiClient.SellCargo(ctx, shipSymbol, goodSymbol, units, token)
	if err != nil {
		return nil, err
	}

	return &TransactionResult{
		TotalAmount:    result.TotalRevenue,
		UnitsProcessed: result.UnitsSold,
	}, nil
}

// ValidatePreconditions checks if the ship has sufficient cargo for sale.
func (s *SellStrategy) ValidatePreconditions(ship *navigation.Ship, goodSymbol string, units int) error {
	currentUnits := ship.Cargo().GetItemUnits(goodSymbol)
	if currentUnits < units {
		return fmt.Errorf("insufficient cargo: need %d, have %d", units, currentUnits)
	}
	return nil
}

// GetTransactionType returns "sell" for logging and error messages.
func (s *SellStrategy) GetTransactionType() string {
	return "sell"
}

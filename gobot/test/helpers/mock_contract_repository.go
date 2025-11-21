package helpers

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
)

// MockContractRepository is a test double for ContractRepository interface
type MockContractRepository struct {
	mu        sync.RWMutex
	contracts map[string]*contract.Contract // contractID -> contract

	// Error injection
	shouldError bool
	errorMsg    string
}

// NewMockContractRepository creates a new mock contract repository
func NewMockContractRepository() *MockContractRepository {
	return &MockContractRepository{
		contracts: make(map[string]*contract.Contract),
	}
}

// AddContract adds a contract to the repository
func (r *MockContractRepository) AddContract(c *contract.Contract) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contracts[c.ContractID()] = c
}

// FindByID finds a contract by ID
func (r *MockContractRepository) FindByID(ctx context.Context, contractID string, playerID int) (*contract.Contract, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.shouldError {
		return nil, fmt.Errorf("%s", r.errorMsg)
	}

	c, ok := r.contracts[contractID]
	if !ok {
		return nil, fmt.Errorf("contract not found")
	}

	// Verify player ownership
	if c.PlayerID().Value() != playerID {
		return nil, fmt.Errorf("contract not found")
	}

	return c, nil
}

// FindActiveContracts finds all active contracts for a player
func (r *MockContractRepository) FindActiveContracts(ctx context.Context, playerID int) ([]*contract.Contract, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.shouldError {
		return nil, fmt.Errorf("%s", r.errorMsg)
	}

	var activeContracts []*contract.Contract
	for _, c := range r.contracts {
		if c.PlayerID().Value() == playerID && c.Accepted() && !c.Fulfilled() {
			activeContracts = append(activeContracts, c)
		}
	}

	return activeContracts, nil
}

// Save saves a contract
func (r *MockContractRepository) Save(ctx context.Context, c *contract.Contract) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.shouldError {
		return fmt.Errorf("%s", r.errorMsg)
	}

	r.contracts[c.ContractID()] = c
	return nil
}

// SetError configures error injection
func (r *MockContractRepository) SetError(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shouldError = true
	r.errorMsg = msg
}

// ClearError clears error injection
func (r *MockContractRepository) ClearError() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shouldError = false
	r.errorMsg = ""
}

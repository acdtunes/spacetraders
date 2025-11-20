package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"gorm.io/gorm"
)

// GormContractRepository implements ContractRepository using GORM
type GormContractRepository struct {
	db *gorm.DB
}

// NewGormContractRepository creates a new GORM contract repository
func NewGormContractRepository(db *gorm.DB) *GormContractRepository {
	return &GormContractRepository{db: db}
}

// FindByID retrieves a contract by ID and player ID
func (r *GormContractRepository) FindByID(ctx context.Context, contractID string, playerID int) (*contract.Contract, error) {
	var model ContractModel
	result := r.db.WithContext(ctx).Where("id = ? AND player_id = ?", contractID, playerID).First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("contract not found: %s", contractID)
		}
		return nil, fmt.Errorf("failed to find contract: %w", result.Error)
	}

	return r.modelToEntity(&model)
}

// FindActiveContracts retrieves all active contracts for a player (accepted but not fulfilled)
func (r *GormContractRepository) FindActiveContracts(ctx context.Context, playerID int) ([]*contract.Contract, error) {
	var models []ContractModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND accepted = ? AND fulfilled = ?", playerID, true, false).
		Find(&models)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find active contracts: %w", result.Error)
	}

	contracts := make([]*contract.Contract, 0, len(models))
	for _, model := range models {
		entity, err := r.modelToEntity(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert contract %s: %w", model.ID, err)
		}
		contracts = append(contracts, entity)
	}

	return contracts, nil
}

// Add persists a contract to the database
func (r *GormContractRepository) Add(ctx context.Context, c *contract.Contract) error {
	model, err := r.entityToModel(c)
	if err != nil {
		return fmt.Errorf("failed to convert contract to model: %w", err)
	}

	// Upsert: create or update
	result := r.db.WithContext(ctx).Save(model)
	if result.Error != nil {
		return fmt.Errorf("failed to add contract: %w", result.Error)
	}

	return nil
}

// modelToEntity converts database model to domain entity
func (r *GormContractRepository) modelToEntity(model *ContractModel) (*contract.Contract, error) {
	// Unmarshal deliveries JSON
	var deliveries []contract.Delivery
	if err := json.Unmarshal([]byte(model.DeliveriesJSON), &deliveries); err != nil {
		return nil, fmt.Errorf("failed to unmarshal deliveries: %w", err)
	}

	terms := contract.ContractTerms{
		Payment: contract.Payment{
			OnAccepted:  model.PaymentOnAccepted,
			OnFulfilled: model.PaymentOnFulfilled,
		},
		Deliveries:       deliveries,
		DeadlineToAccept: model.DeadlineToAccept,
		Deadline:         model.Deadline,
	}

	// Create new contract using constructor
	c, err := contract.NewContract(
		model.ID,
		model.PlayerID,
		model.FactionSymbol,
		model.Type,
		terms,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create contract entity: %w", err)
	}

	// Restore accepted/fulfilled state (these are private fields, need to reconstruct)
	// Since the contract entity has private fields, we need to handle accepted/fulfilled state
	// If the contract was accepted, call Accept() to set the state
	if model.Accepted {
		if err := c.Accept(); err != nil {
			return nil, fmt.Errorf("failed to set accepted state: %w", err)
		}
	}

	// If fulfilled, we need to restore that state as well
	// Since Fulfill() requires deliveries to be complete, we trust the database state
	// and manually set the state if needed. However, Contract doesn't expose a setter.
	// We need to check if there's a way to restore this state.

	// Looking at the Contract entity, fulfilled is set via Fulfill() which checks CanFulfill()
	// The deliveries in the model already have UnitsFulfilled set correctly
	// So if model.Fulfilled is true, the deliveries should already be complete
	// and we can call Fulfill()
	if model.Fulfilled {
		if err := c.Fulfill(); err != nil {
			// This might fail if deliveries aren't complete, which would be a data integrity issue
			return nil, fmt.Errorf("failed to set fulfilled state: %w", err)
		}
	}

	return c, nil
}

// entityToModel converts domain entity to database model
func (r *GormContractRepository) entityToModel(c *contract.Contract) (*ContractModel, error) {
	// Marshal deliveries to JSON
	deliveriesJSON, err := json.Marshal(c.Terms().Deliveries)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal deliveries: %w", err)
	}

	return &ContractModel{
		ID:                 c.ContractID(),
		PlayerID:           c.PlayerID(),
		FactionSymbol:      c.FactionSymbol(),
		Type:               c.Type(),
		Accepted:           c.Accepted(),
		Fulfilled:          c.Fulfilled(),
		DeadlineToAccept:   c.Terms().DeadlineToAccept,
		Deadline:           c.Terms().Deadline,
		PaymentOnAccepted:  c.Terms().Payment.OnAccepted,
		PaymentOnFulfilled: c.Terms().Payment.OnFulfilled,
		DeliveriesJSON:     string(deliveriesJSON),
		LastUpdated:        time.Now().UTC().Format(time.RFC3339),
	}, nil
}

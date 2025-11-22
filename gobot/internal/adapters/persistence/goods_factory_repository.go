package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"gorm.io/gorm"
)

// GormGoodsFactoryRepository implements GoodsFactoryRepository using GORM
type GormGoodsFactoryRepository struct {
	db *gorm.DB
}

// NewGormGoodsFactoryRepository creates a new GORM goods factory repository
func NewGormGoodsFactoryRepository(db *gorm.DB) *GormGoodsFactoryRepository {
	return &GormGoodsFactoryRepository{db: db}
}

// Create persists a new goods factory to the database
func (r *GormGoodsFactoryRepository) Create(ctx context.Context, factory *goods.GoodsFactory) error {
	model, err := r.entityToModel(factory)
	if err != nil {
		return fmt.Errorf("failed to convert factory to model: %w", err)
	}

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return fmt.Errorf("failed to create goods factory: %w", result.Error)
	}

	return nil
}

// Update persists changes to an existing goods factory
func (r *GormGoodsFactoryRepository) Update(ctx context.Context, factory *goods.GoodsFactory) error {
	model, err := r.entityToModel(factory)
	if err != nil {
		return fmt.Errorf("failed to convert factory to model: %w", err)
	}

	result := r.db.WithContext(ctx).Save(model)
	if result.Error != nil {
		return fmt.Errorf("failed to update goods factory: %w", result.Error)
	}

	return nil
}

// FindByID retrieves a goods factory by ID and player ID
func (r *GormGoodsFactoryRepository) FindByID(ctx context.Context, id string, playerID int) (*goods.GoodsFactory, error) {
	var model GoodsFactoryModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id, playerID).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("goods factory not found: %s", id)
		}
		return nil, fmt.Errorf("failed to find goods factory: %w", result.Error)
	}

	return r.modelToEntity(&model)
}

// FindActiveByPlayer retrieves all active goods factories for a player
func (r *GormGoodsFactoryRepository) FindActiveByPlayer(ctx context.Context, playerID int) ([]*goods.GoodsFactory, error) {
	var models []GoodsFactoryModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND status IN (?)", playerID, []string{"PENDING", "ACTIVE"}).
		Find(&models)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find active factories: %w", result.Error)
	}

	factories := make([]*goods.GoodsFactory, 0, len(models))
	for _, model := range models {
		entity, err := r.modelToEntity(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert factory %s: %w", model.ID, err)
		}
		factories = append(factories, entity)
	}

	return factories, nil
}

// Delete removes a goods factory from the database
func (r *GormGoodsFactoryRepository) Delete(ctx context.Context, id string, playerID int) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id, playerID).
		Delete(&GoodsFactoryModel{})

	if result.Error != nil {
		return fmt.Errorf("failed to delete goods factory: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("goods factory not found: %s", id)
	}

	return nil
}

// entityToModel converts domain entity to database model
func (r *GormGoodsFactoryRepository) entityToModel(factory *goods.GoodsFactory) (*GoodsFactoryModel, error) {
	// Serialize dependency tree to JSON
	treeJSON, err := json.Marshal(factory.DependencyTree())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal dependency tree: %w", err)
	}

	// Serialize metadata to JSON
	metadataJSON, err := json.Marshal(factory.Metadata())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	model := &GoodsFactoryModel{
		ID:               factory.ID(),
		PlayerID:         factory.PlayerID(),
		TargetGood:       factory.TargetGood(),
		SystemSymbol:     factory.SystemSymbol(),
		DependencyTree:   string(treeJSON),
		Status:           string(factory.Status()),
		Metadata:         string(metadataJSON),
		QuantityAcquired: factory.QuantityAcquired(),
		TotalCost:        factory.TotalCost(),
	}

	// Set timestamps from lifecycle
	model.CreatedAt = factory.CreatedAt()
	model.UpdatedAt = factory.UpdatedAt()

	startedAt := factory.StartedAt()
	if startedAt != nil && !startedAt.IsZero() {
		model.StartedAt = startedAt
	}

	stoppedAt := factory.StoppedAt()
	if stoppedAt != nil && !stoppedAt.IsZero() {
		model.CompletedAt = stoppedAt
	}

	return model, nil
}

// modelToEntity converts database model to domain entity
func (r *GormGoodsFactoryRepository) modelToEntity(model *GoodsFactoryModel) (*goods.GoodsFactory, error) {
	// Deserialize dependency tree
	var dependencyTree *goods.SupplyChainNode
	if err := json.Unmarshal([]byte(model.DependencyTree), &dependencyTree); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dependency tree: %w", err)
	}

	// Deserialize metadata
	var metadata map[string]interface{}
	if model.Metadata != "" && model.Metadata != "null" {
		if err := json.Unmarshal([]byte(model.Metadata), &metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
	}

	// Create entity using NewGoodsFactory (uses RealClock by default)
	factory := goods.NewGoodsFactory(
		model.ID,
		model.PlayerID,
		model.TargetGood,
		model.SystemSymbol,
		dependencyTree,
		metadata,
		nil, // clock - will default to RealClock
	)

	// Restore quantity and cost from database
	if model.QuantityAcquired > 0 {
		factory.SetQuantityAcquired(model.QuantityAcquired)
	}

	// Restore total cost by adding it
	// Note: We can't set it directly, but since it starts at 0, adding works
	if model.TotalCost > 0 {
		factory.AddCost(model.TotalCost)
	}

	// Restore status using lifecycle state machine
	switch goods.FactoryStatus(model.Status) {
	case goods.FactoryStatusPending:
		// Already in pending state
	case goods.FactoryStatusActive:
		if err := factory.Start(); err != nil {
			return nil, fmt.Errorf("failed to restore active status: %w", err)
		}
	case goods.FactoryStatusCompleted:
		if model.QuantityAcquired > 0 {
			// Start factory first, then complete it
			if err := factory.Start(); err != nil {
				return nil, fmt.Errorf("failed to start factory before completing: %w", err)
			}
			if err := factory.Complete(); err != nil {
				return nil, fmt.Errorf("failed to restore completed status: %w", err)
			}
		}
	case goods.FactoryStatusFailed:
		if err := factory.Fail(fmt.Errorf("restored from database")); err != nil {
			return nil, fmt.Errorf("failed to restore failed status: %w", err)
		}
	case goods.FactoryStatusStopped:
		if err := factory.Stop(); err != nil {
			return nil, fmt.Errorf("failed to restore stopped status: %w", err)
		}
	}

	return factory, nil
}

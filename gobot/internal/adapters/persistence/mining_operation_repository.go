package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/mining"
	"gorm.io/gorm"
)

// MiningOperationRepository implements the mining.MiningOperationRepository interface
type MiningOperationRepository struct {
	db *gorm.DB
}

// NewMiningOperationRepository creates a new mining operation repository
func NewMiningOperationRepository(db *gorm.DB) *MiningOperationRepository {
	return &MiningOperationRepository{db: db}
}

// Add creates a new mining operation in the database
func (r *MiningOperationRepository) Add(ctx context.Context, op *mining.MiningOperation) error {
	model, err := r.toModel(op)
	if err != nil {
		return fmt.Errorf("failed to convert to model: %w", err)
	}

	if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
		return fmt.Errorf("failed to add mining operation: %w", err)
	}

	return nil
}

// FindByID retrieves a mining operation by its ID and player ID
func (r *MiningOperationRepository) FindByID(ctx context.Context, id string, playerID int) (*mining.MiningOperation, error) {
	var model MiningOperationModel
	if err := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id, playerID).
		First(&model).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find mining operation: %w", err)
	}

	return r.toEntity(&model)
}

// Save persists changes to an existing mining operation
func (r *MiningOperationRepository) Save(ctx context.Context, op *mining.MiningOperation) error {
	model, err := r.toModel(op)
	if err != nil {
		return fmt.Errorf("failed to convert to model: %w", err)
	}

	if err := r.db.WithContext(ctx).Save(model).Error; err != nil {
		return fmt.Errorf("failed to save mining operation: %w", err)
	}

	return nil
}

// Remove removes a mining operation from the database
func (r *MiningOperationRepository) Remove(ctx context.Context, id string, playerID int) error {
	if err := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id, playerID).
		Delete(&MiningOperationModel{}).Error; err != nil {
		return fmt.Errorf("failed to remove mining operation: %w", err)
	}

	return nil
}

// FindActive retrieves all active (RUNNING) operations for a player
func (r *MiningOperationRepository) FindActive(ctx context.Context, playerID int) ([]*mining.MiningOperation, error) {
	return r.FindByStatus(ctx, playerID, mining.OperationStatusRunning)
}

// FindByStatus retrieves all operations with a given status for a player
func (r *MiningOperationRepository) FindByStatus(ctx context.Context, playerID int, status mining.OperationStatus) ([]*mining.MiningOperation, error) {
	var models []MiningOperationModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND status = ?", playerID, string(status)).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to find mining operations: %w", err)
	}

	operations := make([]*mining.MiningOperation, len(models))
	for i, model := range models {
		op, err := r.toEntity(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert model: %w", err)
		}
		operations[i] = op
	}

	return operations, nil
}

// toModel converts a domain entity to a database model
func (r *MiningOperationRepository) toModel(op *mining.MiningOperation) (*MiningOperationModel, error) {
	data := op.ToData()

	// Serialize ship arrays to JSON
	minerShipsJSON, err := json.Marshal(data.MinerShips)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal miner ships: %w", err)
	}

	transportShipsJSON, err := json.Marshal(data.TransportShips)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transport ships: %w", err)
	}

	return &MiningOperationModel{
		ID:             data.ID,
		PlayerID:       data.PlayerID,
		AsteroidField:  data.AsteroidField,
		Status:         data.Status,
		TopNOres:       data.TopNOres,
		MinerShips:     string(minerShipsJSON),
		TransportShips: string(transportShipsJSON),
		BatchThreshold: data.BatchThreshold,
		BatchTimeout:   data.BatchTimeout,
		MaxIterations:  data.MaxIterations,
		LastError:      data.LastError,
		CreatedAt:      data.CreatedAt,
		UpdatedAt:      data.UpdatedAt,
		StartedAt:      data.StartedAt,
		StoppedAt:      data.StoppedAt,
	}, nil
}

// toEntity converts a database model to a domain entity
func (r *MiningOperationRepository) toEntity(model *MiningOperationModel) (*mining.MiningOperation, error) {
	// Deserialize ship arrays from JSON
	var minerShips []string
	if err := json.Unmarshal([]byte(model.MinerShips), &minerShips); err != nil {
		return nil, fmt.Errorf("failed to unmarshal miner ships: %w", err)
	}

	var transportShips []string
	if err := json.Unmarshal([]byte(model.TransportShips), &transportShips); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transport ships: %w", err)
	}

	data := &mining.MiningOperationData{
		ID:             model.ID,
		PlayerID:       model.PlayerID,
		AsteroidField:  model.AsteroidField,
		Status:         model.Status,
		TopNOres:       model.TopNOres,
		MinerShips:     minerShips,
		TransportShips: transportShips,
		BatchThreshold: model.BatchThreshold,
		BatchTimeout:   model.BatchTimeout,
		MaxIterations:  model.MaxIterations,
		LastError:      model.LastError,
		CreatedAt:      model.CreatedAt,
		UpdatedAt:      model.UpdatedAt,
		StartedAt:      model.StartedAt,
		StoppedAt:      model.StoppedAt,
	}

	return mining.FromData(data, nil), nil
}

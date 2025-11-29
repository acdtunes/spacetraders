package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/gas"
	"gorm.io/gorm"
)

// GasOperationRepository implements the gas.OperationRepository interface
type GasOperationRepository struct {
	db *gorm.DB
}

// NewGasOperationRepository creates a new gas operation repository
func NewGasOperationRepository(db *gorm.DB) *GasOperationRepository {
	return &GasOperationRepository{db: db}
}

// Add creates a new gas operation in the database
func (r *GasOperationRepository) Add(ctx context.Context, op *gas.Operation) error {
	model, err := r.toModel(op)
	if err != nil {
		return fmt.Errorf("failed to convert to model: %w", err)
	}

	if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
		return fmt.Errorf("failed to add gas operation: %w", err)
	}

	return nil
}

// FindByID retrieves a gas operation by its ID and player ID
func (r *GasOperationRepository) FindByID(ctx context.Context, id string, playerID int) (*gas.Operation, error) {
	var model GasOperationModel
	if err := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id, playerID).
		First(&model).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find gas operation: %w", err)
	}

	return r.toEntity(&model)
}

// Save persists changes to an existing gas operation
func (r *GasOperationRepository) Save(ctx context.Context, op *gas.Operation) error {
	model, err := r.toModel(op)
	if err != nil {
		return fmt.Errorf("failed to convert to model: %w", err)
	}

	if err := r.db.WithContext(ctx).Save(model).Error; err != nil {
		return fmt.Errorf("failed to save gas operation: %w", err)
	}

	return nil
}

// Remove removes a gas operation from the database
func (r *GasOperationRepository) Remove(ctx context.Context, id string, playerID int) error {
	if err := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id, playerID).
		Delete(&GasOperationModel{}).Error; err != nil {
		return fmt.Errorf("failed to remove gas operation: %w", err)
	}

	return nil
}

// FindActive retrieves all active (RUNNING) operations for a player
func (r *GasOperationRepository) FindActive(ctx context.Context, playerID int) ([]*gas.Operation, error) {
	return r.FindByStatus(ctx, playerID, gas.OperationStatusRunning)
}

// FindByStatus retrieves all operations with a given status for a player
func (r *GasOperationRepository) FindByStatus(ctx context.Context, playerID int, status gas.OperationStatus) ([]*gas.Operation, error) {
	var models []GasOperationModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND status = ?", playerID, string(status)).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to find gas operations: %w", err)
	}

	operations := make([]*gas.Operation, len(models))
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
func (r *GasOperationRepository) toModel(op *gas.Operation) (*GasOperationModel, error) {
	data := op.ToData()

	// Serialize ship arrays to JSON
	siphonShipsJSON, err := json.Marshal(data.SiphonShips)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal siphon ships: %w", err)
	}

	transportShipsJSON, err := json.Marshal(data.TransportShips)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transport ships: %w", err)
	}

	return &GasOperationModel{
		ID:             data.ID,
		PlayerID:       data.PlayerID,
		GasGiant:       data.GasGiant,
		Status:         data.Status,
		SiphonShips:    string(siphonShipsJSON),
		TransportShips: string(transportShipsJSON),
		MaxIterations:  data.MaxIterations,
		LastError:      data.LastError,
		CreatedAt:      data.CreatedAt,
		UpdatedAt:      data.UpdatedAt,
		StartedAt:      data.StartedAt,
		StoppedAt:      data.StoppedAt,
	}, nil
}

// toEntity converts a database model to a domain entity
func (r *GasOperationRepository) toEntity(model *GasOperationModel) (*gas.Operation, error) {
	// Deserialize ship arrays from JSON
	var siphonShips []string
	if model.SiphonShips != "" {
		if err := json.Unmarshal([]byte(model.SiphonShips), &siphonShips); err != nil {
			return nil, fmt.Errorf("failed to unmarshal siphon ships: %w", err)
		}
	}

	var transportShips []string
	if model.TransportShips != "" {
		if err := json.Unmarshal([]byte(model.TransportShips), &transportShips); err != nil {
			return nil, fmt.Errorf("failed to unmarshal transport ships: %w", err)
		}
	}

	data := &gas.OperationData{
		ID:             model.ID,
		PlayerID:       model.PlayerID,
		GasGiant:       model.GasGiant,
		Status:         model.Status,
		SiphonShips:    siphonShips,
		TransportShips: transportShips,
		MaxIterations:  model.MaxIterations,
		LastError:      model.LastError,
		CreatedAt:      model.CreatedAt,
		UpdatedAt:      model.UpdatedAt,
		StartedAt:      model.StartedAt,
		StoppedAt:      model.StoppedAt,
	}

	return gas.FromData(data, nil), nil
}

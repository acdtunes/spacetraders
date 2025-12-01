package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"gorm.io/gorm"
)

// StorageOperationRepository implements the storage.StorageOperationRepository interface
type StorageOperationRepository struct {
	db    *gorm.DB
	clock shared.Clock
}

// NewStorageOperationRepository creates a new storage operation repository
func NewStorageOperationRepository(db *gorm.DB, clock shared.Clock) *StorageOperationRepository {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &StorageOperationRepository{db: db, clock: clock}
}

// Create persists a new storage operation
func (r *StorageOperationRepository) Create(ctx context.Context, operation *storage.StorageOperation) error {
	model, err := r.toModel(operation)
	if err != nil {
		return fmt.Errorf("failed to convert to model: %w", err)
	}

	if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
		return fmt.Errorf("failed to create storage operation: %w", err)
	}

	return nil
}

// Update saves changes to an existing storage operation
func (r *StorageOperationRepository) Update(ctx context.Context, operation *storage.StorageOperation) error {
	model, err := r.toModel(operation)
	if err != nil {
		return fmt.Errorf("failed to convert to model: %w", err)
	}

	if err := r.db.WithContext(ctx).Save(model).Error; err != nil {
		return fmt.Errorf("failed to update storage operation: %w", err)
	}

	return nil
}

// FindByID retrieves a storage operation by its ID
func (r *StorageOperationRepository) FindByID(ctx context.Context, id string) (*storage.StorageOperation, error) {
	var model StorageOperationModel
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		First(&model).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find storage operation: %w", err)
	}

	return r.toEntity(&model)
}

// FindByPlayerID retrieves all storage operations for a player
func (r *StorageOperationRepository) FindByPlayerID(ctx context.Context, playerID int) ([]*storage.StorageOperation, error) {
	var models []StorageOperationModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to find storage operations: %w", err)
	}

	return r.toEntities(models)
}

// FindByStatus retrieves storage operations by status for a player
func (r *StorageOperationRepository) FindByStatus(ctx context.Context, playerID int, statuses []storage.OperationStatus) ([]*storage.StorageOperation, error) {
	statusStrings := make([]string, len(statuses))
	for i, s := range statuses {
		statusStrings[i] = string(s)
	}

	var models []StorageOperationModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND status IN ?", playerID, statusStrings).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to find storage operations: %w", err)
	}

	return r.toEntities(models)
}

// FindByGood retrieves storage operations that support a specific good
func (r *StorageOperationRepository) FindByGood(ctx context.Context, playerID int, goodSymbol string) ([]*storage.StorageOperation, error) {
	// We use a text search on the JSON array since PostgreSQL supports this
	// The supported_goods column contains a JSON array like ["GAS_A", "GAS_B"]
	var models []StorageOperationModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND supported_goods LIKE ?", playerID, "%"+goodSymbol+"%").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to find storage operations by good: %w", err)
	}

	// Filter in Go to ensure exact match (LIKE might match partial strings)
	filtered := make([]StorageOperationModel, 0)
	for _, model := range models {
		var goods []string
		if model.SupportedGoods != "" {
			if err := json.Unmarshal([]byte(model.SupportedGoods), &goods); err == nil {
				for _, g := range goods {
					if g == goodSymbol {
						filtered = append(filtered, model)
						break
					}
				}
			}
		}
	}

	return r.toEntities(filtered)
}

// FindRunning retrieves all running storage operations for a player
func (r *StorageOperationRepository) FindRunning(ctx context.Context, playerID int) ([]*storage.StorageOperation, error) {
	return r.FindByStatus(ctx, playerID, []storage.OperationStatus{storage.OperationStatusRunning})
}

// FindRunningByWaypoint retrieves a running storage operation for a specific waypoint (gas giant)
// Returns nil if no running operation exists for that waypoint
// NOTE: Only returns the FIRST matching operation. Use FindAllRunningByWaypoint to get all.
func (r *StorageOperationRepository) FindRunningByWaypoint(ctx context.Context, playerID int, waypointSymbol string) (*storage.StorageOperation, error) {
	var model StorageOperationModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND waypoint_symbol = ? AND status = ?", playerID, waypointSymbol, string(storage.OperationStatusRunning)).
		First(&model).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find running storage operation by waypoint: %w", err)
	}

	return r.toEntity(&model)
}

// FindAllRunningByWaypoint retrieves ALL running storage operations for a specific waypoint
// Used to stop all old operations when starting a new one (prevents duplicate operations)
func (r *StorageOperationRepository) FindAllRunningByWaypoint(ctx context.Context, playerID int, waypointSymbol string) ([]*storage.StorageOperation, error) {
	var models []StorageOperationModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND waypoint_symbol = ? AND status = ?", playerID, waypointSymbol, string(storage.OperationStatusRunning)).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to find all running storage operations by waypoint: %w", err)
	}

	return r.toEntities(models)
}

// Delete removes a storage operation
func (r *StorageOperationRepository) Delete(ctx context.Context, id string) error {
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&StorageOperationModel{}).Error; err != nil {
		return fmt.Errorf("failed to delete storage operation: %w", err)
	}

	return nil
}

// toModel converts a domain entity to a database model
func (r *StorageOperationRepository) toModel(op *storage.StorageOperation) (*StorageOperationModel, error) {
	data := op.ToData()

	// Serialize ship arrays to JSON
	extractorShipsJSON, err := json.Marshal(data.ExtractorShips)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal extractor ships: %w", err)
	}

	storageShipsJSON, err := json.Marshal(data.StorageShips)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal storage ships: %w", err)
	}

	supportedGoodsJSON, err := json.Marshal(data.SupportedGoods)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal supported goods: %w", err)
	}

	return &StorageOperationModel{
		ID:             data.ID,
		PlayerID:       data.PlayerID,
		WaypointSymbol: data.WaypointSymbol,
		OperationType:  data.OperationType,
		Status:         data.Status,
		ExtractorShips: string(extractorShipsJSON),
		StorageShips:   string(storageShipsJSON),
		SupportedGoods: string(supportedGoodsJSON),
		LastError:      data.LastError,
		CreatedAt:      data.CreatedAt,
		UpdatedAt:      data.UpdatedAt,
		StartedAt:      data.StartedAt,
		StoppedAt:      data.StoppedAt,
	}, nil
}

// toEntity converts a database model to a domain entity
func (r *StorageOperationRepository) toEntity(model *StorageOperationModel) (*storage.StorageOperation, error) {
	// Deserialize ship arrays from JSON
	var extractorShips []string
	if model.ExtractorShips != "" {
		if err := json.Unmarshal([]byte(model.ExtractorShips), &extractorShips); err != nil {
			return nil, fmt.Errorf("failed to unmarshal extractor ships: %w", err)
		}
	}

	var storageShips []string
	if model.StorageShips != "" {
		if err := json.Unmarshal([]byte(model.StorageShips), &storageShips); err != nil {
			return nil, fmt.Errorf("failed to unmarshal storage ships: %w", err)
		}
	}

	var supportedGoods []string
	if model.SupportedGoods != "" {
		if err := json.Unmarshal([]byte(model.SupportedGoods), &supportedGoods); err != nil {
			return nil, fmt.Errorf("failed to unmarshal supported goods: %w", err)
		}
	}

	data := &storage.StorageOperationData{
		ID:             model.ID,
		PlayerID:       model.PlayerID,
		WaypointSymbol: model.WaypointSymbol,
		OperationType:  model.OperationType,
		Status:         model.Status,
		ExtractorShips: extractorShips,
		StorageShips:   storageShips,
		SupportedGoods: supportedGoods,
		LastError:      model.LastError,
		CreatedAt:      model.CreatedAt,
		UpdatedAt:      model.UpdatedAt,
		StartedAt:      model.StartedAt,
		StoppedAt:      model.StoppedAt,
	}

	return storage.StorageOperationFromData(data, r.clock), nil
}

// toEntities converts multiple database models to domain entities
func (r *StorageOperationRepository) toEntities(models []StorageOperationModel) ([]*storage.StorageOperation, error) {
	operations := make([]*storage.StorageOperation, len(models))
	for i, model := range models {
		op, err := r.toEntity(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert model: %w", err)
		}
		operations[i] = op
	}
	return operations, nil
}

// Verify interface implementation
var _ storage.StorageOperationRepository = (*StorageOperationRepository)(nil)

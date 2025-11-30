package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// ContainerRepositoryGORM implements container persistence using GORM
type ContainerRepositoryGORM struct {
	db *gorm.DB
}

// NewContainerRepository creates a new GORM-based container repository
func NewContainerRepository(db *gorm.DB) *ContainerRepositoryGORM {
	return &ContainerRepositoryGORM{db: db}
}

// Add creates a new container record in the database
func (r *ContainerRepositoryGORM) Add(
	ctx context.Context,
	containerEntity *container.Container,
	commandType string,
) error {
	// Serialize metadata to JSON (this is the config in Container entity)
	configJSON, err := json.Marshal(containerEntity.Metadata())
	if err != nil {
		return fmt.Errorf("failed to serialize config: %w", err)
	}

	now := time.Now()

	// Map restart count to restart policy for database storage
	// Go implementation uses maxRestarts count, not policy string
	restartPolicy := "no"
	if containerEntity.MaxRestarts() > 0 {
		restartPolicy = "on-failure"
	}

	model := &ContainerModel{
		ID:                containerEntity.ID(),
		PlayerID:          containerEntity.PlayerID(),
		ContainerType:     string(containerEntity.Type()),
		CommandType:       commandType,
		Status:            string(containerEntity.Status()),
		ParentContainerID: containerEntity.ParentContainerID(),
		RestartPolicy:     restartPolicy,
		RestartCount:      containerEntity.RestartCount(),
		Config:            string(configJSON),
		StartedAt:         &now,
		StoppedAt:         nil,
		ExitCode:          nil,
		ExitReason:        "",
	}

	if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
		return fmt.Errorf("failed to insert container: %w", err)
	}

	return nil
}

// UpdateStatus updates container status and completion info
func (r *ContainerRepositoryGORM) UpdateStatus(
	ctx context.Context,
	containerID string,
	playerID int,
	status container.ContainerStatus,
	stoppedAt *time.Time,
	exitCode *int,
	exitReason string,
) error {
	updates := map[string]interface{}{
		"status": string(status),
	}

	if stoppedAt != nil {
		updates["stopped_at"] = stoppedAt
		updates["exit_code"] = exitCode
		updates["exit_reason"] = exitReason
	}

	result := r.db.WithContext(ctx).
		Model(&ContainerModel{}).
		Where("id = ? AND player_id = ?", containerID, playerID).
		Updates(updates)

	if result.Error != nil {
		return fmt.Errorf("failed to update container status: %w", result.Error)
	}

	return nil
}

// Get retrieves a single container by ID
func (r *ContainerRepositoryGORM) Get(
	ctx context.Context,
	containerID string,
	playerID int,
) (*ContainerModel, error) {
	var model ContainerModel

	result := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", containerID, playerID).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get container: %w", result.Error)
	}

	return &model, nil
}

// ListByStatus lists all containers with a specific status
func (r *ContainerRepositoryGORM) ListByStatus(
	ctx context.Context,
	status container.ContainerStatus,
	playerID *int,
) ([]*ContainerModel, error) {
	var models []*ContainerModel

	query := r.db.WithContext(ctx).Where("status = ?", string(status))

	if playerID != nil {
		query = query.Where("player_id = ?", *playerID)
	}

	if err := query.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list containers by status: %w", err)
	}

	return models, nil
}

// ListAll lists all containers, optionally filtered by player
func (r *ContainerRepositoryGORM) ListAll(
	ctx context.Context,
	playerID *int,
) ([]*ContainerModel, error) {
	var models []*ContainerModel

	query := r.db.WithContext(ctx)

	if playerID != nil {
		query = query.Where("player_id = ?", *playerID)
	}

	if err := query.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	return models, nil
}

// Remove removes a container record
func (r *ContainerRepositoryGORM) Remove(
	ctx context.Context,
	containerID string,
	playerID int,
) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", containerID, playerID).
		Delete(&ContainerModel{})

	if result.Error != nil {
		return fmt.Errorf("failed to remove container: %w", result.Error)
	}

	return nil
}

// ContainerSummary is an internal query result struct for simplified container lookups.
// For full container data, use the Container domain entity.
// This struct is used by coordinators to check container status efficiently.
type ContainerSummary struct {
	ID            string
	ContainerType string
	Status        string
}

// ListByStatusSimple returns simplified container info (for coordinators)
func (r *ContainerRepositoryGORM) ListByStatusSimple(
	ctx context.Context,
	status string,
	playerID *int,
) ([]ContainerSummary, error) {
	var models []*ContainerModel

	query := r.db.WithContext(ctx).Where("status = ?", status)

	if playerID != nil {
		query = query.Where("player_id = ?", *playerID)
	}

	if err := query.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list containers by status: %w", err)
	}

	// Convert to ContainerSummary
	result := make([]ContainerSummary, len(models))
	for i, model := range models {
		result[i] = ContainerSummary{
			ID:            model.ID,
			ContainerType: model.ContainerType,
			Status:        model.Status,
		}
	}

	return result, nil
}

// CreateIfNoActiveWorker atomically creates a worker container only if no other
// CONTRACT_WORKFLOW container is RUNNING for the player. Returns true if created,
// false if another worker already exists.
func (r *ContainerRepositoryGORM) CreateIfNoActiveWorker(
	ctx context.Context,
	containerEntity *container.Container,
	commandType string,
) (bool, error) {
	var created bool

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock and check for existing active workers
		var count int64
		if err := tx.Model(&ContainerModel{}).
			Where("container_type = ? AND status = ? AND player_id = ?",
				"CONTRACT_WORKFLOW", "RUNNING", containerEntity.PlayerID()).
			Count(&count).Error; err != nil {
			return fmt.Errorf("failed to count active workers: %w", err)
		}

		if count > 0 {
			// Another worker already exists
			created = false
			return nil
		}

		// No active worker, create new one
		configJSON, err := json.Marshal(containerEntity.Metadata())
		if err != nil {
			return fmt.Errorf("failed to serialize config: %w", err)
		}

		now := time.Now()
		restartPolicy := "no"
		if containerEntity.MaxRestarts() > 0 {
			restartPolicy = "on-failure"
		}

		model := &ContainerModel{
			ID:                containerEntity.ID(),
			PlayerID:          containerEntity.PlayerID(),
			ContainerType:     string(containerEntity.Type()),
			CommandType:       commandType,
			Status:            string(containerEntity.Status()),
			ParentContainerID: containerEntity.ParentContainerID(),
			RestartPolicy:     restartPolicy,
			RestartCount:      containerEntity.RestartCount(),
			Config:            string(configJSON),
			StartedAt:         &now,
			StoppedAt:         nil,
			ExitCode:          nil,
			ExitReason:        "",
		}

		if err := tx.Create(model).Error; err != nil {
			return fmt.Errorf("failed to insert container: %w", err)
		}

		created = true
		return nil
	})

	return created, err
}

// FindChildContainers retrieves all direct children of a parent container
// Returns empty slice if no children found (not an error)
func (r *ContainerRepositoryGORM) FindChildContainers(
	ctx context.Context,
	parentContainerID string,
	playerID int,
) ([]*ContainerModel, error) {
	var models []*ContainerModel

	err := r.db.WithContext(ctx).
		Where("parent_container_id = ? AND player_id = ?", parentContainerID, playerID).
		Order("started_at ASC"). // Oldest children first for consistent ordering
		Find(&models).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find child containers: %w", err)
	}

	return models, nil
}

// FindActiveCoordinatorByTypeAndSystem finds an active (PENDING or RUNNING) coordinator
// of the given type for the specified system. Returns nil if none found.
// Used to enforce singleton coordinators per system.
func (r *ContainerRepositoryGORM) FindActiveCoordinatorByTypeAndSystem(
	ctx context.Context,
	containerType string,
	systemSymbol string,
	playerID int,
) (*ContainerModel, error) {
	var model ContainerModel

	// Search for active coordinators with matching system in config
	// Config is JSON with "system_symbol" field
	result := r.db.WithContext(ctx).
		Where("container_type = ? AND player_id = ? AND status IN (?, ?)",
			containerType, playerID, "PENDING", "RUNNING").
		Where("config LIKE ?", fmt.Sprintf(`%%"system_symbol":"%s"%%`, systemSymbol)).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find active coordinator: %w", result.Error)
	}

	return &model, nil
}

// StopOrphanedWorkersByParent marks all RUNNING/PENDING worker containers
// with the given parent container ID as STOPPED. Used during coordinator
// startup to clean up orphaned workers from crashed coordinators.
func (r *ContainerRepositoryGORM) StopOrphanedWorkersByParent(
	ctx context.Context,
	parentContainerID string,
	playerID int,
) (int64, error) {
	now := time.Now()
	exitCode := 1

	result := r.db.WithContext(ctx).
		Model(&ContainerModel{}).
		Where("parent_container_id = ? AND player_id = ? AND status IN (?, ?)",
			parentContainerID, playerID, "PENDING", "RUNNING").
		Updates(map[string]interface{}{
			"status":      "STOPPED",
			"stopped_at":  &now,
			"exit_code":   &exitCode,
			"exit_reason": "orphaned_by_coordinator_restart",
		})

	if result.Error != nil {
		return 0, fmt.Errorf("failed to stop orphaned workers: %w", result.Error)
	}

	return result.RowsAffected, nil
}

// StopAllOrphanedManufacturingWorkers marks ALL RUNNING/PENDING manufacturing task worker
// containers for a player as STOPPED. Used at coordinator startup to ensure clean state.
// This prevents orphaned workers from crashed coordinators from holding ships.
func (r *ContainerRepositoryGORM) StopAllOrphanedManufacturingWorkers(
	ctx context.Context,
	playerID int,
) (int64, error) {
	now := time.Now()
	exitCode := 1

	result := r.db.WithContext(ctx).
		Model(&ContainerModel{}).
		Where("container_type = ? AND player_id = ? AND status IN (?, ?)",
			"MANUFACTURING_TASK_WORKER", playerID, "PENDING", "RUNNING").
		Updates(map[string]interface{}{
			"status":      "STOPPED",
			"stopped_at":  &now,
			"exit_code":   &exitCode,
			"exit_reason": "orphaned_by_coordinator_restart",
		})

	if result.Error != nil {
		return 0, fmt.Errorf("failed to stop orphaned workers: %w", result.Error)
	}

	return result.RowsAffected, nil
}

// FindActiveGasCoordinator finds an active (PENDING or RUNNING) gas coordinator
// for the specified gas giant. Returns nil if none found.
// Used to enforce singleton gas coordinators per gas giant.
func (r *ContainerRepositoryGORM) FindActiveGasCoordinator(
	ctx context.Context,
	gasGiant string,
	playerID int,
) (*ContainerModel, error) {
	var model ContainerModel

	// Search for active gas coordinators with matching gas_giant in config
	result := r.db.WithContext(ctx).
		Where("container_type = ? AND player_id = ? AND status IN (?, ?)",
			"GAS_COORDINATOR", playerID, "PENDING", "RUNNING").
		Where("config LIKE ?", fmt.Sprintf(`%%"gas_giant":"%s"%%`, gasGiant)).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find active gas coordinator: %w", result.Error)
	}

	return &model, nil
}

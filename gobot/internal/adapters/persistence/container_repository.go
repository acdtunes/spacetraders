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

// Insert creates a new container record in the database
func (r *ContainerRepositoryGORM) Insert(
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
		ContainerID:   containerEntity.ID(),
		PlayerID:      containerEntity.PlayerID(),
		ContainerType: string(containerEntity.Type()),
		CommandType:   commandType,
		Status:        string(containerEntity.Status()),
		RestartPolicy: restartPolicy,
		RestartCount:  containerEntity.RestartCount(),
		Config:        string(configJSON),
		StartedAt:     &now,
		StoppedAt:     nil,
		ExitCode:      nil,
		ExitReason:    "",
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
		Where("container_id = ? AND player_id = ?", containerID, playerID).
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
		Where("container_id = ? AND player_id = ?", containerID, playerID).
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

// Delete removes a container record
func (r *ContainerRepositoryGORM) Delete(
	ctx context.Context,
	containerID string,
	playerID int,
) error {
	result := r.db.WithContext(ctx).
		Where("container_id = ? AND player_id = ?", containerID, playerID).
		Delete(&ContainerModel{})

	if result.Error != nil {
		return fmt.Errorf("failed to delete container: %w", result.Error)
	}

	return nil
}

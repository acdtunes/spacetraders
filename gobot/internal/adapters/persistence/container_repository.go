package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

const (
	containerStatusPending = "PENDING"
	containerStatusRunning = "RUNNING"

	restartPolicyNone      = "no"
	restartPolicyOnFailure = "on-failure"
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
	now := time.Now()
	model, err := newContainerModel(containerEntity, commandType, now)
	if err != nil {
		return err
	}
	model.HeartbeatAt = &now

	if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
		return fmt.Errorf("failed to insert container: %w", err)
	}

	return nil
}

// newContainerModel maps the entity onto a fresh container row. HeartbeatAt is left NULL —
// only Add seeds it — and a NULL heartbeat is excluded from the captain's staleness detector.
func newContainerModel(
	containerEntity *container.Container,
	commandType string,
	now time.Time,
) (*ContainerModel, error) {
	configJSON, err := json.Marshal(containerEntity.Metadata())
	if err != nil {
		return nil, fmt.Errorf("failed to serialize config: %w", err)
	}

	restartPolicy := restartPolicyNone
	if containerEntity.MaxRestarts() > 0 {
		restartPolicy = restartPolicyOnFailure
	}

	return &ContainerModel{
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
	}, nil
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

// UpdateContainerConfig overwrites just the persisted config JSON of a container. It
// is a SINGLE-COLUMN GORM Updates, so it never touches status, heartbeat_at, or any
// other column the runner writes concurrently — and config has no other writer during
// a run (it is set once at Add and only ever amended here), so a caller's
// read-modify-write of the config map is race-free at the column level. Used by the
// arb run to durably record its already-incurred buy cost so a restart-rebuilt resume
// reports honest P&L (RULINGS #2).
func (r *ContainerRepositoryGORM) UpdateContainerConfig(
	ctx context.Context,
	containerID string,
	playerID int,
	configJSON string,
) error {
	result := r.db.WithContext(ctx).
		Model(&ContainerModel{}).
		Where("id = ? AND player_id = ?", containerID, playerID).
		Updates(map[string]interface{}{"config": configJSON})

	if result.Error != nil {
		return fmt.Errorf("failed to update container config: %w", result.Error)
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
		var count int64
		if err := tx.Model(&ContainerModel{}).
			Where("container_type = ? AND status = ? AND player_id = ?",
				"CONTRACT_WORKFLOW", containerStatusRunning, containerEntity.PlayerID()).
			Count(&count).Error; err != nil {
			return fmt.Errorf("failed to count active workers: %w", err)
		}

		if count > 0 {
			created = false
			return nil
		}

		model, err := newContainerModel(containerEntity, commandType, time.Now())
		if err != nil {
			return err
		}

		if err := tx.Create(model).Error; err != nil {
			return fmt.Errorf("failed to insert container: %w", err)
		}

		created = true
		return nil
	})

	return created, err
}

// UpdateContainerHeartbeat updates the heartbeat timestamp for a container
func (r *ContainerRepositoryGORM) UpdateContainerHeartbeat(
	ctx context.Context,
	containerID string,
) error {
	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&ContainerModel{}).
		Where("id = ?", containerID).
		Update("heartbeat_at", now)

	if result.Error != nil {
		return fmt.Errorf("failed to update heartbeat: %w", result.Error)
	}
	return nil
}

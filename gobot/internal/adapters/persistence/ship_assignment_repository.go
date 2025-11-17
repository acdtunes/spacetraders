package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
)

// ShipAssignmentRepositoryGORM implements ship assignment persistence using GORM
type ShipAssignmentRepositoryGORM struct {
	db *gorm.DB
}

// NewShipAssignmentRepository creates a new GORM-based ship assignment repository
func NewShipAssignmentRepository(db *gorm.DB) *ShipAssignmentRepositoryGORM {
	return &ShipAssignmentRepositoryGORM{db: db}
}

// Insert creates a new ship assignment record in the database
// Uses UPSERT pattern to handle reassigning ships that have released assignments
func (r *ShipAssignmentRepositoryGORM) Insert(
	ctx context.Context,
	assignment *daemon.ShipAssignment,
) error {
	model := &ShipAssignmentModel{
		ShipSymbol:  assignment.ShipSymbol(),
		PlayerID:    assignment.PlayerID(),
		ContainerID: assignment.ContainerID(),
		Status:      string(assignment.Status()),
		AssignedAt:  &[]time.Time{assignment.AssignedAt()}[0],
	}

	// Use UPSERT: on conflict with (ship_symbol, player_id), update the row
	// This allows reassigning ships that have old "released" assignments
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"container_id", "status", "assigned_at", "released_at", "release_reason"}),
		UpdateAll: false,
	}).Create(model).Error; err != nil {
		return fmt.Errorf("failed to insert ship assignment: %w", err)
	}

	return nil
}

// FindByShip retrieves the active assignment for a ship
func (r *ShipAssignmentRepositoryGORM) FindByShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
) (*daemon.ShipAssignment, error) {
	var model ShipAssignmentModel

	err := r.db.WithContext(ctx).
		Where("ship_symbol = ? AND player_id = ? AND status = ?", shipSymbol, playerID, "active").
		First(&model).Error

	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to find ship assignment: %w", err)
	}

	// Convert model to domain entity
	assignment := daemon.NewShipAssignment(
		model.ShipSymbol,
		model.PlayerID,
		model.ContainerID,
		nil, // Clock not needed for retrieval
	)

	return assignment, nil
}

// FindByContainer retrieves all ship assignments for a container
func (r *ShipAssignmentRepositoryGORM) FindByContainer(
	ctx context.Context,
	containerID string,
	playerID int,
) ([]*daemon.ShipAssignment, error) {
	var models []ShipAssignmentModel

	err := r.db.WithContext(ctx).
		Where("container_id = ? AND player_id = ?", containerID, playerID).
		Find(&models).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find container assignments: %w", err)
	}

	assignments := make([]*daemon.ShipAssignment, 0, len(models))
	for _, model := range models {
		assignment := daemon.NewShipAssignment(
			model.ShipSymbol,
			model.PlayerID,
			model.ContainerID,
			nil, // Clock not needed for retrieval
		)
		assignments = append(assignments, assignment)
	}

	return assignments, nil
}

// Release marks a ship assignment as released
func (r *ShipAssignmentRepositoryGORM) Release(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	reason string,
) error {
	now := time.Now()

	result := r.db.WithContext(ctx).
		Model(&ShipAssignmentModel{}).
		Where("ship_symbol = ? AND player_id = ? AND status = ?", shipSymbol, playerID, "active").
		Updates(map[string]interface{}{
			"status":         "released",
			"released_at":    now,
			"release_reason": reason,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to release ship assignment: %w", result.Error)
	}

	return nil
}

// ReleaseByContainer releases all ship assignments for a container
func (r *ShipAssignmentRepositoryGORM) ReleaseByContainer(
	ctx context.Context,
	containerID string,
	playerID int,
	reason string,
) error {
	now := time.Now()

	result := r.db.WithContext(ctx).
		Model(&ShipAssignmentModel{}).
		Where("container_id = ? AND player_id = ? AND status = ?", containerID, playerID, "active").
		Updates(map[string]interface{}{
			"status":         "released",
			"released_at":    now,
			"release_reason": reason,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to release container assignments: %w", result.Error)
	}

	return nil
}

// ReleaseAllActive releases all active ship assignments
// Used during daemon startup to clean up zombie assignments from previous runs
// Returns the number of assignments released
func (r *ShipAssignmentRepositoryGORM) ReleaseAllActive(
	ctx context.Context,
	reason string,
) (int, error) {
	now := time.Now()

	result := r.db.WithContext(ctx).
		Model(&ShipAssignmentModel{}).
		Where("status = ?", "active").
		Updates(map[string]interface{}{
			"status":         "released",
			"released_at":    now,
			"release_reason": reason,
		})

	if result.Error != nil {
		return 0, fmt.Errorf("failed to release all active assignments: %w", result.Error)
	}

	return int(result.RowsAffected), nil
}

package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// ShipAssignmentRepositoryGORM implements ship assignment persistence using GORM
// DEPRECATED: This repository is being phased out. Use ShipRepository methods instead.
// Ship assignment is now part of the Ship aggregate in the navigation bounded context.
type ShipAssignmentRepositoryGORM struct {
	db *gorm.DB
}

// NewShipAssignmentRepository creates a new GORM-based ship assignment repository
// DEPRECATED: Use ShipRepository methods instead.
func NewShipAssignmentRepository(db *gorm.DB) *ShipAssignmentRepositoryGORM {
	return &ShipAssignmentRepositoryGORM{db: db}
}

// Assign creates or updates a ship assignment
func (r *ShipAssignmentRepositoryGORM) Assign(
	ctx context.Context,
	assignment *container.ShipAssignment,
) error {
	// Check for existing active assignment
	existingAssignment, err := r.FindByShip(ctx, assignment.ShipSymbol(), assignment.PlayerID())
	if err != nil {
		return fmt.Errorf("failed to check existing assignment: %w", err)
	}

	if existingAssignment != nil && existingAssignment.Status() == "active" {
		return fmt.Errorf("ship %s is already assigned to container %s",
			assignment.ShipSymbol(), existingAssignment.ContainerID())
	}

	// Convert empty container ID to NULL for database (FK constraint compatibility)
	containerIDPtr := stringToPtr(assignment.ContainerID())

	model := &ShipModel{
		ShipSymbol:       assignment.ShipSymbol(),
		PlayerID:         assignment.PlayerID(),
		ContainerID:      containerIDPtr, // Use pointer to support NULL
		AssignmentStatus: string(assignment.Status()),
		AssignedAt:       &[]time.Time{assignment.AssignedAt()}[0],
	}

	// Use UPSERT: on conflict with (ship_symbol, player_id), update the row
	// This allows reassigning ships that have old "idle" assignments
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"container_id", "assignment_status", "assigned_at", "released_at", "release_reason"}),
		UpdateAll: false,
	}).Create(model).Error; err != nil {
		return fmt.Errorf("failed to assign ship: %w", err)
	}

	return nil
}

// FindByShip retrieves the active assignment for a ship
func (r *ShipAssignmentRepositoryGORM) FindByShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
) (*container.ShipAssignment, error) {
	var model ShipModel

	err := r.db.WithContext(ctx).
		Where("ship_symbol = ? AND player_id = ? AND assignment_status = ?", shipSymbol, playerID, "active").
		First(&model).Error

	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to find ship assignment: %w", err)
	}

	containerID := derefString(model.ContainerID)

	assignment := container.NewShipAssignment(
		model.ShipSymbol,
		model.PlayerID,
		containerID,
		nil, // Clock not needed for retrieval
	)

	return assignment, nil
}

// FindByShipSymbol retrieves the assignment for a ship by symbol
// This is an alias for FindByShip for interface compatibility
func (r *ShipAssignmentRepositoryGORM) FindByShipSymbol(
	ctx context.Context,
	shipSymbol string,
	playerID int,
) (*container.ShipAssignment, error) {
	return r.FindByShip(ctx, shipSymbol, playerID)
}

// FindByContainer retrieves all ship assignments for a container
func (r *ShipAssignmentRepositoryGORM) FindByContainer(
	ctx context.Context,
	containerID string,
	playerID int,
) ([]*container.ShipAssignment, error) {
	var models []ShipModel

	err := r.db.WithContext(ctx).
		Where("container_id = ? AND player_id = ?", containerID, playerID).
		Find(&models).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find container assignments: %w", err)
	}

	assignments := make([]*container.ShipAssignment, 0, len(models))
	for _, model := range models {
		containerID := derefString(model.ContainerID)

		assignment := container.NewShipAssignment(
			model.ShipSymbol,
			model.PlayerID,
			containerID,
			nil, // Clock not needed for retrieval
		)
		assignments = append(assignments, assignment)
	}

	return assignments, nil
}

// Release marks a ship assignment as idle and clears the container reference
func (r *ShipAssignmentRepositoryGORM) Release(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	reason string,
) error {
	now := time.Now()

	result := r.db.WithContext(ctx).
		Model(&ShipModel{}).
		Where("ship_symbol = ? AND player_id = ? AND assignment_status = ?", shipSymbol, playerID, "active").
		Updates(map[string]interface{}{
			"assignment_status": "idle",
			"container_id":      nil,
			"released_at":       now,
			"release_reason":    reason,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to release ship assignment: %w", result.Error)
	}

	return nil
}

// Transfer transfers a ship assignment from one container to another
// This is used by the contract fleet coordinator to transfer ships between
// the coordinator and worker containers
func (r *ShipAssignmentRepositoryGORM) Transfer(
	ctx context.Context,
	shipSymbol string,
	fromContainerID string,
	toContainerID string,
) error {
	now := time.Now()

	result := r.db.WithContext(ctx).
		Model(&ShipModel{}).
		Where("ship_symbol = ? AND container_id = ? AND assignment_status = ?", shipSymbol, fromContainerID, "active").
		Updates(map[string]interface{}{
			"container_id": toContainerID,
			"assigned_at":  now,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to transfer ship assignment: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("no active assignment found for ship %s with container %s", shipSymbol, fromContainerID)
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
		Model(&ShipModel{}).
		Where("container_id = ? AND player_id = ? AND assignment_status = ?", containerID, playerID, "active").
		Updates(map[string]interface{}{
			"assignment_status": "idle",
			"container_id":      nil,
			"released_at":       now,
			"release_reason":    reason,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to release container assignments: %w", result.Error)
	}

	return nil
}

// ReleaseAllActive releases all active ship assignments for the given player
// Used during daemon startup to clean up zombie assignments from previous runs
// Returns the number of assignments released
//
// Captain reservations (assignment_owner="captain") are deliberately excluded:
// they use the same assignment_status="active" as a live coordinator claim, but
// a reservation's whole purpose (sp-i1ku) is to survive daemon restarts, so an
// owner-blind release here would silently un-reserve a captain-held hull on
// every restart.
func (r *ShipAssignmentRepositoryGORM) ReleaseAllActive(
	ctx context.Context,
	playerID int,
	reason string,
) (int, error) {
	now := time.Now()

	result := r.db.WithContext(ctx).
		Model(&ShipModel{}).
		Where("player_id = ? AND assignment_status = ?", playerID, "active").
		Where("assignment_owner IS NULL OR assignment_owner != ?", string(navigation.AssignmentOwnerCaptain)).
		Updates(map[string]interface{}{
			"assignment_status": "idle",
			"container_id":      nil,
			"released_at":       now,
			"release_reason":    reason,
		})

	if result.Error != nil {
		return 0, fmt.Errorf("failed to release all active assignments: %w", result.Error)
	}

	return int(result.RowsAffected), nil
}

// ShipAssignmentInfo is a bulk-read projection combining a ship's role, owning
// container (if any), and cache timestamp, used by observability CLI verbs
// (e.g. `ship list`) that need this data for every ship, idle or assigned.
type ShipAssignmentInfo struct {
	ShipSymbol  string
	Role        string
	ContainerID string // empty when the ship is idle
	SyncedAt    time.Time

	// AssignmentOwner is "captain" for a captain reservation (sp-i1ku), or
	// "container"/"" otherwise. ContainerID is always empty for a captain
	// reservation, so callers must check this field to distinguish "reserved
	// by the captain" from "genuinely idle" — both would otherwise render
	// identically as an empty ContainerID.
	AssignmentOwner string
	// AssignmentReason is the free-text reason recorded at reserve time
	// (sp-i1ku). Empty when the ship isn't captain-reserved, or when the
	// captain gave no reason.
	AssignmentReason string

	// DedicatedFleet (sp-snmb) is the ship's permanent fleet dedication (e.g.
	// "contract"), or "" when unreserved. Unlike ContainerID/AssignmentOwner
	// above, this is independent of any transient container claim. Surfaced
	// by `ship list` (sp-ioqt) so a hull pinned to the wrong fleet at
	// purchase — the sp-lybx class of mistake — is visible at a glance
	// instead of requiring a per-ship cross-check against `fleet list`.
	DedicatedFleet string
}

// ListActive returns role/assignment/cache-age info for every ship owned by a
// player, so callers can render an owning container id (or blank for idle)
// alongside each ship without per-ship lookups.
func (r *ShipAssignmentRepositoryGORM) ListActive(
	ctx context.Context,
	playerID int,
) ([]ShipAssignmentInfo, error) {
	var models []ShipModel

	if err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list ship assignments: %w", err)
	}

	infos := make([]ShipAssignmentInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, ShipAssignmentInfo{
			ShipSymbol:       model.ShipSymbol,
			Role:             model.Role,
			ContainerID:      derefString(model.ContainerID),
			SyncedAt:         model.SyncedAt,
			AssignmentOwner:  model.AssignmentOwner,
			AssignmentReason: model.AssignmentReason,
			DedicatedFleet:   model.DedicatedFleet,
		})
	}

	return infos, nil
}

// CountByContainerPrefix counts active assignments where container ID starts with prefix
func (r *ShipAssignmentRepositoryGORM) CountByContainerPrefix(
	ctx context.Context,
	prefix string,
	playerID int,
) (int, error) {
	var count int64

	result := r.db.WithContext(ctx).
		Model(&ShipModel{}).
		Where("container_id LIKE ?", prefix+"%").
		Where("player_id = ?", playerID).
		Where("assignment_status = ?", "active").
		Count(&count)

	if result.Error != nil {
		return 0, fmt.Errorf("failed to count assignments by prefix: %w", result.Error)
	}

	return int(count), nil
}

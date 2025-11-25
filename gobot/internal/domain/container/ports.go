package container

import (
	"context"
)

// ShipAssignmentRepository defines persistence operations for ship assignments
type ShipAssignmentRepository interface {
	// Assign creates or updates a ship assignment
	Assign(ctx context.Context, assignment *ShipAssignment) error

	// FindByShip retrieves the active assignment for a ship
	FindByShip(ctx context.Context, shipSymbol string, playerID int) (*ShipAssignment, error)

	// FindByShipSymbol retrieves the assignment for a ship by symbol
	FindByShipSymbol(ctx context.Context, shipSymbol string, playerID int) (*ShipAssignment, error)

	// FindByContainer retrieves all ship assignments for a container
	FindByContainer(ctx context.Context, containerID string, playerID int) ([]*ShipAssignment, error)

	// Release marks a ship assignment as released
	Release(ctx context.Context, shipSymbol string, playerID int, reason string) error

	// Transfer transfers a ship assignment from one container to another
	Transfer(ctx context.Context, shipSymbol string, fromContainerID string, toContainerID string) error

	// ReleaseByContainer releases all ship assignments for a container
	ReleaseByContainer(ctx context.Context, containerID string, playerID int, reason string) error

	// ReleaseAllActive releases all active ship assignments (used for daemon startup cleanup)
	ReleaseAllActive(ctx context.Context, reason string) (int, error)

	// CountByContainerPrefix counts active assignments where container ID starts with prefix
	CountByContainerPrefix(ctx context.Context, prefix string, playerID int) (int, error)
}

package daemon

import (
	"context"
	"errors"
)

var (
	// ErrInvalidCommandType is returned when a command has an unexpected type
	ErrInvalidCommandType = errors.New("invalid command type")
)

// Container represents a background daemon container
type Container struct {
	ID       string
	PlayerID uint
	Status   string // "STARTING", "RUNNING", "STOPPED", "FAILED", etc.
	Type     string // "scout-tour", "navigate", "contract", etc.
}

// DaemonClient defines operations for interacting with the daemon server
// This interface allows application layer commands to query and create background containers
type DaemonClient interface {
	// ListContainers retrieves all containers for a player
	ListContainers(ctx context.Context, playerID uint) ([]Container, error)

	// CreateScoutTourContainer creates a background container for scout tour operations
	// containerID: Unique container identifier (e.g., "scout-tour-scout-1-abc12345")
	// playerID: Player who owns this operation
	// command: The scout tour command to execute in the container
	CreateScoutTourContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error
}

// ShipAssignmentRepository defines persistence operations for ship assignments
type ShipAssignmentRepository interface {
	// Insert creates a new ship assignment record
	Insert(ctx context.Context, assignment *ShipAssignment) error

	// FindByShip retrieves the active assignment for a ship
	FindByShip(ctx context.Context, shipSymbol string, playerID int) (*ShipAssignment, error)

	// FindByContainer retrieves all ship assignments for a container
	FindByContainer(ctx context.Context, containerID string, playerID int) ([]*ShipAssignment, error)

	// Release marks a ship assignment as released
	Release(ctx context.Context, shipSymbol string, playerID int, reason string) error

	// ReleaseByContainer releases all ship assignments for a container
	ReleaseByContainer(ctx context.Context, containerID string, playerID int, reason string) error
}

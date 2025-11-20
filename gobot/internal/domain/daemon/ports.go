package daemon

import (
	"context"
	"errors"
)

var (
	// ErrInvalidCommandType is returned when a command has an unexpected type
	ErrInvalidCommandType = errors.New("invalid command type")
)

// ContainerInfo represents container metadata for daemon client communication.
// This is a lightweight DTO used at the gRPC boundary.
// PlayerID uses the domain standard int type.
type ContainerInfo struct {
	ID       string
	PlayerID int    // Domain standard int type
	Status   string // "STARTING", "RUNNING", "STOPPED", "FAILED", etc.
	Type     string // "scout-tour", "navigate", "contract", etc.
}

// DaemonClient defines operations for interacting with the daemon server
// This interface allows application layer commands to query and create background containers
type DaemonClient interface {
	// ListContainers retrieves all containers for a player
	ListContainers(ctx context.Context, playerID uint) ([]ContainerInfo, error)

	// CreateScoutTourContainer creates a background container for scout tour operations
	// containerID: Unique container identifier (e.g., "scout-tour-scout-1-abc12345")
	// playerID: Player who owns this operation
	// command: The scout tour command to execute in the container
	CreateScoutTourContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error

	// CreateContractWorkflowContainer creates AND STARTS a background container for contract workflow operations
	// containerID: Unique container identifier (e.g., "contract-work-SHIP-1-123456")
	// playerID: Player who owns this operation
	// command: The contract workflow command to execute in the container
	// completionCallback: Optional channel to signal completion to coordinator
	CreateContractWorkflowContainer(ctx context.Context, containerID string, playerID uint, command interface{}, completionCallback chan<- string) error

	// PersistContractWorkflowContainer creates (but does NOT start) a worker container in DB
	// This allows transferring ships to the container before starting it
	PersistContractWorkflowContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error

	// StartContractWorkflowContainer starts a previously persisted worker container
	StartContractWorkflowContainer(ctx context.Context, containerID string, completionCallback chan<- string) error

	// StopContainer stops a running container
	// containerID: The container to stop
	StopContainer(ctx context.Context, containerID string) error

	// PersistMiningWorkerContainer creates (but does NOT start) a mining worker container in DB
	PersistMiningWorkerContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error

	// StartMiningWorkerContainer starts a previously persisted mining worker container
	StartMiningWorkerContainer(ctx context.Context, containerID string, completionCallback chan<- string) error

	// PersistTransportWorkerContainer creates (but does NOT start) a transport worker container in DB
	PersistTransportWorkerContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error

	// StartTransportWorkerContainer starts a previously persisted transport worker container
	StartTransportWorkerContainer(ctx context.Context, containerID string, completionCallback chan<- string) error

	// PersistMiningCoordinatorContainer creates (but does NOT start) a mining coordinator container in DB
	PersistMiningCoordinatorContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error

	// StartMiningCoordinatorContainer starts a previously persisted mining coordinator container
	StartMiningCoordinatorContainer(ctx context.Context, containerID string) error
}

// ShipAssignmentRepository defines persistence operations for ship assignments
type ShipAssignmentRepository interface {
	// Insert creates a new ship assignment record
	Insert(ctx context.Context, assignment *ShipAssignment) error

	// Assign creates or updates a ship assignment (alias for Insert with upsert behavior)
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
}

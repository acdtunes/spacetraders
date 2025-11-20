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

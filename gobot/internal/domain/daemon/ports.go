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

	// PersistManufacturingTaskWorkerContainer creates (but does NOT start) a manufacturing task worker container in DB
	// This is for task-based parallel manufacturing (uses task ID reference, not embedded opportunity)
	PersistManufacturingTaskWorkerContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error

	// StartManufacturingTaskWorkerContainer starts a previously persisted manufacturing task worker container
	// completionCallback: Optional channel to signal completion to coordinator
	StartManufacturingTaskWorkerContainer(ctx context.Context, containerID string, completionCallback chan<- string) error

	// PersistGasSiphonWorkerContainer creates (but does NOT start) a gas siphon worker container in DB
	PersistGasSiphonWorkerContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error

	// StartGasSiphonWorkerContainer starts a previously persisted gas siphon worker container
	StartGasSiphonWorkerContainer(ctx context.Context, containerID string, completionCallback chan<- string) error

	// PersistGasTransportWorkerContainer creates (but does NOT start) a gas transport worker container in DB
	PersistGasTransportWorkerContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error

	// StartGasTransportWorkerContainer starts a previously persisted gas transport worker container
	StartGasTransportWorkerContainer(ctx context.Context, containerID string, completionCallback chan<- string) error

	// PersistStorageShipContainer creates (but does NOT start) a storage ship worker container in DB.
	// The container will navigate the ship to the gas giant and register with storage coordinator.
	PersistStorageShipContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error

	// StartStorageShipContainer starts a previously persisted storage ship worker container.
	StartStorageShipContainer(ctx context.Context, containerID string, completionCallback chan<- string) error
}

package daemon

import (
	"context"
	"errors"
)

var (
	// ErrInvalidCommandType is returned when a command has an unexpected type
	ErrInvalidCommandType = errors.New("invalid command type")

	ErrUnknownContainerKind = errors.New("unknown container kind")
)

type ContainerKind string

const (
	ContainerKindContractWorkflow        ContainerKind = "contract_workflow"
	ContainerKindManufacturingTaskWorker ContainerKind = "manufacturing_task_worker"
	ContainerKindGasSiphonWorker         ContainerKind = "gas_siphon_worker"
	ContainerKindStorageShip             ContainerKind = "storage_ship"
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

	PersistContainer(ctx context.Context, kind ContainerKind, containerID string, playerID uint, command interface{}) error

	StartContainer(ctx context.Context, kind ContainerKind, containerID string) error

	// StopContainer stops a running container
	// containerID: The container to stop
	StopContainer(ctx context.Context, containerID string) error

	// CleanupStaleManufacturingWorkers detects and stops manufacturing task workers that
	// are RUNNING but have no recent log activity (likely crashed without cleanup).
	// staleTimeoutMinutes: How long (in minutes) a worker can go without activity before being stale.
	// Returns the number of workers cleaned up.
	CleanupStaleManufacturingWorkers(ctx context.Context, playerID int, staleTimeoutMinutes int) (int64, error)
}

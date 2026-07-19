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
	ContainerKindContractWorkflow ContainerKind = "contract_workflow"
	ContainerKindGasSiphonWorker  ContainerKind = "gas_siphon_worker"
	ContainerKindStorageShip      ContainerKind = "storage_ship"
	// ContainerKindScoutTour is a scout_tour spawned as a managed worker by the
	// scout_post_coordinator: persisted with a coordinator_id so restart
	// recovery skips it and the coordinator respawns it.
	ContainerKindScoutTour ContainerKind = "scout_tour"
	// ContainerKindScoutReposition is a one-shot cross-gate relay the
	// scout_post_coordinator spawns to jump-route an idle satellite to an unmanned
	// frontier post. Like a scout_tour it is a coordinator-managed worker
	// (coordinator_id → restart recovery skips it, preserving the claim; the
	// coordinator re-adopts). It reuses the trade-route coordinator's multi-jump
	// travel() (no new jump logic) to fly the satellite to the post's system, then
	// exits; the next in-system reconcile mans the post — manning stays in-system
	// only, reposition just moves the hull there first.
	ContainerKindScoutReposition ContainerKind = "scout_reposition"
	// ContainerKindWorkerFerry is a one-shot cross-system relay the
	// worker_rebalancer_coordinator spawns to jump-route an idle light-hauler to a
	// worker-starved factory system. Like a scout_reposition relay it is a
	// coordinator-managed worker (coordinator_id → restart recovery skips it, preserving
	// the claim; the coordinator reclaims it on arrival or interruption). It reuses the
	// trade-route coordinator's multi-jump travel() (no new jump logic) to fly the hull
	// to the vacancy system, then exits; the destination factory's own idle-hauler
	// discovery claims the now-idle hull in-system.
	ContainerKindWorkerFerry ContainerKind = "worker_ferry"
	// ContainerKindCargoLiquidation is a one-shot worker the contract fleet coordinator
	// spawns on a parked-with-cargo hull to self-clear its stranded leftover cargo:
	// sell at the best in-system bid, jettison only as a last resort below a
	// configured value floor, hold otherwise. Like worker_ferry it is a
	// coordinator-managed worker (coordinator_id → restart recovery skips it, preserving
	// the claim; the coordinator re-evaluates the now-cleared hull on its next pass). It
	// reuses the existing navigate/dock/sell/jettison commands — no new ship I/O.
	ContainerKindCargoLiquidation ContainerKind = "cargo_liquidation"
)

// ContainerInfo represents container metadata for daemon client communication.
// This is a lightweight DTO used at the gRPC boundary.
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
}

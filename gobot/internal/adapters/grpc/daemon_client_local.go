package grpc

import (
	"context"

	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
)

// DaemonClientLocal implements daemon.DaemonClient by directly calling the daemon server
// This is used during daemon startup to avoid circular dependency (server can't gRPC to itself before starting)
type DaemonClientLocal struct {
	server *DaemonServer
}

// NewDaemonClientLocal creates a new local daemon client
func NewDaemonClientLocal(server *DaemonServer) *DaemonClientLocal {
	return &DaemonClientLocal{
		server: server,
	}
}

// ListContainers retrieves all containers for a player
func (c *DaemonClientLocal) ListContainers(ctx context.Context, playerID uint) ([]daemon.ContainerInfo, error) {
	playerIDInt := int(playerID)
	containers := c.server.ListContainers(&playerIDInt, nil)

	result := make([]daemon.ContainerInfo, 0, len(containers))
	for _, cont := range containers {
		result = append(result, daemon.ContainerInfo{
			ID:       cont.ID(),
			PlayerID: cont.PlayerID(),
			Status:   string(cont.Status()),
			Type:     string(cont.Type()),
		})
	}

	return result, nil
}

// CreateScoutTourContainer creates a background container for scout tour operations
func (c *DaemonClientLocal) CreateScoutTourContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	// Type assert to ScoutTourCommand
	cmd, ok := command.(*scoutingCmd.ScoutTourCommand)
	if !ok {
		return daemon.ErrInvalidCommandType
	}

	// Call server's ScoutTour method directly (bypasses gRPC layer)
	_, err := c.server.ScoutTour(ctx, containerID, cmd.ShipSymbol, cmd.Markets, cmd.Iterations, int(playerID))
	return err
}

// CreateContractWorkflowContainer creates AND STARTS a background container for contract workflow operations
func (c *DaemonClientLocal) CreateContractWorkflowContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
	completionCallback chan<- string,
) error {
	// Type assert to ContractWorkflowCommand
	cmd, ok := command.(*contractCmd.RunWorkflowCommand)
	if !ok {
		return daemon.ErrInvalidCommandType
	}

	// Call server's ContractWorkflow method directly
	_, err := c.server.ContractWorkflow(ctx, containerID, cmd.ShipSymbol, int(playerID), cmd.CoordinatorID, completionCallback)
	return err
}

// PersistContractWorkflowContainer creates (but does NOT start) a worker container in DB
func (c *DaemonClientLocal) PersistContractWorkflowContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	// Type assert to ContractWorkflowCommand
	cmd, ok := command.(*contractCmd.RunWorkflowCommand)
	if !ok {
		return daemon.ErrInvalidCommandType
	}

	// Persist only, don't start
	return c.server.PersistContractWorkflow(ctx, containerID, cmd.ShipSymbol, int(playerID), cmd.CoordinatorID)
}

// StartContractWorkflowContainer starts a previously persisted worker container
func (c *DaemonClientLocal) StartContractWorkflowContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	return c.server.StartContractWorkflow(ctx, containerID, completionCallback)
}

// StopContainer stops a running container
func (c *DaemonClientLocal) StopContainer(ctx context.Context, containerID string) error {
	// Call server's StopContainer method directly (bypasses gRPC layer)
	return c.server.StopContainer(containerID)
}

// PersistManufacturingTaskWorkerContainer creates (but does NOT start) a manufacturing task worker container in DB
func (c *DaemonClientLocal) PersistManufacturingTaskWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	return c.server.PersistManufacturingTaskWorkerContainer(ctx, containerID, playerID, command)
}

// StartManufacturingTaskWorkerContainer starts a previously persisted manufacturing task worker container
func (c *DaemonClientLocal) StartManufacturingTaskWorkerContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	return c.server.StartManufacturingTaskWorkerContainer(ctx, containerID, completionCallback)
}

// PersistGasSiphonWorkerContainer creates (but does NOT start) a gas siphon worker container in DB
func (c *DaemonClientLocal) PersistGasSiphonWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	return c.server.PersistGasSiphonWorkerContainer(ctx, containerID, playerID, command)
}

// StartGasSiphonWorkerContainer starts a previously persisted gas siphon worker container
func (c *DaemonClientLocal) StartGasSiphonWorkerContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	return c.server.StartGasSiphonWorkerContainer(ctx, containerID, completionCallback)
}

// PersistGasTransportWorkerContainer creates (but does NOT start) a gas transport worker container in DB
func (c *DaemonClientLocal) PersistGasTransportWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	return c.server.PersistGasTransportWorkerContainer(ctx, containerID, playerID, command)
}

// StartGasTransportWorkerContainer starts a previously persisted gas transport worker container
func (c *DaemonClientLocal) StartGasTransportWorkerContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	return c.server.StartGasTransportWorkerContainer(ctx, containerID, completionCallback)
}

// PersistStorageShipContainer creates (but does NOT start) a storage ship worker container in DB
func (c *DaemonClientLocal) PersistStorageShipContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	return c.server.PersistStorageShipContainer(ctx, containerID, playerID, command)
}

// StartStorageShipContainer starts a previously persisted storage ship worker container
func (c *DaemonClientLocal) StartStorageShipContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	return c.server.StartStorageShipContainer(ctx, containerID, completionCallback)
}

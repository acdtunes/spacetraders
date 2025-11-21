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

// PersistMiningWorkerContainer creates (but does NOT start) a mining worker container in DB
func (c *DaemonClientLocal) PersistMiningWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	return c.server.PersistMiningWorkerContainer(ctx, containerID, playerID, command)
}

// StartMiningWorkerContainer starts a previously persisted mining worker container
func (c *DaemonClientLocal) StartMiningWorkerContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	return c.server.StartMiningWorkerContainer(ctx, containerID, completionCallback)
}

// PersistTransportWorkerContainer creates (but does NOT start) a transport worker container in DB
func (c *DaemonClientLocal) PersistTransportWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	return c.server.PersistTransportWorkerContainer(ctx, containerID, playerID, command)
}

// StartTransportWorkerContainer starts a previously persisted transport worker container
func (c *DaemonClientLocal) StartTransportWorkerContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	return c.server.StartTransportWorkerContainer(ctx, containerID, completionCallback)
}

// PersistMiningCoordinatorContainer creates (but does NOT start) a mining coordinator container in DB
func (c *DaemonClientLocal) PersistMiningCoordinatorContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	return c.server.PersistMiningCoordinatorContainer(ctx, containerID, playerID, command)
}

// StartMiningCoordinatorContainer starts a previously persisted mining coordinator container
func (c *DaemonClientLocal) StartMiningCoordinatorContainer(
	ctx context.Context,
	containerID string,
) error {
	return c.server.StartMiningCoordinatorContainer(ctx, containerID)
}

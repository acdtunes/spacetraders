package grpc

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/application/scouting"
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
func (c *DaemonClientLocal) ListContainers(ctx context.Context, playerID uint) ([]daemon.Container, error) {
	playerIDInt := int(playerID)
	containers := c.server.ListContainers(&playerIDInt, nil)

	result := make([]daemon.Container, 0, len(containers))
	for _, cont := range containers {
		result = append(result, daemon.Container{
			ID:       cont.ID(),
			PlayerID: uint(cont.PlayerID()),
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
	cmd, ok := command.(*scouting.ScoutTourCommand)
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
	cmd, ok := command.(*contract.ContractWorkflowCommand)
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
	cmd, ok := command.(*contract.ContractWorkflowCommand)
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

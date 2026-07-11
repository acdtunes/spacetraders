package grpc

import (
	"context"
	"fmt"

	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	liquidationCmd "github.com/andrescamacho/spacetraders-go/internal/application/liquidation"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
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

func (c *DaemonClientLocal) PersistContainer(
	ctx context.Context,
	kind daemon.ContainerKind,
	containerID string,
	playerID uint,
	command interface{},
) error {
	switch kind {
	case daemon.ContainerKindContractWorkflow:
		cmd, ok := command.(*contractCmd.RunWorkflowCommand)
		if !ok {
			return daemon.ErrInvalidCommandType
		}
		return c.server.PersistContractWorkflow(ctx, containerID, cmd.ShipSymbol, int(playerID), cmd.CoordinatorID)
	case daemon.ContainerKindManufacturingTaskWorker:
		return c.server.PersistManufacturingTaskWorkerContainer(ctx, containerID, playerID, command)
	case daemon.ContainerKindGasSiphonWorker:
		return c.server.PersistGasSiphonWorkerContainer(ctx, containerID, playerID, command)
	case daemon.ContainerKindStorageShip:
		return c.server.PersistStorageShipContainer(ctx, containerID, playerID, command)
	case daemon.ContainerKindScoutTour:
		cmd, ok := command.(*scoutingCmd.ScoutTourCommand)
		if !ok {
			return daemon.ErrInvalidCommandType
		}
		return c.server.PersistScoutTourWorker(ctx, containerID, cmd.ShipSymbol, cmd.Markets, cmd.Iterations, int(cmd.ScanInterval.Seconds()), int(playerID), cmd.CoordinatorID)
	case daemon.ContainerKindScoutReposition:
		cmd, ok := command.(*scoutingCmd.ScoutRepositionCommand)
		if !ok {
			return daemon.ErrInvalidCommandType
		}
		// sp-o34q: forward MaxRepositionJumps — the segment that dropped the bound on the way to
		// the persisted config, degrading the live relay to the strict 5-jump resolver.
		return c.server.PersistScoutRepositionWorker(ctx, containerID, cmd.ShipSymbol, cmd.DestinationWaypoint, int(playerID), cmd.CoordinatorID, cmd.MaxRepositionJumps)
	case daemon.ContainerKindWorkerFerry:
		cmd, ok := command.(*tradingCmd.WorkerFerryCommand)
		if !ok {
			return daemon.ErrInvalidCommandType
		}
		return c.server.PersistWorkerFerryWorker(ctx, containerID, cmd.ShipSymbol, cmd.DestinationWaypoint, int(playerID), cmd.CoordinatorID)
	case daemon.ContainerKindCargoLiquidation:
		cmd, ok := command.(*liquidationCmd.LiquidateCargoCommand)
		if !ok {
			return daemon.ErrInvalidCommandType
		}
		return c.server.PersistCargoLiquidationWorker(ctx, containerID, cmd.ShipSymbol, cmd.MinJettisonValue, int(playerID), cmd.CoordinatorID)
	}
	return fmt.Errorf("%w: %q", daemon.ErrUnknownContainerKind, kind)
}

func (c *DaemonClientLocal) StartContainer(
	ctx context.Context,
	kind daemon.ContainerKind,
	containerID string,
) error {
	switch kind {
	case daemon.ContainerKindContractWorkflow:
		return c.server.StartContractWorkflow(ctx, containerID)
	case daemon.ContainerKindManufacturingTaskWorker:
		return c.server.StartManufacturingTaskWorkerContainer(ctx, containerID)
	case daemon.ContainerKindGasSiphonWorker:
		return c.server.StartGasSiphonWorkerContainer(ctx, containerID)
	case daemon.ContainerKindStorageShip:
		return c.server.StartStorageShipContainer(ctx, containerID)
	case daemon.ContainerKindScoutTour:
		return c.server.StartScoutTour(ctx, containerID)
	case daemon.ContainerKindScoutReposition:
		return c.server.StartScoutReposition(ctx, containerID)
	case daemon.ContainerKindWorkerFerry:
		return c.server.StartWorkerFerry(ctx, containerID)
	case daemon.ContainerKindCargoLiquidation:
		return c.server.StartCargoLiquidation(ctx, containerID)
	}
	return fmt.Errorf("%w: %q", daemon.ErrUnknownContainerKind, kind)
}

// StopContainer stops a running container
func (c *DaemonClientLocal) StopContainer(ctx context.Context, containerID string) error {
	// Call server's StopContainer method directly (bypasses gRPC layer)
	return c.server.StopContainer(containerID)
}

// CleanupStaleManufacturingWorkers detects and stops manufacturing task workers that
// are RUNNING but have no recent log activity (likely crashed without cleanup).
func (c *DaemonClientLocal) CleanupStaleManufacturingWorkers(
	ctx context.Context,
	playerID int,
	staleTimeoutMinutes int,
) (int64, error) {
	return c.server.CleanupStaleManufacturingWorkers(ctx, playerID, staleTimeoutMinutes)
}

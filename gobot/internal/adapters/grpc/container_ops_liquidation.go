package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// PersistCargoLiquidationWorker persists (but does NOT start) a cargo_liquidation
// container the contract fleet coordinator manages (sp-39oi). Like a worker_ferry it
// carries a coordinator_id and a parent link, so daemon restart recovery SKIPS it (marks
// it worker_interrupted, preserving the ship claim) and leaves the reclaim/re-evaluation
// to the coordinator's next pass. It wraps exactly ONE iteration — the whole liquidation —
// and the coordinator owns re-dispatch (CoordinatorOwnsIterations in the registry). Twin of
// PersistWorkerFerryWorker. minJettisonValue is persisted so a mid-liquidation restart
// rebuilds the same last-resort jettison floor.
func (s *DaemonServer) PersistCargoLiquidationWorker(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	minJettisonValue int,
	playerID int,
	coordinatorID string,
) error {
	config := map[string]interface{}{
		"ship_symbol":        shipSymbol,
		"min_jettison_value": minJettisonValue,
		"coordinator_id":     coordinatorID,
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeCargoLiquidation,
		playerID,
		1, // one iteration = the whole liquidation; the coordinator owns re-dispatch
		&coordinatorID,
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "cargo_liquidation"); err != nil {
		return fmt.Errorf("failed to persist cargo liquidation worker: %w", err)
	}
	return nil
}

// StartCargoLiquidation starts a previously persisted cargo_liquidation container (the
// coordinator-managed liquidation path). Mirrors StartWorkerFerry: load the persisted
// model, rebuild the command from its config, and run it.
func (s *DaemonServer) StartCargoLiquidation(ctx context.Context, containerID string) error {
	allContainers, err := s.containerRepo.ListAll(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	var containerModel *persistence.ContainerModel
	for _, c := range allContainers {
		if c.ID == containerID {
			containerModel = c
			break
		}
	}
	if containerModel == nil {
		return fmt.Errorf("container %s not found", containerID)
	}

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	cmd, err := s.buildCommandForType("cargo_liquidation", config, containerModel.PlayerID, containerModel.ID)
	if err != nil {
		return fmt.Errorf("failed to create command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		1, // one iteration = the whole liquidation
		containerModel.ParentContainerID,
		config,
		nil,
	)

	s.startContainerRunner(containerEntity, cmd, containerID, "Container")

	return nil
}

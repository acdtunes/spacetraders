package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// PersistWorkerFerryWorker persists (but does NOT start) a worker_ferry container: a one-shot
// cross-system relay that moves a hull to a destination waypoint. Like a scout_reposition worker
// it carries a coordinator_id and a parent link, so daemon restart recovery SKIPS it (marks it
// worker_interrupted, preserving the ship claim) and leaves the reclaim/re-evaluation to the
// managing coordinator's reconcile pass. It wraps exactly ONE iteration — the whole ferry — and
// the coordinator owns re-dispatch (CoordinatorOwnsIterations in the registry). Twin of
// the retired scout-reposition relay. (Retained after the factory-ops retirement, sp-hoj8u: worker_ferry
// is a generic hull-move primitive the daemon's persist/start dispatch and container recovery still
// reference; its former spawner — the worker_rebalancer_coordinator — was retired with the factories.)
func (s *DaemonServer) PersistWorkerFerryWorker(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	destinationWaypoint string,
	playerID int,
	coordinatorID string,
) error {
	config := map[string]interface{}{
		"ship_symbol":    shipSymbol,
		"destination":    destinationWaypoint,
		"coordinator_id": coordinatorID,
		// The ferry-reposition jump bound, sourced from the daemon's live [trade_fleet]
		// config (the SAME daemon-global tuning the tour reposition rides, sp-kl16 — a ferry is a
		// hull MOVE, so it earns the same stored-adjacency reach past unreadable frontier gates).
		// Persisted as-is (0 too, so an absent knob survives a rebuild unchanged);
		// buildWorkerFerryCommand reads it back and the ferry's Handle resolves 0/absent → its
		// default 12. This is the o34q WRITE side — unlike the TOP-LEVEL tour, the ferry IS
		// persisted via PersistContainer and rebuilt on every start, so an unstamped bound would be
		// DROPPED and the cross-cluster leg would silently degrade to the strict fail-closed
		// resolver.
		"reposition_jump_bound": s.tradeFleetConfig.RepositionJumpBound,
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeWorkerFerry,
		playerID,
		1, // one iteration = the whole ferry; the coordinator owns re-dispatch
		&coordinatorID,
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "worker_ferry"); err != nil {
		return fmt.Errorf("failed to persist worker ferry worker: %w", err)
	}
	return nil
}

// StartWorkerFerry starts a previously persisted worker_ferry container (the
// coordinator-managed ferry path). Load the persisted model,
// rebuild the command from its config, and run it.
func (s *DaemonServer) StartWorkerFerry(ctx context.Context, containerID string) error {
	containerModel, err := s.findContainerModelByID(ctx, containerID)
	if err != nil {
		return err
	}

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	cmd, err := s.buildCommandForType("worker_ferry", config, containerModel.PlayerID, containerModel.ID)
	if err != nil {
		return fmt.Errorf("failed to create command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		1, // one iteration = the whole ferry
		containerModel.ParentContainerID,
		config,
		nil,
	)

	s.startContainerRunner(containerEntity, cmd, containerID, "Container")

	return nil
}

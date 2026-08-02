package grpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// ListContainers returns all registered containers
func (s *DaemonServer) ListContainers(playerID *int, status *string) []*container.Container {
	s.containersMu.RLock()
	defer s.containersMu.RUnlock()

	containers := make([]*container.Container, 0, len(s.containers))

	// Parse comma-separated status filter into map for O(1) lookup
	var allowedStatuses map[string]bool
	if status != nil && *status != "" {
		allowedStatuses = make(map[string]bool)
		statuses := strings.Split(*status, ",")
		for _, s := range statuses {
			trimmed := strings.TrimSpace(s)
			if trimmed != "" {
				allowedStatuses[trimmed] = true
			}
		}
	}

	for _, runner := range s.containers {
		cont := runner.Container()

		if playerID != nil && cont.PlayerID() != *playerID {
			continue
		}

		if allowedStatuses != nil {
			if !allowedStatuses[string(cont.Status())] {
				continue
			}
		}

		containers = append(containers, cont)
	}

	return containers
}

// GetContainer retrieves a specific container
func (s *DaemonServer) GetContainer(containerID string) (*container.Container, error) {
	s.containersMu.RLock()
	defer s.containersMu.RUnlock()

	runner, exists := s.containers[containerID]
	if !exists {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}

	return runner.Container(), nil
}

// PersistedContainerConfig returns a container's config JSON as persisted in the
// database — Store A, the source of truth that live config mutations write
// (UpdateContainerConfig, e.g. a `fleet hub add|remove`). The in-memory container
// entity's Metadata() is frozen at launch by NewContainer and does NOT reflect a
// live mutation until a daemon restart rebuilds the entity, so the `container get`
// read path must source the displayed config here rather than serialize the launch
// snapshot (sp-aoy2). found is false when no row exists for the (id, playerID) pair.
func (s *DaemonServer) PersistedContainerConfig(ctx context.Context, containerID string, playerID int) (config string, found bool, err error) {
	model, err := s.containerRepo.Get(ctx, containerID, playerID)
	if err != nil {
		return "", false, fmt.Errorf("failed to read persisted container config: %w", err)
	}
	if model == nil {
		return "", false, nil
	}
	return model.Config, true, nil
}

// StopContainer stops a running container and all its child containers
func (s *DaemonServer) StopContainer(containerID string) error {
	s.containersMu.RLock()
	runner, exists := s.containers[containerID]
	s.containersMu.RUnlock()

	if !exists {
		return fmt.Errorf("container not found: %s", containerID)
	}

	playerID := runner.containerEntity.PlayerID()

	// Find and stop all child containers first (depth-first)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	childContainers, err := s.containerRepo.FindChildContainers(ctx, containerID, playerID)
	if err != nil {
		fmt.Printf("Warning: failed to find child containers for %s: %v\n", containerID, err)
	} else {
		for _, child := range childContainers {
			// Only stop RUNNING or PENDING children
			if child.Status != "RUNNING" && child.Status != "PENDING" {
				continue
			}

			s.containersMu.RLock()
			childRunner, childExists := s.containers[child.ID]
			s.containersMu.RUnlock()

			if childExists {
				fmt.Printf("Stopping child container: %s\n", child.ID)
				if err := childRunner.Stop(); err != nil {
					fmt.Printf("Warning: failed to stop child container %s: %v\n", child.ID, err)
				}
			} else {
				// Child not in memory (orphaned) - update DB directly
				fmt.Printf("Marking orphaned child container as stopped: %s\n", child.ID)
				now := time.Now()
				exitCode := 0
				if err := s.containerRepo.UpdateStatus(ctx, child.ID, playerID, container.ContainerStatusStopped, &now, &exitCode, "parent stopped"); err != nil {
					fmt.Printf("Warning: failed to update orphaned child container %s: %v\n", child.ID, err)
				}
			}
		}
	}

	// Now stop the parent container
	stopErr := runner.Stop()

	// A gas coordinator's storage_operations row must be terminalized
	// alongside its container. Left at RUNNING, every manufacturing coordinator
	// keeps discovering an "active" storage source at a now-dead coordinator and
	// spawns STORAGE_ACQUIRE_DELIVER tasks against ships that are no longer there
	// - the recurring storage wedge. ctx is still live here (unlike the stopped
	// container's own cancelled ctx), so this write isn't racing shutdown.
	//
	// A warehouse container's storage_operations row needs the identical
	// terminalization, for the identical reason. Left un-terminalized, the stale
	// "zombie" RUNNING row keeps surfacing alongside its live replacement at the
	// same waypoint - the stocker/tour warehouse lookup can resolve to the dead
	// operation (whose registered storage ships are gone, so it always reads back
	// zero free space) and wrongly declare a warehouse with real free space full.
	// operationID == containerID for both container types (see
	// command_factory_registry.go's buildWarehouseCommand / gas-coordinator
	// equivalent), so the single call below covers both.
	if runner.containerEntity.Type() == container.ContainerTypeGasCoordinator ||
		runner.containerEntity.Type() == container.ContainerTypeWarehouse {
		s.terminalizeStorageOperation(ctx, containerID)
	}

	return stopErr
}

// terminalizeStorageOperation moves a gas coordinator's or warehouse's
// storage_operations row to a terminal status when its container is stopped
// (sp-86yb gas coordinators; sp-3lj5 extends this to warehouses). No-ops if
// there's no matching row, or it already reached a terminal status (idempotent -
// never clobbers e.g. an already-COMPLETED row back to STOPPED).
func (s *DaemonServer) terminalizeStorageOperation(ctx context.Context, operationID string) {
	if s.db == nil {
		return
	}

	storageOpRepo := persistence.NewStorageOperationRepository(s.db, s.clock)
	op, err := storageOpRepo.FindByID(ctx, operationID)
	if err != nil {
		fmt.Printf("Warning: failed to load storage operation %s for terminalization: %v\n", operationID, err)
		return
	}
	if op == nil || op.IsFinished() {
		return
	}

	if err := op.Stop(); err != nil {
		fmt.Printf("Warning: failed to transition storage operation %s to stopped: %v\n", operationID, err)
		return
	}

	if err := storageOpRepo.Update(ctx, op); err != nil {
		fmt.Printf("Warning: failed to persist stopped storage operation %s: %v\n", operationID, err)
	}
}

func (s *DaemonServer) registerContainer(containerID string, runner *ContainerRunner) {
	s.containersMu.Lock()
	defer s.containersMu.Unlock()
	s.containers[containerID] = runner
}

// interruptAllContainers interrupts all container goroutines and marks them as INTERRUPTED
// Allows containers to be recovered on daemon restart
func (s *DaemonServer) interruptAllContainers() {
	s.containersMu.Lock()
	runners := make([]*ContainerRunner, 0, len(s.containers))
	for _, runner := range s.containers {
		runners = append(runners, runner)
	}
	s.containersMu.Unlock()

	fmt.Printf("Interrupting %d running container(s) (will be recovered on restart)...\n", len(runners))

	// Cancel all container contexts to stop goroutines
	for _, runner := range runners {
		runner.cancelFunc() // Stop goroutine execution
	}

	// Wait briefly for goroutines to exit
	time.Sleep(1 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, runner := range runners {
		// Only mark as INTERRUPTED if container is RUNNING
		// Skip containers that are already in terminal states (STOPPED, COMPLETED, FAILED)
		currentStatus := runner.containerEntity.Status()
		if currentStatus != container.ContainerStatusRunning {
			fmt.Printf("Skipping container %s (status: %s, not RUNNING)\n", runner.containerEntity.ID(), currentStatus)
			continue
		}

		now := time.Now()
		if err := s.containerRepo.UpdateStatus(
			ctx,
			runner.containerEntity.ID(),
			runner.containerEntity.PlayerID(),
			container.ContainerStatusInterrupted,
			&now,              // stoppedAt - when daemon interrupted
			nil,               // exitCode - nil for interruption
			"daemon_shutdown", // exitReason
		); err != nil {
			fmt.Printf("Warning: Failed to mark container %s as INTERRUPTED: %v\n", runner.containerEntity.ID(), err)
		}
	}

	fmt.Println("All containers interrupted and marked as INTERRUPTED in database")
}

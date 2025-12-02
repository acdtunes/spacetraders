package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ContainerRepository interface for querying container state
type ContainerRepository interface {
	ListByStatusSimple(ctx context.Context, status string, playerID *int) ([]persistence.ContainerSummary, error)
}

// WorkerLifecycleManager handles worker container lifecycle operations
type WorkerLifecycleManager struct {
	daemonClient  daemon.DaemonClient
	containerRepo ContainerRepository
	shipRepo      navigation.ShipRepository
}

// NewWorkerLifecycleManager creates a new worker lifecycle manager service
func NewWorkerLifecycleManager(
	daemonClient daemon.DaemonClient,
	containerRepo ContainerRepository,
	shipRepo navigation.ShipRepository,
) *WorkerLifecycleManager {
	return &WorkerLifecycleManager{
		daemonClient:  daemonClient,
		containerRepo: containerRepo,
		shipRepo:      shipRepo,
	}
}

// FindExistingWorkers finds any existing ContractWorkflow containers that might still be running
func (m *WorkerLifecycleManager) FindExistingWorkers(
	ctx context.Context,
	playerID int,
) ([]persistence.ContainerSummary, error) {
	// Query for RUNNING contract workflow containers
	runningWorkers, err := m.containerRepo.ListByStatusSimple(ctx, "RUNNING", &playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to query running workers: %w", err)
	}

	// Filter for CONTRACT_WORKFLOW type only
	var workers []persistence.ContainerSummary
	for _, container := range runningWorkers {
		if container.ContainerType == "CONTRACT_WORKFLOW" {
			workers = append(workers, container)
		}
	}

	return workers, nil
}

// StopExistingWorkers stops existing worker containers with resilience logic:
// - If a worker's ship has cargo, let it continue (resilient restart)
// - If a worker's ship is empty, stop it (cleanup stale containers)
func (m *WorkerLifecycleManager) StopExistingWorkers(ctx context.Context, playerID int) error {
	logger := common.LoggerFromContext(ctx)

	existingWorkers, err := m.FindExistingWorkers(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to check for existing workers: %w", err)
	}

	if len(existingWorkers) == 0 {
		return nil
	}

	logger.Log("INFO", fmt.Sprintf("Found %d existing CONTRACT_WORKFLOW workers - checking for in-progress deliveries", len(existingWorkers)), nil)

	stoppedCount := 0
	resumedCount := 0

	for _, worker := range existingWorkers {
		// Get ships assigned to this container (using Ship aggregate)
		assignedShips, err := m.shipRepo.FindByContainer(ctx, worker.ID, shared.MustNewPlayerID(playerID))
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to get ships for container %s: %v - stopping it", worker.ID, err), nil)
			_ = m.daemonClient.StopContainer(ctx, worker.ID)
			stoppedCount++
			continue
		}

		if len(assignedShips) == 0 {
			logger.Log("INFO", fmt.Sprintf("Container %s has no assigned ships - stopping it", worker.ID), nil)
			_ = m.daemonClient.StopContainer(ctx, worker.ID)
			stoppedCount++
			continue
		}

		// Check if any assigned ship has cargo (indicating in-progress delivery)
		hasCargoInProgress := false
		for _, ship := range assignedShips {
			if !ship.Cargo().IsEmpty() {
				logger.Log("INFO", fmt.Sprintf("Ship %s has %d units of cargo - allowing container %s to resume and complete delivery",
					ship.ShipSymbol(), ship.Cargo().Units, worker.ID), map[string]interface{}{
					"container_id": worker.ID,
					"ship_symbol":  ship.ShipSymbol(),
					"cargo_units":  ship.Cargo().Units,
					"action":       "resume_delivery",
				})
				hasCargoInProgress = true
				break
			}
		}

		if hasCargoInProgress {
			// Don't stop this container - let it resume and complete delivery
			resumedCount++
		} else {
			// No cargo found, safe to stop this container
			logger.Log("INFO", fmt.Sprintf("Container %s has no cargo in progress - stopping it", worker.ID), nil)
			_ = m.daemonClient.StopContainer(ctx, worker.ID)
			stoppedCount++
		}
	}

	if resumedCount > 0 {
		logger.Log("INFO", fmt.Sprintf("Resilient restart: %d workers with cargo will continue delivery, %d empty workers stopped",
			resumedCount, stoppedCount), nil)
	} else if stoppedCount > 0 {
		logger.Log("INFO", fmt.Sprintf("Stopped %d empty worker containers", stoppedCount), nil)
	}

	return nil
}

// StopWorkerContainer stops a specific worker container
func (m *WorkerLifecycleManager) StopWorkerContainer(ctx context.Context, containerID string) error {
	return m.daemonClient.StopContainer(ctx, containerID)
}

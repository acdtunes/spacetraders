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

func (m *WorkerLifecycleManager) ReclaimShipsFromInterruptedWorkers(
	ctx context.Context,
	playerID int,
	clock shared.Clock,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	failedContainers, err := m.containerRepo.ListByStatusSimple(ctx, "FAILED", &playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to query failed workers: %w", err)
	}

	reclaimed := 0
	for _, worker := range failedContainers {
		if worker.ContainerType != "CONTRACT_WORKFLOW" {
			continue
		}
		ships, err := m.shipRepo.FindByContainer(ctx, worker.ID, shared.MustNewPlayerID(playerID))
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to get ships for failed container %s: %v", worker.ID, err), nil)
			continue
		}
		for _, ship := range ships {
			if !ship.IsAssigned() {
				continue
			}
			ship.ForceRelease("worker_interrupted", clock)
			if err := m.shipRepo.Save(ctx, ship); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to save reclaimed ship %s from container %s: %v", ship.ShipSymbol(), worker.ID, err), nil)
				continue
			}
			logger.Log("INFO", fmt.Sprintf("Reclaimed ship %s from interrupted worker %s", ship.ShipSymbol(), worker.ID), nil)
			reclaimed++
		}
	}

	return reclaimed, nil
}

// FindInterruptedWorkerShipsWithCargo returns ships still holding cargo that were
// assigned to CONTRACT_WORKFLOW workers a daemon restart marked FAILED
// (markWorkerInterrupted). Such a ship is mid-delivery — it holds contract cargo
// for an already-accepted contract — so the coordinator re-adopts it to RESUME the
// delivery leg (readoptInterruptedDeliveries) rather than restarting the workflow
// from negotiate/find-purchase-market (sp-tgp5). Ships with empty cargo are NOT
// returned: they were mid-purchase or mid-navigation with nothing aboard, and
// ReclaimShipsFromInterruptedWorkers correctly frees them into normal discovery.
// This is the read-only counterpart to that reclaim — it identifies which
// interrupted ships have delivery work to salvage, and never mutates ship state.
func (m *WorkerLifecycleManager) FindInterruptedWorkerShipsWithCargo(
	ctx context.Context,
	playerID int,
) ([]*navigation.Ship, error) {
	failedContainers, err := m.containerRepo.ListByStatusSimple(ctx, "FAILED", &playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to query failed workers: %w", err)
	}

	var laden []*navigation.Ship
	for _, worker := range failedContainers {
		if worker.ContainerType != "CONTRACT_WORKFLOW" {
			continue
		}
		ships, err := m.shipRepo.FindByContainer(ctx, worker.ID, shared.MustNewPlayerID(playerID))
		if err != nil {
			return nil, fmt.Errorf("failed to get ships for failed container %s: %w", worker.ID, err)
		}
		for _, ship := range ships {
			if !ship.IsAssigned() {
				continue
			}
			if ship.Cargo().IsEmpty() {
				continue
			}
			laden = append(laden, ship)
		}
	}
	return laden, nil
}

// StopWorkerContainer stops a specific worker container
func (m *WorkerLifecycleManager) StopWorkerContainer(ctx context.Context, containerID string) error {
	return m.daemonClient.StopContainer(ctx, containerID)
}

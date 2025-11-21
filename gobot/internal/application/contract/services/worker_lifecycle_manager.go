package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
)

// ContainerRepository interface for querying container state
type ContainerRepository interface {
	ListByStatusSimple(ctx context.Context, status string, playerID *int) ([]persistence.ContainerSummary, error)
}

// WorkerLifecycleManager handles worker container lifecycle operations
type WorkerLifecycleManager struct {
	daemonClient  daemon.DaemonClient
	containerRepo ContainerRepository
}

// NewWorkerLifecycleManager creates a new worker lifecycle manager service
func NewWorkerLifecycleManager(
	daemonClient daemon.DaemonClient,
	containerRepo ContainerRepository,
) *WorkerLifecycleManager {
	return &WorkerLifecycleManager{
		daemonClient:  daemonClient,
		containerRepo: containerRepo,
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

// StopExistingWorkers stops all existing worker containers from previous sessions
func (m *WorkerLifecycleManager) StopExistingWorkers(ctx context.Context, playerID int) error {
	logger := common.LoggerFromContext(ctx)

	existingWorkers, err := m.FindExistingWorkers(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to check for existing workers: %w", err)
	}

	if len(existingWorkers) == 0 {
		return nil
	}

	logger.Log("WARNING", fmt.Sprintf("Found %d existing CONTRACT_WORKFLOW workers from previous session - stopping them to prevent conflicts", len(existingWorkers)), nil)

	for _, worker := range existingWorkers {
		logger.Log("INFO", fmt.Sprintf("Stopping existing worker container: %s", worker.ID), nil)
		if err := m.daemonClient.StopContainer(ctx, worker.ID); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to stop existing worker %s: %v", worker.ID, err), nil)
		}
	}

	logger.Log("INFO", "All existing workers stopped, coordinator will create new workers as needed", nil)
	return nil
}

// StopWorkerContainer stops a specific worker container
func (m *WorkerLifecycleManager) StopWorkerContainer(ctx context.Context, containerID string) error {
	return m.daemonClient.StopContainer(ctx, containerID)
}

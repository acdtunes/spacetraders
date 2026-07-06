package services

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// SupplyMonitor polls factories and marks COLLECT tasks as ready when supply reaches HIGH.
// It runs as a background service, periodically checking factory supply levels.
// It composes three collaborators:
//   - FactorySupplyPoller: poll loop, factory supply checks, COLLECT_SELL creation
//   - ReplenishmentPlanner: ACQUIRE_DELIVER / STORAGE_ACQUIRE_DELIVER creation
//   - TaskActivator: PENDING<->READY transitions for gated tasks
type SupplyMonitor struct {
	poller    *FactorySupplyPoller
	activator *TaskActivator
}

// NewSupplyMonitor creates a new supply monitor.
// The sell market distributor and event publisher are supplied by the
// composition root; eventPublisher may be nil when no coordination is needed.
func NewSupplyMonitor(
	marketRepo market.MarketRepository,
	factoryTracker *manufacturing.FactoryStateTracker,
	factoryStateRepo manufacturing.FactoryStateRepository,
	pipelineRepo manufacturing.PipelineRepository,
	taskQueue ManufacturingTaskQueue,
	taskRepo manufacturing.TaskRepository,
	sellMarketDistrib *SellMarketDistributor,
	marketLocator *MarketLocator,
	storageOpRepo storage.StorageOperationRepository,
	eventPublisher navigation.ShipEventPublisher,
	pollInterval time.Duration,
	playerID int,
) *SupplyMonitor {
	notifier := &taskReadyNotifier{publisher: eventPublisher, playerID: playerID}
	supply := marketSupplyReader{marketRepo: marketRepo, playerID: playerID}
	activator := &TaskActivator{
		taskRepo:      taskRepo,
		pipelineRepo:  pipelineRepo,
		taskQueue:     taskQueue,
		marketLocator: marketLocator,
		supply:        supply,
		playerID:      playerID,
		notifier:      notifier,
	}
	replenisher := &ReplenishmentPlanner{
		marketRepo:     marketRepo,
		taskRepo:       taskRepo,
		taskQueue:      taskQueue,
		pipelineRepo:   pipelineRepo,
		marketLocator:  marketLocator,
		storageSources: NewStorageSourceFinder(storageOpRepo),
		supply:         supply,
		playerID:       playerID,
		notifier:       notifier,
	}
	poller := &FactorySupplyPoller{
		marketRepo:        marketRepo,
		factoryTracker:    factoryTracker,
		factoryStateRepo:  factoryStateRepo,
		pipelineRepo:      pipelineRepo,
		taskQueue:         taskQueue,
		taskRepo:          taskRepo,
		sellMarketDistrib: sellMarketDistrib,
		replenisher:       replenisher,
		activator:         activator,
		supply:            supply,
		notifier:          notifier,
		pollInterval:      pollInterval,
		playerID:          playerID,
	}

	return &SupplyMonitor{
		poller:    poller,
		activator: activator,
	}
}

// ActivateSupplyGatedTasks checks all PENDING ACQUIRE_DELIVER tasks and activates
// those whose source market now has HIGH/ABUNDANT supply.
func (m *SupplyMonitor) ActivateSupplyGatedTasks(ctx context.Context) int {
	return m.activator.ActivateSupplyGatedTasks(ctx)
}

// ActivateCollectionPipelineTasks activates PENDING and enqueues READY COLLECT_SELL tasks from COLLECTION pipelines.
func (m *SupplyMonitor) ActivateCollectionPipelineTasks(ctx context.Context) int {
	return m.activator.ActivateCollectionPipelineTasks(ctx)
}

// ActivateConstructionTasks activates PENDING DELIVER_TO_CONSTRUCTION tasks whose dependencies completed.
func (m *SupplyMonitor) ActivateConstructionTasks(ctx context.Context) int {
	return m.activator.ActivateConstructionTasks(ctx)
}

// DeactivateSaturatedAcquireDeliverTasks resets READY ACQUIRE_DELIVER tasks to PENDING
// when the factory's input supply has become HIGH/ABUNDANT since the task was marked ready.
func (m *SupplyMonitor) DeactivateSaturatedAcquireDeliverTasks(ctx context.Context) int {
	return m.activator.DeactivateSaturatedAcquireDeliverTasks(ctx)
}

// Run starts the supply monitor background loop
func (m *SupplyMonitor) Run(ctx context.Context) {
	m.poller.Run(ctx)
}

// PollOnce performs a single poll of factories (for testing/manual triggering)
func (m *SupplyMonitor) PollOnce(ctx context.Context) {
	m.poller.PollOnce(ctx)
}

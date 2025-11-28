package manufacturing

import (
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// CoordinatorDependencies groups all external dependencies for manufacturing.
type CoordinatorDependencies struct {
	// Repositories
	PipelineRepo       manufacturing.PipelineRepository
	TaskRepo           manufacturing.TaskRepository
	FactoryStateRepo   manufacturing.FactoryStateRepository
	ShipRepo           navigation.ShipRepository
	MarketRepo         market.MarketRepository
	ShipAssignmentRepo container.ShipAssignmentRepository

	// External services
	WaypointProvider system.IWaypointProvider

	// Finders (from services package)
	DemandFinder                *services.ManufacturingDemandFinder
	CollectionOpportunityFinder *services.CollectionOpportunityFinder
	PipelinePlanner             *services.PipelinePlanner

	// Shared state
	TaskQueue      *services.TaskQueue
	FactoryTracker *manufacturing.FactoryStateTracker

	// Domain services
	Clock shared.Clock
}

// ManufacturingCoordinator is the composition root for manufacturing services.
// It constructs all services with proper dependency injection and eliminates circular deps.
type ManufacturingCoordinator struct {
	// Shared state (no circular deps)
	registry           *ActivePipelineRegistry
	assignmentTracker  *AssignmentTracker
	taskQueue          *services.TaskQueue

	// Domain services
	priorityCalculator *manufacturing.TaskPriorityCalculator
	reservationPolicy  *manufacturing.WorkerReservationPolicy
	readinessSpec      *manufacturing.TaskReadinessSpecification
	liquidationPolicy  *manufacturing.LiquidationPolicy

	// Focused application services
	shipSelector       *ShipSelector
	conditionChecker   *MarketConditionChecker
	reconciler         *AssignmentReconciler
	completionChecker  *PipelineCompletionChecker
	pipelineRecycler   *PipelineRecycler
	taskRescuer        *TaskRescuer

	// Original managers (kept for backward compatibility during transition)
	pipelineManager    *PipelineLifecycleManager
	taskAssignment     *TaskAssignmentManager
}

// NewManufacturingCoordinator constructs and wires all services.
// Order matters: build leaf services first, then composites.
func NewManufacturingCoordinator(deps CoordinatorDependencies) *ManufacturingCoordinator {
	c := &ManufacturingCoordinator{}

	// 1. Shared state (no deps)
	c.registry = NewActivePipelineRegistry()
	c.assignmentTracker = NewAssignmentTracker()
	c.taskQueue = deps.TaskQueue
	if c.taskQueue == nil {
		c.taskQueue = services.NewTaskQueue()
	}

	// 2. Domain services
	clock := deps.Clock
	if clock == nil {
		clock = shared.NewRealClock()
	}
	c.priorityCalculator = manufacturing.NewTaskPriorityCalculator(clock)
	c.reservationPolicy = manufacturing.NewWorkerReservationPolicy()
	c.readinessSpec = manufacturing.NewTaskReadinessSpecification()
	c.liquidationPolicy = manufacturing.NewLiquidationPolicy()

	// 3. Focused application services
	c.shipSelector = NewShipSelector(deps.WaypointProvider)
	c.conditionChecker = NewMarketConditionChecker(deps.MarketRepo, c.readinessSpec)
	c.reconciler = NewAssignmentReconciler(deps.TaskRepo, c.assignmentTracker)
	c.completionChecker = NewPipelineCompletionChecker(
		deps.PipelineRepo,
		deps.TaskRepo,
		c.registry,
	)
	c.pipelineRecycler = NewPipelineRecycler(
		deps.PipelineRepo,
		deps.TaskRepo,
		deps.ShipAssignmentRepo,
		c.taskQueue,
		deps.FactoryTracker,
		c.registry,
	)
	c.taskRescuer = NewTaskRescuer(
		deps.TaskRepo,
		c.taskQueue,
		c.conditionChecker,
	)

	return c
}

// GetRegistry returns the active pipeline registry.
func (c *ManufacturingCoordinator) GetRegistry() *ActivePipelineRegistry {
	return c.registry
}

// GetAssignmentTracker returns the assignment tracker.
func (c *ManufacturingCoordinator) GetAssignmentTracker() *AssignmentTracker {
	return c.assignmentTracker
}

// GetTaskQueue returns the task queue.
func (c *ManufacturingCoordinator) GetTaskQueue() *services.TaskQueue {
	return c.taskQueue
}

// GetPriorityCalculator returns the priority calculator.
func (c *ManufacturingCoordinator) GetPriorityCalculator() *manufacturing.TaskPriorityCalculator {
	return c.priorityCalculator
}

// GetReservationPolicy returns the reservation policy.
func (c *ManufacturingCoordinator) GetReservationPolicy() *manufacturing.WorkerReservationPolicy {
	return c.reservationPolicy
}

// GetReadinessSpec returns the readiness specification.
func (c *ManufacturingCoordinator) GetReadinessSpec() *manufacturing.TaskReadinessSpecification {
	return c.readinessSpec
}

// GetLiquidationPolicy returns the liquidation policy.
func (c *ManufacturingCoordinator) GetLiquidationPolicy() *manufacturing.LiquidationPolicy {
	return c.liquidationPolicy
}

// GetShipSelector returns the ship selector.
func (c *ManufacturingCoordinator) GetShipSelector() *ShipSelector {
	return c.shipSelector
}

// GetConditionChecker returns the market condition checker.
func (c *ManufacturingCoordinator) GetConditionChecker() *MarketConditionChecker {
	return c.conditionChecker
}

// GetReconciler returns the assignment reconciler.
func (c *ManufacturingCoordinator) GetReconciler() *AssignmentReconciler {
	return c.reconciler
}

// GetCompletionChecker returns the pipeline completion checker.
func (c *ManufacturingCoordinator) GetCompletionChecker() *PipelineCompletionChecker {
	return c.completionChecker
}

// GetPipelineRecycler returns the pipeline recycler.
func (c *ManufacturingCoordinator) GetPipelineRecycler() *PipelineRecycler {
	return c.pipelineRecycler
}

// GetTaskRescuer returns the task rescuer.
func (c *ManufacturingCoordinator) GetTaskRescuer() *TaskRescuer {
	return c.taskRescuer
}

// GetActivePipelineGetter returns a read-only getter for active pipelines.
// This can be injected into other services without creating circular dependencies.
func (c *ManufacturingCoordinator) GetActivePipelineGetter() func() map[string]*manufacturing.ManufacturingPipeline {
	return c.registry.GetGetter()
}

// SetPipelineManager sets the pipeline manager (for backward compatibility).
func (c *ManufacturingCoordinator) SetPipelineManager(pm *PipelineLifecycleManager) {
	c.pipelineManager = pm
	// Sync registry with manager's active pipelines
	if pm != nil {
		c.registry.SetAll(pm.GetActivePipelines())
	}
}

// SetTaskAssignmentManager sets the task assignment manager (for backward compatibility).
func (c *ManufacturingCoordinator) SetTaskAssignmentManager(tam *TaskAssignmentManager) {
	c.taskAssignment = tam
}

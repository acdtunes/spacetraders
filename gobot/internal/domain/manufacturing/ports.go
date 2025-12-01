package manufacturing

import "context"

// PipelineRepository handles persistence of manufacturing pipelines
type PipelineRepository interface {
	// Create persists a new pipeline
	Create(ctx context.Context, pipeline *ManufacturingPipeline) error

	// Update saves changes to an existing pipeline
	Update(ctx context.Context, pipeline *ManufacturingPipeline) error

	// FindByID retrieves a pipeline by its ID
	FindByID(ctx context.Context, id string) (*ManufacturingPipeline, error)

	// FindByPlayerID retrieves all pipelines for a player
	FindByPlayerID(ctx context.Context, playerID int) ([]*ManufacturingPipeline, error)

	// FindByStatus retrieves pipelines by status for a player
	FindByStatus(ctx context.Context, playerID int, statuses []PipelineStatus) ([]*ManufacturingPipeline, error)

	// FindActiveForProduct checks if there's an active pipeline for a product
	FindActiveForProduct(ctx context.Context, playerID int, productGood string) (*ManufacturingPipeline, error)

	// FindActiveCollectionForProduct checks if there's an active COLLECTION pipeline for a product
	// Used to prevent duplicate collection pipelines for the same good
	FindActiveCollectionForProduct(ctx context.Context, playerID int, productGood string) (*ManufacturingPipeline, error)

	// CountActiveFabricationPipelines counts only FABRICATION pipelines that are active
	// This is used for max_pipelines limiting
	CountActiveFabricationPipelines(ctx context.Context, playerID int) (int, error)

	// CountActiveCollectionPipelines counts only COLLECTION pipelines that are active
	// This is used for max_collection_pipelines limiting (0 = unlimited)
	CountActiveCollectionPipelines(ctx context.Context, playerID int) (int, error)

	// Delete removes a pipeline (cascades to tasks)
	Delete(ctx context.Context, id string) error
}

// TaskRepository handles persistence of manufacturing tasks
type TaskRepository interface {
	// Create persists a new task
	Create(ctx context.Context, task *ManufacturingTask) error

	// CreateBatch persists multiple tasks in a single transaction
	CreateBatch(ctx context.Context, tasks []*ManufacturingTask) error

	// Update saves changes to an existing task
	Update(ctx context.Context, task *ManufacturingTask) error

	// AssignTaskAtomically assigns a ship to a task atomically using SELECT FOR UPDATE
	// This prevents race conditions where multiple workers try to assign the same task
	AssignTaskAtomically(ctx context.Context, taskID string, shipSymbol string) error

	// FindByID retrieves a task by its ID
	FindByID(ctx context.Context, id string) (*ManufacturingTask, error)

	// FindByPipelineID retrieves all tasks for a pipeline
	FindByPipelineID(ctx context.Context, pipelineID string) ([]*ManufacturingTask, error)

	// FindByPipelineAndStatus retrieves tasks for a pipeline filtered by status
	FindByPipelineAndStatus(ctx context.Context, pipelineID string, status TaskStatus) ([]*ManufacturingTask, error)

	// FindByStatus retrieves tasks by status for a player
	FindByStatus(ctx context.Context, playerID int, status TaskStatus) ([]*ManufacturingTask, error)

	// FindReadyTasks retrieves all READY tasks for a player, sorted by priority
	FindReadyTasks(ctx context.Context, playerID int) ([]*ManufacturingTask, error)

	// FindByAssignedShip retrieves tasks assigned to a specific ship
	FindByAssignedShip(ctx context.Context, shipSymbol string) (*ManufacturingTask, error)

	// FindIncomplete retrieves all non-terminal tasks for a player
	FindIncomplete(ctx context.Context, playerID int) ([]*ManufacturingTask, error)

	// FindAvailableByGood retrieves PENDING or READY tasks for a specific good
	// Used by orphaned cargo handler to find tasks that can use existing cargo
	FindAvailableByGood(ctx context.Context, playerID int, good string) ([]*ManufacturingTask, error)

	// FindDependencies retrieves the task dependencies for a task
	FindDependencies(ctx context.Context, taskID string) ([]string, error)

	// AddDependency adds a dependency between tasks
	AddDependency(ctx context.Context, taskID string, dependsOnID string) error

	// Delete removes a task
	Delete(ctx context.Context, id string) error
}

// FactoryStateRepository handles persistence of factory states
type FactoryStateRepository interface {
	// Create persists a new factory state
	Create(ctx context.Context, state *FactoryState) error

	// Update saves changes to an existing factory state
	Update(ctx context.Context, state *FactoryState) error

	// FindByID retrieves a factory state by database ID
	FindByID(ctx context.Context, id int) (*FactoryState, error)

	// FindByFactory retrieves factory state for a specific factory/output/pipeline
	FindByFactory(ctx context.Context, pipelineID string, factorySymbol string, outputGood string) (*FactoryState, error)

	// FindByPipelineID retrieves all factory states for a pipeline
	FindByPipelineID(ctx context.Context, pipelineID string) ([]*FactoryState, error)

	// FindPending retrieves factory states awaiting production for a player
	FindPending(ctx context.Context, playerID int) ([]*FactoryState, error)

	// FindReadyForCollection retrieves factory states ready for collection
	FindReadyForCollection(ctx context.Context, playerID int) ([]*FactoryState, error)

	// Delete removes a factory state
	Delete(ctx context.Context, id int) error

	// DeleteByPipelineID removes all factory states for a pipeline
	DeleteByPipelineID(ctx context.Context, pipelineID string) error
}

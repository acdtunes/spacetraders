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

	// FindByConstructionSite retrieves the pipeline for a specific construction site (for idempotency).
	// Returns nil, nil if no active pipeline exists for this site.
	// Only returns non-terminal pipelines (excludes COMPLETED, FAILED).
	FindByConstructionSite(ctx context.Context, constructionSiteSymbol string, playerID int) (*ManufacturingPipeline, error)

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
	// Only returns tasks from EXECUTING pipelines (excludes FAILED/CANCELLED/COMPLETED)
	FindAvailableByGood(ctx context.Context, playerID int, good string) ([]*ManufacturingTask, error)

	// FindReadyWithActivePipeline retrieves READY tasks from EXECUTING pipelines only
	// Used by TaskRescuer to avoid rescuing tasks from FAILED pipelines
	FindReadyWithActivePipeline(ctx context.Context, playerID int) ([]*ManufacturingTask, error)

	// FindDependencies retrieves the task dependencies for a task
	FindDependencies(ctx context.Context, taskID string) ([]string, error)

	// AddDependency adds a dependency between tasks
	AddDependency(ctx context.Context, taskID string, dependsOnID string) error

	// Delete removes a task
	Delete(ctx context.Context, id string) error

	// ExistsLiquidateForShipAndGood checks if a LIQUIDATE task already exists for a ship+good
	// Returns true if there's an incomplete LIQUIDATE task for this ship/good combination
	ExistsLiquidateForShipAndGood(ctx context.Context, shipSymbol string, good string, playerID int) (bool, error)
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

// ConstructionSiteRepository provides access to construction site data.
// Construction sites are fetched from the SpaceTraders API.
type ConstructionSiteRepository interface {
	// FindByWaypoint retrieves construction site information from API.
	// Returns the current state of the construction site including material requirements.
	FindByWaypoint(ctx context.Context, waypointSymbol string, playerID int) (*ConstructionSite, error)

	// SupplyMaterial delivers materials to construction site.
	// Uses POST /my/ships/{shipSymbol}/construction/supply API endpoint.
	SupplyMaterial(ctx context.Context, shipSymbol, waypointSymbol, tradeSymbol string, units int, playerID int) (*ConstructionSupplyResult, error)
}

// ConstructionSupplyResult contains the result of a construction supply operation.
type ConstructionSupplyResult struct {
	// Construction is the updated construction site state after supply
	Construction *ConstructionSite
	// UnitsDelivered is the number of units delivered
	UnitsDelivered int
	// CargoCapacity is the ship's cargo capacity after supply
	CargoCapacity int
	// CargoUnits is the ship's cargo units after supply
	CargoUnits int
}

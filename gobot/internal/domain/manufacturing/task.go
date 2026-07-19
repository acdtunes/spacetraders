package manufacturing

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TaskType represents the type of manufacturing task
type TaskType string

const (
	// TaskTypeAcquireDeliver - Atomic: Buy from export market AND deliver to factory
	// Same ship buys goods and delivers them, preventing "orphaned cargo" bugs
	TaskTypeAcquireDeliver TaskType = "ACQUIRE_DELIVER"

	// TaskTypeCollectSell - Atomic: Collect from factory AND sell at demand market
	// Same ship collects goods and sells them, preventing "orphaned cargo" bugs
	TaskTypeCollectSell TaskType = "COLLECT_SELL"

	// TaskTypeLiquidate - Sell orphaned cargo to recover investment
	TaskTypeLiquidate TaskType = "LIQUIDATE"

	// TaskTypeStorageAcquireDeliver - Acquire cargo from storage ships AND deliver to destination
	// Hauler navigates to storage operation waypoint, waits for cargo, transfers, then delivers.
	// Used for gas extraction, mining, and other buffered resource operations.
	TaskTypeStorageAcquireDeliver TaskType = "STORAGE_ACQUIRE_DELIVER"

	// TaskTypeDeliverToConstruction - Atomic: Acquire goods AND deliver to construction site
	// Same ship buys/collects goods and supplies them to construction. Uses SupplyConstruction API.
	TaskTypeDeliverToConstruction TaskType = "DELIVER_TO_CONSTRUCTION"
)

// TaskStatus represents the current status of a task
type TaskStatus string

const (
	// TaskStatusPending - Waiting for dependencies
	TaskStatusPending TaskStatus = "PENDING"

	// TaskStatusReady - All dependencies met, can execute
	TaskStatusReady TaskStatus = "READY"

	// TaskStatusAssigned - Assigned to a ship
	TaskStatusAssigned TaskStatus = "ASSIGNED"

	// TaskStatusExecuting - Ship is executing
	TaskStatusExecuting TaskStatus = "EXECUTING"

	// TaskStatusCompleted - Successfully completed
	TaskStatusCompleted TaskStatus = "COMPLETED"

	// TaskStatusFailed - Failed (may retry)
	TaskStatusFailed TaskStatus = "FAILED"

	// TaskStatusCancelled - Cancelled (pipeline recycled)
	TaskStatusCancelled TaskStatus = "CANCELLED"
)

// Task priority constants
// Higher priority tasks are processed first
// COLLECT_SELL has higher priority to ensure revenue generation
// Aging mechanism (+2/min) naturally prioritizes starved tasks over time
const (
	// PriorityAcquireDeliver - Deliver inputs to factories so they can produce
	PriorityAcquireDeliver = 10

	// PriorityCollectSell - Higher priority than ACQUIRE_DELIVER
	// Revenue generation must not be blocked by input deliveries
	// This ensures factory outputs are collected and sold promptly
	PriorityCollectSell = 50

	// PriorityLiquidate - High priority to recover investment from orphaned cargo
	PriorityLiquidate = 100

	// PriorityStorageAcquireDeliver - Same as ACQUIRE_DELIVER for equal treatment
	// Storage tasks compete fairly with market acquisition tasks
	PriorityStorageAcquireDeliver = 10

	// PriorityDeliverToConstruction - Higher priority for construction deliveries
	// Construction projects have deadlines and compete with other players
	PriorityDeliverToConstruction = 75
)

// Priority tuning constants for preventing task starvation
// These constants control the aging algorithm and task type reservations
const (
	// MaxAgingBonus caps the maximum priority bonus from aging
	// This prevents runaway priority accumulation from very old tasks
	// After 50 minutes, both task types reach max bonus regardless of age
	MaxAgingBonus = 100

	// AgingRatePerMinute controls how fast aging priority increases
	// +2 per minute means a 50-minute wait gives max bonus (100)
	AgingRatePerMinute = 2

	// MinCollectSellWorkers reserves minimum workers for COLLECT_SELL tasks
	// This prevents complete starvation when ACQUIRE_DELIVER has aging advantage
	MinCollectSellWorkers = 3

	// MinAcquireDeliverWorkers reserves minimum workers for ACQUIRE_DELIVER tasks
	// This ensures factories continue receiving inputs
	MinAcquireDeliverWorkers = 3
)

// Supply-based priority boosts for ACQUIRE_DELIVER tasks
// Higher supply = better prices = higher priority
// This ensures we buy from cheapest markets first
const (
	// SupplyPriorityAbundant - Highest priority, best prices
	SupplyPriorityAbundant = 30

	// SupplyPriorityHigh - Good prices
	SupplyPriorityHigh = 20

	// SupplyPriorityModerate - Acceptable prices, lowest priority among allowed
	SupplyPriorityModerate = 0
)

const (
	// DefaultMaxRetries is the default number of retry attempts for failed tasks
	DefaultMaxRetries = 3
)

func nowPtr() *time.Time {
	now := time.Now()
	return &now
}

// ManufacturingTask represents a single atomic task in the manufacturing pipeline.
// Tasks are the unit of work assignment - each can be independently executed by a ship.
//
// Task Types (atomic - same ship does both phases):
//   - ACQUIRE_DELIVER: Buy from export market AND deliver to factory
//   - COLLECT_SELL: Collect from factory (when supply HIGH) AND sell at demand market
//   - LIQUIDATE: Sell orphaned cargo to recover investment
//   - STORAGE_ACQUIRE_DELIVER: Wait for cargo from storage ships AND deliver to factory
//
// State Machine:
//
//	PENDING -> READY -> ASSIGNED -> EXECUTING -> COMPLETED
//	                                        \-> FAILED -> PENDING (retry)
type ManufacturingTask struct {
	id       string
	taskType TaskType
	status   TaskStatus

	// What to acquire/deliver/collect/sell
	good     string
	quantity int // Desired quantity (0 = fill cargo)

	// Where
	sourceMarket       string // For ACQUIRE: export market to buy from
	targetMarket       string // For DELIVER/SELL: destination market
	factorySymbol      string // For COLLECT: factory to collect from
	storageOperationID string // For STORAGE_ACQUIRE_DELIVER: storage operation to acquire from
	storageWaypoint    string // For STORAGE_ACQUIRE_DELIVER: waypoint where storage ships are located
	constructionSite   string // For DELIVER_TO_CONSTRUCTION: construction site waypoint symbol

	// Dependencies
	dependsOn  []string // Task IDs that must complete first
	pipelineID string   // Parent pipeline this task belongs to
	playerID   int      // Player who owns this task

	// Execution
	assignedShip string // Ship symbol executing this task
	priority     int    // Higher = more urgent

	// Retry tracking
	retryCount int
	maxRetries int

	// Timing
	createdAt   time.Time
	readyAt     *time.Time
	startedAt   *time.Time
	completedAt *time.Time

	// Results
	actualQuantity int // Actual quantity acquired/delivered
	totalCost      int // Cost incurred
	totalRevenue   int // Revenue earned (for SELL)
	errorMessage   string

	// Tracks which phase has completed so recovery can skip completed work.
	collectPhaseCompleted bool       // COLLECT_SELL: did we collect from factory?
	acquirePhaseCompleted bool       // ACQUIRE_DELIVER: did we buy from market?
	phaseCompletedAt      *time.Time // When phase completed
}

// NewManufacturingTask creates a new manufacturing task
func NewManufacturingTask(
	taskType TaskType,
	good string,
	pipelineID string,
	playerID int,
) *ManufacturingTask {
	return &ManufacturingTask{
		id:         uuid.New().String(),
		taskType:   taskType,
		status:     TaskStatusPending,
		good:       good,
		pipelineID: pipelineID,
		playerID:   playerID,
		dependsOn:  make([]string, 0),
		priority:   0,
		retryCount: 0,
		maxRetries: DefaultMaxRetries,
		createdAt:  time.Now(),
	}
}

// NewLiquidationTask creates a task to sell orphaned cargo
func NewLiquidationTask(playerID int, shipSymbol string, good string, quantity int, targetMarket string) *ManufacturingTask {
	task := NewManufacturingTask(TaskTypeLiquidate, good, "", playerID)
	task.targetMarket = targetMarket
	task.quantity = quantity
	task.assignedShip = shipSymbol
	task.status = TaskStatusReady // Liquidation tasks are immediately ready
	task.priority = PriorityLiquidate
	task.readyAt = nowPtr()
	return task
}

// NewAcquireDeliverTask creates an atomic task to buy from export market AND deliver to factory.
// This replaces the separate ACQUIRE + DELIVER pattern to ensure the same ship does both operations.
func NewAcquireDeliverTask(pipelineID string, playerID int, good string, sourceMarket string, factorySymbol string, dependsOn []string) *ManufacturingTask {
	task := NewManufacturingTask(TaskTypeAcquireDeliver, good, pipelineID, playerID)
	task.sourceMarket = sourceMarket   // Where to buy from
	task.factorySymbol = factorySymbol // Where to deliver to
	task.dependsOn = dependsOn
	task.priority = PriorityAcquireDeliver // Higher priority - feeds factories
	return task
}

// NewCollectSellTask creates an atomic task to collect from factory AND sell at demand market.
// This replaces the separate COLLECT + SELL pattern to ensure the same ship does both operations.
func NewCollectSellTask(pipelineID string, playerID int, good string, factorySymbol string, targetMarket string, dependsOn []string) *ManufacturingTask {
	task := NewManufacturingTask(TaskTypeCollectSell, good, pipelineID, playerID)
	task.factorySymbol = factorySymbol // Where to collect from
	task.targetMarket = targetMarket   // Where to sell
	task.dependsOn = dependsOn
	task.priority = PriorityCollectSell // Lower priority, aging prevents starvation
	return task
}

// NewStorageAcquireDeliverTask creates a task to acquire cargo from storage ships AND deliver to destination.
// The hauler navigates to the storage operation waypoint, waits for cargo from storage ships,
// transfers the cargo, then navigates to and delivers to the factory/destination.
//
// Parameters:
//   - pipelineID: Parent pipeline (can be empty for standalone tasks)
//   - playerID: Player who owns this task
//   - good: The cargo type to acquire
//   - storageOperationID: ID of the storage operation to acquire from
//   - storageWaypoint: Waypoint where storage ships are located
//   - factorySymbol: Destination factory to deliver to
//   - dependsOn: Task IDs that must complete first (usually empty)
func NewStorageAcquireDeliverTask(
	pipelineID string,
	playerID int,
	good string,
	storageOperationID string,
	storageWaypoint string,
	factorySymbol string,
	dependsOn []string,
) *ManufacturingTask {
	task := NewManufacturingTask(TaskTypeStorageAcquireDeliver, good, pipelineID, playerID)
	task.storageOperationID = storageOperationID
	task.storageWaypoint = storageWaypoint
	task.factorySymbol = factorySymbol // Where to deliver to
	task.dependsOn = dependsOn
	task.priority = PriorityStorageAcquireDeliver
	return task
}

// NewDeliverToConstructionTask creates a task to acquire goods AND deliver to a construction site.
// This is similar to ACQUIRE_DELIVER but uses SupplyConstruction API instead of selling to market.
//
// Parameters:
//   - pipelineID: Parent pipeline (CONSTRUCTION type)
//   - playerID: Player who owns this task
//   - good: The cargo type to acquire and deliver
//   - sourceMarket: Market to purchase goods from (empty if collecting from factory)
//   - factorySymbol: Factory to collect from (empty if purchasing from market)
//   - constructionSite: Waypoint of construction site to deliver to
//   - dependsOn: Task IDs that must complete first
func NewDeliverToConstructionTask(
	pipelineID string,
	playerID int,
	good string,
	sourceMarket string,
	factorySymbol string,
	constructionSite string,
	dependsOn []string,
) *ManufacturingTask {
	task := NewManufacturingTask(TaskTypeDeliverToConstruction, good, pipelineID, playerID)
	task.sourceMarket = sourceMarket         // Where to buy from (if market-based)
	task.factorySymbol = factorySymbol       // Where to collect from (if factory-based)
	task.constructionSite = constructionSite // Where to deliver to
	task.dependsOn = dependsOn
	task.priority = PriorityDeliverToConstruction
	return task
}

// Getters

func (t *ManufacturingTask) ID() string                 { return t.id }
func (t *ManufacturingTask) TaskType() TaskType         { return t.taskType }
func (t *ManufacturingTask) Status() TaskStatus         { return t.status }
func (t *ManufacturingTask) Good() string               { return t.good }
func (t *ManufacturingTask) Quantity() int              { return t.quantity }
func (t *ManufacturingTask) SourceMarket() string       { return t.sourceMarket }
func (t *ManufacturingTask) TargetMarket() string       { return t.targetMarket }
func (t *ManufacturingTask) FactorySymbol() string      { return t.factorySymbol }
func (t *ManufacturingTask) StorageOperationID() string { return t.storageOperationID }
func (t *ManufacturingTask) StorageWaypoint() string    { return t.storageWaypoint }
func (t *ManufacturingTask) ConstructionSite() string   { return t.constructionSite }
func (t *ManufacturingTask) DependsOn() []string        { return t.dependsOn }
func (t *ManufacturingTask) PipelineID() string         { return t.pipelineID }
func (t *ManufacturingTask) PlayerID() int              { return t.playerID }
func (t *ManufacturingTask) AssignedShip() string       { return t.assignedShip }
func (t *ManufacturingTask) Priority() int              { return t.priority }
func (t *ManufacturingTask) RetryCount() int            { return t.retryCount }
func (t *ManufacturingTask) MaxRetries() int            { return t.maxRetries }
func (t *ManufacturingTask) CreatedAt() time.Time       { return t.createdAt }
func (t *ManufacturingTask) ReadyAt() *time.Time        { return t.readyAt }
func (t *ManufacturingTask) StartedAt() *time.Time      { return t.startedAt }
func (t *ManufacturingTask) CompletedAt() *time.Time    { return t.completedAt }
func (t *ManufacturingTask) ActualQuantity() int        { return t.actualQuantity }
func (t *ManufacturingTask) TotalCost() int             { return t.totalCost }
func (t *ManufacturingTask) TotalRevenue() int          { return t.totalRevenue }
func (t *ManufacturingTask) ErrorMessage() string       { return t.errorMessage }

// Phase tracking getters
func (t *ManufacturingTask) CollectPhaseCompleted() bool  { return t.collectPhaseCompleted }
func (t *ManufacturingTask) AcquirePhaseCompleted() bool  { return t.acquirePhaseCompleted }
func (t *ManufacturingTask) PhaseCompletedAt() *time.Time { return t.phaseCompletedAt }

// SetStorageOperationID sets the storage operation ID for storage-based collection.
// Used by COLLECT_SELL tasks that collect from storage ships instead of factories.
func (t *ManufacturingTask) SetStorageOperationID(id string) {
	t.storageOperationID = id
}

// SetStorageWaypoint sets the storage waypoint for storage-based collection.
// Used by COLLECT_SELL tasks that collect from storage ships instead of factories.
func (t *ManufacturingTask) SetStorageWaypoint(waypoint string) {
	t.storageWaypoint = waypoint
}

// IsStorageBasedCollection returns true if this task collects from storage ships.
// When true, the executor should use storage ship transfer instead of market purchase.
func (t *ManufacturingTask) IsStorageBasedCollection() bool {
	return t.storageOperationID != "" && t.storageWaypoint != ""
}

// Setters for execution results

func (t *ManufacturingTask) SetActualQuantity(qty int)   { t.actualQuantity = qty }
func (t *ManufacturingTask) SetTotalCost(cost int)       { t.totalCost = cost }
func (t *ManufacturingTask) SetTotalRevenue(revenue int) { t.totalRevenue = revenue }
func (t *ManufacturingTask) SetPriority(priority int)    { t.priority = priority }
func (t *ManufacturingTask) SetQuantity(qty int)         { t.quantity = qty }

// IsDeferredConstruction reports whether this is a construction delivery whose
// buy source could not be located at planning time (no export cleared the
// MODERATE+ floor and no import held accumulated stock). Deferred tasks carry
// neither a source market nor a factory, stay PENDING, and are re-sourced by the
// supply monitor when market supply regenerates - mirroring how COLLECT_SELL and
// supply-gated ACQUIRE_DELIVER tasks recover. They keep the deferred material
// visible in pipeline/task state instead of dropping it.
func (t *ManufacturingTask) IsDeferredConstruction() bool {
	return t.taskType == TaskTypeDeliverToConstruction && t.sourceMarket == "" && t.factorySymbol == ""
}

// AddDependency adds a task ID to this task's dependencies
func (t *ManufacturingTask) AddDependency(taskID string) {
	t.dependsOn = append(t.dependsOn, taskID)
}

// HasDependencies returns true if this task has unmet dependencies
func (t *ManufacturingTask) HasDependencies() bool {
	return len(t.dependsOn) > 0
}

// CanRetry returns true if the task can be retried
func (t *ManufacturingTask) CanRetry() bool {
	return t.status == TaskStatusFailed && t.retryCount < t.maxRetries
}

// IsTerminal returns true if the task is in a terminal state (COMPLETED, CANCELLED, or FAILED with no retries)
func (t *ManufacturingTask) IsTerminal() bool {
	if t.status == TaskStatusCompleted || t.status == TaskStatusCancelled {
		return true
	}
	if t.status == TaskStatusFailed && !t.CanRetry() {
		return true
	}
	return false
}

// NetProfit returns the net profit for this task (revenue - cost)
func (t *ManufacturingTask) NetProfit() int {
	return t.totalRevenue - t.totalCost
}

// GetFirstDestination returns the initial destination waypoint for the task.
// For ACQUIRE_DELIVER: source market, For COLLECT_SELL: factory, For LIQUIDATE: target market.
func (t *ManufacturingTask) GetFirstDestination() string {
	switch t.taskType {
	case TaskTypeAcquireDeliver:
		return t.sourceMarket
	case TaskTypeCollectSell:
		return t.factorySymbol
	case TaskTypeLiquidate:
		return t.targetMarket
	case TaskTypeStorageAcquireDeliver:
		return t.storageWaypoint
	case TaskTypeDeliverToConstruction:
		if t.sourceMarket != "" {
			return t.sourceMarket
		}
		return t.factorySymbol
	default:
		return ""
	}
}

// GetFinalDestination returns where the task delivers goods.
// For ACQUIRE_DELIVER: factory (deliver inputs), For COLLECT_SELL/LIQUIDATE: target market (sell outputs).
// For STORAGE_ACQUIRE_DELIVER: factory (deliver collected cargo).
// For DELIVER_TO_CONSTRUCTION: construction site (supply materials).
func (t *ManufacturingTask) GetFinalDestination() string {
	switch t.taskType {
	case TaskTypeAcquireDeliver, TaskTypeStorageAcquireDeliver:
		return t.factorySymbol // Deliver to factory
	case TaskTypeCollectSell, TaskTypeLiquidate:
		return t.targetMarket // Sell at market
	case TaskTypeDeliverToConstruction:
		return t.constructionSite // Deliver to construction site
	default:
		return t.targetMarket
	}
}

// String provides human-readable representation
func (t *ManufacturingTask) String() string {
	return fmt.Sprintf("Task[%s, type=%s, good=%s, status=%s, priority=%d]",
		t.id[:8], t.taskType, t.good, t.status, t.priority)
}

// ReconstituteTask creates a task from persisted data (for repository use only)
func ReconstituteTask(
	id string,
	pipelineID string,
	playerID int,
	taskType TaskType,
	status TaskStatus,
	good string,
	quantity int,
	actualQuantity int,
	sourceMarket string,
	targetMarket string,
	factorySymbol string,
	storageOperationID string,
	storageWaypoint string,
	constructionSite string,
	dependsOn []string,
	assignedShip string,
	priority int,
	retryCount int,
	maxRetries int,
	totalCost int,
	totalRevenue int,
	errorMessage string,
	createdAt time.Time,
	readyAt *time.Time,
	startedAt *time.Time,
	completedAt *time.Time,
	// Phase tracking fields
	collectPhaseCompleted bool,
	acquirePhaseCompleted bool,
	phaseCompletedAt *time.Time,
) *ManufacturingTask {
	return &ManufacturingTask{
		id:                 id,
		pipelineID:         pipelineID,
		playerID:           playerID,
		taskType:           taskType,
		status:             status,
		good:               good,
		quantity:           quantity,
		actualQuantity:     actualQuantity,
		sourceMarket:       sourceMarket,
		targetMarket:       targetMarket,
		factorySymbol:      factorySymbol,
		storageOperationID: storageOperationID,
		storageWaypoint:    storageWaypoint,
		constructionSite:   constructionSite,
		dependsOn:          dependsOn,
		assignedShip:       assignedShip,
		priority:           priority,
		retryCount:         retryCount,
		maxRetries:         maxRetries,
		totalCost:          totalCost,
		totalRevenue:       totalRevenue,
		errorMessage:       errorMessage,
		createdAt:          createdAt,
		readyAt:            readyAt,
		startedAt:          startedAt,
		completedAt:        completedAt,
		// Phase tracking fields
		collectPhaseCompleted: collectPhaseCompleted,
		acquirePhaseCompleted: acquirePhaseCompleted,
		phaseCompletedAt:      phaseCompletedAt,
	}
}

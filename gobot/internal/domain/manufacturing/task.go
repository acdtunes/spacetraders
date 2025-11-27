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
// EQUAL priority for ACQUIRE_DELIVER and COLLECT_SELL ensures balanced throughput
// Aging mechanism (+2/min) naturally prioritizes starved tasks over time
const (
	// PriorityAcquireDeliver - Deliver inputs to factories so they can produce
	PriorityAcquireDeliver = 10

	// PriorityCollectSell - Equal to ACQUIRE_DELIVER for balanced throughput
	// Both input deliveries and output collection are equally important
	// Aging ensures older tasks get priority regardless of type
	PriorityCollectSell = 10

	// PriorityLiquidate - High priority to recover investment from orphaned cargo
	PriorityLiquidate = 100
)

const (
	// DefaultMaxRetries is the default number of retry attempts for failed tasks
	DefaultMaxRetries = 3
)

// ManufacturingTask represents a single atomic task in the manufacturing pipeline.
// Tasks are the unit of work assignment - each can be independently executed by a ship.
//
// Task Types (atomic - same ship does both phases):
//   - ACQUIRE_DELIVER: Buy from export market AND deliver to factory
//   - COLLECT_SELL: Collect from factory (when supply HIGH) AND sell at demand market
//   - LIQUIDATE: Sell orphaned cargo to recover investment
//
// State Machine:
//   PENDING -> READY -> ASSIGNED -> EXECUTING -> COMPLETED
//                                           \-> FAILED -> PENDING (retry)
type ManufacturingTask struct {
	id       string
	taskType TaskType
	status   TaskStatus

	// What to acquire/deliver/collect/sell
	good     string
	quantity int // Desired quantity (0 = fill cargo)

	// Where
	sourceMarket  string // For ACQUIRE: export market to buy from
	targetMarket  string // For DELIVER/SELL: destination market
	factorySymbol string // For COLLECT: factory to collect from

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
	task.priority = 100           // High priority - recover investment ASAP
	now := time.Now()
	task.readyAt = &now
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

// ReconstructTask rebuilds a task from persistence
func ReconstructTask(
	id string,
	taskType TaskType,
	status TaskStatus,
	good string,
	quantity int,
	sourceMarket string,
	targetMarket string,
	factorySymbol string,
	dependsOn []string,
	pipelineID string,
	playerID int,
	assignedShip string,
	priority int,
	retryCount int,
	maxRetries int,
	createdAt time.Time,
	readyAt *time.Time,
	startedAt *time.Time,
	completedAt *time.Time,
	actualQuantity int,
	totalCost int,
	totalRevenue int,
	errorMessage string,
) *ManufacturingTask {
	return &ManufacturingTask{
		id:             id,
		taskType:       taskType,
		status:         status,
		good:           good,
		quantity:       quantity,
		sourceMarket:   sourceMarket,
		targetMarket:   targetMarket,
		factorySymbol:  factorySymbol,
		dependsOn:      dependsOn,
		pipelineID:     pipelineID,
		playerID:       playerID,
		assignedShip:   assignedShip,
		priority:       priority,
		retryCount:     retryCount,
		maxRetries:     maxRetries,
		createdAt:      createdAt,
		readyAt:        readyAt,
		startedAt:      startedAt,
		completedAt:    completedAt,
		actualQuantity: actualQuantity,
		totalCost:      totalCost,
		totalRevenue:   totalRevenue,
		errorMessage:   errorMessage,
	}
}

// Getters

func (t *ManufacturingTask) ID() string              { return t.id }
func (t *ManufacturingTask) TaskType() TaskType      { return t.taskType }
func (t *ManufacturingTask) Status() TaskStatus      { return t.status }
func (t *ManufacturingTask) Good() string            { return t.good }
func (t *ManufacturingTask) Quantity() int           { return t.quantity }
func (t *ManufacturingTask) SourceMarket() string    { return t.sourceMarket }
func (t *ManufacturingTask) TargetMarket() string    { return t.targetMarket }
func (t *ManufacturingTask) FactorySymbol() string   { return t.factorySymbol }
func (t *ManufacturingTask) DependsOn() []string     { return t.dependsOn }
func (t *ManufacturingTask) PipelineID() string      { return t.pipelineID }
func (t *ManufacturingTask) PlayerID() int           { return t.playerID }
func (t *ManufacturingTask) AssignedShip() string    { return t.assignedShip }
func (t *ManufacturingTask) Priority() int           { return t.priority }
func (t *ManufacturingTask) RetryCount() int         { return t.retryCount }
func (t *ManufacturingTask) MaxRetries() int         { return t.maxRetries }
func (t *ManufacturingTask) CreatedAt() time.Time    { return t.createdAt }
func (t *ManufacturingTask) ReadyAt() *time.Time     { return t.readyAt }
func (t *ManufacturingTask) StartedAt() *time.Time   { return t.startedAt }
func (t *ManufacturingTask) CompletedAt() *time.Time { return t.completedAt }
func (t *ManufacturingTask) ActualQuantity() int     { return t.actualQuantity }
func (t *ManufacturingTask) TotalCost() int          { return t.totalCost }
func (t *ManufacturingTask) TotalRevenue() int       { return t.totalRevenue }
func (t *ManufacturingTask) ErrorMessage() string    { return t.errorMessage }

// State transitions

// MarkReady transitions task from PENDING to READY
func (t *ManufacturingTask) MarkReady() error {
	if t.status != TaskStatusPending {
		return &ErrInvalidTaskTransition{
			TaskID: t.id,
			From:   t.status,
			To:     TaskStatusReady,
		}
	}
	t.status = TaskStatusReady
	now := time.Now()
	t.readyAt = &now
	return nil
}

// AssignShip assigns a ship to execute this task
func (t *ManufacturingTask) AssignShip(shipSymbol string) error {
	if t.status != TaskStatusReady {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusAssigned,
			Description: "can only assign ship to READY tasks",
		}
	}
	if t.assignedShip != "" && t.assignedShip != shipSymbol {
		return &ErrTaskAlreadyAssigned{
			TaskID:       t.id,
			AssignedShip: t.assignedShip,
		}
	}
	t.status = TaskStatusAssigned
	t.assignedShip = shipSymbol
	return nil
}

// StartExecution marks the task as executing
func (t *ManufacturingTask) StartExecution() error {
	if t.status != TaskStatusAssigned {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusExecuting,
			Description: "can only start execution from ASSIGNED state",
		}
	}
	t.status = TaskStatusExecuting
	now := time.Now()
	t.startedAt = &now
	return nil
}

// Complete marks the task as successfully completed
// NOTE: assignedShip is preserved for ship affinity - downstream tasks (like SELL)
// need to know which ship executed upstream tasks (like COLLECT) that have the cargo
func (t *ManufacturingTask) Complete() error {
	if t.status != TaskStatusExecuting {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusCompleted,
			Description: "can only complete from EXECUTING state",
		}
	}
	t.status = TaskStatusCompleted
	now := time.Now()
	t.completedAt = &now
	// DO NOT clear assignedShip - downstream tasks need this for ship affinity
	// Ship assignment release is handled by the container runner, not the task state
	return nil
}

// Fail marks the task as failed
func (t *ManufacturingTask) Fail(errorMsg string) error {
	if t.status != TaskStatusExecuting && t.status != TaskStatusAssigned {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusFailed,
			Description: "can only fail from EXECUTING or ASSIGNED state",
		}
	}
	t.status = TaskStatusFailed
	t.errorMessage = errorMsg
	now := time.Now()
	t.completedAt = &now
	t.retryCount++
	t.assignedShip = "" // Release ship
	return nil
}

// ResetForRetry prepares the task for a retry attempt
func (t *ManufacturingTask) ResetForRetry() error {
	if t.status != TaskStatusFailed {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusPending,
			Description: "can only retry FAILED tasks",
		}
	}
	if t.retryCount >= t.maxRetries {
		return &ErrMaxRetriesExceeded{
			TaskID:     t.id,
			RetryCount: t.retryCount,
			MaxRetries: t.maxRetries,
		}
	}
	t.status = TaskStatusPending
	t.errorMessage = ""
	t.startedAt = nil
	t.completedAt = nil
	t.readyAt = nil // Reset so MarkReady() sets fresh timestamp for fair aging
	return nil
}

// RollbackAssignment returns task to READY state (used on assignment failure)
func (t *ManufacturingTask) RollbackAssignment() error {
	if t.status != TaskStatusAssigned {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusReady,
			Description: "can only rollback from ASSIGNED state",
		}
	}
	t.status = TaskStatusReady
	t.assignedShip = ""
	// Reset readyAt for fair aging - assignment failure shouldn't give priority bonus
	now := time.Now()
	t.readyAt = &now
	return nil
}

// RollbackExecution returns task to READY state (used on execution interruption, e.g., daemon restart)
// IMPORTANT: We preserve assignedShip because for SELL tasks, the ship still has cargo that needs to be sold.
// The ship assignment in the database will be released separately, but we need to remember which ship
// was executing so we can re-assign the same ship when the task is retried.
func (t *ManufacturingTask) RollbackExecution() error {
	if t.status != TaskStatusExecuting {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusReady,
			Description: "can only rollback from EXECUTING state",
		}
	}
	t.status = TaskStatusReady
	// NOTE: We intentionally DO NOT clear assignedShip here.
	// For SELL tasks, the ship has cargo from the COLLECT task and must complete the sell.
	// The coordinator will use this to re-assign the same ship.
	// t.assignedShip = "" // REMOVED - preserve ship for recovery
	t.startedAt = nil
	// Reset readyAt to prevent accumulating aging priority after recovery
	// Without this, recovered tasks would have artificially high priority
	now := time.Now()
	t.readyAt = &now
	return nil
}

// ResetToPending returns task from READY back to PENDING state.
// Used when market conditions change (e.g., sell market becomes saturated) and we want
// the SupplyMonitor to re-evaluate the task later when conditions improve.
func (t *ManufacturingTask) ResetToPending() error {
	if t.status != TaskStatusReady {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusPending,
			Description: "can only reset to pending from READY state",
		}
	}
	t.status = TaskStatusPending
	t.readyAt = nil
	return nil
}

// Cancel marks the task as cancelled (used when pipeline is recycled).
// Can only cancel PENDING or READY tasks - tasks that are executing should complete or fail.
func (t *ManufacturingTask) Cancel(reason string) error {
	if t.status != TaskStatusPending && t.status != TaskStatusReady {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusCancelled,
			Description: "can only cancel PENDING or READY tasks",
		}
	}
	t.status = TaskStatusCancelled
	t.errorMessage = reason
	now := time.Now()
	t.completedAt = &now
	return nil
}

// Setters for execution results

func (t *ManufacturingTask) SetActualQuantity(qty int)   { t.actualQuantity = qty }
func (t *ManufacturingTask) SetTotalCost(cost int)       { t.totalCost = cost }
func (t *ManufacturingTask) SetTotalRevenue(revenue int) { t.totalRevenue = revenue }
func (t *ManufacturingTask) SetPriority(priority int)    { t.priority = priority }
func (t *ManufacturingTask) SetQuantity(qty int)         { t.quantity = qty }

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

// GetDestination returns the starting destination waypoint for this task.
// For atomic tasks, returns the first destination (source for acquire, factory for collect).
func (t *ManufacturingTask) GetDestination() string {
	switch t.taskType {
	case TaskTypeAcquireDeliver:
		return t.sourceMarket
	case TaskTypeCollectSell:
		return t.factorySymbol
	case TaskTypeLiquidate:
		return t.targetMarket
	default:
		return ""
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
) *ManufacturingTask {
	return &ManufacturingTask{
		id:             id,
		pipelineID:     pipelineID,
		playerID:       playerID,
		taskType:       taskType,
		status:         status,
		good:           good,
		quantity:       quantity,
		actualQuantity: actualQuantity,
		sourceMarket:   sourceMarket,
		targetMarket:   targetMarket,
		factorySymbol:  factorySymbol,
		dependsOn:      dependsOn,
		assignedShip:   assignedShip,
		priority:       priority,
		retryCount:     retryCount,
		maxRetries:     maxRetries,
		totalCost:      totalCost,
		totalRevenue:   totalRevenue,
		errorMessage:   errorMessage,
		createdAt:      createdAt,
		readyAt:        readyAt,
		startedAt:      startedAt,
		completedAt:    completedAt,
	}
}

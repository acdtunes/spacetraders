package manufacturing

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TaskType represents the type of manufacturing task
type TaskType string

const (
	// TaskTypeAcquire - Buy raw material from export market
	TaskTypeAcquire TaskType = "ACQUIRE"

	// TaskTypeDeliver - Deliver material to factory
	TaskTypeDeliver TaskType = "DELIVER"

	// TaskTypeCollect - Buy produced good from factory (when supply HIGH)
	TaskTypeCollect TaskType = "COLLECT"

	// TaskTypeSell - Sell final product at demand market
	TaskTypeSell TaskType = "SELL"

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
)

const (
	// DefaultMaxRetries is the default number of retry attempts for failed tasks
	DefaultMaxRetries = 3
)

// ManufacturingTask represents a single atomic task in the manufacturing pipeline.
// Tasks are the unit of work assignment - each can be independently executed by a ship.
//
// Task Types:
//   - ACQUIRE: Buy raw material from export market
//   - DELIVER: Deliver material to factory (triggers production)
//   - COLLECT: Buy produced good from factory (when supply HIGH)
//   - SELL: Sell final product at demand market
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

// NewAcquireTask creates a task to buy goods from an export market
func NewAcquireTask(pipelineID string, playerID int, good string, sourceMarket string) *ManufacturingTask {
	task := NewManufacturingTask(TaskTypeAcquire, good, pipelineID, playerID)
	task.sourceMarket = sourceMarket
	return task
}

// NewDeliverTask creates a task to deliver goods to a factory
func NewDeliverTask(pipelineID string, playerID int, good string, targetMarket string, dependsOn []string) *ManufacturingTask {
	task := NewManufacturingTask(TaskTypeDeliver, good, pipelineID, playerID)
	task.targetMarket = targetMarket
	task.dependsOn = dependsOn
	return task
}

// NewCollectTask creates a task to collect produced goods from a factory
func NewCollectTask(pipelineID string, playerID int, good string, factorySymbol string, dependsOn []string) *ManufacturingTask {
	task := NewManufacturingTask(TaskTypeCollect, good, pipelineID, playerID)
	task.factorySymbol = factorySymbol
	task.dependsOn = dependsOn
	return task
}

// NewSellTask creates a task to sell goods at a demand market
func NewSellTask(pipelineID string, playerID int, good string, targetMarket string, dependsOn []string) *ManufacturingTask {
	task := NewManufacturingTask(TaskTypeSell, good, pipelineID, playerID)
	task.targetMarket = targetMarket
	task.dependsOn = dependsOn
	return task
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
	t.assignedShip = "" // Release ship
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

// IsTerminal returns true if the task is in a terminal state (COMPLETED or FAILED with no retries)
func (t *ManufacturingTask) IsTerminal() bool {
	if t.status == TaskStatusCompleted {
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

// GetDestination returns the destination waypoint for this task
func (t *ManufacturingTask) GetDestination() string {
	switch t.taskType {
	case TaskTypeAcquire:
		return t.sourceMarket
	case TaskTypeDeliver, TaskTypeSell, TaskTypeLiquidate:
		return t.targetMarket
	case TaskTypeCollect:
		return t.factorySymbol
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

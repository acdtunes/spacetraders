package manufacturing

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PipelineStatus represents the current status of a manufacturing pipeline
type PipelineStatus string

const (
	// PipelineStatusPlanning - Tasks are being created
	PipelineStatusPlanning PipelineStatus = "PLANNING"

	// PipelineStatusExecuting - Tasks are being executed
	PipelineStatusExecuting PipelineStatus = "EXECUTING"

	// PipelineStatusCompleted - All tasks completed successfully
	PipelineStatusCompleted PipelineStatus = "COMPLETED"

	// PipelineStatusFailed - Pipeline failed (unrecoverable)
	PipelineStatusFailed PipelineStatus = "FAILED"

	// PipelineStatusCancelled - Pipeline was cancelled by user
	PipelineStatusCancelled PipelineStatus = "CANCELLED"
)

// PipelineType distinguishes fabrication pipelines (limited) from collection pipelines (unlimited)
type PipelineType string

const (
	// PipelineTypeFabrication - Acquires inputs and delivers to factory
	// These pipelines are counted toward max_pipelines limit
	PipelineTypeFabrication PipelineType = "FABRICATION"

	// PipelineTypeCollection - Collects factory output and sells
	// These pipelines are unlimited (not counted toward max_pipelines)
	PipelineTypeCollection PipelineType = "COLLECTION"
)

// ManufacturingPipeline represents a complete manufacturing run for one product.
// A pipeline contains all tasks required to manufacture and sell a product.
//
// Lifecycle:
//   PLANNING -> EXECUTING -> COMPLETED
//                        \-> FAILED
//                        \-> CANCELLED
//
// Financial Tracking:
//   - totalCost: Sum of all ACQUIRE and COLLECT costs
//   - totalRevenue: Revenue from SELL task
//   - netProfit: totalRevenue - totalCost
type ManufacturingPipeline struct {
	id             string
	sequenceNumber int          // Sequential number for this pipeline (1, 2, 3...)
	pipelineType   PipelineType // FABRICATION (limited) or COLLECTION (unlimited)
	productGood    string       // Final product (e.g., LASER_RIFLES)
	sellMarket     string       // Where to sell final product
	expectedPrice  int          // Expected sale price per unit
	playerID       int

	status PipelineStatus

	// Task tracking
	tasks       []*ManufacturingTask
	tasksByID   map[string]*ManufacturingTask
	taskCount   int
	tasksReady  int
	tasksDone   int
	tasksFailed int

	// Financial tracking
	totalCost    int
	totalRevenue int
	netProfit    int

	// Timing
	createdAt   time.Time
	startedAt   *time.Time
	completedAt *time.Time

	// Error tracking
	errorMessage string
}

// NewPipeline creates a new fabrication pipeline (counted toward max_pipelines limit)
func NewPipeline(productGood string, sellMarket string, expectedPrice int, playerID int) *ManufacturingPipeline {
	return &ManufacturingPipeline{
		id:            uuid.New().String(),
		pipelineType:  PipelineTypeFabrication,
		productGood:   productGood,
		sellMarket:    sellMarket,
		expectedPrice: expectedPrice,
		playerID:      playerID,
		status:        PipelineStatusPlanning,
		tasks:         make([]*ManufacturingTask, 0),
		tasksByID:     make(map[string]*ManufacturingTask),
		createdAt:     time.Now(),
	}
}

// NewCollectionPipeline creates a new collection pipeline (unlimited, not counted toward max_pipelines)
func NewCollectionPipeline(productGood string, sellMarket string, expectedPrice int, playerID int) *ManufacturingPipeline {
	return &ManufacturingPipeline{
		id:            uuid.New().String(),
		pipelineType:  PipelineTypeCollection,
		productGood:   productGood,
		sellMarket:    sellMarket,
		expectedPrice: expectedPrice,
		playerID:      playerID,
		status:        PipelineStatusPlanning,
		tasks:         make([]*ManufacturingTask, 0),
		tasksByID:     make(map[string]*ManufacturingTask),
		createdAt:     time.Now(),
	}
}

// ReconstructPipeline rebuilds a pipeline from persistence
func ReconstructPipeline(
	id string,
	sequenceNumber int,
	pipelineType PipelineType,
	productGood string,
	sellMarket string,
	expectedPrice int,
	playerID int,
	status PipelineStatus,
	totalCost int,
	totalRevenue int,
	netProfit int,
	createdAt time.Time,
	startedAt *time.Time,
	completedAt *time.Time,
	errorMessage string,
) *ManufacturingPipeline {
	// Default to FABRICATION if not specified (for backward compatibility)
	if pipelineType == "" {
		pipelineType = PipelineTypeFabrication
	}
	return &ManufacturingPipeline{
		id:             id,
		sequenceNumber: sequenceNumber,
		pipelineType:   pipelineType,
		productGood:    productGood,
		sellMarket:     sellMarket,
		expectedPrice:  expectedPrice,
		playerID:       playerID,
		status:         status,
		tasks:          make([]*ManufacturingTask, 0),
		tasksByID:      make(map[string]*ManufacturingTask),
		totalCost:      totalCost,
		totalRevenue:   totalRevenue,
		netProfit:      netProfit,
		createdAt:      createdAt,
		startedAt:      startedAt,
		completedAt:    completedAt,
		errorMessage:   errorMessage,
	}
}

// Getters

func (p *ManufacturingPipeline) ID() string              { return p.id }
func (p *ManufacturingPipeline) SequenceNumber() int     { return p.sequenceNumber }
func (p *ManufacturingPipeline) PipelineType() PipelineType { return p.pipelineType }
func (p *ManufacturingPipeline) ProductGood() string     { return p.productGood }
func (p *ManufacturingPipeline) SellMarket() string      { return p.sellMarket }
func (p *ManufacturingPipeline) ExpectedPrice() int      { return p.expectedPrice }
func (p *ManufacturingPipeline) PlayerID() int           { return p.playerID }
func (p *ManufacturingPipeline) Status() PipelineStatus  { return p.status }
func (p *ManufacturingPipeline) TotalCost() int          { return p.totalCost }
func (p *ManufacturingPipeline) TotalRevenue() int       { return p.totalRevenue }
func (p *ManufacturingPipeline) NetProfit() int          { return p.netProfit }
func (p *ManufacturingPipeline) CreatedAt() time.Time    { return p.createdAt }
func (p *ManufacturingPipeline) StartedAt() *time.Time   { return p.startedAt }
func (p *ManufacturingPipeline) CompletedAt() *time.Time { return p.completedAt }
func (p *ManufacturingPipeline) ErrorMessage() string    { return p.errorMessage }
func (p *ManufacturingPipeline) TaskCount() int          { return p.taskCount }
func (p *ManufacturingPipeline) TasksReady() int         { return p.tasksReady }
func (p *ManufacturingPipeline) TasksDone() int          { return p.tasksDone }
func (p *ManufacturingPipeline) TasksFailed() int        { return p.tasksFailed }

// SetSequenceNumber sets the sequence number (called by repository during Add)
func (p *ManufacturingPipeline) SetSequenceNumber(seq int) { p.sequenceNumber = seq }

// IsFabrication returns true if this is a fabrication pipeline (counted toward max_pipelines)
func (p *ManufacturingPipeline) IsFabrication() bool { return p.pipelineType == PipelineTypeFabrication }

// IsCollection returns true if this is a collection pipeline (unlimited)
func (p *ManufacturingPipeline) IsCollection() bool { return p.pipelineType == PipelineTypeCollection }

// Tasks returns a copy of all tasks in this pipeline
func (p *ManufacturingPipeline) Tasks() []*ManufacturingTask {
	result := make([]*ManufacturingTask, len(p.tasks))
	copy(result, p.tasks)
	return result
}

// GetTask returns a task by ID
func (p *ManufacturingPipeline) GetTask(taskID string) *ManufacturingTask {
	return p.tasksByID[taskID]
}

// Task management

// AddTask adds a task to this pipeline
func (p *ManufacturingPipeline) AddTask(task *ManufacturingTask) error {
	if p.status != PipelineStatusPlanning {
		return &ErrInvalidPipelineTransition{
			PipelineID:  p.id,
			From:        p.status,
			To:          p.status,
			Description: "can only add tasks during PLANNING",
		}
	}
	p.tasks = append(p.tasks, task)
	p.tasksByID[task.ID()] = task
	p.taskCount++
	return nil
}

// SetTasks sets all tasks for this pipeline (used during reconstruction)
func (p *ManufacturingPipeline) SetTasks(tasks []*ManufacturingTask) {
	p.tasks = tasks
	p.tasksByID = make(map[string]*ManufacturingTask)
	for _, task := range tasks {
		p.tasksByID[task.ID()] = task
	}
	p.taskCount = len(tasks)
	p.recalculateTaskStats()
}

// recalculateTaskStats updates task counters from current task states
func (p *ManufacturingPipeline) recalculateTaskStats() {
	p.tasksReady = 0
	p.tasksDone = 0
	p.tasksFailed = 0

	for _, task := range p.tasks {
		switch task.Status() {
		case TaskStatusReady:
			p.tasksReady++
		case TaskStatusCompleted:
			p.tasksDone++
		case TaskStatusFailed:
			if !task.CanRetry() {
				p.tasksFailed++
			}
		}
	}
}

// State transitions

// Start transitions pipeline from PLANNING to EXECUTING
func (p *ManufacturingPipeline) Start() error {
	if p.status != PipelineStatusPlanning {
		return &ErrInvalidPipelineTransition{
			PipelineID: p.id,
			From:       p.status,
			To:         PipelineStatusExecuting,
		}
	}
	p.status = PipelineStatusExecuting
	now := time.Now()
	p.startedAt = &now

	// Mark initial tasks as ready (those with no dependencies)
	// EXCEPTION: COLLECT_SELL tasks stay PENDING until SupplyMonitor detects
	// factory supply is HIGH/ABUNDANT - they should NOT be marked ready at start
	for _, task := range p.tasks {
		if !task.HasDependencies() && task.Status() == TaskStatusPending {
			// Skip COLLECT_SELL tasks - they're gated by factory supply, not dependencies
			if task.TaskType() == TaskTypeCollectSell {
				continue
			}
			if err := task.MarkReady(); err == nil {
				p.tasksReady++
			}
		}
	}

	return nil
}

// Complete transitions pipeline to COMPLETED
func (p *ManufacturingPipeline) Complete() error {
	if p.status != PipelineStatusExecuting {
		return &ErrInvalidPipelineTransition{
			PipelineID: p.id,
			From:       p.status,
			To:         PipelineStatusCompleted,
		}
	}
	p.status = PipelineStatusCompleted
	now := time.Now()
	p.completedAt = &now
	p.calculateFinancials()
	return nil
}

// Fail transitions pipeline to FAILED
func (p *ManufacturingPipeline) Fail(errorMsg string) error {
	if p.status != PipelineStatusPlanning && p.status != PipelineStatusExecuting {
		return &ErrInvalidPipelineTransition{
			PipelineID: p.id,
			From:       p.status,
			To:         PipelineStatusFailed,
		}
	}
	p.status = PipelineStatusFailed
	p.errorMessage = errorMsg
	now := time.Now()
	p.completedAt = &now
	p.calculateFinancials()
	return nil
}

// Cancel transitions pipeline to CANCELLED
func (p *ManufacturingPipeline) Cancel() error {
	if p.status != PipelineStatusPlanning && p.status != PipelineStatusExecuting {
		return &ErrInvalidPipelineTransition{
			PipelineID: p.id,
			From:       p.status,
			To:         PipelineStatusCancelled,
		}
	}
	p.status = PipelineStatusCancelled
	now := time.Now()
	p.completedAt = &now
	p.calculateFinancials()
	return nil
}

// Task completion handling

// OnTaskCompleted should be called when a task completes
// Returns true if all tasks are now complete
func (p *ManufacturingPipeline) OnTaskCompleted(taskID string) (bool, error) {
	task := p.tasksByID[taskID]
	if task == nil {
		return false, &ErrTaskNotFound{TaskID: taskID}
	}

	p.tasksDone++

	// Update financials
	p.totalCost += task.TotalCost()
	p.totalRevenue += task.TotalRevenue()
	p.netProfit = p.totalRevenue - p.totalCost

	// Check if any tasks that depend on this one can now be marked ready
	p.updateDependentTasks(taskID)

	// Check if all tasks are complete
	return p.tasksDone >= p.taskCount, nil
}

// OnTaskFailed should be called when a task fails
// Returns true if the pipeline should be considered failed
func (p *ManufacturingPipeline) OnTaskFailed(taskID string) (bool, error) {
	task := p.tasksByID[taskID]
	if task == nil {
		return false, &ErrTaskNotFound{TaskID: taskID}
	}

	// If task can retry, don't count as failed yet
	if task.CanRetry() {
		return false, nil
	}

	p.tasksFailed++

	// Pipeline fails if any critical task fails without retries
	// For now, any failed task is critical
	return true, nil
}

// updateDependentTasks marks tasks as ready when their dependencies complete
func (p *ManufacturingPipeline) updateDependentTasks(completedTaskID string) {
	for _, task := range p.tasks {
		if task.Status() != TaskStatusPending {
			continue
		}

		// Check if this task depends on the completed task
		dependsOnCompleted := false
		for _, dep := range task.DependsOn() {
			if dep == completedTaskID {
				dependsOnCompleted = true
				break
			}
		}

		if !dependsOnCompleted {
			continue
		}

		// Check if all dependencies are now complete
		allDepsComplete := true
		for _, depID := range task.DependsOn() {
			depTask := p.tasksByID[depID]
			if depTask == nil || depTask.Status() != TaskStatusCompleted {
				allDepsComplete = false
				break
			}
		}

		if allDepsComplete {
			if err := task.MarkReady(); err == nil {
				p.tasksReady++
			}
		}
	}
}

// calculateFinancials updates financial totals from all tasks
func (p *ManufacturingPipeline) calculateFinancials() {
	p.totalCost = 0
	p.totalRevenue = 0

	for _, task := range p.tasks {
		p.totalCost += task.TotalCost()
		p.totalRevenue += task.TotalRevenue()
	}

	p.netProfit = p.totalRevenue - p.totalCost
}

// Query methods

// GetReadyTasks returns all tasks that are ready to execute
func (p *ManufacturingPipeline) GetReadyTasks() []*ManufacturingTask {
	ready := make([]*ManufacturingTask, 0)
	for _, task := range p.tasks {
		if task.Status() == TaskStatusReady {
			ready = append(ready, task)
		}
	}
	return ready
}

// GetPendingTasks returns all tasks that are pending
func (p *ManufacturingPipeline) GetPendingTasks() []*ManufacturingTask {
	pending := make([]*ManufacturingTask, 0)
	for _, task := range p.tasks {
		if task.Status() == TaskStatusPending {
			pending = append(pending, task)
		}
	}
	return pending
}

// GetExecutingTasks returns all tasks currently being executed
func (p *ManufacturingPipeline) GetExecutingTasks() []*ManufacturingTask {
	executing := make([]*ManufacturingTask, 0)
	for _, task := range p.tasks {
		if task.Status() == TaskStatusExecuting || task.Status() == TaskStatusAssigned {
			executing = append(executing, task)
		}
	}
	return executing
}

// Progress returns completion percentage (0-100)
func (p *ManufacturingPipeline) Progress() float64 {
	if p.taskCount == 0 {
		return 0
	}
	return float64(p.tasksDone) / float64(p.taskCount) * 100
}

// IsTerminal returns true if pipeline is in a terminal state
func (p *ManufacturingPipeline) IsTerminal() bool {
	return p.status == PipelineStatusCompleted ||
		p.status == PipelineStatusFailed ||
		p.status == PipelineStatusCancelled
}

// RuntimeDuration returns how long the pipeline has been running
func (p *ManufacturingPipeline) RuntimeDuration() time.Duration {
	if p.startedAt == nil {
		return 0
	}
	if p.completedAt != nil {
		return p.completedAt.Sub(*p.startedAt)
	}
	return time.Since(*p.startedAt)
}

// String provides human-readable representation
func (p *ManufacturingPipeline) String() string {
	return fmt.Sprintf("Pipeline[%s, product=%s, status=%s, progress=%.0f%%, profit=%d]",
		p.id[:8], p.productGood, p.status, p.Progress(), p.netProfit)
}

// ReconstitutePipeline creates a pipeline from persisted data (for repository use only)
func ReconstitutePipeline(
	id string,
	sequenceNumber int,
	pipelineType PipelineType,
	productGood string,
	sellMarket string,
	expectedPrice int,
	playerID int,
	status PipelineStatus,
	totalCost int,
	totalRevenue int,
	netProfit int,
	errorMessage string,
	createdAt time.Time,
	startedAt *time.Time,
	completedAt *time.Time,
) *ManufacturingPipeline {
	// Default to FABRICATION if not specified (for backward compatibility)
	if pipelineType == "" {
		pipelineType = PipelineTypeFabrication
	}
	return &ManufacturingPipeline{
		id:             id,
		sequenceNumber: sequenceNumber,
		pipelineType:   pipelineType,
		productGood:    productGood,
		sellMarket:     sellMarket,
		expectedPrice:  expectedPrice,
		playerID:       playerID,
		status:         status,
		totalCost:      totalCost,
		totalRevenue:   totalRevenue,
		netProfit:      netProfit,
		errorMessage:   errorMessage,
		createdAt:      createdAt,
		startedAt:      startedAt,
		completedAt:    completedAt,
		tasks:          make([]*ManufacturingTask, 0),
		tasksByID:      make(map[string]*ManufacturingTask),
	}
}

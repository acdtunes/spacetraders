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

	// PipelineTypeConstruction - Produces and delivers goods to construction sites
	// Used for supplying materials to jump gates and other construction projects
	PipelineTypeConstruction PipelineType = "CONSTRUCTION"
)

const defaultConstructionMaxWorkers = 5

// ManufacturingPipeline represents a complete manufacturing run for one product.
// A pipeline contains all tasks required to manufacture and sell a product.
//
// Lifecycle:
//
//	PLANNING -> EXECUTING -> COMPLETED
//	                     \-> FAILED
//	                     \-> CANCELLED
//
// Financial Tracking:
//   - totalCost: Sum of all ACQUIRE and COLLECT costs
//   - totalRevenue: Revenue from SELL task
//   - netProfit: totalRevenue - totalCost
type ManufacturingPipeline struct {
	id             string
	sequenceNumber int          // Sequential number for this pipeline (1, 2, 3...)
	pipelineType   PipelineType // FABRICATION (limited) or COLLECTION (unlimited) or CONSTRUCTION
	productGood    string       // Final product (e.g., LASER_RIFLES)
	sellMarket     string       // Where to sell final product
	expectedPrice  int          // Expected sale price per unit
	playerID       int

	status PipelineStatus

	// Task tracking
	tasks     []*ManufacturingTask
	tasksByID map[string]*ManufacturingTask

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

	// Construction-specific fields (only used when pipelineType == CONSTRUCTION)
	constructionSite string                        // Waypoint symbol of construction site (e.g., "X1-FB5-I61")
	materials        []*ConstructionMaterialTarget // Materials to deliver with their quantities
	supplyChainDepth int                           // How deep to go in supply chain (0=full, 1=raw, 2=intermediate)
	maxWorkers       int                           // Maximum parallel workers (0=unlimited, default 5)
	minSupply        string                        // Caller-set EXPORT sourcing floor (sp-ezz9/sp-j2hq), e.g. "SCARCE". Empty = unset (defaults to MODERATE).
}

// NewPipeline creates a new fabrication pipeline (counted toward max_pipelines limit)
func NewPipeline(productGood string, sellMarket string, expectedPrice int, playerID int) *ManufacturingPipeline {
	return newSalesPipeline(PipelineTypeFabrication, productGood, sellMarket, expectedPrice, playerID)
}

// NewCollectionPipeline creates a new collection pipeline (unlimited, not counted toward max_pipelines)
func NewCollectionPipeline(productGood string, sellMarket string, expectedPrice int, playerID int) *ManufacturingPipeline {
	return newSalesPipeline(PipelineTypeCollection, productGood, sellMarket, expectedPrice, playerID)
}

func newSalesPipeline(pipelineType PipelineType, productGood string, sellMarket string, expectedPrice int, playerID int) *ManufacturingPipeline {
	return &ManufacturingPipeline{
		id:            uuid.New().String(),
		pipelineType:  pipelineType,
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

// NewConstructionPipeline creates a new construction pipeline for delivering materials to a construction site.
// Construction pipelines track multiple materials with their individual delivery progress.
func NewConstructionPipeline(constructionSite string, playerID int, supplyChainDepth int, maxWorkers int) *ManufacturingPipeline {
	if maxWorkers <= 0 {
		maxWorkers = defaultConstructionMaxWorkers
	}
	return &ManufacturingPipeline{
		id:               uuid.New().String(),
		pipelineType:     PipelineTypeConstruction,
		productGood:      "", // Set by first material added
		sellMarket:       constructionSite,
		expectedPrice:    0, // Construction doesn't have sale prices
		playerID:         playerID,
		status:           PipelineStatusPlanning,
		tasks:            make([]*ManufacturingTask, 0),
		tasksByID:        make(map[string]*ManufacturingTask),
		createdAt:        time.Now(),
		constructionSite: constructionSite,
		materials:        make([]*ConstructionMaterialTarget, 0),
		supplyChainDepth: supplyChainDepth,
		maxWorkers:       maxWorkers,
	}
}

// Getters

func (p *ManufacturingPipeline) ID() string                 { return p.id }
func (p *ManufacturingPipeline) SequenceNumber() int        { return p.sequenceNumber }
func (p *ManufacturingPipeline) PipelineType() PipelineType { return p.pipelineType }
func (p *ManufacturingPipeline) ProductGood() string        { return p.productGood }
func (p *ManufacturingPipeline) SellMarket() string         { return p.sellMarket }
func (p *ManufacturingPipeline) ExpectedPrice() int         { return p.expectedPrice }
func (p *ManufacturingPipeline) PlayerID() int              { return p.playerID }
func (p *ManufacturingPipeline) Status() PipelineStatus     { return p.status }
func (p *ManufacturingPipeline) TotalCost() int             { return p.totalCost }
func (p *ManufacturingPipeline) TotalRevenue() int          { return p.totalRevenue }
func (p *ManufacturingPipeline) NetProfit() int             { return p.netProfit }
func (p *ManufacturingPipeline) CreatedAt() time.Time       { return p.createdAt }
func (p *ManufacturingPipeline) StartedAt() *time.Time      { return p.startedAt }
func (p *ManufacturingPipeline) CompletedAt() *time.Time    { return p.completedAt }
func (p *ManufacturingPipeline) ErrorMessage() string       { return p.errorMessage }
func (p *ManufacturingPipeline) TaskCount() int             { return len(p.tasks) }

func (p *ManufacturingPipeline) TasksReady() int {
	return p.countTasks(func(task *ManufacturingTask) bool {
		return task.Status() == TaskStatusReady
	})
}

func (p *ManufacturingPipeline) TasksDone() int {
	return p.countTasks(func(task *ManufacturingTask) bool {
		return task.Status() == TaskStatusCompleted
	})
}

func (p *ManufacturingPipeline) TasksFailed() int {
	return p.countTasks(func(task *ManufacturingTask) bool {
		return task.Status() == TaskStatusFailed && !task.CanRetry()
	})
}

func (p *ManufacturingPipeline) countTasks(predicate func(*ManufacturingTask) bool) int {
	count := 0
	for _, task := range p.tasks {
		if predicate(task) {
			count++
		}
	}
	return count
}

// SetSequenceNumber sets the sequence number (called by repository during Add)
func (p *ManufacturingPipeline) SetSequenceNumber(seq int) { p.sequenceNumber = seq }

// Construction-specific getters

// ConstructionSite returns the waypoint symbol of the construction site (CONSTRUCTION pipelines only)
func (p *ManufacturingPipeline) ConstructionSite() string { return p.constructionSite }

// Materials returns the material targets for this construction pipeline
func (p *ManufacturingPipeline) Materials() []*ConstructionMaterialTarget {
	result := make([]*ConstructionMaterialTarget, len(p.materials))
	copy(result, p.materials)
	return result
}

// SupplyChainDepth returns how deep to go in the supply chain (CONSTRUCTION pipelines only)
// 0 = full chain (produce everything), 1 = raw materials only, 2 = intermediate goods
func (p *ManufacturingPipeline) SupplyChainDepth() int { return p.supplyChainDepth }

// MaxWorkers returns the maximum parallel workers for this pipeline (CONSTRUCTION pipelines only)
// 0 = unlimited, default is 5
func (p *ManufacturingPipeline) MaxWorkers() int { return p.maxWorkers }

// MinSupply returns the caller-set EXPORT sourcing floor for this construction
// pipeline (e.g. "SCARCE"), CONSTRUCTION pipelines only. Empty string means
// unset, which callers (MarketLocator.FindConstructionSource) treat as the
// default MODERATE floor.
func (p *ManufacturingPipeline) MinSupply() string { return p.minSupply }

// SetMinSupply sets the caller-set EXPORT sourcing floor for this construction
// pipeline. Used both when planning a new pipeline and when resuming an
// existing one with an updated --min-supply flag (sp-j2hq).
func (p *ManufacturingPipeline) SetMinSupply(minSupply string) { p.minSupply = minSupply }

// AddMaterial adds a material target to the construction pipeline
func (p *ManufacturingPipeline) AddMaterial(material *ConstructionMaterialTarget) error {
	if p.pipelineType != PipelineTypeConstruction {
		return fmt.Errorf("can only add materials to CONSTRUCTION pipelines")
	}
	if p.status != PipelineStatusPlanning {
		return &ErrInvalidPipelineTransition{
			PipelineID:  p.id,
			From:        p.status,
			To:          p.status,
			Description: "can only add materials during PLANNING",
		}
	}
	p.materials = append(p.materials, material)
	// Set productGood to first material for display purposes
	if p.productGood == "" {
		p.productGood = material.TradeSymbol()
	}
	return nil
}

// SetMaterials sets all materials for the pipeline (used during reconstruction)
func (p *ManufacturingPipeline) SetMaterials(materials []*ConstructionMaterialTarget) {
	p.materials = materials
}

// GetMaterial returns the material target for a specific trade symbol
func (p *ManufacturingPipeline) GetMaterial(tradeSymbol string) *ConstructionMaterialTarget {
	for _, m := range p.materials {
		if m.TradeSymbol() == tradeSymbol {
			return m
		}
	}
	return nil
}

// RecordMaterialDelivery updates the delivered quantity for a specific material
func (p *ManufacturingPipeline) RecordMaterialDelivery(tradeSymbol string, units int) error {
	material := p.GetMaterial(tradeSymbol)
	if material == nil {
		return fmt.Errorf("material %s not found in pipeline", tradeSymbol)
	}
	material.RecordDelivery(units)
	return nil
}

// ConstructionProgress returns overall completion percentage across all materials
func (p *ManufacturingPipeline) ConstructionProgress() float64 {
	if len(p.materials) == 0 {
		return 0
	}
	var totalTarget, totalDelivered int
	for _, m := range p.materials {
		totalTarget += m.TargetQuantity()
		totalDelivered += m.DeliveredQuantity()
	}
	if totalTarget == 0 {
		return 100.0
	}
	return float64(totalDelivered) / float64(totalTarget) * 100
}

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
	return nil
}

// SetTasks sets all tasks for this pipeline (used during reconstruction)
func (p *ManufacturingPipeline) SetTasks(tasks []*ManufacturingTask) {
	p.tasks = tasks
	p.tasksByID = make(map[string]*ManufacturingTask)
	for _, task := range tasks {
		p.tasksByID[task.ID()] = task
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
	p.startedAt = nowPtr()

	// Mark initial tasks as ready (those with no dependencies)
	// EXCEPTION: COLLECT_SELL tasks stay PENDING until SupplyMonitor detects
	// factory supply is HIGH/ABUNDANT - they should NOT be marked ready at start
	for _, task := range p.tasks {
		if !task.HasDependencies() && task.Status() == TaskStatusPending {
			// Skip COLLECT_SELL tasks - they're gated by factory supply, not dependencies
			if task.TaskType() == TaskTypeCollectSell {
				continue
			}
			// Skip DEFERRED construction deliveries - they have no buy source yet
			// and are re-sourced by the SupplyMonitor when supply regenerates.
			// Marking them ready would dispatch a task that cannot acquire goods.
			if task.IsDeferredConstruction() {
				continue
			}
			_ = task.MarkReady()
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
	p.completedAt = nowPtr()
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
	p.completedAt = nowPtr()
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
	p.completedAt = nowPtr()
	p.calculateFinancials()
	return nil
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

// Progress returns completion percentage (0-100)
func (p *ManufacturingPipeline) Progress() float64 {
	if len(p.tasks) == 0 {
		return 0
	}
	return float64(p.TasksDone()) / float64(len(p.tasks)) * 100
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
	// Construction-specific fields
	constructionSite string,
	supplyChainDepth int,
	maxWorkers int,
	minSupply string,
) *ManufacturingPipeline {
	// Default to FABRICATION if not specified (for backward compatibility)
	if pipelineType == "" {
		pipelineType = PipelineTypeFabrication
	}
	return &ManufacturingPipeline{
		id:               id,
		sequenceNumber:   sequenceNumber,
		pipelineType:     pipelineType,
		productGood:      productGood,
		sellMarket:       sellMarket,
		expectedPrice:    expectedPrice,
		playerID:         playerID,
		status:           status,
		totalCost:        totalCost,
		totalRevenue:     totalRevenue,
		netProfit:        netProfit,
		errorMessage:     errorMessage,
		createdAt:        createdAt,
		startedAt:        startedAt,
		completedAt:      completedAt,
		tasks:            make([]*ManufacturingTask, 0),
		tasksByID:        make(map[string]*ManufacturingTask),
		constructionSite: constructionSite,
		materials:        make([]*ConstructionMaterialTarget, 0), // Set via SetMaterials after reconstruction
		supplyChainDepth: supplyChainDepth,
		maxWorkers:       maxWorkers,
		minSupply:        minSupply,
	}
}

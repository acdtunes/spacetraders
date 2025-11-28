package manufacturing

// ReadinessConditions holds market/factory state for readiness evaluation.
type ReadinessConditions struct {
	SourceSupply     SupplyLevel
	FactorySupply    SupplyLevel
	SellMarketSupply SupplyLevel
	DependenciesMet  bool
	IsRawMaterial    bool
	FactoryReady     bool // Has factory reached collection threshold?
}

// TaskReadinessSpecification encapsulates task readiness business rules.
// A task is ready when all preconditions for execution are met.
type TaskReadinessSpecification struct{}

// NewTaskReadinessSpecification creates a new specification.
func NewTaskReadinessSpecification() *TaskReadinessSpecification {
	return &TaskReadinessSpecification{}
}

// CanExecute returns true if the task can be executed given current conditions.
func (s *TaskReadinessSpecification) CanExecute(task *ManufacturingTask, cond ReadinessConditions) bool {
	switch task.TaskType() {
	case TaskTypeAcquireDeliver:
		return s.canExecuteAcquireDeliver(task, cond)
	case TaskTypeCollectSell:
		return s.canExecuteCollectSell(cond)
	case TaskTypeLiquidate:
		return true // Liquidation always allowed
	default:
		return false
	}
}

// canExecuteAcquireDeliver checks if an ACQUIRE_DELIVER task can execute.
func (s *TaskReadinessSpecification) canExecuteAcquireDeliver(task *ManufacturingTask, cond ReadinessConditions) bool {
	// Must have dependencies met (unless raw material)
	if !cond.DependenciesMet && !cond.IsRawMaterial {
		return false
	}
	// Source market must have purchasable supply
	return cond.SourceSupply.AllowsPurchase()
}

// canExecuteCollectSell checks if a COLLECT_SELL task can execute.
func (s *TaskReadinessSpecification) canExecuteCollectSell(cond ReadinessConditions) bool {
	// Factory must have produced output (HIGH or ABUNDANT supply)
	if !cond.FactorySupply.IsFavorableForCollection() && !cond.FactoryReady {
		return false
	}
	// Sell market should not be saturated
	return !cond.SellMarketSupply.IsSaturated()
}

// ShouldSkipSellMarket returns true if sell market is too saturated.
func (s *TaskReadinessSpecification) ShouldSkipSellMarket(supply SupplyLevel) bool {
	return supply.IsSaturated()
}

// IsFactoryReadyForCollection returns true if factory supply is favorable for collection.
// This requires HIGH or ABUNDANT supply, indicating the factory has produced output.
func (s *TaskReadinessSpecification) IsFactoryReadyForCollection(factorySupply SupplyLevel) bool {
	return factorySupply.IsFavorableForCollection()
}

// CanSourceFromMarket returns true if the source market has purchasable supply.
func (s *TaskReadinessSpecification) CanSourceFromMarket(sourceSupply SupplyLevel) bool {
	return sourceSupply.AllowsPurchase()
}

// RequiresAbundantSupplyToStart returns true if the task requires ABUNDANT supply
// at assignment time (to provide buffer for supply drops during navigation).
// We require ABUNDANT to START a task, giving a buffer for supply drops.
// The executor will still collect if supply is HIGH when the ship arrives.
func (s *TaskReadinessSpecification) RequiresAbundantSupplyToStart(taskType TaskType) bool {
	return taskType == TaskTypeCollectSell
}

// GetMinimumStartSupply returns the minimum supply level required to start a task.
func (s *TaskReadinessSpecification) GetMinimumStartSupply(taskType TaskType) SupplyLevel {
	if taskType == TaskTypeCollectSell {
		return SupplyLevelAbundant // Require ABUNDANT to start, executor allows HIGH on arrival
	}
	return SupplyLevelModerate // Default for other task types
}

// GetMinimumExecuteSupply returns the minimum supply level required to execute a task.
// This is checked when the ship arrives and is about to perform the operation.
func (s *TaskReadinessSpecification) GetMinimumExecuteSupply(taskType TaskType) SupplyLevel {
	if taskType == TaskTypeCollectSell {
		return SupplyLevelHigh // Can execute at HIGH (more lenient than start)
	}
	return SupplyLevelLimited // Can execute at LIMITED for purchases
}

// EvaluateReadiness returns a detailed assessment of task readiness.
func (s *TaskReadinessSpecification) EvaluateReadiness(task *ManufacturingTask, cond ReadinessConditions) *ReadinessAssessment {
	assessment := &ReadinessAssessment{
		TaskID:   task.ID(),
		TaskType: task.TaskType(),
		CanStart: true,
		Reasons:  make([]string, 0),
	}

	switch task.TaskType() {
	case TaskTypeAcquireDeliver:
		if !cond.DependenciesMet && !cond.IsRawMaterial {
			assessment.CanStart = false
			assessment.Reasons = append(assessment.Reasons, "dependencies not met")
		}
		if !cond.SourceSupply.AllowsPurchase() {
			assessment.CanStart = false
			assessment.Reasons = append(assessment.Reasons, "source supply is SCARCE")
		}

	case TaskTypeCollectSell:
		if !cond.FactorySupply.IsFavorableForCollection() && !cond.FactoryReady {
			assessment.CanStart = false
			assessment.Reasons = append(assessment.Reasons, "factory supply not HIGH/ABUNDANT")
		}
		if cond.SellMarketSupply.IsSaturated() {
			assessment.CanStart = false
			assessment.Reasons = append(assessment.Reasons, "sell market saturated")
		}

	case TaskTypeLiquidate:
		// Always can start
	}

	return assessment
}

// ReadinessAssessment contains detailed information about task readiness.
type ReadinessAssessment struct {
	TaskID   string
	TaskType TaskType
	CanStart bool
	Reasons  []string
}

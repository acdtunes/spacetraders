package manufacturing

import "fmt"

// ErrInvalidTaskTransition indicates an invalid task state transition
type ErrInvalidTaskTransition struct {
	TaskID      string
	From        TaskStatus
	To          TaskStatus
	Description string
}

func (e *ErrInvalidTaskTransition) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("invalid task transition for %s: %s -> %s: %s",
			e.TaskID, e.From, e.To, e.Description)
	}
	return fmt.Sprintf("invalid task transition for %s: %s -> %s",
		e.TaskID, e.From, e.To)
}

// ErrInvalidPipelineTransition indicates an invalid pipeline state transition
type ErrInvalidPipelineTransition struct {
	PipelineID  string
	From        PipelineStatus
	To          PipelineStatus
	Description string
}

func (e *ErrInvalidPipelineTransition) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("invalid pipeline transition for %s: %s -> %s: %s",
			e.PipelineID, e.From, e.To, e.Description)
	}
	return fmt.Sprintf("invalid pipeline transition for %s: %s -> %s",
		e.PipelineID, e.From, e.To)
}

// ErrTaskNotFound indicates a task could not be found
type ErrTaskNotFound struct {
	TaskID string
}

func (e *ErrTaskNotFound) Error() string {
	return fmt.Sprintf("task not found: %s", e.TaskID)
}

// ErrPipelineNotFound indicates a pipeline could not be found
type ErrPipelineNotFound struct {
	PipelineID string
}

func (e *ErrPipelineNotFound) Error() string {
	return fmt.Sprintf("pipeline not found: %s", e.PipelineID)
}

// ErrTaskAlreadyAssigned indicates a task is already assigned to a ship
type ErrTaskAlreadyAssigned struct {
	TaskID       string
	AssignedShip string
}

func (e *ErrTaskAlreadyAssigned) Error() string {
	return fmt.Sprintf("task %s already assigned to ship %s", e.TaskID, e.AssignedShip)
}

// ErrDependencyNotMet indicates a task dependency has not been satisfied
type ErrDependencyNotMet struct {
	TaskID          string
	DependencyID    string
	DependencyState TaskStatus
}

func (e *ErrDependencyNotMet) Error() string {
	return fmt.Sprintf("task %s depends on %s which is in state %s",
		e.TaskID, e.DependencyID, e.DependencyState)
}

// ErrFactoryNotReady indicates a factory is not ready for collection
type ErrFactoryNotReady struct {
	FactorySymbol string
	OutputGood    string
	CurrentSupply string
	RequiredSupply string
}

func (e *ErrFactoryNotReady) Error() string {
	return fmt.Sprintf("factory %s not ready for %s collection: supply is %s (need %s)",
		e.FactorySymbol, e.OutputGood, e.CurrentSupply, e.RequiredSupply)
}

// ErrInsufficientInputs indicates not all inputs have been delivered to a factory
type ErrInsufficientInputs struct {
	FactorySymbol  string
	OutputGood     string
	MissingInputs  []string
}

func (e *ErrInsufficientInputs) Error() string {
	return fmt.Sprintf("factory %s missing inputs for %s: %v",
		e.FactorySymbol, e.OutputGood, e.MissingInputs)
}

// ErrNoValidSellMarket indicates no valid sell market could be found
type ErrNoValidSellMarket struct {
	Good   string
	Reason string
}

func (e *ErrNoValidSellMarket) Error() string {
	return fmt.Sprintf("no valid sell market for %s: %s", e.Good, e.Reason)
}

// NewErrNoValidSellMarket creates a new ErrNoValidSellMarket error
func NewErrNoValidSellMarket(good string) *ErrNoValidSellMarket {
	return &ErrNoValidSellMarket{
		Good:   good,
		Reason: "no markets found that import this good",
	}
}

// ErrMaxRetriesExceeded indicates a task has exceeded its retry limit
type ErrMaxRetriesExceeded struct {
	TaskID     string
	RetryCount int
	MaxRetries int
}

func (e *ErrMaxRetriesExceeded) Error() string {
	return fmt.Sprintf("task %s exceeded max retries (%d/%d)",
		e.TaskID, e.RetryCount, e.MaxRetries)
}

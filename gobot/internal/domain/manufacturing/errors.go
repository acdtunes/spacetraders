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

// ErrTaskAlreadyAssigned indicates a task is already assigned to a ship
type ErrTaskAlreadyAssigned struct {
	TaskID       string
	AssignedShip string
}

func (e *ErrTaskAlreadyAssigned) Error() string {
	return fmt.Sprintf("task %s already assigned to ship %s", e.TaskID, e.AssignedShip)
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

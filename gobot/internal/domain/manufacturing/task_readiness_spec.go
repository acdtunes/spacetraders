package manufacturing

// TaskReadinessSpecification encapsulates task readiness business rules.
// A task is ready when all preconditions for execution are met.
type TaskReadinessSpecification struct{}

// NewTaskReadinessSpecification creates a new specification.
func NewTaskReadinessSpecification() *TaskReadinessSpecification {
	return &TaskReadinessSpecification{}
}

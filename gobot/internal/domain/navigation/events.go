package navigation

// WorkerCompletedEvent is published when a worker container completes execution.
// Used by coordinators to track worker lifecycle and assign new work.
type WorkerCompletedEvent struct {
	ContainerID   string
	PlayerID      int
	ShipSymbol    string
	CoordinatorID string // Parent coordinator container ID
	Success       bool
	Error         string // Error message if failed
}

// TasksBecameReadyEvent is published when tasks become ready for assignment.
// Used by manufacturing coordinator to trigger task assignment.
type TasksBecameReadyEvent struct {
	PlayerID   int
	PipelineID string // Optional: specific pipeline with ready tasks
}

// TransportRequestedEvent is published when a siphon requests transport assignment.
// Used by gas coordination to signal transport need.
type TransportRequestedEvent struct {
	SiphonSymbol string
	PlayerID     int
}

// TransferCompletedEvent is published when a cargo transfer is completed.
// Used by gas coordination to track transfer lifecycle.
type TransferCompletedEvent struct {
	SiphonSymbol    string
	TransportSymbol string
	PlayerID        int
}

package navigation

// WorkerCompletedEvent is published when a worker container completes execution.
// Used by coordinators to track worker lifecycle and assign new work.
type WorkerCompletedEvent struct {
	ContainerID   string // Unique container identifier
	PlayerID      int    // Player who owns this worker
	ShipSymbol    string // Ship that completed work
	CoordinatorID string // Parent coordinator container ID
	Success       bool   // Whether the worker completed successfully
	Error         string // Error message if failed
}

// TasksBecameReadyEvent is published when tasks become ready for assignment.
// Used by manufacturing coordinator to trigger task assignment.
type TasksBecameReadyEvent struct {
	PlayerID   int    // Player whose tasks became ready
	PipelineID string // Optional: specific pipeline with ready tasks
}

// TransportRequestedEvent is published when a siphon requests transport assignment.
// Used by gas coordination to signal transport need.
type TransportRequestedEvent struct {
	SiphonSymbol string // Ship symbol of the siphon
	PlayerID     int    // Player who owns the operation
}

// TransferCompletedEvent is published when a cargo transfer is completed.
// Used by gas coordination to track transfer lifecycle.
type TransferCompletedEvent struct {
	SiphonSymbol    string // Ship symbol of the siphon
	TransportSymbol string // Ship symbol of the transport
	PlayerID        int    // Player who owns the operation
}

package types

import "github.com/andrescamacho/spacetraders-go/internal/domain/goods"

// RunFactoryWorkerCommand initiates a factory worker to produce a good
type RunFactoryWorkerCommand struct {
	PlayerID       int
	ShipSymbol     string
	ProductionNode *goods.SupplyChainNode
	FactoryID      string
	SystemSymbol   string
	CoordinatorID  string              // Optional: for signaling completion back to coordinator
	CompletionChan chan<- WorkerResult // Optional: channel for async completion signaling
}

// RunFactoryWorkerResponse contains the result of the worker operation
type RunFactoryWorkerResponse struct {
	FactoryID        string
	Good             string
	QuantityAcquired int
	TotalCost        int
	Completed        bool
	Error            string
}

// WorkerResult is sent via channel when worker completes
type WorkerResult struct {
	FactoryID        string
	Good             string
	QuantityAcquired int
	TotalCost        int
	Error            error
}

// RunFactoryCoordinatorCommand initiates a factory coordinator for fleet-based production
type RunFactoryCoordinatorCommand struct {
	PlayerID      int
	TargetGood    string
	SystemSymbol  string // Where to produce (defaults to current system)
	ContainerID   string // Container ID for ship assignment tracking
	MaxIterations int    // Maximum iterations to run (-1 for infinite, 0 for single run, >0 for specific count)
	// InputsOnly, when true, feeds the dependency tree but does NOT harvest the
	// fabricated output: the factory produces the target good and leaves it in its
	// export stock for a construction pipeline to source. This is the era-2 gate-fill
	// fix — a harvesting factory bought back its own 149 FAB_MATS and froze the fill
	// (sp-q02m). Default (false) preserves the original harvest-the-output behavior.
	InputsOnly bool
}

// RunFactoryCoordinatorResponse contains the result of the coordinator operation
type RunFactoryCoordinatorResponse struct {
	FactoryID        string
	TargetGood       string
	QuantityAcquired int
	TotalCost        int
	NodesCompleted   int
	NodesTotal       int
	ShipsUsed        int
	Completed        bool
	Error            string
	// NoWorkReason is set when the iteration completed cleanly (Error == "")
	// but performed no work at all — pre-spend guard park, or every claimable
	// node parked for lack of a claimable hull (sp-2q2o). A -1 (infinite)
	// caller uses this to back off before the next iteration instead of
	// spinning; it stays empty on any iteration that produced something.
	NoWorkReason string
}

package types

import "github.com/andrescamacho/spacetraders-go/internal/domain/goods"

// RunFactoryWorkerCommand initiates a factory worker to produce a good
type RunFactoryWorkerCommand struct {
	PlayerID         int
	ShipSymbol       string
	ProductionNode   *goods.SupplyChainNode
	FactoryID        string
	SystemSymbol     string
	CoordinatorID    string // Optional: for signaling completion back to coordinator
	CompletionChan   chan<- WorkerResult // Optional: channel for async completion signaling
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

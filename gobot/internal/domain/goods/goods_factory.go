package goods

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FactoryStatus represents the lifecycle state of a goods factory
type FactoryStatus string

const (
	// FactoryStatusPending indicates factory is queued but not started
	FactoryStatusPending FactoryStatus = "PENDING"

	// FactoryStatusActive indicates factory is actively producing
	FactoryStatusActive FactoryStatus = "ACTIVE"

	// FactoryStatusCompleted indicates factory finished successfully
	FactoryStatusCompleted FactoryStatus = "COMPLETED"

	// FactoryStatusFailed indicates factory encountered an error
	FactoryStatusFailed FactoryStatus = "FAILED"

	// FactoryStatusStopped indicates factory was stopped by user
	FactoryStatusStopped FactoryStatus = "STOPPED"
)

// GoodsFactory is the aggregate root for goods production operations.
// It orchestrates the automated production of any good in the SpaceTraders supply chain
// by recursively acquiring inputs, delivering them to manufacturing waypoints,
// and coordinating multi-ship fleets for parallel production.
//
// Design Principles:
// - Market-Driven: Prefers buying when available, fabricates only when necessary
// - Fleet Coordination: Parallelizes production across multiple ships
// - Recursive Production: Builds complete dependency trees to raw materials
// - No Fixed Quantities: Acquires whatever quantity is available at markets
type GoodsFactory struct {
	id             string
	playerID       int
	targetGood     string
	systemSymbol   string
	dependencyTree *SupplyChainNode
	status         FactoryStatus
	metadata       map[string]interface{}
	lifecycle      *shared.LifecycleStateMachine
	clock          shared.Clock

	// Tracking metrics (set during execution)
	quantityAcquired int
	totalCost        int // Credits spent on purchases
	shipsUsed        int // Number of ships utilized
	marketQueries    int // Number of market queries performed
	parallelLevels   int // Number of parallel execution levels
	estimatedSpeedup float64 // Estimated speedup from parallelization
}

// NewGoodsFactory creates a new goods factory instance
// If clock is nil, uses RealClock (production behavior)
func NewGoodsFactory(
	id string,
	playerID int,
	targetGood string,
	systemSymbol string,
	dependencyTree *SupplyChainNode,
	metadata map[string]interface{},
	clock shared.Clock,
) *GoodsFactory {
	// Default to real clock if not provided
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &GoodsFactory{
		id:               id,
		playerID:         playerID,
		targetGood:       targetGood,
		systemSymbol:     systemSymbol,
		dependencyTree:   dependencyTree,
		status:           FactoryStatusPending,
		metadata:         metadata,
		lifecycle:        shared.NewLifecycleStateMachine(clock),
		clock:            clock,
		quantityAcquired: 0,
		totalCost:        0,
	}
}

// Getters

func (f *GoodsFactory) ID() string                       { return f.id }
func (f *GoodsFactory) PlayerID() int                    { return f.playerID }
func (f *GoodsFactory) TargetGood() string               { return f.targetGood }
func (f *GoodsFactory) SystemSymbol() string             { return f.systemSymbol }
func (f *GoodsFactory) DependencyTree() *SupplyChainNode { return f.dependencyTree }
func (f *GoodsFactory) Status() FactoryStatus            { return f.status }
func (f *GoodsFactory) Metadata() map[string]interface{} { return f.metadata }
func (f *GoodsFactory) QuantityAcquired() int            { return f.quantityAcquired }
func (f *GoodsFactory) TotalCost() int                   { return f.totalCost }
func (f *GoodsFactory) ShipsUsed() int                   { return f.shipsUsed }
func (f *GoodsFactory) MarketQueries() int               { return f.marketQueries }
func (f *GoodsFactory) ParallelLevels() int              { return f.parallelLevels }
func (f *GoodsFactory) EstimatedSpeedup() float64        { return f.estimatedSpeedup }

// Lifecycle timestamp accessors (delegate to lifecycle machine)

func (f *GoodsFactory) CreatedAt() time.Time  { return f.lifecycle.CreatedAt() }
func (f *GoodsFactory) UpdatedAt() time.Time  { return f.lifecycle.UpdatedAt() }
func (f *GoodsFactory) StartedAt() *time.Time { return f.lifecycle.StartedAt() }
func (f *GoodsFactory) StoppedAt() *time.Time { return f.lifecycle.StoppedAt() }
func (f *GoodsFactory) LastError() error      { return f.lifecycle.LastError() }

// State transition methods

// Start transitions factory from PENDING to ACTIVE state
func (f *GoodsFactory) Start() error {
	if f.status != FactoryStatusPending && f.status != FactoryStatusStopped {
		return &ErrInvalidFactoryState{
			CurrentState: string(f.status),
			Attempted:    "start",
		}
	}

	err := f.lifecycle.Start()
	if err != nil {
		return err
	}

	f.status = FactoryStatusActive
	return nil
}

// Complete transitions factory from ACTIVE to COMPLETED state
func (f *GoodsFactory) Complete() error {
	if f.status != FactoryStatusActive {
		return &ErrInvalidFactoryState{
			CurrentState: string(f.status),
			Attempted:    "complete",
		}
	}

	err := f.lifecycle.Complete()
	if err != nil {
		return err
	}

	f.status = FactoryStatusCompleted
	return nil
}

// Fail transitions factory to FAILED state with error
func (f *GoodsFactory) Fail(reason error) error {
	if f.status == FactoryStatusCompleted || f.status == FactoryStatusStopped {
		return &ErrInvalidFactoryState{
			CurrentState: string(f.status),
			Attempted:    "fail",
		}
	}

	err := f.lifecycle.Fail(reason)
	if err != nil {
		return err
	}

	f.status = FactoryStatusFailed
	return nil
}

// Stop transitions factory to STOPPED state
func (f *GoodsFactory) Stop() error {
	if f.status == FactoryStatusCompleted || f.status == FactoryStatusStopped {
		return &ErrInvalidFactoryState{
			CurrentState: string(f.status),
			Attempted:    "stop",
		}
	}

	err := f.lifecycle.Stop()
	if err != nil {
		return err
	}

	f.status = FactoryStatusStopped
	return nil
}

// Validation guards

// CanStart returns true if factory can be started
func (f *GoodsFactory) CanStart() bool {
	return f.status == FactoryStatusPending || f.status == FactoryStatusStopped
}

// CanComplete returns true if factory can be completed
func (f *GoodsFactory) CanComplete() bool {
	return f.status == FactoryStatusActive
}

// CanFail returns true if factory can be failed
func (f *GoodsFactory) CanFail() bool {
	return f.status != FactoryStatusCompleted && f.status != FactoryStatusStopped
}

// CanStop returns true if factory can be stopped
func (f *GoodsFactory) CanStop() bool {
	return f.status != FactoryStatusCompleted && f.status != FactoryStatusStopped
}

// State queries

// IsActive returns true if factory is currently producing
func (f *GoodsFactory) IsActive() bool {
	return f.status == FactoryStatusActive
}

// IsFinished returns true if factory has completed, failed, or stopped
func (f *GoodsFactory) IsFinished() bool {
	return f.status == FactoryStatusCompleted ||
		f.status == FactoryStatusFailed ||
		f.status == FactoryStatusStopped
}

// Metrics tracking

// SetQuantityAcquired updates the quantity acquired metric
func (f *GoodsFactory) SetQuantityAcquired(quantity int) {
	f.quantityAcquired = quantity
	f.lifecycle.UpdateTimestamp()
}

// AddCost adds to the total cost metric
func (f *GoodsFactory) AddCost(cost int) {
	f.totalCost += cost
	f.lifecycle.UpdateTimestamp()
}

// SetShipsUsed updates the ships used metric
func (f *GoodsFactory) SetShipsUsed(count int) {
	f.shipsUsed = count
	f.lifecycle.UpdateTimestamp()
}

// IncrementMarketQueries increments the market queries counter
func (f *GoodsFactory) IncrementMarketQueries() {
	f.marketQueries++
	f.lifecycle.UpdateTimestamp()
}

// SetParallelMetrics updates parallel execution metrics
func (f *GoodsFactory) SetParallelMetrics(levels int, speedup float64) {
	f.parallelLevels = levels
	f.estimatedSpeedup = speedup
	f.lifecycle.UpdateTimestamp()
}

// AverageProductionTimePerNode calculates average time per node
func (f *GoodsFactory) AverageProductionTimePerNode() time.Duration {
	total := f.TotalNodes()
	if total == 0 {
		return 0
	}

	runtime := f.RuntimeDuration()
	return runtime / time.Duration(total)
}

// EfficiencyMetrics returns various efficiency metrics
func (f *GoodsFactory) EfficiencyMetrics() map[string]interface{} {
	metrics := make(map[string]interface{})

	// Cost per unit
	if f.quantityAcquired > 0 {
		metrics["cost_per_unit"] = float64(f.totalCost) / float64(f.quantityAcquired)
	} else {
		metrics["cost_per_unit"] = 0.0
	}

	// Nodes per minute
	runtime := f.RuntimeDuration()
	if runtime > 0 {
		nodesPerMinute := float64(f.CompletedNodes()) / runtime.Minutes()
		metrics["nodes_per_minute"] = nodesPerMinute
	} else {
		metrics["nodes_per_minute"] = 0.0
	}

	// Parallel efficiency
	if f.parallelLevels > 0 {
		metrics["parallel_levels"] = f.parallelLevels
		metrics["estimated_speedup"] = f.estimatedSpeedup
	}

	// Ship utilization
	if f.shipsUsed > 0 {
		metrics["ships_used"] = f.shipsUsed
		if f.parallelLevels > 0 {
			metrics["avg_ships_per_level"] = float64(f.shipsUsed) / float64(f.parallelLevels)
		}
	}

	// Market queries
	if f.marketQueries > 0 {
		metrics["market_queries"] = f.marketQueries
		if f.TotalNodes() > 0 {
			metrics["queries_per_node"] = float64(f.marketQueries) / float64(f.TotalNodes())
		}
	}

	return metrics
}

// Metadata management

// UpdateMetadata merges new metadata into existing metadata
func (f *GoodsFactory) UpdateMetadata(updates map[string]interface{}) {
	if f.metadata == nil {
		f.metadata = make(map[string]interface{})
	}

	for key, value := range updates {
		f.metadata[key] = value
	}

	f.lifecycle.UpdateTimestamp()
}

// GetMetadataValue retrieves a specific metadata value
func (f *GoodsFactory) GetMetadataValue(key string) (interface{}, bool) {
	if f.metadata == nil {
		return nil, false
	}

	value, exists := f.metadata[key]
	return value, exists
}

// Runtime calculation

// RuntimeDuration calculates how long the factory has been/was running
func (f *GoodsFactory) RuntimeDuration() time.Duration {
	return f.lifecycle.RuntimeDuration()
}

// Tree analysis

// TotalNodes returns the total number of nodes in the dependency tree
func (f *GoodsFactory) TotalNodes() int {
	if f.dependencyTree == nil {
		return 0
	}
	return f.dependencyTree.CountNodes()
}

// CompletedNodes returns the number of completed nodes in the dependency tree
func (f *GoodsFactory) CompletedNodes() int {
	if f.dependencyTree == nil {
		return 0
	}

	count := 0
	nodes := f.dependencyTree.FlattenToList()
	for _, node := range nodes {
		if node.Completed {
			count++
		}
	}
	return count
}

// Progress returns the completion percentage (0-100)
func (f *GoodsFactory) Progress() int {
	total := f.TotalNodes()
	if total == 0 {
		return 0
	}

	completed := f.CompletedNodes()
	return (completed * 100) / total
}

// String provides human-readable representation
func (f *GoodsFactory) String() string {
	return fmt.Sprintf("GoodsFactory[%s, target=%s, status=%s, progress=%d%%]",
		f.id, f.targetGood, f.status, f.Progress())
}

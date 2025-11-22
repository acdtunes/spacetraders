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

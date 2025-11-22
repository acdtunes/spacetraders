package container

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ContainerStatus represents the lifecycle state of a container
type ContainerStatus string

const (
	// ContainerStatusPending indicates container is queued but not started
	ContainerStatusPending ContainerStatus = "PENDING"

	// ContainerStatusRunning indicates container is actively executing
	ContainerStatusRunning ContainerStatus = "RUNNING"

	// ContainerStatusCompleted indicates container finished successfully
	ContainerStatusCompleted ContainerStatus = "COMPLETED"

	// ContainerStatusFailed indicates container encountered an error
	ContainerStatusFailed ContainerStatus = "FAILED"

	// ContainerStatusStopping indicates container is gracefully shutting down
	ContainerStatusStopping ContainerStatus = "STOPPING"

	// ContainerStatusStopped indicates container was stopped by user
	ContainerStatusStopped ContainerStatus = "STOPPED"

	// ContainerStatusInterrupted indicates container was running when daemon stopped, pending recovery
	ContainerStatusInterrupted ContainerStatus = "INTERRUPTED"
)

// ContainerType categorizes the operation type
type ContainerType string

const (
	ContainerTypeNavigate                 ContainerType = "NAVIGATE"
	ContainerTypeDock                     ContainerType = "DOCK"
	ContainerTypeOrbit                    ContainerType = "ORBIT"
	ContainerTypeRefuel                   ContainerType = "REFUEL"
	ContainerTypeScout                    ContainerType = "SCOUT"
	ContainerTypeMining                   ContainerType = "MINING"
	ContainerTypeMiningWorker             ContainerType = "MINING_WORKER"
	ContainerTypeMiningCoordinator        ContainerType = "MINING_COORDINATOR"
	ContainerTypeTransportWorker          ContainerType = "TRANSPORT_WORKER"
	ContainerTypeContract                 ContainerType = "CONTRACT"
	ContainerTypeContractWorkflow         ContainerType = "CONTRACT_WORKFLOW"
	ContainerTypeContractFleetCoordinator ContainerType = "CONTRACT_FLEET_COORDINATOR"
	ContainerTypeBalancing                ContainerType = "BALANCING"
	ContainerTypeTrading                  ContainerType = "TRADING"
	ContainerTypeScoutFleetAssignment     ContainerType = "SCOUT_FLEET_ASSIGNMENT"
	ContainerTypePurchase                 ContainerType = "PURCHASE"
)

const (
	// MaxRestartAttempts defines the maximum number of automatic restart attempts
	// for a failed container. This prevents infinite restart loops while allowing
	// recovery from transient failures.
	MaxRestartAttempts = 3
)

// Container represents a background operation running in the daemon
// Containers are the unit of work orchestration - each runs in its own goroutine
// and can be started, stopped, monitored, and restarted independently.
//
// Lifecycle Integration:
// - Uses LifecycleStateMachine for core state management and timestamps
// - Adds Container-specific states (STOPPING, INTERRUPTED)
// - Adds Container-specific features (iterations, restarts, metadata)
type Container struct {
	id            string
	containerType ContainerType
	playerID      int

	// Core lifecycle managed by state machine
	lifecycle *shared.LifecycleStateMachine

	// Container-specific state extensions
	stopping    bool // Indicates STOPPING state (graceful shutdown)
	interrupted bool // Indicates INTERRUPTED state (daemon crash recovery)

	// Iteration tracking for looping operations
	currentIteration int
	maxIterations    int // -1 for infinite

	// Restart tracking
	restartCount int
	maxRestarts  int

	// Operation-specific metadata (JSON-serializable)
	metadata map[string]interface{}

	// Time provider for testability (delegated to lifecycle)
	clock shared.Clock
}

// NewContainer creates a new container instance
// If clock is nil, uses RealClock (production behavior)
func NewContainer(
	id string,
	containerType ContainerType,
	playerID int,
	maxIterations int,
	metadata map[string]interface{},
	clock shared.Clock,
) *Container {
	// Default to real clock if not provided
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &Container{
		id:               id,
		containerType:    containerType,
		playerID:         playerID,
		lifecycle:        shared.NewLifecycleStateMachine(clock),
		stopping:         false,
		interrupted:      false,
		currentIteration: 0,
		maxIterations:    maxIterations,
		restartCount:     0,
		maxRestarts:      MaxRestartAttempts,
		metadata:         metadata,
		clock:            clock,
	}
}

// Getters

func (c *Container) ID() string                       { return c.id }
func (c *Container) Type() ContainerType              { return c.containerType }
func (c *Container) PlayerID() int                    { return c.playerID }
func (c *Container) CurrentIteration() int            { return c.currentIteration }
func (c *Container) MaxIterations() int               { return c.maxIterations }
func (c *Container) RestartCount() int                { return c.restartCount }
func (c *Container) MaxRestarts() int                 { return c.maxRestarts }
func (c *Container) Metadata() map[string]interface{} { return c.metadata }

// Lifecycle timestamp accessors (delegate to lifecycle machine)

func (c *Container) CreatedAt() time.Time  { return c.lifecycle.CreatedAt() }
func (c *Container) UpdatedAt() time.Time  { return c.lifecycle.UpdatedAt() }
func (c *Container) StartedAt() *time.Time { return c.lifecycle.StartedAt() }
func (c *Container) StoppedAt() *time.Time { return c.lifecycle.StoppedAt() }
func (c *Container) LastError() error      { return c.lifecycle.LastError() }

// Status returns the current container status
// Maps LifecycleStatus to ContainerStatus with Container-specific extensions
func (c *Container) Status() ContainerStatus {
	// Check Container-specific states first
	if c.stopping {
		return ContainerStatusStopping
	}
	if c.interrupted {
		return ContainerStatusInterrupted
	}

	// Map lifecycle states to container states
	switch c.lifecycle.Status() {
	case shared.LifecycleStatusPending:
		return ContainerStatusPending
	case shared.LifecycleStatusRunning:
		return ContainerStatusRunning
	case shared.LifecycleStatusCompleted:
		return ContainerStatusCompleted
	case shared.LifecycleStatusFailed:
		return ContainerStatusFailed
	case shared.LifecycleStatusStopped:
		return ContainerStatusStopped
	default:
		return ContainerStatusPending // Safe default
	}
}

// State transition methods

// Start transitions container to RUNNING state
// Delegates to lifecycle state machine
func (c *Container) Start() error {
	status := c.Status()
	if status != ContainerStatusPending && status != ContainerStatusStopped {
		return fmt.Errorf("cannot start container in %s state", status)
	}

	// Clear Container-specific flags
	c.stopping = false
	c.interrupted = false

	return c.lifecycle.Start()
}

// Complete transitions container to COMPLETED state
// Delegates to lifecycle state machine
func (c *Container) Complete() error {
	status := c.Status()
	if status != ContainerStatusRunning {
		return fmt.Errorf("cannot complete container in %s state", status)
	}

	c.stopping = false
	return c.lifecycle.Complete()
}

// Fail transitions container to FAILED state with error
// Delegates to lifecycle state machine with error tracking
func (c *Container) Fail(err error) error {
	status := c.Status()
	if status == ContainerStatusCompleted || status == ContainerStatusStopped {
		return fmt.Errorf("cannot fail container in %s state", status)
	}

	c.stopping = false
	return c.lifecycle.Fail(err)
}

// Stop transitions container to STOPPING then STOPPED state
// STOPPING is a Container-specific state for graceful shutdown
func (c *Container) Stop() error {
	status := c.Status()
	if status == ContainerStatusCompleted || status == ContainerStatusStopped {
		return fmt.Errorf("cannot stop container in %s state", status)
	}

	// First go to STOPPING to signal graceful shutdown
	if status == ContainerStatusRunning {
		c.stopping = true
		c.lifecycle.UpdateTimestamp()
		return nil
	}

	// Then finalize to STOPPED
	c.stopping = false
	return c.lifecycle.Stop()
}

// MarkStopped finalizes the stop transition
// Transitions from STOPPING to STOPPED
func (c *Container) MarkStopped() error {
	if c.Status() != ContainerStatusStopping {
		return fmt.Errorf("cannot mark stopped when not in stopping state")
	}

	c.stopping = false
	return c.lifecycle.Stop()
}

// Iteration management

// IncrementIteration advances the iteration counter
func (c *Container) IncrementIteration() error {
	if c.Status() != ContainerStatusRunning {
		return fmt.Errorf("cannot increment iteration in %s state", c.Status())
	}

	c.currentIteration++
	c.lifecycle.UpdateTimestamp()
	return nil
}

// ShouldContinue checks if container should continue iterating
func (c *Container) ShouldContinue() bool {
	// Infinite loop: maxIterations = -1
	if c.maxIterations == -1 {
		return true
	}

	// Finite loop: check if more iterations remain
	return c.currentIteration < c.maxIterations
}

// Restart management

// CanRestart checks if container is eligible for restart
func (c *Container) CanRestart() bool {
	if c.Status() != ContainerStatusFailed {
		return false
	}

	return c.restartCount < c.maxRestarts
}

// IncrementRestartCount advances the restart counter
func (c *Container) IncrementRestartCount() {
	c.restartCount++
	c.lifecycle.UpdateTimestamp()
}

// ResetForRestart prepares container for restart attempt
// Delegates to lifecycle state machine for state reset
func (c *Container) ResetForRestart() error {
	if !c.CanRestart() {
		return fmt.Errorf("container cannot be restarted (restarts: %d/%d)",
			c.restartCount, c.maxRestarts)
	}

	c.stopping = false
	c.interrupted = false
	c.lifecycle.ResetForRestart()
	c.IncrementRestartCount()
	return nil
}

// Metadata management

// UpdateMetadata merges new metadata into existing metadata
func (c *Container) UpdateMetadata(updates map[string]interface{}) {
	if c.metadata == nil {
		c.metadata = make(map[string]interface{})
	}

	for key, value := range updates {
		c.metadata[key] = value
	}

	c.lifecycle.UpdateTimestamp()
}

// GetMetadataValue retrieves a specific metadata value
func (c *Container) GetMetadataValue(key string) (interface{}, bool) {
	if c.metadata == nil {
		return nil, false
	}

	value, exists := c.metadata[key]
	return value, exists
}

// State queries

// IsRunning returns true if container is currently executing
func (c *Container) IsRunning() bool {
	return c.Status() == ContainerStatusRunning
}

// IsFinished returns true if container has completed or failed
func (c *Container) IsFinished() bool {
	status := c.Status()
	return status == ContainerStatusCompleted ||
		status == ContainerStatusFailed ||
		status == ContainerStatusStopped
}

// IsStopping returns true if container is gracefully shutting down
func (c *Container) IsStopping() bool {
	return c.stopping
}

// Runtime calculation

// RuntimeDuration calculates how long the container has been running
// Delegates to lifecycle state machine
func (c *Container) RuntimeDuration() time.Duration {
	return c.lifecycle.RuntimeDuration()
}

// String provides human-readable representation
func (c *Container) String() string {
	return fmt.Sprintf("Container[%s, type=%s, status=%s, iteration=%d/%d, restarts=%d]",
		c.id, c.containerType, c.Status(), c.currentIteration, c.maxIterations, c.restartCount)
}

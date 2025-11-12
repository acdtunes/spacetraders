package container

import (
	"fmt"
	"time"
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
)

// ContainerType categorizes the operation type
type ContainerType string

const (
	ContainerTypeNavigate   ContainerType = "NAVIGATE"
	ContainerTypeDock       ContainerType = "DOCK"
	ContainerTypeOrbit      ContainerType = "ORBIT"
	ContainerTypeRefuel     ContainerType = "REFUEL"
	ContainerTypeScout      ContainerType = "SCOUT"
	ContainerTypeMining     ContainerType = "MINING"
	ContainerTypeContract   ContainerType = "CONTRACT"
	ContainerTypeTrading    ContainerType = "TRADING"
)

// Container represents a background operation running in the daemon
// Containers are the unit of work orchestration - each runs in its own goroutine
// and can be started, stopped, monitored, and restarted independently.
type Container struct {
	id           string
	containerType ContainerType
	status       ContainerStatus
	playerID     int

	// Iteration tracking for looping operations
	currentIteration int
	maxIterations    int // -1 for infinite

	// Restart tracking
	restartCount int
	maxRestarts  int

	// Lifecycle timestamps
	createdAt time.Time
	updatedAt time.Time
	startedAt *time.Time
	stoppedAt *time.Time

	// Operation-specific metadata (JSON-serializable)
	metadata map[string]interface{}

	// Error tracking
	lastError error
}

// NewContainer creates a new container instance
func NewContainer(
	id string,
	containerType ContainerType,
	playerID int,
	maxIterations int,
	metadata map[string]interface{},
) *Container {
	now := time.Now()
	return &Container{
		id:               id,
		containerType:    containerType,
		status:           ContainerStatusPending,
		playerID:         playerID,
		currentIteration: 0,
		maxIterations:    maxIterations,
		restartCount:     0,
		maxRestarts:      3, // Default: allow 3 restarts
		createdAt:        now,
		updatedAt:        now,
		metadata:         metadata,
	}
}

// Getters

func (c *Container) ID() string                           { return c.id }
func (c *Container) Type() ContainerType                  { return c.containerType }
func (c *Container) Status() ContainerStatus              { return c.status }
func (c *Container) PlayerID() int                        { return c.playerID }
func (c *Container) CurrentIteration() int                { return c.currentIteration }
func (c *Container) MaxIterations() int                   { return c.maxIterations }
func (c *Container) RestartCount() int                    { return c.restartCount }
func (c *Container) MaxRestarts() int                     { return c.maxRestarts }
func (c *Container) CreatedAt() time.Time                 { return c.createdAt }
func (c *Container) UpdatedAt() time.Time                 { return c.updatedAt }
func (c *Container) StartedAt() *time.Time                { return c.startedAt }
func (c *Container) StoppedAt() *time.Time                { return c.stoppedAt }
func (c *Container) Metadata() map[string]interface{}     { return c.metadata }
func (c *Container) LastError() error                     { return c.lastError }

// State transition methods

// Start transitions container to RUNNING state
func (c *Container) Start() error {
	if c.status != ContainerStatusPending && c.status != ContainerStatusStopped {
		return fmt.Errorf("cannot start container in %s state", c.status)
	}

	now := time.Now()
	c.status = ContainerStatusRunning
	c.startedAt = &now
	c.updatedAt = now
	return nil
}

// Complete transitions container to COMPLETED state
func (c *Container) Complete() error {
	if c.status != ContainerStatusRunning {
		return fmt.Errorf("cannot complete container in %s state", c.status)
	}

	now := time.Now()
	c.status = ContainerStatusCompleted
	c.stoppedAt = &now
	c.updatedAt = now
	return nil
}

// Fail transitions container to FAILED state with error
func (c *Container) Fail(err error) error {
	if c.status == ContainerStatusCompleted || c.status == ContainerStatusStopped {
		return fmt.Errorf("cannot fail container in %s state", c.status)
	}

	now := time.Now()
	c.status = ContainerStatusFailed
	c.lastError = err
	c.stoppedAt = &now
	c.updatedAt = now
	return nil
}

// Stop transitions container to STOPPING then STOPPED state
func (c *Container) Stop() error {
	if c.status == ContainerStatusCompleted || c.status == ContainerStatusStopped {
		return fmt.Errorf("cannot stop container in %s state", c.status)
	}

	now := time.Now()
	// First go to STOPPING to signal graceful shutdown
	if c.status == ContainerStatusRunning {
		c.status = ContainerStatusStopping
		c.updatedAt = now
		return nil
	}

	// Then finalize to STOPPED
	c.status = ContainerStatusStopped
	c.stoppedAt = &now
	c.updatedAt = now
	return nil
}

// MarkStopped finalizes the stop transition
func (c *Container) MarkStopped() error {
	if c.status != ContainerStatusStopping {
		return fmt.Errorf("cannot mark stopped when not in stopping state")
	}

	now := time.Now()
	c.status = ContainerStatusStopped
	c.stoppedAt = &now
	c.updatedAt = now
	return nil
}

// Iteration management

// IncrementIteration advances the iteration counter
func (c *Container) IncrementIteration() error {
	if c.status != ContainerStatusRunning {
		return fmt.Errorf("cannot increment iteration in %s state", c.status)
	}

	c.currentIteration++
	c.updatedAt = time.Now()
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
	if c.status != ContainerStatusFailed {
		return false
	}

	return c.restartCount < c.maxRestarts
}

// IncrementRestartCount advances the restart counter
func (c *Container) IncrementRestartCount() {
	c.restartCount++
	c.updatedAt = time.Now()
}

// ResetForRestart prepares container for restart attempt
func (c *Container) ResetForRestart() error {
	if !c.CanRestart() {
		return fmt.Errorf("container cannot be restarted (restarts: %d/%d)",
			c.restartCount, c.maxRestarts)
	}

	c.status = ContainerStatusPending
	c.lastError = nil
	c.stoppedAt = nil
	c.IncrementRestartCount()
	c.updatedAt = time.Now()
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

	c.updatedAt = time.Now()
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
	return c.status == ContainerStatusRunning
}

// IsFinished returns true if container has completed or failed
func (c *Container) IsFinished() bool {
	return c.status == ContainerStatusCompleted ||
		c.status == ContainerStatusFailed ||
		c.status == ContainerStatusStopped
}

// IsStopping returns true if container is gracefully shutting down
func (c *Container) IsStopping() bool {
	return c.status == ContainerStatusStopping
}

// Runtime calculation

// RuntimeDuration calculates how long the container has been running
func (c *Container) RuntimeDuration() time.Duration {
	if c.startedAt == nil {
		return 0
	}

	endTime := time.Now()
	if c.stoppedAt != nil {
		endTime = *c.stoppedAt
	}

	return endTime.Sub(*c.startedAt)
}

// String provides human-readable representation
func (c *Container) String() string {
	return fmt.Sprintf("Container[%s, type=%s, status=%s, iteration=%d/%d, restarts=%d]",
		c.id, c.containerType, c.status, c.currentIteration, c.maxIterations, c.restartCount)
}

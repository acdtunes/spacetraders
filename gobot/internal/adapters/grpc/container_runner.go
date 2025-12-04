package grpc

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ContainerRunner executes a container operation in a background goroutine
// Manages the lifecycle of a single container including error handling and restarts
type ContainerRunner struct {
	containerEntity *container.Container
	mediator        common.Mediator
	command         interface{} // The command to execute (must implement mediator request)
	logRepo         persistence.ContainerLogRepository
	containerRepo   *persistence.ContainerRepositoryGORM
	shipRepo        navigation.ShipRepository
	clock           shared.Clock

	// Execution control
	ctx        context.Context
	cancelFunc context.CancelFunc
	done       chan struct{}
	mu         sync.RWMutex

	// Heartbeat control
	heartbeatStop chan struct{}  // Signal to stop heartbeat goroutine
	heartbeatDone chan struct{}  // Signal that heartbeat goroutine has stopped
	heartbeatOnce sync.Once      // Ensures heartbeat is only stopped once

	// Event publisher for completion notifications
	// Publishes WorkerCompletedEvent when container completes or fails
	eventPublisher navigation.ShipEventPublisher

	// In-memory log cache for quick access (logs also persisted to DB)
	logs []LogEntry
}

// LogEntry represents a single log message from a container
type LogEntry struct {
	Timestamp time.Time
	Level     string
	Message   string
	Metadata  map[string]interface{}
}

// NewContainerRunner creates a new container runner
func NewContainerRunner(
	containerEntity *container.Container,
	mediator common.Mediator,
	command interface{},
	logRepo persistence.ContainerLogRepository,
	containerRepo *persistence.ContainerRepositoryGORM,
	shipRepo navigation.ShipRepository,
	clock shared.Clock,
) *ContainerRunner {
	ctx, cancel := context.WithCancel(context.Background())
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &ContainerRunner{
		containerEntity:    containerEntity,
		mediator:           mediator,
		command:            command,
		logRepo:            logRepo,
		containerRepo:      containerRepo,
		shipRepo:           shipRepo,
		clock:              clock,
		ctx:                ctx,
		cancelFunc:         cancel,
		done:          make(chan struct{}),
		heartbeatStop: make(chan struct{}),
		heartbeatDone: make(chan struct{}),
		logs:          make([]LogEntry, 0),
	}
}

// SetEventPublisher sets the event publisher for completion notifications.
// This should be called before Start().
func (r *ContainerRunner) SetEventPublisher(publisher navigation.ShipEventPublisher) {
	r.eventPublisher = publisher
}

// Container returns the underlying container entity
func (r *ContainerRunner) Container() *container.Container {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.containerEntity
}

// Start begins container execution
func (r *ContainerRunner) Start() error {
	r.mu.Lock()
	if err := r.containerEntity.Start(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	r.log("INFO", "Container started", nil)

	// Persist status update to database (RUNNING)
	if r.containerRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := r.containerRepo.UpdateStatus(
			ctx,
			r.containerEntity.ID(),
			r.containerEntity.PlayerID(),
			container.ContainerStatusRunning,
			nil,
			nil,
			"",
		); err != nil {
			r.log("ERROR", fmt.Sprintf("Failed to persist RUNNING status: %v", err), nil)
		}
	}

	// Create ship assignments if this container uses ships
	// This prevents concurrent containers from operating on the same ship
	if err := r.createShipAssignments(); err != nil {
		r.log("ERROR", fmt.Sprintf("Failed to create ship assignments: %v", err), nil)
		return fmt.Errorf("failed to create ship assignments: %w", err)
	}

	// Start heartbeat goroutine to update heartbeat_at periodically
	// This allows detection of crashed containers that don't update their heartbeat
	go r.runHeartbeat()

	// Execute the container operation
	go r.execute()

	return nil
}

// Stop gracefully stops the container
func (r *ContainerRunner) Stop() error {
	r.mu.Lock()
	if err := r.containerEntity.Stop(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	r.log("INFO", "Container stopping...", nil)

	// Stop the heartbeat goroutine
	r.stopHeartbeat()

	// Cancel context to signal stop
	r.cancelFunc()

	// Wait for completion (with timeout)
	select {
	case <-r.done:
		r.log("INFO", "Container stopped gracefully", nil)
	case <-time.After(10 * time.Second):
		r.log("WARNING", "Container did not stop within timeout", nil)
	}

	r.mu.Lock()
	r.containerEntity.MarkStopped()
	r.mu.Unlock()

	// Persist STOPPED status to database
	if r.containerRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		now := time.Now()
		if err := r.containerRepo.UpdateStatus(
			ctx,
			r.containerEntity.ID(),
			r.containerEntity.PlayerID(),
			container.ContainerStatusStopped,
			&now,      // stoppedAt
			nil,       // exitCode (nil for graceful stop)
			"stopped", // exitReason
		); err != nil {
			r.log("ERROR", fmt.Sprintf("Failed to persist STOPPED status: %v", err), nil)
		}
	}

	// Release ship assignments for this container
	r.releaseShipAssignments("stopped")

	return nil
}

// runHeartbeat periodically updates the container's heartbeat timestamp
// This allows detection of crashed containers that stop updating their heartbeat
func (r *ContainerRunner) runHeartbeat() {
	defer close(r.heartbeatDone)

	// Update heartbeat every 30 seconds
	// Stale timeout is 2 minutes, so 30s gives us 4 heartbeats before considered stale
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.heartbeatStop:
			return
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			if r.containerRepo != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := r.containerRepo.UpdateContainerHeartbeat(ctx, r.containerEntity.ID()); err != nil {
					// Log but don't fail - heartbeat is best-effort
					r.log("WARN", fmt.Sprintf("Failed to update heartbeat: %v", err), nil)
				}
				cancel()
			}
		}
	}
}

// stopHeartbeat stops the heartbeat goroutine (safe to call multiple times)
func (r *ContainerRunner) stopHeartbeat() {
	r.heartbeatOnce.Do(func() {
		close(r.heartbeatStop)
		// Wait for heartbeat goroutine to finish (with timeout)
		select {
		case <-r.heartbeatDone:
			// Heartbeat goroutine stopped
		case <-time.After(2 * time.Second):
			// Timeout waiting for heartbeat to stop
		}
	})
}

// execute runs the container operation loop
func (r *ContainerRunner) execute() {
	defer close(r.done)

	// Add startup jitter to spread out API calls and prevent thundering herd
	// when multiple containers start simultaneously (0-5 seconds)
	jitter := time.Duration(rand.Intn(5000)) * time.Millisecond
	r.log("INFO", fmt.Sprintf("Startup jitter: waiting %v before first API call", jitter), nil)

	select {
	case <-time.After(jitter):
		// Jitter complete, continue to execution
	case <-r.ctx.Done():
		// Context canceled during jitter, exit immediately
		r.log("INFO", "Context canceled during startup jitter", nil)
		return
	}

	// Iteration loop (supports multi-iteration operations like scout tours)
	for {
		// Check if we should continue
		r.mu.RLock()
		shouldContinue := r.containerEntity.ShouldContinue()
		isStopping := r.containerEntity.IsStopping()
		r.mu.RUnlock()

		if !shouldContinue || isStopping {
			break
		}

		// Execute single iteration
		if err := r.executeIteration(); err != nil {
			// Check if error is due to context cancellation (shutdown signal)
			// Don't retry on context cancellation - exit immediately
			if r.ctx.Err() != nil {
				r.log("INFO", "Context canceled, stopping container", nil)
				// Signal completion on context cancellation (graceful shutdown)
				r.signalCompletion()
				r.releaseShipAssignments("canceled")
				return
			}

			r.handleError(err)

			// Check if we should retry
			r.mu.RLock()
			canRestart := r.containerEntity.CanRestart()
			r.mu.RUnlock()

			if canRestart {
				r.log("INFO", fmt.Sprintf("Retrying after error (attempt %d)",
					r.containerEntity.RestartCount()+1), nil)
				r.mu.Lock()
				r.containerEntity.ResetForRestart()
				r.containerEntity.Start()
				r.mu.Unlock()

				// Record restart metrics
				metrics.RecordContainerRestart(r.containerEntity)

				continue // DON'T signal completion - we're restarting
			}

			// UNRECOVERABLE ERROR: Only NOW do we signal completion and release ships
			// This is the critical fix - completion is signaled AFTER restart decision
			r.log("INFO", "Container failed with unrecoverable error, signaling completion", nil)
			r.signalCompletionWithStatus(false, err.Error())
			r.releaseShipAssignments("failed")
			return // Exit on unrecoverable error
		}

		// Increment iteration counter
		r.mu.Lock()
		r.containerEntity.IncrementIteration()
		r.mu.Unlock()

		// Record iteration metrics
		metrics.RecordContainerIteration(r.containerEntity)

		r.log("INFO", fmt.Sprintf("Iteration %d completed",
			r.containerEntity.CurrentIteration()), nil)

		// Check for stop signal
		select {
		case <-r.ctx.Done():
			r.log("INFO", "Stop signal received", nil)
			return
		default:
			// Continue to next iteration
		}
	}

	// Stop heartbeat before marking as completed
	r.stopHeartbeat()

	// Mark as completed
	r.mu.Lock()
	r.containerEntity.Complete()
	r.mu.Unlock()

	// Record completion metrics
	metrics.RecordContainerCompletion(r.containerEntity)

	r.log("INFO", "Container completed successfully", map[string]interface{}{
		"iterations": r.containerEntity.CurrentIteration(),
		"runtime":    r.containerEntity.RuntimeDuration().String(),
	})

	// Persist completion to database
	if r.containerRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		now := time.Now()
		exitCode := 0

		if err := r.containerRepo.UpdateStatus(
			ctx,
			r.containerEntity.ID(),
			r.containerEntity.PlayerID(),
			container.ContainerStatusCompleted,
			&now,
			&exitCode,
			"",
		); err != nil {
			r.log("ERROR", fmt.Sprintf("Failed to persist COMPLETED status: %v", err), nil)
		}
	}

	// CRITICAL: Release ship assignments BEFORE signaling completion
	// This prevents race condition where coordinator discovers ship before it's released
	// causing "ship already assigned to container" errors
	r.releaseShipAssignments("completed")

	// Signal completion to coordinator (if callback set)
	// Now safe to signal - ship is fully released
	r.signalCompletion()
}

// signalCompletion signals container completion via event bus.
func (r *ContainerRunner) signalCompletion() {
	r.signalCompletionWithStatus(true, "")
}

// signalCompletionWithStatus signals container completion with success status and error message via event bus.
func (r *ContainerRunner) signalCompletionWithStatus(success bool, errMsg string) {
	if r.eventPublisher == nil {
		return
	}

	metadata := r.containerEntity.Metadata()

	// Extract ship symbol from container metadata
	shipSymbol, ok := metadata["ship_symbol"].(string)
	if !ok {
		r.log("WARNING", "No ship_symbol in metadata, cannot signal completion", nil)
		return
	}

	coordinatorID, _ := metadata["coordinator_id"].(string)

	event := navigation.WorkerCompletedEvent{
		ContainerID:   r.containerEntity.ID(),
		PlayerID:      r.containerEntity.PlayerID(),
		ShipSymbol:    shipSymbol,
		CoordinatorID: coordinatorID,
		Success:       success,
		Error:         errMsg,
	}

	r.eventPublisher.PublishWorkerCompleted(event)
	r.log("INFO", fmt.Sprintf("Published completion event for ship %s (success=%t)", shipSymbol, success), nil)
}

// executeIteration executes a single iteration of the container operation
func (r *ContainerRunner) executeIteration() error {
	r.log("INFO", "Executing iteration", map[string]interface{}{
		"iteration": r.containerEntity.CurrentIteration() + 1,
	})

	// Add logger to context so handlers can log
	ctxWithLogger := common.WithLogger(r.ctx, r)

	// Execute command via mediator
	result, err := r.mediator.Send(ctxWithLogger, r.command)
	if err != nil {
		return fmt.Errorf("command execution failed: %w", err)
	}

	// Log command result
	r.log("INFO", fmt.Sprintf("Command executed, result type: %T", result), nil)

	return nil
}

// handleError handles execution errors
// NOTE: This does NOT signal completion or release ships - that's done by the caller
// AFTER determining whether to restart. This prevents premature ship release before restart.
func (r *ContainerRunner) handleError(err error) {
	r.log("ERROR", err.Error(), nil)

	r.mu.Lock()
	r.containerEntity.Fail(err)
	r.mu.Unlock()

	// Record failure metrics
	metrics.RecordContainerCompletion(r.containerEntity)

	// Persist failure to database
	if r.containerRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		now := time.Now()
		exitCode := 1

		if dbErr := r.containerRepo.UpdateStatus(
			ctx,
			r.containerEntity.ID(),
			r.containerEntity.PlayerID(),
			container.ContainerStatusFailed,
			&now,
			&exitCode,
			err.Error(),
		); dbErr != nil {
			r.log("ERROR", fmt.Sprintf("Failed to persist FAILED status: %v", dbErr), nil)
		}
	}

	// NOTE: signalCompletion and releaseShipAssignments are NOT called here.
	// They are called by execute() ONLY when the container is truly done (not restarting).
	// This prevents the bug where completion is signaled before restart decision.
}

// Logging

// Log adds a log entry (implements common.ContainerLogger interface)
func (r *ContainerRunner) Log(level, message string, metadata map[string]interface{}) {
	r.mu.Lock()
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
		Metadata:  metadata,
	}

	// Add to in-memory cache
	r.logs = append(r.logs, entry)
	r.mu.Unlock()

	// Print to stdout
	fmt.Printf("[%s] [%s] %s: %s\n",
		entry.Timestamp.Format(time.RFC3339),
		r.containerEntity.ID(),
		level,
		message,
	)

	// Persist to database (async to avoid blocking)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := r.logRepo.Log(ctx, r.containerEntity.ID(), r.containerEntity.PlayerID(), message, level, metadata); err != nil {
			// Log error to stdout if DB write fails (but don't block execution)
			fmt.Printf("[%s] [%s] ERROR: Failed to persist log to DB: %v\n",
				time.Now().Format(time.RFC3339),
				r.containerEntity.ID(),
				err,
			)
		}
	}()
}

// log is a lowercase alias for backward compatibility with existing code
func (r *ContainerRunner) log(level, message string, metadata map[string]interface{}) {
	r.Log(level, message, metadata)
}

// GetLogs returns all logs for this container
func (r *ContainerRunner) GetLogs(limit *int, level *string) []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Filter logs
	filtered := make([]LogEntry, 0)
	for _, log := range r.logs {
		if level != nil && log.Level != *level {
			continue
		}
		filtered = append(filtered, log)
	}

	// Apply limit
	if limit != nil && *limit < len(filtered) {
		// Return most recent logs
		start := len(filtered) - *limit
		return filtered[start:]
	}

	return filtered
}

// createShipAssignments creates ship assignments from container metadata
// Checks for "ship_symbol" (single ship) in the metadata map
// This prevents concurrent containers from operating on the same ship
func (r *ContainerRunner) createShipAssignments() error {
	if r.shipRepo == nil {
		return nil
	}

	metadata := r.containerEntity.Metadata()

	// Check for single ship
	if shipSymbol, ok := metadata["ship_symbol"].(string); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		playerID := shared.MustNewPlayerID(r.containerEntity.PlayerID())
		ship, err := r.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
		}

		// Check if ship is already assigned to THIS container (recovered container)
		if ship.IsAssigned() && ship.ContainerID() == r.containerEntity.ID() {
			r.log("INFO", fmt.Sprintf("Ship %s already assigned to this container (recovered)", shipSymbol), nil)
			return nil
		}

		// Assign ship to container using Ship aggregate
		if err := ship.AssignToContainer(r.containerEntity.ID(), r.clock); err != nil {
			return fmt.Errorf("failed to assign ship %s: %w", shipSymbol, err)
		}

		if err := r.shipRepo.Save(ctx, ship); err != nil {
			return fmt.Errorf("failed to persist ship %s assignment: %w", shipSymbol, err)
		}

		r.log("INFO", fmt.Sprintf("Assigned ship %s to container", shipSymbol), nil)
	}

	// No ship_symbol in config = no ships to assign (e.g., scout-fleet-assignment)
	return nil
}

// releaseShipAssignments releases all ship assignments for this container
func (r *ContainerRunner) releaseShipAssignments(reason string) {
	if r.shipRepo == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	playerID := shared.MustNewPlayerID(r.containerEntity.PlayerID())
	assignedShips, err := r.shipRepo.FindByContainer(ctx, r.containerEntity.ID(), playerID)
	if err != nil {
		r.log("ERROR", fmt.Sprintf("Failed to find ships for container: %v", err), nil)
		return
	}

	for _, ship := range assignedShips {
		ship.ForceRelease(reason, r.clock)
		if err := r.shipRepo.Save(ctx, ship); err != nil {
			r.log("ERROR", fmt.Sprintf("Failed to release ship %s: %v", ship.ShipSymbol(), err), nil)
		}
	}

	if len(assignedShips) > 0 {
		r.log("INFO", fmt.Sprintf("Released %d ship assignments (reason: %s)", len(assignedShips), reason), nil)
	}
}

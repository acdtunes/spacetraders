package grpc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
)

// ContainerRunner executes a container operation in a background goroutine
// Manages the lifecycle of a single container including error handling and restarts
type ContainerRunner struct {
	containerEntity    *container.Container
	mediator           common.Mediator
	command            interface{} // The command to execute (must implement mediator request)
	logRepo            persistence.ContainerLogRepository
	containerRepo      *persistence.ContainerRepositoryGORM
	shipAssignmentRepo *persistence.ShipAssignmentRepositoryGORM

	// Execution control
	ctx        context.Context
	cancelFunc context.CancelFunc
	done       chan struct{}
	mu         sync.RWMutex

	// Completion callback (optional)
	// Called when container completes or fails, passing ship symbol if available
	completionCallback chan<- string

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
	shipAssignmentRepo *persistence.ShipAssignmentRepositoryGORM,
) *ContainerRunner {
	ctx, cancel := context.WithCancel(context.Background())

	return &ContainerRunner{
		containerEntity:    containerEntity,
		mediator:           mediator,
		command:            command,
		logRepo:            logRepo,
		containerRepo:      containerRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		ctx:                ctx,
		cancelFunc:         cancel,
		done:               make(chan struct{}),
		completionCallback: nil, // Can be set later via SetCompletionCallback
		logs:               make([]LogEntry, 0),
	}
}

// SetCompletionCallback sets the completion callback channel
// This should be called before Start() if a callback is needed
func (r *ContainerRunner) SetCompletionCallback(callback chan<- string) {
	r.completionCallback = callback
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

// execute runs the container operation loop
func (r *ContainerRunner) execute() {
	defer close(r.done)

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
				continue
			}

			return // Exit on unrecoverable error
		}

		// Increment iteration counter
		r.mu.Lock()
		r.containerEntity.IncrementIteration()
		r.mu.Unlock()

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

	// Mark as completed
	r.mu.Lock()
	r.containerEntity.Complete()
	r.mu.Unlock()

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

	// Signal completion BEFORE releasing (so coordinator can atomically transfer ship)
	r.signalCompletion()

	// Release ship assignments for this container
	// NOTE: If completion callback is set, coordinator handles the transfer
	// Otherwise, release normally
	if r.completionCallback == nil {
		r.releaseShipAssignments("completed")
	}
	// If callback is set, coordinator is responsible for transferring the ship back
}

// signalCompletion signals container completion via callback channel
func (r *ContainerRunner) signalCompletion() {
	if r.completionCallback == nil {
		return // No callback configured
	}

	// Extract ship symbol from container metadata
	shipSymbol, ok := r.containerEntity.Metadata()["ship_symbol"].(string)
	if !ok {
		r.log("WARNING", "No ship_symbol in metadata, cannot signal completion", nil)
		return
	}

	// Send signal (non-blocking)
	select {
	case r.completionCallback <- shipSymbol:
		r.log("INFO", fmt.Sprintf("Signaled completion for ship %s", shipSymbol), nil)
	default:
		r.log("WARNING", fmt.Sprintf("Completion callback channel full/closed for ship %s", shipSymbol), nil)
	}
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
func (r *ContainerRunner) handleError(err error) {
	r.log("ERROR", err.Error(), nil)

	r.mu.Lock()
	r.containerEntity.Fail(err)
	r.mu.Unlock()

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

	// Signal completion BEFORE releasing (even on failure)
	r.signalCompletion()

	// Release ship assignments for this container
	// NOTE: If completion callback is set, coordinator handles the transfer
	if r.completionCallback == nil {
		r.releaseShipAssignments("failed")
	}
	// If callback is set, coordinator is responsible for handling the ship
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

		if err := r.logRepo.Log(ctx, r.containerEntity.ID(), r.containerEntity.PlayerID(), message, level); err != nil {
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
	if r.shipAssignmentRepo == nil {
		return nil
	}

	metadata := r.containerEntity.Metadata()

	// Check for single ship
	if shipSymbol, ok := metadata["ship_symbol"].(string); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Check if assignment already exists (for recovered containers)
		existingAssignment, err := r.shipAssignmentRepo.FindByShip(ctx, shipSymbol, r.containerEntity.PlayerID())
		if err != nil {
			return fmt.Errorf("failed to check existing assignment: %w", err)
		}

		// If assignment exists for THIS container, skip creation (recovered container)
		if existingAssignment != nil && existingAssignment.ContainerID() == r.containerEntity.ID() {
			r.log("INFO", fmt.Sprintf("Ship %s already assigned to this container (recovered)", shipSymbol), nil)
			return nil
		}

		// Create ship assignment
		assignment := daemon.NewShipAssignment(
			shipSymbol,
			r.containerEntity.PlayerID(),
			r.containerEntity.ID(),
			nil,
		)

		if err := r.shipAssignmentRepo.Insert(ctx, assignment); err != nil {
			return fmt.Errorf("failed to assign ship %s: %w", shipSymbol, err)
		}

		r.log("INFO", fmt.Sprintf("Assigned ship %s to container", shipSymbol), nil)
	}

	// No ship_symbol in config = no ships to assign (e.g., scout-fleet-assignment)
	return nil
}

// releaseShipAssignments releases all ship assignments for this container
func (r *ContainerRunner) releaseShipAssignments(reason string) {
	if r.shipAssignmentRepo == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := r.shipAssignmentRepo.ReleaseByContainer(
		ctx,
		r.containerEntity.ID(),
		r.containerEntity.PlayerID(),
		reason,
	)

	if err != nil {
		r.log("ERROR", fmt.Sprintf("Failed to release ship assignments: %v", err), nil)
	} else {
		r.log("INFO", fmt.Sprintf("Released ship assignments (reason: %s)", reason), nil)
	}
}

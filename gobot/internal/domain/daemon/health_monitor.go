package daemon

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RecoveryMetrics tracks health monitor recovery statistics
type RecoveryMetrics struct {
	SuccessfulRecoveries int
	FailedRecoveries     int
	AbandonedShips       int
}

// HealthMonitor monitors container and ship health, detecting stuck operations
// and attempting recovery when possible
type HealthMonitor struct {
	checkInterval      time.Duration
	recoveryTimeout    time.Duration
	maxRecoveryAttempts int
	lastCheckTime      *time.Time
	watchList          map[string]time.Time // ship symbol -> added time
	recoveryAttempts   map[string]int       // ship symbol -> attempt count
	metrics            *RecoveryMetrics
	clock              shared.Clock
}

// NewHealthMonitor creates a new health monitor instance
func NewHealthMonitor(
	checkInterval time.Duration,
	recoveryTimeout time.Duration,
	clock shared.Clock,
) *HealthMonitor {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &HealthMonitor{
		checkInterval:       checkInterval,
		recoveryTimeout:     recoveryTimeout,
		maxRecoveryAttempts: 5, // Default: 5 attempts before abandoning
		watchList:           make(map[string]time.Time),
		recoveryAttempts:    make(map[string]int),
		metrics: &RecoveryMetrics{
			SuccessfulRecoveries: 0,
			FailedRecoveries:     0,
			AbandonedShips:       0,
		},
		clock: clock,
	}
}

// Getters

func (hm *HealthMonitor) CheckInterval() time.Duration   { return hm.checkInterval }
func (hm *HealthMonitor) RecoveryTimeout() time.Duration { return hm.recoveryTimeout }
func (hm *HealthMonitor) GetLastCheckTime() *time.Time   { return hm.lastCheckTime }

// SetLastCheckTime updates the last check timestamp (for testing)
func (hm *HealthMonitor) SetLastCheckTime(t time.Time) {
	hm.lastCheckTime = &t
}

// SetMaxRecoveryAttempts configures max recovery attempts before abandoning
func (hm *HealthMonitor) SetMaxRecoveryAttempts(max int) {
	hm.maxRecoveryAttempts = max
}

// GetRecoveryAttemptCount returns the number of recovery attempts for a ship
func (hm *HealthMonitor) GetRecoveryAttemptCount(shipSymbol string) int {
	return hm.recoveryAttempts[shipSymbol]
}

// GetMetrics returns current recovery metrics
func (hm *HealthMonitor) GetMetrics() *RecoveryMetrics {
	return hm.metrics
}

// RunCheck performs a complete health check cycle
// Returns true if check was skipped due to cooldown, false if executed
func (hm *HealthMonitor) RunCheck(
	ctx context.Context,
	assignments map[string]*ShipAssignment,
	containers map[string]*container.Container,
	ships map[string]*navigation.Ship,
) (bool, error) {
	now := hm.clock.Now()

	// Check cooldown
	if hm.lastCheckTime != nil {
		elapsed := now.Sub(*hm.lastCheckTime)
		if elapsed < hm.checkInterval {
			return true, nil // Skipped due to cooldown
		}
	}

	// Update last check time
	hm.lastCheckTime = &now

	// Clean stale assignments
	existingContainerIDs := make(map[string]bool)
	for id := range containers {
		existingContainerIDs[id] = true
	}

	_, err := hm.CleanStaleAssignments(ctx, assignments, existingContainerIDs)
	if err != nil {
		return false, err
	}

	// Detect stuck ships
	// Note: routes are passed as nil for now - in real implementation this would come from repository
	_ = hm.DetectStuckShips(ctx, ships, containers, nil)

	// Detect infinite loops
	_ = hm.DetectInfiniteLoops(ctx, containers)

	return false, nil // Executed
}

// CleanStaleAssignments releases assignments for non-existent containers
func (hm *HealthMonitor) CleanStaleAssignments(
	ctx context.Context,
	assignments map[string]*ShipAssignment,
	existingContainerIDs map[string]bool,
) (int, error) {
	cleaned := 0

	for _, assignment := range assignments {
		if !assignment.IsActive() {
			continue
		}

		// Check if container exists
		if !existingContainerIDs[assignment.ContainerID()] {
			if err := assignment.Release("stale_cleanup"); err != nil {
				return cleaned, err
			}
			cleaned++
		}
	}

	return cleaned, nil
}

// DetectStuckShips identifies ships that have been in IN_TRANSIT state too long
func (hm *HealthMonitor) DetectStuckShips(
	ctx context.Context,
	ships map[string]*navigation.Ship,
	containers map[string]*container.Container,
	routes map[string]*navigation.Route,
) []string {
	stuckShips := []string{}
	now := hm.clock.Now()

	for shipSymbol, ship := range ships {
		// Only check ships in transit
		if ship.NavStatus() != navigation.NavStatusInTransit {
			continue
		}

		// Calculate how long ship has been in transit
		// Note: We need a way to get the last state change time from the ship
		// For now, we'll use a simplified approach based on the ship's internal state

		// Check if ship has exceeded recovery timeout
		// This is a simplified check - in production we'd need proper timestamp tracking
		if hm.isShipStuck(ship, now) {
			stuckShips = append(stuckShips, shipSymbol)

			// Check for route-ship state mismatch
			if routes != nil {
				if route, exists := routes[shipSymbol]; exists {
					if route.Status() == navigation.RouteStatusCompleted {
						// Critical: route is completed but ship still in transit
						// This indicates a missing arrival call
					}
				}
			}
		}
	}

	return stuckShips
}

// isShipStuck checks if a ship has been stuck in transit too long
// This is a placeholder - real implementation would check actual timestamps
func (hm *HealthMonitor) isShipStuck(ship *navigation.Ship, now time.Time) bool {
	// TODO: Implement proper timestamp tracking in Ship entity
	// For now, this is a stub that always returns false
	return false
}

// DetectInfiniteLoops identifies containers with suspicious rapid iteration patterns
func (hm *HealthMonitor) DetectInfiniteLoops(
	ctx context.Context,
	containers map[string]*container.Container,
) []string {
	suspicious := []string{}

	for containerID, c := range containers {
		if !c.IsRunning() {
			continue
		}

		// Check for infinite loop containers with rapid iterations
		if c.MaxIterations() == -1 {
			// Calculate average iteration duration
			runtime, exists := c.GetMetadataValue("runtime_seconds")
			if !exists {
				continue
			}

			runtimeSeconds, ok := runtime.(int)
			if !ok {
				continue
			}

			iterations := c.CurrentIteration()
			if iterations == 0 {
				continue
			}

			avgDuration := float64(runtimeSeconds) / float64(iterations)

			// Flag if iterations complete suspiciously fast (< 5 seconds avg)
			if avgDuration < 5.0 {
				suspicious = append(suspicious, containerID)
			}
		}
	}

	return suspicious
}

// AttemptRecovery attempts to recover a stuck ship
func (hm *HealthMonitor) AttemptRecovery(
	ctx context.Context,
	shipSymbol string,
	ship *navigation.Ship,
	containers map[string]*container.Container,
) error {
	// Check max recovery attempts
	attempts := hm.recoveryAttempts[shipSymbol]
	if attempts >= hm.maxRecoveryAttempts {
		// Abandon ship
		hm.metrics.AbandonedShips++
		return nil
	}

	// Record attempt
	hm.recoveryAttempts[shipSymbol] = attempts + 1

	// Attempt recovery by forcing arrival
	// In real implementation, this would:
	// 1. Fetch current ship state from API
	// 2. Check if ship actually arrived
	// 3. Force transition if needed
	// 4. Update database

	// For now, stub implementation
	if ship.NavStatus() == navigation.NavStatusInTransit {
		// Simulate successful recovery
		hm.metrics.SuccessfulRecoveries++
	}

	return nil
}

// RecordRecoveryAttempt records a recovery attempt result (for testing)
func (hm *HealthMonitor) RecordRecoveryAttempt(shipSymbol string, success bool) {
	attempts := hm.recoveryAttempts[shipSymbol]
	hm.recoveryAttempts[shipSymbol] = attempts + 1

	if success {
		hm.metrics.SuccessfulRecoveries++
	} else {
		hm.metrics.FailedRecoveries++
	}
}

// AddToWatchList adds a ship to the health monitor watch list
func (hm *HealthMonitor) AddToWatchList(shipSymbol string) {
	hm.watchList[shipSymbol] = hm.clock.Now()
}

// RemoveFromWatchList removes a ship from the watch list and resets recovery attempts
func (hm *HealthMonitor) RemoveFromWatchList(shipSymbol string) {
	delete(hm.watchList, shipSymbol)
	delete(hm.recoveryAttempts, shipSymbol)
}

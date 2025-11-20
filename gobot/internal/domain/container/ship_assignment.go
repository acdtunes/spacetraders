package container

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// AssignmentStatus represents the state of a ship assignment
type AssignmentStatus string

const (
	// AssignmentStatusActive indicates ship is currently assigned and locked
	AssignmentStatusActive AssignmentStatus = "active"

	// AssignmentStatusReleased indicates ship has been released from assignment
	AssignmentStatusReleased AssignmentStatus = "released"
)

// ShipAssignment represents a ship being assigned to a container operation
// This provides ship-level locking to prevent concurrent operations on the same ship
type ShipAssignment struct {
	shipSymbol    string
	playerID      int
	containerID   string
	status        AssignmentStatus
	assignedAt    time.Time
	releasedAt    *time.Time
	releaseReason *string
	clock         shared.Clock
}

// NewShipAssignment creates a new active ship assignment
func NewShipAssignment(
	shipSymbol string,
	playerID int,
	containerID string,
	clock shared.Clock,
) *ShipAssignment {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &ShipAssignment{
		shipSymbol:  shipSymbol,
		playerID:    playerID,
		containerID: containerID,
		status:      AssignmentStatusActive,
		assignedAt:  clock.Now(),
		clock:       clock,
	}
}

// Getters

func (sa *ShipAssignment) ShipSymbol() string           { return sa.shipSymbol }
func (sa *ShipAssignment) PlayerID() int                { return sa.playerID }
func (sa *ShipAssignment) ContainerID() string          { return sa.containerID }
func (sa *ShipAssignment) Status() AssignmentStatus     { return sa.status }
func (sa *ShipAssignment) AssignedAt() time.Time        { return sa.assignedAt }
func (sa *ShipAssignment) ReleasedAt() *time.Time       { return sa.releasedAt }
func (sa *ShipAssignment) ReleaseReason() *string       { return sa.releaseReason }

// Release marks the assignment as released with a reason
func (sa *ShipAssignment) Release(reason string) error {
	if sa.status == AssignmentStatusReleased {
		return fmt.Errorf("assignment already released")
	}

	now := sa.clock.Now()
	sa.status = AssignmentStatusReleased
	sa.releasedAt = &now
	sa.releaseReason = &reason

	return nil
}

// ForceRelease forcefully releases the assignment regardless of current state
// Used for cleaning up stale assignments
func (sa *ShipAssignment) ForceRelease(reason string) error {
	now := sa.clock.Now()
	sa.status = AssignmentStatusReleased
	sa.releasedAt = &now
	sa.releaseReason = &reason

	return nil
}

// IsStale checks if the assignment is older than the given timeout duration
func (sa *ShipAssignment) IsStale(timeout time.Duration) bool {
	if sa.status == AssignmentStatusReleased {
		return false
	}

	age := sa.clock.Now().Sub(sa.assignedAt)
	return age > timeout
}

// IsActive returns true if the assignment is currently active
func (sa *ShipAssignment) IsActive() bool {
	return sa.status == AssignmentStatusActive
}

// String provides human-readable representation
func (sa *ShipAssignment) String() string {
	return fmt.Sprintf("ShipAssignment[ship=%s, container=%s, status=%s]",
		sa.shipSymbol, sa.containerID, sa.status)
}

// ShipAssignmentManager manages ship assignments and enforces locking
type ShipAssignmentManager struct {
	assignments map[string]*ShipAssignment // key: shipSymbol
	clock       shared.Clock
}

// NewShipAssignmentManager creates a new ship assignment manager
func NewShipAssignmentManager(clock shared.Clock) *ShipAssignmentManager {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &ShipAssignmentManager{
		assignments: make(map[string]*ShipAssignment),
		clock:       clock,
	}
}

// AssignShip assigns a ship to a container operation
// Returns error if ship is already assigned to another container
func (sam *ShipAssignmentManager) AssignShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	containerID string,
) (*ShipAssignment, error) {
	// Check if ship is already assigned
	if existing, exists := sam.assignments[shipSymbol]; exists {
		if existing.IsActive() {
			return nil, fmt.Errorf("ship is already assigned to another container")
		}
	}

	// Create new assignment
	assignment := NewShipAssignment(shipSymbol, playerID, containerID, sam.clock)
	sam.assignments[shipSymbol] = assignment

	return assignment, nil
}

// GetAssignment retrieves the current assignment for a ship
func (sam *ShipAssignmentManager) GetAssignment(shipSymbol string) (*ShipAssignment, bool) {
	assignment, exists := sam.assignments[shipSymbol]
	return assignment, exists
}

// ReleaseAssignment releases a ship from its current assignment
func (sam *ShipAssignmentManager) ReleaseAssignment(shipSymbol string, reason string) error {
	assignment, exists := sam.assignments[shipSymbol]
	if !exists {
		return fmt.Errorf("no assignment found for ship %s", shipSymbol)
	}

	return assignment.Release(reason)
}

// ReleaseAll releases all active assignments with the given reason
func (sam *ShipAssignmentManager) ReleaseAll(reason string) error {
	for _, assignment := range sam.assignments {
		if assignment.IsActive() {
			if err := assignment.Release(reason); err != nil {
				return err
			}
		}
	}
	return nil
}

// CleanOrphanedAssignments releases assignments for non-existent containers
func (sam *ShipAssignmentManager) CleanOrphanedAssignments(
	existingContainerIDs map[string]bool,
) (int, error) {
	cleaned := 0

	for _, assignment := range sam.assignments {
		if !assignment.IsActive() {
			continue
		}

		// Check if container exists
		if !existingContainerIDs[assignment.ContainerID()] {
			if err := assignment.Release("orphaned_cleanup"); err != nil {
				return cleaned, err
			}
			cleaned++
		}
	}

	return cleaned, nil
}

// CleanStaleAssignments releases assignments older than the timeout
func (sam *ShipAssignmentManager) CleanStaleAssignments(timeout time.Duration) (int, error) {
	cleaned := 0

	for _, assignment := range sam.assignments {
		if !assignment.IsActive() {
			continue
		}

		if assignment.IsStale(timeout) {
			if err := assignment.ForceRelease("stale_timeout"); err != nil {
				return cleaned, err
			}
			cleaned++
		}
	}

	return cleaned, nil
}

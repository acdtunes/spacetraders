package container

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// AssignmentStatus represents the state of a ship assignment
type AssignmentStatus string

const (
	// AssignmentStatusActive indicates ship is currently assigned and locked
	AssignmentStatusActive AssignmentStatus = "active"

	// AssignmentStatusIdle indicates ship has been released from assignment
	AssignmentStatusIdle AssignmentStatus = "idle"
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

func (sa *ShipAssignment) ShipSymbol() string       { return sa.shipSymbol }
func (sa *ShipAssignment) PlayerID() int            { return sa.playerID }
func (sa *ShipAssignment) ContainerID() string      { return sa.containerID }
func (sa *ShipAssignment) Status() AssignmentStatus { return sa.status }
func (sa *ShipAssignment) AssignedAt() time.Time    { return sa.assignedAt }
func (sa *ShipAssignment) ReleasedAt() *time.Time   { return sa.releasedAt }
func (sa *ShipAssignment) ReleaseReason() *string   { return sa.releaseReason }

// Release marks the assignment as idle with a reason
func (sa *ShipAssignment) Release(reason string) error {
	if sa.status == AssignmentStatusIdle {
		return fmt.Errorf("assignment already idle")
	}

	sa.markReleased(reason)
	return nil
}

// ForceRelease forcefully releases the assignment regardless of current state
// Used for cleaning up stale assignments
func (sa *ShipAssignment) ForceRelease(reason string) error {
	sa.markReleased(reason)
	return nil
}

// markReleased clears the container reference and stamps the release metadata.
func (sa *ShipAssignment) markReleased(reason string) {
	now := sa.clock.Now()
	sa.status = AssignmentStatusIdle
	sa.containerID = ""
	sa.releasedAt = &now
	sa.releaseReason = &reason
}

func (sa *ShipAssignment) IsStale(timeout time.Duration) bool {
	if sa.status == AssignmentStatusIdle {
		return false
	}

	age := sa.clock.Now().Sub(sa.assignedAt)
	return age > timeout
}

func (sa *ShipAssignment) IsActive() bool {
	return sa.status == AssignmentStatusActive
}

func (sa *ShipAssignment) String() string {
	return fmt.Sprintf("ShipAssignment[ship=%s, container=%s, status=%s]",
		sa.shipSymbol, sa.containerID, sa.status)
}

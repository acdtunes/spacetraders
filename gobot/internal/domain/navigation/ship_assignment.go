package navigation

import "time"

// AssignmentStatus represents the state of a ship assignment
type AssignmentStatus string

const (
	// AssignmentStatusActive indicates ship is currently assigned and working
	AssignmentStatusActive AssignmentStatus = "active"

	// AssignmentStatusIdle indicates ship is available for assignment
	AssignmentStatusIdle AssignmentStatus = "idle"
)

// ShipAssignment is a value object representing a ship's current container assignment.
// It is owned by the Ship aggregate and managed through Ship methods.
//
// Value Object Properties:
// - Immutable - operations return new instances
// - No identity - compared by attributes
// - Lifecycle bound to Ship aggregate
type ShipAssignment struct {
	containerID   string
	status        AssignmentStatus
	assignedAt    time.Time
	releasedAt    *time.Time
	releaseReason *string
}

// NewActiveAssignment creates a new active assignment to a container
func NewActiveAssignment(containerID string, assignedAt time.Time) *ShipAssignment {
	return &ShipAssignment{
		containerID: containerID,
		status:      AssignmentStatusActive,
		assignedAt:  assignedAt,
	}
}

// NewIdleAssignment creates a new idle (unassigned) state
func NewIdleAssignment() *ShipAssignment {
	return &ShipAssignment{
		status: AssignmentStatusIdle,
	}
}

// ReconstructAssignment reconstructs a ShipAssignment from persistence data.
// This is used by repositories to hydrate the value object from database records.
func ReconstructAssignment(
	containerID string,
	status AssignmentStatus,
	assignedAt time.Time,
	releasedAt *time.Time,
	releaseReason *string,
) *ShipAssignment {
	return &ShipAssignment{
		containerID:   containerID,
		status:        status,
		assignedAt:    assignedAt,
		releasedAt:    releasedAt,
		releaseReason: releaseReason,
	}
}

// Getters - value objects expose their data through getters

func (a *ShipAssignment) ContainerID() string      { return a.containerID }
func (a *ShipAssignment) Status() AssignmentStatus { return a.status }
func (a *ShipAssignment) AssignedAt() time.Time    { return a.assignedAt }
func (a *ShipAssignment) ReleasedAt() *time.Time   { return a.releasedAt }
func (a *ShipAssignment) ReleaseReason() *string   { return a.releaseReason }

// IsActive returns true if the ship is currently assigned to a container
func (a *ShipAssignment) IsActive() bool {
	return a.status == AssignmentStatusActive
}

// IsIdle returns true if the ship is not assigned to any container
func (a *ShipAssignment) IsIdle() bool {
	return a.status == AssignmentStatusIdle
}

// Released returns a new ShipAssignment in idle state with release metadata.
// Value objects are immutable, so this returns a new instance.
func (a *ShipAssignment) Released(reason string, releasedAt time.Time) *ShipAssignment {
	return &ShipAssignment{
		containerID:   "",
		status:        AssignmentStatusIdle,
		assignedAt:    a.assignedAt,
		releasedAt:    &releasedAt,
		releaseReason: &reason,
	}
}

// TransferredTo returns a new ShipAssignment for a different container.
// Preserves active status but updates container and assignment time.
func (a *ShipAssignment) TransferredTo(newContainerID string, transferredAt time.Time) *ShipAssignment {
	return &ShipAssignment{
		containerID: newContainerID,
		status:      AssignmentStatusActive,
		assignedAt:  transferredAt,
	}
}

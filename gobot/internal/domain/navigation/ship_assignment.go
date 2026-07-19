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

// AssignmentOwner distinguishes who holds an active assignment: a coordinator
// container, or the captain directly.
type AssignmentOwner string

const (
	// AssignmentOwnerContainer indicates a coordinator container holds the
	// assignment. This is the default owner when none is recorded.
	AssignmentOwnerContainer AssignmentOwner = "container"

	// AssignmentOwnerCaptain indicates the captain holds the assignment directly
	// for manual, hands-on use — invisible to coordinator discovery.
	AssignmentOwnerCaptain AssignmentOwner = "captain"
)

// ShipAssignment is a value object representing a ship's current container assignment.
// It is owned by the Ship aggregate and managed through Ship methods.
//
// Value Object Properties:
// - Immutable - operations return new instances
// - No identity - compared by attributes
// - Lifecycle bound to Ship aggregate
type ShipAssignment struct {
	containerID       string
	status            AssignmentStatus
	assignedAt        time.Time
	releasedAt        *time.Time
	releaseReason     *string
	owner             AssignmentOwner
	reservationReason *string
}

func NewActiveAssignment(containerID string, assignedAt time.Time) *ShipAssignment {
	return &ShipAssignment{
		containerID: containerID,
		status:      AssignmentStatusActive,
		assignedAt:  assignedAt,
		owner:       AssignmentOwnerContainer,
	}
}

func NewIdleAssignment() *ShipAssignment {
	return &ShipAssignment{
		status: AssignmentStatusIdle,
	}
}

// NewCaptainReservation creates a new active assignment held by the captain
// directly rather than a container. It intentionally leaves containerID empty
// — a captain reservation is never a container claim, so it is invisible to
// every container-keyed lookup by construction. An empty reason is
// stored as no reason (nil), not an empty string.
func NewCaptainReservation(reason string, reservedAt time.Time) *ShipAssignment {
	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	return &ShipAssignment{
		status:            AssignmentStatusActive,
		owner:             AssignmentOwnerCaptain,
		assignedAt:        reservedAt,
		reservationReason: reasonPtr,
	}
}

func ReconstructAssignment(
	containerID string,
	status AssignmentStatus,
	assignedAt time.Time,
	releasedAt *time.Time,
	releaseReason *string,
	owner AssignmentOwner,
	reservationReason *string,
) *ShipAssignment {
	return &ShipAssignment{
		containerID:       containerID,
		status:            status,
		assignedAt:        assignedAt,
		releasedAt:        releasedAt,
		releaseReason:     releaseReason,
		owner:             owner,
		reservationReason: reservationReason,
	}
}

// Getters - value objects expose their data through getters

func (a *ShipAssignment) ContainerID() string        { return a.containerID }
func (a *ShipAssignment) Status() AssignmentStatus   { return a.status }
func (a *ShipAssignment) AssignedAt() time.Time      { return a.assignedAt }
func (a *ShipAssignment) ReleasedAt() *time.Time     { return a.releasedAt }
func (a *ShipAssignment) ReleaseReason() *string     { return a.releaseReason }
func (a *ShipAssignment) Owner() AssignmentOwner     { return a.owner }
func (a *ShipAssignment) ReservationReason() *string { return a.reservationReason }

func (a *ShipAssignment) IsActive() bool {
	return a.status == AssignmentStatusActive
}

func (a *ShipAssignment) IsIdle() bool {
	return a.status == AssignmentStatusIdle
}

// IsCaptainReservation returns true if this is an active assignment held by
// the captain directly, rather than any coordinator container.
func (a *ShipAssignment) IsCaptainReservation() bool {
	return a.status == AssignmentStatusActive && a.owner == AssignmentOwnerCaptain
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
// Preserves active status but updates container and assignment time. Not
// reachable for captain reservations: ReserveByCaptain already refuses to
// reserve a ship that is assigned to anything, so no container can ever
// transfer-claim a captain-reserved ship.
func (a *ShipAssignment) TransferredTo(newContainerID string, transferredAt time.Time) *ShipAssignment {
	return &ShipAssignment{
		containerID: newContainerID,
		status:      AssignmentStatusActive,
		assignedAt:  transferredAt,
		owner:       AssignmentOwnerContainer,
	}
}

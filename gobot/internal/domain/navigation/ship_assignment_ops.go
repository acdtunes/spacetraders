package navigation

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Assignment Management
//
// These methods manage the ship's container assignment state.
// Assignments are persisted to database via ShipRepository.Save().

// Assignment returns the ship's current assignment (may be nil if never assigned)
func (s *Ship) Assignment() *ShipAssignment {
	return s.assignment
}

// IsIdle returns true if the ship is available for assignment
// A ship is idle if it has no assignment or its assignment is in idle state
func (s *Ship) IsIdle() bool {
	return s.assignment == nil || s.assignment.IsIdle()
}

func (s *Ship) IsAssigned() bool {
	return s.assignment != nil && s.assignment.IsActive()
}

// ContainerID returns the ID of the container this ship is assigned to
// Returns empty string if ship is not assigned
func (s *Ship) ContainerID() string {
	if s.assignment == nil {
		return ""
	}
	return s.assignment.ContainerID()
}

// ReleasedWithinBy reports whether the ship is IDLE because an operation releasing with reason
// handed it back less than window ago. IsAssigned() covers a hull mid-job; this covers the gap
// either side of it, where an operation working a hull BETWEEN legs is indistinguishable from one
// that never held it. The window keeps this a HAND-BACK rather than a second ownership claim: it
// lapses on its own, so an operation that stopped working cannot hold a hull it is not using. A
// release timestamped in the future (clock skew) counts as inside it — the safe direction.
func (s *Ship) ReleasedWithinBy(reason string, now time.Time, window time.Duration) bool {
	if s.assignment == nil || !s.assignment.IsIdle() {
		return false
	}
	if r := s.assignment.ReleaseReason(); r == nil || *r != reason {
		return false
	}
	releasedAt := s.assignment.ReleasedAt()
	if releasedAt == nil {
		return false
	}
	return now.Sub(*releasedAt) < window
}

// AssignToContainer assigns the ship to a container operation.
// Returns a *shared.ShipAlreadyAssignedError if the ship is already assigned to
// another container, or a *shared.ShipReservedByCaptainError if the captain has
// reserved the ship for direct manual use — coordinators must not
// silently steal a captain reservation. The two are distinct types on purpose:
// the already-assigned case can be a transient claim-handoff race that clears
// on a brief retry, whereas a captain reservation is a standing
// rejection the caller must honour immediately.
func (s *Ship) AssignToContainer(containerID string, clock shared.Clock) error {
	if s.IsAssigned() {
		if s.assignment.IsCaptainReservation() {
			return shared.NewShipReservedByCaptainError(s.shipSymbol, s.CaptainReservationReason())
		}
		return shared.NewShipAlreadyAssignedError(s.shipSymbol, s.assignment.ContainerID())
	}

	s.assignment = NewActiveAssignment(containerID, clock.Now())
	return nil
}

// ReserveByCaptain reserves the ship for the captain's direct manual use,
// hiding it from every coordinator's assignment discovery. A captain
// reservation is modeled as an active assignment owned by the captain instead
// of a container — it is therefore already invisible to every coordinator
// claim path through the exact same IsAssigned() check they use today
// (AssignToContainer, and any coordinator that mirrors it), so no coordinator
// needs to change.
func (s *Ship) ReserveByCaptain(reason string, clock shared.Clock) error {
	if s.IsAssigned() {
		if s.assignment.IsCaptainReservation() {
			return fmt.Errorf("ship %s is already reserved by the captain", s.shipSymbol)
		}
		return fmt.Errorf("ship %s is already assigned to container %s",
			s.shipSymbol, s.assignment.ContainerID())
	}

	s.assignment = NewCaptainReservation(reason, clock.Now())
	return nil
}

// ReleaseCaptainReservation clears a captain reservation, returning the ship
// to idle so normal coordinator discovery can claim it again. Returns a
// ShipNotReservedError if the ship is not currently reserved by the captain
// — release is specifically for captain reservations, not a generic
// "clear any assignment" escape hatch.
func (s *Ship) ReleaseCaptainReservation(reason string, clock shared.Clock) error {
	if !s.IsReservedByCaptain() {
		return shared.NewShipNotReservedError(s.shipSymbol)
	}

	s.assignment = s.assignment.Released(reason, clock.Now())
	return nil
}

// IsReservedByCaptain returns true if the ship's active assignment is a
// captain reservation rather than a container claim.
func (s *Ship) IsReservedByCaptain() bool {
	return s.assignment != nil && s.assignment.IsCaptainReservation()
}

// CaptainReservationReason returns the free-text reason given at reserve
// time, or "" if the ship is not captain-reserved or no reason was given.
func (s *Ship) CaptainReservationReason() string {
	if !s.IsReservedByCaptain() {
		return ""
	}
	if r := s.assignment.ReservationReason(); r != nil {
		return *r
	}
	return ""
}

// Release releases the ship from its current assignment.
// Returns error if ship is not currently assigned.
func (s *Ship) Release(reason string, clock shared.Clock) error {
	if !s.IsAssigned() {
		return fmt.Errorf("ship %s is not assigned to any container", s.shipSymbol)
	}

	s.assignment = s.assignment.Released(reason, clock.Now())
	return nil
}

// ForceRelease forcefully releases the ship regardless of current state.
// Used for cleanup operations (e.g., daemon restart).
func (s *Ship) ForceRelease(reason string, clock shared.Clock) {
	if s.assignment == nil {
		s.assignment = NewIdleAssignment()
		return
	}

	s.assignment = s.assignment.Released(reason, clock.Now())
}

// TransferToContainer transfers the ship to a different container.
// Returns error if ship is not currently assigned.
func (s *Ship) TransferToContainer(newContainerID string, clock shared.Clock) error {
	if !s.IsAssigned() {
		return fmt.Errorf("ship %s is not assigned to any container", s.shipSymbol)
	}

	s.assignment = s.assignment.TransferredTo(newContainerID, clock.Now())
	return nil
}

// SetAssignment sets the ship's assignment state directly.
// Used by repositories when loading from database.
// NOTE: Prefer using AssignToContainer/Release for domain operations.
func (s *Ship) SetAssignment(assignment *ShipAssignment) {
	s.assignment = assignment
}

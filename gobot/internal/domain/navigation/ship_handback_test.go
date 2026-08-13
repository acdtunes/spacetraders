package navigation_test

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const handbackReason = "construction_supply_complete"

var handbackNow = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

// idleAfterRelease is the state an operation leaves a hull in when it hands the claim back.
func idleAfterRelease(reason string, ago time.Duration) *navigation.ShipAssignment {
	releasedAt := handbackNow.Add(-ago)
	return navigation.ReconstructAssignment(
		"", navigation.AssignmentStatusIdle, releasedAt.Add(-time.Hour), &releasedAt, &reason,
		navigation.AssignmentOwnerContainer, nil,
	)
}

// ReleasedWithinBy is the "just handed back by that operation" signal: the only thing that
// distinguishes a hull an operation is between legs with from one nobody is using, once the claim
// has dropped and no fleet tag was ever written.
func TestShip_ReleasedWithinBy(t *testing.T) {
	tests := []struct {
		name       string
		assignment *navigation.ShipAssignment
		want       bool
	}{
		{"released by that operation moments ago", idleAfterRelease(handbackReason, 30*time.Second), true},
		{"released by that operation long ago", idleAfterRelease(handbackReason, 2*time.Hour), false},
		{"released by a different operation", idleAfterRelease("contract_delivery_complete", 30*time.Second), false},
		{"never assigned", navigation.NewIdleAssignment(), false},
		// Clock skew must not free a hull: the safe direction is to leave it with the operation
		// that last worked it.
		{"released in the future", idleAfterRelease(handbackReason, -time.Minute), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ship := newReservationTestShip(t, "TORWIND-8")
			ship.SetAssignment(tc.assignment)
			if got := ship.ReleasedWithinBy(handbackReason, handbackNow, time.Hour); got != tc.want {
				t.Fatalf("ReleasedWithinBy = %v, want %v", got, tc.want)
			}
		})
	}
}

// An ACTIVE assignment is never a hand-back: the hull is working, not waiting to be re-dispatched.
func TestShip_ReleasedWithinBy_ActiveAssignmentIsNeverAHandback(t *testing.T) {
	ship := newReservationTestShip(t, "TORWIND-8")
	if err := ship.AssignToContainer("C-1", &shared.MockClock{CurrentTime: handbackNow}); err != nil {
		t.Fatalf("assign to container: %v", err)
	}
	if ship.ReleasedWithinBy(handbackReason, handbackNow, time.Hour) {
		t.Fatal("a hull holding a live claim must not read as handed back")
	}
}

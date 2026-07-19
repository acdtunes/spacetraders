package navigation_test

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newReservationTestShip builds a plain idle ship for captain-reservation tests.
func newReservationTestShip(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(80, 100)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	location, err := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		location,
		fuel,
		100,
		40,
		cargo,
		9,
		"FRAME_HAULER",
		"HAULER",
		nil,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// Reserving an idle hull for the captain makes it both "assigned" (so every
// coordinator's existing IsAssigned() guard already skips it) and identifiable
// as a captain reservation specifically, carrying the given reason.
func TestReserveByCaptain_MarksShipAssignedAndReservedWithReason(t *testing.T) {
	ship := newReservationTestShip(t, "ENDURANCE-1")
	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}

	err := ship.ReserveByCaptain("manual gate-supply errand", clock)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !ship.IsAssigned() {
		t.Fatalf("expected ship to be assigned after captain reservation")
	}
	if !ship.IsReservedByCaptain() {
		t.Fatalf("expected ship to be reported as reserved by the captain")
	}
	if got := ship.CaptainReservationReason(); got != "manual gate-supply errand" {
		t.Fatalf("expected reservation reason %q, got %q", "manual gate-supply errand", got)
	}
}

// This is the load-bearing proof point: a captain reservation must be invisible
// to coordinator discovery via the EXACT SAME mechanism every coordinator already
// uses today (AssignToContainer's IsAssigned() guard) — no coordinator code
// changes are required.
func TestReserveByCaptain_BlocksCoordinatorAssignToContainer(t *testing.T) {
	ship := newReservationTestShip(t, "ENDURANCE-1")
	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}
	if err := ship.ReserveByCaptain("manual errand", clock); err != nil {
		t.Fatalf("ReserveByCaptain: %v", err)
	}

	err := ship.AssignToContainer("trade-route-ENDURANCE-1-abc123", clock)

	if err == nil {
		t.Fatalf("expected coordinator claim to be rejected on a captain-reserved ship")
	}
	if !ship.IsReservedByCaptain() {
		t.Fatalf("expected the captain reservation to remain intact after a rejected claim attempt")
	}
	if ship.ContainerID() != "" {
		t.Fatalf("expected no container to have claimed the ship, got %q", ship.ContainerID())
	}
}

// A ship a coordinator already claims cannot be captain-reserved out from under
// it — reservation must not silently steal an active container claim.
func TestReserveByCaptain_RejectsWhenAlreadyAssignedToContainer(t *testing.T) {
	ship := newReservationTestShip(t, "ENDURANCE-1")
	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}
	if err := ship.AssignToContainer("trade-route-ENDURANCE-1-abc123", clock); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}

	err := ship.ReserveByCaptain("manual errand", clock)

	if err == nil {
		t.Fatalf("expected reservation to be rejected when ship is already claimed by a container")
	}
	if ship.ContainerID() != "trade-route-ENDURANCE-1-abc123" {
		t.Fatalf("expected the existing container claim to remain intact, got %q", ship.ContainerID())
	}
}

// Reserving an already-captain-reserved ship is rejected with a clear,
// distinguishable error rather than silently overwriting the existing
// reservation's reason.
func TestReserveByCaptain_RejectsWhenAlreadyReservedByCaptain(t *testing.T) {
	ship := newReservationTestShip(t, "ENDURANCE-1")
	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}
	if err := ship.ReserveByCaptain("first errand", clock); err != nil {
		t.Fatalf("ReserveByCaptain: %v", err)
	}

	err := ship.ReserveByCaptain("second errand", clock)

	if err == nil {
		t.Fatalf("expected re-reserving an already-reserved ship to fail")
	}
	if got := ship.CaptainReservationReason(); got != "first errand" {
		t.Fatalf("expected original reason preserved, got %q", got)
	}
}

// Releasing clears the captain reservation and returns the ship to idle so the
// exact same coordinator discovery mechanism can claim it again.
func TestReleaseCaptainReservation_ReturnsShipToIdleAndAllowsContainerClaim(t *testing.T) {
	ship := newReservationTestShip(t, "ENDURANCE-1")
	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}
	if err := ship.ReserveByCaptain("manual errand", clock); err != nil {
		t.Fatalf("ReserveByCaptain: %v", err)
	}

	err := ship.ReleaseCaptainReservation("errand complete", clock)
	if err != nil {
		t.Fatalf("expected no error releasing a captain reservation, got: %v", err)
	}

	if ship.IsReservedByCaptain() {
		t.Fatalf("expected captain reservation cleared")
	}
	if ship.IsAssigned() {
		t.Fatalf("expected ship to be idle after release")
	}
	if err := ship.AssignToContainer("mfg-coordinator-ENDURANCE-1-def456", clock); err != nil {
		t.Fatalf("expected coordinator claim to succeed after release, got: %v", err)
	}
}

// Releasing a ship that isn't captain-reserved (idle, or claimed by a container)
// is rejected — `ship release` must not silently no-op on the wrong hull.
func TestReleaseCaptainReservation_RejectsWhenNotReserved(t *testing.T) {
	ship := newReservationTestShip(t, "ENDURANCE-1")
	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}

	err := ship.ReleaseCaptainReservation("oops", clock)

	if err == nil {
		t.Fatalf("expected releasing an idle (non-reserved) ship to fail")
	}
}

// Releasing a ship that a container currently owns must also fail — release is
// specifically for captain reservations, not a generic "clear any assignment"
// escape hatch.
func TestReleaseCaptainReservation_RejectsWhenAssignedToContainer(t *testing.T) {
	ship := newReservationTestShip(t, "ENDURANCE-1")
	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}
	if err := ship.AssignToContainer("trade-route-ENDURANCE-1-abc123", clock); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}

	err := ship.ReleaseCaptainReservation("oops", clock)

	if err == nil {
		t.Fatalf("expected releasing a container-claimed ship via captain release to fail")
	}
	if ship.ContainerID() != "trade-route-ENDURANCE-1-abc123" {
		t.Fatalf("expected the container claim to remain intact, got %q", ship.ContainerID())
	}
}

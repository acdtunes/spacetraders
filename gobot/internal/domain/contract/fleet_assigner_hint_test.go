package contract

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// assignmentFor returns the target waypoint assigned to shipSymbol, or "" if the
// ship was left unassigned.
func assignmentFor(assignments []Assignment, shipSymbol string) string {
	for _, a := range assignments {
		if a.ShipSymbol == shipSymbol {
			return a.TargetWaypoint
		}
	}
	return ""
}

// hintTestWaypoint builds a market waypoint at (x,y).
func hintTestWaypoint(t *testing.T, symbol string, x, y float64) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, x, y)
	if err != nil {
		t.Fatalf("NewWaypoint(%s): %v", symbol, err)
	}
	return wp
}

// sp-1ef0 assigner pin A1: a high-confidence pre-position hint biases the idle hull
// toward the predicted next-source market EVEN THOUGH pure distance round-robin would
// send it to a nearer market. This is the whole feature: closer to the next source
// when the delivery starts.
func TestAssignShipsToTargetsWithHint_HighConfidence_PrePositionsIdleHull(t *testing.T) {
	assigner := NewFleetAssigner()

	// Idle hull at origin. NEAR market is the distance-optimal pick; FAR market is
	// the predicted next source.
	ship := newSelectorTestShipWithHull(t, "HAULER-1", "HAULER", 0, 0, 30, 40)
	near := hintTestWaypoint(t, "X1-SYS-NEAR", 100, 0)
	far := hintTestWaypoint(t, "X1-SYS-FAR", 900, 0)

	hint := PrePositionHint{TargetWaypoint: "X1-SYS-FAR", Confidence: 1.0, Threshold: 0.8}

	assignments, err := assigner.AssignShipsToTargetsWithHint(
		[]*navigation.Ship{ship},
		[]*shared.Waypoint{near, far},
		hint,
	)
	if err != nil {
		t.Fatalf("AssignShipsToTargetsWithHint: %v", err)
	}

	if got := assignmentFor(assignments, "HAULER-1"); got != "X1-SYS-FAR" {
		t.Errorf("idle hull assigned to %q, want X1-SYS-FAR (high-confidence pre-position)", got)
	}
}

// sp-1ef0 assigner pin A2 (the guard): a sub-threshold hint must be ignored - the
// hull falls straight through to distance round-robin and goes to the NEAR market.
// Low confidence => no wasted move.
func TestAssignShipsToTargetsWithHint_LowConfidence_FallsBackToDistance(t *testing.T) {
	assigner := NewFleetAssigner()

	ship := newSelectorTestShipWithHull(t, "HAULER-1", "HAULER", 0, 0, 30, 40)
	near := hintTestWaypoint(t, "X1-SYS-NEAR", 100, 0)
	far := hintTestWaypoint(t, "X1-SYS-FAR", 900, 0)

	hint := PrePositionHint{TargetWaypoint: "X1-SYS-FAR", Confidence: 0.5, Threshold: 0.8}

	assignments, err := assigner.AssignShipsToTargetsWithHint(
		[]*navigation.Ship{ship},
		[]*shared.Waypoint{near, far},
		hint,
	)
	if err != nil {
		t.Fatalf("AssignShipsToTargetsWithHint: %v", err)
	}

	if got := assignmentFor(assignments, "HAULER-1"); got != "X1-SYS-NEAR" {
		t.Errorf("idle hull assigned to %q, want X1-SYS-NEAR (low-confidence hint must be ignored)", got)
	}
}

// sp-1ef0 assigner pin A3: an empty hint leaves the legacy distance behavior exactly
// as-is (the new path must not perturb the no-signal case).
func TestAssignShipsToTargetsWithHint_EmptyHint_DistanceUnchanged(t *testing.T) {
	assigner := NewFleetAssigner()

	ship := newSelectorTestShipWithHull(t, "HAULER-1", "HAULER", 0, 0, 30, 40)
	near := hintTestWaypoint(t, "X1-SYS-NEAR", 100, 0)
	far := hintTestWaypoint(t, "X1-SYS-FAR", 900, 0)

	assignments, err := assigner.AssignShipsToTargetsWithHint(
		[]*navigation.Ship{ship},
		[]*shared.Waypoint{near, far},
		PrePositionHint{},
	)
	if err != nil {
		t.Fatalf("AssignShipsToTargetsWithHint: %v", err)
	}

	if got := assignmentFor(assignments, "HAULER-1"); got != "X1-SYS-NEAR" {
		t.Errorf("idle hull assigned to %q, want X1-SYS-NEAR (empty hint => distance only)", got)
	}
}

package contract

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Observability (sp-4a4e): the coordinator's "Ship selection completed" log named
// only the winner, so a 746-unit pick was impossible to diagnose - was the closer
// command ship even in the pool? The candidate summary must enumerate every
// candidate with its distance to the target and mark the command ship, e.g.
// "TORWIND-3@0.00, TORWIND-1@50.00(command)".
func TestSummarizeCandidates_EnumeratesEveryCandidateWithDistanceMarkingCommand(t *testing.T) {
	target, err := shared.NewWaypoint("X1-TW-MKT", 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	hauler := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 0, 0)     // at target -> 0.00
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 30, 40) // 3-4-5 -> 50.00, command

	summary := summarizeCandidates([]*navigation.Ship{hauler, command}, target)

	if !strings.Contains(summary, "TORWIND-3@0.00") {
		t.Fatalf("candidate summary %q must list the hauler with its distance", summary)
	}
	if !strings.Contains(summary, "TORWIND-1@50.00(command)") {
		t.Fatalf("candidate summary %q must list the command ship with its distance and a (command) mark", summary)
	}
}

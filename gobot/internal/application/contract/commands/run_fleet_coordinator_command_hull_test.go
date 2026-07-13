package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// hasRegularHaulerCandidate computes the sp-sqq5 last-resort verdict the main
// loop feeds into spawnContractWorker's commandDraftAllowed guard
// (run_fleet_coordinator.go:717) - the claim-side backstop that refuses to
// draft an undedicated command frigate whenever a regular hauler was among
// this pass's discovered candidates. It is the ONE piece of the two-layer
// sp-sqq5 fix with no coverage anywhere else: the Layer 1 tests
// (ship_pool_manager_test.go) exercise FindIdleLightHaulers only, and the
// Layer 2 tests (spawn_contract_worker_pin_test.go) hand-pick
// commandDraftAllowed as a literal true/false rather than deriving it from
// real candidates. A regression here (inverted condition, wrong role check)
// would silently defeat the claim-side backstop while every other sp-sqq5
// test stayed green - this closes that gap directly against the pure
// function.
func TestHasRegularHaulerCandidate(t *testing.T) {
	hauler := newHaulerShip(t, "TORWIND-3", "")
	otherHauler := newHaulerShip(t, "TORWIND-4", "")
	command := newCommandFrigateTestShip(t)

	tests := []struct {
		name       string
		candidates []*navigation.Ship
		want       bool
	}{
		{
			name:       "empty candidate list has no regular hauler",
			candidates: nil,
			want:       false,
		},
		{
			name:       "command frigate only - no regular hauler present",
			candidates: []*navigation.Ship{command},
			want:       false,
		},
		{
			name:       "regular hauler only",
			candidates: []*navigation.Ship{hauler},
			want:       true,
		},
		{
			name:       "command frigate alongside a regular hauler - hauler makes the frigate not last-resort",
			candidates: []*navigation.Ship{command, hauler},
			want:       true,
		},
		{
			name:       "regular hauler listed after the command frigate - order must not matter",
			candidates: []*navigation.Ship{command, otherHauler, hauler},
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasRegularHaulerCandidate(tt.candidates)
			if got != tt.want {
				t.Fatalf("hasRegularHaulerCandidate() = %v, want %v", got, tt.want)
			}
		})
	}
}

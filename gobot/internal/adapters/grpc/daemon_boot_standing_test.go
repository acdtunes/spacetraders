package grpc

import (
	"testing"

	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// sp-382j: Admiral-selected launch model (a) — the construction-supply drain must be a STANDING
// coordinator launched unconditionally at every daemon boot (like GoodsFactoryCoordinator /
// StartGoodsFactory and the other standing coordinators), not merely bootstrap-EnsureRunning-only.
// Before this, with no bootstrapper ever having run, the ConstructionCoordinator never started
// even once, so RecoverRunningContainers (which only re-adopts containers already PERSISTED as
// RUNNING) found nothing to recover — a live gate-construction pipeline could sit unsupplied
// forever with the drain never having run a single tick.

func TestDaemonBoot_LaunchesConstructionCoordinatorStanding(t *testing.T) {
	// (a) the coordinator must be declared in the boot standing set, so daemon Start() launches it
	// unconditionally rather than depending on a bootstrapper ever having run.
	found := false
	for _, ct := range bootStandingCoordinatorTypes {
		if ct == container.ContainerTypeConstructionCoordinator {
			found = true
		}
	}
	if !found {
		t.Fatalf("bootStandingCoordinatorTypes must include ConstructionCoordinator, got %v", bootStandingCoordinatorTypes)
	}

	// (b) launched with iterations=-1 (standing: loops forever inside Handle, never bounded) — the
	// same infinite-drain default the bootstrap gate's EnsureRunning launch already relies on, so
	// the boot-standing launch and the bootstrap-gate launch agree on the same defaulting path.
	s := newFactoryTestServer()
	built, err := s.buildCommandForType("construction_coordinator", map[string]interface{}{
		"container_id": "boot-standing-test",
	}, 1, "boot-standing-test")
	if err != nil {
		t.Fatalf("buildCommandForType(construction_coordinator) failed: %v", err)
	}
	cmd, ok := built.(*goodsCmd.RunConstructionCoordinatorCommand)
	if !ok {
		t.Fatalf("expected *RunConstructionCoordinatorCommand, got %T", built)
	}
	if cmd.MaxIterations != -1 {
		t.Fatalf("boot-standing launch must default to MaxIterations=-1 (infinite drain loop), got %d", cmd.MaxIterations)
	}
}

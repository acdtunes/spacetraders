package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// daemon_boot_standing_relocator_test.go — the ARMING test for the opportunity relocator
// (sp-zvywu Part 2).
//
// WHY THIS TEST EXISTS. "Closed is not armed" is a repeat failure on this fleet: a coordinator can be
// fully built, registered with the mediator, restart-recoverable and covered by tests, and still
// never run a single tick because nothing calls its launcher. That is not a partial delivery, it is a
// silent zero — and it looks exactly like a working feature from every angle except the fleet's
// behaviour. The relocator is required to ship ARMED, so its membership in the boot-standing set is
// itself a behaviour worth pinning.

func TestDaemonBoot_LaunchesTheOpportunityRelocatorStanding(t *testing.T) {
	// (a) It must be declared boot-standing, so daemon Start() launches it unconditionally rather than
	// waiting for an arming step, a bootstrapper phase, or an operator verb that nobody will run.
	found := false
	for _, ct := range bootStandingCoordinatorTypes {
		if ct == container.ContainerTypeOpportunityRelocator {
			found = true
		}
	}
	if !found {
		t.Fatalf("bootStandingCoordinatorTypes must include the OpportunityRelocator, or it never runs a single tick however complete the engine is; got %v", bootStandingCoordinatorTypes)
	}

	// (b) The launch must build a command whose knobs are all left at their documented defaults
	// (RULINGS #5) — identity only. An all-zero knob set is what proves the reconciler resolves its own
	// thresholds and caps rather than depending on launch-time config that a boot-standing launch has
	// no way to supply.
	built, err := newFactoryTestServer().buildCommandForType("opportunity_relocator", map[string]interface{}{
		"container_id": "boot-standing-relocator-test",
	}, 1, "boot-standing-relocator-test")
	if err != nil {
		t.Fatalf("buildCommandForType(opportunity_relocator) failed: %v", err)
	}
	cmd, ok := built.(*tradingCmd.RunOpportunityRelocatorCommand)
	if !ok {
		t.Fatalf("expected *RunOpportunityRelocatorCommand, got %T", built)
	}
	if cmd.ContainerID != "boot-standing-relocator-test" || cmd.PlayerID != 1 {
		t.Fatalf("identity did not survive the build: container %q, player %d", cmd.ContainerID, cmd.PlayerID)
	}

	// (c) THE ARMED STATE IS "RUNNING". The only stop is the shared operator kill-switch, and an
	// absent key must read as NOT disabled — an absent-key-is-disabled default would make a bare
	// deploy silently dormant, which is the arming seam this bead forbids.
	if cmd.RepositionDisabled {
		t.Fatal("a launch config with no reposition_disabled key built a DISABLED relocator; an absent kill-switch must read as armed, or a bare deploy is silently dormant")
	}
}

// THE END-TO-END ARMING PROOF. Membership in the boot-standing set is necessary but NOT sufficient:
// the launch is a switch statement, and a type listed with no matching case is listed and never
// launched. A mutation probe removing the dispatch case survived the membership assertion above,
// which is exactly how a silent zero would reach production — so this drives the real boot path and
// asserts a container was actually persisted.
func TestEnsureBootStandingCoordinators_LaunchesTheOpportunityRelocatorWhenAbsent(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.playerRepo = persistence.NewGormPlayerRepository(db)

	// The launched standing coordinators spawn background runners that block on the (blocking) test
	// mediator; a cancelable context lets them exit cleanly when the test ends.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.ensureBootStandingCoordinators(ctx, playerID)

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeOpportunityRelocator),
		"boot must launch exactly one standing opportunity relocator when none is running; a listed type with no dispatch case never runs a tick")
}

// IDEMPOTENCE: a warm restart must re-adopt the running relocator, never launch a twin. Two reconcile
// loops would race over the same hulls and each see its own concurrency budget, so the fleet-wide
// max_concurrent_relocations cap would silently double.
func TestEnsureBootStandingCoordinators_DoesNotLaunchATwinRelocator(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.playerRepo = persistence.NewGormPlayerRepository(db)

	insertRunningContainer(t, db, "relocator-existing", "opportunity_relocator",
		string(container.ContainerTypeOpportunityRelocator), `{"container_id":"relocator-existing"}`, playerID, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.ensureBootStandingCoordinators(ctx, playerID)

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeOpportunityRelocator),
		"a warm restart launched a TWIN relocator; two reconcile loops would each see their own concurrency budget and double the fleet-wide cap")
}

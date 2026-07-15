package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// DEPLOY-INERT pin (sp-s1ek): adding the LAUNCH VERB for the shipyard-backfill sweep
// (sp-rhju built the engine) must change NOTHING until an operator explicitly starts
// it. Like the frontier/capacity coordinators it must NEVER be boot-standing-armed the
// way the market-freshness sizer is (sp-orgp, bootStandingCoordinatorTypes). If a future
// change adds it to the boot set, this test is the tripwire.
func TestShipyardBackfill_NotBootStandingArmed(t *testing.T) {
	for _, ct := range bootStandingCoordinatorTypes {
		require.NotEqual(t, container.ContainerTypeShipyardBackfillCoordinator, ct,
			"the shipyard backfill coordinator must stay deploy-inert: never boot-standing-armed")
	}
}

// Double-launch guard: a second explicit start for a player who already has an ACTIVE
// (PENDING/RUNNING) shipyard-backfill coordinator must refuse loudly instead of spawning
// a twin standing loop — two sweeps would double-declare sweep-once posts against the
// same charted-but-unscanned set and double-spend the idle-probe budget. Mirrors the
// guarded launches elsewhere (capacity reconciler / auto-outfit).
func TestShipyardBackfillCoordinatorRefusesDoubleLaunch(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "shipyard-backfill-existing", "shipyard_backfill_coordinator",
		string(container.ContainerTypeShipyardBackfillCoordinator), `{"container_id":"shipyard-backfill-existing"}`, playerID, nil)

	_, err := s.ShipyardBackfillCoordinator(context.Background(), playerID, 0, 0)

	require.Error(t, err)
	require.Contains(t, err.Error(), "already running")
	require.Contains(t, err.Error(), "shipyard-backfill-existing",
		"the refusal must name the existing container so the operator can stop it")
}

// Restart recovery (RULINGS #2): a RUNNING shipyard-backfill container must rebuild
// byte-identically from its persisted launch config through the SAME buildCommandForType
// the creation path uses — launch and recovery can never drift. The persisted knobs
// (tick_interval_secs, max_dispatches_per_cycle) round-trip through JSON exactly as the
// container repository stores them, so a daemon restart re-adopts the sweep with the same
// cadence and rate cap the operator launched it with.
func TestRecoveryFactoryRebuildsShipyardBackfillCoordinatorCommand(t *testing.T) {
	s := newFactoryTestServer()

	launchConfig := map[string]interface{}{
		"container_id":             "shipyard-backfill-7",
		"tick_interval_secs":       90,
		"max_dispatches_per_cycle": 5,
	}

	built, err := s.buildCommandForType("shipyard_backfill_coordinator", jsonRoundTrip(t, launchConfig), 7, "shipyard-backfill-7")
	require.NoError(t, err)

	cmd, ok := built.(*scoutingCmd.RunShipyardBackfillCoordinatorCommand)
	require.True(t, ok, "expected *RunShipyardBackfillCoordinatorCommand, got %T", built)
	require.Equal(t, &scoutingCmd.RunShipyardBackfillCoordinatorCommand{
		PlayerID:              shared.MustNewPlayerID(7),
		ContainerID:           "shipyard-backfill-7",
		TickIntervalSecs:      90,
		MaxDispatchesPerCycle: 5,
	}, cmd)
}

// Creation WRITE side (sp-s1ek): the CLI knobs, threaded through the RPC into
// ShipyardBackfillCoordinator, must persist a container of the RIGHT TYPE carrying the
// launch knobs so recovery (proven above) can read them back — and the method must
// return the new container's id. This is the observable outcome of the launch verb: a
// standing, recovery-safe container exists after the call.
func TestShipyardBackfillCoordinatorPersistsLaunchConfig(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	ctx := context.Background()

	containerID, err := s.ShipyardBackfillCoordinator(ctx, playerID, 90, 5)
	require.NoError(t, err)
	require.NotEmpty(t, containerID, "the launch verb must return the new container id")
	// Release the blocking launch goroutine so it exits cleanly.
	if r := s.registeredRunner(containerID); r != nil {
		r.cancelFunc()
	}

	model, err := s.containerRepo.Get(ctx, containerID, playerID)
	require.NoError(t, err)
	require.NotNil(t, model)
	require.Equal(t, string(container.ContainerTypeShipyardBackfillCoordinator), model.ContainerType,
		"the launch verb must persist a SHIPYARD_BACKFILL_COORDINATOR container")
	require.Contains(t, model.Config, "tick_interval_secs")
	require.Contains(t, model.Config, "max_dispatches_per_cycle",
		"the per-cycle rate cap must be persisted so recovery re-adopts the operator's launch config")
}

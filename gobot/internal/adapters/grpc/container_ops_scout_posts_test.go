package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// Double-launch guard (sp-9ujl): ONE standing scout-post coordinator per player.
// GenerateContainerID mints a fresh random id each call (it is NOT keyed by player), so
// without this guard a second launch — e.g. a captain's manual `scout post-coordinator`
// while the boot-standing one (sp-9ujl) is already up — spawns a TWIN reconcile loop, and
// the two fight over the same posts and idle probes (double SetAssignedHull / re-partition /
// relay dispatch). The refusal must name the live container so the operator can stop it,
// mirroring TestCapacityReconcilerCoordinatorRefusesDoubleLaunch.
func TestScoutPostCoordinatorRefusesDoubleLaunch(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "scoutpost-existing", "scout_post_coordinator",
		string(container.ContainerTypeScoutPostCoordinator), `{"container_id":"scoutpost-existing"}`, playerID, nil)

	_, err := s.ScoutPostCoordinator(context.Background(), playerID, 0)

	require.Error(t, err)
	require.Contains(t, err.Error(), "already running")
	require.Contains(t, err.Error(), "scoutpost-existing",
		"the refusal must name the existing container so the operator can stop it")
	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeScoutPostCoordinator),
		"no duplicate scout-post coordinator may be persisted when one is already running")
}

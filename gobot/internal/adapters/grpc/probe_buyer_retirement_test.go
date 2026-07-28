package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// The probe-buyer retirement (Admiral 2026-07-28), pinned the same three ways the
// frontier-expansion and market-freshness retirements are.
//
// Why it needs pinning at all: this coordinator was BOOT-STANDING, which is exactly what made its
// cost unbounded — nothing had to launch it, so nothing had to notice it, and the first tick after
// bootstrap reached EXPANSION it bought 9 SHIP_PROBE for 245,316 credits in five minutes. A silent
// resurrection (re-adding the type to the boot set, or a builder creeping back into the registry)
// would restore precisely that, and the diff that did it would look innocuous.

// The launch verb answers honestly and persists nothing. The gRPC surface is kept so a residual
// caller — an old CLI, a script, a captain habit — gets a clear answer rather than a missing method.
func TestRetiredProbeBuyerStartVerb_ReturnsRetiredError(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	s := &DaemonServer{containerRepo: repo}

	_, err := s.ProbeBuyerFleetCoordinator(context.Background(), playerID, 0)

	require.Error(t, err)
	require.Contains(t, err.Error(), "retired", "the probe-buyer start verb must say it is retired")
	require.Contains(t, err.Error(), "probe-sensing", "the error must point the operator at the successor")

	var count int64
	require.NoError(t, db.Model(&persistence.ContainerModel{}).Where("player_id = ?", playerID).Count(&count).Error)
	require.Zero(t, count, "a retired verb must persist nothing")
}

// Recovery must treat the persisted row as terminated rather than as an unexplained loss. There IS
// such a row on the live fleet — probe_buyer_coordinator-player-5-b8bedd4f, STOPPED by hand right
// after the burst — so this is a live migration concern, not a hypothetical one.
func TestProbeBuyerCommandTypeIsRetired_SoAStaleRowRecoversCleanly(t *testing.T) {
	require.True(t, retiredCommandTypes["probe_buyer_coordinator"],
		"a persisted probe_buyer_coordinator row must be skipped at recovery, not reported as a loss")
}

// The boot-standing set is the resurrection surface that matters: membership alone is what made
// this coordinator run unattended on every daemon start.
func TestBootStandingSet_ExcludesTheRetiredProbeBuyer(t *testing.T) {
	for _, containerType := range bootStandingCoordinatorTypes {
		require.NotEqual(t, "PROBE_BUYER_COORDINATOR", string(containerType),
			"the probe-buyer coordinator is retired — boot-standing it is what let it spend 245,316 "+
				"credits unattended")
	}
}

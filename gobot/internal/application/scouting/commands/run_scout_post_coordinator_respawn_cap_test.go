package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newRespawnLoopFixture wires a single standing post with one idle in-system satellite and an
// EMPTY container-status query, so every tour the coordinator spawns reads dead on the next tick
// (found=false) and the post crash-respawn-loops — exactly the pathological loop sp-py4n caps.
// The reconciler mans the post on tick 1, then respawns its dead tour once per tick thereafter.
func newRespawnLoopFixture(t *testing.T) (*RunScoutPostCoordinatorHandler, *fakeScoutPostRepo, *fakeScoutDaemonClient, *fakeContainerStatusQuery, *shared.MockClock) {
	t.Helper()
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{}}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, cq, &fakeMarketProvider{}, clock)
	return handler, postRepo, daemonClient, cq, clock
}

// TestScoutPostRespawnCap_ConsecutiveFailures_ParkPostAfterCap pins the sp-py4n heart: a post
// whose tour dies every tick is respawned only up to the cap, then PARKED for a backoff window
// instead of respawned forever. Without the cap the reconciler starts a fresh tour on every one
// of the eight ticks below; with it, respawns stop at the cap and the persisted counter records
// the exhaustion.
func TestScoutPostRespawnCap_ConsecutiveFailures_ParkPostAfterCap(t *testing.T) {
	handler, postRepo, daemonClient, _, clock := newRespawnLoopFixture(t)

	cmd := scoutPostTestCmd()
	cmd.RespawnAttemptCap = 3

	for i := 0; i < 8; i++ {
		require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
	}

	got := postRepo.find("X1-GZ7")
	require.GreaterOrEqual(t, got.RespawnAttempts, 3, "the consecutive-respawn counter must reach the cap")
	require.True(t, clock.Now().Before(got.RespawnParkedUntil), "the post must be parked for a backoff window once the cap is hit")
	require.Len(t, daemonClient.started, 3, "respawns must stop at the cap, not fire every tick for eight ticks")
}

// TestScoutPostRespawnCap_HealthyTourResetsTheStreak pins reset-on-success: a tour that finally
// runs healthy for one tick zeroes the consecutive-respawn counter, so the cap measures
// CONSECUTIVE failures, not lifetime. An intermittent post therefore never falsely parks.
func TestScoutPostRespawnCap_HealthyTourResetsTheStreak(t *testing.T) {
	handler, postRepo, daemonClient, cq, _ := newRespawnLoopFixture(t)

	cmd := scoutPostTestCmd()
	cmd.RespawnAttemptCap = 3

	// Tick 1 mans the post; ticks 2 and 3 respawn its dead tour twice (still below the cap of 3).
	for i := 0; i < 3; i++ {
		require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
	}
	require.Equal(t, 2, postRepo.find("X1-GZ7").RespawnAttempts, "two consecutive respawns must be counted")

	// The latest tour now survives to be observed RUNNING for a tick.
	latestTour := daemonClient.persisted[len(daemonClient.persisted)-1]
	cq.byStatus["RUNNING"] = []persistence.ContainerSummary{{ID: latestTour}}
	require.NoError(t, handler.reconcileOnce(context.Background(), cmd))

	got := postRepo.find("X1-GZ7")
	require.Equal(t, 0, got.RespawnAttempts, "a tour observed healthy resets the consecutive-respawn streak")
	require.True(t, got.RespawnParkedUntil.IsZero(), "a reset post is not parked")
}

// TestScoutPostRespawnCap_Disabled_RespawnsWithoutLimit pins the RULINGS #5 disable escape: with
// the cap turned off the coordinator respawns a dead tour every tick (the pre-py4n behavior) and
// never parks — so a captain can lift the cap without a redeploy if it ever mis-parks a post.
func TestScoutPostRespawnCap_Disabled_RespawnsWithoutLimit(t *testing.T) {
	handler, postRepo, daemonClient, _, _ := newRespawnLoopFixture(t)

	cmd := scoutPostTestCmd()
	cmd.RespawnAttemptCap = 3
	cmd.RespawnCapDisabled = true

	for i := 0; i < 8; i++ {
		require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
	}

	require.Len(t, daemonClient.started, 8, "a disabled cap respawns every tick, unbounded")
	require.True(t, postRepo.find("X1-GZ7").RespawnParkedUntil.IsZero(), "a disabled cap never parks a post")
}

package commands

// Pass 0.5 (zombie-worker sweep): every reconcile pass is POST-driven, so a
// RUNNING coordinator-spawned tour or relay whose post was removed is invisible
// to all of them — Pass 0 frees hulls only under a DEAD container, and a
// standing tour runs iterations=-1 forever. These tests pin the sweep that
// closes the gap: stop the worker, reclaim its hull, and never touch a
// manually-launched tour (empty coordinator_id) or a post-referenced worker.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// runningScoutWorker declares a container RUNNING in BOTH fixtures the
// coordinator reads — byStatus (the manning passes' view) and the scout-worker
// list (the sweep's view) — so a test can never mis-state the world by wiring
// one without the other.
func runningScoutWorker(cq *fakeContainerStatusQuery, containerID, coordinatorID string) {
	if cq.byStatus == nil {
		cq.byStatus = map[string][]persistence.ContainerSummary{}
	}
	cq.byStatus["RUNNING"] = append(cq.byStatus["RUNNING"], persistence.ContainerSummary{ID: containerID, Status: "RUNNING"})
	cq.runningScoutWorkers = append(cq.runningScoutWorkers, persistence.ScoutWorkerSummary{ID: containerID, CoordinatorID: coordinatorID})
}

// The C1 shape end-to-end: a post is declared and manned by the coordinator
// itself, then removed (the sensing adoption's Remove). The next reconcile must
// stop the now-orphaned RUNNING tour and free its hull — without the sweep the
// tour scans forever (iterations=-1) and the hull never returns to the pool.
func TestScoutPost_RemovedPostRunningTourSweptAndHullFreed(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding, FreshnessTarget: time.Hour}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, cq, &fakeMarketProvider{}, clock)
	cmd := scoutPostTestCmd()

	// Tick 1: the coordinator mans the post.
	require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
	require.Len(t, daemonClient.persisted, 1)
	tourID := daemonClient.persisted[0]
	require.False(t, sat.IsIdle(), "the satellite is claimed to the tour")

	// The tour is now RUNNING and carries this coordinator's id; the post is
	// removed out from under it (the sensing coordinator's adoption Remove).
	runningScoutWorker(cq, tourID, cmd.ContainerID)
	require.NoError(t, postRepo.Remove(context.Background(), 1, "X1-GZ7"))

	// Tick 2: the sweep stops the zombie and frees the hull — even though the
	// posts table is now EMPTY (removing the last post is exactly this case).
	require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
	require.Contains(t, daemonClient.stopped, tourID, "the removed post's running tour must be stopped")
	require.True(t, sat.IsIdle(), "the swept tour's hull must return to the idle pool")
	require.Contains(t, shipRepo.releases, "SAT-1")
}

// A manually-launched CLI tour carries NO coordinator_id: it is an operator's
// hull, never the reconciler's to stop — whatever posts exist.
func TestScoutPost_ManualTourNeverSweptAsZombie(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	postRepo := &fakeScoutPostRepo{}
	sat := newScoutTestSatellite(t, "SAT-9", "X1-QW1-A1")
	require.NoError(t, sat.AssignToContainer("manual-tour-1", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{}
	runningScoutWorker(cq, "manual-tour-1", "") // empty coordinator_id = manual
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, cq, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Empty(t, daemonClient.stopped, "a manual tour (empty coordinator_id) is untouchable")
	require.False(t, sat.IsIdle(), "the manual tour keeps its hull")
	require.Empty(t, shipRepo.releases)
}

// An in-flight reposition RELAY of a removed post is the same zombie class as a
// tour: RUNNING, coordinator-spawned, referenced by no slot — swept identically.
// A PRIOR coordinator instance's id counts (any non-empty id), so a tour
// spawned before a daemon restart is still reclaimable.
func TestScoutPost_RemovedPostInflightRelaySweptAndHullReclaimed(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	postRepo := &fakeScoutPostRepo{}
	sat := newScoutTestSatellite(t, "SAT-4", "X1-AB2-A1")
	require.NoError(t, sat.AssignToContainer("relay-z1", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{}
	runningScoutWorker(cq, "relay-z1", "scoutpost-prior-instance")
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, cq, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Contains(t, daemonClient.stopped, "relay-z1", "the removed post's in-flight relay must be stopped")
	require.True(t, sat.IsIdle(), "the relay's probe returns to the pool")
}

// A worker a post slot references — tour or in-flight relay — is Pass 1 /
// Pass 1.5 territory: the sweep must be a strict no-op for it, or every healthy
// manned post would be torn down each tick.
func TestScoutPost_PostReferencedWorkersNeverSwept(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	manned := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding,
		FreshnessTarget: time.Hour, AssignedHull: "SAT-1", TourContainerID: "tour-live",
	}
	repositioning := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-QW1", Kind: domainScouting.PostKindStanding,
		FreshnessTarget: time.Hour, RepositionContainerID: "relay-live",
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{manned, repositioning}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	require.NoError(t, sat.AssignToContainer("tour-live", clock))
	probe := newScoutTestSatellite(t, "SAT-2", "X1-ZZ9-A1")
	require.NoError(t, probe.AssignToContainer("relay-live", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat, probe}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{}
	runningScoutWorker(cq, "tour-live", "scoutpost-1")
	runningScoutWorker(cq, "relay-live", "scoutpost-1")
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, cq, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Empty(t, daemonClient.stopped, "slot-referenced workers are never the sweep's to stop")
	require.False(t, sat.IsIdle())
	require.False(t, probe.IsIdle())
}

// Adversarial: the worker list arrives ALONGSIDE an error. Consuming it would
// stop workers on unverified evidence; the sweep must skip the tick instead.
func TestScoutPost_ZombieSweepSkipsOnListError(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	postRepo := &fakeScoutPostRepo{}
	sat := newScoutTestSatellite(t, "SAT-7", "X1-GZ7-A1")
	require.NoError(t, sat.AssignToContainer("tour-z9", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{listWorkersErr: errors.New("containers table down")}
	runningScoutWorker(cq, "tour-z9", "scoutpost-1") // tempting zombie beside the error
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, cq, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Empty(t, daemonClient.stopped, "a worker list returned alongside an error must never be consumed")
	require.False(t, sat.IsIdle())
}

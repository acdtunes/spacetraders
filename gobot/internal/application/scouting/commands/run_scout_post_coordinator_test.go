package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// scoutCaptureLogger implements common.ContainerLogger and records message text so
// tests can assert on the honest park/repair reasons the coordinator logs.
type scoutCaptureLogger struct {
	messages []string
}

func (l *scoutCaptureLogger) Log(_, message string, _ map[string]interface{}) {
	l.messages = append(l.messages, message)
}

// loggedContaining reports whether any captured message contains every substring.
func (l *scoutCaptureLogger) loggedContaining(subs ...string) bool {
	for _, m := range l.messages {
		all := true
		for _, s := range subs {
			if !strings.Contains(m, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// ---- fakes -----------------------------------------------------------------

// fakeScoutPostRepo is an in-memory ScoutPostRepository. It returns the stored
// pointers from ListActive so the reconciler's in-place mutations are visible,
// mirroring how the real repo round-trips through the DB.
type fakeScoutPostRepo struct {
	posts   []*domainScouting.ScoutPost
	removed []string
}

func (r *fakeScoutPostRepo) ListActive(_ context.Context, _ int) ([]*domainScouting.ScoutPost, error) {
	return r.posts, nil
}

func (r *fakeScoutPostRepo) Upsert(_ context.Context, post *domainScouting.ScoutPost) error {
	for i, p := range r.posts {
		if p.SystemSymbol == post.SystemSymbol {
			r.posts[i] = post
			return nil
		}
	}
	r.posts = append(r.posts, post)
	return nil
}

func (r *fakeScoutPostRepo) Remove(_ context.Context, _ int, systemSymbol string) error {
	r.removed = append(r.removed, systemSymbol)
	kept := r.posts[:0]
	for _, p := range r.posts {
		if p.SystemSymbol != systemSymbol {
			kept = append(kept, p)
		}
	}
	r.posts = kept
	return nil
}

func (r *fakeScoutPostRepo) find(system string) *domainScouting.ScoutPost {
	for _, p := range r.posts {
		if p.SystemSymbol == system {
			return p
		}
	}
	return nil
}

type claimRecord struct {
	ship      string
	container string
	operation string
}

// fakeScoutShipRepo is a ShipRepository over a fixed roster of ship entities.
// Idle/assignment state is derived from the entities themselves, so a
// ForceRelease in pass 1 makes a hull idle for pass 2, exactly like the DB.
type fakeScoutShipRepo struct {
	navigation.ShipRepository
	ships    []*navigation.Ship
	clock    shared.Clock
	claims   []claimRecord
	releases []string
}

func (r *fakeScoutShipRepo) FindIdleByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	var idle []*navigation.Ship
	for _, s := range r.ships {
		if s.IsIdle() {
			idle = append(idle, s)
		}
	}
	return idle, nil
}

func (r *fakeScoutShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	var matched []*navigation.Ship
	for _, s := range r.ships {
		if s.ContainerID() == containerID {
			matched = append(matched, s)
		}
	}
	return matched, nil
}

func (r *fakeScoutShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	for _, s := range r.ships {
		if s.ShipSymbol() == symbol {
			return s, nil
		}
	}
	return nil, fmt.Errorf("ship %s not found", symbol)
}

func (r *fakeScoutShipRepo) ClaimShip(_ context.Context, shipSymbol, containerID string, _ shared.PlayerID, operation string) error {
	for _, s := range r.ships {
		if s.ShipSymbol() != shipSymbol {
			continue
		}
		if fleet := s.DedicatedFleet(); fleet != "" && fleet != operation {
			return fmt.Errorf("ship %s dedicated to fleet %q", shipSymbol, fleet)
		}
		if err := s.AssignToContainer(containerID, r.clock); err != nil {
			return err
		}
		r.claims = append(r.claims, claimRecord{ship: shipSymbol, container: containerID, operation: operation})
		return nil
	}
	return fmt.Errorf("ship %s not found", shipSymbol)
}

func (r *fakeScoutShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	if ship.IsIdle() {
		r.releases = append(r.releases, ship.ShipSymbol())
	}
	return nil
}

// fakeScoutDaemonClient records the worker lifecycle calls the coordinator makes.
type fakeScoutDaemonClient struct {
	daemon.DaemonClient
	persisted  []string // container IDs persisted (scout_tour workers)
	started    []string
	stopped    []string
	startErr   error
	persistErr error
}

func (c *fakeScoutDaemonClient) PersistContainer(_ context.Context, kind daemon.ContainerKind, containerID string, _ uint, _ interface{}) error {
	if c.persistErr != nil {
		return c.persistErr
	}
	if kind != daemon.ContainerKindScoutTour {
		return fmt.Errorf("unexpected kind %q", kind)
	}
	c.persisted = append(c.persisted, containerID)
	return nil
}

func (c *fakeScoutDaemonClient) StartContainer(_ context.Context, _ daemon.ContainerKind, containerID string) error {
	if c.startErr != nil {
		return c.startErr
	}
	c.started = append(c.started, containerID)
	return nil
}

func (c *fakeScoutDaemonClient) StopContainer(_ context.Context, containerID string) error {
	c.stopped = append(c.stopped, containerID)
	return nil
}

// fakeContainerStatusQuery returns configured containers per status.
type fakeContainerStatusQuery struct {
	byStatus map[string][]persistence.ContainerSummary
}

func (q *fakeContainerStatusQuery) ListByStatusSimple(_ context.Context, status string, _ *int) ([]persistence.ContainerSummary, error) {
	return q.byStatus[status], nil
}

// fakeMarketProvider returns one marketplace waypoint per system so every post is
// man-able. Systems in emptySystems return no markets.
type fakeMarketProvider struct {
	emptySystems map[string]bool
}

func (m *fakeMarketProvider) ListBySystemWithTrait(_ context.Context, systemSymbol, _ string) ([]*shared.Waypoint, error) {
	if m.emptySystems[systemSymbol] {
		return nil, nil
	}
	wp, err := shared.NewWaypoint(systemSymbol+"-A1", 0, 0)
	if err != nil {
		return nil, err
	}
	return []*shared.Waypoint{wp}, nil
}

// ---- helpers ---------------------------------------------------------------

func newScoutTestSatellite(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	return newScoutTestShip(t, symbol, waypoint, "SATELLITE", "FRAME_PROBE")
}

func newScoutTestShip(t *testing.T, symbol, waypoint, role, frame string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 100, 0, cargo, 30, frame, role, nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	return ship
}

func newTestScoutPostHandler(postRepo *fakeScoutPostRepo, shipRepo *fakeScoutShipRepo, daemonClient *fakeScoutDaemonClient, cq *fakeContainerStatusQuery, mp *fakeMarketProvider, clock shared.Clock) *RunScoutPostCoordinatorHandler {
	return &RunScoutPostCoordinatorHandler{
		postRepo:       postRepo,
		shipRepo:       shipRepo,
		daemonClient:   daemonClient,
		containerQuery: cq,
		marketProvider: mp,
		clock:          clock,
	}
}

func scoutPostTestCmd() *RunScoutPostCoordinatorCommand {
	return &RunScoutPostCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(1),
		ContainerID: "scoutpost-1",
	}
}

// ---- tests: acceptance criteria -------------------------------------------

// Acceptance (2): add a post → an idle satellite claims it unprompted next tick.
func TestScoutPost_UnmannedPost_ClaimsIdleSatellite(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Len(t, shipRepo.claims, 1, "the idle in-system satellite must be claimed")
	require.Equal(t, "SAT-1", shipRepo.claims[0].ship)
	require.Equal(t, scoutPostFleet, shipRepo.claims[0].operation)
	require.Len(t, daemonClient.persisted, 1, "a scout tour must be persisted")
	require.Len(t, daemonClient.started, 1, "the scout tour must be started")
	got := postRepo.find("X1-GZ7")
	require.Equal(t, "SAT-1", got.AssignedHull, "post now manned by the satellite")
	require.NotEmpty(t, got.TourContainerID)
}

// Acceptance (1) + (4): a post whose tour died (e.g. after a restart marks it
// worker_interrupted, hull still on the dead container) is respawned within one
// tick, re-adopting the SAME hull onto the SAME post.
func TestScoutPost_DeadTour_RespawnsWithinOneTick(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding, AssignedHull: "SAT-1", TourContainerID: "dead-tour"}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	require.NoError(t, sat.AssignToContainer("dead-tour", clock)) // still stuck on the dead container
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	// "dead-tour" is NOT in the RUNNING set → the coordinator treats it as dead.
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Contains(t, shipRepo.releases, "SAT-1", "the hull must be reclaimed from the dead tour")
	require.Len(t, daemonClient.started, 1, "a fresh tour must be started")
	got := postRepo.find("X1-GZ7")
	require.Equal(t, "SAT-1", got.AssignedHull, "the same hull re-mans the same post")
	require.NotEqual(t, "dead-tour", got.TourContainerID, "the post points at the new tour")
	require.NotEmpty(t, got.TourContainerID)
}

// A healthy tour (RUNNING) is left completely untouched — no reclaim, no respawn.
func TestScoutPost_HealthyTour_LeftUntouched(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding, AssignedHull: "SAT-1", TourContainerID: "live-tour"}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	require.NoError(t, sat.AssignToContainer("live-tour", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "live-tour", ContainerType: "SCOUT", Status: "RUNNING"}},
	}}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, cq, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Empty(t, shipRepo.releases, "a live tour's hull is never reclaimed")
	require.Empty(t, daemonClient.started, "a live tour is never respawned")
	require.Equal(t, "live-tour", postRepo.find("X1-GZ7").TourContainerID)
}

// Acceptance (3), in-system-scoped (sp-qxa4): the old "zero satellites idle while
// unmanned posts exist" is now system-scoped. A satellite may sit idle in system A
// while a post in system B is unmanned — that is CORRECT, not a violation: handing
// the A-satellite to the B-post would crash the cross-system tour. Each post is
// manned only by an in-system hull; a post whose system has no satellite parks.
func TestScoutPost_InSystemScoped_SatelliteMayIdleWhileCrossSystemPostUnmanned(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-AAA", Kind: domainScouting.PostKindStanding},
		{PlayerID: 1, SystemSymbol: "X1-BBB", Kind: domainScouting.PostKindStanding},
	}}
	satA := newScoutTestSatellite(t, "SAT-A", "X1-AAA-A1") // in-system for X1-AAA
	satZ := newScoutTestSatellite(t, "SAT-Z", "X1-ZZZ-A1") // idle in X1-ZZZ — in-system for NO post
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{satA, satZ}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Len(t, shipRepo.claims, 1, "only the in-system match is claimed")
	require.Equal(t, "SAT-A", shipRepo.claims[0].ship)
	require.Equal(t, "SAT-A", postRepo.find("X1-AAA").AssignedHull, "the in-system satellite mans its post")
	require.Empty(t, postRepo.find("X1-BBB").AssignedHull, "the cross-system post parks — never handed the idle Z satellite")
	require.True(t, satZ.IsIdle(), "a satellite may idle in its own system while a cross-system post is unmanned — correct, not poached")
}

// Acceptance (5) / RULINGS #7: a satellite pinned to another fleet is never
// claimed, even when it is the only idle hull and a post is unmanned.
func TestScoutPost_PinnedSatellite_NeverClaimed(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	pinned := newScoutTestSatellite(t, "SAT-PINNED", "X1-GZ7-A1")
	pinned.SetDedicatedFleet("contract") // reserved for the contract coordinator
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{pinned}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Empty(t, shipRepo.claims, "a pinned hull must never be claimed")
	require.Empty(t, daemonClient.started, "no tour may be spawned on a pinned hull")
	require.True(t, pinned.IsIdle(), "the pinned hull stays idle and untouched")
	require.Empty(t, postRepo.find("X1-GZ7").AssignedHull, "the post stays unmanned")
}

// A non-satellite idle hull (the command frigate, a hauler) is never claimed for a post.
func TestScoutPost_NonSatelliteHull_NeverClaimed(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	hauler := newScoutTestShip(t, "HAULER-1", "X1-GZ7-A1", "HAULER", "FRAME_HAULER")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{hauler}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Empty(t, shipRepo.claims, "only SATELLITE-role hulls may be claimed")
	require.True(t, hauler.IsIdle())
}

// A completed sweep-once post is retired and its hull released, freeing it for the
// next unmanned post.
func TestScoutPost_SweepOnceCompleted_RetiresAndReleasesHull(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-FRONTIER", Kind: domainScouting.PostKindSweepOnce, AssignedHull: "SAT-1", TourContainerID: "sweep-tour"}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-FRONTIER-A1")
	require.NoError(t, sat.AssignToContainer("sweep-tour", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{
		"COMPLETED": {{ID: "sweep-tour", ContainerType: "SCOUT", Status: "COMPLETED"}},
	}}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, cq, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Contains(t, postRepo.removed, "X1-FRONTIER", "a completed sweep-once post must be retired")
	require.Contains(t, shipRepo.releases, "SAT-1", "the sweep-once hull must be released")
	require.Nil(t, postRepo.find("X1-FRONTIER"), "the post is gone")
	require.True(t, sat.IsIdle(), "the freed satellite is available for another post")
}

// A crashed (FAILED, not COMPLETED) sweep-once tour is retried, not retired.
func TestScoutPost_SweepOnceCrashed_Retried(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-FRONTIER", Kind: domainScouting.PostKindSweepOnce, AssignedHull: "SAT-1", TourContainerID: "crashed-tour"}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-FRONTIER-A1")
	require.NoError(t, sat.AssignToContainer("crashed-tour", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	// crashed-tour is in neither RUNNING nor COMPLETED → dead, retry.
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.NotContains(t, postRepo.removed, "X1-FRONTIER", "a crashed sweep-once is retried, not retired")
	require.Len(t, daemonClient.started, 1, "the sweep-once tour is respawned")
	require.Equal(t, "SAT-1", postRepo.find("X1-FRONTIER").AssignedHull)
}

// sortPostsByPriority orders standing posts before sweep-once, deterministic by
// system within a kind. Under in-system-only matching (sp-qxa4) each post draws only
// from its own system's satellite pool (posts are keyed by system, so two never
// compete for one hull) — the ordering no longer causes cross-system stealing, but it
// still gives the reconcile a stable, standing-first iteration order. Tested directly.
func TestScoutPost_SortPostsByPriority_StandingBeforeSweepOnce(t *testing.T) {
	posts := []*domainScouting.ScoutPost{
		{SystemSymbol: "X1-SWEEP-B", Kind: domainScouting.PostKindSweepOnce},
		{SystemSymbol: "X1-STAND-B", Kind: domainScouting.PostKindStanding},
		{SystemSymbol: "X1-STAND-A", Kind: domainScouting.PostKindStanding},
		{SystemSymbol: "X1-SWEEP-A", Kind: domainScouting.PostKindSweepOnce},
	}
	sortPostsByPriority(posts)

	got := []string{posts[0].SystemSymbol, posts[1].SystemSymbol, posts[2].SystemSymbol, posts[3].SystemSymbol}
	require.Equal(t, []string{"X1-STAND-A", "X1-STAND-B", "X1-SWEEP-A", "X1-SWEEP-B"}, got,
		"standing posts sort before sweep-once, deterministic by system within a kind")
}

// Only the in-system satellite is ever selected; the out-of-system one is left idle,
// never claimed (sp-qxa4 in-system-only matching), even though it sorts first.
func TestScoutPost_SelectsInSystemSatellite_CrossSystemLeftIdle(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	satFar := newScoutTestSatellite(t, "SAT-FAR", "X1-OTHER-A1") // sorts first by symbol, but out of system
	satNear := newScoutTestSatellite(t, "SAT-NEAR", "X1-GZ7-A1") // in the post's system
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{satFar, satNear}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, "SAT-NEAR", postRepo.find("X1-GZ7").AssignedHull, "the in-system satellite mans the post")
	require.Len(t, shipRepo.claims, 1, "only the in-system satellite is claimed")
	require.Equal(t, "SAT-NEAR", shipRepo.claims[0].ship)
	require.True(t, satFar.IsIdle(), "the out-of-system satellite is never claimed")
}

// A post whose system has no known marketplace waypoints is not manned — the
// coordinator does not spawn a zero-market tour or burn the in-system satellite's
// claim; the hull stays idle in-system until the system is charted. A charted post
// with its own in-system satellite is manned normally.
func TestScoutPost_NoKnownMarkets_LeavesInSystemSatelliteIdle(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-UNCHARTED", Kind: domainScouting.PostKindStanding},
		{PlayerID: 1, SystemSymbol: "X1-CHARTED", Kind: domainScouting.PostKindStanding},
	}}
	satU := newScoutTestSatellite(t, "SAT-U", "X1-UNCHARTED-A1") // in-system for the uncharted post
	satC := newScoutTestSatellite(t, "SAT-C", "X1-CHARTED-A1")   // in-system for the charted post
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{satU, satC}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMarketProvider{emptySystems: map[string]bool{"X1-UNCHARTED": true}}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, mp, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Empty(t, postRepo.find("X1-UNCHARTED").AssignedHull, "the uncharted post stays unmanned")
	require.True(t, satU.IsIdle(), "its in-system satellite is not burned on a zero-market tour")
	require.Equal(t, "SAT-C", postRepo.find("X1-CHARTED").AssignedHull, "the charted post is manned by its in-system satellite")
}

// A start-failure rolls the claim back so the hull is not stranded.
func TestScoutPost_StartFailure_ReleasesHull(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{startErr: fmt.Errorf("boom")}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Contains(t, shipRepo.releases, "SAT-1", "a failed start must release the claimed hull")
	require.Contains(t, daemonClient.stopped, daemonClient.persisted[0], "the persisted worker must be cleaned up")
	require.Empty(t, postRepo.find("X1-GZ7").AssignedHull, "the post stays unmanned after a failed start")
}

// ---- tests: sp-qxa4 in-system-only matching -------------------------------

// Root cause (sp-qxa4): only a cross-system idle satellite exists — the live
// 1A@DP51 / post-in-PA3 shape. It must NEVER be selected: the scout tour navigates
// in-system only, so a cross-system assignment crash-respawn-loops. The post parks.
func TestScoutPost_CrossSystemSatellite_NeverSelected(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-PA3", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	far := newScoutTestSatellite(t, "SAT-1A", "X1-DP51-A1") // idle in DP51, post is in PA3
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{far}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Empty(t, shipRepo.claims, "a cross-system satellite is never claimed for a post")
	require.Empty(t, daemonClient.started, "no cross-system tour is ever spawned")
	require.Empty(t, postRepo.find("X1-PA3").AssignedHull, "the post parks unmanned")
	require.True(t, far.IsIdle(), "the cross-system satellite stays idle in its own system")
}

// An unmanned post with no in-system satellite (but a repositionable one idle
// elsewhere) parks with an honest, system-scoped reason in the message text (sp-qxa4).
func TestScoutPost_UnmannedPost_ParksWithReason(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-PA3", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	far := newScoutTestSatellite(t, "SAT-1A", "X1-DP51-A1") // idle elsewhere — a repositionable candidate
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{far}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	logger := &scoutCaptureLogger{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	ctx := common.WithLogger(context.Background(), logger)
	require.NoError(t, handler.reconcileOnce(ctx, scoutPostTestCmd()))

	require.Empty(t, postRepo.find("X1-PA3").AssignedHull, "the post is unmanned")
	require.True(t, logger.loggedContaining("X1-PA3", "no in-system satellite"),
		"the park must be logged with an honest, system-scoped reason in the message text")
}

// Repair pass (sp-qxa4): the live incident — a post ASSIGNED a hull that is not in
// the post's system (1A stranded in DP51 while manning the PA3 post), the crash-loop
// even flickering RUNNING. On reconcile the assignment is released: tour stopped (NOT
// respawned), hull freed, assignment cleared — both return to the pool. This heals the
// incident at deploy with no manual cleanup. The freed hull, still cross-system, is
// then correctly parked (not re-manned onto the wrong post) by pass 2.
func TestScoutPost_RepairPass_ReleasesMismatchedAssignment(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-PA3", Kind: domainScouting.PostKindStanding, AssignedHull: "SAT-1A", TourContainerID: "cross-tour"}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1A", "X1-DP51-A1") // stranded in DP51, post is in PA3
	require.NoError(t, sat.AssignToContainer("cross-tour", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	// The crash-looping tour is momentarily RUNNING — the repair must still fire.
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "cross-tour", ContainerType: "SCOUT", Status: "RUNNING"}},
	}}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, cq, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Contains(t, daemonClient.stopped, "cross-tour", "the crash-looping cross-system tour is stopped, not respawned")
	require.Contains(t, shipRepo.releases, "SAT-1A", "the stranded hull is released back to the pool")
	require.True(t, sat.IsIdle(), "the hull is idle again, available for correct re-matching")
	got := postRepo.find("X1-PA3")
	require.Empty(t, got.AssignedHull, "the post's assignment is cleared")
	require.Empty(t, got.TourContainerID, "the dead tour reference is cleared")
	require.Empty(t, daemonClient.started, "the cross-system tour is not respawned onto the same wrong hull")
}

// A parked post self-heals the moment a satellite arrives in its system — no
// coordinator restart, no manual intervention (sp-qxa4). Two ticks: parks, then mans.
func TestScoutPost_InSystemArrival_SelfHeals(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-PA3", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{newScoutTestSatellite(t, "SAT-1A", "X1-DP51-A1")}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	// Tick 1: the only satellite is cross-system → the post parks unmanned.
	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	require.Empty(t, postRepo.find("X1-PA3").AssignedHull, "tick 1: no in-system satellite, post parks")
	require.Empty(t, shipRepo.claims, "tick 1: nothing is claimed")

	// The captain repositions the satellite into the post's system (it arrives).
	shipRepo.ships = []*navigation.Ship{newScoutTestSatellite(t, "SAT-1A", "X1-PA3-A1")}

	// Tick 2: now in-system → the post self-heals, manned with no restart.
	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	require.Equal(t, "SAT-1A", postRepo.find("X1-PA3").AssignedHull, "tick 2: in-system arrival self-heals the post")
	require.Len(t, shipRepo.claims, 1, "tick 2: the now-in-system satellite is claimed")
}

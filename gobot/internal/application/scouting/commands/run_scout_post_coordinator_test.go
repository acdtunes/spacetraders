package commands

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

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

// Acceptance (3): zero satellites idle while unmanned posts exist — every idle
// satellite is claimed when there are at least as many unmanned posts.
func TestScoutPost_ManyPostsFewSatellites_NoSatelliteLeftIdle(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-AAA", Kind: domainScouting.PostKindStanding},
		{PlayerID: 1, SystemSymbol: "X1-BBB", Kind: domainScouting.PostKindStanding},
		{PlayerID: 1, SystemSymbol: "X1-CCC", Kind: domainScouting.PostKindStanding},
	}}
	// Two idle satellites, three unmanned posts.
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-ZZZ-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-ZZZ-A2"),
	}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Len(t, shipRepo.claims, 2, "both idle satellites must be claimed")
	idle, _ := shipRepo.FindIdleByPlayer(context.Background(), shared.MustNewPlayerID(1))
	require.Empty(t, idle, "no satellite may be left idle while posts are unmanned")
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

// When satellites are scarce, standing posts are manned before sweep-once posts.
func TestScoutPost_StandingPostsPrioritizedOverSweepOnce(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-SWEEP", Kind: domainScouting.PostKindSweepOnce},
		{PlayerID: 1, SystemSymbol: "X1-STAND", Kind: domainScouting.PostKindStanding},
	}}
	// Only one idle satellite for two posts.
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{newScoutTestSatellite(t, "SAT-1", "X1-OTHER-A1")}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, "SAT-1", postRepo.find("X1-STAND").AssignedHull, "the standing post is manned first")
	require.Empty(t, postRepo.find("X1-SWEEP").AssignedHull, "the sweep-once post waits")
}

// An in-system idle satellite is preferred over an out-of-system one (no needless
// repositioning flight).
func TestScoutPost_PrefersInSystemSatellite(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-FAR", "X1-OTHER-A1"), // sorts first by symbol, but out of system
		newScoutTestSatellite(t, "SAT-NEAR", "X1-GZ7-A1"),  // in the post's system
	}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, "SAT-NEAR", postRepo.find("X1-GZ7").AssignedHull, "the in-system satellite is preferred")
}

// A post in an uncharted system (no marketplace waypoints) does not consume a
// satellite — it is left for a man-able post this tick.
func TestScoutPost_NoKnownMarkets_LeavesSatelliteForAnotherPost(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-UNCHARTED", Kind: domainScouting.PostKindStanding},
		{PlayerID: 1, SystemSymbol: "X1-CHARTED", Kind: domainScouting.PostKindStanding},
	}}
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{newScoutTestSatellite(t, "SAT-1", "X1-OTHER-A1")}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMarketProvider{emptySystems: map[string]bool{"X1-UNCHARTED": true}}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, mp, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Empty(t, postRepo.find("X1-UNCHARTED").AssignedHull, "the uncharted post stays unmanned")
	require.Equal(t, "SAT-1", postRepo.find("X1-CHARTED").AssignedHull, "the satellite goes to the man-able post")
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

package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
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
	persisted         []string            // container IDs persisted (scout_tour workers)
	persistedTourCmds []*ScoutTourCommand // the *ScoutTourCommand captured per persisted tour, same order as persisted (sp-zixw)
	repositioned      []string            // container IDs persisted (scout_reposition relays, sp-s232)
	started           []string
	stopped           []string
	startErr          error
	persistErr        error
}

func (c *fakeScoutDaemonClient) PersistContainer(_ context.Context, kind daemon.ContainerKind, containerID string, _ uint, command interface{}) error {
	if c.persistErr != nil {
		return c.persistErr
	}
	switch kind {
	case daemon.ContainerKindScoutTour:
		c.persisted = append(c.persisted, containerID)
		if tourCmd, ok := command.(*ScoutTourCommand); ok {
			c.persistedTourCmds = append(c.persistedTourCmds, tourCmd)
		}
	case daemon.ContainerKindScoutReposition:
		c.repositioned = append(c.repositioned, containerID)
	default:
		return fmt.Errorf("unexpected kind %q", kind)
	}
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

// fakeGateGraph resolves jump-hop distances from a fixed "from->to" → hop-count table
// (sp-s232). A missing entry is UNROUTABLE (mirrors gategraph's ErrUnroutable / fetch
// failure — the coordinator skips the candidate either way). Path returns a slice of
// hops+1 systems; only its length feeds the coordinator's nearest-by-hops selection.
type fakeGateGraph struct {
	hops map[string]int // "FROM->TO" → jump hops
}

func (g *fakeGateGraph) Path(_ context.Context, from, to string, _ int) ([]string, error) {
	n, ok := g.hops[from+"->"+to]
	if !ok {
		return nil, fmt.Errorf("no jump-gate route from %s to %s", from, to)
	}
	path := make([]string, n+1) // n hops → n+1 systems inclusive
	for i := range path {
		path[i] = fmt.Sprintf("%s#%d", from, i)
	}
	path[0], path[n] = from, to
	return path, nil
}

// fakeGraphProvider stands in for the presence-free graph service (sp-nn0y). GetGraph
// records the systems it was asked to discover and, on success, "charts" the system by
// removing it from the paired fakeMarketProvider's emptySystems — mirroring the real
// BuildSystemGraph→waypointRepo.Add→ListBySystemWithTrait round-trip (era-scoping is the
// reused Add path's own invariant, sp-vapw). err simulates an API failure; a system left
// in noMarkets stays marketless after discovery (charted but genuinely barren).
type fakeGraphProvider struct {
	mp        *fakeMarketProvider
	requested []string
	err       error
	noMarkets map[string]bool
}

func (g *fakeGraphProvider) GetGraph(_ context.Context, systemSymbol string, _ bool, _ int) (*system.GraphLoadResult, error) {
	g.requested = append(g.requested, systemSymbol)
	if g.err != nil {
		return nil, g.err
	}
	// Discovery charts the system: unless it is genuinely barren, it now has a marketplace
	// waypoint the market provider will surface (the persisted era-scoped row).
	if !g.noMarkets[systemSymbol] {
		delete(g.mp.emptySystems, systemSymbol)
	}
	return &system.GraphLoadResult{Source: "api"}, nil
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

// ---- tests: sp-s232 cross-gate repositioning ------------------------------

// Acceptance (sp-s232): a post with no in-system satellite but a routable idle one
// elsewhere gets that satellite JUMP-ROUTED to it — the FEWEST-hops candidate is
// chosen (not the first-sorted), claimed for the relay (poach-guarded), and the post
// records the in-flight relay while staying UNMANNED (manning is still in-system only).
func TestScoutPost_Reposition_DispatchesNearestByHops(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-FAR", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	satFar := newScoutTestSatellite(t, "SAT-A", "X1-DISTANT-A1") // sorts FIRST, but 3 hops away
	satNear := newScoutTestSatellite(t, "SAT-Z", "X1-NEAR-A1")   // sorts LAST, but 1 hop away
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{satFar, satNear}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)
	handler.gateGraph = &fakeGateGraph{hops: map[string]int{
		"X1-NEAR->X1-FAR":    1,
		"X1-DISTANT->X1-FAR": 3,
	}}
	logger := &scoutCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	require.NoError(t, handler.reconcileOnce(ctx, scoutPostTestCmd()))

	require.Len(t, daemonClient.repositioned, 1, "exactly one reposition relay is dispatched")
	require.Len(t, daemonClient.started, 1, "the relay is started")
	require.Len(t, shipRepo.claims, 1, "the chosen satellite is claimed for the relay")
	require.Equal(t, "SAT-Z", shipRepo.claims[0].ship, "the FEWEST-hops satellite is chosen, not the first-sorted")
	require.Equal(t, scoutPostFleet, shipRepo.claims[0].operation, "claimed under the scout fleet op (poach-guarded, RULINGS #7)")
	require.Equal(t, daemonClient.repositioned[0], shipRepo.claims[0].container, "the claim binds the satellite to the relay container")
	require.Equal(t, daemonClient.repositioned[0], postRepo.find("X1-FAR").RepositionContainerID, "the post records the in-flight relay")
	require.Empty(t, postRepo.find("X1-FAR").AssignedHull, "the post is NOT manned during transit — manning stays in-system only")
	require.True(t, satFar.IsIdle(), "the farther satellite is untouched")
	require.True(t, logger.loggedContaining("X1-FAR", "repositioning SAT-Z", "1 jump"), "the dispatch logs the honest relay reason with hop count")
}

// One relay per post: while a relay is airborne (its container RUNNING) the coordinator
// dispatches no second relay and claims no other hull for the same post (sp-s232).
func TestScoutPost_Reposition_OneRelayPerPost_NoSecondDispatch(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-FAR", Kind: domainScouting.PostKindStanding, RepositionContainerID: "relay-inflight"}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-NEAR-A1") // idle, routable — but a relay is already airborne
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "relay-inflight", ContainerType: "SCOUT_REPOSITION", Status: "RUNNING"}},
	}}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, cq, &fakeMarketProvider{}, clock)
	handler.gateGraph = &fakeGateGraph{hops: map[string]int{"X1-NEAR->X1-FAR": 1}}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Empty(t, daemonClient.repositioned, "no second relay while one is airborne")
	require.Empty(t, shipRepo.claims, "the idle satellite is not claimed for a duplicate relay")
	require.Equal(t, "relay-inflight", postRepo.find("X1-FAR").RepositionContainerID, "the in-flight relay reference is preserved")
	require.True(t, sat.IsIdle(), "the idle satellite stays idle — one relay per post")
}

// Fail-closed: when no idle satellite can be jump-routed to the post, the coordinator
// dispatches NO relay and parks the post honest — never flies a hull it cannot route
// (sp-s232).
func TestScoutPost_Reposition_Unroutable_ParksFailClosed(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-ISLAND", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-MAINLAND-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)
	handler.gateGraph = &fakeGateGraph{hops: map[string]int{}} // no route MAINLAND->ISLAND
	logger := &scoutCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	require.NoError(t, handler.reconcileOnce(ctx, scoutPostTestCmd()))

	require.Empty(t, daemonClient.repositioned, "an unroutable post dispatches no relay (fail-closed)")
	require.Empty(t, shipRepo.claims, "no satellite is claimed for an un-routable relay")
	require.Empty(t, postRepo.find("X1-ISLAND").AssignedHull, "the post parks unmanned")
	require.Empty(t, postRepo.find("X1-ISLAND").RepositionContainerID, "no relay is recorded")
	require.True(t, sat.IsIdle(), "the unreachable satellite stays idle")
	require.True(t, logger.loggedContaining("X1-ISLAND", "no jump-routable satellite"), "the fail-closed park reason is logged")
}

// A relay that ended (failed or restart-interrupted, hull still claimed) is reclaimed
// and its reference cleared; the per-post backoff armed at dispatch then prevents an
// immediate re-dispatch, so a fast-failing relay never hot-loops (sp-s232 / sp-py4n).
func TestScoutPost_Reposition_RelayEnded_ReclaimsAndBacksOff(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-FAR", Kind: domainScouting.PostKindStanding},
	}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-SRC-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)
	handler.gateGraph = &fakeGateGraph{hops: map[string]int{"X1-SRC->X1-FAR": 2}}
	logger := &scoutCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	// Tick 1: dispatch the relay (arms the per-post backoff).
	require.NoError(t, handler.reconcileOnce(ctx, scoutPostTestCmd()))
	require.Len(t, daemonClient.repositioned, 1, "tick 1 dispatches the relay")
	require.Equal(t, daemonClient.repositioned[0], postRepo.find("X1-FAR").RepositionContainerID)
	require.False(t, sat.IsIdle(), "the satellite is claimed to the relay")

	// The relay ends with the claim still on the hull (restart-interrupted) and is NOT
	// in the RUNNING set. The clock does NOT advance → still inside the backoff window.
	// Tick 2: reclaim the hull, clear the relay, and DO NOT re-dispatch.
	require.NoError(t, handler.reconcileOnce(ctx, scoutPostTestCmd()))

	require.Contains(t, shipRepo.releases, "SAT-1", "the ended relay's hull is reclaimed (recoverable)")
	require.Empty(t, postRepo.find("X1-FAR").RepositionContainerID, "the ended relay reference is cleared")
	require.Len(t, daemonClient.repositioned, 1, "NO second relay inside the backoff window (no hot-loop)")
	require.True(t, logger.loggedContaining("X1-FAR", "backing off"), "the backoff park reason is logged")
}

// Arrival → in-system manning: once a relay lands its satellite idle in the post's
// system (relay no longer RUNNING), the next tick clears the relay and mans the post
// IN-SYSTEM via a tour — proving the sp-qxa4 manning invariant is untouched: reposition
// only moved the hull there first (sp-s232).
func TestScoutPost_Reposition_ArrivalMansInSystem(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-FAR", Kind: domainScouting.PostKindStanding, RepositionContainerID: "relay-landed"}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-FAR-A1") // the relay landed it in the post's system, idle
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	// relay-landed is NOT in RUNNING → the relay completed; the hull is idle in-system.
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)
	handler.gateGraph = &fakeGateGraph{hops: map[string]int{}} // irrelevant: manning is in-system

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	got := postRepo.find("X1-FAR")
	require.Equal(t, "SAT-1", got.AssignedHull, "the arrived satellite mans the post in-system (qxa4 invariant)")
	require.Empty(t, got.RepositionContainerID, "the landed relay reference is cleared")
	require.NotEmpty(t, got.TourContainerID, "a tour is spawned in-system")
	require.Len(t, daemonClient.persisted, 1, "the in-system tour is persisted (not another relay)")
	require.Empty(t, daemonClient.repositioned, "no new relay — the satellite arrived, it is manned locally")
	require.Len(t, shipRepo.claims, 1, "the satellite is claimed for the tour")
	require.Equal(t, "SAT-1", shipRepo.claims[0].ship)
}

// In-system manning ALWAYS wins over repositioning for the same satellite (sp-s232):
// all in-system manning (2a) runs before any reposition (2b), so an idle satellite in a
// post's own system is manned there — never relayed away to a higher-priority post that
// could reach it. X1-AAA (sorts first, needs a relay) does NOT steal X1-BBB's local hull.
func TestScoutPost_Reposition_InSystemManningWinsOverReposition(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-AAA", Kind: domainScouting.PostKindStanding}, // sorts FIRST, no in-system sat
		{PlayerID: 1, SystemSymbol: "X1-BBB", Kind: domainScouting.PostKindStanding}, // has the only idle sat in-system
	}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-BBB-A1") // idle in BBB — the ONLY idle satellite
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)
	handler.gateGraph = &fakeGateGraph{hops: map[string]int{"X1-BBB->X1-AAA": 1}} // SAT-1 COULD relay to AAA

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, "SAT-1", postRepo.find("X1-BBB").AssignedHull, "the in-system satellite mans its own post (2a)")
	require.NotEmpty(t, postRepo.find("X1-BBB").TourContainerID, "manned via a tour")
	require.Empty(t, daemonClient.repositioned, "the in-system satellite is NEVER repositioned away — manning wins over relaying")
	require.Empty(t, postRepo.find("X1-AAA").AssignedHull, "the cross-system post parks this tick (its candidate was manned locally)")
	require.Empty(t, postRepo.find("X1-AAA").RepositionContainerID, "no relay for AAA — no idle satellite remained after in-system manning")
	require.Len(t, shipRepo.claims, 1, "exactly one claim — the in-system tour, not a relay")
	require.Equal(t, scoutPostFleet, shipRepo.claims[0].operation)
}

// With no gate graph wired, repositioning is DISABLED: a satellite-less post parks with
// the pre-s232 in-system reason and no relay is ever dispatched (sp-s232 fail-open).
func TestScoutPost_Reposition_NoGateGraph_ParksUnchanged(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-FAR", Kind: domainScouting.PostKindStanding}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-NEAR-A1") // idle elsewhere — but no gate graph to route it
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	logger := &scoutCaptureLogger{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)
	// gateGraph deliberately left nil.

	ctx := common.WithLogger(context.Background(), logger)
	require.NoError(t, handler.reconcileOnce(ctx, scoutPostTestCmd()))

	require.Empty(t, daemonClient.repositioned, "no relay is dispatched without a gate graph")
	require.Empty(t, shipRepo.claims, "no satellite is claimed")
	require.Empty(t, postRepo.find("X1-FAR").AssignedHull, "the post parks, exactly as before sp-s232")
	require.True(t, logger.loggedContaining("X1-FAR", "no in-system satellite"), "the pre-s232 park reason is preserved")
}

// ---- tests: sp-nn0y virgin-system waypoint discovery ----------------------

// Acceptance (sp-nn0y): a reposition target with NO known market waypoint (a virgin
// frontier system — the s232 park-forever bug) is DISCOVERED presence-free via the API and
// repositioned the SAME tick. The discovery charts the system (persisting its waypoints
// era-scoped via the reused BuildSystemGraph→Add path), the re-read now surfaces a market,
// and the routable satellite is relayed in — no satellite need already be in the system.
func TestScoutPost_Reposition_VirginSystem_DiscoversAndDispatches(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-VIRGIN", Kind: domainScouting.PostKindSweepOnce}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-SRC-A1") // idle elsewhere, 1 hop from the virgin system
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMarketProvider{emptySystems: map[string]bool{"X1-VIRGIN": true}} // no KNOWN markets yet
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, mp, clock)
	handler.gateGraph = &fakeGateGraph{hops: map[string]int{"X1-SRC->X1-VIRGIN": 1}}
	gp := &fakeGraphProvider{mp: mp}
	handler.graphProvider = gp
	logger := &scoutCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	require.NoError(t, handler.reconcileOnce(ctx, scoutPostTestCmd()))

	require.Equal(t, []string{"X1-VIRGIN"}, gp.requested, "the virgin system is discovered presence-free via the API")
	require.Len(t, daemonClient.repositioned, 1, "the relay is dispatched the SAME tick discovery charts the system")
	require.Len(t, shipRepo.claims, 1, "the routable satellite is claimed for the relay")
	require.Equal(t, "SAT-1", shipRepo.claims[0].ship)
	require.Equal(t, daemonClient.repositioned[0], postRepo.find("X1-VIRGIN").RepositionContainerID, "the post records the in-flight relay")
	require.Empty(t, postRepo.find("X1-VIRGIN").AssignedHull, "the post is NOT manned during transit — manning stays in-system only")
	require.True(t, logger.loggedContaining("X1-VIRGIN", "Discovered", "repositioning"), "the same-tick discovery+dispatch is logged honestly")
}

// A virgin system that discovery charts as GENUINELY marketless parks with a DISTINCT
// 'unserviceable' reason — never the 'not yet scanned' park — so the captain can tell a
// barren system apart from an unscanned one and remove the post. No relay is dispatched
// (nothing to scan) and no satellite is claimed (sp-nn0y).
func TestScoutPost_Reposition_VirginSystem_NoMarkets_ParksUnserviceable(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-BARREN", Kind: domainScouting.PostKindSweepOnce}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-SRC-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMarketProvider{emptySystems: map[string]bool{"X1-BARREN": true}}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, mp, clock)
	handler.gateGraph = &fakeGateGraph{hops: map[string]int{"X1-SRC->X1-BARREN": 1}}
	gp := &fakeGraphProvider{mp: mp, noMarkets: map[string]bool{"X1-BARREN": true}} // discovery succeeds, finds no marketplaces
	handler.graphProvider = gp
	logger := &scoutCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	require.NoError(t, handler.reconcileOnce(ctx, scoutPostTestCmd()))

	require.Equal(t, []string{"X1-BARREN"}, gp.requested, "discovery is attempted")
	require.Empty(t, daemonClient.repositioned, "a genuinely marketless system dispatches no relay")
	require.Empty(t, shipRepo.claims, "no satellite is claimed for an unserviceable post")
	require.Empty(t, postRepo.find("X1-BARREN").RepositionContainerID, "no relay is recorded")
	require.True(t, sat.IsIdle(), "the satellite stays idle")
	require.True(t, logger.loggedContaining("X1-BARREN", "unserviceable"), "the DISTINCT unserviceable reason is logged, not 'not yet scanned'")
}

// A virgin discovery whose API call FAILS parks fail-closed (nothing spent, no relay) and
// arms the reposition backoff, so the API is NOT re-probed every tick — the second tick
// inside the window attempts no discovery at all (sp-nn0y / sp-py4n anti-hot-loop).
func TestScoutPost_Reposition_VirginDiscoveryFails_ParksAndBacksOff(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-VIRGIN", Kind: domainScouting.PostKindSweepOnce}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-SRC-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMarketProvider{emptySystems: map[string]bool{"X1-VIRGIN": true}}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, mp, clock)
	handler.gateGraph = &fakeGateGraph{hops: map[string]int{"X1-SRC->X1-VIRGIN": 1}}
	gp := &fakeGraphProvider{mp: mp, err: fmt.Errorf("api down")}
	handler.graphProvider = gp
	logger := &scoutCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	// Tick 1: discovery fails → fail-closed park, backoff armed.
	require.NoError(t, handler.reconcileOnce(ctx, scoutPostTestCmd()))
	require.Len(t, gp.requested, 1, "tick 1 attempts discovery once")
	require.Empty(t, daemonClient.repositioned, "a failed discovery dispatches no relay (fail-closed)")
	require.Empty(t, shipRepo.claims, "no satellite is claimed")
	require.Empty(t, postRepo.find("X1-VIRGIN").RepositionContainerID, "no relay is recorded")
	require.True(t, sat.IsIdle(), "the satellite stays idle")
	require.True(t, logger.loggedContaining("X1-VIRGIN", "fail-closed"), "the fail-closed park reason is logged")

	// Tick 2 inside the backoff window (clock not advanced): the pass-2b backoff check fires
	// FIRST, so NO second discovery is attempted — the API is not hammered every tick.
	require.NoError(t, handler.reconcileOnce(ctx, scoutPostTestCmd()))
	require.Len(t, gp.requested, 1, "no second discovery inside the backoff window (no per-tick API hammering)")
	require.True(t, logger.loggedContaining("X1-VIRGIN", "backing off"), "the intervening tick parks via the backoff, not a fresh probe")
}

// ---- tests: sp-zixw probe scan-interval derivation -------------------------

// A 60m freshness target derives to exactly the 30m cap (60/2=30, landing exactly
// on scanIntervalCap): the boundary case proving the derivation clamps at, not past,
// the ceiling (sp-zixw — replaces the old hardcoded 5m wait that ignored freshness
// entirely).
func TestScoutPost_SpawnTour_ScanInterval_60mFreshnessClampsAtCap(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding, FreshnessTarget: 60 * time.Minute}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Len(t, daemonClient.persistedTourCmds, 1, "a scout tour must be persisted")
	require.Equal(t, 30*time.Minute, daemonClient.persistedTourCmds[0].ScanInterval, "60m freshness halves to 30m, exactly at the cap")
}

// A 20m freshness target derives to 10m, well inside [floor, cap] — proving the
// derivation is not simply clamping everything to one bound (sp-zixw).
func TestScoutPost_SpawnTour_ScanInterval_20mFreshnessUnclamped(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding, FreshnessTarget: 20 * time.Minute}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Len(t, daemonClient.persistedTourCmds, 1, "a scout tour must be persisted")
	require.Equal(t, 10*time.Minute, daemonClient.persistedTourCmds[0].ScanInterval, "20m freshness halves to 10m, unclamped")
}

// A zero/unset freshness target (post.FreshnessTarget never configured) derives to
// zero, which clamps UP to the 5m floor — the coordinator path has no "direct
// launch" 15m default to fall back on, so an absent freshness must not resolve to a
// zero-wait busy loop (sp-zixw).
func TestScoutPost_SpawnTour_ScanInterval_ZeroFreshnessClampsToFloor(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding} // FreshnessTarget left zero
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	handler := newTestScoutPostHandler(postRepo, shipRepo, daemonClient, &fakeContainerStatusQuery{}, &fakeMarketProvider{}, clock)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Len(t, daemonClient.persistedTourCmds, 1, "a scout tour must be persisted")
	require.Equal(t, 5*time.Minute, daemonClient.persistedTourCmds[0].ScanInterval, "zero freshness clamps up to the 5m floor")
}

// ---- fakes & helpers: sp-enry multi-probe partitions ----------------------

// fakeScoutRoutingClient partitions markets round-robin across the N synthetic slot
// ships the coordinator supplies, so partitions are disjoint, complete, and
// deterministic — a faithful stand-in for the VRP service for partition-logic tests.
// It records how many times PartitionFleet was called so a test can assert the
// coordinator re-partitions ONLY on a hull-budget change.
type fakeScoutRoutingClient struct {
	routing.RoutingClient
	calls int
	err   error
}

func (c *fakeScoutRoutingClient) PartitionFleet(_ context.Context, req *routing.VRPRequest) (*routing.VRPResponse, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	assignments := make(map[string]*routing.ShipTourData, len(req.ShipSymbols))
	for _, id := range req.ShipSymbols {
		assignments[id] = &routing.ShipTourData{}
	}
	for j, market := range req.MarketWaypoints {
		id := req.ShipSymbols[j%len(req.ShipSymbols)]
		assignments[id].Waypoints = append(assignments[id].Waypoints, market)
	}
	return &routing.VRPResponse{Assignments: assignments}, nil
}

// fakeMultiMarketProvider returns a configured list of marketplace waypoints per
// system, so a partition test can hand a post more than one market.
type fakeMultiMarketProvider struct {
	markets map[string][]string
}

func (m *fakeMultiMarketProvider) ListBySystemWithTrait(_ context.Context, systemSymbol, _ string) ([]*shared.Waypoint, error) {
	syms := m.markets[systemSymbol]
	wps := make([]*shared.Waypoint, 0, len(syms))
	for _, s := range syms {
		wp, err := shared.NewWaypoint(s, 0, 0)
		if err != nil {
			return nil, err
		}
		wps = append(wps, wp)
	}
	return wps, nil
}

// partitionOf collects the full market set a post assigns across all its slots, and
// the per-slot partitions in slot order, so a test can assert disjointness/coverage.
func partitionOf(post *domainScouting.ScoutPost) (all []string, perSlot [][]string) {
	for _, slot := range post.Slots() {
		p := append([]string(nil), slot.Partition()...)
		perSlot = append(perSlot, p)
		all = append(all, p...)
	}
	sort.Strings(all)
	return all, perSlot
}

// Acceptance / transition: a fresh 3-hull post with 3 idle in-system satellites
// converges to a FULL disjoint partition in ONE reconcile cycle — the exact shape the
// harbormaster's KA42 hulls=3 declaration relies on.
func TestScoutPost_MultiHull_ConvergesInOneCycle(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-KA42", Kind: domainScouting.PostKindStanding, Hulls: 3, FreshnessTarget: 30 * time.Minute}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sats := []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-KA42-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-KA42-A2"),
		newScoutTestSatellite(t, "SAT-3", "X1-KA42-A3"),
	}
	shipRepo := &fakeScoutShipRepo{ships: sats, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-KA42": {"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"},
	}}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: &fakeContainerStatusQuery{}, marketProvider: mp, clock: clock,
		routingClient: &fakeScoutRoutingClient{}, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	got := postRepo.find("X1-KA42")
	require.Equal(t, 3, got.MannedCount(), "all 3 slots manned in one cycle")
	require.True(t, got.IsFullyManned())
	require.Len(t, daemonClient.started, 3, "3 disjoint tours started")

	all, perSlot := partitionOf(got)
	require.Equal(t, []string{"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"}, all,
		"the partition covers every market, with no duplicates (disjoint + complete)")
	for _, p := range perSlot {
		require.Len(t, p, 2, "6 markets split evenly across 3 probes")
	}
	// Each spawned tour flies ONLY its slot's partition — not all 6 markets.
	for _, cmd := range daemonClient.persistedTourCmds {
		require.Len(t, cmd.Markets, 2, "a probe tours its disjoint partition, not the whole system")
		require.Equal(t, 15*time.Minute, cmd.ScanInterval, "30m freshness halves to 15m for every probe on the post")
	}
}

// Partition STABILITY: with the hull budget unchanged, a second reconcile tick does
// NOT re-partition — the frozen partitions are reused and the VRP is not called again.
func TestScoutPost_MultiHull_PartitionStableAcrossTicks(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-KA42", Kind: domainScouting.PostKindStanding, Hulls: 3, FreshnessTarget: 30 * time.Minute}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sats := []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-KA42-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-KA42-A2"),
		newScoutTestSatellite(t, "SAT-3", "X1-KA42-A3"),
	}
	shipRepo := &fakeScoutShipRepo{ships: sats, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-KA42": {"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"},
	}}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{}}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: cq, marketProvider: mp, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	require.Equal(t, 1, rc.calls, "partitioned once on first sight of the 3-hull post")
	_, firstPartitions := partitionOf(postRepo.find("X1-KA42"))

	// Mark all spawned tours RUNNING so tick 2 leaves the healthy slots untouched.
	running := make([]persistence.ContainerSummary, 0, len(daemonClient.started))
	for _, id := range daemonClient.started {
		running = append(running, persistence.ContainerSummary{ID: id, Status: "RUNNING"})
	}
	cq.byStatus["RUNNING"] = running

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, 1, rc.calls, "a stable hull budget must NOT re-partition on later ticks")
	_, secondPartitions := partitionOf(postRepo.find("X1-KA42"))
	require.Equal(t, firstPartitions, secondPartitions, "the frozen partition is unchanged across ticks")
}

// Restart survival: a post reloaded from persistence with its partitions and hull
// assignments intact but its tours INTERRUPTED (not RUNNING) re-mans each probe onto
// the SAME partition without re-partitioning — a restart never triggers a mass re-tour.
func TestScoutPost_MultiHull_RestartReAdoptsSamePartitionNoRepartition(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	// The persisted (reloaded) post: 3 hulls, partitions frozen, hulls recorded on the
	// slots, tours pointing at now-dead (interrupted) containers.
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-KA42", Kind: domainScouting.PostKindStanding, Hulls: 3, FreshnessTarget: 30 * time.Minute,
		AssignedHull: "SAT-1", TourContainerID: "dead-0", PrimaryPartition: []string{"X1-KA42-M1", "X1-KA42-M4"},
		ExtraSlots: []domainScouting.ScoutPostSlot{
			{AssignedHull: "SAT-2", TourContainerID: "dead-1", Partition: []string{"X1-KA42-M2", "X1-KA42-M5"}},
			{AssignedHull: "SAT-3", TourContainerID: "dead-2", Partition: []string{"X1-KA42-M3", "X1-KA42-M6"}},
		},
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sats := []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-KA42-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-KA42-A2"),
		newScoutTestSatellite(t, "SAT-3", "X1-KA42-A3"),
	}
	// The hulls are stuck on the interrupted containers (claim preserved across restart).
	require.NoError(t, sats[0].AssignToContainer("dead-0", clock))
	require.NoError(t, sats[1].AssignToContainer("dead-1", clock))
	require.NoError(t, sats[2].AssignToContainer("dead-2", clock))
	shipRepo := &fakeScoutShipRepo{ships: sats, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-KA42": {"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"},
	}}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: &fakeContainerStatusQuery{}, marketProvider: mp, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, 0, rc.calls, "a reloaded, already-partitioned post must NOT re-partition on restart")
	got := postRepo.find("X1-KA42")
	require.Equal(t, []string{"X1-KA42-M1", "X1-KA42-M4"}, got.PrimaryPartition, "primary partition survives the restart")
	require.Equal(t, []string{"X1-KA42-M2", "X1-KA42-M5"}, got.ExtraSlots[0].Partition)
	require.Equal(t, []string{"X1-KA42-M3", "X1-KA42-M6"}, got.ExtraSlots[1].Partition)
	require.Len(t, daemonClient.started, 3, "all three interrupted tours are respawned over their same partitions")
	require.True(t, got.IsFullyManned(), "every slot re-manned")
	// Each respawned tour flies its original partition.
	for _, cmd := range daemonClient.persistedTourCmds {
		require.Len(t, cmd.Markets, 2)
	}
}

// Dead-slot re-man: when ONE probe of a 3-hull post dies, only that slot is re-manned
// (onto its SAME partition) and its siblings are left untouched — no re-partition.
func TestScoutPost_MultiHull_DeadSlotRemannedKeepsPartition(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-KA42", Kind: domainScouting.PostKindStanding, Hulls: 3, FreshnessTarget: 30 * time.Minute,
		AssignedHull: "SAT-1", TourContainerID: "live-0", PrimaryPartition: []string{"X1-KA42-M1", "X1-KA42-M4"},
		ExtraSlots: []domainScouting.ScoutPostSlot{
			{AssignedHull: "SAT-2", TourContainerID: "dead-1", Partition: []string{"X1-KA42-M2", "X1-KA42-M5"}},
			{AssignedHull: "SAT-3", TourContainerID: "live-2", Partition: []string{"X1-KA42-M3", "X1-KA42-M6"}},
		},
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sats := []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-KA42-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-KA42-A2"),
		newScoutTestSatellite(t, "SAT-3", "X1-KA42-A3"),
	}
	require.NoError(t, sats[0].AssignToContainer("live-0", clock))
	require.NoError(t, sats[1].AssignToContainer("dead-1", clock)) // this probe's tour died
	require.NoError(t, sats[2].AssignToContainer("live-2", clock))
	shipRepo := &fakeScoutShipRepo{ships: sats, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-KA42": {"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"},
	}}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "live-0", Status: "RUNNING"}, {ID: "live-2", Status: "RUNNING"}},
	}}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: cq, marketProvider: mp, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, 0, rc.calls, "a single dead probe must NOT trigger a re-partition")
	require.Contains(t, shipRepo.releases, "SAT-2", "the dead probe's hull is reclaimed")
	require.Len(t, daemonClient.started, 1, "only the dead slot is respawned; the two live tours are untouched")
	got := postRepo.find("X1-KA42")
	require.True(t, got.IsFullyManned(), "the post is fully manned again")
	require.Equal(t, "live-0", got.TourContainerID, "the healthy primary tour is untouched")
	require.Equal(t, "live-2", got.ExtraSlots[1].TourContainerID, "the healthy third tour is untouched")
	require.Equal(t, []string{"X1-KA42-M2", "X1-KA42-M5"}, got.ExtraSlots[0].Partition, "the re-manned slot keeps its exact partition")
	require.Equal(t, "SAT-2", got.ExtraSlots[0].AssignedHull, "the same idle in-system hull re-mans the slot")
}

// A single-hull post NEVER calls the VRP partitioner even when one is wired — the
// pre-enry path is untouched (byte-identical behavior).
func TestScoutPost_SingleHull_NeverPartitions(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding} // Hulls unset → single-hull
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: &fakeContainerStatusQuery{}, marketProvider: &fakeMarketProvider{}, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, 0, rc.calls, "a single-hull post must never partition")
	got := postRepo.find("X1-GZ7")
	require.Equal(t, "SAT-1", got.AssignedHull)
	require.Empty(t, got.ExtraSlots, "no extra slots are materialized for a single-hull post")
	require.Len(t, daemonClient.persistedTourCmds, 1)
	require.Equal(t, []string{"X1-GZ7-A1"}, daemonClient.persistedTourCmds[0].Markets, "the single probe tours ALL markets, not a partition")
}

// A hull-budget CHANGE re-partitions (requirement #1: "N changing recomputes"). A
// running 2-hull post whose budget is raised to 3 tears down its old tours and
// rebuilds 3 disjoint partitions, then re-mans them.
func TestScoutPost_MultiHull_BudgetChangeRepartitions(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	// Reloaded post: budget now 3, but only 1 extra slot materialized (was 2-hull).
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-KA42", Kind: domainScouting.PostKindStanding, Hulls: 3, FreshnessTarget: 30 * time.Minute,
		AssignedHull: "SAT-1", TourContainerID: "live-0", PrimaryPartition: []string{"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3"},
		ExtraSlots: []domainScouting.ScoutPostSlot{
			{AssignedHull: "SAT-2", TourContainerID: "live-1", Partition: []string{"X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"}},
		},
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sats := []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-KA42-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-KA42-A2"),
		newScoutTestSatellite(t, "SAT-3", "X1-KA42-A3"),
	}
	require.NoError(t, sats[0].AssignToContainer("live-0", clock))
	require.NoError(t, sats[1].AssignToContainer("live-1", clock))
	shipRepo := &fakeScoutShipRepo{ships: sats, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-KA42": {"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"},
	}}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: &fakeContainerStatusQuery{}, marketProvider: mp, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, 1, rc.calls, "the budget change re-partitions exactly once")
	require.Contains(t, daemonClient.stopped, "live-0", "the old primary tour is stopped for re-partition")
	require.Contains(t, daemonClient.stopped, "live-1", "the old extra tour is stopped for re-partition")
	got := postRepo.find("X1-KA42")
	require.Len(t, got.ExtraSlots, 2, "the post now has 3 slots (primary + 2 extra)")
	all, perSlot := partitionOf(got)
	require.Equal(t, []string{"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"}, all)
	for _, p := range perSlot {
		require.Len(t, p, 2, "6 markets now split across 3 probes")
	}
}

// Reducing a multi-probe post to a single hull reverts it to the pre-enry single-slot
// shape and frees the surplus probes to the pool (no stale extra slots linger).
func TestScoutPost_MultiHull_RevertToSingleHullFreesSurplus(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-KA42", Kind: domainScouting.PostKindStanding, Hulls: 1, FreshnessTarget: 30 * time.Minute,
		AssignedHull: "SAT-1", TourContainerID: "live-0", PrimaryPartition: []string{"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3"},
		ExtraSlots: []domainScouting.ScoutPostSlot{
			{AssignedHull: "SAT-2", TourContainerID: "live-1", Partition: []string{"X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"}},
		},
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sats := []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-KA42-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-KA42-A2"),
	}
	require.NoError(t, sats[0].AssignToContainer("live-0", clock))
	require.NoError(t, sats[1].AssignToContainer("live-1", clock))
	shipRepo := &fakeScoutShipRepo{ships: sats, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-KA42": {"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"},
	}}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: &fakeContainerStatusQuery{}, marketProvider: mp, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, 0, rc.calls, "reverting to single-hull needs no VRP")
	require.Contains(t, shipRepo.releases, "SAT-2", "the surplus probe is freed to the pool")
	got := postRepo.find("X1-KA42")
	require.Empty(t, got.ExtraSlots, "no stale extra slots linger after the revert")
	require.Empty(t, got.PrimaryPartition, "the primary reverts to touring all markets")
	// After teardown the primary is re-manned over ALL markets in the same tick.
	require.Equal(t, 1, got.MannedCount())
	require.Len(t, daemonClient.persistedTourCmds, 1)
	require.Len(t, daemonClient.persistedTourCmds[0].Markets, 6, "the single probe tours all markets again")
}

// ---- tests: sp-ykhl market-set drift (debounced re-cut) -------------------
//
// enry's partition is stable on hull-count alone: it never re-cuts just because a tick
// passed. But the market SET is a partition input too — nn0y virgin discovery keeps
// adding markets to a system after a post's tours are already cut, and a market
// discovered post-cut belongs to NO partition, so it goes permanently stale even though
// the post reads fully manned. These tests cover the debounced fix: a partitioned
// (hulls>1) post re-cuts once accumulated drift (additions AND removals, same set, no
// special-casing) reaches a count threshold OR has been pending at least a max age —
// whichever fires first — while an unchanged market set re-cuts exactly as rarely as an
// unchanged hull budget does (never).

// Debounced re-cut STABILITY: with both the hull budget AND the market set unchanged,
// repeated reconcile ticks must NOT re-partition and must NOT even open a pending-drift
// episode — the sp-enry "same hulls, same markets ⇒ zero re-cuts" invariant holds under
// the new drift-aware code path, not just the old budget-only one.
func TestScoutPost_MultiHull_MarketDriftStableWhenUnchanged(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-KA42", Kind: domainScouting.PostKindStanding, Hulls: 3, FreshnessTarget: 30 * time.Minute}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sats := []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-KA42-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-KA42-A2"),
		newScoutTestSatellite(t, "SAT-3", "X1-KA42-A3"),
	}
	shipRepo := &fakeScoutShipRepo{ships: sats, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-KA42": {"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"},
	}}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{}}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: cq, marketProvider: mp, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	require.Equal(t, 1, rc.calls, "partitioned once on first sight of the 3-hull post")

	running := make([]persistence.ContainerSummary, 0, len(daemonClient.started))
	for _, id := range daemonClient.started {
		running = append(running, persistence.ContainerSummary{ID: id, Status: "RUNNING"})
	}
	cq.byStatus["RUNNING"] = running

	for i := 0; i < 3; i++ {
		require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	}

	require.Equal(t, 1, rc.calls, "an unchanged market set must never re-cut, across any number of ticks")
	require.Empty(t, handler.driftPendingSince, "no drift episode is ever opened when the market set never differs from the partition union")
}

// Debounce COUNT floor: a market discovered post-cut, while still below BOTH the count
// threshold and the age ceiling, must not trigger a re-cut yet — it is tracked as
// pending, not silently dropped and not immediately fixed either.
func TestScoutPost_MultiHull_MarketDriftBelowThresholdAndAgeNoRecut(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-KA42", Kind: domainScouting.PostKindStanding, Hulls: 3, FreshnessTarget: 30 * time.Minute}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sats := []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-KA42-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-KA42-A2"),
		newScoutTestSatellite(t, "SAT-3", "X1-KA42-A3"),
	}
	shipRepo := &fakeScoutShipRepo{ships: sats, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-KA42": {"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"},
	}}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{}}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: cq, marketProvider: mp, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	require.Equal(t, 1, rc.calls)
	running := make([]persistence.ContainerSummary, 0, len(daemonClient.started))
	for _, id := range daemonClient.started {
		running = append(running, persistence.ContainerSummary{ID: id, Status: "RUNNING"})
	}
	cq.byStatus["RUNNING"] = running

	// One new market appears — below the default count threshold (2) — no time passes.
	mp.markets["X1-KA42"] = append(mp.markets["X1-KA42"], "X1-KA42-M7")

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, 1, rc.calls, "one market below both the count threshold and the age ceiling must not re-cut")
	_, perSlot := partitionOf(postRepo.find("X1-KA42"))
	found := false
	for _, p := range perSlot {
		for _, m := range p {
			if m == "X1-KA42-M7" {
				found = true
			}
		}
	}
	require.False(t, found, "the new market is not yet in any partition — it is pending, not silently dropped")
	require.NotEmpty(t, handler.driftPendingSince, "the drift is tracked as pending even though it hasn't fired yet")
}

// Debounce COUNT trigger: once the accumulated new-market count reaches the configured
// threshold (default 2), the post re-cuts on that very tick — no time needs to pass —
// and the new markets join the fresh partition exactly once each.
func TestScoutPost_MultiHull_MarketDriftThresholdTriggersRecut(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-KA42", Kind: domainScouting.PostKindStanding, Hulls: 3, FreshnessTarget: 30 * time.Minute}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sats := []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-KA42-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-KA42-A2"),
		newScoutTestSatellite(t, "SAT-3", "X1-KA42-A3"),
	}
	shipRepo := &fakeScoutShipRepo{ships: sats, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-KA42": {"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"},
	}}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{}}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: cq, marketProvider: mp, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	require.Equal(t, 1, rc.calls)
	running := make([]persistence.ContainerSummary, 0, len(daemonClient.started))
	for _, id := range daemonClient.started {
		running = append(running, persistence.ContainerSummary{ID: id, Status: "RUNNING"})
	}
	cq.byStatus["RUNNING"] = running

	// Two new markets appear — reaching the default threshold — no time needs to pass.
	mp.markets["X1-KA42"] = append(mp.markets["X1-KA42"], "X1-KA42-M7", "X1-KA42-M8")

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, 2, rc.calls, "reaching the count threshold re-cuts exactly once")
	got := postRepo.find("X1-KA42")
	all, perSlot := partitionOf(got)
	require.Equal(t, []string{"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6", "X1-KA42-M7", "X1-KA42-M8"}, all,
		"the re-cut partition covers the full new market set, disjointly")
	m7Count, m8Count := 0, 0
	for _, p := range perSlot {
		for _, m := range p {
			if m == "X1-KA42-M7" {
				m7Count++
			}
			if m == "X1-KA42-M8" {
				m8Count++
			}
		}
	}
	require.Equal(t, 1, m7Count, "the new market lands in exactly one partition")
	require.Equal(t, 1, m8Count, "the new market lands in exactly one partition")
	require.Empty(t, handler.driftPendingSince, "the re-cut resolves and clears the pending-drift episode")
}

// Debounce AGE trigger: a single new market that never reaches the count threshold on
// its own still forces a re-cut once it has been pending at least the configured max age
// (default 60m) — nn0y virgin discovery must not be able to strand a market forever just
// by trickling markets in one at a time.
func TestScoutPost_MultiHull_MarketDriftAgeTriggersRecut(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-KA42", Kind: domainScouting.PostKindStanding, Hulls: 3, FreshnessTarget: 30 * time.Minute}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sats := []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-KA42-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-KA42-A2"),
		newScoutTestSatellite(t, "SAT-3", "X1-KA42-A3"),
	}
	shipRepo := &fakeScoutShipRepo{ships: sats, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-KA42": {"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"},
	}}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{}}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: cq, marketProvider: mp, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	require.Equal(t, 1, rc.calls)
	running := make([]persistence.ContainerSummary, 0, len(daemonClient.started))
	for _, id := range daemonClient.started {
		running = append(running, persistence.ContainerSummary{ID: id, Status: "RUNNING"})
	}
	cq.byStatus["RUNNING"] = running

	// One new market appears — never reaches the count threshold on its own.
	mp.markets["X1-KA42"] = append(mp.markets["X1-KA42"], "X1-KA42-M7")

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	require.Equal(t, 1, rc.calls, "one market below threshold does not yet re-cut")

	clock.Advance(61 * time.Minute)
	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, 2, rc.calls, "a single market past the max age re-cuts even though it never reached the count threshold")
	_, perSlot := partitionOf(postRepo.find("X1-KA42"))
	found := false
	for _, p := range perSlot {
		for _, m := range p {
			if m == "X1-KA42-M7" {
				found = true
			}
		}
	}
	require.True(t, found, "the long-waiting market is now in a partition")
	require.Empty(t, handler.driftPendingSince, "the re-cut resolves and clears the pending-drift episode")
}

// Debounce folds REMOVALS into the same drift check as additions: a market that
// disappears from the discovered set just drops out of the union — no special casing —
// and counts toward the same threshold/age trigger.
func TestScoutPost_MultiHull_MarketRemovalTriggersDrift(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-KA42", Kind: domainScouting.PostKindStanding, Hulls: 3, FreshnessTarget: 30 * time.Minute}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sats := []*navigation.Ship{
		newScoutTestSatellite(t, "SAT-1", "X1-KA42-A1"),
		newScoutTestSatellite(t, "SAT-2", "X1-KA42-A2"),
		newScoutTestSatellite(t, "SAT-3", "X1-KA42-A3"),
	}
	shipRepo := &fakeScoutShipRepo{ships: sats, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-KA42": {"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4", "X1-KA42-M5", "X1-KA42-M6"},
	}}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{}}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: cq, marketProvider: mp, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	require.Equal(t, 1, rc.calls)
	running := make([]persistence.ContainerSummary, 0, len(daemonClient.started))
	for _, id := range daemonClient.started {
		running = append(running, persistence.ContainerSummary{ID: id, Status: "RUNNING"})
	}
	cq.byStatus["RUNNING"] = running

	// Two markets stop being discovered (e.g. no longer carry a marketplace trait) —
	// removals reach the same default count threshold as additions.
	mp.markets["X1-KA42"] = []string{"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4"}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, 2, rc.calls, "removals reaching the threshold re-cut exactly like additions")
	got := postRepo.find("X1-KA42")
	all, _ := partitionOf(got)
	require.Equal(t, []string{"X1-KA42-M1", "X1-KA42-M2", "X1-KA42-M3", "X1-KA42-M4"}, all,
		"the removed markets are absent from every partition after the re-cut")
	require.Empty(t, handler.driftPendingSince, "the re-cut resolves and clears the pending-drift episode")
}

// hulls=1 finding: a single-hull post's ensurePartitions is a pure no-op regardless of
// how much its market set grows — it never opens a drift episode and never calls the VRP
// partitioner. (The single probe's OWN tour still snapshots its market list once at
// spawn rather than re-reading it per circuit — see scout_tour.go's rotateTourToStart /
// ScoutTourCommand.Markets — so the SAME staleness this bead fixes for partitioned posts
// can still occur for a single-hull post's tour. That snapshot is a different code path
// than partitioning, out of this bead's explicit scope ("a partitioned (hulls>1) post"),
// and is reported rather than fixed here.)
func TestScoutPost_SingleHull_MarketGrowthNeverTriggersPartitionOrDrift(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding} // Hulls unset → single-hull
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{
		"X1-GZ7": {"X1-GZ7-M1", "X1-GZ7-M2"},
	}}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{}}
	rc := &fakeScoutRoutingClient{}
	handler := &RunScoutPostCoordinatorHandler{
		postRepo: postRepo, shipRepo: shipRepo, daemonClient: daemonClient,
		containerQuery: cq, marketProvider: mp, clock: clock,
		routingClient: rc, repositionBackoffUntil: map[string]time.Time{},
	}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	require.Equal(t, 0, rc.calls)
	running := make([]persistence.ContainerSummary, 0, len(daemonClient.started))
	for _, id := range daemonClient.started {
		running = append(running, persistence.ContainerSummary{ID: id, Status: "RUNNING"})
	}
	cq.byStatus["RUNNING"] = running

	// The market set grows well past both the count threshold and (via clock.Advance)
	// the age ceiling — a partitioned post would re-cut; a single-hull post must not care.
	mp.markets["X1-GZ7"] = append(mp.markets["X1-GZ7"], "X1-GZ7-M3", "X1-GZ7-M4", "X1-GZ7-M5")
	clock.Advance(2 * time.Hour)

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Equal(t, 0, rc.calls, "a single-hull post never partitions, no matter how much its market set grows")
	got := postRepo.find("X1-GZ7")
	require.Empty(t, got.ExtraSlots, "still no extra slots")
	require.Empty(t, got.PrimaryPartition, "still no partition — the single probe tours all markets directly")
	require.Empty(t, handler.driftPendingSince, "single-hull posts never open a drift episode")
}

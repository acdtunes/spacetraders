package commands

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// ---- fakes for the scout-markets transactional-reset tests (sp-8k9m) --------

// fakeMarketsShipRepo is a ShipRepository over a fixed roster. It records every
// ForceRelease (a Save of a now-idle hull) so a test can assert the reset never
// released — i.e. abandoned — a hull it could not re-man.
type fakeMarketsShipRepo struct {
	navigation.ShipRepository
	ships      []*navigation.Ship
	findAllErr error
	released   []string
}

func (r *fakeMarketsShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	if r.findAllErr != nil {
		return nil, r.findAllErr
	}
	return r.ships, nil
}

func (r *fakeMarketsShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	for _, s := range r.ships {
		if s.ShipSymbol() == symbol {
			return s, nil
		}
	}
	return nil, fmt.Errorf("ship %s not found", symbol)
}

func (r *fakeMarketsShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	if ship.IsIdle() {
		r.released = append(r.released, ship.ShipSymbol())
	}
	return nil
}

// fakeMarketsDaemon records StopContainer + CreateScoutTourContainer calls and can
// fail the create to model a respawn that fails at the commit step.
type fakeMarketsDaemon struct {
	daemon.DaemonClient
	stopped   []string
	created   []string
	createErr error
}

func (d *fakeMarketsDaemon) StopContainer(_ context.Context, containerID string) error {
	d.stopped = append(d.stopped, containerID)
	return nil
}

func (d *fakeMarketsDaemon) CreateScoutTourContainer(_ context.Context, containerID string, _ uint, _ interface{}) error {
	if d.createErr != nil {
		return d.createErr
	}
	d.created = append(d.created, containerID)
	return nil
}

// fakeMarketsGraph returns a one-marketplace system graph, or an injected error.
type fakeMarketsGraph struct {
	system.ISystemGraphProvider
	err error
}

func (g *fakeMarketsGraph) GetGraph(_ context.Context, systemSymbol string, _ bool, _ int) (*system.GraphLoadResult, error) {
	if g.err != nil {
		return nil, g.err
	}
	wp, err := shared.NewWaypoint(systemSymbol+"-A1", 0, 0)
	if err != nil {
		return nil, err
	}
	return &system.GraphLoadResult{
		Graph: &system.NavigationGraph{
			SystemSymbol: systemSymbol,
			Waypoints:    map[string]*shared.Waypoint{wp.Symbol: wp},
		},
		Source: "api",
	}, nil
}

// fakeMarketsRouting partitions one market per ship, errors, or (empty) returns a VRP
// response that assigns NOTHING without erroring — the degenerate-plan case (sp-8k9m).
type fakeMarketsRouting struct {
	routing.RoutingClient
	err   error
	empty bool
}

func (c *fakeMarketsRouting) PartitionFleet(_ context.Context, req *routing.VRPRequest) (*routing.VRPResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.empty {
		return &routing.VRPResponse{Assignments: map[string]*routing.ShipTourData{}}, nil
	}
	assignments := make(map[string]*routing.ShipTourData, len(req.ShipSymbols))
	for i, ship := range req.ShipSymbols {
		wp := req.MarketWaypoints[i%len(req.MarketWaypoints)]
		assignments[ship] = &routing.ShipTourData{Waypoints: []string{wp}}
	}
	return &routing.VRPResponse{Assignments: assignments}, nil
}

func newMannedScoutShip(t *testing.T, symbol, waypoint, containerID string, clock shared.Clock) *navigation.Ship {
	t.Helper()
	ship := newScoutTestSatellite(t, symbol, waypoint)
	require.NoError(t, ship.AssignToContainer(containerID, clock))
	require.True(t, ship.IsAssigned(), "fixture hull must start manned on its old container")
	return ship
}

// The C81/SN21 shape (sp-8k9m defect #2): two hulls already manning a system's posts.
// `scout all-markets` was run to re-partition them; the VRP that computes the new tours
// FAILS. A non-transactional reset stops the old tours and releases the hulls BEFORE that
// failure, so the system goes dark with nothing to re-man it. The reset must instead
// compute the whole re-man plan first and, on failure, leave the old posts running and
// report the failure honestly — never a teardown it cannot follow with a re-man.
func TestScoutMarkets_RepartitionPlanFails_KeepsOldPostsRunning(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	shipA := newMannedScoutShip(t, "SAT-A", "X1-C81-A1", "old-tour-A", clock)
	shipB := newMannedScoutShip(t, "SAT-B", "X1-C81-B1", "old-tour-B", clock)
	shipRepo := &fakeMarketsShipRepo{ships: []*navigation.Ship{shipA, shipB}}
	daemonC := &fakeMarketsDaemon{}
	handler := NewScoutMarketsHandler(
		shipRepo,
		&fakeMarketsGraph{},
		&fakeMarketsRouting{err: fmt.Errorf("VRP partition failed")},
		daemonC,
		clock,
	)

	cmd := &ScoutMarketsCommand{
		PlayerID:     shared.MustNewPlayerID(1),
		ShipSymbols:  []string{"SAT-A", "SAT-B"},
		SystemSymbol: "X1-C81",
		Markets:      []string{"X1-C81-A1", "X1-C81-B1"},
		Iterations:   -1,
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.Error(t, err, "a failed re-man plan must surface as an honest error")
	require.Empty(t, daemonC.stopped, "old scout containers must NOT be stopped when the re-man plan fails")
	require.Empty(t, shipRepo.released, "no hull may be released when it cannot be re-manned")
	require.True(t, shipA.IsAssigned(), "SAT-A stays on its old container (C81 stays manned)")
	require.True(t, shipB.IsAssigned(), "SAT-B stays on its old container (C81 stays manned)")
}

// The system-graph read for the re-partition fails. Same invariant: nothing is torn down.
func TestScoutMarkets_GraphLoadFails_KeepsOldPostsRunning(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	shipA := newMannedScoutShip(t, "SAT-A", "X1-SN21-A1", "old-tour-A", clock)
	shipB := newMannedScoutShip(t, "SAT-B", "X1-SN21-B1", "old-tour-B", clock)
	shipRepo := &fakeMarketsShipRepo{ships: []*navigation.Ship{shipA, shipB}}
	daemonC := &fakeMarketsDaemon{}
	handler := NewScoutMarketsHandler(
		shipRepo,
		&fakeMarketsGraph{err: fmt.Errorf("graph unavailable")},
		&fakeMarketsRouting{},
		daemonC,
		clock,
	)

	cmd := &ScoutMarketsCommand{
		PlayerID:     shared.MustNewPlayerID(1),
		ShipSymbols:  []string{"SAT-A", "SAT-B"},
		SystemSymbol: "X1-SN21",
		Markets:      []string{"X1-SN21-A1", "X1-SN21-B1"},
		Iterations:   -1,
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.Error(t, err)
	require.Empty(t, daemonC.stopped, "old scout containers must NOT be stopped when the graph read fails")
	require.Empty(t, shipRepo.released, "no hull may be released when it cannot be re-manned")
}

// A requested hull is missing from the fleet: the re-man plan cannot be built, so — again —
// nothing is torn down. (The pre-fix reset stopped the hulls it COULD find before erroring.)
func TestScoutMarkets_ShipMissing_KeepsOldPostsRunning(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	shipA := newMannedScoutShip(t, "SAT-A", "X1-C81-A1", "old-tour-A", clock)
	shipRepo := &fakeMarketsShipRepo{ships: []*navigation.Ship{shipA}}
	daemonC := &fakeMarketsDaemon{}
	handler := NewScoutMarketsHandler(
		shipRepo,
		&fakeMarketsGraph{},
		&fakeMarketsRouting{},
		daemonC,
		clock,
	)

	cmd := &ScoutMarketsCommand{
		PlayerID:     shared.MustNewPlayerID(1),
		ShipSymbols:  []string{"SAT-A", "SAT-GONE"},
		SystemSymbol: "X1-C81",
		Markets:      []string{"X1-C81-A1", "X1-C81-B1"},
		Iterations:   -1,
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.Error(t, err)
	require.Empty(t, daemonC.stopped, "the found hull must NOT be stopped when a sibling is missing")
	require.Empty(t, shipRepo.released, "no hull released when the re-man plan is incomplete")
	require.True(t, shipA.IsAssigned(), "SAT-A stays manned")
}

// An empty market set is a no-op reset: there is nothing to re-man toward, so the reset
// must not stop the existing posts (the pre-fix code stopped them, then early-returned on
// the empty markets — the exact reset-without-re-man that darkened C81/SN21).
func TestScoutMarkets_EmptyMarkets_DoesNotStopPosts(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	shipA := newMannedScoutShip(t, "SAT-A", "X1-C81-A1", "old-tour-A", clock)
	shipRepo := &fakeMarketsShipRepo{ships: []*navigation.Ship{shipA}}
	daemonC := &fakeMarketsDaemon{}
	handler := NewScoutMarketsHandler(shipRepo, &fakeMarketsGraph{}, &fakeMarketsRouting{}, daemonC, clock)

	cmd := &ScoutMarketsCommand{
		PlayerID:     shared.MustNewPlayerID(1),
		ShipSymbols:  []string{"SAT-A"},
		SystemSymbol: "X1-C81",
		Markets:      []string{},
		Iterations:   -1,
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.NoError(t, err, "an empty-market reset is a benign no-op")
	require.Empty(t, daemonC.stopped, "an empty-market reset must not stop any existing post")
	require.Empty(t, shipRepo.released, "an empty-market reset must not release any hull")
	require.True(t, shipA.IsAssigned(), "SAT-A stays manned")
}

// A VRP that returns an EMPTY partition (assigns no ship any market) without erroring is a
// degenerate plan, not a re-man (sp-8k9m finding b). The reset must refuse LOUDLY before any
// teardown — never stop the posts and report "complete" while darkening the system.
func TestScoutMarkets_DegenerateEmptyPlan_KeepsOldPostsRunning(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	shipA := newMannedScoutShip(t, "SAT-A", "X1-C81-A1", "old-tour-A", clock)
	shipB := newMannedScoutShip(t, "SAT-B", "X1-C81-B1", "old-tour-B", clock)
	shipRepo := &fakeMarketsShipRepo{ships: []*navigation.Ship{shipA, shipB}}
	daemonC := &fakeMarketsDaemon{}
	handler := NewScoutMarketsHandler(shipRepo, &fakeMarketsGraph{}, &fakeMarketsRouting{empty: true}, daemonC, clock)

	cmd := &ScoutMarketsCommand{
		PlayerID:     shared.MustNewPlayerID(1),
		ShipSymbols:  []string{"SAT-A", "SAT-B"},
		SystemSymbol: "X1-C81",
		Markets:      []string{"X1-C81-A1", "X1-C81-B1"},
		Iterations:   -1,
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.Error(t, err, "an empty-plan reset must fail loudly, never report success on zero re-man")
	require.Empty(t, daemonC.stopped, "the old posts must NOT be stopped for an empty plan")
	require.Empty(t, daemonC.created, "no new tours are spawned")
	require.Empty(t, shipRepo.released, "no hull is released")
}

// sp-8k9m finding f: a cross-system assignment (a hull at KN67 handed markets in PA62) is a
// doomed tour — NavigateRoute is in-system, so it crash-loops on the foreign waypoint and
// the hull sits claimed but idle (the 7 stuck KN67 probes). The reset must REFUSE it at the
// spawn seam, before any teardown, never spawn it and never darken the old post.
func TestScoutMarkets_CrossSystemAssignment_RefusedAtSpawn(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	sat := newMannedScoutShip(t, "SAT-1", "X1-KN67-ZF4D", "old-tour-1", clock) // hull sits in KN67
	shipRepo := &fakeMarketsShipRepo{ships: []*navigation.Ship{sat}}
	daemonC := &fakeMarketsDaemon{}
	handler := NewScoutMarketsHandler(shipRepo, &fakeMarketsGraph{}, &fakeMarketsRouting{}, daemonC, clock)

	cmd := &ScoutMarketsCommand{
		PlayerID:     shared.MustNewPlayerID(1),
		ShipSymbols:  []string{"SAT-1"},
		SystemSymbol: "X1-PA62",
		Markets:      []string{"X1-PA62-C23A"}, // a market the KN67 hull cannot navigate to
		Iterations:   -1,
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.Error(t, err, "a cross-system assignment must be refused, never spawned into a crash-loop")
	require.Contains(t, err.Error(), "cross-system", "the refusal names the cross-system violation")
	require.Empty(t, daemonC.stopped, "the old post must NOT be torn down for a doomed cross-system tour")
	require.Empty(t, daemonC.created, "no cross-system tour is spawned")
	require.Empty(t, shipRepo.released, "the hull is not released")
	require.True(t, sat.IsAssigned(), "the hull stays on its old container")
}

// The happy path: with a computable plan the reset DOES stop the old tour and spawn a new
// one. Pins that the transactional reorder still performs the swap when the re-man is real.
func TestScoutMarkets_PlanReady_StopsOldAndSpawnsNew(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	shipA := newMannedScoutShip(t, "SAT-A", "X1-C81-A1", "old-tour-A", clock)
	shipRepo := &fakeMarketsShipRepo{ships: []*navigation.Ship{shipA}}
	daemonC := &fakeMarketsDaemon{}
	handler := NewScoutMarketsHandler(shipRepo, &fakeMarketsGraph{}, &fakeMarketsRouting{}, daemonC, clock)

	cmd := &ScoutMarketsCommand{
		PlayerID:     shared.MustNewPlayerID(1),
		ShipSymbols:  []string{"SAT-A"},
		SystemSymbol: "X1-C81",
		Markets:      []string{"X1-C81-A1"},
		Iterations:   -1,
	}

	resp, err := handler.Handle(context.Background(), cmd)

	require.NoError(t, err)
	require.Contains(t, daemonC.stopped, "old-tour-A", "the old tour is stopped once the re-man is guaranteed")
	require.Len(t, daemonC.created, 1, "a fresh scout tour is spawned for the reset hull")
	r, ok := resp.(*ScoutMarketsResponse)
	require.True(t, ok)
	require.Len(t, r.ContainerIDs, 1, "the response reports the newly spawned tour")
}

package navigation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	shipApp "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// sp-9l4p — cross-system navigation resolve + auto-sync + gate-crossing delegation.
//
// A NavigateRouteCommand whose destination lives in a DIFFERENT system than the ship
// used to fail-close at validateWaypointCache ("waypoint <dest> not found in cache for
// system <current>"): the intra-system OR-Tools planner and the raw /navigate API can
// only move a hull WITHIN one system. That single defect stalled a live copper contract
// (net -90k) AND capped the frontier depth campaign — the target-aware probe-buy
// navigates an idle hull from home to a frontier shipyard and fail-closed every cycle.
//
// The fix resolves the destination in ITS OWN system (auto-syncing that system's graph
// on-demand via the shared provider, cache-first + bounded), then delegates the physical
// gate-crossing move to the shared travel machinery (RepositionToWaypoint). Both victims
// converge on THIS handler, so one shared-seam change fixes both. These tests drive the
// real Handle (the driving port); doubles sit only at the driven-port boundaries — the
// system-graph provider, the waypoint repository, and the cross-system router.

// fakeGraphProvider fakes system.ISystemGraphProvider. It serves a per-system
// NavigationGraph and records every GetGraph call so a test can assert WHICH system was
// synced and whether a force-refresh was needed. It models the real provider's
// cache-first contract: a system is FETCHED (one API build) on its first non-forced miss
// OR any forced refresh, and a later cache-first read is served from the built graph with
// no extra fetch — the frugality property the cross-system auto-sync relies on.
type fakeGraphProvider struct {
	graphs     map[string]*system.NavigationGraph
	built      map[string]bool
	buildCount map[string]int
	forceCount map[string]int
	err        error
}

func newFakeGraphProvider() *fakeGraphProvider {
	return &fakeGraphProvider{
		graphs:     map[string]*system.NavigationGraph{},
		built:      map[string]bool{},
		buildCount: map[string]int{},
		forceCount: map[string]int{},
	}
}

func (f *fakeGraphProvider) GetGraph(_ context.Context, systemSymbol string, forceRefresh bool, _ int) (*system.GraphLoadResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	if forceRefresh {
		f.forceCount[systemSymbol]++
	}
	if forceRefresh || !f.built[systemSymbol] {
		f.buildCount[systemSymbol]++
		f.built[systemSymbol] = true
	}
	return &system.GraphLoadResult{Graph: f.graphs[systemSymbol], Source: "test"}, nil
}

// fakeWaypointRepo is the driven WaypointRepository the enricher reads; ListBySystem
// returns nothing so EnrichGraphWaypoints falls back to the graph's own waypoints (the
// trait-enrichment DB layer is irrelevant to the cross-system resolve under test).
type fakeWaypointRepo struct {
	system.WaypointRepository
}

func (f *fakeWaypointRepo) ListBySystem(_ context.Context, _ string) ([]*shared.Waypoint, error) {
	return nil, nil
}

// fakeNavShipRepo returns a fixed ship for FindBySymbol (the handler both loads the ship
// and, on the delegated path, reloads it for the arrival response).
type fakeNavShipRepo struct {
	domainNavigation.ShipRepository
	ship *domainNavigation.Ship
}

func (f *fakeNavShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	return f.ship, nil
}

func newCrossSystemTestShip(t *testing.T, symbol, locationSymbol string) *domainNavigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(locationSymbol, 1, 1)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	ship, err := domainNavigation.NewShip(
		symbol, shared.MustNewPlayerID(1), loc, fuel, 100, 0, cargo, 30,
		"FRAME_FRIGATE", "COMMAND", nil, domainNavigation.NavStatusInOrbit,
	)
	require.NoError(t, err)
	return ship
}

func graphWith(t *testing.T, systemSymbol string, waypointSymbols ...string) *system.NavigationGraph {
	t.Helper()
	g := system.NewNavigationGraph(systemSymbol)
	for _, s := range waypointSymbols {
		wp, err := shared.NewWaypoint(s, 1, 1)
		require.NoError(t, err)
		g.AddWaypoint(wp)
	}
	return g
}

// handlerFor builds a NavigateRouteHandler wired with the driven doubles under test. The
// route planner and executor are nil: the delegated cross-system path returns before
// reaching them, and the fail-closed path returns at validateWaypointCache before them.
func handlerFor(shipRepo domainNavigation.ShipRepository, graphProvider system.ISystemGraphProvider) *NavigateRouteHandler {
	enricher := shipApp.NewWaypointEnricher(&fakeWaypointRepo{})
	return NewNavigateRouteHandler(shipRepo, graphProvider, enricher, nil, nil)
}

// AC1 (the live bug) + AC5 (the frontier probe-buy shared resolver): a hull at X1-VB74
// navigating to the frontier shipyard X1-KN83-AF7C — a system NOT yet cached — auto-syncs
// X1-KN83, confirms the waypoint is real, and delegates the physical move to the shared
// gate-crossing travel machinery. RED before the fix: today this fail-closes with the
// exact "not found in cache for system X1-VB74" error.
func TestNavigateRoute_CrossSystem_AutoSyncsAndDelegatesToGateCrossing(t *testing.T) {
	const (
		shipSymbol = "TORWIND-E"
		origin     = "X1-VB74-J1"
		frontier   = "X1-KN83-AF7C" // the live sp-9l4p frontier shipyard
	)
	graph := newFakeGraphProvider()
	graph.graphs["X1-VB74"] = graphWith(t, "X1-VB74", origin)
	graph.graphs["X1-KN83"] = graphWith(t, "X1-KN83", frontier)

	router := &fakeCrossSystemRouter{}
	handler := handlerFor(&fakeNavShipRepo{ship: newCrossSystemTestShip(t, shipSymbol, origin)}, graph)
	handler.WithCrossSystemRouter(router)

	resp, err := handler.Handle(context.Background(), &NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: frontier,
		PlayerID:    shared.MustNewPlayerID(1),
	})

	require.NoError(t, err, "a cross-system destination must no longer fail-close")
	require.Equal(t, []string{shipSymbol + "->" + frontier}, router.calls,
		"the physical move is delegated to the shared gate-crossing travel machinery")
	require.Equal(t, 1, graph.buildCount["X1-KN83"], "the destination system's graph is auto-synced")
	navResp, ok := resp.(*NavigateRouteResponse)
	require.True(t, ok)
	require.Equal(t, "completed", navResp.Status)
	require.Equal(t, frontier, navResp.CurrentLocation, "navigation proceeds to the cross-system destination")
}

// Mutation / load-bearing check: the SAME cross-system navigation with NO router wired
// fail-closes with the exact pre-fix error. Removing the delegation makes AC1 fail-closed
// again — the fix, not the test scaffolding, is what unblocks the move.
func TestNavigateRoute_CrossSystem_NoRouterWired_FailsClosed(t *testing.T) {
	const (
		shipSymbol = "TORWIND-E"
		origin     = "X1-VB74-J1"
		frontier   = "X1-KN83-AF7C"
	)
	graph := newFakeGraphProvider()
	graph.graphs["X1-VB74"] = graphWith(t, "X1-VB74", origin)
	graph.graphs["X1-KN83"] = graphWith(t, "X1-KN83", frontier)

	handler := handlerFor(&fakeNavShipRepo{ship: newCrossSystemTestShip(t, shipSymbol, origin)}, graph)
	// intentionally NOT wiring a cross-system router

	_, err := handler.Handle(context.Background(), &NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: frontier,
		PlayerID:    shared.MustNewPlayerID(1),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in cache for system X1-VB74",
		"with no router the handler is byte-identical to the pre-fix fail-closed behaviour")
}

// AC2: when the destination's own system is ALREADY cached, the waypoint resolves from a
// cache-first read and the move is delegated with NO redundant API sync (no force-refresh,
// no fetch).
func TestNavigateRoute_CrossSystem_AlreadyCached_NoRedundantSync(t *testing.T) {
	const (
		origin   = "X1-VB74-J1"
		frontier = "X1-KN83-AF7C"
	)
	graph := newFakeGraphProvider()
	graph.graphs["X1-KN83"] = graphWith(t, "X1-KN83", frontier)
	graph.built["X1-KN83"] = true // already synced this cycle

	router := &fakeCrossSystemRouter{}
	handler := handlerFor(&fakeNavShipRepo{ship: newCrossSystemTestShip(t, "SCOUT-1", origin)}, graph)
	handler.WithCrossSystemRouter(router)

	_, err := handler.Handle(context.Background(), &NavigateRouteCommand{
		ShipSymbol:  "SCOUT-1",
		Destination: frontier,
		PlayerID:    shared.MustNewPlayerID(1),
	})

	require.NoError(t, err)
	require.Len(t, router.calls, 1, "the cross-system move is still delegated")
	require.Equal(t, 0, graph.forceCount["X1-KN83"], "a cached destination system is not force-refreshed")
	require.Equal(t, 0, graph.buildCount["X1-KN83"], "a cached destination system triggers no redundant API fetch")
}

// AC4 (frugality): the same cross-system target navigated twice in a window syncs its
// system at most ONCE — the second resolve is served cache-first, so a cross-system miss
// never becomes a per-tick API storm.
func TestNavigateRoute_CrossSystem_SyncsAtMostOncePerWindow(t *testing.T) {
	const (
		origin   = "X1-VB74-J1"
		frontier = "X1-KN83-AF7C"
	)
	graph := newFakeGraphProvider()
	graph.graphs["X1-KN83"] = graphWith(t, "X1-KN83", frontier)

	router := &fakeCrossSystemRouter{}
	handler := handlerFor(&fakeNavShipRepo{ship: newCrossSystemTestShip(t, "SCOUT-1", origin)}, graph)
	handler.WithCrossSystemRouter(router)

	cmd := &NavigateRouteCommand{ShipSymbol: "SCOUT-1", Destination: frontier, PlayerID: shared.MustNewPlayerID(1)}
	_, err1 := handler.Handle(context.Background(), cmd)
	_, err2 := handler.Handle(context.Background(), cmd)

	require.NoError(t, err1)
	require.NoError(t, err2)
	require.Len(t, router.calls, 2, "both navigations are delegated")
	require.Equal(t, 1, graph.buildCount["X1-KN83"], "the destination system is synced at most once per window")
}

// AC3 (last resort): a genuinely-unknown cross-system waypoint — absent even after a
// force-refresh — is NOT flown to. The handler force-syncs once, still cannot confirm the
// waypoint, and falls through to the existing fail-closed validation. Never navigate to a
// waypoint the graph provider cannot confirm exists.
func TestNavigateRoute_CrossSystem_UnknownWaypoint_FailsClosed(t *testing.T) {
	const (
		origin  = "X1-VB74-J1"
		unknown = "X1-KN83-ZZZZ" // X1-KN83 exists, but this waypoint does not
	)
	graph := newFakeGraphProvider()
	graph.graphs["X1-VB74"] = graphWith(t, "X1-VB74", origin)
	graph.graphs["X1-KN83"] = graphWith(t, "X1-KN83", "X1-KN83-AF7C") // real system, unknown lacks its waypoint

	router := &fakeCrossSystemRouter{}
	handler := handlerFor(&fakeNavShipRepo{ship: newCrossSystemTestShip(t, "SCOUT-1", origin)}, graph)
	handler.WithCrossSystemRouter(router)

	_, err := handler.Handle(context.Background(), &NavigateRouteCommand{
		ShipSymbol:  "SCOUT-1",
		Destination: unknown,
		PlayerID:    shared.MustNewPlayerID(1),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in cache", "an unconfirmable waypoint still fails closed (last resort)")
	require.Empty(t, router.calls, "a hull is never committed to a journey to an unknown waypoint")
	require.Equal(t, 1, graph.forceCount["X1-KN83"], "a force-refresh is attempted before giving up")
}

// Safety pin: a SAME-system destination is never routed through the cross-system machinery
// — even a failing in-system navigation stays on the intra-system path. Guards against
// over-delegation breaking ordinary navigation.
func TestNavigateRoute_SameSystem_NotDelegated(t *testing.T) {
	const (
		origin = "X1-VB74-J1"
		inDest = "X1-VB74-Q9" // same system, deliberately absent from the graph
	)
	graph := newFakeGraphProvider()
	graph.graphs["X1-VB74"] = graphWith(t, "X1-VB74", origin) // inDest missing on purpose

	router := &fakeCrossSystemRouter{}
	handler := handlerFor(&fakeNavShipRepo{ship: newCrossSystemTestShip(t, "SCOUT-1", origin)}, graph)
	handler.WithCrossSystemRouter(router)

	_, err := handler.Handle(context.Background(), &NavigateRouteCommand{
		ShipSymbol:  "SCOUT-1",
		Destination: inDest,
		PlayerID:    shared.MustNewPlayerID(1),
	})

	require.Error(t, err, "an in-system waypoint missing from the graph still fails on the intra-system path")
	require.Empty(t, router.calls, "same-system navigation is never delegated to the cross-system router")
}

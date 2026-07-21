package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// homeStubShipRepo embeds the domain interface so only the methods homing
// uses need concrete implementations; any unexpected call panics on a
// nil-method deref.
type homeStubShipRepo struct {
	navigation.ShipRepository
	ship  *navigation.Ship
	fleet []*navigation.Ship // served by FindAllByPlayer for peer-occupancy tests
}

func (s *homeStubShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return s.ship, nil
}

func (s *homeStubShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return s.fleet, nil
}

// homeMultiShipRepo serves a fleet of ships by symbol (FindBySymbol) and the whole
// set for peer-occupancy (FindAllByPlayer), so a test can home several co-located
// hulls through independent Handle calls and observe the collective distribution.
type homeMultiShipRepo struct {
	navigation.ShipRepository
	ships map[string]*navigation.Ship
	fleet []*navigation.Ship
}

func (s *homeMultiShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	ship, ok := s.ships[symbol]
	if !ok {
		return nil, fmt.Errorf("ship %s not found", symbol)
	}
	return ship, nil
}

func (s *homeMultiShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return s.fleet, nil
}

// homeStubGraphProvider serves a fixed, pre-built graph regardless of the
// system symbol requested - every test here uses a single system.
type homeStubGraphProvider struct {
	graph *system.NavigationGraph
}

func (s *homeStubGraphProvider) GetGraph(_ context.Context, _ string, _ bool, _ int) (*system.GraphLoadResult, error) {
	return &system.GraphLoadResult{Graph: s.graph, Source: "database"}, nil
}

// homeFakeMediator records every NavigateRouteCommand sent to it. Any other
// command type is a bug in HomeShipHandler - homing should only ever
// navigate - so it fails the test loudly instead of silently ignoring it.
type homeFakeMediator struct {
	common.Mediator
	navigateCalls []*shipNav.NavigateRouteCommand
}

func (m *homeFakeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipNav.NavigateRouteCommand:
		m.navigateCalls = append(m.navigateCalls, cmd)
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected mediator command in test: %T (homing should only ever send a NavigateRouteCommand)", request)
	}
}

// newHomeTestShip builds an idle, docked ship at the given waypoint for the
// homing handler tests.
func newHomeTestShip(t *testing.T, symbol, waypointSymbol string, x, y float64) *navigation.Ship {
	t.Helper()
	return newHomeTestShipWithStatus(t, symbol, waypointSymbol, x, y, navigation.NavStatusDocked)
}

// newHomeTestShipWithStatus builds an idle ship with an explicit nav status -
// for an in-transit fixture the waypoint is the ship's DESTINATION, matching
// how transit is modeled (CurrentLocation is the destination once underway).
func newHomeTestShipWithStatus(t *testing.T, symbol, waypointSymbol string, x, y float64, status navigation.NavStatus) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(80, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		location,
		fuel,
		0,
		80,
		cargo,
		9,
		"FRAME_HAULER",
		"HAULER",
		nil,
		status,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

func homeTestWaypoint(t *testing.T, symbol string, x, y float64) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, x, y)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	return wp
}

func homeTestGraph(waypoints ...*shared.Waypoint) *system.NavigationGraph {
	graph := system.NewNavigationGraph("X1-TEST")
	for _, wp := range waypoints {
		graph.AddWaypoint(wp)
	}
	return graph
}

// A dedicated ship sitting idle with two configured standby stations at
// different distances must home to the nearer one (sp-snmb).
func TestHomeShipHandler_NavigatesToNearestStandbyStation(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-A1", 0, 0)
	near := homeTestWaypoint(t, "X1-TEST-B2", 10, 0)
	far := homeTestWaypoint(t, "X1-TEST-C3", 100, 0)

	shipRepo := &homeStubShipRepo{ship: ship}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(near, far)}
	mediator := &homeFakeMediator{}

	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-C3", "X1-TEST-B2"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(mediator.navigateCalls) != 1 {
		t.Fatalf("expected exactly one navigate dispatch, got %d", len(mediator.navigateCalls))
	}
	if mediator.navigateCalls[0].Destination != "X1-TEST-B2" {
		t.Fatalf("expected navigation to the nearer station X1-TEST-B2, got %s", mediator.navigateCalls[0].Destination)
	}

	homeResp, ok := resp.(*HomeShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if !homeResp.Navigated {
		t.Fatalf("expected Navigated=true, got %+v", homeResp)
	}
	if homeResp.TargetStation != "X1-TEST-B2" {
		t.Fatalf("expected TargetStation X1-TEST-B2, got %s", homeResp.TargetStation)
	}
	if homeResp.Distance != 10 {
		t.Fatalf("expected Distance 10, got %f", homeResp.Distance)
	}
}

// No configured standby stations means homing is disabled entirely: the
// claim-filter still applies, the ship just never relocates when idle.
func TestHomeShipHandler_NoStandbyStationsConfigured_NoOp(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-A1", 0, 0)
	shipRepo := &homeStubShipRepo{ship: ship}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph()}
	mediator := &homeFakeMediator{}

	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: nil,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mediator.navigateCalls) != 0 {
		t.Fatalf("expected no navigate dispatch with no standby stations configured, got %d", len(mediator.navigateCalls))
	}
	homeResp, ok := resp.(*HomeShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if homeResp.Navigated {
		t.Fatalf("expected Navigated=false with no standby stations configured, got %+v", homeResp)
	}
}

// A ship already parked at one of the configured standby stations must not
// re-navigate to itself.
func TestHomeShipHandler_AlreadyAtStandbyStation_NoOp(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-B2", 10, 0)
	near := homeTestWaypoint(t, "X1-TEST-B2", 10, 0)
	far := homeTestWaypoint(t, "X1-TEST-C3", 100, 0)

	shipRepo := &homeStubShipRepo{ship: ship}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(near, far)}
	mediator := &homeFakeMediator{}

	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-C3", "X1-TEST-B2"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mediator.navigateCalls) != 0 {
		t.Fatalf("expected no navigate dispatch when already at a standby station, got %d", len(mediator.navigateCalls))
	}
	homeResp, ok := resp.(*HomeShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if homeResp.Navigated {
		t.Fatalf("expected Navigated=false when already at standby station, got %+v", homeResp)
	}
	if homeResp.TargetStation != "X1-TEST-B2" {
		t.Fatalf("expected TargetStation X1-TEST-B2 (where the ship already is), got %s", homeResp.TargetStation)
	}
	if homeResp.Distance != 0 {
		t.Fatalf("expected Distance 0 when already at the target station, got %f", homeResp.Distance)
	}
}

// Balanced-standby homing (l7h2 Phase 3): with a fleet peer already parked at
// the nearer station, the ship must home to the emptier, farther one -
// fewest-assigned-first with distance only breaking ties. Nearest-only homing
// clumped every idle hull on one hub.
func TestHomeShipHandler_BalancesAcrossStandbyStations_AvoidsOccupiedHub(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-A1", 0, 0)
	peer := newHomeTestShip(t, "TORWIND-5", "X1-TEST-B2", 10, 0) // parked at the near hub
	near := homeTestWaypoint(t, "X1-TEST-B2", 10, 0)
	far := homeTestWaypoint(t, "X1-TEST-C3", 100, 0)

	shipRepo := &homeStubShipRepo{ship: ship, fleet: []*navigation.Ship{ship, peer}}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(near, far)}
	mediator := &homeFakeMediator{}

	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-B2", "X1-TEST-C3"},
		FleetShips:      []string{"TORWIND-4", "TORWIND-5"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(mediator.navigateCalls) != 1 {
		t.Fatalf("expected exactly one navigate dispatch, got %d", len(mediator.navigateCalls))
	}
	if mediator.navigateCalls[0].Destination != "X1-TEST-C3" {
		t.Fatalf("expected navigation to the unoccupied station X1-TEST-C3, got %s", mediator.navigateCalls[0].Destination)
	}
	homeResp, ok := resp.(*HomeShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if homeResp.TargetStation != "X1-TEST-C3" {
		t.Fatalf("expected TargetStation X1-TEST-C3, got %s", homeResp.TargetStation)
	}
}

// A peer still flying toward a station occupies it for balancing purposes:
// its CurrentLocation is already the destination once transit starts, so two
// hulls homed back-to-back must pick different hubs.
func TestHomeShipHandler_InTransitPeerCountsAtItsDestination(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-A1", 0, 0)
	peer := newHomeTestShipWithStatus(t, "TORWIND-5", "X1-TEST-B2", 10, 0, navigation.NavStatusInTransit) // flying to the near hub
	near := homeTestWaypoint(t, "X1-TEST-B2", 10, 0)
	far := homeTestWaypoint(t, "X1-TEST-C3", 100, 0)

	shipRepo := &homeStubShipRepo{ship: ship, fleet: []*navigation.Ship{ship, peer}}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(near, far)}
	mediator := &homeFakeMediator{}

	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	_, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-B2", "X1-TEST-C3"},
		FleetShips:      []string{"TORWIND-4", "TORWIND-5"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(mediator.navigateCalls) != 1 {
		t.Fatalf("expected exactly one navigate dispatch, got %d", len(mediator.navigateCalls))
	}
	if mediator.navigateCalls[0].Destination != "X1-TEST-C3" {
		t.Fatalf("expected navigation away from the hub the in-transit peer is heading to, got %s", mediator.navigateCalls[0].Destination)
	}
}

// Invariant (l7h2 Phase 3): homing applies to idle standby hulls only - a
// hull claimed by a container is never relocated, regardless of what the
// dispatcher believed when it fired the command.
func TestHomeShipHandler_ClaimedHullNeverMoved(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-A1", 0, 0)
	if err := ship.AssignToContainer("worker-1", shared.NewRealClock()); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}
	near := homeTestWaypoint(t, "X1-TEST-B2", 10, 0)

	shipRepo := &homeStubShipRepo{ship: ship}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(near)}
	mediator := &homeFakeMediator{}

	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-B2"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mediator.navigateCalls) != 0 {
		t.Fatalf("expected no navigate dispatch for a claimed hull, got %d", len(mediator.navigateCalls))
	}
	homeResp, ok := resp.(*HomeShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if homeResp.Navigated {
		t.Fatalf("expected Navigated=false for a claimed hull, got %+v", homeResp)
	}
}

// Invariant (l7h2 Phase 3): a hull already mid-flight is never re-routed by
// homing.
func TestHomeShipHandler_InTransitHullNeverMoved(t *testing.T) {
	ship := newHomeTestShipWithStatus(t, "TORWIND-4", "X1-TEST-A1", 0, 0, navigation.NavStatusInTransit)
	near := homeTestWaypoint(t, "X1-TEST-B2", 10, 0)

	shipRepo := &homeStubShipRepo{ship: ship}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(near)}
	mediator := &homeFakeMediator{}

	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-B2"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mediator.navigateCalls) != 0 {
		t.Fatalf("expected no navigate dispatch for an in-transit hull, got %d", len(mediator.navigateCalls))
	}
	homeResp, ok := resp.(*HomeShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if homeResp.Navigated {
		t.Fatalf("expected Navigated=false for an in-transit hull, got %+v", homeResp)
	}
}

// THE LOAD-BEARING PROOF (sp-jydtb): several idle dedicated hulls sitting at the
// SAME sink, homed against the same snapshot with no peer parked at any hub yet,
// must SPREAD across the standby set instead of all piling on one point. The old
// occupancy+distance balancer collapses them (every hub reads occupancy 0, so the
// pure-distance tie sends every hull to the same nearest/first hub) - capping the
// contract op at ~1.28x. The demand-ranked spread must fan them out.
func TestHomeShipHandler_CoLocatedIdleHulls_SpreadNotPiled(t *testing.T) {
	// Three hulls all idle at the same sink Z, equidistant from three empty hubs.
	h1 := newHomeTestShip(t, "TORWIND-1", "X1-TEST-Z", 0, 0)
	h2 := newHomeTestShip(t, "TORWIND-2", "X1-TEST-Z", 0, 0)
	h3 := newHomeTestShip(t, "TORWIND-3", "X1-TEST-Z", 0, 0)
	a := homeTestWaypoint(t, "X1-TEST-A", 10, 0)
	b := homeTestWaypoint(t, "X1-TEST-B", 0, 10)
	c := homeTestWaypoint(t, "X1-TEST-C", -10, 0) // all three exactly distance 10 from Z

	repo := &homeMultiShipRepo{
		ships: map[string]*navigation.Ship{"TORWIND-1": h1, "TORWIND-2": h2, "TORWIND-3": h3},
		fleet: []*navigation.Ship{h1, h2, h3},
	}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(a, b, c)}
	fleetShips := []string{"TORWIND-1", "TORWIND-2", "TORWIND-3"}
	stations := []string{"X1-TEST-A", "X1-TEST-B", "X1-TEST-C"}

	targets := map[string]struct{}{}
	for _, sym := range fleetShips {
		mediator := &homeFakeMediator{}
		handler := NewHomeShipHandler(mediator, repo, graphProvider)
		resp, err := handler.Handle(context.Background(), &HomeShipCommand{
			ShipSymbol:      sym,
			PlayerID:        shared.MustNewPlayerID(1),
			StandbyStations: stations,
			FleetShips:      fleetShips,
		})
		if err != nil {
			t.Fatalf("Handle(%s): %v", sym, err)
		}
		homeResp := resp.(*HomeShipResponse)
		targets[homeResp.TargetStation] = struct{}{}
	}

	if len(targets) != 3 {
		got := make([]string, 0, len(targets))
		for tSym := range targets {
			got = append(got, tSym)
		}
		t.Fatalf("co-located idle hulls piled onto %d hub(s) %v - expected a 3-way spread across the standby set", len(targets), got)
	}
}

// Demand-ranked homing (sp-jydtb): a lone idle hull must home to the
// HIGHEST-DEMAND standby waypoint, not merely the nearest one. The old balancer is
// demand-blind and picks nearest; with a demand signal supplied the hull must
// prefer the high-demand sink even when it is farther.
func TestHomeShipHandler_DemandRankedHoming_PrefersHighestDemandOverNearest(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-ORIGIN", 0, 0)
	near := homeTestWaypoint(t, "X1-TEST-NEAR", 10, 0)   // nearest, low demand
	farHot := homeTestWaypoint(t, "X1-TEST-HOT", 100, 0) // farther, highest demand

	repo := &homeMultiShipRepo{
		ships: map[string]*navigation.Ship{"TORWIND-4": ship},
		fleet: []*navigation.Ship{ship},
	}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(near, farHot)}
	mediator := &homeFakeMediator{}
	handler := NewHomeShipHandler(mediator, repo, graphProvider)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-NEAR", "X1-TEST-HOT"},
		FleetShips:      []string{"TORWIND-4"},
		StandbyDemand:   map[string]float64{"X1-TEST-NEAR": 1, "X1-TEST-HOT": 100},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	homeResp := resp.(*HomeShipResponse)
	if homeResp.TargetStation != "X1-TEST-HOT" {
		t.Fatalf("expected homing to the highest-demand hub X1-TEST-HOT, got %s (demand-blind nearest homing)", homeResp.TargetStation)
	}
}

// None of the configured standby stations resolving in the current system's
// graph indicates an operator misconfiguration (typo'd waypoint symbol) -
// this must surface as an error, not a silent no-op.
func TestHomeShipHandler_NoConfiguredStationsFoundInGraph_ReturnsError(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-A1", 0, 0)
	shipRepo := &homeStubShipRepo{ship: ship}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph()} // empty graph
	mediator := &homeFakeMediator{}

	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	_, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-Z9"},
	})
	if err == nil {
		t.Fatalf("expected an error when none of the configured standby stations resolve in the graph")
	}
	if len(mediator.navigateCalls) != 0 {
		t.Fatalf("expected no navigate dispatch on graph-resolution failure, got %d", len(mediator.navigateCalls))
	}
}

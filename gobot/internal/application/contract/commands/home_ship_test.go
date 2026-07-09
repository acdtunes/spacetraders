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

// homeStubShipRepo embeds the domain interface so only FindBySymbol needs a
// concrete implementation; any unexpected call panics on a nil-method deref.
type homeStubShipRepo struct {
	navigation.ShipRepository
	ship *navigation.Ship
}

func (s *homeStubShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return s.ship, nil
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
		navigation.NavStatusDocked,
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

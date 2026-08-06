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

// A lone idle dedicated hull homes to its fixed slot. With FEWER hulls than slots the caller's
// slot ORDER is the PLACEMENT PRIORITY (sp-9suun: the era-invariant anchors lead, the tail is
// dropped), so the single hull takes slot[0] — here deliberately BOTH the alphabetically-LAST
// and the FARTHER waypoint, so this fails if either a symbol ordering or a nearest-first
// heuristic ever creeps back into the assignment.
//
// (Before sp-9suun the set was unordered and this hull took the alphabetically-first slot; that
// silently stranded the two highest-value anchors whenever the fleet was short of the set.)
func TestHomeShipHandler_NavigatesToItsFixedSlot(t *testing.T) {
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
	if mediator.navigateCalls[0].Destination != "X1-TEST-C3" {
		t.Fatalf("expected navigation to the top-PRIORITY slot X1-TEST-C3, got %s", mediator.navigateCalls[0].Destination)
	}

	homeResp, ok := resp.(*HomeShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if !homeResp.Navigated {
		t.Fatalf("expected Navigated=true, got %+v", homeResp)
	}
	if homeResp.TargetStation != "X1-TEST-C3" {
		t.Fatalf("expected TargetStation X1-TEST-C3, got %s", homeResp.TargetStation)
	}
	if homeResp.Distance != 100 {
		t.Fatalf("expected Distance 100, got %f", homeResp.Distance)
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

// A ship already parked at ITS assigned slot (C3 = the top-PRIORITY slot for the lone-hull
// roster, sp-9suun) must not re-navigate to itself — the "already home only if at MY slot" rule,
// which is what makes a second homing pass move no hull.
func TestHomeShipHandler_AlreadyAtItsSlot_NoOp(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-C3", 100, 0)
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
	if homeResp.TargetStation != "X1-TEST-C3" {
		t.Fatalf("expected TargetStation X1-TEST-C3 (where the ship already is), got %s", homeResp.TargetStation)
	}
	if homeResp.Distance != 0 {
		t.Fatalf("expected Distance 0 when already at the target station, got %f", homeResp.Distance)
	}
}

// The complement of the rule above: "already home" means AT MY SLOT, not at ANY standby
// station. With a 2-hull roster both slots survive the priority truncation; TORWIND-4 owns B2
// (symbol-sorted roster [TORWIND-4,TORWIND-9] zipped onto the PRIORITY-ordered slots [B2,C3])
// and is parked on its PEER's slot C3, so it must still relocate — otherwise a hull could squat a
// peer's slot forever and the design's one-hull-per-park spread quietly collapses.
func TestHomeShipHandler_AtAPeersSlot_StillRelocatesToItsOwn(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-C3", 100, 0)
	near := homeTestWaypoint(t, "X1-TEST-B2", 10, 0)
	far := homeTestWaypoint(t, "X1-TEST-C3", 100, 0)

	shipRepo := &homeStubShipRepo{ship: ship}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(near, far)}
	mediator := &homeFakeMediator{}

	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		FleetShips:      []string{"TORWIND-4", "TORWIND-9"},
		StandbyStations: []string{"X1-TEST-B2", "X1-TEST-C3"}, // PLACEMENT PRIORITY: B2 first
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mediator.navigateCalls) != 1 || mediator.navigateCalls[0].Destination != "X1-TEST-B2" {
		t.Fatalf("expected relocation to its OWN slot X1-TEST-B2, got %+v", mediator.navigateCalls)
	}
	homeResp, ok := resp.(*HomeShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if !homeResp.Navigated || homeResp.TargetStation != "X1-TEST-B2" {
		t.Fatalf("expected Navigated=true to X1-TEST-B2, got %+v", homeResp)
	}
}

// FIXED PLACEMENT: each hull homes to ITS OWN slot — the symbol-sorted roster zipped onto the
// PRIORITY-ordered slots — ignoring occupancy. TORWIND-4 (roster index 0) owns the first priority
// slot B2 and homes there even though a peer sits at B2 (the old occupancy balancer would have
// sent it to the "emptier" C3). The slot list is the caller's placement priority and is honoured
// as given, never re-sorted, so B2 must be listed first for TORWIND-4 to own it.
func TestHomeShipHandler_HomesToItsOwnFixedSlot(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-A1", 0, 0)
	peer := newHomeTestShip(t, "TORWIND-5", "X1-TEST-B2", 10, 0) // sitting at TORWIND-4's slot
	b := homeTestWaypoint(t, "X1-TEST-B2", 10, 0)
	c := homeTestWaypoint(t, "X1-TEST-C3", 100, 0)

	shipRepo := &homeStubShipRepo{ship: ship, fleet: []*navigation.Ship{ship, peer}}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(b, c)}
	mediator := &homeFakeMediator{}

	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-B2", "X1-TEST-C3"}, // PLACEMENT PRIORITY: B2 first
		FleetShips:      []string{"TORWIND-4", "TORWIND-5"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(mediator.navigateCalls) != 1 || mediator.navigateCalls[0].Destination != "X1-TEST-B2" {
		t.Fatalf("TORWIND-4 (roster index 0) must home to its OWN slot B2, got %v", mediator.navigateCalls)
	}
	if resp.(*HomeShipResponse).TargetStation != "X1-TEST-B2" {
		t.Fatalf("TargetStation = %s, want its fixed slot B2", resp.(*HomeShipResponse).TargetStation)
	}
}

// The "already home ONLY if at MY slot" rule: a hull sitting at a PEER's slot is NOT left
// there (the old "at ANY standby station" rule) — it moves to its OWN slot. TORWIND-5 (roster index 1)
// owns C3 but sits at B2 (TORWIND-4's slot); it must navigate to C3.
func TestHomeShipHandler_MovesOffAPeersSlotToItsOwn(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-5", "X1-TEST-B2", 10, 0) // sitting at TORWIND-4's slot, not its own
	peer := newHomeTestShip(t, "TORWIND-4", "X1-TEST-B2", 10, 0)
	b := homeTestWaypoint(t, "X1-TEST-B2", 10, 0)
	c := homeTestWaypoint(t, "X1-TEST-C3", 100, 0)

	shipRepo := &homeStubShipRepo{ship: ship, fleet: []*navigation.Ship{ship, peer}}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(b, c)}
	mediator := &homeFakeMediator{}

	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	_, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-5",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-B2", "X1-TEST-C3"},
		FleetShips:      []string{"TORWIND-4", "TORWIND-5"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(mediator.navigateCalls) != 1 || mediator.navigateCalls[0].Destination != "X1-TEST-C3" {
		t.Fatalf("TORWIND-5 at a PEER's slot B2 must move to its OWN slot C3 (not left at ANY station), got %v", mediator.navigateCalls)
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

// THE LOAD-BEARING PROOF: several idle dedicated hulls sitting at the SAME sink, each homed
// by an INDEPENDENT Handle call (as the live concurrent between-legs homing fires), must land on N
// DISTINCT slots — one per waypoint, never piled. The old runtime distributor piled them on the
// top-demand hub when peers were in-transit/invisible (the live K83 pile); the fixed symbol-zip gives
// each hull its OWN slot from the roster + slot set, independent of demand/occupancy/timing.
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

// A second homing pass moves NO hull (no thrash): a hull already AT its assigned slot stays put — the
// deterministic symbol-zip re-computes the same slot every pass.
func TestHomeShipHandler_SecondPassAtItsSlotIsNoOp(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-B2", 10, 0) // already at its slot (roster index 0 → B2)
	b := homeTestWaypoint(t, "X1-TEST-B2", 10, 0)
	c := homeTestWaypoint(t, "X1-TEST-C3", 100, 0)

	shipRepo := &homeStubShipRepo{ship: ship, fleet: []*navigation.Ship{ship}}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(b, c)}
	mediator := &homeFakeMediator{}
	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-4",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-B2", "X1-TEST-C3"},
		FleetShips:      []string{"TORWIND-4"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mediator.navigateCalls) != 0 || resp.(*HomeShipResponse).Navigated {
		t.Fatalf("a hull already at its fixed slot must not move (no thrash), got navigate=%v", mediator.navigateCalls)
	}
}

// A surplus hull beyond the slot count owns NO slot and is left where it is — for the scaler to re-role
// into a warehouse, never piled onto an occupied slot. TORWIND-9 is roster index 2 with only 2 slots.
func TestHomeShipHandler_SurplusHullOwnsNoSlotAndIsNotMoved(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-9", "X1-TEST-A1", 0, 0)
	b := homeTestWaypoint(t, "X1-TEST-B2", 10, 0)
	c := homeTestWaypoint(t, "X1-TEST-C3", 100, 0)

	shipRepo := &homeStubShipRepo{ship: ship, fleet: []*navigation.Ship{ship}}
	graphProvider := &homeStubGraphProvider{graph: homeTestGraph(b, c)}
	mediator := &homeFakeMediator{}
	handler := NewHomeShipHandler(mediator, shipRepo, graphProvider)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-9",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: []string{"X1-TEST-B2", "X1-TEST-C3"},
		FleetShips:      []string{"TORWIND-4", "TORWIND-5", "TORWIND-9"}, // 3 hulls, 2 slots → TORWIND-9 surplus
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(mediator.navigateCalls) != 0 || resp.(*HomeShipResponse).Navigated {
		t.Fatalf("a surplus hull (index 2, only 2 slots) owns no slot and must not move, got navigate=%v", mediator.navigateCalls)
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

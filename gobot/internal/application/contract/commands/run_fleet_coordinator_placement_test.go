package commands

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests cover the FIXED-placement homing: the contract coordinator's between-legs
// homing and the idle-arb re-home must carry the ≤6 fixed placement slots and — when the fleet-hub set
// is empty — the placement provider's slots must AUTO-drive homing (no manual `fleet hub` pins). The
// runtime homing zips each hull to its OWN distinct slot (no demand, no occupancy). Assertions are on
// the observable outcome (the dispatched HomeShipCommand / the distinct target set), never internal calls.

// stubPlacementProvider serves the fixed ≤6 placement slots, standing in for the role-resolver-backed
// grpc provider the daemon wires. Shared across the commands-package homing tests.
type stubPlacementProvider struct {
	slots []string
}

func (s *stubPlacementProvider) StandbyPlacement(_ context.Context, _ int) ([]string, error) {
	return s.slots, nil
}

var _ appContract.StandbyPlacementProvider = (*stubPlacementProvider)(nil)

// recordingHomeMediator captures the HomeShipCommand a homer dispatches (fire-and-forget), signaling on
// a channel so a test can await the async send.
type recordingHomeMediator struct {
	common.Mediator
	got chan *HomeShipCommand
}

func (m *recordingHomeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	if cmd, ok := request.(*HomeShipCommand); ok {
		m.got <- cmd
		return &HomeShipResponse{}, nil
	}
	return nil, fmt.Errorf("unexpected mediator command in test: %T", request)
}

// The idle-arb between-legs re-home (mediatorShipHomer.HomeShip): the dispatched HomeShipCommand must
// carry the ≤6 fixed placement slots, and with the passed-in live set EMPTY the placement provider must
// auto-drive the re-home (the auto hub-placement).
func TestMediatorShipHomer_HomeShip_CarriesFixedSlotsAndAutoResolvesEmptySet(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-5", "X1-UM5-Z", 0, 0)
	shipRepo := &homeStubShipRepo{ship: ship, fleet: []*navigation.Ship{ship}}
	got := make(chan *HomeShipCommand, 1)
	med := &recordingHomeMediator{got: got}
	provider := &stubPlacementProvider{slots: []string{"X1-UM5-G49", "X1-UM5-K83"}}

	homer := &mediatorShipHomer{
		mediator:          med,
		shipRepo:          shipRepo,
		playerID:          shared.MustNewPlayerID(1),
		fleet:             dedicatedFleetContract,
		placementProvider: provider,
	}

	// Empty live standby set — the fixed placement slots must auto-drive the re-home.
	if err := homer.HomeShip(context.Background(), "TORWIND-5", nil); err != nil {
		t.Fatalf("HomeShip: %v", err)
	}

	select {
	case cmd := <-got:
		wantStations := []string{"X1-UM5-G49", "X1-UM5-K83"}
		if !reflect.DeepEqual(cmd.StandbyStations, wantStations) {
			t.Fatalf("an empty live set must auto-resolve to the fixed placement slots %v, got %v", wantStations, cmd.StandbyStations)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HomeShip never dispatched a HomeShipCommand")
	}
}

// The coordinator's between-legs homing end-to-end: with the fleet hub EMPTY and a placement provider
// wired, several co-located idle hulls must land on DISTINCT fixed slots — the pile-up fix. Each hull
// resolves its OWN slot from the symbol-zip of the roster onto the slots.
func TestCoordinatorHoming_EmptyFleetHub_FixedPlacementSpreadsCoLocatedHulls(t *testing.T) {
	h1 := newHomeTestShip(t, "TORWIND-1", "X1-UM5-Z", 0, 0)
	h2 := newHomeTestShip(t, "TORWIND-2", "X1-UM5-Z", 0, 0)
	h3 := newHomeTestShip(t, "TORWIND-3", "X1-UM5-Z", 0, 0)
	g := homeTestWaypoint(t, "X1-UM5-G49", 54, -33)
	k := homeTestWaypoint(t, "X1-UM5-K83", 8, 104)
	e := homeTestWaypoint(t, "X1-UM5-E43", 50, 24)

	repo := &homeMultiShipRepo{
		ships: map[string]*navigation.Ship{"TORWIND-1": h1, "TORWIND-2": h2, "TORWIND-3": h3},
		fleet: []*navigation.Ship{h1, h2, h3},
	}
	graph := &homeStubGraphProvider{graph: homeTestGraph(g, k, e)}
	provider := &stubPlacementProvider{slots: []string{"X1-UM5-G49", "X1-UM5-K83", "X1-UM5-E43"}}
	fleetShips := []string{"TORWIND-1", "TORWIND-2", "TORWIND-3"}

	targets := map[string]struct{}{}
	for _, sym := range fleetShips {
		// Empty fleet-hub set → auto-driven by the fixed placement slots.
		stations := appContract.ResolveStandbyForHoming(context.Background(), nil, provider, 1, nil)
		mediator := &homeFakeMediator{}
		handler := NewHomeShipHandler(mediator, repo, graph)
		resp, err := handler.Handle(context.Background(), &HomeShipCommand{
			ShipSymbol:      sym,
			PlayerID:        shared.MustNewPlayerID(1),
			StandbyStations: stations,
			FleetShips:      fleetShips,
		})
		if err != nil {
			t.Fatalf("Handle(%s): %v", sym, err)
		}
		targets[resp.(*HomeShipResponse).TargetStation] = struct{}{}
	}

	if len(targets) != 3 {
		got := make([]string, 0, len(targets))
		for sym := range targets {
			got = append(got, sym)
		}
		t.Fatalf("co-located idle hulls piled onto %d slot(s) %v — expected a 3-way spread across the fixed placement slots", len(targets), got)
	}
}

// Control: no fleet hub AND no placement resolved → the resolved set is empty and homing stays disabled
// (home_ship returns Navigated:false on an empty set), so the fix is inert until placement exists.
func TestCoordinatorHoming_NoHubNoPlacement_HomingDisabled(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-1", "X1-UM5-Z", 0, 0)
	repo := &homeStubShipRepo{ship: ship, fleet: []*navigation.Ship{ship}}
	graph := &homeStubGraphProvider{graph: homeTestGraph(homeTestWaypoint(t, "X1-UM5-G49", 54, -33))}
	provider := &stubPlacementProvider{slots: nil}

	stations := appContract.ResolveStandbyForHoming(context.Background(), nil, provider, 1, nil)
	mediator := &homeFakeMediator{}
	handler := NewHomeShipHandler(mediator, repo, graph)
	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-1",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: stations,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.(*HomeShipResponse).Navigated {
		t.Fatalf("no hub + no placement must leave homing disabled, got Navigated=true")
	}
	if len(mediator.navigateCalls) != 0 {
		t.Fatalf("disabled homing must produce no navigation, got %+v", mediator.navigateCalls)
	}
}

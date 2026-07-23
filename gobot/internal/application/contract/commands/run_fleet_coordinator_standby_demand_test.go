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

// These tests cover C2c (sp-5rakx, epic sp-9le3x): the contract coordinator's
// BETWEEN-LEGS homing must carry the per-park DEMAND weights so idle hulls spread
// demand-ranked across the central sinks instead of piling at one point (the live
// J59 1.06x bug), AND — when the fleet-hub set is empty — the role-resolved central
// parks must AUTO-drive homing (no manual `fleet hub` pins). Assertions are on the
// observable outcome (the demand-ranked HomeShipCommand / the distinct target set),
// never on internal calls.

// stubDemandProvider serves a fixed per-park demand map, standing in for the
// role-resolver-backed grpc provider the daemon wires.
type stubDemandProvider struct {
	demand map[string]float64
}

func (s *stubDemandProvider) StandbyDemand(_ context.Context, _ int) (map[string]float64, error) {
	return s.demand, nil
}

var _ appContract.StandbyDemandProvider = (*stubDemandProvider)(nil)

// recordingHomeMediator captures the HomeShipCommand a homer dispatches (fire-and-
// forget), signaling on a channel so a test can await the async send.
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

// Hook 2 (mediatorShipHomer.HomeShip — the idle-arb between-legs re-home): the dispatched
// HomeShipCommand must carry the per-park demand weights, and with the passed-in live set
// EMPTY the role-resolved central parks must auto-drive the re-home (the sp-bu6ma auto
// hub-placement). Pre-fix this built the command with no demand and did nothing on an
// empty set.
func TestMediatorShipHomer_HomeShip_CarriesDemandAndAutoResolvesEmptySet(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-5", "X1-UM5-Z", 0, 0)
	shipRepo := &homeStubShipRepo{ship: ship, fleet: []*navigation.Ship{ship}}
	got := make(chan *HomeShipCommand, 1)
	med := &recordingHomeMediator{got: got}
	provider := &stubDemandProvider{demand: map[string]float64{"X1-UM5-G49": 340, "X1-UM5-K83": 260}}

	homer := &mediatorShipHomer{
		mediator:       med,
		shipRepo:       shipRepo,
		playerID:       shared.MustNewPlayerID(1),
		fleet:          dedicatedFleetContract,
		demandProvider: provider,
	}

	// Empty live standby set (the live bug) — role demand must auto-drive the re-home.
	if err := homer.HomeShip(context.Background(), "TORWIND-5", nil); err != nil {
		t.Fatalf("HomeShip: %v", err)
	}

	select {
	case cmd := <-got:
		if !reflect.DeepEqual(cmd.StandbyDemand, provider.demand) {
			t.Fatalf("re-home HomeShipCommand must carry the per-park demand weights, got %v", cmd.StandbyDemand)
		}
		wantStations := []string{"X1-UM5-G49", "X1-UM5-K83"} // sorted role parks
		if !reflect.DeepEqual(cmd.StandbyStations, wantStations) {
			t.Fatalf("an empty live set must auto-resolve to the role central parks %v, got %v", wantStations, cmd.StandbyStations)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HomeShip never dispatched a HomeShipCommand")
	}
}

// Hook 1 (the coordinator's between-legs homing composition) end-to-end: with the fleet
// hub EMPTY and a demand provider wired, several co-located idle hulls must SPREAD across
// the auto-resolved distinct central parks — the J59 pile-up fix. Mirrors the coordinator
// loop: resolve (stations, demand) for the empty live set, then run the balanced homing.
func TestCoordinatorHoming_EmptyFleetHub_RoleDemandSpreadsCoLocatedHulls(t *testing.T) {
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
	provider := &stubDemandProvider{demand: map[string]float64{"X1-UM5-G49": 340, "X1-UM5-K83": 260, "X1-UM5-E43": 240}}
	fleetShips := []string{"TORWIND-1", "TORWIND-2", "TORWIND-3"}

	targets := map[string]struct{}{}
	for _, sym := range fleetShips {
		// Empty fleet-hub set → auto-driven by the role parks + demand.
		stations, demand := appContract.ResolveStandbyForHoming(context.Background(), nil, provider, 1, nil)
		mediator := &homeFakeMediator{}
		handler := NewHomeShipHandler(mediator, repo, graph)
		resp, err := handler.Handle(context.Background(), &HomeShipCommand{
			ShipSymbol:      sym,
			PlayerID:        shared.MustNewPlayerID(1),
			StandbyStations: stations,
			StandbyDemand:   demand,
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
		t.Fatalf("co-located idle hulls piled onto %d park(s) %v — expected a 3-way spread across the auto-resolved role parks", len(targets), got)
	}
}

// Control: no fleet hub AND no demand signal → the resolved set is empty and homing stays
// disabled (byte-identical to today — home_ship returns Navigated:false on an empty set),
// so the fix is inert until a demand signal exists.
func TestCoordinatorHoming_NoHubNoDemand_HomingDisabled(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-1", "X1-UM5-Z", 0, 0)
	repo := &homeStubShipRepo{ship: ship, fleet: []*navigation.Ship{ship}}
	graph := &homeStubGraphProvider{graph: homeTestGraph(homeTestWaypoint(t, "X1-UM5-G49", 54, -33))}
	provider := &stubDemandProvider{demand: map[string]float64{}}

	stations, demand := appContract.ResolveStandbyForHoming(context.Background(), nil, provider, 1, nil)
	mediator := &homeFakeMediator{}
	handler := NewHomeShipHandler(mediator, repo, graph)
	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      "TORWIND-1",
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: stations,
		StandbyDemand:   demand,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.(*HomeShipResponse).Navigated {
		t.Fatalf("no hub + no demand must leave homing disabled, got Navigated=true")
	}
	if len(mediator.navigateCalls) != 0 {
		t.Fatalf("disabled homing must produce no navigation, got %+v", mediator.navigateCalls)
	}
}

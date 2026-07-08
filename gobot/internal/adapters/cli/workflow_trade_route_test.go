package cli

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNav "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// recordingMediator captures the atomic commands the nav handler dispatches and the
// ORDER they arrive in, so a test can assert the trade-route moves its hull with
// NavigateDirect (which claims no container, never a re-claiming route) and that it
// ORBITS a docked hull before navigating.
//
// It also models the SpaceTraders contract the live sp-sj7p run hit: a NavigateDirect
// fired on a hull still DOCKED from the preceding buy is rejected by the API with 4236
// (not in orbit). This fake reproduces that — NavigateDirect while docked returns the
// 4236 error — and flips to in-orbit only once an OrbitShipCommand is dispatched.
// OrbitShip is idempotent here (mirroring the real tactics.OrbitShipHandler, which
// returns already_in_orbit without an API call or error — see
// tactics/state_transition_test.go "orbit while already in orbit"): a redundant orbit
// on an already-orbiting hull is a no-op, never an error.
type recordingMediator struct {
	inOrbit    bool // false = DOCKED (the post-buy starting state seen live)
	calls      []string
	orbitCmds  []*shipTypes.OrbitShipCommand
	directCmds []*shipTypes.NavigateDirectCommand
}

func (m *recordingMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipTypes.OrbitShipCommand:
		m.calls = append(m.calls, "orbit")
		m.orbitCmds = append(m.orbitCmds, cmd)
		m.inOrbit = true
		return &shipTypes.OrbitShipResponse{Status: "in_orbit"}, nil
	case *shipTypes.NavigateDirectCommand:
		m.calls = append(m.calls, "navigate")
		m.directCmds = append(m.directCmds, cmd)
		if !m.inOrbit {
			// The live sp-sj7p failure: navigate dispatched on a DOCKED hull.
			return nil, fmt.Errorf("API 4236 Ship is not currently in orbit at %s", cmd.Destination)
		}
		return &shipTypes.NavigateDirectResponse{Status: "navigating"}, nil
	default:
		return nil, fmt.Errorf("unexpected command %T (trade-route nav must orbit then NavigateDirect, not a re-claiming route)", request)
	}
}

func (m *recordingMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *recordingMediator) RegisterMiddleware(middleware common.Middleware) {}

// arrivedShipRepo reports the hull as already arrived (not IN_TRANSIT), so the
// handler's arrival poll returns on the first check.
type arrivedShipRepo struct {
	domainNav.ShipRepository
	ship *domainNav.Ship
}

func (r *arrivedShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*domainNav.Ship, error) {
	return r.ship, nil
}

func newDockedShip(t *testing.T, symbol, at string) *domainNav.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(at, 0, 0)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	ship, err := domainNav.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, domainNav.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

// The trade-route's navigation handler must move the ALREADY-CLAIMED hull with a
// direct navigate (no container assignment), never through the daemon's re-claiming
// NavigateShip RPC that caused the sp-2sam self-collision. Structurally the handler
// no longer holds a daemon client; this pins the behaviour: a NavigateRoute leg is
// dispatched as exactly one NavigateDirect command and the handler waits for arrival.
func TestInProcessNavHandler_MovesClaimedHullViaDirectNavigate(t *testing.T) {
	ship := newDockedShip(t, "TORWIND-8", "X1-ZV-IMPORT")
	med := &recordingMediator{}
	h := &inProcessNavHandler{
		mediator:     med,
		shipRepo:     &arrivedShipRepo{ship: ship},
		playerID:     1,
		pollInterval: time.Millisecond,
		timeout:      time.Second,
	}

	resp, err := h.Handle(context.Background(), &navCmd.NavigateRouteCommand{
		ShipSymbol:  "TORWIND-8",
		Destination: "X1-ZV-IMPORT",
		PlayerID:    shared.MustNewPlayerID(1),
	})
	if err != nil {
		t.Fatalf("in-process navigate returned error: %v", err)
	}
	navResp, ok := resp.(*navCmd.NavigateRouteResponse)
	if !ok || navResp.Status != "completed" {
		t.Fatalf("expected a completed navigation, got %+v", resp)
	}
	if len(med.directCmds) != 1 {
		t.Fatalf("expected exactly one NavigateDirect dispatch (no re-claiming route), got %d", len(med.directCmds))
	}
	if med.directCmds[0].Destination != "X1-ZV-IMPORT" || med.directCmds[0].ShipSymbol != "TORWIND-8" {
		t.Fatalf("NavigateDirect targeted the wrong hull/destination: %+v", med.directCmds[0])
	}
}

// The preceding buy leaves the hull DOCKED (cargo transactions require docked). The
// live sp-sj7p run then fired NavigateDirect on that docked hull and the API rejected
// it with 4236 ("Ship is not currently in orbit at X1-KA42-J56"). The handler must
// ORBIT the hull first, then navigate. RED before the fix: the docked mediator returns
// 4236 and Handle surfaces it. GREEN after: orbit precedes navigate and the leg
// completes.
func TestInProcessNavHandler_OrbitsBeforeNavigatingFromDockedHull(t *testing.T) {
	ship := newDockedShip(t, "TORWIND-3", "X1-KA42-J56")
	med := &recordingMediator{inOrbit: false} // post-buy DOCKED — the live starting state
	h := &inProcessNavHandler{
		mediator:     med,
		shipRepo:     &arrivedShipRepo{ship: ship},
		playerID:     1,
		pollInterval: time.Millisecond,
		timeout:      time.Second,
	}

	resp, err := h.Handle(context.Background(), &navCmd.NavigateRouteCommand{
		ShipSymbol:  "TORWIND-3",
		Destination: "X1-KA42-H49",
		PlayerID:    shared.MustNewPlayerID(1),
	})
	if err != nil {
		t.Fatalf("nav from a DOCKED hull must orbit before navigating, got error: %v", err)
	}
	navResp, ok := resp.(*navCmd.NavigateRouteResponse)
	if !ok || navResp.Status != "completed" {
		t.Fatalf("expected a completed navigation, got %+v", resp)
	}
	if len(med.orbitCmds) == 0 {
		t.Fatalf("handler never orbited the docked hull before navigating")
	}
	if len(med.calls) < 2 || med.calls[0] != "orbit" || med.calls[1] != "navigate" {
		t.Fatalf("expected orbit before navigate, got call order %v", med.calls)
	}
	if med.orbitCmds[0].ShipSymbol != "TORWIND-3" {
		t.Fatalf("orbited the wrong hull: %+v", med.orbitCmds[0])
	}
}

// A later leg starts with the hull ALREADY in orbit (it arrived from a prior leg and
// did not dock). Orbiting again must be tolerated — the real OrbitShipHandler returns
// already_in_orbit without an API call or error — and the leg must still navigate
// exactly once. This guards the fix against a double-orbit regression on later legs.
func TestInProcessNavHandler_AlreadyInOrbitHullNavigatesWithoutDoubleOrbitError(t *testing.T) {
	ship := newDockedShip(t, "TORWIND-3", "X1-KA42-H49")
	med := &recordingMediator{inOrbit: true} // already orbiting from a prior arrival
	h := &inProcessNavHandler{
		mediator:     med,
		shipRepo:     &arrivedShipRepo{ship: ship},
		playerID:     1,
		pollInterval: time.Millisecond,
		timeout:      time.Second,
	}

	resp, err := h.Handle(context.Background(), &navCmd.NavigateRouteCommand{
		ShipSymbol:  "TORWIND-3",
		Destination: "X1-KA42-J56",
		PlayerID:    shared.MustNewPlayerID(1),
	})
	if err != nil {
		t.Fatalf("already-orbiting hull must still navigate without a double-orbit error, got: %v", err)
	}
	if _, ok := resp.(*navCmd.NavigateRouteResponse); !ok {
		t.Fatalf("expected NavigateRouteResponse, got %T", resp)
	}
	if len(med.directCmds) != 1 {
		t.Fatalf("expected exactly one NavigateDirect dispatch, got %d", len(med.directCmds))
	}
}

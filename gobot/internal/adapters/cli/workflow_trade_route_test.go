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

// recordingMediator captures the atomic commands the nav handler dispatches, so a
// test can assert the trade-route moves its hull with NavigateDirect (which claims no
// container) rather than any command that would re-claim the parent-owned hull.
type recordingMediator struct {
	directCmds []*shipTypes.NavigateDirectCommand
}

func (m *recordingMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipTypes.NavigateDirectCommand:
		m.directCmds = append(m.directCmds, cmd)
		return &shipTypes.NavigateDirectResponse{Status: "navigating"}, nil
	default:
		return nil, fmt.Errorf("unexpected command %T (trade-route nav must use NavigateDirect, not a re-claiming route)", request)
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

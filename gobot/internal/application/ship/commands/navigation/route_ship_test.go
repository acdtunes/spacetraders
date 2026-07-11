package navigation

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeCrossSystemRouter records the cross-system move delegated to the shared
// multi-jump travel machinery and can inject a failure. The route verb must
// forward exactly one point-to-point move and surface its error verbatim —
// it writes no routing/jump logic of its own (sp-6hjw REUSE ruling).
type fakeCrossSystemRouter struct {
	calls   []string // "ship->destination"
	players []int    // the playerID forwarded per call
	err     error
}

func (f *fakeCrossSystemRouter) RepositionToWaypoint(_ context.Context, shipSymbol, destinationWaypoint string, playerID int) error {
	f.calls = append(f.calls, shipSymbol+"->"+destinationWaypoint)
	f.players = append(f.players, playerID)
	return f.err
}

// The route verb is a thin wrapper: it delegates the whole cross-system flight to
// the trade-route coordinator's exported RepositionToWaypoint (the SAME multi-jump
// travel() the arb/trade/scout circuits use) and reports the completed move. This
// is the primitive a manual cross-gate hull move (warehouse dispatch, spare
// repositioning) needs — no hand-rolled navigate-to-gate + jump + navigate (sp-6hjw).
func TestRouteShip_DelegatesCrossSystemTravelToRouter(t *testing.T) {
	router := &fakeCrossSystemRouter{}
	handler := NewRouteShipHandler(router)
	cmd := &RouteShipCommand{
		ShipSymbol:  "ENDURANCE-7",
		Destination: "X1-FAR-A1",
		PlayerID:    shared.MustNewPlayerID(1),
	}

	resp, err := handler.Handle(context.Background(), cmd)

	require.NoError(t, err)
	require.Equal(t, []string{"ENDURANCE-7->X1-FAR-A1"}, router.calls, "the verb delegates exactly one cross-system move to shared travel()")
	r, ok := resp.(*RouteShipResponse)
	require.True(t, ok, "returns a RouteShipResponse")
	require.Equal(t, "ENDURANCE-7", r.ShipSymbol)
	require.Equal(t, "X1-FAR-A1", r.Destination)
}

// A travel failure is surfaced as an error so the container FAILS honestly (the
// runner releases the claim) rather than reporting a false success on a hull left
// stranded mid-route. The underlying cause (e.g. an unroutable destination naming
// both systems) is preserved verbatim for the operator.
func TestRouteShip_RouterError_FailsHonestly(t *testing.T) {
	router := &fakeCrossSystemRouter{err: fmt.Errorf("no gate path from X1-KA42 to X1-JP61")}
	handler := NewRouteShipHandler(router)
	cmd := &RouteShipCommand{
		ShipSymbol:  "ENDURANCE-7",
		Destination: "X1-JP61-B1",
		PlayerID:    shared.MustNewPlayerID(1),
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.Error(t, err, "a travel failure surfaces — never a false success on a stranded hull")
	require.Contains(t, err.Error(), "no gate path from X1-KA42 to X1-JP61", "the underlying cause is preserved verbatim")
}

// The verb forwards its command's PlayerID to the movement port so the RIGHT
// player's hull is flown — a manual op is always scoped to one agent's fleet.
func TestRouteShip_ForwardsPlayerID(t *testing.T) {
	router := &fakeCrossSystemRouter{}
	handler := NewRouteShipHandler(router)
	cmd := &RouteShipCommand{
		ShipSymbol:  "SPARE-2",
		Destination: "X1-FAR-A1",
		PlayerID:    shared.MustNewPlayerID(4),
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.NoError(t, err)
	require.Equal(t, []int{4}, router.players, "the verb forwards its player scope to travel()")
}

// A nil-typed request is rejected rather than panicking — the mediator contract
// is total, and a mis-dispatched command must fail as an error.
func TestRouteShip_RejectsWrongRequestType(t *testing.T) {
	handler := NewRouteShipHandler(&fakeCrossSystemRouter{})

	_, err := handler.Handle(context.Background(), "not-a-route-command")

	require.Error(t, err, "a non-RouteShipCommand request is rejected")
}

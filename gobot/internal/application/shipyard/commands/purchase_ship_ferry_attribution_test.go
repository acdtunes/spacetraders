package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	navigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ferryCtxMediator captures the context the ferry's navigate is dispatched with. The
// context is the whole subject here: everything the leg spends downstream — the refuel
// above all — reads its attribution from it.
type ferryCtxMediator struct {
	common.Mediator
	navCtx  context.Context
	navSent bool
}

func (m *ferryCtxMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	if _, ok := request.(*shipNav.NavigateRouteCommand); ok {
		m.navCtx = ctx
		m.navSent = true
		return nil, nil
	}
	return nil, errors.New("ferryCtxMediator: unexpected request")
}

// ferryShipRepo serves the post-navigation reload with a hull parked at the origin.
type ferryShipRepo struct {
	navigation.ShipRepository
	ship *navigation.Ship
}

func (r *ferryShipRepo) FindBySymbol(context.Context, string, shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

func ferryTestShip(t *testing.T, at string) *navigation.Ship {
	t.Helper()
	wp, err := shared.NewWaypoint(at, 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(400, 400)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		"TORWIND-11", shared.MustNewPlayerID(1), wp, fuel, 400,
		100, cargo, 40, "FRAME_PROBE", "SATELLITE", nil,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// ferriedOperationContext drives the real ferry leg and returns the operation context the
// navigate was dispatched under.
func ferriedOperationContext(t *testing.T, requested string) *shared.OperationContext {
	t.Helper()
	med := &ferryCtxMediator{}
	origin := ferryTestShip(t, "X1-KP46-A1")
	h := &PurchaseShipHandler{mediator: med, shipRepo: &ferryShipRepo{ship: origin}}

	if _, err := h.navigateToShipyard(context.Background(),
		&PurchaseShipCommand{
			PurchasingShipSymbol: "TORWIND-11",
			ShipType:             "SHIP_PROBE",
			PlayerID:             shared.MustNewPlayerID(1),
			OperationType:        requested,
		},
		"X1-KP46-YARD", // a DIFFERENT waypoint, so the ferry actually flies
		origin,
	); err != nil {
		t.Fatalf("navigateToShipyard: %v", err)
	}
	// Calibration: a hull already at the yard returns before dispatching anything, and the
	// assertions below would then pass against a nil context for the wrong reason.
	if !med.navSent {
		t.Fatal("the ferry never dispatched a navigate, so nothing was attributed and this proves nothing")
	}
	return shared.OperationContextFromContext(med.navCtx)
}

// TestPurchaseShip_FerryCarriesTheCallersOperation is the defect. The flight to the yard
// burns fuel, and the refuel that pays for it reads its attribution off the context. With
// nothing stamped the refuel books under the unpropagated else-branch, so a continuous cost
// of buying ships is filed as if an operator had typed it by hand.
func TestPurchaseShip_FerryCarriesTheCallersOperation(t *testing.T) {
	opCtx := ferriedOperationContext(t, "sensing coverage")

	if opCtx == nil {
		t.Fatal("the ferry flew with no operation context, so everything it spends books as unpropagated")
	}
	if !opCtx.IsValid() {
		t.Fatalf("operation context is incomplete %+v - the readers all require both fields, so a half-filled one attributes nothing", opCtx)
	}
	if opCtx.OperationType != "sensing coverage" {
		t.Fatalf("ferry booked under %q, want the caller's own %q", opCtx.OperationType, "sensing coverage")
	}
}

// The default matters as much as the named case: a caller that names nothing is growing the
// fleet, and its ferry must book under that rather than under the else-branch.
func TestPurchaseShip_UnnamedFerryBooksFleetExpansion(t *testing.T) {
	opCtx := ferriedOperationContext(t, "")

	if opCtx == nil || !opCtx.IsValid() {
		t.Fatalf("an unnamed purchase still ferries a hull and still burns fuel; got %+v", opCtx)
	}
	if opCtx.OperationType != OperationTypeFleetExpansion {
		t.Fatalf("unnamed ferry booked under %q, want %q", opCtx.OperationType, OperationTypeFleetExpansion)
	}
}

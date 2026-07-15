package commands

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// warehouseOpCtxMediator records the operation_type carried on the ctx of the parking
// NavigateRouteCommand the warehouse fires to move its hull to the home waypoint. That
// hop's refuels inherit its ctx, so the ctx decides whether they attribute to the
// warehouse operation or fall through to operation_type='manual' (sp-zc8i). Returning
// (nil, nil) is enough: setup's navResp type-assertion is guarded, so it keeps the
// original ship and still reaches registration.
type warehouseOpCtxMediator struct {
	sawNav    bool
	navOpType string
}

func capturedWarehouseOpType(ctx context.Context) string {
	if oc := shared.OperationContextFromContext(ctx); oc != nil {
		return oc.NormalizedOperationType()
	}
	return ""
}

func (m *warehouseOpCtxMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	if _, ok := request.(*shipNav.NavigateRouteCommand); ok {
		m.sawNav = true
		m.navOpType = capturedWarehouseOpType(ctx)
	}
	return nil, nil
}

func (m *warehouseOpCtxMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *warehouseOpCtxMediator) RegisterMiddleware(common.Middleware)               {}

// The context-less warehouse parking hop (sp-zc8i). When its dedicated hull is parked
// away from the home waypoint, the warehouse navigates it there on a bare ctx, so that
// hop's refuels landed operation_type='manual'. This asserts at the NavigateRouteCommand
// boundary that the parking hop — and thus the refuels it drives — runs under
// operation_type='warehouse'.
func TestRunWarehouse_ParkingNavigate_CarriesWarehouseOperationContext(t *testing.T) {
	ctx := context.Background()
	logger := common.LoggerFromContext(ctx)

	// A hull parked AWAY from its home waypoint, so setup must navigate it home.
	hull := newWarehouseTestHull(t, "HULL-STORE-OPTYPE", "X1-HOME-Z9", 120, nil)
	med := &warehouseOpCtxMediator{}
	handler := NewRunWarehouseHandler(
		med, &stubWarehouseShipRepo{ship: hull}, newStubWarehouseOpRepo(), storageApp.NewInMemoryStorageCoordinator(), nil,
	)

	_, err := handler.setup(ctx, &RunWarehouseCommand{
		ShipSymbol:     "HULL-STORE-OPTYPE",
		WaypointSymbol: "X1-HOME-A1",
		PlayerID:       shared.MustNewPlayerID(1),
		ContainerID:    "warehouse-X1-HOME-A1",
		OperationID:    "warehouse-X1-HOME-A1",
		SupportedGoods: []string{"IRON_ORE"},
	}, logger)

	require.NoError(t, err)
	require.True(t, med.sawNav, "a hull parked away from home is navigated to the home waypoint")
	require.Equal(t, "warehouse", med.navOpType,
		"parking navigate recorded operation_type=%q, want \"warehouse\" (was the 'manual' leak)", med.navOpType)
}

package liquidation

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// liquidationOpCtxMediator drives the navigate->dock->sell leg like the recording
// mediator, but ALSO records the operation_type carried on the ctx of each sell and
// navigate dispatch: the liquidation worker must stamp its ctx at the boundary so its
// SELL_CARGO row — and every refuel the RouteExecutor fires inside the navigate to the
// sink — inherits the liquidation operation instead of falling through to
// operation_type='manual'.
type liquidationOpCtxMediator struct {
	sawSell, sawNav       bool
	sellOpType, navOpType string
	sellPricePerUnit      int
}

// capturedLiquidationOpType extracts the operation_type the ledger recorder would
// persist for a command dispatched under ctx — exactly what cargo_transaction.go and
// refuel_ship.go read (shared.OperationContextFromContext(...).NormalizedOperationType()),
// or "" when no operation context is present (the 'manual' fallback).
func capturedLiquidationOpType(ctx context.Context) string {
	if oc := shared.OperationContextFromContext(ctx); oc != nil {
		return oc.NormalizedOperationType()
	}
	return ""
}

func (m *liquidationOpCtxMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *navCmd.NavigateRouteCommand:
		m.sawNav = true
		m.navOpType = capturedLiquidationOpType(ctx)
		return nil, nil
	case *shipTypes.DockShipCommand:
		return nil, nil
	case *shipCargo.SellCargoCommand:
		m.sawSell = true
		m.sellOpType = capturedLiquidationOpType(ctx)
		return &shipCargo.SellCargoResponse{UnitsSold: cmd.Units, TotalRevenue: cmd.Units * m.sellPricePerUnit}, nil
	default:
		return nil, nil
	}
}

func (m *liquidationOpCtxMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *liquidationOpCtxMediator) RegisterMiddleware(common.Middleware)               {}

// The liquidation worker delegates its SELL to the shared cargo command, which is
// ctx-transparent: whatever operation context sits on the Handle ctx is what the
// SELL_CARGO row records. This asserts the observable outcome at the ledger-dispatch
// boundary: a run bearing a CoordinatorID records its sell (and its sink navigate)
// under operation_type='liquidation'.
func TestLiquidateCargo_SellLeg_CarriesLiquidationOperationContext(t *testing.T) {
	ship := shipWithCargo(t, "TORWIND-OPTYPE-1", "X1-KA42-A1", []*shared.CargoItem{item(t, "SILICON_CRYSTALS", 66)})
	repo := &fakeSyncShipRepo{ship: ship}
	mkt := &fakeMarketRepo{byGood: map[string]*market.BestMarketBuyingResult{
		"SILICON_CRYSTALS": {WaypointSymbol: "X1-KA42-B7", TradeSymbol: "SILICON_CRYSTALS", PurchasePrice: 2200},
	}}
	med := &liquidationOpCtxMediator{sellPricePerUnit: 2200}
	h := NewLiquidateCargoHandler(repo, mkt, med)

	_, err := h.Handle(context.Background(), &LiquidateCargoCommand{
		PlayerID:      shared.MustNewPlayerID(1),
		ShipSymbol:    "TORWIND-OPTYPE-1",
		CoordinatorID: "contract-coordinator-XYZ",
	})

	require.NoError(t, err)
	require.True(t, med.sawSell, "expected the worker to sell the leftover lot")
	require.Equal(t, "liquidation", med.sellOpType,
		"SELL_CARGO recorded operation_type=%q, want \"liquidation\" (was the 'manual' leak)", med.sellOpType)
	require.True(t, med.sawNav, "expected the worker to navigate to the best in-system sink")
	require.Equal(t, "liquidation", med.navOpType,
		"the sink navigate (the refuel carrier) ran under operation_type=%q, want \"liquidation\"", med.navOpType)
}

// A worker spawned without a CoordinatorID (direct/CLI, unmanaged) yields a nil
// operation context and honestly stays 'manual' — NewOperationContext rejects an empty
// container id, so the stamp is a no-op and nothing is mis-attributed.
func TestLiquidateCargo_NoCoordinatorID_StaysManual(t *testing.T) {
	ship := shipWithCargo(t, "TORWIND-OPTYPE-2", "X1-KA42-B7", []*shared.CargoItem{item(t, "PLASTICS", 40)})
	repo := &fakeSyncShipRepo{ship: ship}
	mkt := &fakeMarketRepo{byGood: map[string]*market.BestMarketBuyingResult{
		"PLASTICS": {WaypointSymbol: "X1-KA42-B7", PurchasePrice: 1800},
	}}
	med := &liquidationOpCtxMediator{sellPricePerUnit: 1800}
	h := NewLiquidateCargoHandler(repo, mkt, med)

	_, err := h.Handle(context.Background(), &LiquidateCargoCommand{
		PlayerID:   shared.MustNewPlayerID(1),
		ShipSymbol: "TORWIND-OPTYPE-2",
	})

	require.NoError(t, err)
	require.True(t, med.sawSell)
	require.Equal(t, "", med.sellOpType, "an unmanaged liquidation stays operation_type='manual' (empty context)")
}

package commands

import (
	"context"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// arbOpCtxMediator drives the one-shot buy->travel->sell legs on the same lane
// economics the other arb cases use (buy at trSourceAsk, sell at trSellRevenue) but
// ALSO records the operation_type carried on the ctx of each buy, sell, and travel
// dispatch. It is the observation point for sp-ieqj: the arb coordinator must stamp
// its ctx so the shared trade-route legs — and every refuel the RouteExecutor fires
// inside travel — inherit the arb operation instead of falling through to
// operation_type='manual', which credited 46.9M of arbitrage P&L to no engine.
type arbOpCtxMediator struct {
	sawBuy, sawSell, sawTravel          bool
	buyOpType, sellOpType, travelOpType string
}

// capturedArbOpType extracts the operation_type the ledger recorder would persist
// for a command dispatched under ctx — exactly what cargo_transaction.go and
// refuel_ship.go read (shared.OperationContextFromContext(...).NormalizedOperationType()),
// or "" when no operation context is present (the 'manual' fallback).
func capturedArbOpType(ctx context.Context) string {
	if oc := shared.OperationContextFromContext(ctx); oc != nil {
		return oc.NormalizedOperationType()
	}
	return ""
}

func (m *arbOpCtxMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.sawBuy = true
		m.buyOpType = capturedArbOpType(ctx)
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * trSourceAsk, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.sawSell = true
		m.sellOpType = capturedArbOpType(ctx)
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * trSellRevenue, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	case *navCmd.NavigateRouteCommand:
		m.sawTravel = true
		m.travelOpType = capturedArbOpType(ctx)
		return nil, nil
	default:
		return nil, nil // dock, etc. succeed silently
	}
}

func (m *arbOpCtxMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *arbOpCtxMediator) RegisterMiddleware(common.Middleware)               {}

// newArbOpCtxHandler mirrors newArbHandler's wiring (same market economics, single
// claimed hull, spend-floor guard disabled) but swaps in the ctx-capturing mediator.
func newArbOpCtxHandler(ship *navigation.Ship) (*RunArbCoordinatorHandler, *arbOpCtxMediator) {
	fixture := &trFixture{}
	med := &arbOpCtxMediator{}
	marketRepo := &trFakeMarketRepo{fixture: fixture}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunArbCoordinatorHandler(med, shipRepo, marketRepo, nil, nil, nil)
	return handler, med
}

// FIX (b) — the context-less goods-trader (sp-ieqj). The arb coordinator delegates its
// buy and sell to the shared trade-route legs, which are ctx-transparent: whatever
// operation context sits on the Handle ctx is what the PURCHASE_CARGO / SELL_CARGO rows
// record. Because the coordinator never stamped one, both legs landed
// operation_type='manual'. This asserts the observable outcome at the ledger-dispatch
// boundary: a run bearing a ContainerID records BOTH legs under operation_type='arb_run'.
func TestArbCoordinator_BuyAndSell_CarryArbRunOperationContext(t *testing.T) {
	ship := newTradeHauler(t, "ARB-OPTYPE-1")
	h, med := newArbOpCtxHandler(ship)

	_, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol:  ship.ShipSymbol(),
		ContainerID: "arb-run-ARB-OPTYPE-1",
		Good:        trGood,
		BuyAt:       trSource,
		SellAt:      trDest,
		PlayerID:    1,
	})
	if err != nil {
		t.Fatalf("arb returned error: %v", err)
	}
	if !med.sawBuy || !med.sawSell {
		t.Fatalf("expected the one-shot to buy and sell, saw buy=%v sell=%v", med.sawBuy, med.sawSell)
	}
	if med.buyOpType != "arb_run" {
		t.Errorf("PURCHASE_CARGO recorded operation_type=%q, want \"arb_run\" (was the 'manual' leak)", med.buyOpType)
	}
	if med.sellOpType != "arb_run" {
		t.Errorf("SELL_CARGO recorded operation_type=%q, want \"arb_run\" (was the 'manual' leak)", med.sellOpType)
	}
}

// FIX (a) — refuels inherit their operation (sp-ieqj). Refuels fire inside travel's
// RouteExecutor, which propagates the travel ctx verbatim to every RefuelShipCommand
// (route_executor.refuelShip sends on the same ctx; refuel_ship.go then reads
// OperationContextFromContext or falls back to 'manual'). So the single ctx the
// coordinator hands to travel decides whether that run's refuels are attributed. This
// asserts at the NavigateRouteCommand boundary that an arb run's travel — and thus the
// refuels it drives — runs under operation_type='arb_run' rather than 'manual'.
func TestArbCoordinator_Travel_CarriesArbRunContext_SoRefuelsInherit(t *testing.T) {
	ship := newTradeHauler(t, "ARB-OPTYPE-2")
	h, med := newArbOpCtxHandler(ship)

	_, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol:  ship.ShipSymbol(),
		ContainerID: "arb-run-ARB-OPTYPE-2",
		Good:        trGood,
		BuyAt:       trSource,
		SellAt:      trDest,
		PlayerID:    1,
	})
	if err != nil {
		t.Fatalf("arb returned error: %v", err)
	}
	if !med.sawTravel {
		t.Fatal("expected the one-shot to travel to the destination, no NavigateRouteCommand seen")
	}
	if med.travelOpType != "arb_run" {
		t.Errorf("travel (the refuel carrier) ran under operation_type=%q, want \"arb_run\" (was the 'manual' leak)", med.travelOpType)
	}
}

package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ladderFakeMediator drives the purchase path like
// insufficientCreditsFakeMediator, but returns a real PurchaseCargoResponse so
// the ladder cap (sp-1z2h) has a realized per-unit price to compare against.
type ladderFakeMediator struct {
	common.Mediator

	navShip       *navigation.Ship
	perUnitPrice  int
	purchaseCalls int
}

func (m *ladderFakeMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch req := request.(type) {
	case *shipNav.NavigateRouteCommand:
		return &shipNav.NavigateRouteResponse{Status: "completed", Ship: m.navShip}, nil

	case *shipTypes.DockShipCommand:
		return nil, nil

	case *shipCargo.PurchaseCargoCommand:
		m.purchaseCalls++
		return &shipCargo.PurchaseCargoResponse{
			TotalCost:        req.Units * m.perUnitPrice,
			UnitsAdded:       req.Units,
			TransactionCount: 1,
		}, nil

	default:
		return nil, fmt.Errorf("unexpected mediator command in ladder test: %T", request)
	}
}

func ladderDelivery(units int) domainContract.Delivery {
	return domainContract.Delivery{
		TradeSymbol:       "IRON_ORE",
		DestinationSymbol: "X1-TEST-A1",
		UnitsRequired:     units,
		UnitsFulfilled:    0,
	}
}

func TestPurchaseLoop_LadderBreach_StopsBuyingAndLogsNumbers(t *testing.T) {
	ship := buildShipWithIronOre(t, 0) // capacity 40 → 80 units = 2 trips
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	mediator := &ladderFakeMediator{navShip: ship, perUnitPrice: 200} // 200 > 1.5×100
	cargoManager := NewCargoManager(mediator, shipRepo)
	executor := NewDeliveryExecutor(mediator, shipRepo, cargoManager)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	profitResult := &contractQueries.ProfitabilityResult{
		PurchaseCost:           80 * 100,
		CheapestMarketWaypoint: "X1-TEST-M1",
		MarketPrices:           map[string]int{"IRON_ORE": 100},
	}

	_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil, ladderDelivery(80), profitResult, &RunWorkflowResponse{}, nil)
	if err != nil {
		t.Fatalf("a ladder breach is a decision, not a failure: %v", err)
	}

	if mediator.purchaseCalls != 1 {
		t.Fatalf("breached trip must stop FURTHER buying: expected 1 purchase call, got %d", mediator.purchaseCalls)
	}

	warnings := logger.warnings()
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one ladder WARNING, got %d: %v", len(warnings), warnings)
	}
	msg := warnings[0]
	for _, want := range []string{
		"realized 200/unit", // what the trip actually paid
		"projected ask 100", // the basis the defer gate projected against
		"X1-TEST-M1",        // the market
		"40 units still unsourced",
		"never-skip",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("ladder WARNING must carry %q in the message text, got: %s", want, msg)
		}
	}
}

func TestPurchaseLoop_RealizedWithinCap_BuysAllTrips(t *testing.T) {
	ship := buildShipWithIronOre(t, 0)
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	mediator := &ladderFakeMediator{navShip: ship, perUnitPrice: 120} // 120 ≤ 1.5×100
	cargoManager := NewCargoManager(mediator, shipRepo)
	executor := NewDeliveryExecutor(mediator, shipRepo, cargoManager)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	profitResult := &contractQueries.ProfitabilityResult{
		PurchaseCost:           80 * 100,
		CheapestMarketWaypoint: "X1-TEST-M1",
		MarketPrices:           map[string]int{"IRON_ORE": 100},
	}

	_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil, ladderDelivery(80), profitResult, &RunWorkflowResponse{}, nil)
	if err != nil {
		t.Fatalf("in-cap purchases must run to completion: %v", err)
	}
	if mediator.purchaseCalls != 2 {
		t.Fatalf("expected both trips to buy, got %d purchase calls", mediator.purchaseCalls)
	}
	if warnings := logger.warnings(); len(warnings) != 0 {
		t.Fatalf("no WARNING expected inside the cap, got: %v", warnings)
	}
}

func TestPurchaseLoop_NoProjectedBasis_LadderCapDisabled(t *testing.T) {
	ship := buildShipWithIronOre(t, 0)
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	mediator := &ladderFakeMediator{navShip: ship, perUnitPrice: 9999} // any price
	cargoManager := NewCargoManager(mediator, shipRepo)
	executor := NewDeliveryExecutor(mediator, shipRepo, cargoManager)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	// Pre-sp-1z2h ProfitabilityResult shape: no MarketPrices basis → the cap
	// has nothing to compare against and must not fire.
	profitResult := &contractQueries.ProfitabilityResult{
		PurchaseCost:           80 * 100,
		CheapestMarketWaypoint: "X1-TEST-M1",
	}

	_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil, ladderDelivery(80), profitResult, &RunWorkflowResponse{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mediator.purchaseCalls != 2 {
		t.Fatalf("cap must be inert without a basis, got %d purchase calls", mediator.purchaseCalls)
	}
	if warnings := logger.warnings(); len(warnings) != 0 {
		t.Fatalf("no WARNING expected without a basis, got: %v", warnings)
	}
}

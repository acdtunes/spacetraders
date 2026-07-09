package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// newArbHandler wires the one-shot arb coordinator onto the SAME lane economics the
// trade-route tests use (trFakeMarketRepo: source ask 2000, dest bid 4000 before any
// fill; trFakeMediator: buy at 2000/unit, sell at 3500/unit). apiClient is caller-
// supplied so the spend-floor cases can inject a live-treasury fake; nil leaves the
// guard disabled, exactly as the base happy-path/caps/margin cases want it.
func newArbHandler(ship *navigation.Ship, apiClient domainPorts.APIClient) (*RunArbCoordinatorHandler, *trFakeMediator) {
	fixture := &trFixture{}
	mediator := &trFakeMediator{fixture: fixture}
	marketRepo := &trFakeMarketRepo{fixture: fixture}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunArbCoordinatorHandler(mediator, shipRepo, marketRepo, nil, nil, apiClient)
	return handler, mediator
}

func arbResponse(t *testing.T, resp interface{}) *RunArbCoordinatorResponse {
	t.Helper()
	arb, ok := resp.(*RunArbCoordinatorResponse)
	if !ok || arb == nil {
		t.Fatalf("unexpected response %T", resp)
	}
	return arb
}

// The core acceptance: ONE command buys at the source, travels to the destination,
// sells ONCE, and stops — no loop, no second visit. The hull's full 40u hold is
// bought at 2000 and sold at 3500 for a clean +60000.
func TestArbCoordinator_HappyOneShot_BuysTravelsSellsStops(t *testing.T) {
	ship := newTradeHauler(t, "ARB-1")
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("arb returned error: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Completed || arb.Aborted {
		t.Fatalf("expected a completed, non-aborted run, got %+v", arb)
	}
	if arb.Good != trGood || arb.SourceWaypoint != trSource || arb.DestWaypoint != trDest {
		t.Fatalf("wrong lane: good=%q source=%q dest=%q", arb.Good, arb.SourceWaypoint, arb.DestWaypoint)
	}
	// Full 40u hold, bought at 2000 and sold at 3500.
	if arb.UnitsTraded != 40 {
		t.Fatalf("expected 40 units traded (full hold), got %d", arb.UnitsTraded)
	}
	if arb.TotalCost != 80000 || arb.TotalRevenue != 140000 || arb.NetProfit != 60000 {
		t.Fatalf("unexpected economics: cost=%d revenue=%d net=%d", arb.TotalCost, arb.TotalRevenue, arb.NetProfit)
	}
	// The margin gate saw the live 2000 ask vs the 4000 dest bid.
	if arb.SourceAsk != 2000 || arb.DestBid != 4000 || arb.MarginPerUnit != 2000 {
		t.Fatalf("unexpected gate prices: ask=%d bid=%d margin=%d", arb.SourceAsk, arb.DestBid, arb.MarginPerUnit)
	}
	// ONE-SHOT: exactly one buy and one sell — never a loop.
	if len(mediator.purchases) != 1 || len(mediator.sells) != 1 {
		t.Fatalf("expected exactly 1 buy and 1 sell (one-shot, no loop), got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

// --max-units caps the tranche below the hull's hold.
func TestArbCoordinator_MaxUnitsCapHonored(t *testing.T) {
	ship := newTradeHauler(t, "ARB-2")
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		MaxUnits:   15,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("arb returned error: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Completed {
		t.Fatalf("expected completion, got %+v", arb)
	}
	if arb.UnitsTraded != 15 {
		t.Fatalf("expected 15 units (max-units cap), got %d", arb.UnitsTraded)
	}
	if len(mediator.purchases) != 1 || mediator.purchases[0].Units != 15 {
		t.Fatalf("expected a single 15u purchase, got %+v", mediator.purchases)
	}
	if arb.TotalCost != 30000 {
		t.Fatalf("expected cost 30000 (15 x 2000), got %d", arb.TotalCost)
	}
}

// --max-spend caps the tranche by working capital: 50000 / 2000 ask = 25 units, so the
// buy never spends more than the cap.
func TestArbCoordinator_MaxSpendCapHonored(t *testing.T) {
	ship := newTradeHauler(t, "ARB-3")
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		MaxSpend:   50000,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("arb returned error: %v", err)
	}
	arb := arbResponse(t, resp)

	if arb.UnitsTraded != 25 {
		t.Fatalf("expected 25 units (50000 / 2000 ask), got %d", arb.UnitsTraded)
	}
	if arb.TotalCost != 50000 {
		t.Fatalf("expected cost exactly at the 50000 cap, got %d", arb.TotalCost)
	}
	if arb.TotalCost > 50000 {
		t.Fatalf("max-spend cap breached: cost %d > 50000", arb.TotalCost)
	}
	if len(mediator.purchases) != 1 || mediator.purchases[0].Units != 25 {
		t.Fatalf("expected a single 25u purchase, got %+v", mediator.purchases)
	}
}

// --min-margin refuses the buy pre-flight when the spread misses the floor: the live
// margin is 2000/unit (4000 bid − 2000 ask), so a 2500 floor aborts before any buy.
func TestArbCoordinator_MinMarginAbortsBeforeBuy(t *testing.T) {
	ship := newTradeHauler(t, "ARB-4")
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		MinMargin:  2500,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Aborted || !arb.MarginAbort {
		t.Fatalf("expected a margin abort, got %+v", arb)
	}
	if arb.Completed {
		t.Fatalf("a refused run must not report Completed, got %+v", arb)
	}
	if arb.MarginPerUnit != 2000 || arb.MinMarginFloor != 2500 {
		t.Fatalf("expected margin 2000 vs floor 2500, got margin=%d floor=%d", arb.MarginPerUnit, arb.MinMarginFloor)
	}
	if arb.AbortReason == "" {
		t.Fatalf("a margin abort must report why")
	}
	if len(mediator.purchases) != 0 || len(mediator.sells) != 0 {
		t.Fatalf("expected zero trades on a margin abort, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

// The spend-floor guard refuses a buy that would drop live treasury below the reserve:
// 100000 credits − (40u x 2000) 80000 projected = 20000 < 50000 default reserve.
func TestArbCoordinator_SpendFloorAbortsBeforeBreachingBuy(t *testing.T) {
	ship := newTradeHauler(t, "ARB-5")
	apiClient := &sfFakeAPIClient{credits: 100000}
	h, mediator := newArbHandler(ship, apiClient)

	ctx := auth.WithPlayerToken(context.Background(), "TOKEN-ARB5")
	resp, err := h.Handle(ctx, &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Aborted || !arb.SpendFloorAbort {
		t.Fatalf("expected a spend-floor abort, got %+v", arb)
	}
	if arb.TreasuryAtAbort != 100000 {
		t.Fatalf("expected the live treasury figure 100000 that revealed the breach, got %d", arb.TreasuryAtAbort)
	}
	if arb.ReserveFloor != defaultWorkingCapitalReserve {
		t.Fatalf("expected the default reserve floor %d, got %d", defaultWorkingCapitalReserve, arb.ReserveFloor)
	}
	if len(mediator.purchases) != 0 || len(mediator.sells) != 0 {
		t.Fatalf("expected zero trades on a spend-floor abort, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

// Fail-closed: a wired apiClient with NO player token in context must abort the buy
// rather than spend blind, even with ample credits.
func TestArbCoordinator_SpendFloorFailsClosedWhenTokenMissing(t *testing.T) {
	ship := newTradeHauler(t, "ARB-6")
	apiClient := &sfFakeAPIClient{credits: 1000000} // ample — the abort must come from the missing token
	h, mediator := newArbHandler(ship, apiClient)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Aborted || !arb.SpendFloorAbort {
		t.Fatalf("expected a fail-closed spend-floor abort on the missing token, got %+v", arb)
	}
	if arb.TreasuryAtAbort != 0 {
		t.Fatalf("a blind fail-closed abort must not populate a live figure it never observed, got %d", arb.TreasuryAtAbort)
	}
	if len(mediator.purchases) != 0 {
		t.Fatalf("expected zero buys on a fail-closed abort, got %d", len(mediator.purchases))
	}
}

// A comfortable treasury must NOT trip the guard: the run trades with the same
// economics as the no-apiClient happy path, proving the guard is not overly aggressive.
func TestArbCoordinator_SpendFloorDoesNotAbortWhenTreasuryClears(t *testing.T) {
	ship := newTradeHauler(t, "ARB-7")
	apiClient := &sfFakeAPIClient{credits: 500000}
	h, mediator := newArbHandler(ship, apiClient)

	ctx := auth.WithPlayerToken(context.Background(), "TOKEN-ARB7")
	resp, err := h.Handle(ctx, &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("arb returned error: %v", err)
	}
	arb := arbResponse(t, resp)

	if arb.SpendFloorAbort || arb.Aborted {
		t.Fatalf("did not expect an abort with a comfortable treasury, got %+v", arb)
	}
	if !arb.Completed || arb.UnitsTraded != 40 || arb.NetProfit != 60000 {
		t.Fatalf("expected the full +60000 trade, got %+v", arb)
	}
	if len(mediator.purchases) != 1 || len(mediator.sells) != 1 {
		t.Fatalf("expected exactly 1 buy and 1 sell, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

// The location guard refuses to buy when the hull is not actually at the source: the
// hull is docked at trSource, so a run targeting BuyAt=trDest aborts before buying.
func TestArbCoordinator_LocationGuardAbortsWhenNotAtSource(t *testing.T) {
	ship := newTradeHauler(t, "ARB-8") // docked at trSource
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trDest,   // hull is NOT here
		SellAt:     trSource, // must differ from BuyAt
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Aborted || !arb.LocationAbort {
		t.Fatalf("expected a location abort, got %+v", arb)
	}
	if arb.ExpectedLocation != trDest || arb.ActualLocation != trSource {
		t.Fatalf("expected location %s but ship at %s; got expected=%q actual=%q", trDest, trSource, arb.ExpectedLocation, arb.ActualLocation)
	}
	if len(mediator.purchases) != 0 {
		t.Fatalf("expected zero buys on a location abort, got %d", len(mediator.purchases))
	}
}

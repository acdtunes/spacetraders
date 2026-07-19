package commands

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
)

// --- The arb executor arms the per-tranche sell floor and treats a
// floored held-remainder as an HONEST failure completion (never a false success).

// The arb coordinator must ARM the sell with the 80%-of-quote floor: DestBid is
// quoted 4000, so the SellCargoCommand carries MinBidPerUnit = ceil(0.80×4000) =
// 3200. Remove the arming and this goes red (MinBidPerUnit 0). The fake mediator
// records the sell command so the armed floor is inspectable.
func TestArbCoordinator_SellFloor_ArmsQuotedBidFloorOnTheSell(t *testing.T) {
	ship := newTradeHauler(t, "ARB-ARM")
	h, mediator := newArbHandler(ship, nil)

	_, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("happy run errored: %v", err)
	}
	if len(mediator.sells) != 1 {
		t.Fatalf("expected exactly one sell, got %d", len(mediator.sells))
	}
	if got := mediator.sells[0].MinBidPerUnit; got != 3200 {
		t.Fatalf("the sell must be armed with the 80%%-of-quote floor (ceil(0.80×4000)=3200), got %d", got)
	}
}

// The floor fraction is config-driven (reusing the 80% knob): a run with an
// explicit SellFloorFraction arms ceil(fraction × quoted bid) instead of the
// default. 0.50 × 4000 = 2000.
func TestArbCoordinator_SellFloor_UsesConfiguredFraction(t *testing.T) {
	ship := newTradeHauler(t, "ARB-FRAC")
	h, mediator := newArbHandler(ship, nil)

	_, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol:        ship.ShipSymbol(),
		Good:              trGood,
		BuyAt:             trSource,
		SellAt:            trDest,
		PlayerID:          1,
		SellFloorFraction: 0.50,
	})
	if err != nil {
		t.Fatalf("run errored: %v", err)
	}
	if got := mediator.sells[0].MinBidPerUnit; got != 2000 {
		t.Fatalf("the floor must use the configured 50%%-of-quote (2000), got %d", got)
	}
}

// arbSellFloorMediator drives buy→travel→sell but makes the SELL report a
// per-tranche floor abort: it sells only soldUnits and returns FloorAborted with
// the crashed bid, exactly as the real handler does when the live bid collapses
// mid-sale. It records buys/sells like the other arb fakes.
type arbSellFloorMediator struct {
	purchases []*shipCargo.PurchaseCargoCommand
	sells     []*shipCargo.SellCargoCommand
	soldUnits int
	floorBid  int
}

func (m *arbSellFloorMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.purchases = append(m.purchases, cmd)
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * trSourceAsk, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.sells = append(m.sells, cmd)
		return &shipCargo.SellCargoResponse{
			TotalRevenue:     m.soldUnits * trSellRevenue,
			UnitsSold:        m.soldUnits,
			TransactionCount: 1,
			FloorAborted:     true,
			FloorObservedBid: m.floorBid,
		}, nil
	default:
		return nil, nil
	}
}

func (m *arbSellFloorMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *arbSellFloorMediator) RegisterMiddleware(common.Middleware)               {}

// THE H50 case at the arb-coordinator seam: the live bid crashes mid-sale, the
// floor aborts with the remainder held aboard, and the run reports an HONEST
// failure (non-nil error → the runner's signalCompletionWithStatus(false)) that
// names the floor — never the false success the incident logged. The held cargo
// is left for the next liquidation leg.
func TestArbCoordinator_SellFloorAbort_HeldRemainder_IsHonestFailure(t *testing.T) {
	ship := newTradeHauler(t, "ARB-H50") // buys the full 40u hold
	mediator := &arbSellFloorMediator{soldUnits: 15, floorBid: 4}
	h := arbHandlerWith(mediator, ship)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err == nil {
		t.Fatal("a sell-floor abort with a held remainder must return an error (honest failure), not a silent success")
	}
	arb := arbResponse(t, resp)

	if arb.Completed {
		t.Fatalf("a floored run must NOT report Completed, got %+v", arb)
	}
	if !arb.SellFloorAbort {
		t.Fatalf("a floored run must set SellFloorAbort, got %+v", arb)
	}
	// The message names the floor and the held cargo (good + location) so it is
	// greppable and the liquidation path can act.
	for _, want := range []string{"sell-floor", trGood, trDest} {
		if !strings.Contains(arb.AbortReason, want) {
			t.Fatalf("floor-abort message %q must mention %q", arb.AbortReason, want)
		}
	}
	// Only the 15 units that sold before the crash are booked; the remaining 25
	// stay aboard (not dumped at 4/u).
	if arb.UnitsTraded != 15 {
		t.Fatalf("only the units sold before the floor may be booked, got %d", arb.UnitsTraded)
	}
	if len(mediator.sells) != 1 {
		t.Fatalf("the coordinator issues one floored sell, got %d", len(mediator.sells))
	}
}

// A healthy sell (no floor abort reported) completes normally, proving the arming
// does not turn ordinary sells into failures.
func TestArbCoordinator_SellFloor_HealthySale_Completes(t *testing.T) {
	ship := newTradeHauler(t, "ARB-OK")
	h, _ := newArbHandler(ship, nil) // trFakeMediator: full sale, no FloorAborted

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a healthy floored-arming run must complete, got error: %v", err)
	}
	arb := arbResponse(t, resp)
	if !arb.Completed || arb.SellFloorAbort {
		t.Fatalf("a healthy sale must complete without a floor abort, got %+v", arb)
	}
	if arb.UnitsTraded != 40 {
		t.Fatalf("the full tranche must sell on a healthy bid, got %d", arb.UnitsTraded)
	}
}

// compile-time guard: the fake satisfies the mediator interface.
var _ common.Mediator = (*arbSellFloorMediator)(nil)

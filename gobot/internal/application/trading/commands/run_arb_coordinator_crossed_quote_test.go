package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// The arb coordinator re-reads BOTH sides live and computes margin = destBid −
// sourceAsk. That subtraction is a money guard, and it is the one guard in the
// trading path that a crossed quote defeats by making the margin look BIGGER
// rather than smaller — so it fails OPEN unless a crossed quote is refused first.
//
// Unlike a ranked lane, the arb command arrives with its source and destination
// already chosen (the long-haul engine picks them through BestSourcesAcrossSystems /
// BestSinksAcrossSystems, not through RankSpreads), so trading.GoodListing.IsCrossed
// never sees these rows. This guard is that path's equivalent.
//
// The numbers are a real pre-sp-en5h7 MACHINERY row: the API quoted purchasePrice
// 6334 / sellPrice 3123, and the transposed writer stored purchase_price=3123,
// sell_price=6334. Read with the corrected accessors that is ask 3123 / bid 6334 —
// a market selling below what it pays. Believed, it reports a 3211/unit margin that
// clears any floor the captain is likely to set, on a lane that does not exist.
type arbCrossedMarketRepo struct {
	*trFakeMarketRepo
}

func (r *arbCrossedMarketRepo) GetMarketData(_ context.Context, waypointSymbol string, _ int) (*market.Market, error) {
	supply, activity := "MODERATE", "STRONG"
	good, err := market.NewTradeGood(trGood, &supply, &activity, 3123, 6334, 60, market.TradeTypeImport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
}

func TestArbCoordinator_CrossedQuote_RefusesBeforeBuy(t *testing.T) {
	ship := newTradeHauler(t, "ARB-CROSSED")
	fixture := &trFixture{}
	mediator := &trFakeMediator{fixture: fixture}
	marketRepo := &arbCrossedMarketRepo{trFakeMarketRepo: &trFakeMarketRepo{fixture: fixture}}
	h := NewRunArbCoordinatorHandler(mediator, &trFakeShipRepo{ship: ship}, marketRepo, nil, nil, nil)

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

	if !arb.Aborted || !arb.MarginAbort {
		t.Fatalf("a crossed quote must abort before the buy, got %+v", arb)
	}
	if arb.AbortReason == "" {
		t.Fatalf("a crossed-quote refusal must say why")
	}
	// The whole point: not one credit is spent on the phantom spread.
	if len(mediator.purchases) != 0 || len(mediator.sells) != 0 {
		t.Fatalf("expected zero trades on a crossed quote, got %d buys / %d sells",
			len(mediator.purchases), len(mediator.sells))
	}
	if arb.Completed {
		t.Fatalf("a refused run must not report Completed, got %+v", arb)
	}
}

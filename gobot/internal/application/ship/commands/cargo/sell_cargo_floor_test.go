package cargo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- sp-lbbm per-tranche sell floor -----------------------------------------
//
// The H50 loss: after tranche 1 crushed the SHIP_PARTS bid 19,950→4, the sale
// KEPT going (five tranches for 27 credits total). The floor re-reads the LIVE
// bid before each tranche and aborts the remainder when it falls below the armed
// per-unit floor, leaving the rest aboard. These drive the real
// CargoTransactionHandler tranche loop.

// floorMarketFixture models a sink whose bid crashes after the first sale, shared
// between the market repo (which serves the current bid) and the fake API (which
// records the sale and depresses the bid). limit is the per-tranche transaction
// size, so a large order splits into multiple tranches — the shape the floor
// governs.
type floorMarketFixture struct {
	healthyBid int
	crashedBid int
	limit      int
	sellsDone  int
}

func (f *floorMarketFixture) bid() int {
	if f.sellsDone == 0 {
		return f.healthyBid
	}
	return f.crashedBid
}

type floorFakeMarketRepo struct {
	scoutingQuery.MarketRepository
	fix      *floorMarketFixture
	waypoint string
	good     string
}

func (r *floorFakeMarketRepo) GetMarketData(_ context.Context, _ string, _ int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "STRONG"
	// PurchasePrice = the bid we RECEIVE selling; the 6th arg is the transaction
	// (tranche) limit GetTransactionLimit reads.
	g, err := market.NewTradeGood(r.good, &supply, &activity, r.fix.bid(), r.fix.bid()+100, r.fix.limit, market.TradeTypeImport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(r.waypoint, []market.TradeGood{*g}, time.Now())
}

type floorFakeAPI struct {
	domainPorts.APIClient
	fix   *floorMarketFixture
	sells []int // units per sell tranche that actually reached the API
}

func (c *floorFakeAPI) SellCargo(_ context.Context, _, _ string, units int, _ string) (*domainPorts.SellResult, error) {
	c.sells = append(c.sells, units)
	rev := units * c.fix.bid() // realized at the current bid
	c.fix.sellsDone++          // the sale depresses the sink for the next tranche
	return &domainPorts.SellResult{TotalRevenue: rev, UnitsSold: units}, nil
}

type floorFakeRefresher struct{ err error }

func (r *floorFakeRefresher) ScanAndSaveMarket(_ context.Context, _ uint, _ string) error {
	return r.err
}

func newFloorSellHandler(t *testing.T, fix *floorMarketFixture, refresher MarketRefresher) (*SellCargoHandler, *floorFakeAPI) {
	t.Helper()
	api := &floorFakeAPI{fix: fix}
	marketRepo := &floorFakeMarketRepo{fix: fix, waypoint: testBuyWaypoint, good: optypeGood}
	shipRepo := &buyFakeShipRepo{ship: newDockedShipWithCargo(t, 1, optypeGood, 40)}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "tok")}
	h := NewSellCargoHandler(shipRepo, playerRepo, api, marketRepo, &buyRecordingMediator{}, refresher)
	return h, api
}

func runFloorSell(t *testing.T, h *SellCargoHandler, minBid int) *SellCargoResponse {
	t.Helper()
	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := h.Handle(ctx, &SellCargoCommand{
		ShipSymbol: "OPTYPE-1", GoodSymbol: optypeGood, Units: 40,
		PlayerID: shared.MustNewPlayerID(1), MinBidPerUnit: minBid,
	})
	require.NoError(t, err)
	return resp.(*SellCargoResponse)
}

// THE RED case: the live bid crashes 20,000→4 after tranche 1, so the floor (80%
// of the 20,000 quote = 16,000) aborts the remaining tranches — only the healthy
// first tranche sells, the rest stays aboard.
func TestSellCargo_PerTrancheFloor_AbortsOnBidCrash_HoldsRemainder(t *testing.T) {
	fix := &floorMarketFixture{healthyBid: 20000, crashedBid: 4, limit: 15}
	h, api := newFloorSellHandler(t, fix, &floorFakeRefresher{})

	sr := runFloorSell(t, h, 16000)

	if !sr.FloorAborted {
		t.Fatalf("the sell floor must abort when the live bid crashes below the floor, got %+v", sr)
	}
	if sr.UnitsSold != 15 {
		t.Fatalf("only the first (healthy) tranche of 15 may sell; the rest held aboard, got %d", sr.UnitsSold)
	}
	if sr.FloorObservedBid != 4 {
		t.Fatalf("the abort must report the crashed live bid 4, got %d", sr.FloorObservedBid)
	}
	if len(api.sells) != 1 {
		t.Fatalf("exactly one tranche may reach the API (the rest aborted), got %v", api.sells)
	}
}

// A bid that HOLDS never trips the floor: the whole order sells across its
// tranches, so the guard is not overly aggressive.
func TestSellCargo_PerTrancheFloor_HealthyBid_SellsWholeOrder(t *testing.T) {
	fix := &floorMarketFixture{healthyBid: 20000, crashedBid: 20000, limit: 15}
	h, api := newFloorSellHandler(t, fix, &floorFakeRefresher{})

	sr := runFloorSell(t, h, 16000)

	if sr.FloorAborted {
		t.Fatalf("a bid that holds must not trip the floor, got %+v", sr)
	}
	if sr.UnitsSold != 40 {
		t.Fatalf("all 40 units must sell when the bid holds, got %d", sr.UnitsSold)
	}
	if len(api.sells) != 3 {
		t.Fatalf("40 units at limit 15 = three tranches (15,15,10), got %v", api.sells)
	}
}

// The floor is strictly opt-in: with MinBidPerUnit==0 the loop is byte-identical
// to before — the whole order sells even as the bid crashes (the pre-fix path
// every non-arb caller still runs).
func TestSellCargo_NoFloorArmed_UnchangedEvenIfBidCrashes(t *testing.T) {
	fix := &floorMarketFixture{healthyBid: 20000, crashedBid: 4, limit: 15}
	h, api := newFloorSellHandler(t, fix, &floorFakeRefresher{})

	sr := runFloorSell(t, h, 0) // no floor

	if sr.FloorAborted {
		t.Fatalf("with no floor armed the sale must never floor-abort, got %+v", sr)
	}
	if sr.UnitsSold != 40 {
		t.Fatalf("with no floor all 40 units sell regardless of the bid, got %d", sr.UnitsSold)
	}
	if len(api.sells) != 3 {
		t.Fatalf("expected three unfloored tranches, got %v", api.sells)
	}
}

// Fail CLOSED: a wired refresher that ERRORS means the live bid cannot be
// verified, so even the first tranche is held rather than sold blind (RULINGS #4).
func TestSellCargo_PerTrancheFloor_FailsClosedWhenLiveReadErrors(t *testing.T) {
	fix := &floorMarketFixture{healthyBid: 20000, crashedBid: 20000, limit: 15}
	h, api := newFloorSellHandler(t, fix, &floorFakeRefresher{err: errors.New("market scan down")})

	sr := runFloorSell(t, h, 16000)

	if !sr.FloorAborted {
		t.Fatalf("a wired refresher that errors must fail CLOSED (unverifiable bid), got %+v", sr)
	}
	if sr.UnitsSold != 0 {
		t.Fatalf("fail-closed on the first tranche holds the whole order, got %d", sr.UnitsSold)
	}
	if len(api.sells) != 0 {
		t.Fatalf("no tranche may sell when the live bid cannot be verified, got %v", api.sells)
	}
}

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
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- sp-9mkf per-tranche buy ceiling ----------------------------------------
//
// The D39 loss: SHIP_PARTS bought against a cached quote while the source ask
// laddered 3,985→4,942→~7k INSIDE a single dispatch, realising −3,430/u. A single
// pre-buy margin check saw only the first (cheap) ask; the multi-tranche buy then
// walked the price up unguarded. The ceiling re-reads the LIVE ask before each
// tranche and aborts the remainder (left unbought) once it rises above the armed
// per-unit ceiling. These drive the real CargoTransactionHandler tranche loop — the
// exact mirror of the sp-lbbm sell floor.

// ceilingMarketFixture models a source whose ask ladders up after the first buy,
// shared between the market repo (which serves the current ask) and the fake API
// (which records the buy and raises the ask). limit is the per-tranche transaction
// size, so a large order splits into multiple tranches — the shape the ceiling
// governs.
type ceilingMarketFixture struct {
	healthyAsk int
	laddedAsk  int
	limit      int
	buysDone   int
}

func (f *ceilingMarketFixture) ask() int {
	if f.buysDone == 0 {
		return f.healthyAsk
	}
	return f.laddedAsk
}

type ceilingFakeMarketRepo struct {
	scoutingQuery.MarketRepository
	fix      *ceilingMarketFixture
	waypoint string
	good     string
}

func (r *ceilingFakeMarketRepo) GetMarketData(_ context.Context, _ string, _ int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "WEAK"
	// SellPrice (5th arg) = the ASK we pay buying; PurchasePrice (4th) = the bid.
	// The 6th arg is the transaction (tranche) limit GetTransactionLimit reads.
	// EXPORT: this is a buy SOURCE.
	g, err := market.NewTradeGood(r.good, &supply, &activity, r.fix.ask()-100, r.fix.ask(), r.fix.limit, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(r.waypoint, []market.TradeGood{*g}, time.Now())
}

type ceilingFakeAPI struct {
	domainPorts.APIClient
	fix  *ceilingMarketFixture
	buys []int // units per buy tranche that actually reached the API
}

func (c *ceilingFakeAPI) PurchaseCargo(_ context.Context, _, _ string, units int, _ string) (*domainPorts.PurchaseResult, error) {
	c.buys = append(c.buys, units)
	cost := units * c.fix.ask() // realized at the current ask
	c.fix.buysDone++            // the buy ladders the source for the next tranche
	return &domainPorts.PurchaseResult{TotalCost: cost, UnitsAdded: units}, nil
}

type ceilingFakeRefresher struct{ err error }

func (r *ceilingFakeRefresher) ScanAndSaveMarket(_ context.Context, _ uint, _ string) error {
	return r.err
}

func newCeilingBuyHandler(t *testing.T, fix *ceilingMarketFixture, refresher MarketRefresher) (*PurchaseCargoHandler, *ceilingFakeAPI) {
	t.Helper()
	api := &ceilingFakeAPI{fix: fix}
	marketRepo := &ceilingFakeMarketRepo{fix: fix, waypoint: testBuyWaypoint, good: optypeGood}
	// A docked hull with 40 free cargo units (empty hold, capacity 40).
	shipRepo := &buyFakeShipRepo{ship: newDockedBuyer(t, 40, 0, navigation.NavStatusDocked)}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "tok")}
	h := NewPurchaseCargoHandler(shipRepo, playerRepo, api, marketRepo, &buyRecordingMediator{}, refresher)
	return h, api
}

func runCeilingBuy(t *testing.T, h *PurchaseCargoHandler, maxAsk int) *PurchaseCargoResponse {
	t.Helper()
	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := h.Handle(ctx, &PurchaseCargoCommand{
		ShipSymbol: testBuyShip, GoodSymbol: optypeGood, Units: 40,
		PlayerID: shared.MustNewPlayerID(1), MaxAskPerUnit: maxAsk,
	})
	require.NoError(t, err)
	return resp.(*PurchaseCargoResponse)
}

// THE RED case: the live ask ladders 4,000→7,000 after tranche 1, so the ceiling
// (5,000) aborts the remaining tranches — only the healthy first tranche buys, the
// rest is left unbought (never spent above the ceiling).
func TestPurchaseCargo_PerTrancheCeiling_AbortsOnAskLadder_LeavesRemainderUnbought(t *testing.T) {
	fix := &ceilingMarketFixture{healthyAsk: 4000, laddedAsk: 7000, limit: 15}
	h, api := newCeilingBuyHandler(t, fix, &ceilingFakeRefresher{})

	pr := runCeilingBuy(t, h, 5000)

	if !pr.CeilingAborted {
		t.Fatalf("the buy ceiling must abort when the live ask ladders above the ceiling, got %+v", pr)
	}
	if pr.UnitsAdded != 15 {
		t.Fatalf("only the first (cheap) tranche of 15 may buy; the rest left unbought, got %d", pr.UnitsAdded)
	}
	if pr.CeilingObservedAsk != 7000 {
		t.Fatalf("the abort must report the laddered live ask 7000, got %d", pr.CeilingObservedAsk)
	}
	if len(api.buys) != 1 {
		t.Fatalf("exactly one tranche may reach the API (the rest aborted), got %v", api.buys)
	}
}

// An ask that HOLDS never trips the ceiling: the whole order buys across its
// tranches, so the guard is not overly aggressive.
func TestPurchaseCargo_PerTrancheCeiling_HealthyAsk_BuysWholeOrder(t *testing.T) {
	fix := &ceilingMarketFixture{healthyAsk: 4000, laddedAsk: 4000, limit: 15}
	h, api := newCeilingBuyHandler(t, fix, &ceilingFakeRefresher{})

	pr := runCeilingBuy(t, h, 5000)

	if pr.CeilingAborted {
		t.Fatalf("an ask that holds must not trip the ceiling, got %+v", pr)
	}
	if pr.UnitsAdded != 40 {
		t.Fatalf("all 40 units must buy when the ask holds below the ceiling, got %d", pr.UnitsAdded)
	}
	if len(api.buys) != 3 {
		t.Fatalf("40 units at limit 15 = three tranches (15,15,10), got %v", api.buys)
	}
}

// The ceiling is strictly opt-in: with MaxAskPerUnit==0 the loop is byte-identical
// to before — the whole order buys even as the ask ladders (the pre-fix path every
// non-arb caller still runs).
func TestPurchaseCargo_NoCeilingArmed_UnchangedEvenIfAskLadders(t *testing.T) {
	fix := &ceilingMarketFixture{healthyAsk: 4000, laddedAsk: 7000, limit: 15}
	h, api := newCeilingBuyHandler(t, fix, &ceilingFakeRefresher{})

	pr := runCeilingBuy(t, h, 0) // no ceiling

	if pr.CeilingAborted {
		t.Fatalf("with no ceiling armed the buy must never ceiling-abort, got %+v", pr)
	}
	if pr.UnitsAdded != 40 {
		t.Fatalf("with no ceiling all 40 units buy regardless of the ask, got %d", pr.UnitsAdded)
	}
	if len(api.buys) != 3 {
		t.Fatalf("expected three uncapped tranches, got %v", api.buys)
	}
}

// Fail CLOSED: a wired refresher that ERRORS means the live ask cannot be verified,
// so even the first tranche is held rather than bought blind (RULINGS #4).
func TestPurchaseCargo_PerTrancheCeiling_FailsClosedWhenLiveReadErrors(t *testing.T) {
	fix := &ceilingMarketFixture{healthyAsk: 4000, laddedAsk: 4000, limit: 15}
	h, api := newCeilingBuyHandler(t, fix, &ceilingFakeRefresher{err: errors.New("market scan down")})

	pr := runCeilingBuy(t, h, 5000)

	if !pr.CeilingAborted {
		t.Fatalf("a wired refresher that errors must fail CLOSED (unverifiable ask), got %+v", pr)
	}
	if pr.UnitsAdded != 0 {
		t.Fatalf("fail-closed on the first tranche buys nothing, got %d", pr.UnitsAdded)
	}
	if len(api.buys) != 0 {
		t.Fatalf("no tranche may buy when the live ask cannot be verified, got %v", api.buys)
	}
}

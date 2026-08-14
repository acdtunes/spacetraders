package cargo

import (
	"context"
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

// sp-7q61f adversarial review finding #1: flyVisits captures ONE dedupBracket
// per VISIT (run_trade_route_coordinator_circuit.go), forwarded unchanged
// into CargoTransactionCommand and read by EVERY tranche of a multi-tranche
// purchase inside CargoTransactionHandler.executeTransactions. The buy-ceiling
// guard's own reason to exist is catching a purchase's OWN earlier tranche
// laddering the ask (the D39 incident, proven undeduped by
// purchase_cargo_ceiling_test.go's
// TestPurchaseCargo_PerTrancheCeiling_AbortsOnAskLadder_LeavesRemainderUnbought).
// With a bracket armed and left untouched across tranches, dedup could keep
// reusing tranche 1's now-stale row for tranche 2+ and never notice the
// ladder. These tests prove: (a) tranche 1 may still legitimately dedup (the
// ticket's actual point), and (b) tranche 2+ must NEVER dedup — the bracket
// must be invalidated the moment a purchase tranche actually executes.

// ladderDedupFixture is the shared state behind both the market repo and the
// fake purchase API for a multi-tranche buy whose TRUE ask ladders after the
// first tranche fills (the D39 shape), combined with a scan-dedup-eligible
// cached row. cachedAsk is what GetMarketData currently reports — it only
// advances to the laddered ask once a LIVE scan actually runs
// (ScanAndSaveMarket); a deduped tranche reads the row completely unchanged,
// exactly like reality — a skipped live scan cannot discover a price move.
type ladderDedupFixture struct {
	healthyAsk int
	laddedAsk  int
	limit      int
	buysDone   int
	cachedAsk  int
	updatedAt  time.Time
}

func (f *ladderDedupFixture) trueAsk() int {
	if f.buysDone == 0 {
		return f.healthyAsk
	}
	return f.laddedAsk
}

type ladderDedupMarketRepo struct {
	scoutingQuery.MarketRepository
	fix      *ladderDedupFixture
	waypoint string
	good     string
}

func (r *ladderDedupMarketRepo) GetMarketData(_ context.Context, _ string, _ int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "WEAK"
	g, err := market.NewTradeGood(r.good, &supply, &activity, r.fix.cachedAsk, r.fix.cachedAsk-100, r.fix.limit, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(r.waypoint, []market.TradeGood{*g}, r.fix.updatedAt)
}

// ladderDedupRefresher is the live-scan port: every call is what a real
// GetMarket API call would be — it discovers the TRUE (possibly laddered)
// ask and re-stamps the row's freshness. ceilingCalls counts only the
// buy-ceiling guard's own calls (WithLiveScanRequired-stamped), the same
// split dedupCeilingRefresher (purchase_cargo_scandedup_test.go) uses.
type ladderDedupRefresher struct {
	fix          *ladderDedupFixture
	clock        *shared.MockClock
	ceilingCalls int
	otherCalls   int
}

func (r *ladderDedupRefresher) ScanAndSaveMarket(ctx context.Context, _ uint, _ string) error {
	if shared.LiveScanRequiredFromContext(ctx) {
		r.ceilingCalls++
	} else {
		r.otherCalls++
	}
	r.fix.cachedAsk = r.fix.trueAsk()
	r.fix.updatedAt = r.clock.Now()
	return nil
}

// ladderDedupAPI records each tranche's size and ladders the TRUE ask after
// every successful buy — the market moving because OUR OWN purchase just
// consumed supply, independent of whether anything has re-scanned it yet.
type ladderDedupAPI struct {
	domainPorts.APIClient
	fix  *ladderDedupFixture
	buys []int
}

func (a *ladderDedupAPI) PurchaseCargo(_ context.Context, _, _ string, units int, _ string) (*domainPorts.PurchaseResult, error) {
	a.buys = append(a.buys, units)
	a.fix.buysDone++
	return &domainPorts.PurchaseResult{TotalCost: units * 100, UnitsAdded: units}, nil
}

func newLadderDedupHandler(t *testing.T, fix *ladderDedupFixture, clock *shared.MockClock) (*PurchaseCargoHandler, *ladderDedupAPI, *ladderDedupRefresher) {
	t.Helper()
	api := &ladderDedupAPI{fix: fix}
	repo := &ladderDedupMarketRepo{fix: fix, waypoint: testBuyWaypoint, good: optypeGood}
	refresher := &ladderDedupRefresher{fix: fix, clock: clock}
	shipRepo := &buyFakeShipRepo{ship: newDockedBuyer(t, 40, 0, navigation.NavStatusDocked)}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "tok")}
	h := NewPurchaseCargoHandler(shipRepo, playerRepo, api, repo, &buyRecordingMediator{}, refresher)
	h.delegate.SetClock(clock)
	return h, api, refresher
}

// THE RED case this test proves fixed: a dedup bracket armed for the visit
// must NOT survive past tranche 1. The true ask ladders 4,000->7,000 the
// instant tranche 1 buys (D39 shape); tranche 2's ceiling check MUST catch it
// exactly as it would with no dedup armed at all — proving the bracket cannot
// be reused to blind the guard to a purchase's own laddering across its later
// tranches.
func TestDedupCeiling_MultiTranchePurchase_BracketMustNotSurviveTranche1_LadderStillCaught(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	fix := &ladderDedupFixture{healthyAsk: 4000, laddedAsk: 7000, limit: 15, cachedAsk: 4000, updatedAt: clock.CurrentTime}
	h, api, refresher := newLadderDedupHandler(t, fix, clock)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := h.Handle(ctx, &PurchaseCargoCommand{
		ShipSymbol: testBuyShip, GoodSymbol: optypeGood, Units: 40,
		PlayerID: shared.MustNewPlayerID(1), MaxAskPerUnit: 5000,
		ScanDedupBeforeTravel: clock.CurrentTime, ScanDedupAfterArrival: clock.CurrentTime,
	})
	require.NoError(t, err)
	pr := resp.(*PurchaseCargoResponse)

	require.True(t, pr.CeilingAborted, "tranche 2 must still catch the ladder even though a dedup bracket was armed for the visit")
	require.Equal(t, 7000, pr.CeilingObservedAsk)
	require.Equal(t, 15, pr.UnitsAdded, "only tranche 1 (the cheap ask) may buy; the ladder must stop the remainder unbought")
	require.Equal(t, 1, refresher.ceilingCalls, "tranche 1 legitimately dedups (0 scans); tranche 2 must be forced back onto a live scan (1) to ever see the ladder")
	require.Len(t, api.buys, 1)
}

// Tranche 1 dedup is the ticket's actual point and must keep working: a
// purchase that completes in a SINGLE tranche (units within the market's
// transaction limit) may still spend zero live scans for the ceiling guard.
func TestDedupCeiling_SingleTranchePurchase_StillDedupsTranche1(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	fix := &ladderDedupFixture{healthyAsk: 4000, laddedAsk: 7000, limit: 15, cachedAsk: 4000, updatedAt: clock.CurrentTime}
	h, api, refresher := newLadderDedupHandler(t, fix, clock)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := h.Handle(ctx, &PurchaseCargoCommand{
		ShipSymbol: testBuyShip, GoodSymbol: optypeGood, Units: 10, // within the tranche limit (15): a single tranche
		PlayerID: shared.MustNewPlayerID(1), MaxAskPerUnit: 5000,
		ScanDedupBeforeTravel: clock.CurrentTime, ScanDedupAfterArrival: clock.CurrentTime,
	})
	require.NoError(t, err)
	pr := resp.(*PurchaseCargoResponse)

	require.False(t, pr.CeilingAborted)
	require.Equal(t, 10, pr.UnitsAdded)
	require.Equal(t, 0, refresher.ceilingCalls, "the whole purchase fits in tranche 1, so the ceiling guard must spend zero live scans")
	require.Len(t, api.buys, 1)
}

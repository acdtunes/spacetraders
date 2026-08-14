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

// sp-7q61f: the buy-ceiling guard's half of the scan-dedup A/B test. Mirrors
// the trade-route package's own run_trade_route_coordinator_scandedup_test.go
// three categories at this call site (liveAskForCeiling), proving the reuse
// this bead adds never changes what the ceiling decides, only whether a
// request is spent, and only when the CALLER (the trade-route circuit) has set
// a bracket — every other cargo caller (contract delivery, manufacturing,
// refuel, the arb/tour/stocker legs) leaves it zero and is unaffected.

// dedupCeilingMarketRepo serves an ask whose LastUpdated is fully test-
// controlled, so a test can simulate a row proven fresh (>= the bracket floor)
// or not, independent of real wall-clock timing.
type dedupCeilingMarketRepo struct {
	scoutingQuery.MarketRepository
	ask       int
	updatedAt time.Time
	waypoint  string
	good      string
}

func (r *dedupCeilingMarketRepo) GetMarketData(_ context.Context, _ string, _ int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "WEAK"
	g, err := market.NewTradeGood(r.good, &supply, &activity, r.ask, r.ask-100, 100, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	updated := r.updatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	return market.NewMarket(r.waypoint, []market.TradeGood{*g}, updated)
}

// dedupCeilingRefresher counts every call, split by WHICH caller made it — the
// observable proxy for "an API request was spent". CargoTransactionHandler has
// TWO independent ScanAndSaveMarket call sites: the buy-ceiling guard under
// test here (always stamps shared.WithLiveScanRequired, sp-7q61f's dedup
// target) and the UNRELATED post-trade impact scan
// (cargo_transaction_market.go's refreshMarketData, fires after every
// successful purchase regardless of this bead). Splitting by the ctx stamp is
// what lets a test assert on the guard's OWN call count without being tripped
// by the other, pre-existing one. On success it overwrites the row with
// refreshAsk stamped at the clock's current instant — exactly what a real live
// GetMarket call would do.
type dedupCeilingRefresher struct {
	ceilingCalls int // calls stamped WithLiveScanRequired — the guard under test
	otherCalls   int // every other call (the pre-existing post-trade impact scan)
	refreshAsk   int
	repo         *dedupCeilingMarketRepo
	clock        *shared.MockClock
}

func (r *dedupCeilingRefresher) ScanAndSaveMarket(ctx context.Context, _ uint, _ string) error {
	if shared.LiveScanRequiredFromContext(ctx) {
		r.ceilingCalls++
	} else {
		r.otherCalls++
	}
	r.repo.ask = r.refreshAsk
	r.repo.updatedAt = r.clock.Now()
	return nil
}

// dedupCeilingAPI is a minimal purchase API fake: the ceiling verdict under
// test is decided entirely by liveAskForCeiling (dedupCeilingMarketRepo /
// dedupCeilingRefresher above), so the actual purchase execution price is
// deliberately decoupled and inert here.
type dedupCeilingAPI struct {
	domainPorts.APIClient
	buys []int
}

func (a *dedupCeilingAPI) PurchaseCargo(_ context.Context, _, _ string, units int, _ string) (*domainPorts.PurchaseResult, error) {
	a.buys = append(a.buys, units)
	return &domainPorts.PurchaseResult{TotalCost: units * 100, UnitsAdded: units}, nil
}

func newDedupCeilingHandler(t *testing.T, repo *dedupCeilingMarketRepo, refresher MarketRefresher, clock shared.Clock) (*PurchaseCargoHandler, *dedupCeilingAPI) {
	t.Helper()
	api := &dedupCeilingAPI{}
	shipRepo := &buyFakeShipRepo{ship: newDockedBuyer(t, 40, 0, navigation.NavStatusDocked)}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "tok")}
	h := NewPurchaseCargoHandler(shipRepo, playerRepo, api, repo, &buyRecordingMediator{}, refresher)
	h.delegate.SetClock(clock)
	return h, api
}

func runDedupCeilingBuy(t *testing.T, h *PurchaseCargoHandler, maxAsk int, beforeTravel, afterArrival time.Time) *PurchaseCargoResponse {
	t.Helper()
	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := h.Handle(ctx, &PurchaseCargoCommand{
		ShipSymbol: testBuyShip, GoodSymbol: optypeGood, Units: 10,
		PlayerID: shared.MustNewPlayerID(1), MaxAskPerUnit: maxAsk,
		ScanDedupBeforeTravel: beforeTravel, ScanDedupAfterArrival: afterArrival,
	})
	require.NoError(t, err)
	return resp.(*PurchaseCargoResponse)
}

// --- Category 1: the guard is NOT weakened -----------------------------------

// TestDedupCeiling_BracketSetButRowNotProvenFresh_StillDoesLiveScan proves the
// ceiling guard's protection survives even with a bracket set: when the cached
// row cannot prove it reflects THIS visit's own arrival (here: it predates the
// bracket floor), the guard falls through to its unconditional live scan. The
// stale cached ask (4000, within the 5000 ceiling) would wrongly let the buy
// through if dedup had incorrectly reused it; the fresh scan instead reveals
// 7000 (beyond the ceiling) and aborts — an incorrect dedup hit would flip this
// test to a false negative.
func TestDedupCeiling_BracketSetButRowNotProvenFresh_StillDoesLiveScan(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	repo := &dedupCeilingMarketRepo{ask: 4000, waypoint: testBuyWaypoint, good: optypeGood, updatedAt: clock.CurrentTime.Add(-30 * time.Second)}
	refresher := &dedupCeilingRefresher{repo: repo, refreshAsk: 7000, clock: clock}
	h, api := newDedupCeilingHandler(t, repo, refresher, clock)

	pr := runDedupCeilingBuy(t, h, 5000, clock.CurrentTime, clock.CurrentTime)

	require.True(t, pr.CeilingAborted, "an unprovable-fresh row must still spend a live scan and abort on the moved ask")
	require.Equal(t, 7000, pr.CeilingObservedAsk)
	require.GreaterOrEqual(t, refresher.ceilingCalls, 1, "the ceiling guard itself must have taken its own live scan")
	require.Empty(t, api.buys, "the ceiling must abort before any tranche reaches the API")
}

// TestDedupCeiling_NoBracket_UnchangedFromToday proves the default-off path is
// byte-identical: every caller but the trade-route circuit's allowlisted
// first-visit purchase leaves ScanDedupBeforeTravel/AfterArrival at their zero
// value, so the guard always takes the live scan — even though the row here
// satisfies every OTHER dedup condition (it is maximally fresh).
func TestDedupCeiling_NoBracket_UnchangedFromToday(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	repo := &dedupCeilingMarketRepo{ask: 4000, waypoint: testBuyWaypoint, good: optypeGood, updatedAt: clock.CurrentTime}
	refresher := &dedupCeilingRefresher{repo: repo, refreshAsk: 4200, clock: clock}
	h, api := newDedupCeilingHandler(t, repo, refresher, clock)

	pr := runDedupCeilingBuy(t, h, 5000, time.Time{}, time.Time{}) // zero bracket

	require.False(t, pr.CeilingAborted)
	require.GreaterOrEqual(t, refresher.ceilingCalls, 1, "a zero bracket must always take the live scan — no caller not explicitly opted in is ever affected")
	require.Equal(t, 10, pr.UnitsAdded)
	require.Len(t, api.buys, 1)
}

// --- Category 2: correct dedup -----------------------------------------------

// TestDedupCeiling_BracketArmedRowProvenFresh_ReusesArrivalScan is the true
// back-to-back same-visit case: a bracket whose row is proven fresh (LastUpdated
// at the bracket floor) must be reused — spending ZERO live scans for the
// ceiling check — while landing the SAME verdict a fresh scan would have.
func TestDedupCeiling_BracketArmedRowProvenFresh_ReusesArrivalScan(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	repo := &dedupCeilingMarketRepo{ask: 4000, waypoint: testBuyWaypoint, good: optypeGood, updatedAt: clock.CurrentTime}
	refresher := &dedupCeilingRefresher{repo: repo, refreshAsk: 9999, clock: clock} // would abort IF ever called
	h, api := newDedupCeilingHandler(t, repo, refresher, clock)

	pr := runDedupCeilingBuy(t, h, 5000, clock.CurrentTime, clock.CurrentTime)

	require.False(t, pr.CeilingAborted, "the deduped read (4000) clears the 5000 ceiling")
	require.Equal(t, 0, refresher.ceilingCalls, "an eligible reuse must spend ZERO live scans for the ceiling guard itself")
	require.Equal(t, 10, pr.UnitsAdded)
	require.Len(t, api.buys, 1)
}

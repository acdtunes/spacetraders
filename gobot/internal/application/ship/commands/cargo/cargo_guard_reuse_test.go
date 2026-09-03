package cargo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-htzl1.10: the per-tranche money guards re-read the live market before EVERY
// tranche — ~800 Get Market calls an hour at a fleet pinned to the API ceiling.
// The Admiral ruled (2026-09-03) that one visit's read may serve the next tranche
// of the same good at the same market when the previous tranche's REALISED price
// still clears the guard with headroom, verified again after that tranche executes.

func TestGuardReadReusable(t *testing.T) {
	const (
		headroom = 10
		maxAge   = 120 * time.Second
	)
	cases := []struct {
		name        string
		realised    int
		guard       int
		isBuy       bool
		headroomPct int
		elapsed     time.Duration
		complete    bool
		want        bool
	}{
		{"buy with headroom to spare", 1000, 1150, true, headroom, 0, true, true},
		{"buy whose headroom would breach the ceiling", 1000, 1050, true, headroom, 0, true, false},
		{"sell with headroom to spare", 1000, 850, false, headroom, 0, true, true},
		{"sell whose headroom would breach the floor", 1000, 950, false, headroom, 0, true, false},
		{"reuse disarmed", 1000, 1150, true, 0, 0, true, false},
		{"read older than maxAge", 1000, 1150, true, headroom, 121 * time.Second, true, false},
		{"previous tranche incomplete", 1000, 1150, true, headroom, 0, false, false},
		{"no realised price", 0, 1150, true, headroom, 0, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := guardReadReusable(tc.realised, tc.guard, tc.isBuy, tc.headroomPct, tc.elapsed, maxAge, tc.complete)
			require.Equal(t, tc.want, got)
		})
	}
}

// --- the loop: the real executeTransactions over counting fakes ----------------

// reuseFixture is a market whose TRUE price moves the instant a tranche fills.
// The cached row only ever catches up when a LIVE scan actually runs, so a
// reused read cannot see a move — exactly like reality.
type reuseFixture struct {
	firstPrice int // what tranche 1 realises
	laterPrice int // what every tranche after it realises
	limit      int
	fills      int
	cached     int
	perFill    time.Duration // clock the fills burn, for the maxAge case
	clock      *shared.MockClock
}

func (f *reuseFixture) truePrice() int {
	if f.fills == 0 {
		return f.firstPrice
	}
	return f.laterPrice
}

type reuseMarketRepo struct {
	scoutingQuery.MarketRepository
	fix    *reuseFixture
	export bool // a buy SOURCE; false is a sell SINK
}

func (r *reuseMarketRepo) GetMarketData(_ context.Context, _ string, _ int) (*market.Market, error) {
	supply, activity := "MODERATE", "STRONG"
	ask, bid, tradeType := r.fix.cached, r.fix.cached-100, market.TradeTypeExport
	if !r.export {
		ask, bid, tradeType = r.fix.cached+100, r.fix.cached, market.TradeTypeImport
	}
	g, err := market.NewTradeGood(optypeGood, &supply, &activity, ask, bid, r.fix.limit, tradeType)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(testBuyWaypoint, []market.TradeGood{*g}, r.fix.clock.Now())
}

// reuseRefresher counts the guard's OWN live reads (the WithLiveScanRequired
// stamp) apart from the unrelated post-trade impact scan.
type reuseRefresher struct {
	fix        *reuseFixture
	guardCalls int
	otherCalls int
}

func (r *reuseRefresher) ScanAndSaveMarket(ctx context.Context, _ uint, _ string) error {
	if shared.LiveScanRequiredFromContext(ctx) {
		r.guardCalls++
	} else {
		r.otherCalls++
	}
	r.fix.cached = r.fix.truePrice()
	return nil
}

// reuseAPI realises each tranche at the market's TRUE price and burns the
// fixture's per-fill time, so a chain of reuses ages exactly as it would live.
// liveLimit/shortFillFirst/serverUnits inject the three ways a tranche ends up
// INCOMPLETE — a 4604 clamp, a short fill, a 4219 hold reconcile.
type reuseAPI struct {
	domainPorts.APIClient
	fix            *reuseFixture
	buys           []int
	sells          []int
	liveLimit      int  // >0: a larger tranche is rejected 4604 stating this limit
	shortFillFirst int  // >0: the first fill moves this many fewer units, no error
	serverUnits    int  // >0: the hold the server really has, rejecting 4219 above it
	shorted        bool // the short fill has been spent
}

func (a *reuseAPI) fill(units int) int {
	a.fix.fills++
	a.fix.clock.Advance(a.fix.perFill)
	if a.shortFillFirst > 0 && !a.shorted {
		a.shorted = true
		return units - a.shortFillFirst
	}
	return units
}

func (a *reuseAPI) PurchaseCargo(_ context.Context, _, good string, units int, _ string) (*domainPorts.PurchaseResult, error) {
	a.buys = append(a.buys, units)
	if a.liveLimit > 0 && units > a.liveLimit {
		return nil, marketVolume4604("purchase", testBuyWaypoint, good, units, a.liveLimit)
	}
	price := a.fix.truePrice()
	moved := a.fill(units)
	return &domainPorts.PurchaseResult{TotalCost: moved * price, UnitsAdded: moved}, nil
}

func (a *reuseAPI) SellCargo(_ context.Context, ship, good string, units int, _ string) (*domainPorts.SellResult, error) {
	a.sells = append(a.sells, units)
	if a.liveLimit > 0 && units > a.liveLimit {
		return nil, marketVolume4604("sell", testBuyWaypoint, good, units, a.liveLimit)
	}
	if a.serverUnits > 0 && units > a.serverUnits {
		return nil, cargoShortfall4219(ship, good, units, a.serverUnits)
	}
	price := a.fix.truePrice()
	moved := a.fill(units)
	if a.serverUnits > 0 {
		a.serverUnits -= moved
	}
	return &domainPorts.SellResult{TotalRevenue: moved * price, UnitsSold: moved}, nil
}

// reuseCapturingLogger records the guard's own WARNING/DEBUG rows so a test can
// pin that a breach is REPORTED even where it raises no abort flag.
type reuseCapturingLogger struct {
	rows []map[string]interface{}
}

func (l *reuseCapturingLogger) Log(_, _ string, metadata map[string]interface{}) {
	if metadata != nil {
		l.rows = append(l.rows, metadata)
	}
}

func (l *reuseCapturingLogger) rowsWithAction(action string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, row := range l.rows {
		if row["action"] == action {
			out = append(out, row)
		}
	}
	return out
}

// reuseCtx is the token-bearing context, with a capturing logger when one is given.
func reuseCtx(logger *reuseCapturingLogger) context.Context {
	ctx := auth.WithPlayerToken(context.Background(), "tok")
	if logger != nil {
		ctx = logging.WithLogger(ctx, logger)
	}
	return ctx
}

func runReuseBuyWith(t *testing.T, fix *reuseFixture, api *reuseAPI, logger *reuseCapturingLogger, maxAsk, headroomPct int, maxAge time.Duration) (*PurchaseCargoResponse, *reuseRefresher) {
	t.Helper()
	refresher := &reuseRefresher{fix: fix}
	h := NewPurchaseCargoHandler(
		&buyFakeShipRepo{ship: newDockedBuyer(t, 40, 0, navigation.NavStatusDocked)},
		&buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "tok")},
		api, &reuseMarketRepo{fix: fix, export: true}, &buyRecordingMediator{}, refresher)
	h.delegate.SetClock(fix.clock)
	h.SetGuardReuse(headroomPct, maxAge)

	resp, err := h.Handle(reuseCtx(logger), &PurchaseCargoCommand{
		ShipSymbol: testBuyShip, GoodSymbol: optypeGood, Units: 40,
		PlayerID: shared.MustNewPlayerID(1), MaxAskPerUnit: maxAsk,
	})
	require.NoError(t, err)
	return resp.(*PurchaseCargoResponse), refresher
}

func runReuseSellWith(t *testing.T, fix *reuseFixture, api *reuseAPI, logger *reuseCapturingLogger, minBid, headroomPct int, maxAge time.Duration) (*SellCargoResponse, *reuseRefresher) {
	t.Helper()
	refresher := &reuseRefresher{fix: fix}
	h := NewSellCargoHandler(
		&buyFakeShipRepo{ship: newDockedShipWithCargo(t, 1, optypeGood, 40)},
		&buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "tok")},
		api, &reuseMarketRepo{fix: fix}, &buyRecordingMediator{}, refresher)
	h.delegate.SetClock(fix.clock)
	h.SetGuardReuse(headroomPct, maxAge)

	resp, err := h.Handle(reuseCtx(logger), &SellCargoCommand{
		ShipSymbol: "OPTYPE-1", GoodSymbol: optypeGood, Units: 40,
		PlayerID: shared.MustNewPlayerID(1), MinBidPerUnit: minBid,
	})
	require.NoError(t, err)
	return resp.(*SellCargoResponse), refresher
}

func runReuseBuy(t *testing.T, fix *reuseFixture, maxAsk, headroomPct int, maxAge time.Duration) (*PurchaseCargoResponse, *reuseAPI, *reuseRefresher) {
	t.Helper()
	api := &reuseAPI{fix: fix}
	resp, refresher := runReuseBuyWith(t, fix, api, nil, maxAsk, headroomPct, maxAge)
	return resp, api, refresher
}

func runReuseSell(t *testing.T, fix *reuseFixture, minBid, headroomPct int, maxAge time.Duration) (*SellCargoResponse, *reuseAPI, *reuseRefresher) {
	t.Helper()
	api := &reuseAPI{fix: fix}
	resp, refresher := runReuseSellWith(t, fix, api, nil, minBid, headroomPct, maxAge)
	return resp, api, refresher
}

func newReuseFixture(first, later, limit int, perFill time.Duration) *reuseFixture {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	return &reuseFixture{firstPrice: first, laterPrice: later, limit: limit, cached: first, perFill: perFill, clock: clock}
}

// THE SAVING: an ask that holds buys all three tranches on ONE live read, where
// today's guard spends three.
func TestPurchaseCargo_GuardReuse_AskHolds_OneLiveReadForThreeTranches(t *testing.T) {
	fix := newReuseFixture(4000, 4000, 15, 0)

	pr, api, refresher := runReuseBuy(t, fix, 5000, DefaultGuardReuseHeadroomPct, DefaultGuardReuseMaxAge)

	require.False(t, pr.CeilingAborted)
	require.Equal(t, 40, pr.UnitsAdded, "an ask that holds must still buy the whole order")
	require.Len(t, api.buys, 3)
	require.Equal(t, 1, refresher.guardCalls, "tranches 2 and 3 must ride tranche 1's read (3 live reads before this bead)")
}

// THE FAIL-CLOSED HALF: tranche 2 dispatches on the reused read and realises
// above the ceiling, so the remainder is abandoned and the breach is what the
// abort reports. The exposure is the one tranche the ruling accepts.
func TestPurchaseCargo_GuardReuse_TrancheRealisesAboveCeiling_AbortsRemainder(t *testing.T) {
	fix := newReuseFixture(4000, 6000, 15, 0)

	pr, api, refresher := runReuseBuy(t, fix, 5000, DefaultGuardReuseHeadroomPct, DefaultGuardReuseMaxAge)

	require.True(t, pr.CeilingAborted, "a reused read whose tranche realises above the ceiling must abort the rest")
	require.Equal(t, 6000, pr.CeilingObservedAsk, "the abort reports the realised price that broke the guard")
	require.Equal(t, 30, pr.UnitsAdded)
	require.Len(t, api.buys, 2, "tranche 3 must never be requested")
	require.Equal(t, 1, refresher.guardCalls)
}

// The sell mirror of the saving.
func TestSellCargo_GuardReuse_BidHolds_OneLiveReadForThreeTranches(t *testing.T) {
	fix := newReuseFixture(20000, 20000, 15, 0)

	sr, api, refresher := runReuseSell(t, fix, 16000, DefaultGuardReuseHeadroomPct, DefaultGuardReuseMaxAge)

	require.False(t, sr.FloorAborted)
	require.Equal(t, 40, sr.UnitsSold)
	require.Len(t, api.sells, 3)
	require.Equal(t, 1, refresher.guardCalls, "tranches 2 and 3 must ride tranche 1's read (3 live reads before this bead)")
}

// The sell mirror of the breach: a crushed tranche-2 bid holds the remainder aboard.
func TestSellCargo_GuardReuse_TrancheRealisesBelowFloor_HoldsRemainder(t *testing.T) {
	fix := newReuseFixture(20000, 9000, 15, 0)

	sr, api, refresher := runReuseSell(t, fix, 16000, DefaultGuardReuseHeadroomPct, DefaultGuardReuseMaxAge)

	require.True(t, sr.FloorAborted)
	require.Equal(t, 9000, sr.FloorObservedBid)
	require.Equal(t, 30, sr.UnitsSold)
	require.Len(t, api.sells, 2, "tranche 3 must never be requested")
	require.Equal(t, 1, refresher.guardCalls)
}

// Headroom 0 disarms the reuse: every tranche takes its own live read, exactly
// as the guard behaved before this bead.
func TestPurchaseCargo_GuardReuse_HeadroomZero_IsALiveReadPerTranche(t *testing.T) {
	fix := newReuseFixture(4000, 4000, 15, 0)

	pr, api, refresher := runReuseBuy(t, fix, 5000, 0, DefaultGuardReuseMaxAge)

	require.False(t, pr.CeilingAborted)
	require.Equal(t, 40, pr.UnitsAdded)
	require.Len(t, api.buys, 3)
	require.Equal(t, 3, refresher.guardCalls, "a disarmed reuse must be byte-identical to the pre-bead guard")
}

// A price within the headroom is not enough on its own: a read past maxAge is
// stale evidence, so the guard buys a fresh one.
func TestPurchaseCargo_GuardReuse_ReadPastMaxAge_TakesALiveRead(t *testing.T) {
	fix := newReuseFixture(4000, 4000, 15, 61*time.Second)

	pr, api, refresher := runReuseBuy(t, fix, 5000, DefaultGuardReuseHeadroomPct, 60*time.Second)

	require.False(t, pr.CeilingAborted)
	require.Equal(t, 40, pr.UnitsAdded)
	require.Len(t, api.buys, 3)
	require.Equal(t, 3, refresher.guardCalls, "each tranche's read has aged past maxAge before the next one checks")
}

// --- a breach on the LAST tranche withholds nothing ----------------------------
//
// The abort flags mean "units were held back". The tour spends its ONE sell-floor
// refusal per good on reading FloorAborted, so a flag raised with nothing withheld
// would disarm the H50 floor for every later sell of that good having saved nothing.

func TestSellCargo_GuardReuse_BreachOnTheLastTranche_DoesNotRaiseTheAbortFlag(t *testing.T) {
	fix := newReuseFixture(20000, 9000, 20, 0) // 40 units at 20/tranche: tranche 2 is the last
	logger := &reuseCapturingLogger{}
	api := &reuseAPI{fix: fix}

	sr, refresher := runReuseSellWith(t, fix, api, logger, 16000, DefaultGuardReuseHeadroomPct, DefaultGuardReuseMaxAge)

	require.False(t, sr.FloorAborted, "a breach that withheld nothing must not burn the tour's one refusal for this good")
	require.Zero(t, sr.FloorObservedBid)
	require.Equal(t, 40, sr.UnitsSold, "both tranches sold; there was never a remainder to hold aboard")
	require.Len(t, api.sells, 2)
	require.Equal(t, 1, refresher.guardCalls)
	require.Len(t, logger.rowsWithAction("guard_reuse_breach"), 1, "the breach is still REPORTED, it just raises no abort")
}

func TestPurchaseCargo_GuardReuse_BreachOnTheLastTranche_DoesNotRaiseTheAbortFlag(t *testing.T) {
	fix := newReuseFixture(4000, 6000, 20, 0)
	logger := &reuseCapturingLogger{}
	api := &reuseAPI{fix: fix}

	pr, refresher := runReuseBuyWith(t, fix, api, logger, 5000, DefaultGuardReuseHeadroomPct, DefaultGuardReuseMaxAge)

	require.False(t, pr.CeilingAborted, "a breach that left nothing unbought must not read as a margin abort")
	require.Zero(t, pr.CeilingObservedAsk)
	require.Equal(t, 40, pr.UnitsAdded)
	require.Len(t, api.buys, 2)
	require.Equal(t, 1, refresher.guardCalls)
	require.Len(t, logger.rowsWithAction("guard_reuse_breach"), 1)
}

// --- an INCOMPLETE tranche never licenses the next one's reuse -----------------
//
// Each of these fails if `trancheComplete && result.UnitsProcessed == unitsToProcess`
// is weakened to `true`: the tranche after the reconcile would ride a read the
// reconcile itself proved nothing about.

func TestPurchaseCargo_GuardReuse_ClampedTranche_ForcesALiveReadOnTheNext(t *testing.T) {
	fix := newReuseFixture(4000, 4000, 20, 0) // the CACHED depth: 20/tranche
	api := &reuseAPI{fix: fix, liveLimit: 15} // the market really takes 15

	pr, refresher := runReuseBuyWith(t, fix, api, nil, 5000, DefaultGuardReuseHeadroomPct, DefaultGuardReuseMaxAge)

	require.False(t, pr.CeilingAborted)
	require.Equal(t, 40, pr.UnitsAdded)
	require.Equal(t, []int{20, 15, 15, 10}, api.buys, "the rejected 20, its clamped retry, then the tail at the corrected depth")
	require.Equal(t, 2, refresher.guardCalls, "the clamped tranche 1 licenses nothing, so tranche 2 re-reads live; tranche 3 may then reuse")
}

func TestSellCargo_GuardReuse_ClampedTranche_ForcesALiveReadOnTheNext(t *testing.T) {
	fix := newReuseFixture(20000, 20000, 20, 0)
	api := &reuseAPI{fix: fix, liveLimit: 15}

	sr, refresher := runReuseSellWith(t, fix, api, nil, 16000, DefaultGuardReuseHeadroomPct, DefaultGuardReuseMaxAge)

	require.False(t, sr.FloorAborted)
	require.Equal(t, 40, sr.UnitsSold)
	require.Equal(t, []int{20, 15, 15, 10}, api.sells)
	require.Equal(t, 2, refresher.guardCalls, "the clamped tranche 1 licenses nothing, so tranche 2 re-reads live; tranche 3 may then reuse")
}

// A SHORT FILL needs no error at all: the API simply moves fewer units than the
// tranche asked for, which is the other half of the completeness test.
func TestPurchaseCargo_GuardReuse_ShortFilledTranche_ForcesALiveReadOnTheNext(t *testing.T) {
	fix := newReuseFixture(4000, 4000, 15, 0)
	api := &reuseAPI{fix: fix, shortFillFirst: 5}

	pr, refresher := runReuseBuyWith(t, fix, api, nil, 5000, DefaultGuardReuseHeadroomPct, DefaultGuardReuseMaxAge)

	require.False(t, pr.CeilingAborted)
	require.Equal(t, 35, pr.UnitsAdded, "tranche 1 moved 5 units fewer than it asked for")
	require.Equal(t, []int{15, 15, 10}, api.buys)
	require.Equal(t, 2, refresher.guardCalls, "a short-filled tranche 1 licenses nothing, so tranche 2 re-reads live")
}

// The 4219 mirror: a hold reconcile closes the transaction where it lands, so no
// later tranche may ride the reused read — and a breach on it is still reported
// while the reconcile's own outcome stands unchanged.
func TestSellCargo_GuardReuse_ShortfallReconciledTranche_EndsTheSaleAndReportsTheBreach(t *testing.T) {
	fix := newReuseFixture(20000, 9000, 15, 0)
	logger := &reuseCapturingLogger{}
	api := &reuseAPI{fix: fix, serverUnits: 25} // the hull really holds 25, not 40

	sr, refresher := runReuseSellWith(t, fix, api, logger, 16000, DefaultGuardReuseHeadroomPct, DefaultGuardReuseMaxAge)

	require.False(t, sr.FloorAborted, "the reconcile closes the sale; the breach must not also raise an abort over an empty hold")
	require.Equal(t, 25, sr.UnitsSold, "everything the server said was aboard")
	require.Equal(t, []int{15, 15, 10}, api.sells, "tranche 2's rejection, its clamped retry, and then nothing more")
	require.Equal(t, 1, refresher.guardCalls, "only tranche 1 read live; the reconciled tranche 2 ends the loop")
	breaches := logger.rowsWithAction("guard_reuse_breach")
	require.Len(t, breaches, 1)
	require.Equal(t, true, breaches[0]["shortfall"], "the breach row must say the reconcile is what closed the transaction")
}

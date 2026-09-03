package commands

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// tourCeilingShipRepo adds the CAS-retry save path persistCargoDelta calls after a
// successful purchase; trFakeShipRepo's embedded nil ShipRepository panics on it.
// Every return value is discarded by the caller, so a no-op is faithful.
type tourCeilingShipRepo struct {
	*trFakeShipRepo
}

func (r *tourCeilingShipRepo) SaveWithRetry(_ context.Context, _ string, _ shared.PlayerID, _ navigation.ShipMutation) (*navigation.Ship, bool, error) {
	return nil, false, nil
}

// Proves the SHARED scan-dedup machinery empirically protects a purchase driven the
// way tour drives one: h.legs.purchaseWithCeiling directly, via the same
// startScanDedupBracket/confirmScanDedupArrival pair buyLookbackItem/executeBuy use,
// with a REAL cargo.PurchaseCargoHandler behind h.legs' mediator so
// liveAskForCeiling/reuseScanDedup/DedupEligible run for real. Complements
// run_tour_coordinator_scandedup_test.go (proves tour's own code threads a real
// bracket) with proof the shared guard honors it once threaded.

// tourCeilingFixture models a purchase whose TRUE ask ladders the instant a tranche
// buys (independent of whether anything has re-scanned it yet) — the D39 shape.
type tourCeilingFixture struct {
	healthyAsk int
	laddedAsk  int
	limit      int // the market's per-transaction tranche limit
	buysDone   int
	cachedAsk  int
	updatedAt  time.Time
}

func (f *tourCeilingFixture) trueAsk() int {
	if f.buysDone == 0 {
		return f.healthyAsk
	}
	return f.laddedAsk
}

const (
	tourCeilingWaypoint = "X1-CEIL-A"
	tourCeilingGood     = "PARTS"
	tourCeilingShip     = "TOUR-CEIL-1"
)

// tourCeilingMarketRepo satisfies both h.legs' and the cargo handler's market-repo
// ports with one fake; embedding both anonymously would collide on the field name, so
// only market.MarketRepository is embedded and the other's two extra methods are
// stubbed explicitly.
type tourCeilingMarketRepo struct {
	market.MarketRepository
	fix *tourCeilingFixture
}

func (r *tourCeilingMarketRepo) UpsertMarketData(_ context.Context, _ uint, _ string, _ []market.TradeGood, _ time.Time) error {
	return nil
}

func (r *tourCeilingMarketRepo) ListMarketsInSystem(_ context.Context, _ uint, _ string, _ int) ([]market.Market, error) {
	return nil, nil
}

func (r *tourCeilingMarketRepo) GetMarketData(_ context.Context, _ string, _ int) (*market.Market, error) {
	supply, activity := "MODERATE", "WEAK"
	g, err := market.NewTradeGood(tourCeilingGood, &supply, &activity, r.fix.cachedAsk, r.fix.cachedAsk-100, r.fix.limit, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	updated := r.fix.updatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	return market.NewMarket(tourCeilingWaypoint, []market.TradeGood{*g}, updated)
}

// tourCeilingRefresher is the live-scan port: a call discovers the TRUE (possibly
// laddered) ask. ceilingCalls counts only the buy-ceiling guard's own calls, split
// from the unrelated post-trade impact scan.
type tourCeilingRefresher struct {
	fix          *tourCeilingFixture
	clock        *shared.MockClock
	ceilingCalls int
	otherCalls   int
}

func (r *tourCeilingRefresher) ScanAndSaveMarket(ctx context.Context, _ uint, _ string) error {
	if shared.LiveScanRequiredFromContext(ctx) {
		r.ceilingCalls++
	} else {
		r.otherCalls++
	}
	r.fix.cachedAsk = r.fix.trueAsk()
	r.fix.updatedAt = r.clock.Now()
	return nil
}

type tourCeilingAPI struct {
	domainPorts.APIClient
	fix  *tourCeilingFixture
	buys []int
}

func (a *tourCeilingAPI) PurchaseCargo(_ context.Context, _, _ string, units int, _ string) (*domainPorts.PurchaseResult, error) {
	a.buys = append(a.buys, units)
	cost := units * a.fix.trueAsk() // realised at the TRUE ask, laddered or not
	a.fix.buysDone++
	return &domainPorts.PurchaseResult{TotalCost: cost, UnitsAdded: units}, nil
}

type tourCeilingPlayerRepo struct {
	player.PlayerRepository
	p *player.Player
}

func (r *tourCeilingPlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return r.p, nil
}

// innerNoopMediator satisfies CargoTransactionHandler's own mediator dependency,
// unused by this suite's direct-purchase path.
type innerNoopMediator struct{}

func (m *innerNoopMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	return nil, nil
}
func (m *innerNoopMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }
func (m *innerNoopMediator) RegisterMiddleware(_ common.Middleware)                 {}

// tourCeilingRoutingMediator is h.legs' own mediator: it routes a dispatched
// PurchaseCargoCommand to a REAL cargo.PurchaseCargoHandler, as production does.
type tourCeilingRoutingMediator struct {
	purchase *shipCargo.PurchaseCargoHandler
}

func (m *tourCeilingRoutingMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	if cmd, ok := request.(*shipCargo.PurchaseCargoCommand); ok {
		return m.purchase.Handle(ctx, cmd)
	}
	return nil, nil
}
func (m *tourCeilingRoutingMediator) Register(_ reflect.Type, _ common.RequestHandler) error {
	return nil
}
func (m *tourCeilingRoutingMediator) RegisterMiddleware(_ common.Middleware) {}

// newTourCeilingLegs builds h.legs (the exact type tour's own code holds and calls)
// with a real cargo-transaction chain behind its mediator.
func newTourCeilingLegs(t *testing.T, fix *tourCeilingFixture, refresher *tourCeilingRefresher, allowlist ScanDedupAllowlist) (*RunTradeRouteCoordinatorHandler, *tourCeilingAPI) {
	t.Helper()
	repo := &tourCeilingMarketRepo{fix: fix}
	api := &tourCeilingAPI{fix: fix}
	shipRepo := &tourCeilingShipRepo{trFakeShipRepo: &trFakeShipRepo{ship: newDiscHauler(t, tourCeilingShip, tourCeilingWaypoint)}}
	playerRepo := &tourCeilingPlayerRepo{p: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "tok")}
	purchaseHandler := shipCargo.NewPurchaseCargoHandler(shipRepo, playerRepo, api, repo, &innerNoopMediator{}, refresher)

	legs := NewRunTradeRouteCoordinatorHandler(&tourCeilingRoutingMediator{purchase: purchaseHandler}, shipRepo, repo, refresher, refresher.clock, nil)
	legs.SetScanDedupAllowlist(allowlist)
	return legs, api
}

// buyThroughTourLegs captures the bracket and buys exactly as buyLookbackItem /
// executePlan do; travel/dock timing itself is proven in the Layer A test file.
func buyThroughTourLegs(ctx context.Context, legs *RunTradeRouteCoordinatorHandler, units, maxAsk int) (*shipCargo.PurchaseCargoResponse, error) {
	dedup := legs.startScanDedupBracket(ctx, tourCeilingShip, 1)
	dedup = legs.confirmScanDedupArrival(dedup)
	return legs.purchaseWithCeiling(ctx, tourCeilingShip, tourCeilingGood, units, 1, maxAsk, dedup)
}

// --- Category 1: the guard is NOT weakened -----------------------------------

// An armed ship whose cached row cannot prove it reflects THIS call's own arrival
// (stale, predating when the bracket opened) must still fall through to a live scan —
// dedup never overrides eligibility.
func TestTourCeilingLegs_ArmedButRowNotProvenFresh_StillDoesLiveScan(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	fix := &tourCeilingFixture{healthyAsk: 4000, laddedAsk: 4000, limit: 100, cachedAsk: 4000, updatedAt: clock.CurrentTime.Add(-30 * time.Second)}
	refresher := &tourCeilingRefresher{fix: fix, clock: clock}
	allowlist := &inMemoryScanDedupAllowlist{armed: map[string]bool{tourCeilingShip: true}}
	legs, api := newTourCeilingLegs(t, fix, refresher, allowlist)

	// The refresher's live scan reveals a DIFFERENT (higher) ask than the stale cache —
	// a real GetMarket call landing between "before travel" and now.
	fix.healthyAsk, fix.laddedAsk = 7000, 7000

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := buyThroughTourLegs(ctx, legs, 10, 5000)
	if err != nil {
		t.Fatalf("purchaseWithCeiling error: %v", err)
	}

	if !resp.CeilingAborted {
		t.Fatalf("an unprovable-fresh row must still spend a live scan and abort on the moved ask, got %+v", resp)
	}
	if refresher.ceilingCalls < 1 {
		t.Fatalf("the ceiling guard itself must have taken its own live scan, got %d", refresher.ceilingCalls)
	}
	if len(api.buys) != 0 {
		t.Fatalf("the ceiling must abort before any tranche reaches the API, got %d buys", api.buys)
	}
}

// A ship absent from the allowlist always takes the live scan, even on a maximally
// fresh row — only allowlist membership may change the outcome.
func TestTourCeilingLegs_NotArmed_UnchangedFromToday(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	fix := &tourCeilingFixture{healthyAsk: 4000, laddedAsk: 4000, limit: 100, cachedAsk: 4000, updatedAt: clock.CurrentTime}
	refresher := &tourCeilingRefresher{fix: fix, clock: clock}
	allowlist := &inMemoryScanDedupAllowlist{armed: map[string]bool{}} // explicitly empty
	legs, api := newTourCeilingLegs(t, fix, refresher, allowlist)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := buyThroughTourLegs(ctx, legs, 10, 5000)
	if err != nil {
		t.Fatalf("purchaseWithCeiling error: %v", err)
	}

	if resp.CeilingAborted {
		t.Fatalf("a within-tolerance ask must not abort, got %+v", resp)
	}
	if refresher.ceilingCalls < 1 {
		t.Fatalf("an unarmed ship must always take the live scan for the ceiling guard, got %d", refresher.ceilingCalls)
	}
	if len(api.buys) != 1 {
		t.Fatalf("a within-tolerance basis must actually buy, got %d buys", api.buys)
	}
}

// --- Category 2: correct dedup -----------------------------------------------

// The true back-to-back case: an armed ship whose row is proven fresh (updated at the
// bracket floor) must reuse it — spending ZERO live scans for the ceiling guard —
// while landing the same verdict a fresh scan would have.
func TestTourCeilingLegs_ArmedRowProvenFresh_ReusesArrivalScan(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	fix := &tourCeilingFixture{healthyAsk: 4000, laddedAsk: 4000, limit: 100, cachedAsk: 4000, updatedAt: clock.CurrentTime}
	refresher := &tourCeilingRefresher{fix: fix, clock: clock}
	allowlist := &inMemoryScanDedupAllowlist{armed: map[string]bool{tourCeilingShip: true}}
	legs, api := newTourCeilingLegs(t, fix, refresher, allowlist)

	// If the guard were ever forced live, this would abort — proving a false PASS
	// cannot come from an accidental live scan.
	fix.healthyAsk, fix.laddedAsk = 9999, 9999

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := buyThroughTourLegs(ctx, legs, 10, 5000)
	if err != nil {
		t.Fatalf("purchaseWithCeiling error: %v", err)
	}

	if resp.CeilingAborted {
		t.Fatalf("the deduped read (4000) clears the 5000 ceiling, got %+v", resp)
	}
	if refresher.ceilingCalls != 0 {
		t.Fatalf("an eligible reuse must spend ZERO live scans for the ceiling guard, got %d", refresher.ceilingCalls)
	}
	if len(api.buys) != 1 || api.buys[0] != 10 {
		t.Fatalf("expected one 10-unit buy, got %v", api.buys)
	}
}

// The empirical multi-tranche proof: the true ask ladders on tranche 1's own fill and
// the ceiling must still catch it, driven through h.legs with a bracket captured the
// way tour's own call sites capture it. Since sp-htzl1.10 tranche 2 rides tranche 1's
// own verified read and its REALISED 7,000 is what trips the abort — the one tranche of
// exposure the Admiral's 2026-09-03 ruling accepts.
func TestTourCeilingLegs_MultiTranchePurchase_BracketMustNotSurviveTranche1_LadderStillCaught(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	fix := &tourCeilingFixture{healthyAsk: 4000, laddedAsk: 7000, limit: 15, cachedAsk: 4000, updatedAt: clock.CurrentTime}
	refresher := &tourCeilingRefresher{fix: fix, clock: clock}
	allowlist := &inMemoryScanDedupAllowlist{armed: map[string]bool{tourCeilingShip: true}}
	legs, api := newTourCeilingLegs(t, fix, refresher, allowlist)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := buyThroughTourLegs(ctx, legs, 40, 5000) // 40 units, 15/tranche → 3 tranches

	if err != nil {
		t.Fatalf("purchaseWithCeiling error: %v", err)
	}
	if !resp.CeilingAborted {
		t.Fatalf("the ladder must still be caught even though a dedup bracket was armed for the call, got %+v", resp)
	}
	if resp.CeilingObservedAsk != 7000 {
		t.Fatalf("expected the ceiling abort to observe the laddered ask 7000, got %d", resp.CeilingObservedAsk)
	}
	if resp.UnitsAdded != 30 {
		t.Fatalf("the ladder must stop the buy at tranche 2, leaving tranche 3 unbought, got %d", resp.UnitsAdded)
	}
	if refresher.ceilingCalls != 0 {
		t.Fatalf("tranche 1 dedups and tranche 2 rides its read; neither spends a scan, got %d", refresher.ceilingCalls)
	}
	if len(api.buys) != 2 {
		t.Fatalf("expected exactly 2 executed tranches (the breach stops tranche 3), got %d: %v", len(api.buys), api.buys)
	}
}

// Tranche-1 dedup is the ticket's actual point and must keep working: a purchase that
// completes in a single tranche may still spend zero live scans for the ceiling guard.
func TestTourCeilingLegs_SingleTranchePurchase_StillDedupsTranche1(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	fix := &tourCeilingFixture{healthyAsk: 4000, laddedAsk: 7000, limit: 15, cachedAsk: 4000, updatedAt: clock.CurrentTime}
	refresher := &tourCeilingRefresher{fix: fix, clock: clock}
	allowlist := &inMemoryScanDedupAllowlist{armed: map[string]bool{tourCeilingShip: true}}
	legs, api := newTourCeilingLegs(t, fix, refresher, allowlist)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := buyThroughTourLegs(ctx, legs, 10, 5000) // within the 15-unit tranche limit: one tranche

	if err != nil {
		t.Fatalf("purchaseWithCeiling error: %v", err)
	}
	if resp.CeilingAborted {
		t.Fatalf("unexpected abort, got %+v", resp)
	}
	if resp.UnitsAdded != 10 {
		t.Fatalf("expected 10 units added, got %d", resp.UnitsAdded)
	}
	if refresher.ceilingCalls != 0 {
		t.Fatalf("the whole purchase fits in tranche 1, so the ceiling guard must spend zero live scans, got %d", refresher.ceilingCalls)
	}
	if len(api.buys) != 1 {
		t.Fatalf("expected 1 buy, got %d", len(api.buys))
	}
}

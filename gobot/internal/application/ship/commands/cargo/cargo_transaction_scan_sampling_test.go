package cargo

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-v34b behavior 2: SAMPLE the deliberate post-trade impact scan. The trade
// executor re-scans a market right after a buy/sell (refreshMarketData →
// ScanAndSaveMarket) to record how the price MOVED — the paired before/after that
// fed the sp-tl68 impact model. That instrumentation is now the top API consumer, so
// it fires on only a config FRACTION of trades (ImpactSampleRate); a non-sampled trade
// falls back to the recent-scan freshness gate (one fresh scan for the decision, no
// extra measurement scan). These drive the REAL CargoTransactionHandler tranche loop
// through the buy driving port and assert on the OBSERVABLE outcome: whether the
// post-trade scan actually hit the market refresher. No ceiling is armed, so the ONLY
// refresher call a trade can make is the post-trade refreshMarketData — the exact scan
// this bead throttles.

// countingRefresher is the driven-port spy at the scan boundary: it records how many
// times the post-trade scan actually fired, which is the whole observable of the
// sampling + freshness gate.
type countingRefresher struct{ scans int }

func (r *countingRefresher) ScanAndSaveMarket(_ context.Context, _ uint, _ string) error {
	r.scans++
	return nil
}

// samplingFakeMarketRepo serves the cached market the freshness gate reads to decide
// whether a non-sampled trade may reuse the cache. lastUpdated drives the age: a zero
// value returns nil (never scanned → the trade must scan), a recent value is FRESH
// (reuse), an old value is STALE (must re-scan). The good carries a large trade volume
// so GetTransactionLimit yields a single tranche — the simplest shape for the gate.
type samplingFakeMarketRepo struct {
	scoutingQuery.MarketRepository
	waypoint    string
	good        string
	lastUpdated time.Time
}

func (r *samplingFakeMarketRepo) GetMarketData(_ context.Context, _ string, _ int) (*market.Market, error) {
	if r.lastUpdated.IsZero() {
		return nil, nil
	}
	supply, activity := "MODERATE", "WEAK"
	g, err := market.NewTradeGood(r.good, &supply, &activity, 100, 200, 1000, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(r.waypoint, []market.TradeGood{*g}, r.lastUpdated)
}

func newSamplingBuyHandler(t *testing.T, repo *samplingFakeMarketRepo, refresher MarketRefresher) *PurchaseCargoHandler {
	t.Helper()
	api := &buyFakeAPIClient{result: &domainPorts.PurchaseResult{TotalCost: 4000, UnitsAdded: 40}}
	shipRepo := &buyFakeShipRepo{ship: newDockedBuyer(t, 40, 0, navigation.NavStatusDocked)}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "tok")}
	return NewPurchaseCargoHandler(shipRepo, playerRepo, api, repo, &buyRecordingMediator{}, refresher)
}

func runSamplingBuy(t *testing.T, h *PurchaseCargoHandler, ctx context.Context) {
	t.Helper()
	resp, err := h.Handle(ctx, &PurchaseCargoCommand{
		ShipSymbol: testBuyShip, GoodSymbol: optypeGood, Units: 40,
		PlayerID: shared.MustNewPlayerID(1),
	})
	require.NoError(t, err)
	pr := resp.(*PurchaseCargoResponse)
	require.Equal(t, 40, pr.UnitsAdded, "the buy itself must complete regardless of the scan-sampling decision")
}

// THE RED case: a non-sampled trade at a market scanned <N ago must REUSE the cache —
// no redundant post-trade GetMarket. This is the load the bead exists to shed.
func TestPostTradeScan_NonSampledFreshMarket_SkipsRedundantScan(t *testing.T) {
	repo := &samplingFakeMarketRepo{waypoint: testBuyWaypoint, good: optypeGood, lastUpdated: time.Now()}
	refresher := &countingRefresher{}
	h := newSamplingBuyHandler(t, repo, refresher)

	ctx := shared.WithScanPolicy(buyCtx(), shared.ScanPolicy{MaxScanAge: 90 * time.Second, ImpactSampleRate: 0})
	runSamplingBuy(t, h, ctx)

	require.Equal(t, 0, refresher.scans,
		"a non-sampled trade at a freshly-scanned market must reuse the cache, not re-scan (the redundant scan sp-v34b sheds)")
}

// A SAMPLED trade still captures the impact pair: the post-trade scan fires so the
// analyst retains ~1 day of consecutive-leg pairs to refit the model per era.
func TestPostTradeScan_SampledTrade_CapturesImpactPair(t *testing.T) {
	repo := &samplingFakeMarketRepo{waypoint: testBuyWaypoint, good: optypeGood, lastUpdated: time.Now()}
	refresher := &countingRefresher{}
	h := newSamplingBuyHandler(t, repo, refresher)

	ctx := shared.WithScanPolicy(buyCtx(), shared.ScanPolicy{MaxScanAge: 90 * time.Second, ImpactSampleRate: 1.0})
	runSamplingBuy(t, h, ctx)

	require.Equal(t, 1, refresher.scans,
		"a sampled trade must STILL fire the post-trade impact scan (records dP/P for the per-era refit)")
}

// A STALE (older than the gate) cached market is still re-scanned even when the trade
// is not sampled: the trade must see fresh-enough prices. Preserves correctness — the
// gate only skips a genuinely fresh cache.
func TestPostTradeScan_StaleMarket_StillScansEvenWhenNotSampled(t *testing.T) {
	repo := &samplingFakeMarketRepo{waypoint: testBuyWaypoint, good: optypeGood, lastUpdated: time.Now().Add(-10 * time.Minute)}
	refresher := &countingRefresher{}
	h := newSamplingBuyHandler(t, repo, refresher)

	ctx := shared.WithScanPolicy(buyCtx(), shared.ScanPolicy{MaxScanAge: 90 * time.Second, ImpactSampleRate: 0})
	runSamplingBuy(t, h, ctx)

	require.Equal(t, 1, refresher.scans,
		"a stale cached market must still be re-scanned so the trade sees fresh-enough prices")
}

// A never-scanned market (no cached row) is always scanned even when not sampled — the
// trade cannot decide on prices that do not exist.
func TestPostTradeScan_NeverScannedMarket_StillScans(t *testing.T) {
	repo := &samplingFakeMarketRepo{waypoint: testBuyWaypoint, good: optypeGood} // zero lastUpdated → nil market
	refresher := &countingRefresher{}
	h := newSamplingBuyHandler(t, repo, refresher)

	ctx := shared.WithScanPolicy(buyCtx(), shared.ScanPolicy{MaxScanAge: 90 * time.Second, ImpactSampleRate: 0})
	runSamplingBuy(t, h, ctx)

	require.Equal(t, 1, refresher.scans, "a never-scanned market must be scanned on any trade")
}

// Deploy-safety + mutation: an INERT policy preserves the pre-sp-v34b unconditional
// post-trade scan. "No policy stamped" is every pre-sp-v34b / non-tour caller; the
// mutation (rate=1.0 + max_age=0) is the explicit proof the two gates are load-bearing
// — turn them off and the full-scan behavior returns.
func TestPostTradeScan_InertPolicy_AlwaysScans(t *testing.T) {
	cases := []struct {
		name   string
		policy *shared.ScanPolicy
	}{
		{"no policy stamped (pre-sp-v34b / non-tour caller)", nil},
		{"mutation: rate=1.0 + max_age=0 restores full-scan", &shared.ScanPolicy{MaxScanAge: 0, ImpactSampleRate: 1.0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &samplingFakeMarketRepo{waypoint: testBuyWaypoint, good: optypeGood, lastUpdated: time.Now()}
			refresher := &countingRefresher{}
			h := newSamplingBuyHandler(t, repo, refresher)

			ctx := buyCtx()
			if tc.policy != nil {
				ctx = shared.WithScanPolicy(ctx, *tc.policy)
			}
			runSamplingBuy(t, h, ctx)

			require.Equal(t, 1, refresher.scans, "an inert policy must preserve the pre-sp-v34b unconditional post-trade scan")
		})
	}
}

// The sampler is the standalone algorithm the sampling gate consumes: deterministic
// per trade key, and UNBIASED across a fixed market/hull (a whole lane is never
// permanently in or out — every occurrence gets a fresh even draw), so ~ImpactSampleRate
// of trades on ANY lane are instrumented. Tested directly because distribution/uniformity
// cannot be asserted through a handler without thousands of dispatches.
func TestSampleImpact_DeterministicAndUnbiased(t *testing.T) {
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("TORWIND-19|IRON_ORE|X1-NK36-D39|%d", i)
		require.Equal(t, sampleImpact(key, 0.15), sampleImpact(key, 0.15),
			"the same trade key must sample deterministically")
	}

	const n = 5000
	sampled := 0
	for i := 0; i < n; i++ {
		// A single fixed lane (same ship/good/market) varied only by the per-trade
		// nonce: proves the lane is not biased permanently in or out.
		if sampleImpact(fmt.Sprintf("TORWIND-19|IRON_ORE|X1-NK36-D39|%d", i), 0.15) {
			sampled++
		}
	}
	frac := float64(sampled) / float64(n)
	require.InDelta(t, 0.15, frac, 0.03, "~15%% of trades on one lane must be sampled (unbiased); got %.3f", frac)

	require.False(t, sampleImpact("anything", 0), "rate 0 never samples")
	require.False(t, sampleImpact("anything", -1), "a non-positive rate never samples")
	require.True(t, sampleImpact("anything", 1), "rate 1 always samples")
	require.True(t, sampleImpact("anything", 2), "a rate >= 1 always samples")
}

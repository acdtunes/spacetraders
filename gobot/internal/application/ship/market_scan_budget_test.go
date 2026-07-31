package ship

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// budgetTestPlayerID is the player every budget fixture admits on behalf of.
// The budgets carry a player only to LABEL what they emit (sp-e4dkw) — one
// bucket, one map size and one rate are shared across the whole daemon — so the
// value is arbitrary and identical everywhere.
const budgetTestPlayerID = 1

// fakeClock is a hand-advanced clock so the allowance's refill can be driven
// deterministically instead of slept through.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestBudget(t *testing.T, rateReqPerSec float64, clampR int) (*ScanBudget, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)}
	b := NewScanBudget(rateReqPerSec, clampR)
	b.setClock(clock.now)
	return b, clock
}

// cachedAt builds a market row whose last-updated stamp is `age` before now, so
// a fixture can position a market precisely either side of its interval.
func cachedAt(t *testing.T, waypoint string, now time.Time, age time.Duration, goods []market.TradeGood) *market.Market {
	t.Helper()
	m, err := market.NewMarket(waypoint, goods, now.Add(-age))
	require.NoError(t, err)
	return m
}

func goodsWithSpread(t *testing.T, symbol string, bid, ask int) []market.TradeGood {
	t.Helper()
	supply := "MODERATE"
	activity := "WEAK"
	// PurchasePrice is the ASK we pay, SellPrice the BID we receive (sp-en5h7).
	g, err := market.NewTradeGood(symbol, &supply, &activity, ask, bid, 60, market.TradeTypeExchange)
	require.NoError(t, err)
	return []market.TradeGood{*g}
}

// seedMap registers n measured markets of identical spread, so the gate has a
// realistic denominator.
//
// THE MAP SIZE IS LOAD-BEARING IN EVERY CAP FIXTURE, not scene-setting. The
// anti-starvation bound is ValueClampR x marketsKnown / rate, so on a ONE-market
// map it is only ~23 seconds: any market a fixture calls "overdue" is instantly
// past the bound, the escape hatch fires, and the test measures the escape while
// believing it measures the cap. A cap fixture must therefore hold staleness
// inside the band (interval, bound), which needs a map big enough for that band
// to exist. staleBand returns an age in it.
func seedMap(t *testing.T, b *ScanBudget, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		b.Observe(fmt.Sprintf("X1-AA-A%d", i), goodsWithSpread(t, "FUEL", 90, 110))
	}
}

// seedMapWithHotMarketUnderTest registers n dull markets plus one conspicuously
// wide-spread market, and returns that market's symbol.
//
// A CAP FIXTURE MUST USE THE HOT MARKET, not a dull one, and the reason is a trap
// this suite walked into once already. The value reserve declines a
// baseline-weight market whenever the bucket is below the reserve, so a dull
// market is refused by the VALUE BAR long before the token cap is consulted — a
// "budget cap" test built on a dull market stays green when the cap is deleted,
// because the bar was doing the work all along. A market clamped at ValueClampR
// clears the bar at every fill, which leaves the token cap as the only rule that
// can decline it.
func seedMapWithHotMarketUnderTest(t *testing.T, b *ScanBudget, n int) string {
	t.Helper()
	for i := 0; i < n; i++ {
		b.Observe(fmt.Sprintf("X1-AA-DULL%d", i), goodsWithSpread(t, "FUEL", 99, 101))
	}
	const hot = "X1-AA-HOT"
	b.Observe(hot, goodsWithSpread(t, "FUEL", 20, 180))

	b.mu.Lock()
	median, _, _ := b.aggregateLocked()
	weight := b.weightLocked(hot, median)
	b.mu.Unlock()
	require.InDelta(t, float64(b.Snapshot().ValueClampR), weight, 1e-9,
		"the market under test must sit at the value clamp, or the bar and not the cap is what declines it")
	return hot
}

// staleBand is an age that is comfortably overdue for a baseline market yet well
// short of the anti-starvation bound, so the budget cap and the value bar are the
// rules under test.
func staleBand(t *testing.T, b *ScanBudget) time.Duration {
	t.Helper()
	snap := b.Snapshot()
	age := snap.TypicalInterval * 2
	require.Greater(t, age, snap.TypicalInterval, "fixture must be overdue")
	require.Less(t, age, snap.WorstCaseStaleness, "fixture must not reach the starvation escape")
	return age
}

// -----------------------------------------------------------------------------
// The budget is enforced by construction — there is no unpaced scanner.
// -----------------------------------------------------------------------------

func TestNewScanBudget_NonsenseArgumentsResolveToDefaultsRatherThanDisablingPacing(t *testing.T) {
	for _, rate := range []float64{0, -1} {
		b := NewScanBudget(rate, 8)
		assert.InDelta(t, defaultBudgetReqPerSec, b.Snapshot().RateReqPerSec, 1e-9,
			"a non-positive rate must fall back to the default budget, never to unlimited")
	}
	assert.Equal(t, defaultValueClampR, NewScanBudget(0.35, 0).Snapshot().ValueClampR)
	assert.Equal(t, defaultValueClampR, NewScanBudget(0.35, -3).Snapshot().ValueClampR)
}

func TestNewMarketScanner_AlwaysHasABudgetSoNoCallerCanBuildAnUnpacedScanner(t *testing.T) {
	s := NewMarketScanner(nil, nil, nil, nil)

	require.NotNil(t, s.ScanBudget(), "the scanner must not be constructible without an allowance")
	assert.InDelta(t, defaultBudgetReqPerSec, s.ScanBudget().Snapshot().RateReqPerSec, 1e-9)
}

func TestSetScanBudget_IgnoresNilSoTheAllowanceCannotBeRemoved(t *testing.T) {
	s := NewMarketScanner(nil, nil, nil, nil)
	configured := NewScanBudget(0.9, 4)

	s.SetScanBudget(configured)
	require.Same(t, configured, s.ScanBudget())

	s.SetScanBudget(nil)
	assert.Same(t, configured, s.ScanBudget(), "nil must not clear the allowance")
}

// -----------------------------------------------------------------------------
// The hard cap: sustained admissions track the configured rate.
// -----------------------------------------------------------------------------

// TestAdmit_SustainedAdmissionsTrackTheConfiguredRate is the end-to-end budget
// claim on the stateful gate: however hard callers push, the number of requests
// authorised over a window is the budget times the window, plus the burst.
func TestAdmit_SustainedAdmissionsTrackTheConfiguredRate(t *testing.T) {
	const ratePerSec = 0.5
	b, clock := newTestBudget(t, ratePerSec, 8)
	ctx := context.Background()

	hot := seedMapWithHotMarketUnderTest(t, b, 50)
	age := staleBand(t, b)

	const window = 2000 * time.Second
	deadline := clock.now().Add(window)
	admitted := 0
	for clock.now().Before(deadline) {
		// A market comfortably past its interval but far short of the worst-case
		// bound, asked about relentlessly.
		cached := cachedAt(t, hot, clock.now(), age, goodsWithSpread(t, "FUEL", 20, 180))
		if b.Admit(ctx, budgetTestPlayerID, hot, cached, marketscan.Discretionary) == marketscan.Spend {
			admitted++
		}
		clock.advance(time.Second)
	}

	expected := ratePerSec * window.Seconds()
	assert.LessOrEqual(t, float64(admitted), expected+burstRequests+1,
		"sustained admissions must not exceed the budget plus one burst")
	assert.Greater(t, float64(admitted), expected*0.5,
		"and the budget must actually be spent, not merely capped")
}

func TestAdmit_ExhaustedAllowanceDeclinesAnOverdueMarket(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)
	ctx := context.Background()
	hot := seedMapWithHotMarketUnderTest(t, b, 50)
	age := staleBand(t, b)

	// Drain the bucket without letting the clock advance, so nothing refills.
	drained := 0
	for i := 0; i < burstRequests+4; i++ {
		cached := cachedAt(t, hot, clock.now(), age, goodsWithSpread(t, "FUEL", 20, 180))
		if b.Admit(ctx, budgetTestPlayerID, hot, cached, marketscan.Discretionary) == marketscan.Spend {
			drained++
		}
	}

	assert.LessOrEqual(t, drained, burstRequests,
		"with a frozen clock the gate may authorise at most one bucket's worth")
	assert.Positive(t, b.Snapshot().Declined)
}

func TestAdmit_AllowanceRefillsAsTimePasses(t *testing.T) {
	b, clock := newTestBudget(t, 1.0, 8)
	ctx := context.Background()
	hot := seedMapWithHotMarketUnderTest(t, b, 50)
	age := staleBand(t, b)
	stale := func() *market.Market {
		return cachedAt(t, hot, clock.now(), age, goodsWithSpread(t, "FUEL", 20, 180))
	}

	for i := 0; i < burstRequests+4; i++ {
		b.Admit(ctx, budgetTestPlayerID, hot, stale(), marketscan.Discretionary)
	}
	require.Equal(t, marketscan.ServeFromStore, b.Admit(ctx, budgetTestPlayerID, hot, stale(), marketscan.Discretionary),
		"bucket must be empty before the refill is tested")

	clock.advance(2 * time.Second) // 1.0 req/s => 2 tokens back

	assert.Equal(t, marketscan.Spend, b.Admit(ctx, budgetTestPlayerID, hot, stale(), marketscan.Discretionary))
}

// -----------------------------------------------------------------------------
// Invariant: a growing map converts reads into cache hits, not into requests.
// -----------------------------------------------------------------------------

// TestAdmit_TheSameReadIsDeclinedOnALargerMapAtTheSameBudget is the invariant
// observed through the gate rather than the arithmetic: one market, one cache
// age, one budget — and the answer flips from spend to serve-from-store purely
// because more markets are now known.
func TestAdmit_TheSameReadIsDeclinedOnALargerMapAtTheSameBudget(t *testing.T) {
	ctx := context.Background()
	const age = 3 * time.Minute

	ask := func(b *ScanBudget, clock *fakeClock) marketscan.Decision {
		cached := cachedAt(t, "X1-AA-A1", clock.now(), age, goodsWithSpread(t, "FUEL", 90, 110))
		return b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", cached, marketscan.Discretionary)
	}

	small, smallClock := newTestBudget(t, 0.35, 8)
	for i := 0; i < 20; i++ {
		small.Observe(fmt.Sprintf("X1-AA-A%d", i), goodsWithSpread(t, "FUEL", 90, 110))
	}

	large, largeClock := newTestBudget(t, 0.35, 8)
	for i := 0; i < 2000; i++ {
		large.Observe(fmt.Sprintf("X1-AA-A%d", i), goodsWithSpread(t, "FUEL", 90, 110))
	}

	assert.Equal(t, marketscan.Spend, ask(small, smallClock),
		"on a 20-market map a 3-minute-old row is overdue")
	assert.Equal(t, marketscan.ServeFromStore, ask(large, largeClock),
		"on a 2000-market map the identical read is served from store — the map grew, the spend did not")

	assert.Greater(t, large.Snapshot().TypicalInterval, small.Snapshot().TypicalInterval,
		"the interval is an OUTPUT of budget over markets known")
	assert.InDelta(t, small.Snapshot().RateReqPerSec, large.Snapshot().RateReqPerSec, 1e-9,
		"and the budget itself is unchanged by the map")
}

// -----------------------------------------------------------------------------
// Value weighting drives the rotation.
// -----------------------------------------------------------------------------

func TestObserve_WideSpreadMarketEarnsAShorterIntervalThanANarrowOne(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)

	// A fleet of narrow markets, plus one conspicuously wide one.
	for i := 0; i < 30; i++ {
		b.Observe(fmt.Sprintf("X1-AA-N%d", i), goodsWithSpread(t, "FUEL", 99, 101))
	}
	b.Observe("X1-AA-WIDE", goodsWithSpread(t, "FUEL", 50, 150))

	b.mu.Lock()
	median, total, _ := b.aggregateLocked()
	wide := b.weightLocked("X1-AA-WIDE", median)
	narrow := b.weightLocked("X1-AA-N0", median)
	b.mu.Unlock()

	assert.Greater(t, wide, narrow, "the wider-spread market must earn more attention")
	assert.Less(t, marketscan.Interval(marketscan.Budget{RateReqPerSec: 0.35, ValueClampR: 8}, wide, total),
		marketscan.Interval(marketscan.Budget{RateReqPerSec: 0.35, ValueClampR: 8}, narrow, total),
		"more attention means a shorter interval")
}

func TestObserve_InvertedQuotesAreNotReadAsANarrowSpread(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)

	// Bid above ask is impossible; it must be dropped, leaving the market
	// unmeasured (at the prior) rather than recorded as uninteresting.
	b.Observe("X1-AA-BAD", goodsWithSpread(t, "FUEL", 150, 50))

	assert.Equal(t, 1, b.Snapshot().MarketsMeasured,
		"an inverted quote still registers an observation entry")
	b.mu.Lock()
	spread := b.spread["X1-AA-BAD"]
	b.mu.Unlock()
	assert.Zero(t, spread, "but it must not be recorded as a real spread reading")
}

func TestAggregate_UnmeasuredMarketsAreCountedAtThePriorNotAtZero(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)
	ctx := context.Background()

	// Two markets asked about but never scanned.
	b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", nil, marketscan.Discretionary)
	b.Admit(ctx, budgetTestPlayerID, "X1-AA-A2", nil, marketscan.Discretionary)

	snap := b.Snapshot()
	assert.Equal(t, 2, snap.MarketsKnown)
	assert.Equal(t, 0, snap.MarketsMeasured)
	assert.InDelta(t, 2*marketscan.PriorWeight(8), snap.TotalWeight, 1e-9,
		"markets nobody has scanned yet still have to be scanned, so they must hold weight")
}

// -----------------------------------------------------------------------------
// No starvation, and the exempt classes.
// -----------------------------------------------------------------------------

// TestAdmit_ACompletelyStarvedMarketIsEventuallyAdmittedEvenWithAnEmptyBucket is
// the anti-starvation guarantee at the gate: the bucket is drained and kept
// drained, the market is the dullest on the map, and it is STILL scanned once it
// passes the worst-case bound. This is what keeps a crushed market's recovery
// detectable.
func TestAdmit_ACompletelyStarvedMarketIsEventuallyAdmittedEvenWithAnEmptyBucket(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		b.Observe(fmt.Sprintf("X1-AA-H%d", i), goodsWithSpread(t, "FUEL", 40, 160)) // hot fleet
	}
	b.Observe("X1-AA-COLD", goodsWithSpread(t, "FUEL", 100, 100)) // flat: no spread at all

	// Drain the allowance and keep the clock still so it cannot refill.
	for i := 0; i < burstRequests*2; i++ {
		b.Admit(ctx, budgetTestPlayerID, "X1-AA-H0", cachedAt(t, "X1-AA-H0", clock.now(), time.Hour, goodsWithSpread(t, "FUEL", 40, 160)), marketscan.Discretionary)
	}
	require.Less(t, b.Snapshot().TokensAvailable, 1.0, "fixture must leave the allowance exhausted")

	bound := b.Snapshot().WorstCaseStaleness
	require.Less(t, bound, 30*24*time.Hour, "the bound must be a real number")

	justInside := cachedAt(t, "X1-AA-COLD", clock.now(), bound-time.Minute, goodsWithSpread(t, "FUEL", 100, 100))
	assert.Equal(t, marketscan.ServeFromStore, b.Admit(ctx, budgetTestPlayerID, "X1-AA-COLD", justInside, marketscan.Discretionary),
		"before the bound the cold market yields, which is what makes the bound meaningful")

	pastBound := cachedAt(t, "X1-AA-COLD", clock.now(), bound+time.Minute, goodsWithSpread(t, "FUEL", 100, 100))
	assert.Equal(t, marketscan.Spend, b.Admit(ctx, budgetTestPlayerID, "X1-AA-COLD", pastBound, marketscan.Discretionary),
		"past the bound it is scanned regardless of value or contention — no market starves")
}

func TestAdmit_NeverScannedMarketIsAdmittedEvenWithAnEmptyAllowance(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)
	ctx := context.Background()
	seedMap(t, b, 50)
	// Drain with money-guard reads: those are never declined and always debit, so
	// they can empty the bucket outright. Discretionary reads cannot — the value
	// reserve stops a baseline market draining the last of the allowance, which is
	// the point of the reserve.
	for i := 0; i < burstRequests*2; i++ {
		b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", cachedAt(t, "X1-AA-A1", clock.now(), time.Second, goodsWithSpread(t, "FUEL", 40, 160)), marketscan.Earning)
	}
	require.Less(t, b.Snapshot().TokensAvailable, 1.0)

	assert.Equal(t, marketscan.Spend, b.Admit(ctx, budgetTestPlayerID, "X1-AA-NEW", nil, marketscan.Discretionary),
		"there is nothing in the store to serve, so a never-scanned market cannot be declined")
}

func TestAdmit_MarketRowStampedInTheFutureIsTreatedAsUnknownNotAsFresh(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)

	future := cachedAt(t, "X1-AA-A1", clock.now(), -time.Hour, goodsWithSpread(t, "FUEL", 90, 110))

	assert.Equal(t, marketscan.Spend, b.Admit(context.Background(), budgetTestPlayerID, "X1-AA-A1", future, marketscan.Discretionary),
		"one bad timestamp must not mute a market indefinitely")
}

// TestAdmit_EarningReadIsAdmittedWithTheAllowanceExhaustedAndStillDebitsIt is the
// money-guard exemption at the gate, and the second half matters as much as the
// first: an exempt read is METERED, so a burst of trade-critical verification
// squeezes discretionary scanning instead of being added on top of the budget.
func TestAdmit_EarningReadIsAdmittedWithTheAllowanceExhaustedAndStillDebitsIt(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)
	ctx := context.Background()
	seedMap(t, b, 50)
	overdue := staleBand(t, b)
	fresh := func() *market.Market {
		return cachedAt(t, "X1-AA-A1", clock.now(), time.Second, goodsWithSpread(t, "FUEL", 90, 110))
	}

	// A just-scanned market with a completely open allowance is declined as a
	// discretionary read...
	require.Equal(t, marketscan.ServeFromStore, b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", fresh(), marketscan.Discretionary))

	// ...and admitted as a money-guard read, over and over, even once the
	// allowance is long gone.
	for i := 0; i < burstRequests*3; i++ {
		require.Equal(t, marketscan.Spend, b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", fresh(), marketscan.Earning),
			"a pre-commit money guard is never served from store (RULINGS #4)")
	}

	snap := b.Snapshot()
	assert.Positive(t, snap.Forced, "reads admitted against an empty bucket must be recorded as overdrafts")
	assert.Less(t, snap.TokensAvailable, 1.0,
		"and they must have consumed the allowance, so discretionary scanning is squeezed")

	assert.Equal(t, marketscan.ServeFromStore,
		b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", cachedAt(t, "X1-AA-A1", clock.now(), overdue, goodsWithSpread(t, "FUEL", 90, 110)), marketscan.Discretionary),
		"an overdue discretionary read now finds the allowance spent by the guards")
}

func TestAdmit_PairedReadIsAdmittedOnAFreshCacheButStillNeedsAToken(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)
	ctx := context.Background()
	seedMap(t, b, 50)
	fresh := func() *market.Market {
		return cachedAt(t, "X1-AA-A1", clock.now(), 2*time.Second, goodsWithSpread(t, "FUEL", 90, 110))
	}

	require.Equal(t, marketscan.ServeFromStore, b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", fresh(), marketscan.Discretionary),
		"the same read as a discretionary one is vetoed for freshness")
	assert.Equal(t, marketscan.Spend, b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", fresh(), marketscan.Paired),
		"the after half of an impact pair is not redundant for following its before half")

	// Drain the allowance; the paired read now yields, unlike an Earning one.
	for i := 0; i < burstRequests*3; i++ {
		b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", fresh(), marketscan.Paired)
	}
	assert.Equal(t, marketscan.ServeFromStore, b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", fresh(), marketscan.Paired),
		"instrumentation is what the budget sheds first")
}

// -----------------------------------------------------------------------------
// Map size comes from the charted count when one is available.
// -----------------------------------------------------------------------------

type stubCounter struct {
	counts map[string]int
	err    error
	calls  int
}

func (c *stubCounter) ChartedMarketSystemCounts(ctx context.Context) (map[string]int, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.counts, nil
}

func TestAdmit_MapSizeComesFromTheChartedCountNotJustTheMarketsSeen(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)
	b.SetChartedMarketCounter(&stubCounter{counts: map[string]int{"X1-AA": 200, "X1-BB": 155}})

	b.Admit(context.Background(), budgetTestPlayerID, "X1-AA-A1", nil, marketscan.Discretionary)

	assert.Equal(t, 355, b.Snapshot().MarketsKnown,
		"the denominator is the charted map, not only the markets this gate happens to have been asked about")
}

func TestAdmit_ChartedCountIsCachedAndRefreshedOnItsTTL(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)
	counter := &stubCounter{counts: map[string]int{"X1-AA": 10}}
	b.SetChartedMarketCounter(counter)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", nil, marketscan.Discretionary)
	}
	assert.Equal(t, 1, counter.calls, "the map size must not be re-counted on every admission")

	counter.counts = map[string]int{"X1-AA": 40}
	clock.advance(chartedCountTTL + time.Second)
	b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", nil, marketscan.Discretionary)

	assert.Equal(t, 2, counter.calls)
	assert.Equal(t, 40, b.Snapshot().MarketsKnown, "and it must pick up a map that grew")
}

func TestAdmit_CounterFailureKeepsThePreviousCountRatherThanCollapsingTheDenominator(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)
	counter := &stubCounter{counts: map[string]int{"X1-AA": 300}}
	b.SetChartedMarketCounter(counter)
	ctx := context.Background()

	b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", nil, marketscan.Discretionary)
	require.Equal(t, 300, b.Snapshot().MarketsKnown)

	counter.err = errors.New("database unavailable")
	clock.advance(chartedCountTTL + time.Second)
	b.Admit(ctx, budgetTestPlayerID, "X1-AA-A1", nil, marketscan.Discretionary)

	assert.Equal(t, 300, b.Snapshot().MarketsKnown,
		"a counter hiccup must not reset the denominator, which would collapse every interval to nothing")
}

func TestAdmit_WithNoCounterWiredTheMarketsSeenAreTheDenominatorAndPacingStillHolds(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)
	ctx := context.Background()

	for i := 0; i < 12; i++ {
		b.Admit(ctx, budgetTestPlayerID, fmt.Sprintf("X1-AA-A%d", i), nil, marketscan.Discretionary)
	}

	snap := b.Snapshot()
	assert.Equal(t, 12, snap.MarketsKnown)
	assert.InDelta(t, defaultBudgetReqPerSec, snap.RateReqPerSec, 1e-9,
		"a missing counter must degrade the denominator, never the budget")
}

func TestSetChartedMarketCounter_IgnoresNil(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)
	counter := &stubCounter{counts: map[string]int{"X1-AA": 7}}
	b.SetChartedMarketCounter(counter)
	b.SetChartedMarketCounter(nil)

	b.Admit(context.Background(), budgetTestPlayerID, "X1-AA-A1", nil, marketscan.Discretionary)
	assert.Equal(t, 7, b.Snapshot().MarketsKnown, "nil must not detach a working counter")
}

// -----------------------------------------------------------------------------
// Class derivation from the context tags.
// -----------------------------------------------------------------------------

func TestScanClassOf_OnlyTheStampedCallPathsAreExempt(t *testing.T) {
	assert.Equal(t, marketscan.Discretionary, scanClassOf(context.Background()),
		"an unstamped read is budgeted, which is what makes the budget hold for call sites nobody classified")
	assert.Equal(t, marketscan.Earning, scanClassOf(shared.WithLiveScanRequired(context.Background())))
	assert.Equal(t, marketscan.Paired, scanClassOf(shared.WithPairedScan(context.Background())))
	assert.Equal(t, marketscan.Earning,
		scanClassOf(shared.WithPairedScan(shared.WithLiveScanRequired(context.Background()))),
		"a money guard outranks instrumentation when both are stamped")
}

// -----------------------------------------------------------------------------
// Concurrency: one shared allowance, not one per container.
// -----------------------------------------------------------------------------

func TestAdmit_IsSafeForTheConcurrentContainersThatShareOneAllowance(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)
	ctx := context.Background()

	done := make(chan struct{})
	for w := 0; w < 8; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 50; i++ {
				waypoint := fmt.Sprintf("X1-AA-A%d", i)
				b.Admit(ctx, budgetTestPlayerID, waypoint, nil, marketscan.Discretionary)
				b.Observe(waypoint, goodsWithSpread(t, "FUEL", 90, 110))
				b.Snapshot()
			}
		}(w)
	}
	for w := 0; w < 8; w++ {
		<-done
	}

	assert.Equal(t, 50, b.Snapshot().MarketsKnown)
}

// TestAdmit_UnderContentionTheHotMarketClaimsTheTokenAndTheDullOneYields is the
// value ordering observed through the gate rather than the policy, which is what
// proves the weight is actually plumbed from the spread observations into the
// admission decision. Two markets, both overdue, one allowance — and the one that
// earns most gets it.
func TestAdmit_UnderContentionTheHotMarketClaimsTheTokenAndTheDullOneYields(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)
	ctx := context.Background()
	hot := seedMapWithHotMarketUnderTest(t, b, 50)
	const dull = "X1-AA-DULL0"
	age := staleBand(t, b)

	// Draw the bucket down into the value reserve, where the bar bites. Earning
	// reads drain unconditionally, so they set the fill precisely.
	for b.Snapshot().TokensAvailable > 1.5 {
		b.Admit(ctx, budgetTestPlayerID, hot, cachedAt(t, hot, clock.now(), time.Second, goodsWithSpread(t, "FUEL", 20, 180)), marketscan.Earning)
	}
	require.GreaterOrEqual(t, b.Snapshot().TokensAvailable, 1.0,
		"a whole token must remain, or the cap decides and the value ordering is untested")

	assert.Equal(t, marketscan.ServeFromStore,
		b.Admit(ctx, budgetTestPlayerID, dull, cachedAt(t, dull, clock.now(), age, goodsWithSpread(t, "FUEL", 99, 101)), marketscan.Discretionary),
		"the dull market yields the contended token")
	assert.Equal(t, marketscan.Spend,
		b.Admit(ctx, budgetTestPlayerID, hot, cachedAt(t, hot, clock.now(), age, goodsWithSpread(t, "FUEL", 20, 180)), marketscan.Discretionary),
		"the market that earns most claims it")
}

// TestAdmit_AStarvedMarketOutranksAHotOneOnceItPassesTheBound is the other half of
// the ordering: value decides who goes FIRST, but never who goes at all. Past the
// bound the coldest market on the map is admitted from an allowance a hot market
// cannot draw on.
func TestAdmit_AStarvedMarketOutranksAHotOneOnceItPassesTheBound(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)
	ctx := context.Background()
	hot := seedMapWithHotMarketUnderTest(t, b, 50)
	const dull = "X1-AA-DULL0"

	// Empty the allowance outright.
	for i := 0; i < burstRequests*3; i++ {
		b.Admit(ctx, budgetTestPlayerID, hot, cachedAt(t, hot, clock.now(), time.Second, goodsWithSpread(t, "FUEL", 20, 180)), marketscan.Earning)
	}
	require.Less(t, b.Snapshot().TokensAvailable, 1.0)

	bound := b.Snapshot().WorstCaseStaleness
	assert.Equal(t, marketscan.ServeFromStore,
		b.Admit(ctx, budgetTestPlayerID, hot, cachedAt(t, hot, clock.now(), bound/2, goodsWithSpread(t, "FUEL", 20, 180)), marketscan.Discretionary),
		"even the hottest market cannot draw on an empty allowance")
	assert.Equal(t, marketscan.Spend,
		b.Admit(ctx, budgetTestPlayerID, dull, cachedAt(t, dull, clock.now(), bound+time.Minute, goodsWithSpread(t, "FUEL", 99, 101)), marketscan.Discretionary),
		"but the dullest market past the bound can — value orders the queue, it never empties it")
}

// -----------------------------------------------------------------------------
// Debit: attribution without deniability.
// -----------------------------------------------------------------------------

// TestDebit_ChargesTheAllowanceAndSqueezesDiscretionaryScanning is what makes the
// budget the honest TOTAL rather than merely the total of the reads it can refuse.
// The screen's catalogue gap fill cannot be declined — there is no store to serve
// a cache gap from — but the allowance it consumes is allowance discretionary
// scanning then cannot.
func TestDebit_ChargesTheAllowanceAndSqueezesDiscretionaryScanning(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)
	ctx := context.Background()
	hot := seedMapWithHotMarketUnderTest(t, b, 50)
	age := staleBand(t, b)

	before := b.Snapshot().TokensAvailable
	for i := 0; i < burstRequests*3; i++ {
		b.Debit(budgetTestPlayerID, fmt.Sprintf("X1-AA-UNVISITED%d", i))
	}
	after := b.Snapshot()

	assert.Less(t, after.TokensAvailable, before, "a debited read must consume the allowance")
	assert.Positive(t, after.Forced, "debits past an empty bucket must be recorded as overdrafts")
	assert.Equal(t, marketscan.ServeFromStore,
		b.Admit(ctx, budgetTestPlayerID, hot, cachedAt(t, hot, clock.now(), age, goodsWithSpread(t, "FUEL", 20, 180)), marketscan.Discretionary),
		"and an overdue hot market now finds the allowance spent by the catalogue reads")
}

func TestDebit_RegistersTheMarketSoItJoinsTheDenominator(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)

	b.Debit(budgetTestPlayerID, "X1-AA-UNVISITED")

	snap := b.Snapshot()
	assert.Equal(t, 1, snap.MarketsKnown,
		"a market discovered by the catalogue read shares the budget from then on")
	assert.Equal(t, uint64(1), snap.Admitted)
}

// -----------------------------------------------------------------------------
// RotationInputs: the numbers freshness consumers derive their caps from (sp-k4z5b).
// -----------------------------------------------------------------------------

// The map size must be counted on the FIRST question, not only after the first
// admission. At boot nothing has been admitted yet, and a zero denominator collapses
// every derived freshness cap back to its 75-minute floor — the exact behaviour the
// derivation exists to remove. A consumer asking right after a daemon restart must get
// the real map.
func TestRotationInputs_CountsTheMapBeforeAnythingHasBeenAdmitted(t *testing.T) {
	b, _ := newTestBudget(t, 0.70, 8)
	counter := &stubCounter{counts: map[string]int{"X1-AA": 4000, "X1-BB": 389}}
	b.SetChartedMarketCounter(counter)

	budget, marketsKnown := b.RotationInputs(context.Background())

	assert.Equal(t, 1, counter.calls, "the first rotation question must count the map itself")
	assert.Equal(t, 4389, marketsKnown, "a boot-time consumer must see the real map, not an empty one")
	assert.Equal(t, 0.70, budget.RateReqPerSec)
	assert.Equal(t, 8, budget.ValueClampR)

	// And the derived bound is the one the whole change turns on: hours, not 75 minutes.
	assert.Greater(t, marketscan.FreshnessCap(75*time.Minute, budget, marketsKnown), 2*time.Hour)
}

// A nil budget (no scanner wired) reports an empty map, which FreshnessCap answers with
// the caller's own floor — never a widened cap.
func TestRotationInputs_NilBudgetReportsAnEmptyMap(t *testing.T) {
	var b *ScanBudget
	budget, marketsKnown := b.RotationInputs(context.Background())

	assert.Equal(t, 0, marketsKnown)
	assert.Equal(t, 75*time.Minute, marketscan.FreshnessCap(75*time.Minute, budget, marketsKnown))
}

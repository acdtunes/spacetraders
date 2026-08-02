package marketscan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// era5Budget is the shape the daemon actually runs: a third of a request per
// second shared by every market reader, and the hottest market earning at most
// eight times the coldest one's attention.
func era5Budget() Budget {
	return Budget{RateReqPerSec: 0.35, ValueClampR: 8}
}

// -----------------------------------------------------------------------------
// Invariant 1: total scan cost does not grow with the charted map.
// -----------------------------------------------------------------------------

// TestInterval_TotalScanRateEqualsTheBudgetWhateverTheMapSize is the load-bearing
// arithmetic claim of the whole package: summing each market's scan frequency
// over the known map returns the budget, and returns THE SAME budget for a map
// a hundred times larger. If this ever fails, sensing has gone back to scaling
// with the charted map and the server ceiling binds again.
func TestInterval_TotalScanRateEqualsTheBudgetWhateverTheMapSize(t *testing.T) {
	b := era5Budget()

	for _, marketsKnown := range []int{10, 355, 1000} {
		// A realistic mixed map: a tenth of markets at the clamp, the rest at
		// the baseline. The mix must not matter either.
		weights := make([]float64, 0, marketsKnown)
		total := 0.0
		for i := 0; i < marketsKnown; i++ {
			w := baselineWeight
			if i%10 == 0 {
				w = float64(b.ValueClampR)
			}
			weights = append(weights, w)
			total += w
		}

		aggregateRate := 0.0
		for _, w := range weights {
			aggregateRate += 1.0 / Interval(b, w, total).Seconds()
		}

		assert.InDelta(t, b.RateReqPerSec, aggregateRate, 1e-9,
			"total scan rate across %d known markets must equal the fixed budget", marketsKnown)
	}
}

func TestInterval_WideningMapLengthensEachMarketsInterval(t *testing.T) {
	b := era5Budget()

	small := Interval(b, baselineWeight, 100)
	large := Interval(b, baselineWeight, 1000)

	assert.Greater(t, large, small,
		"knowing ten times as many markets must mean looking at each one ten times less often")
	assert.InDelta(t, 10.0, large.Seconds()/small.Seconds(), 1e-9)
}

func TestInterval_ValuableMarketComesRoundMoreOftenInProportionToItsWeight(t *testing.T) {
	b := era5Budget()
	const total = 400

	dull := Interval(b, baselineWeight, total)
	hot := Interval(b, 4, total)

	assert.InDelta(t, 4.0, dull.Seconds()/hot.Seconds(), 1e-9,
		"a market worth 4x the baseline is scanned 4x as often")
}

func TestInterval_DegenerateInputsReturnTheCeilingRatherThanDividingByZero(t *testing.T) {
	b := era5Budget()

	require.NotPanics(t, func() {
		assert.Equal(t, stalenessCeiling, Interval(Budget{RateReqPerSec: 0, ValueClampR: 8}, 1, 100))
		assert.Equal(t, stalenessCeiling, Interval(b, 0, 100))
		assert.Equal(t, stalenessCeiling, Interval(b, 1, 0))
		assert.Equal(t, stalenessCeiling, Interval(b, 1, -5))
	})
}

func TestInterval_NearZeroRateIsClampedToTheCeilingRatherThanOverflowing(t *testing.T) {
	tiny := Budget{RateReqPerSec: 1e-18, ValueClampR: 8}

	got := Interval(tiny, baselineWeight, 1000)

	assert.Equal(t, stalenessCeiling, got, "an unrepresentable interval clamps instead of wrapping negative")
	assert.Positive(t, got)
}

// -----------------------------------------------------------------------------
// Guarantee: the hard budget cap.
// -----------------------------------------------------------------------------

// TestDecide_BudgetExhaustedServesFromStoreEvenWhenDue is the budget cap itself.
// A market that is genuinely overdue for its interval, and valuable enough to
// clear any bar, is STILL declined when the bucket holds no whole token. Without
// this rule the budget is advisory and the measured 0.644 req/s leak returns.
//
// MUTATION TARGET: deleting the `req.TokensAvailable < 1` branch in Decide must
// fail this test.
func TestDecide_BudgetExhaustedServesFromStoreEvenWhenDue(t *testing.T) {
	b := era5Budget()
	total := 355.0

	req := Request{
		Class:        Discretionary,
		Weight:       float64(b.ValueClampR), // the hottest market there can be
		TotalWeight:  total,
		MarketsKnown: 355,
		// Overdue for its own interval, but nowhere near the starvation escape.
		Staleness:       Interval(b, float64(b.ValueClampR), total) * 2,
		TokensAvailable: 0.4, // less than one whole request
		BucketCapacity:  10,
	}

	require.Less(t, req.Staleness, MaxStaleness(b, 355),
		"fixture must sit below the anti-starvation escape or it proves nothing about the cap")

	assert.Equal(t, ServeFromStore, Decide(b, req),
		"an exhausted budget declines even an overdue, maximally valuable market")
}

func TestDecide_TokenAvailableAndDueAdmitsTheRead(t *testing.T) {
	b := era5Budget()
	total := 355.0

	req := Request{
		Weight:          float64(b.ValueClampR),
		TotalWeight:     total,
		MarketsKnown:    355,
		Staleness:       Interval(b, float64(b.ValueClampR), total) * 2,
		TokensAvailable: 10,
		BucketCapacity:  10,
	}

	assert.Equal(t, Spend, Decide(b, req))
}

// TestDecide_MarketNotYetDueServesFromStore is the mechanism by which a growing
// map costs no more requests: the interval widens, so the same arrival pattern
// produces cache hits instead of API calls.
func TestDecide_MarketNotYetDueServesFromStore(t *testing.T) {
	b := era5Budget()
	total := 355.0

	req := Request{
		Weight:          baselineWeight,
		TotalWeight:     total,
		MarketsKnown:    355,
		Staleness:       Interval(b, baselineWeight, total) / 2, // only half way to due
		TokensAvailable: 10,                                     // budget is wide open
		BucketCapacity:  10,
	}

	assert.Equal(t, ServeFromStore, Decide(b, req),
		"a fresh-enough market is served from store even with the whole budget free")
}

func TestDecide_TheSameArrivalRateCostsFewerRequestsAsTheMapGrows(t *testing.T) {
	b := era5Budget()
	// One reader asking about one market every 60s, on two different map sizes.
	const arrivalGap = 60 * time.Second

	smallMap := Request{Weight: baselineWeight, TotalWeight: 15, MarketsKnown: 15, Staleness: arrivalGap, TokensAvailable: 10, BucketCapacity: 10}
	largeMap := Request{Weight: baselineWeight, TotalWeight: 1500, MarketsKnown: 1500, Staleness: arrivalGap, TokensAvailable: 10, BucketCapacity: 10}

	assert.Equal(t, Spend, Decide(b, smallMap), "on a small map a 60s-old row is due (15 markets / 0.35 req/s = 43s interval)")
	assert.Equal(t, ServeFromStore, Decide(b, largeMap),
		"on a map 100x larger the same 60s-old row is not due yet — the map grew, the spend did not")
}

// -----------------------------------------------------------------------------
// Guarantee: value-weighted priority.
// -----------------------------------------------------------------------------

// TestDecide_UnderContentionAdmitsTheValuableMarketAndDeclinesTheDullOne is the
// priority ordering. Two markets, identically overdue, identical budget state —
// only their value differs, and only the valuable one gets the token.
//
// MUTATION TARGET: flattening the weighting (making requiredWeight return the
// baseline regardless of fill, or ignoring req.Weight in Decide) must fail this
// test.
func TestDecide_UnderContentionAdmitsTheValuableMarketAndDeclinesTheDullOne(t *testing.T) {
	b := era5Budget()
	total := 355.0

	// A nearly-drained bucket: 1.5 of 10 tokens left, so the bar sits high.
	const tokensLeft, capacity = 1.5, 10.0

	overdue := func(weight float64) Request {
		return Request{
			Class:           Discretionary,
			Weight:          weight,
			TotalWeight:     total,
			MarketsKnown:    355,
			Staleness:       Interval(b, weight, total) * 2, // both equally overdue, relative to their own interval
			TokensAvailable: tokensLeft,
			BucketCapacity:  capacity,
		}
	}

	hot := overdue(float64(b.ValueClampR))
	dull := overdue(baselineWeight)

	require.Less(t, hot.Staleness, MaxStaleness(b, 355), "hot fixture must not reach the starvation escape")
	require.Less(t, dull.Staleness, MaxStaleness(b, 355), "dull fixture must not reach the starvation escape")
	require.GreaterOrEqual(t, tokensLeft, 1.0, "fixture must hold a whole token, or the cap decides and value is untested")

	assert.Equal(t, Spend, Decide(b, hot), "the market that earns most claims the scarce token")
	assert.Equal(t, ServeFromStore, Decide(b, dull), "the dull market yields it")
}

func TestDecide_WithTheBucketFullEveryDueMarketIsAdmittedRegardlessOfValue(t *testing.T) {
	b := era5Budget()
	total := 355.0

	dull := Request{
		Weight:          baselineWeight,
		TotalWeight:     total,
		MarketsKnown:    355,
		Staleness:       Interval(b, baselineWeight, total) * 2,
		TokensAvailable: 10,
		BucketCapacity:  10,
	}

	assert.Equal(t, Spend, Decide(b, dull),
		"priority only rations a CONTENDED budget; an uncontended one serves the dull market too")
}

func TestRequiredWeight_RisesFromBaselineToTheClampAsTheBucketDrains(t *testing.T) {
	b := era5Budget()

	assert.InDelta(t, baselineWeight, requiredWeight(b, 10, 10), 1e-9, "full bucket admits everyone")
	assert.InDelta(t, baselineWeight, requiredWeight(b, 5, 10), 1e-9,
		"at the top of the reserve every due market is still admitted — the interval, not the bar, is the primary allocator")
	assert.InDelta(t, float64(b.ValueClampR), requiredWeight(b, 0, 10), 1e-9, "empty bucket admits only the hottest")
	assert.InDelta(t, 4.5, requiredWeight(b, 2.5, 10), 1e-9, "half way into the reserve sits half way up the range")

	// Monotone: draining the bucket never lowers the bar.
	prev := requiredWeight(b, 10, 10)
	for tokens := 9.5; tokens >= 0; tokens -= 0.5 {
		got := requiredWeight(b, tokens, 10)
		assert.GreaterOrEqual(t, got, prev, "the admission bar must not fall as the budget drains")
		prev = got
	}
}

func TestRequiredWeight_FlatClampCollapsesTheBarSoValueStopsRationing(t *testing.T) {
	flat := Budget{RateReqPerSec: 0.35, ValueClampR: 1}

	assert.InDelta(t, baselineWeight, requiredWeight(flat, 10, 10), 1e-9)
	assert.InDelta(t, baselineWeight, requiredWeight(flat, 0, 10), 1e-9,
		"clamp 1 is how weighting is switched off: the bar never rises")
}

func TestRequiredWeight_DegenerateBucketAndClampDoNotAdmitBelowTheBaseline(t *testing.T) {
	b := era5Budget()

	assert.InDelta(t, float64(b.ValueClampR), requiredWeight(b, 5, 0), 1e-9,
		"a zero-capacity bucket is treated as maximally contended, not as free")
	assert.InDelta(t, baselineWeight, requiredWeight(b, 4, 8), 1e-9,
		"a baseline market IS admitted at the reserve boundary; if it were not, cold markets would be served only by the escape hatch")
	assert.GreaterOrEqual(t, requiredWeight(Budget{RateReqPerSec: 0.35, ValueClampR: 0}, 5, 10), baselineWeight,
		"a nonsense clamp below 1 must not produce a bar under the baseline")
	assert.InDelta(t, baselineWeight, requiredWeight(b, 999, 10), 1e-9, "over-full bucket clamps to full")
	assert.InDelta(t, float64(b.ValueClampR), requiredWeight(b, -5, 10), 1e-9, "negative tokens clamp to empty")
}

// -----------------------------------------------------------------------------
// Guarantee: no starvation.
// -----------------------------------------------------------------------------

// TestDecide_PastMaxStalenessAdmitsUnconditionallySoNoMarketStarves is the
// anti-starvation guarantee, and it is proved rather than argued: the two rules
// that could otherwise defer a market forever — an exhausted budget and a bar
// it can never clear — are both applied here at once, and the market is still
// admitted once it passes MaxStaleness.
//
// This is what keeps recovery detectable. A market we crushed by dumping into it
// recovers on its own schedule, and the only way we learn is to look; a cold
// market that could be deferred indefinitely would never be looked at again.
//
// MUTATION TARGET: deleting the `req.Staleness >= MaxStaleness(...)` branch in
// Decide must fail this test.
func TestDecide_PastMaxStalenessAdmitsUnconditionallySoNoMarketStarves(t *testing.T) {
	b := era5Budget()
	const marketsKnown = 355
	total := float64(marketsKnown) * float64(b.ValueClampR)

	starved := Request{
		Class:  Discretionary,
		Weight: baselineWeight, // the coldest market on the map
		// The map is at its maximum total weight, so this market's own interval
		// is as long as it can possibly be...
		TotalWeight:  total,
		MarketsKnown: marketsKnown,
		// ...and it has now waited past even that worst case.
		Staleness: MaxStaleness(b, marketsKnown) + time.Second,
		// Both deferral rules are active: no token, and the bar is at the clamp.
		TokensAvailable: 0,
		BucketCapacity:  10,
	}

	require.Greater(t, requiredWeight(b, starved.TokensAvailable, starved.BucketCapacity), starved.Weight,
		"fixture must be a market the value bar would reject")
	require.Less(t, starved.TokensAvailable, 1.0,
		"fixture must be a market the budget cap would reject")

	assert.Equal(t, Spend, Decide(b, starved),
		"past the worst-case bound a market is scanned no matter how cold or how contended the budget")
}

// TestMaxStaleness_IsFiniteAndBoundsEveryRealMarketsInterval checks the bound is
// sound, not merely typical: no admissible (weight, total) pair on a map of that
// size produces an interval longer than the bound.
func TestMaxStaleness_IsFiniteAndBoundsEveryRealMarketsInterval(t *testing.T) {
	b := era5Budget()

	for _, marketsKnown := range []int{1, 10, 355, 5000} {
		bound := MaxStaleness(b, marketsKnown)
		require.Less(t, bound, stalenessCeiling,
			"the worst case must be a real number, not the arithmetic ceiling, at %d markets", marketsKnown)

		// The worst real map: every market at the clamp, this one at the floor.
		worstTotal := float64(marketsKnown) * float64(b.ValueClampR)
		for _, weight := range []float64{baselineWeight, 2, float64(b.ValueClampR)} {
			assert.LessOrEqual(t, Interval(b, weight, worstTotal), bound,
				"weight %v at %d markets must be bounded by MaxStaleness", weight, marketsKnown)
		}
	}
}

func TestMaxStaleness_GrowsWithTheMapWhichIsThePriceOfAFixedBudget(t *testing.T) {
	b := era5Budget()

	assert.Greater(t, MaxStaleness(b, 1000), MaxStaleness(b, 100),
		"a fixed budget spread over more markets must mean a longer worst case, not a broken one")
}

func TestMaxStaleness_EmptyMapReturnsTheCeilingWithoutDividingByZero(t *testing.T) {
	require.NotPanics(t, func() {
		assert.Equal(t, stalenessCeiling, MaxStaleness(era5Budget(), 0))
		assert.Equal(t, stalenessCeiling, MaxStaleness(era5Budget(), -1))
	})
}

func TestDecide_NeverScannedMarketIsAlwaysAdmitted(t *testing.T) {
	b := era5Budget()

	req := Request{
		Class:           Discretionary,
		Weight:          baselineWeight,
		TotalWeight:     355 * float64(b.ValueClampR),
		MarketsKnown:    355,
		Staleness:       NeverScanned,
		TokensAvailable: 0, // fully exhausted budget
		BucketCapacity:  10,
	}

	assert.Equal(t, Spend, Decide(b, req),
		"a market with no cached row at all cannot be served from store, so it takes the escape")
}

// -----------------------------------------------------------------------------
// Guarantee: earning reads are metered but never denied.
// -----------------------------------------------------------------------------

// TestDecide_EarningReadIsAdmittedEvenWithTheBudgetFullyExhausted protects the
// money guards. liveBidForFloor and liveAskForCeiling fail CLOSED when their
// refresh does not happen, and staleAskAborts exists because a cached ask has
// realised large losses — declining any of them would either strand trading or
// silently downgrade a live guard to a stale one (RULINGS #4).
func TestDecide_EarningReadIsAdmittedEvenWithTheBudgetFullyExhausted(t *testing.T) {
	b := era5Budget()

	req := Request{
		Class:           Earning,
		Weight:          baselineWeight,
		TotalWeight:     355,
		MarketsKnown:    355,
		Staleness:       time.Millisecond, // just scanned; not remotely due
		TokensAvailable: 0,                // and nothing left in the budget
		BucketCapacity:  10,
	}

	assert.Equal(t, Spend, Decide(b, req),
		"a trade-critical live verification is never served from store")
}

func TestDecide_ZeroValueClassIsBudgetedSoAnUntaggedCallerCannotEscapeTheBudget(t *testing.T) {
	b := era5Budget()

	// Exactly the earning fixture above, with the class left at its zero value.
	req := Request{
		Weight:          baselineWeight,
		TotalWeight:     355,
		MarketsKnown:    355,
		Staleness:       time.Millisecond,
		TokensAvailable: 0,
		BucketCapacity:  10,
	}

	require.Equal(t, Discretionary, req.Class, "the zero value must be the budgeted class")
	assert.Equal(t, ServeFromStore, Decide(b, req),
		"forgetting to classify a call site must make it paced, never unmetered")
}

func TestDecide_ZeroValueDecisionSpendsNothing(t *testing.T) {
	var d Decision
	assert.Equal(t, ServeFromStore, d, "an unset decision must not authorise a request")
}

// -----------------------------------------------------------------------------
// Weighting forwarders.
// -----------------------------------------------------------------------------

func TestWeight_TracksTheParkedRotationsClampedSpreadRatio(t *testing.T) {
	assert.InDelta(t, 3.0, Weight(30, 10, 8), 1e-9, "3x the fleet median earns 3x the attention")
	assert.InDelta(t, 8.0, Weight(800, 10, 8), 1e-9, "clamped at ValueClampR")
	assert.InDelta(t, 1.0, Weight(1, 10, 8), 1e-9, "floored at the baseline so nothing leaves the rotation")
	assert.InDelta(t, 1.0, Weight(30, 10, 1), 1e-9, "clamp 1 flattens the weighting entirely")
}

func TestPriorWeight_PutsAnUnmeasuredMarketAheadOfAMeasuredDullOne(t *testing.T) {
	assert.Greater(t, PriorWeight(8), Weight(1, 10, 8),
		"a market we have never measured — including a newly charted one — outranks a dull measured one")
	assert.LessOrEqual(t, PriorWeight(1), 1.0, "and the prior is flattened by clamp 1 along with everything else")
}

func TestUpdateSpread_AdoptsTheFirstObservationThenSmooths(t *testing.T) {
	first := UpdateSpread(0, 40)
	assert.InDelta(t, 40.0, first, 1e-9, "no history means adopt outright rather than blend toward a zero never measured")

	assert.InDelta(t, 0.3*80+0.7*40, UpdateSpread(first, 80), 1e-9)
}

// -----------------------------------------------------------------------------
// Paired measurement: exempt from the freshness veto, and nothing else.
// -----------------------------------------------------------------------------

// TestDecide_PairedReadIsNotVetoedByAFreshCache protects the refit
// corpus. The "after" half of a scan-buy-scan pair arrives moments after the
// "before" half by construction, so judging it on cache freshness would decline
// every impact measurement the price model is fitted from.
func TestDecide_PairedReadIsNotVetoedByAFreshCache(t *testing.T) {
	b := era5Budget()

	req := Request{
		Class:           Paired,
		Weight:          baselineWeight,
		TotalWeight:     355,
		MarketsKnown:    355,
		Staleness:       2 * time.Second, // the "before" scan was seconds ago
		TokensAvailable: 8,
		BucketCapacity:  8,
	}

	require.Less(t, req.Staleness, Interval(b, req.Weight, req.TotalWeight),
		"fixture must be a market the freshness veto would otherwise decline")
	assert.Equal(t, Spend, Decide(b, req),
		"the after half of an impact pair is not redundant for arriving soon after the before half")

	// The same request as an ordinary discretionary read IS declined — which is
	// what makes the exemption load-bearing rather than decorative.
	req.Class = Discretionary
	assert.Equal(t, ServeFromStore, Decide(b, req))
}

func TestDecide_PairedReadStillObeysTheBudgetCapAndTheValueBar(t *testing.T) {
	b := era5Budget()

	base := Request{
		Class:          Paired,
		Weight:         baselineWeight,
		TotalWeight:    355,
		MarketsKnown:   355,
		Staleness:      2 * time.Second,
		BucketCapacity: 8,
	}

	exhausted := base
	exhausted.TokensAvailable = 0.5
	assert.Equal(t, ServeFromStore, Decide(b, exhausted),
		"instrumentation is what a budget under pressure sheds first: no token, no measurement")

	contended := base
	contended.TokensAvailable = 1.2 // a whole token, but the bar is near the clamp
	require.Greater(t, requiredWeight(b, contended.TokensAvailable, contended.BucketCapacity), contended.Weight)
	assert.Equal(t, ServeFromStore, Decide(b, contended),
		"a paired read on a dull market yields the scarce token like any other")
}

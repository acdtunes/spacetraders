package ship

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
)

// market_scan_budget.go is the stateful half of the fleet's ONE market-scan
// budget. The policy — how long a market of a given value waits, what the
// worst-case staleness is, and whether a particular read is admitted — is pure
// and lives in internal/domain/marketscan. What lives here is the state that
// policy reads: which markets are known, how wide each one's spread has been,
// and how much of the allowance is unspent right now.
//
// WHY THE GATE SITS INSIDE MarketScanner. Every market read in the daemon that
// costs an API request funnels through exactly one function pair:
// APIClient.GetMarket, reached either from MarketScanner.ScanAndSaveMarket or
// from the parked screener's own client call. A dozen call paths across
// trading, manufacturing, contracts, scouting and charting reach the scanner,
// and each was individually unpaced — which is how total market reads measured
// 0.644 req/s against a budgeted scanner that was honouring 0.100. Gating the
// choke point means every one of those callers draws from this budget without
// any of them having to know it exists, and a caller added tomorrow is paced by
// default rather than by remembering to ask.

// defaultBudgetReqPerSec is the fleet's market-scan allowance.
//
// Sized from the era-5 measurement this budget was written for: total market
// reads were 0.644 req/s of a 2.00 req/s server ceiling that cannot be raised,
// against an operating point where lifting shipyard reads out of the way
// already took total daemon traffic to 1.46 req/s. 0.35 leaves market sensing a
// meaningful share of the ceiling while cutting its measured draw by roughly
// 46%, and — because the interval widens with the map — it is the number that
// stops mattering how large the map gets.
const defaultBudgetReqPerSec = 0.35

// defaultValueClampR is how much more attention the hottest market may earn
// than the coldest. It matches the parked rotation's own value_clamp_r default
// so both allocate attention on the same scale, and it doubles as the
// anti-starvation constant: no market waits longer than this multiple of a
// market's mean interval.
const defaultValueClampR = 8

// burstRequests is the depth of the allowance bucket.
//
// It is the unspent-token headroom that absorbs arrival bunching — several
// hulls docking at once, or a coordinator sweep touching a handful of markets
// in one tick — without letting a quiet spell bank minutes of spend into a
// later burst that would breach the server ceiling. At the default budget it is
// about 23 seconds of allowance.
//
// It is also the scale the priority ordering is expressed over: the admission
// bar rises from the baseline at a full bucket to the clamp at an empty one, so
// the bucket depth sets how quickly contention starts favouring the valuable
// markets.
const burstRequests = 8

// chartedCountTTL is how long a map-size reading is reused before the counter is
// consulted again. The count only moves when charting finds new markets, so a
// few minutes of staleness costs nothing, and re-counting on every admission
// would put a database query on the scan path.
const chartedCountTTL = 5 * time.Minute

// ChartedMarketCounter reports how many market waypoints the player has
// charted — the denominator of "budget ÷ markets known".
//
// It is a narrow optional port rather than a widening of the scanner's market
// repository interface, because that interface is implemented by fakes in
// dozens of tests that have no business knowing the map size. The production
// repository already satisfies this shape, so ScanBudget discovers it by type
// assertion; a store that does not satisfy it falls back to counting the
// markets the gate has itself been asked about, which is a lower bound on the
// map and therefore paces slightly LOOSER but never leaves the budget
// unenforced.
type ChartedMarketCounter interface {
	ChartedMarketSystemCounts(ctx context.Context) (map[string]int, error)
}

// ScanBudget admits or declines market reads against one fixed allowance.
//
// It is safe for concurrent use: every coordinator container in the daemon
// shares one instance, which is the point — a budget that were per-container
// would multiply by the container count and stop being a budget.
type ScanBudget struct {
	mu      sync.Mutex
	policy  marketscan.Budget
	limiter *rate.Limiter
	now     func() time.Time

	// spread holds the smoothed relative bid-ask spread of every market the gate
	// has seen a scan of. Absence means "known but never measured", which is
	// weighted at the optimistic prior rather than at zero.
	spread map[string]float64

	// seen is every market the gate has been asked about. It is the fallback map
	// size when no charted-market counter is wired, and it is unioned with the
	// charted count so a market nobody has charted yet still gets a share.
	seen map[string]struct{}

	counter   ChartedMarketCounter
	charted   int
	chartedAt time.Time

	// aggregate caches the fleet median and total weight, which change only when
	// a spread observation lands or the map size moves.
	aggregateStale bool
	median         float64
	totalWeight    float64

	admitted uint64
	declined uint64
	forced   uint64
}

// NewScanBudget returns a budget enforcing rateReqPerSec across every market
// read, weighting attention up to clampR-fold by observed spread.
//
// Non-positive or nonsensical arguments resolve to the defaults rather than
// disabling the budget: there is deliberately no argument that turns pacing
// off, because an unpaced scanner is the defect this type exists to fix.
func NewScanBudget(rateReqPerSec float64, clampR int) *ScanBudget {
	if rateReqPerSec <= 0 {
		rateReqPerSec = defaultBudgetReqPerSec
	}
	if clampR < 1 {
		clampR = defaultValueClampR
	}
	return &ScanBudget{
		policy:         marketscan.Budget{RateReqPerSec: rateReqPerSec, ValueClampR: clampR},
		limiter:        rate.NewLimiter(rate.Limit(rateReqPerSec), burstRequests),
		now:            time.Now,
		spread:         make(map[string]float64),
		seen:           make(map[string]struct{}),
		aggregateStale: true,
	}
}

// SetChartedMarketCounter wires the map-size source. Passing nil leaves the
// fallback (markets the gate has seen) in place rather than disabling pacing.
func (b *ScanBudget) SetChartedMarketCounter(c ChartedMarketCounter) {
	if c == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.counter = c
}

// setClock replaces the clock for tests. Unexported: production always runs on
// the real one, and a budget whose clock could be swapped from outside the
// package would be a budget that could be stopped from outside the package.
func (b *ScanBudget) setClock(now func() time.Time) {
	if now == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = now
}

// Admit decides whether this market read may spend a request, and consumes the
// request from the allowance when it may.
//
// cached is the market row the caller already holds — the scanner reads it
// anyway to diff price history, so the freshness test and the value weight cost
// no extra query. A nil cached market means never scanned, which the policy
// treats as infinitely stale and always admits.
//
// A forced read (an Earning-class money-guard verification, or a market past
// the anti-starvation bound) still DEBITS the allowance even when the bucket is
// already empty. The overdraft is the mechanism, not a leak: an emptied bucket
// declines discretionary reads until it refills, so a burst of trade-critical
// verification squeezes discretionary scanning instead of being added on top of
// it. That is what keeps this one budget the honest total.
//
// playerID is carried for ATTRIBUTION ONLY — it labels the emitted decision so
// an operator can see whose reads spent the allowance. It does NOT partition the
// budget: there is one bucket, one map size and one rate across the whole daemon,
// which is the property that makes it a budget at all.
func (b *ScanBudget) Admit(ctx context.Context, playerID int, waypoint string, cached *market.Market, class marketscan.Class) marketscan.Decision {
	b.refreshChartedCount(ctx)

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.seen[waypoint]; !ok {
		b.seen[waypoint] = struct{}{}
		b.aggregateStale = true
	}

	now := b.now()
	median, totalWeight, marketsKnown := b.aggregateLocked()

	decision := marketscan.Decide(b.policy, marketscan.Request{
		Class:           class,
		Staleness:       stalenessOf(cached, now),
		Weight:          b.weightLocked(waypoint, median),
		TotalWeight:     totalWeight,
		MarketsKnown:    marketsKnown,
		TokensAvailable: b.limiter.TokensAt(now),
		BucketCapacity:  burstRequests,
	})

	// Emitted from INSIDE the lock, on values this call just re-derived, so the
	// published rate and coverage describe the same budget state the decision was
	// taken against (RULING #2: nothing is held between ticks to be reconciled).
	// Recording is best-effort and cannot alter the decision below.
	recordScanBudgetDecision(playerID, metrics.BudgetMarket, class, decision, b.policy.RateReqPerSec, marketsKnown)

	if decision != marketscan.Spend {
		b.declined++
		return decision
	}

	// AllowN both tests and consumes. Its result is deliberately ignored: a
	// decision to spend has already been made by the policy, and when the bucket
	// is empty this is a forced read whose debit still has to land.
	if !b.limiter.AllowN(now, 1) {
		b.forced++
		recordScanBudgetOverdraft(playerID, metrics.BudgetMarket, class)
	}
	b.admitted++
	return marketscan.Spend
}

// Debit charges one market read to the allowance without offering it the chance
// to be declined.
//
// It exists for the ONE market API read in the daemon that cannot go through
// Admit: the parked screen's catalogue gap fill, which asks a
// charted-but-never-visited market what it DEALS IN. That read has no store to
// fall back on — filling the gap is its whole purpose — and declining it makes
// the screen record a durable rejection of a market it never managed to look at,
// which is worse than the request it saves. It is also bounded by the rate of
// CHARTING rather than by the size of the map, so admitting it unconditionally
// does not put the fixed-budget invariant at risk.
//
// Metering it anyway is what keeps the budget the honest total: the catalogue
// read consumes allowance that discretionary scanning then cannot, so every
// market request in the daemon is attributable to this one number even though not
// every one of them is deniable.
//
// IT REPORTS AS EARNING, and that is the honest label rather than a convenience.
// Earning is this vocabulary's word for "metered but never denied", which is
// exactly what a Debit is — the class names deniability, not motive. Reporting it
// anywhere else would break the one property the counter has to have: that
// decision="spend" is every market request the daemon issues, so it reconciles
// against api_requests_total.
func (b *ScanBudget) Debit(playerID int, waypoint string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.seen[waypoint]; !ok {
		b.seen[waypoint] = struct{}{}
		b.aggregateStale = true
	}
	_, _, marketsKnown := b.aggregateLocked()
	recordScanBudgetDecision(playerID, metrics.BudgetMarket, marketscan.Earning, marketscan.Spend, b.policy.RateReqPerSec, marketsKnown)
	if !b.limiter.AllowN(b.now(), 1) {
		b.forced++
		recordScanBudgetOverdraft(playerID, metrics.BudgetMarket, marketscan.Earning)
	}
	b.admitted++
}

// Observe folds a freshly scanned market's prices into its value estimate, so
// the next rotation weights it on what it is quoting now rather than on what it
// quoted when it was first seen.
//
// Inverted quotes are dropped by SpreadOf rather than counted as narrow ones;
// see marketscan.Quote for why that distinction is load-bearing.
func (b *ScanBudget) Observe(waypoint string, goods []market.TradeGood) {
	if len(goods) == 0 {
		return
	}

	quotes := make([]marketscan.Quote, 0, len(goods))
	for _, g := range goods {
		// Bid is what the market PAYS us, which is the persisted sell_price;
		// Ask is what it CHARGES us, the persisted purchase_price (sp-en5h7).
		quotes = append(quotes, marketscan.Quote{Bid: g.SellPrice(), Ask: g.PurchasePrice()})
	}
	observed, _ := marketscan.SpreadOf(quotes)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.spread[waypoint] = marketscan.UpdateSpread(b.spread[waypoint], observed)
	b.seen[waypoint] = struct{}{}
	b.aggregateStale = true
}

// BudgetSnapshot is a point-in-time read of the budget, for metrics and the
// operator-facing report.
type BudgetSnapshot struct {
	RateReqPerSec   float64
	ValueClampR     int
	MarketsKnown    int
	MarketsMeasured int
	TotalWeight     float64
	MedianSpread    float64
	TokensAvailable float64
	// TypicalInterval is how long a baseline-value market currently waits
	// between scans — the number that grows as the map grows while the rate
	// stays put, and the most direct read on whether the invariant holds.
	TypicalInterval time.Duration
	// WorstCaseStaleness is the anti-starvation bound at the current map size.
	WorstCaseStaleness time.Duration
	Admitted           uint64
	Declined           uint64
	// Forced counts admitted reads that found the bucket already empty: money
	// guards and starvation escapes. A persistently high count means the fixed
	// budget is smaller than the fleet's unavoidable trade-critical verification
	// and should be raised, rather than the guards being weakened.
	Forced uint64
}

// Snapshot reports the budget's current state.
func (b *ScanBudget) Snapshot() BudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	median, totalWeight, marketsKnown := b.aggregateLocked()
	return BudgetSnapshot{
		RateReqPerSec:      b.policy.RateReqPerSec,
		ValueClampR:        b.policy.ValueClampR,
		MarketsKnown:       marketsKnown,
		MarketsMeasured:    len(b.spread),
		TotalWeight:        totalWeight,
		MedianSpread:       median,
		TokensAvailable:    b.limiter.TokensAt(b.now()),
		TypicalInterval:    marketscan.Interval(b.policy, 1, totalWeight),
		WorstCaseStaleness: marketscan.MaxStaleness(b.policy, marketsKnown),
		Admitted:           b.admitted,
		Declined:           b.declined,
		Forced:             b.forced,
	}
}

// RotationInputs reports the allowance and the live map size behind it — the two
// numbers marketscan.FreshnessCap needs to say how old a cached market row may be
// before it is older than this rotation can explain.
//
// It is a LIVE read, not a launch value. The map size is the same denominator
// admission is decided against (re-counted on the charted-count TTL), so a
// consumer derived from it widens on its own as charting finds markets and
// narrows again if the allowance is raised — which is the whole point: a fixed
// minute count chosen at one map size is wrong at every later one.
//
// It takes a context because it refreshes the map size on the same TTL Admit does,
// and that matters at BOOT: until something has counted the map, marketsKnown is
// zero and every derived cap collapses to its floor — which is the 75-minute
// behaviour this whole change exists to remove. Refreshing here means the first
// freshness question asked after a daemon restart is answered against the real map
// rather than against an empty one.
//
// A nil budget (no scanner wired — every test that never builds one) reports a
// zero map, which FreshnessCap answers with the caller's own floor rather than
// with a widened cap.
func (b *ScanBudget) RotationInputs(ctx context.Context) (marketscan.Budget, int) {
	if b == nil {
		return marketscan.Budget{}, 0
	}
	b.refreshChartedCount(ctx)

	b.mu.Lock()
	defer b.mu.Unlock()
	_, _, marketsKnown := b.aggregateLocked()
	return b.policy, marketsKnown
}

// aggregateLocked returns the fleet median spread, the summed weight of every
// known market and the map size, recomputing them only when an observation or a
// new market has invalidated the cache.
//
// The total counts UNMEASURED markets at the optimistic prior, not at zero.
// Omitting them would understate the denominator and hand every measured market
// a shorter interval than the budget can fund — the map would be paced as
// though the markets nobody has scanned yet did not have to be scanned.
func (b *ScanBudget) aggregateLocked() (median, totalWeight float64, marketsKnown int) {
	known := len(b.seen)
	if b.charted > known {
		known = b.charted
	}

	if !b.aggregateStale {
		return b.median, b.totalWeight, known
	}

	spreads := make([]float64, 0, len(b.spread))
	for _, s := range b.spread {
		spreads = append(spreads, s)
	}
	b.median = marketscan.FleetMedianSpread(spreads)

	total := 0.0
	measured := 0
	for _, s := range b.spread {
		if s > 0 {
			total += marketscan.Weight(s, b.median, b.policy.ValueClampR)
			measured++
		}
	}
	if unmeasured := known - measured; unmeasured > 0 {
		total += float64(unmeasured) * marketscan.PriorWeight(b.policy.ValueClampR)
	}
	b.totalWeight = total
	b.aggregateStale = false

	return b.median, b.totalWeight, known
}

// weightLocked is one market's current value weight, or the optimistic prior
// when it has never been measured.
func (b *ScanBudget) weightLocked(waypoint string, median float64) float64 {
	if s, ok := b.spread[waypoint]; ok && s > 0 {
		return marketscan.Weight(s, median, b.policy.ValueClampR)
	}
	return marketscan.PriorWeight(b.policy.ValueClampR)
}

// refreshChartedCount re-reads the map size when the cached reading has aged
// out. It runs OUTSIDE the budget's mutex so a slow database read cannot block
// every other container's admission, and a failed read leaves the previous
// count in place — a counter hiccup must widen nothing and must not reset the
// denominator to zero, which would collapse every interval.
func (b *ScanBudget) refreshChartedCount(ctx context.Context) {
	b.mu.Lock()
	counter := b.counter
	due := b.chartedAt.IsZero() || b.now().Sub(b.chartedAt) >= chartedCountTTL
	b.mu.Unlock()

	if counter == nil || !due {
		return
	}

	counts, err := counter.ChartedMarketSystemCounts(ctx)

	b.mu.Lock()
	defer b.mu.Unlock()
	// Stamp the attempt either way, so a persistently failing counter is retried
	// on the TTL rather than on every single admission.
	b.chartedAt = b.now()
	if err != nil {
		return
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	if total != b.charted {
		b.charted = total
		b.aggregateStale = true
	}
}

// stalenessOf is how long ago a cached market row was written. A market that was
// never scanned, or whose row carries no usable timestamp, is infinitely stale:
// there is nothing in the store to serve, so it must be admitted rather than
// declined into permanent darkness.
func stalenessOf(cached *market.Market, now time.Time) time.Duration {
	if cached == nil {
		return marketscan.NeverScanned
	}
	observed := cached.LastUpdated()
	if observed.IsZero() {
		return marketscan.NeverScanned
	}
	age := now.Sub(observed)
	if age < 0 {
		// A row stamped in the future is bad data, not a fresh reading. Treating
		// it as fresh would let one bad timestamp mute a market indefinitely.
		return marketscan.NeverScanned
	}
	return age
}

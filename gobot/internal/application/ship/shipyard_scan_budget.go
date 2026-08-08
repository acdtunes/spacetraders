package ship

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// YardScanBudget is the stateful half of the fleet's ONE shipyard-read budget,
// the sibling of market_scan_budget.go. The admission arithmetic is NOT
// duplicated here: Interval, MaxStaleness, the anti-starvation escape and the
// Earning/Discretionary classes come from internal/domain/marketscan, and the
// value term from internal/domain/yardscan. What lives here is the state both
// read: which yards are known, what the fleet is shopping for, which yards sell
// it, and how much allowance is unspent.
//
// Allocation is by DEMAND (yardscan.Weight), never uniform rationing: a cadence
// floor can slow every yard down but cannot prioritise the ones a buy loop is
// about to spend millions at. quartermaster_cadence_secs stays a floor the
// budget may not scan past, never the thing that sets the rate.

// The shipyard-read allowance, ~6% of the 2.00 req/s ceiling; constant in map size,
// so a bigger map lengthens intervals rather than traffic. The MILLI form is for
// consumers that DERIVE a cadence from it rather than merely drawing on it.
const (
	defaultYardBudgetReqPerSec = 0.12
	YardBudgetMilliReqPerSec   = int(defaultYardBudgetReqPerSec * 1000)
)

// defaultYardValueClampR is how much more attention the most valuable yard may
// earn than a yard selling nothing we want. It matches the market budget's clamp
// so both allowances express priority on one scale, and it doubles as the
// anti-starvation constant: no known yard waits longer than this multiple of a
// baseline yard's mean interval.
const defaultYardValueClampR = 8

// yardBurstRequests is the depth of the allowance bucket — the headroom that
// absorbs arrival bunching (a tour touching several yards in one tick, or a buy
// loop verifying a price at three candidates) without letting a quiet spell bank
// minutes of spend into a later burst. At the default rate it is about 67 seconds
// of allowance. It is also the scale the priority bar is expressed over: the bar
// climbs from baseline at a full bucket to the clamp at an empty one.
const yardBurstRequests = 8

// YardScanBudget admits or declines shipyard reads against one fixed allowance.
//
// It is safe for concurrent use: every coordinator container in the daemon shares
// one instance, which is the point — a per-container budget would multiply by the
// container count and stop being a budget.
type YardScanBudget struct {
	mu      sync.Mutex
	policy  marketscan.Budget
	limiter *rate.Limiter
	now     func() time.Time

	// seen is every yard the budget has been asked about: the fallback map size
	// when no charted counter is wired, unioned with the charted count so a yard
	// nobody has charted yet still gets a share.
	seen map[string]struct{}

	counter   ChartedYardCounter
	charted   int
	chartedAt time.Time

	catalog     YardCatalogReader
	catalogAt   time.Time
	lastPlayer  int
	catalogHeld bool

	// heavy is the STRUCTURAL half of demand: the hull classes the fleet's
	// acquisition path exists to buy. It is wanted whether or not anything has
	// shopped for it recently, so a cold daemon still prioritises heavy yards
	// before any buy loop has spoken.
	heavy shipyard.HeavyShipTypeSet

	// demand is the OBSERVED half: hull types something has priced or searched for
	// recently, with their last-seen stamp. Together with heavy it forms the wanted
	// set every yard is weighted against.
	demand map[string]time.Time

	// target is the yards a money guard has priced at recently — "we are buying
	// HERE", the strongest signal the weighting has.
	target map[string]time.Time

	// facts is the per-yard demand picture: whether a yard's catalogue holds a
	// wanted type and whether we hold a price for it. Populated from live scans via
	// Observe and refreshed from the store on yardFactsTTL. A yard absent from this
	// map has never been looked at and is weighted at the optimistic prior.
	facts map[string]yardscan.Facts

	// heavySeller names the yards whose catalogue holds one of the hull classes
	// the acquisition path exists to buy.
	//
	// Kept BESIDE facts rather than inside it, because the two answer different
	// questions and Facts is the READ budget's vocabulary. Weight deliberately
	// does not distinguish a heavy seller from any other confirmed unpriced
	// seller — for a one-request read they are worth the same — while a hull move
	// is scarce enough that the distinction is the whole decision. Adding the bit
	// to Facts would have leaked a mover's concern into the rotation's ranking.
	heavySeller map[string]bool

	// presence meters how fast hull repositions may be STARTED. Separate from
	// limiter because it paces a different resource: limiter rations this
	// package's own requests, presence rations navigation the placement machine
	// will issue later on this budget's behalf.
	presence *rate.Limiter

	aggregateStale bool
	totalWeight    float64

	admitted uint64
	declined uint64
	forced   uint64

	presenceIssued   uint64
	presenceDeclined uint64
}

// NewYardScanBudget returns a budget enforcing rateReqPerSec across every shipyard
// read, weighting attention up to clampR-fold by demand.
//
// Non-positive or nonsensical arguments resolve to the defaults rather than
// disabling the budget: there is deliberately no argument that turns pacing off,
// because an unpaced shipyard reader is the defect this type exists to fix.
func NewYardScanBudget(rateReqPerSec float64, clampR int, heavy shipyard.HeavyShipTypeSet) *YardScanBudget {
	if rateReqPerSec <= 0 {
		rateReqPerSec = defaultYardBudgetReqPerSec
	}
	if clampR < 1 {
		clampR = defaultYardValueClampR
	}
	return &YardScanBudget{
		policy:  marketscan.Budget{RateReqPerSec: rateReqPerSec, ValueClampR: clampR},
		limiter: rate.NewLimiter(rate.Limit(rateReqPerSec), yardBurstRequests),
		// The reposition allowance is NOT derived from rateReqPerSec. The read rate
		// is sized against how much of the map has to be kept fresh; the reposition
		// rate is sized against the API headroom a hull move consumes downstream.
		// Tying them would make a decision to scan the map harder silently authorise
		// pulling hulls off their markets faster.
		presence:       rate.NewLimiter(rate.Limit(defaultYardPresenceReqPerSec), yardPresenceBurst),
		now:            time.Now,
		seen:           make(map[string]struct{}),
		heavy:          heavy,
		demand:         make(map[string]time.Time),
		target:         make(map[string]time.Time),
		facts:          make(map[string]yardscan.Facts),
		heavySeller:    make(map[string]bool),
		aggregateStale: true,
	}
}

// SetChartedYardCounter wires the map-size source. Passing nil leaves the fallback
// (yards the budget has seen) in place rather than disabling pacing.
func (b *YardScanBudget) SetChartedYardCounter(c ChartedYardCounter) {
	if c == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.counter = c
}

// SetYardCatalogReader wires the store the demand picture is rebuilt from after a
// restart. Passing nil leaves the budget on live observations alone.
func (b *YardScanBudget) SetYardCatalogReader(c YardCatalogReader) {
	if c == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.catalog = c
}

// setClock replaces the clock for tests. Unexported: production always runs on the
// real one, and a budget whose clock could be swapped from outside the package
// would be a budget that could be stopped from outside the package.
func (b *YardScanBudget) setClock(now func() time.Time) {
	if now == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = now
}

// Admit decides whether this shipyard read may spend a request, and consumes the
// request from the allowance when it may.
//
// lastScanned/known are the yard's persisted scan stamp, which the scanner reads
// anyway for its own recency window — so the freshness test costs no extra query.
// An unknown or unusable stamp reads as never scanned, which the policy treats as
// infinitely stale and always admits.
//
// A forced read (an Earning-class money-guard verification, or a yard past the
// anti-starvation bound) still DEBITS the allowance even when the bucket is empty.
// The overdraft is the mechanism, not a leak: an emptied bucket declines
// discretionary reads until it refills, so a burst of trade-critical verification
// squeezes discretionary scanning instead of being added on top of it. That is
// what keeps this one number the honest total.
func (b *YardScanBudget) Admit(ctx context.Context, playerID int, waypoint string, lastScanned time.Time, known bool, class marketscan.Class) marketscan.Decision {
	b.refreshChartedCount(ctx)
	b.refreshFacts(ctx, playerID)

	b.mu.Lock()
	defer b.mu.Unlock()

	b.lastPlayer = playerID
	if _, ok := b.seen[waypoint]; !ok {
		b.seen[waypoint] = struct{}{}
		b.aggregateStale = true
	}

	now := b.now()
	totalWeight, yardsKnown := b.aggregateLocked()

	decision := marketscan.Decide(b.policy, marketscan.Request{
		Class:           class,
		Staleness:       yardStalenessOf(lastScanned, known, now),
		Weight:          b.weightLocked(waypoint, now),
		TotalWeight:     totalWeight,
		MarketsKnown:    yardsKnown,
		TokensAvailable: b.limiter.TokensAt(now),
		BucketCapacity:  yardBurstRequests,
	})

	// Emitted from INSIDE the lock, on values this call just re-derived, so the
	// published rate and coverage describe the same budget state the decision was
	// taken against (RULING #2). Recording is best-effort and cannot alter the
	// decision below. THIS COUNTER IS THE ONE THAT MAKES THE OVERDRAFT VISIBLE:
	// yard reads ran at 3.2x this allowance with nothing counting the breach.
	recordScanBudgetDecision(playerID, metrics.BudgetShipyard, class, decision, b.policy.RateReqPerSec, yardsKnown)

	if decision != marketscan.Spend {
		b.declined++
		return decision
	}

	// AllowN both tests and consumes. Its result is deliberately ignored: the
	// policy has already decided to spend, and when the bucket is empty this is a
	// forced read whose debit still has to land.
	if !b.limiter.AllowN(now, 1) {
		b.forced++
		recordScanBudgetOverdraft(playerID, metrics.BudgetShipyard, class)
	}
	b.admitted++
	return marketscan.Spend
}

// Debit charges one shipyard read to the allowance without offering it the chance
// to be declined. It exists for a read that must be metered but that no store can
// answer; metering it keeps every shipyard request in the daemon attributable to
// this one number even though not every one of them is deniable.
//
// IT REPORTS AS EARNING for the same reason the market budget's Debit does: the
// class names DENIABILITY, not motive, and "metered but never denied" is exactly
// what Earning means here. Keeping it in the same counter is what lets
// decision="spend" stand for every shipyard request the daemon issues.
func (b *YardScanBudget) Debit(playerID int, waypoint string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.seen[waypoint]; !ok {
		b.seen[waypoint] = struct{}{}
		b.aggregateStale = true
	}
	_, yardsKnown := b.aggregateLocked()
	recordScanBudgetDecision(playerID, metrics.BudgetShipyard, marketscan.Earning, marketscan.Spend, b.policy.RateReqPerSec, yardsKnown)
	if !b.limiter.AllowN(b.now(), 1) {
		b.forced++
		recordScanBudgetOverdraft(playerID, metrics.BudgetShipyard, marketscan.Earning)
	}
	b.admitted++
}

// yardStalenessOf is how long ago a yard's rows were written. A yard never
// scanned, or whose stamp is unusable, is infinitely stale: there is nothing in
// the store to serve, so it must be admitted rather than declined into permanent
// darkness.
func yardStalenessOf(lastScanned time.Time, known bool, now time.Time) time.Duration {
	if !known || lastScanned.IsZero() {
		return marketscan.NeverScanned
	}
	age := now.Sub(lastScanned)
	if age < 0 {
		// A row stamped in the future is bad data, not a fresh reading. Treating it
		// as fresh would let one bad timestamp mute a yard indefinitely.
		return marketscan.NeverScanned
	}
	return age
}

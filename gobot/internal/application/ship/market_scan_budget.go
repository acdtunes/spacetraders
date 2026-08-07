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

// demandWindow is how long a market keeps its share of the allowance after the
// last time a caller asked about it.
//
// IT IS THE DENOMINATOR'S WHOLE DEFINITION, so it is worth being precise about
// what it selects. A market read returns prices only with a hull standing on the
// waypoint — with no ship there the quote sheet comes back empty (see
// internal/adapters/api/market_dto.go) — so a market nobody is standing on
// cannot consume a scan at all. The gate cannot see hulls, but it does not need
// to: the ONLY way a scan happens is that some caller asks, so the markets being
// asked about are exactly the markets able to spend. Everything else holds a
// share of a rate it can never draw.
//
// An hour because that is the parked rotation's own intervalCap: it never lets a
// slot wait longer, so a market the rotation is serving is asked about at least
// hourly, and one that has gone a whole window unasked is being served by
// nothing. The failure direction is benign — a window too short undercounts the
// denominator, which paces LOOSER, and the token bucket is still the hard cap.
const demandWindow = time.Hour

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
	//
	// It is deliberately NOT pruned with the demand set. A spread is a learned
	// estimate of a place, not a claim that anyone is standing there, so a hull
	// returning to a market it measured yesterday resumes on what it learned
	// instead of paying to re-learn it.
	spread map[string]float64

	// demand maps each market to the last time a caller asked about it. Its live
	// entries ARE the denominator — see demandWindow.
	demand map[string]time.Time

	// aggregate caches the fleet median and total weight, which change only when
	// a spread observation lands or the demand set moves.
	aggregateStale bool
	median         float64
	totalWeight    float64

	// spendsSinceDiscretionary is the discretionary floor's only state: how many
	// reads have been admitted since the last discretionary one.
	spendsSinceDiscretionary int

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
		demand:         make(map[string]time.Time),
		aggregateStale: true,
	}
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
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	// Asking IS the demand signal, so the stamp lands whatever the decision turns
	// out to be. A market whose reads are all declined is still a market a hull is
	// standing on, and dropping it from the denominator the moment the budget
	// declined it would shorten the interval that caused the decline — a feedback
	// loop between the rate and its own denominator.
	b.noteDemandLocked(waypoint, now)
	median, totalWeight, marketsKnown := b.aggregateLocked(now)

	decision := marketscan.Decide(b.policy, marketscan.Request{
		Class:                    class,
		Staleness:                stalenessOf(cached, now),
		Weight:                   b.weightLocked(waypoint, median),
		TotalWeight:              totalWeight,
		MarketsKnown:             marketsKnown,
		TokensAvailable:          b.limiter.TokensAt(now),
		BucketCapacity:           burstRequests,
		SpendsSinceDiscretionary: b.spendsSinceDiscretionary,
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
	b.noteSpendLocked(class)
	return marketscan.Spend
}

// noteSpendLocked advances the discretionary floor's counter. Every admitted read
// of any class raises it; a discretionary one clears it, because the floor asks
// how long the class has gone unserved, not how much it has been served.
func (b *ScanBudget) noteSpendLocked(class marketscan.Class) {
	if class == marketscan.Discretionary {
		b.spendsSinceDiscretionary = 0
		return
	}
	b.spendsSinceDiscretionary++
}

// noteDemandLocked records that a caller has just asked about this market, which
// is what puts it in the denominator for the next demandWindow.
func (b *ScanBudget) noteDemandLocked(waypoint string, now time.Time) {
	if _, ok := b.demand[waypoint]; !ok {
		b.aggregateStale = true
	}
	b.demand[waypoint] = now
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

	now := b.now()
	b.noteDemandLocked(waypoint, now)
	_, _, marketsKnown := b.aggregateLocked(now)
	recordScanBudgetDecision(playerID, metrics.BudgetMarket, marketscan.Earning, marketscan.Spend, b.policy.RateReqPerSec, marketsKnown)
	if !b.limiter.AllowN(now, 1) {
		b.forced++
		recordScanBudgetOverdraft(playerID, metrics.BudgetMarket, marketscan.Earning)
	}
	b.admitted++
	b.noteSpendLocked(marketscan.Earning)
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
		// Ask is what it CHARGES us, the persisted purchase_price.
		quotes = append(quotes, marketscan.Quote{Bid: g.SellPrice(), Ask: g.PurchasePrice()})
	}
	observed, _ := marketscan.SpreadOf(quotes)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.spread[waypoint] = marketscan.UpdateSpread(b.spread[waypoint], observed)
	b.noteDemandLocked(waypoint, b.now())
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

	median, totalWeight, marketsKnown := b.aggregateLocked(b.now())
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

// RotationInputs reports the allowance and the live rotation size behind it — the
// two numbers marketscan.FreshnessCap needs to say how old a cached market row may
// be before it is older than this rotation can explain.
//
// It is a LIVE read, not a launch value, and it is THE SAME denominator admission
// is decided against — not a second count of the same thing. That identity is the
// point rather than an implementation detail: when the two were derived
// separately they disagreed by orders of magnitude, and a consumer reading the
// smaller one refused rows the scanner believed it was still explaining.
//
// The context is unused and kept because it is the shape every consumer of this
// port already holds; the rotation size is now in-process state rather than a
// database count, so there is nothing left here that can fail, time out, or serve
// an operator a number from five minutes ago.
//
// IT NEVER REPORTS A PER-PROCESS TALLY, and that prohibition outlives the count it
// was written about. A tally of markets asked about since boot is not a map size at
// all: it starts at zero on every restart and climbs with uptime, so a bound derived
// from it is minutes wide in the window a restart opens, still climbing an hour
// later, and never related to how often the fleet actually re-reads a market. It
// moved 23m14s to 23m37s in four seconds while the count it was OR'd with sat on a
// five-minute TTL. Nothing here may fall back to one.
//
// THE FRESHNESS COUNT AND THE PACING DENOMINATOR ARE NOW ONE NUMBER, which is a
// change from when they had to err in OPPOSITE directions. That requirement was real
// while the denominator was a CHARTING CENSUS. Pacing wanted the smallest count it
// could defend, because under-counting only makes the allowance spend less than it is
// allowed to and a rate guard erring low is still a rate guard. A freshness bound
// wanted the largest, because a bound shorter than the rotation makes every consumer
// discard data that is merely waiting its turn. Both leans were compensating for one
// defect rather than expressing a real difference in what the two consumers need: a
// census counts markets no hull can scan, so it was wrong for pacing and wrong for
// freshness, in opposite directions, and each side had to correct for it separately.
//
// Sizing the rotation on the markets actually under demand dissolves the trade-off.
// The number is then neither a floor to be defended nor a ceiling to be inflated — it
// is what the rotation IS, so the bound it yields is the age the rotation genuinely
// cannot explain, and the two consumers can read the same number without either of
// them leaning. What it costs is that a market OUTSIDE the rotation is held to the
// rotation's bound, and that is the fail-closed direction on purpose: a price for a
// market nothing has looked at in hours is not evidence about that market.
//
// The context is unused and kept because it is the shape every consumer of this port
// already holds; the rotation size is in-process state rather than a database count,
// so there is nothing left here that can fail, time out, or serve an operator a
// number from five minutes ago.
//
// A nil budget (no scanner wired — every test that never builds one), or one nothing
// has asked about yet, reports an empty rotation. That is what makes an uncounted
// rotation legible as "unknown", which FreshnessCap answers with the caller's own
// floor — a number an operator chose, rather than one the process invented.
func (b *ScanBudget) RotationInputs(ctx context.Context) (marketscan.Budget, int) {
	if b == nil {
		return marketscan.Budget{}, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, _, marketsKnown := b.aggregateLocked(b.now())
	return b.policy, marketsKnown
}

// aggregateLocked returns the fleet median spread, the summed weight of the
// rotation and its size, recomputing them only when an observation or a change in
// the demand set has invalidated the cache.
//
// THE SIZE IS THE MARKETS UNDER DEMAND, not every market charted, and that is the
// correction the whole rotation turns on. Dividing a fixed rate across the charted
// map is only sound while every market in it can consume a scan; once the map runs
// far ahead of the fleet, the markets no hull is standing on hold shares of a rate
// they can never draw, and the ones that CAN are left waiting a share of the map
// rather than a share of the work. The linear decay the fixed budget is built on
// is still here — it is just measured against the rotation that exists rather than
// the one the map implies.
//
// The total counts UNMEASURED markets at the optimistic prior, not at zero.
// Omitting them would understate the denominator and hand every measured market a
// shorter interval than the budget can fund — the rotation would be paced as
// though the markets nobody has scanned yet did not have to be scanned.
func (b *ScanBudget) aggregateLocked(now time.Time) (median, totalWeight float64, marketsKnown int) {
	if b.pruneDemandLocked(now) {
		b.aggregateStale = true
	}
	known := len(b.demand)

	if !b.aggregateStale {
		return b.median, b.totalWeight, known
	}

	// Both sums run over the demand set and look the spread up, rather than over
	// the spreads and counting: the weights have to total exactly `known` terms or
	// the rotation is priced for a size it is not, and a market that has lapsed
	// still has a remembered spread.
	spreads := make([]float64, 0, known)
	for waypoint := range b.demand {
		if s := b.spread[waypoint]; s > 0 {
			spreads = append(spreads, s)
		}
	}
	b.median = marketscan.FleetMedianSpread(spreads)

	total := 0.0
	for waypoint := range b.demand {
		if s := b.spread[waypoint]; s > 0 {
			total += marketscan.Weight(s, b.median, b.policy.ValueClampR)
			continue
		}
		total += marketscan.PriorWeight(b.policy.ValueClampR)
	}
	b.totalWeight = total
	b.aggregateStale = false

	return b.median, b.totalWeight, known
}

// pruneDemandLocked drops the markets whose last request has aged past
// demandWindow, and reports whether it dropped any.
//
// Dropping is what makes the denominator self-correcting rather than another
// monotone counter. A set that only ever grows re-creates the same disease more
// slowly: every market a scout passed over once keeps a share forever, and the
// rate drains toward markets that stopped being reachable long ago.
func (b *ScanBudget) pruneDemandLocked(now time.Time) bool {
	pruned := false
	for waypoint, asked := range b.demand {
		if now.Sub(asked) >= demandWindow {
			delete(b.demand, waypoint)
			pruned = true
		}
	}
	return pruned
}

// weightLocked is one market's current value weight, or the optimistic prior
// when it has never been measured.
func (b *ScanBudget) weightLocked(waypoint string, median float64) float64 {
	if s, ok := b.spread[waypoint]; ok && s > 0 {
		return marketscan.Weight(s, median, b.policy.ValueClampR)
	}
	return marketscan.PriorWeight(b.policy.ValueClampR)
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

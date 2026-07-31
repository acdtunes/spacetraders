package ship

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// shipyard_scan_budget.go is the stateful half of the fleet's ONE shipyard-read
// budget (sp-mb0er), the sibling of market_scan_budget.go beside it. The
// admission arithmetic is not duplicated: Interval, MaxStaleness, the
// anti-starvation escape and the Earning/Discretionary classes all come from
// internal/domain/marketscan, which is generic over "a cached row with a value
// weight and a fixed allowance". Only the VALUE TERM differs, and that lives in
// internal/domain/yardscan. What lives here is the state both of them read: which
// yards are known, what the fleet is currently shopping for, which yards sell it,
// and how much of the allowance is unspent right now.
//
// WHY THIS EXISTS. Shipyard reads were measured at 0.844 req/s — 44.7% of the
// entire 2.00 req/s server ceiling that cannot be raised — against market reads
// which had already been brought under one budget at 0.35. Unlike market reads,
// which funnel through a single API choke point, shipyard reads reached
// APIClient.GetShipyard from FOUR call sites that bypassed the scanner entirely,
// so no single gate could cover them. They were masked instead by raising
// quartermaster_cadence_secs from 3600 to 86400 during an incident.
//
// THAT MITIGATION CAUSED THE SECOND FAILURE THIS BUDGET IS SHAPED BY. A cadence
// floor is not a budget: it can slow everything down uniformly but it cannot
// PRIORITISE. With a day-long floor, 84 yards were known to sell
// SHIP_HEAVY_FREIGHTER and exactly 4 had a price; the other 80 sat unpriced for
// 21-24 hours while the fleet bought 24 heavies at up to 2,288,156 against a
// visible cheapest of 1,918,293. The blunt instrument starved precisely the yards
// the buy loop was about to spend millions at. So this budget allocates by DEMAND
// (see yardscan.Weight) rather than merely rationing, and the cadence returns to
// being what its own documentation already claims it is: a floor the budget may
// not scan past, never the thing that sets the rate.

// defaultYardBudgetReqPerSec is the fleet's shipyard-read allowance.
//
// Sized against the measurement that opened the bead: 0.844 req/s of a 2.00 req/s
// ceiling. 0.12 is roughly 6% of the ceiling instead of 44.7%, and it is chosen so
// the incident case clears fast rather than merely slower — a backlog of 84
// demanded-but-unpriced yards, all sitting at the clamp, is swept in about twelve
// minutes (84 ÷ 0.12) against the 21-24 hours the cadence floor was delivering.
//
// It is a constant with respect to the charted map, which is the whole point: a
// map that doubles lengthens each yard's interval rather than doubling traffic, so
// this number never has to be revisited as an era grows.
const defaultYardBudgetReqPerSec = 0.12

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

// yardCountTTL is how long a charted-shipyard count is reused. The count moves
// only when charting finds new yards, so minutes of staleness cost nothing and
// re-counting per admission would put a query on the scan path.
const yardCountTTL = 5 * time.Minute

// yardFactsTTL is how long the demand picture read from the shipyard inventory is
// reused.
//
// It exists for RESTARTS, and that is the case that makes it load-bearing rather
// than an optimisation. Live scans keep the in-memory picture current through
// Observe, but a daemon that has just restarted has observed nothing — and the 80
// starved yards of the incident were known to sell a heavy only in the DATABASE.
// Without this refresh the budget would wake up believing no yard sells anything
// wanted, weight the entire map at the prior, and rediscover the demand ordering
// only as scans happened to land: the exact blindness it exists to prevent.
const yardFactsTTL = 5 * time.Minute

// yardDemandTTL is how long a hull type stays "wanted" after something last
// shopped for it.
//
// Demand is inferred from the budget's own traffic rather than declared by the buy
// loops, so it needs a decay or a single historical purchase would pin a type
// wanted forever. An hour is long enough to span a buy campaign's gaps — an
// autosizer that prices a heavy, waits for treasury, and prices again — and short
// enough that a type the fleet has stopped buying stops claiming the allowance.
const yardDemandTTL = time.Hour

// yardTargetTTL is how long a yard stays "targeted" after a money guard priced a
// hull there. Shorter than the demand window because it names a specific counter:
// it keeps the rotation warm across the navigate-dock-verify-buy sequence, not
// across a campaign.
const yardTargetTTL = 15 * time.Minute

// defaultYardPresenceReqPerSec is how fast the fleet may START repositioning
// hulls to unpriced yards (sp-fox5u) — the meter behind PresenceRequests.
//
// WHAT IS BEING METERED IS NOT THIS PACKAGE'S OWN TRAFFIC, and that is the whole
// reason it is a rate rather than a per-tick count. A shipyard READ costs one
// request and is charged where it is spent. A REPOSITION costs a Navigate, an
// Orbit and a Dock — and a refuel when the hop is long — all issued later, by the
// placement machine, on a tick this budget never sees. So this allowance paces the
// DECISION that causes that traffic, which is the only point at which it can be
// paced at all: once the claim is written the flying is already owed.
//
// SIZED AGAINST THE HEADROOM, not against the work. The server ceiling is a hard
// 2.00 req/s and the fleet runs at about 1.74, so roughly 0.26 is unspent and the
// shipyard read budget above already claims 0.12 of it. One reposition per fifty
// seconds costs about 0.06 req/s of downstream navigation — under a quarter of
// what is left — and still sweeps the ~100 yards that are actually addressable in
// well under two hours. Going faster would buy a shorter sweep of a set that is
// bounded by how many hulls can be spared, not by how fast requests are issued.
const defaultYardPresenceReqPerSec = 0.02

// yardPresenceBurst is the depth of the reposition bucket.
//
// TWO, and deliberately far shallower than yardBurstRequests. A read bucket wants
// depth so a tour touching several yards in one tick is absorbed rather than
// declined; a reposition bucket wants the opposite, because every token it banks
// is a hull that will be pulled off a working market in one burst. Two allows a
// system whose catalogue has just landed to be worked on the tick it lands
// without letting a quiet hour accumulate an evacuation.
const yardPresenceBurst = 2

// ChartedYardCounter reports how many shipyard waypoints the player has charted —
// the denominator of "budget ÷ yards known".
//
// A narrow optional port rather than a widening of the scanner's waypoint
// interface, which dozens of test fakes implement and which has no business
// knowing the map size. The production waypoint repository satisfies it and the
// budget discovers it by type assertion; a store that does not falls back to
// counting the yards the budget has been asked about, which is a lower bound on
// the map and so paces slightly LOOSER but never leaves the budget unenforced.
type ChartedYardCounter interface {
	ChartedShipyardCount(ctx context.Context) (int, error)
}

// YardCatalogReader reports every persisted yard row whose ship type is in the
// given set — the budget's view of which counters sell what the fleet wants, and
// which of those have ever been priced. shipyard.InventoryRepository satisfies it.
type YardCatalogReader interface {
	ListByTypes(ctx context.Context, playerID int, shipTypes []string) ([]shipyard.ShipTypeAvailability, error)
}

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

// NoteDemand records that something is shopping for this hull type, so every yard
// known to sell it rises in the rotation.
//
// Demand is INFERRED from the budget's own traffic rather than declared by the buy
// loops, and that is deliberate: every path that prices a hull already has to name
// the type it is pricing, so the signal is free and cannot drift out of sync with
// what the fleet is actually buying. A buy path added tomorrow contributes demand
// without knowing this budget exists.
func (b *YardScanBudget) NoteDemand(shipType string) {
	if shipType == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, known := b.demand[shipType]; !known {
		// A newly wanted type changes which yards count as valuable, so the cached
		// total is stale and the store picture must be rebuilt against the new set.
		b.aggregateStale = true
		b.catalogAt = time.Time{}
	}
	b.demand[shipType] = b.now()
}

// NoteTarget records that a money guard priced a hull at this yard — the fleet is
// buying HERE, not merely shopping.
//
// It does not exist to admit the guard read: that read is Earning-class and was
// never deniable. It exists so the ROTATION keeps a yard we are actively buying
// from fresh between guard reads, rather than letting it decay to the baseline the
// moment the buy loop looks away.
func (b *YardScanBudget) NoteTarget(waypoint string) {
	if waypoint == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.target[waypoint] = b.now()
	b.aggregateStale = true
}

// Observe folds a freshly scanned yard's catalogue into its value estimate, so the
// next rotation weights it on what it is actually selling and whether that is
// priced, rather than on the optimistic prior it carried while unseen.
//
// A yard that turns out to sell nothing wanted drops to the baseline here, which
// is how the rotation's attention concentrates: every scan either confirms a yard
// is worth watching or retires it.
func (b *YardScanBudget) Observe(waypoint string, availabilities []shipyard.ShipTypeAvailability) {
	if waypoint == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	facts := yardscan.Facts{}
	heavy := false
	for _, a := range availabilities {
		// Recorded from the FULL listing rather than only from the wanted ones. A
		// heavy type is structurally wanted (see wantedLocked), so the two agree
		// today — but reading it off its own predicate is what keeps this fact true
		// if the demand window ever stops covering a heavy class, rather than
		// quietly downgrading the fleet's most expensive counters.
		if b.heavy.Contains(a.ShipType) {
			heavy = true
		}
		if !b.wantedLocked(a.ShipType) {
			continue
		}
		facts.SellsWanted = true
		if a.PurchasePrice > 0 {
			facts.Priced = true
		}
	}
	b.facts[waypoint] = facts
	// A fresh scan REPLACES the heavy reading rather than accumulating it: this is
	// the authoritative listing for the yard, and a counter that has stopped
	// stocking heavies must stop being ranked as one.
	if heavy {
		b.heavySeller[waypoint] = true
	} else {
		delete(b.heavySeller, waypoint)
	}
	b.seen[waypoint] = struct{}{}
	b.aggregateStale = true
}

// PresenceRequests reports the yards the budget wants priced and cannot see,
// best first, capped at limit.
//
// THIS IS THE BUDGET ADMITTING WHAT IT CANNOT FIX. Its top weight tier is
// "confirmed seller of a hull we want, at a price we have never seen", and the
// rotation drives those yards to the head of the queue exactly as designed — but
// the read it then spends comes back WITHOUT a price, because purchasePrice only
// appears in the response when a hull of ours is standing at the counter. The
// demand is correct and the remedy is not a read. So the same tier is published
// here as a request for PRESENCE, which is the only thing that turns it into a
// price. See yardscan.WantsPresence.
//
// A PULL, NEVER A PUSH, and that is a deliberate defence rather than a style
// choice. A budget that PUBLISHED presence demand into a mover would be a
// latching bridge: the mover would hold the last signal it was sent, and a yard
// that got priced — or a fleet that stopped shopping for the type — would leave a
// stale request standing that keeps pulling hulls at a counter nobody needs. Here
// the set is recomputed from live facts on every call, so a yard leaves it the
// moment it is priced and the whole set empties the moment demand decays. There
// is no state to retract because nothing was ever stored.
//
// A ZERO OR NEGATIVE limit yields nothing rather than everything. The caller's
// bound is the last line of defence against a burst of hull moves, and the reading
// that turns an absent bound into "unbounded" is the one that would hurt.
func (b *YardScanBudget) PresenceRequests(ctx context.Context, playerID int, limit int) []yardscan.PresenceRequest {
	if b == nil || limit <= 0 {
		return nil
	}
	// Refreshed FIRST, and this is the restart case that makes the whole feature
	// work rather than an optimisation. A daemon that has just come up has observed
	// nothing, so facts is empty and this would report no yard needs presence — on a
	// fleet where 81 heavy counters sit unpriced in the DATABASE. The store read is
	// what makes the request set survive a restart.
	b.refreshFacts(ctx, playerID)

	b.mu.Lock()
	defer b.mu.Unlock()

	requests := make([]yardscan.PresenceRequest, 0, len(b.facts))
	for waypoint, f := range b.facts {
		if waypoint == "" || !yardscan.WantsPresence(f) {
			continue
		}
		// Targeted is applied here and not stored, exactly as weightLocked does it,
		// so a yard a money guard is buying at right now sorts above an idle lead
		// of the same class.
		f.Targeted = b.targetedLocked(waypoint)
		requests = append(requests, yardscan.PresenceRequest{
			Waypoint: waypoint,
			// Derived from the symbol rather than carried from the inventory row's
			// own system column, because the CONSUMER keys its search by the sensing
			// ledger's system field, which is derived the same way (see
			// newPurchaseCandidate). Two derivations that could disagree would make
			// the pass silently find no hull to send instead of failing loudly.
			System: shared.ExtractSystemSymbol(waypoint),
			Heavy:  b.heavySeller[waypoint],
			Weight: yardscan.Weight(f, b.policy.ValueClampR),
		})
	}

	ranked := yardscan.RankPresence(requests)
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

// AdmitPresence consumes one reposition from the allowance, reporting whether
// there was one to consume.
//
// UNLIKE Admit, THERE IS NO OVERDRAFT AND NO FORCED CLASS. A shipyard read can be
// something a money guard must have — RULINGS #4 forbids serving a pre-buy price
// check from store — so that allowance has to be able to go negative and let the
// debt squeeze discretionary traffic instead of refusing a guard. Nothing about a
// reposition is ever that: no purchase waits on one, and a yard left unpriced for
// another minute costs only that the buy loop keeps choosing among the counters it
// can already see. So this refuses cleanly when the bucket is empty, which is what
// keeps a backlog of a hundred addressable yards from being swept in one tick.
func (b *YardScanBudget) AdmitPresence() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.presence.AllowN(b.now(), 1) {
		b.presenceDeclined++
		return false
	}
	b.presenceIssued++
	return true
}

// YardBudgetSnapshot is a point-in-time read of the budget, for metrics and the
// operator-facing report.
type YardBudgetSnapshot struct {
	RateReqPerSec float64
	ValueClampR   int
	YardsKnown    int
	// YardsWanted is how many known yards sell something the fleet is shopping
	// for, and YardsUnpriced how many of those we hold no price for. The second
	// number IS the incident this budget was written for: it stood at 80 of 84
	// when the cadence floor was the only pacing, and a rotation that is working
	// drives it toward zero.
	YardsWanted     int
	YardsUnpriced   int
	TotalWeight     float64
	TokensAvailable float64
	// TypicalInterval is how long a baseline yard currently waits between reads —
	// the number that grows as the map grows while the rate stays put, and the
	// most direct read on whether the fixed-budget invariant holds.
	TypicalInterval time.Duration
	// WorstCaseStaleness is the anti-starvation bound at the current map size.
	WorstCaseStaleness time.Duration
	Admitted           uint64
	Declined           uint64
	// Forced counts admitted reads that found the bucket already empty: money
	// guards and starvation escapes. A persistently high count means the fixed
	// budget is smaller than the fleet's unavoidable pre-buy verification and
	// should be raised, rather than the guards being weakened (RULINGS #4).
	Forced uint64
	// YardsNeedingPresence is how many known yards sell something wanted, hold no
	// price, and therefore cannot be priced by any amount of reading (sp-fox5u).
	//
	// It is the honest denominator beside YardsUnpriced: the two are the same set,
	// but this name says why the rotation alone will never drive it down. A working
	// presence path drives it toward zero; a flat reading beside a rising
	// PresenceDeclined means the allowance is the binding constraint, and a flat
	// reading beside a flat PresenceIssued means no hull could be spared.
	YardsNeedingPresence int
	// PresenceIssued and PresenceDeclined count repositions started and refused
	// for want of allowance.
	PresenceIssued   uint64
	PresenceDeclined uint64
}

// Snapshot reports the budget's current state.
func (b *YardScanBudget) Snapshot() YardBudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	totalWeight, yardsKnown := b.aggregateLocked()
	wanted, unpriced, needsPresence := 0, 0, 0
	for _, f := range b.facts {
		if yardscan.WantsPresence(f) {
			needsPresence++
		}
		if !f.SellsWanted {
			continue
		}
		wanted++
		if !f.Priced {
			unpriced++
		}
	}
	return YardBudgetSnapshot{
		RateReqPerSec:        b.policy.RateReqPerSec,
		ValueClampR:          b.policy.ValueClampR,
		YardsKnown:           yardsKnown,
		YardsWanted:          wanted,
		YardsUnpriced:        unpriced,
		TotalWeight:          totalWeight,
		TokensAvailable:      b.limiter.TokensAt(b.now()),
		TypicalInterval:      marketscan.Interval(b.policy, yardscan.Baseline, totalWeight),
		WorstCaseStaleness:   marketscan.MaxStaleness(b.policy, yardsKnown),
		Admitted:             b.admitted,
		Declined:             b.declined,
		Forced:               b.forced,
		YardsNeedingPresence: needsPresence,
		PresenceIssued:       b.presenceIssued,
		PresenceDeclined:     b.presenceDeclined,
	}
}

// RotationInputs reports the allowance and the live map size behind it — the two
// numbers marketscan.MaxStaleness needs to say how old a cached yard row may be
// before it is older than this rotation can explain. A nil budget reports a zero
// map, which callers answer with their own floor rather than with a widened cap.
func (b *YardScanBudget) RotationInputs(ctx context.Context) (marketscan.Budget, int) {
	if b == nil {
		return marketscan.Budget{}, 0
	}
	b.refreshChartedCount(ctx)

	b.mu.Lock()
	defer b.mu.Unlock()
	_, yardsKnown := b.aggregateLocked()
	return b.policy, yardsKnown
}

// wantedLocked reports whether the fleet is shopping for this hull type: either it
// is structurally a heavy (always wanted) or something has priced it inside the
// demand window.
func (b *YardScanBudget) wantedLocked(shipType string) bool {
	if shipType == "" {
		return false
	}
	if b.heavy.Contains(shipType) {
		return true
	}
	at, ok := b.demand[shipType]
	return ok && b.now().Sub(at) < yardDemandTTL
}

// wantedTypesLocked is the current wanted set, for the store query that rebuilds
// the demand picture. Expired demand is dropped here rather than swept on a timer,
// so the map cannot outlive the window it is read through.
func (b *YardScanBudget) wantedTypesLocked() []string {
	now := b.now()
	seen := make(map[string]struct{})
	out := make([]string, 0, len(b.demand)+4)
	for _, t := range b.heavy.Members() {
		if _, dup := seen[t]; dup || t == "" {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for t, at := range b.demand {
		if now.Sub(at) >= yardDemandTTL {
			delete(b.demand, t)
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// aggregateLocked returns the summed weight of every known yard and the map size,
// recomputing them only when an observation, a demand change or a new yard has
// invalidated the cache.
//
// Yards nothing is known about are counted at the optimistic prior, not at zero.
// Omitting them would understate the denominator and hand every known yard a
// shorter interval than the budget can fund — the map would be paced as though the
// yards nobody has opened did not have to be opened.
func (b *YardScanBudget) aggregateLocked() (totalWeight float64, yardsKnown int) {
	known := len(b.seen)
	if b.charted > known {
		known = b.charted
	}

	if !b.aggregateStale {
		return b.totalWeight, known
	}

	total := 0.0
	described := 0
	for waypoint, f := range b.facts {
		if described >= known {
			break
		}
		f.Targeted = b.targetedLocked(waypoint)
		total += yardscan.Weight(f, b.policy.ValueClampR)
		described++
	}
	if rest := known - described; rest > 0 {
		total += float64(rest) * yardscan.PriorWeight(b.policy.ValueClampR)
	}
	b.totalWeight = total
	b.aggregateStale = false

	return b.totalWeight, known
}

// weightLocked is one yard's current value weight, or the optimistic prior when
// nothing is known about it.
func (b *YardScanBudget) weightLocked(waypoint string, now time.Time) float64 {
	f, ok := b.facts[waypoint]
	if !ok {
		f = yardscan.Facts{Unknown: true}
	}
	if at, targeted := b.target[waypoint]; targeted && now.Sub(at) < yardTargetTTL {
		f.Targeted = true
	}
	return yardscan.Weight(f, b.policy.ValueClampR)
}

// targetedLocked reports whether a money guard priced at this yard inside the
// target window.
func (b *YardScanBudget) targetedLocked(waypoint string) bool {
	at, ok := b.target[waypoint]
	return ok && b.now().Sub(at) < yardTargetTTL
}

// refreshChartedCount re-reads the map size when the cached reading has aged out.
// It runs OUTSIDE the mutex so a slow query cannot block every other container's
// admission, and a failed read leaves the previous count in place — a counter
// hiccup must widen nothing and must not reset the denominator to zero, which
// would collapse every interval.
func (b *YardScanBudget) refreshChartedCount(ctx context.Context) {
	b.mu.Lock()
	counter := b.counter
	due := b.chartedAt.IsZero() || b.now().Sub(b.chartedAt) >= yardCountTTL
	b.mu.Unlock()

	if counter == nil || !due {
		return
	}

	total, err := counter.ChartedShipyardCount(ctx)

	b.mu.Lock()
	defer b.mu.Unlock()
	// Stamp the attempt either way, so a persistently failing counter is retried on
	// the TTL rather than on every single admission.
	b.chartedAt = b.now()
	if err != nil || total < 0 {
		return
	}
	if total != b.charted {
		b.charted = total
		b.aggregateStale = true
	}
}

// refreshFacts rebuilds the demand picture from the persisted inventory when the
// cached one has aged out.
//
// A failed read leaves the previous picture in place. That direction is the safe
// one: the picture only ever RAISES a yard's weight above the baseline, so losing
// it degrades the budget to an unprioritised rotation rather than to an unpaced
// one, and re-reading on the TTL recovers it.
func (b *YardScanBudget) refreshFacts(ctx context.Context, playerID int) {
	b.mu.Lock()
	catalog := b.catalog
	due := b.catalogAt.IsZero() || b.now().Sub(b.catalogAt) >= yardFactsTTL || playerID != b.lastPlayer && b.catalogHeld
	wanted := b.wantedTypesLocked()
	b.mu.Unlock()

	if catalog == nil || !due || len(wanted) == 0 {
		return
	}

	rows, err := catalog.ListByTypes(ctx, playerID, wanted)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.catalogAt = b.now()
	if err != nil {
		return
	}
	b.catalogHeld = true
	// Stamped here rather than only in Admit, because the dueness test above reads
	// it: a caller that refreshes WITHOUT admitting (PresenceRequests) would
	// otherwise never match lastPlayer, find itself perpetually due, and put a store
	// query on every single tick. Set only on a SUCCESSFUL read, so a failing
	// catalogue still forces the re-read a player change is supposed to force.
	b.lastPlayer = playerID

	// PRICED IS REBUILT PER WAYPOINT, NOT ACCUMULATED, and that is a correctness
	// fix rather than a tidy-up. The rows here are the whole of what the store holds
	// for these yards — ReplaceScan deletes a waypoint's rows and re-inserts from
	// each reading, so a presence-less rescan of a yard whose hull has left writes
	// purchase_price 0 and nothing else survives. A flag that could only ever be
	// SET would latch the old price forever: the yard would read as priced, drop out
	// of the presence queue, and never get another hull — while the number the buy
	// loop is reasoning about is gone from the database. Aggregating first is what
	// lets a yard go dark again.
	priced := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.WaypointSymbol == "" {
			continue
		}
		if row.PurchasePrice > 0 {
			priced[row.WaypointSymbol] = true
		} else if _, seen := priced[row.WaypointSymbol]; !seen {
			priced[row.WaypointSymbol] = false
		}
	}

	for _, row := range rows {
		if row.WaypointSymbol == "" {
			continue
		}
		f := b.facts[row.WaypointSymbol]
		f.Unknown = false
		f.SellsWanted = true
		f.Priced = priced[row.WaypointSymbol]
		b.facts[row.WaypointSymbol] = f
		// The HEAVY flag, unlike Priced, is accumulated and never cleared here.
		// These rows are the wanted-type SUBSET of the inventory rather than a
		// yard's whole listing, so an absence of heavy rows is "the query did not
		// ask about that" and not "the counter stopped stocking them". Observe holds
		// the full listing and is the one place that may demote a heavy yard.
		if b.heavy.Contains(row.ShipType) {
			b.heavySeller[row.WaypointSymbol] = true
		}
		if _, ok := b.seen[row.WaypointSymbol]; !ok {
			b.seen[row.WaypointSymbol] = struct{}{}
		}
	}
	b.aggregateStale = true
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

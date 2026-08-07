// Package marketscan holds the pure arithmetic behind the fleet's ONE
// market-scan budget: how much of the server's request ceiling every market
// read in the daemon may draw between them, how often a market of a given
// value comes round in the rotation, and the worst-case staleness that
// rotation guarantees.
//
// WHY THIS PACKAGE EXISTS. The SpaceTraders rate limiter caps the
// fleet at 2.00 req/s and that ceiling cannot be raised. Sensing scales with
// the map we have charted while trading scales with credits, so the map
// outruns the fleet and the ceiling binds EARLIER every era. Measured live at
// 26 trade hulls: the parked sensing pacer honoured its own 0.100 req/s
// budget, yet total market reads across the daemon were 0.644 req/s — roughly
// 84% of market reading happened outside any budget, because a dozen call
// paths reached the shared market scanner directly.
//
// The organising invariant is therefore:
//
//	TOTAL MARKET SCAN COST DOES NOT GROW WITH THE CHARTED MAP.
//
// Everything else follows from it. The budget is a fixed req/s; the scan
// INTERVAL is an output of budget ÷ markets known, never a per-market
// setting; so the more markets we know, the less often each is looked at, and
// the ceiling stays survivable forever instead of binding earlier every era.
// Within that fixed spend, attention is allocated by value — the markets that
// earn the most are looked at the most — and an unconditional escape hatch
// guarantees every known market still comes round, because market prices are
// volatile and non-monotone: a market we crush recovers on its own, and the
// ONLY way we learn it recovered is to go and look.
//
// Everything here is a value-in/value-out function. There is no clock, no
// repository, no token bucket and no API client: callers pass the observations
// in and get a decision back. The stateful half — the known-market registry,
// the spread estimates and the token bucket the observations are read from —
// lives in internal/application/ship/market_scan_budget.go.
//
// Value weighting is deliberately NOT reimplemented here. It is imported from
// internal/domain/parkedsensing, which already had the priority machinery this
// budget needed (its value_clamp_r weighting and spread EWMA). One weighting
// function, one clamp, one meaning of "hot market" across both the parked
// rotation and every other reader.
package marketscan

import (
	"math"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
)

// Class says why a market read is being made, which is the only thing that can
// exempt one from being denied.
type Class int

const (
	// Discretionary is the ZERO VALUE on purpose. A market read on a call path
	// that carries no class tag is budgeted, so the budget covers call sites
	// nobody remembered to classify — including ones added after this package.
	// The failure mode of forgetting to tag is "this read is paced", never
	// "this read is unmetered", which is the direction that keeps the invariant
	// true.
	Discretionary Class = iota

	// Earning marks a read whose result is consumed by a money guard before a
	// buy or a sell commits: the per-tranche sell floor and buy ceiling, and
	// the pre-buy stale-ask verification. Those guards exist precisely because
	// a CACHED price is not trustworthy enough to trade on — serving them from
	// store would not save a request, it would silently convert a live money
	// guard into a stale one and re-open the realised losses each was written
	// to stop (RULINGS #4: money guards are never weakened).
	//
	// An Earning read is never denied. It is still METERED: it draws its token
	// from the same bucket, so trade-critical verification comes out of the
	// same fixed allowance and squeezes discretionary scanning rather than
	// being added on top of it. That is what keeps ONE budget honest while
	// still admitting a read that genuinely cannot wait.
	Earning

	// Paired marks a read whose entire value comes from being taken IMMEDIATELY
	// AFTER a known earlier one: the "after" half of the scan-buy-scan pair the
	// price-impact model is fitted from, which is what tells the lane
	// ranker how far a market moves when we dump into it.
	//
	// For a paired read, a FRESH cache is not evidence the read is unnecessary —
	// it is the precondition that makes the read worth taking, because the cached
	// row is the "before" observation the "after" is measured against. So the
	// dueness test does not apply. Everything else does: a Paired read still needs
	// a token and still faces the value bar, because instrumentation is exactly
	// what a budget under pressure should shed first. It is also independently
	// sampled upstream (impact_sample_rate, ~0.15), so its draw on the allowance
	// is a rounding error in normal operation.
	//
	// Without this class the budget would silently starve the refit corpus: every
	// "after" scan would be declined for arriving too soon after its own "before",
	// and the model would quietly stop tracking the era.
	Paired
)

// String is the class's metric-label form, and it is defined HERE rather than at
// the recording site so the label domain is the enum itself: a class added to
// this package cannot silently start reporting as something else.
//
// An unrecognised value reports as the zero-value name rather than as a numeric
// fallback, because the only way to reach it is a Class this package does not
// define — and a budget vocabulary that leaked "Class(7)" into a label would
// spray one series per stray integer.
func (c Class) String() string {
	switch c {
	case Earning:
		return "earning"
	case Paired:
		return "paired"
	default:
		return "discretionary"
	}
}

// Decision is what a caller does with the request it just submitted.
type Decision int

const (
	// ServeFromStore is the ZERO VALUE on purpose: an uninitialised or
	// unreached decision spends nothing. The caller skips the API call and
	// reads whatever the cache already holds — which is what every caller
	// already does immediately after a scan, so declining costs the caller no
	// new branch, only slightly older prices.
	ServeFromStore Decision = iota

	// Spend authorises one market API request.
	Spend
)

// String is the decision's metric-label form. Same rule as Class.String: an
// unrecognised value reports as the zero value (ServeFromStore), which is also
// the safe reading — a decision this package cannot name did not authorise a
// request.
func (d Decision) String() string {
	if d == Spend {
		return "spend"
	}
	return "serve_from_store"
}

// NeverScanned is the Staleness a caller passes for a market with no cached row
// at all. It is the largest representable duration, so it exceeds every
// MaxStaleness this package can compute and a never-scanned market is always
// admitted by the anti-starvation escape rather than needing a rule of its own.
const NeverScanned = time.Duration(math.MaxInt64)

// stalenessCeiling bounds the computed worst-case staleness. It is an ARITHMETIC
// guard, not a policy knob: it stops a degenerate budget (a rate rounded to
// almost nothing) from producing an interval measured in centuries, which
// int64 nanoseconds cannot hold and which would be indistinguishable from a
// hung scanner.
//
// Note the contrast with parkedsensing's own intervalCap of one hour, which
// this package deliberately does NOT adopt. A hard hourly cap puts a floor
// under each market's scan rate, so total spend becomes markets ÷ 1h and grows
// with the map — the exact property this budget exists to remove. The ceiling
// here is thirty days: far outside any operational rotation, so it never
// shapes normal behaviour, and only reachable when the budget is misconfigured
// to near zero, where firing the escape is the correct outcome because the
// alternative is data going permanently dark.
const stalenessCeiling = 30 * 24 * time.Hour

// derivedCapCeiling is how far the ROTATION-DERIVED term may widen a consumer's
// freshness cap. It is not stalenessCeiling: that one is an arithmetic guard
// against a misconfigured rate producing a duration int64 cannot hold, thirty days
// out and never meant to shape behaviour. This one is a POLICY bound, and it is
// meant to bind.
//
// WHY A FIXED-BUDGET DERIVATION NEEDS ONE AT ALL. MaxStaleness grows linearly and
// without limit in the population it is measured over, so a cap that is only ever
// max(floor, bound) can be widened without limit by that population — and it
// widens SILENTLY, because nothing refuses and no counter moves. Sized on the
// charting census the derivation reached ~88h, which left the firm-sink FRESH
// clause — a fail-closed money guard — admitting prices nearly four days old. A
// guard that can be un-armed by a number growing underneath it is not fail-closed
// (RULINGS #4), and the direction it failed in is the one nothing observes.
//
// ONE DAY, AND THE RISK IS ENTIRELY ON THE OTHER SIDE. A ceiling set BELOW a
// rotation the fleet really runs is the too-tight cap that cost 87% of trade
// throughput, so the number is sized against the largest rotation on record rather
// than against what looks prudent. That high-water mark is the era-5 incident:
// 4,389 markets at 0.70 req/s derives 13h56m, and rows of that age were explained
// by the rotation and had to be admitted. A day is the smallest round bound that
// sits clear of it, and it carries its own meaning — past a day a price is not
// evidence about a market under any rotation this fleet has ever run.
//
// It still bites where it has to. Against a whole-map census the derivation runs to
// several days, and cutting that to one is the difference between a guard that
// admits prices from last week and one that does not.
//
// It is a backstop rather than the operative term. Sized on the markets actually
// under demand the bound is minutes, so an operator floor governs and this is
// reached only by a rotation of rate x 86400 / ValueClampR markets — some thousands
// at any allowance the fleet has run, and orders of magnitude above the rotation it
// actually keeps.
//
// Calibrating it against a live measurement rather than a constant would be
// better still, and the repository already computes the honest input for it —
// scouting.SystemsFreshness reports a measured per-market scan INTERVAL, which is
// non-circular in a way that observed STALENESS is not (deriving a freshness cap
// from how stale things are lets any amount of staleness justify itself). It is
// deliberately not wired here: this package is value-in/value-out, with no clock
// and no repository, and putting a database read behind a money guard's cap adds a
// failure mode to the guard.
const derivedCapCeiling = 24 * time.Hour

// baselineWeight is the weight of the least valuable market in the rotation.
// parkedsensing.ScanWeight clamps every weight up to it, so it is the floor of
// the weight domain and the required-weight scale starts here.
const baselineWeight = 1.0

// Budget is the fleet's single market-scan allowance.
type Budget struct {
	// RateReqPerSec is THE fixed budget: the total market-read rate every
	// reader in the daemon shares. It is a constant with respect to the
	// charted map — that is the whole point — so growing the map lengthens
	// each market's interval instead of raising this number.
	RateReqPerSec float64

	// ValueClampR is the ceiling on how much more scan attention the hottest
	// market may earn than the baseline. It is simultaneously the
	// anti-starvation constant: because every weight lies in
	// [1, ValueClampR], the coldest market's interval is at most ValueClampR
	// times the mean, which is what makes the worst case finite. 1 flattens
	// the weighting entirely.
	ValueClampR int
}

// Request is one market read seeking admission.
type Request struct {
	// Class is why the read is being made. The zero value is Discretionary.
	Class Class

	// Staleness is how long ago this market's cached row was written, or
	// NeverScanned when there is no cached row at all.
	Staleness time.Duration

	// Weight is this market's value weight from parkedsensing.ScanWeight,
	// in [1, ValueClampR].
	Weight float64

	// TotalWeight is the summed weight of every known market. Together with
	// Weight it fixes this market's share of the fixed budget.
	TotalWeight float64

	// MarketsKnown is how many markets share the budget, and is what the
	// anti-starvation bound is computed against.
	//
	// It is carried separately rather than inferred from TotalWeight, and the
	// difference is not cosmetic. Inferring the count as TotalWeight/ValueClampR
	// yields a bound that, on the ordinary map where most markets sit at the
	// baseline, equals the baseline market's OWN interval — so the escape hatch
	// would trigger at the same moment a cold market merely became due,
	// swallowing the budget cap and the value ordering whole and leaving cold
	// markets effectively unpaced. The bound has to be computed from the real
	// count to sit the intended ValueClampR-fold above a market's own interval.
	MarketsKnown int

	// TokensAvailable is how much of the budget's burst bucket is unspent, and
	// BucketCapacity how large that bucket is. Their RATIO is the contention
	// signal: a full bucket admits every due market, a nearly empty one admits
	// only the most valuable, which is how a priority ordering emerges from
	// per-request admission without a central scheduler.
	TokensAvailable float64
	BucketCapacity  float64

	// SpendsSinceDiscretionary is how many reads the allowance has admitted
	// since the last discretionary one — the only cross-request fact the
	// admission rules need, carried in so the policy stays a pure function of
	// its argument.
	//
	// It is what makes the discretionary FLOOR expressible without a central
	// scheduler. The value bar orders reads by what they earn, and a class that
	// earns nothing directly always loses that comparison; counting how long the
	// class has gone unserved is what lets a per-request rule notice that the
	// ordering has stopped being an ordering and become an exclusion.
	SpendsSinceDiscretionary int
}

// Interval is how long a market of the given weight waits between scans when
// the whole known map is served at the budget's fixed rate.
//
// The fleet spends `rate` scans per second across `totalWeight` shares, so one
// share comes round every totalWeight/rate seconds and a market holding
// `weight` shares comes round `weight` times as often. Summing 1/interval over
// every market gives exactly `rate` whatever the map size — the invariant this
// package exists to hold. Degenerate inputs (a non-positive rate, weight or
// total) yield the arithmetic ceiling rather than dividing by zero.
func Interval(b Budget, weight, totalWeight float64) time.Duration {
	if b.RateReqPerSec <= 0 || weight <= 0 || totalWeight <= 0 {
		return stalenessCeiling
	}
	seconds := totalWeight / (b.RateReqPerSec * weight)
	return clampDuration(seconds)
}

// MaxStaleness is the bounded worst case: no known market goes unscanned for
// longer than this, whatever its value and however contended the budget.
//
// It is the interval of the COLDEST admissible market — weight exactly at the
// baseline, against a map where every market instead holds the maximum weight,
// so totalWeight is marketsKnown × ValueClampR. Any real market's interval is
// at most this, because a real total cannot exceed the all-maximum total and a
// real weight cannot fall below the baseline. That makes the bound sound
// rather than merely typical, and it is what Decide uses as its unconditional
// escape: past this age a market is admitted regardless of its value or the
// state of the bucket.
//
// The bound grows linearly with the map, which is correct and is the price of
// a fixed budget: knowing twice as many markets means looking at each half as
// often. It never becomes infinite, which is what "no starvation" means here.
func MaxStaleness(b Budget, marketsKnown int) time.Duration {
	if marketsKnown <= 0 {
		return stalenessCeiling
	}
	clamp := b.ValueClampR
	if clamp < 1 {
		clamp = 1
	}
	return Interval(b, baselineWeight, float64(marketsKnown)*float64(clamp))
}

// FreshnessCap is the age past which a consumer may refuse a cached market row:
// the larger of the operator's floor and the age the rotation itself cannot
// explain.
//
// It answers the question a freshness-gated consumer should be asking. The WRONG
// question — the one that cost 87% of trade throughput — is "is this row older
// than N minutes", with N chosen when the map was 300 systems. Because the budget
// is fixed, each market's scan interval is an OUTPUT of budget ÷ markets known
// (see the package doc), so N silently invalidates every time charting finds
// another market: at 4,389 markets and 0.70 req/s an even rotation is already
// ~105 minutes, and a 75-minute N discards four fifths of the map as worthless
// when nothing whatever is wrong with it.
//
// MaxStaleness is the RIGHT question expressed as a number. It is the bound the
// scanner's own anti-starvation escape enforces, so refusing at it means a
// consumer refuses exactly when the scanner has failed its own guarantee — the
// only case where a row is genuinely dead rather than merely waiting its turn.
//
// The answer is max(floor, min(bound, derivedCapCeiling)), and each of those three
// terms is there to stop a different way of getting this wrong.
//
// floor is applied as a FLOOR and never as a ceiling. The cap is therefore never
// TIGHTER than the rotation bound, whatever the floor says, so no setting of it
// can re-create the incident by refusing rotation-explained rows; a floor raised
// ABOVE the current bound does widen the cap, which is the direction an operator
// ever needs in an incident. It also means the cap is never below the floor, so a
// consumer armed at a floor can never be disarmed through this function — the
// property the firm-sink money guard depends on (RULINGS #4). Note the ceiling
// bounds the DERIVED term only and never the operator: a floor set above it still
// wins, because clamping an explicit instruction is not this function's business.
//
// derivedCapCeiling is the answer to the failure the first two terms create
// between them. Both of them only ever widen, and the widening is silent — see the
// constant for why a cap nothing can bound is not a fail-closed guard.
//
// An unknown map (marketsKnown <= 0) or an unconfigured budget yields the floor
// alone, and that direction is deliberate: MaxStaleness answers a zero map size
// with the 30-day arithmetic ceiling, and letting an unwired budget widen a money
// guard to thirty days is precisely the fail-OPEN a freshness gate must never do.
func FreshnessCap(floor time.Duration, b Budget, marketsKnown int) time.Duration {
	if marketsKnown <= 0 || b.RateReqPerSec <= 0 {
		return floor
	}
	bound := MaxStaleness(b, marketsKnown)
	if bound > derivedCapCeiling {
		bound = derivedCapCeiling
	}
	if bound > floor {
		return bound
	}
	return floor
}

// valueReserveFraction is the share of the allowance bucket held back for the
// markets that earn the most. Above it every due market is admitted; within it
// the bar climbs toward ValueClampR.
//
// A reserve rather than a bar that starts rising from a full bucket, and the
// difference is not a tuning nicety. Solve the naive version — bar =
// baseline + (1-fill)(R-1) — for a market at the baseline weight and it is
// admitted only when fill is exactly 1, i.e. only at a perfectly full bucket.
// Cold markets would then be served almost entirely by the anti-starvation
// escape rather than by the rotation, which both makes the rotation lumpy and
// concentrates spend on hot markets far past the ValueClampR-fold ratio the
// interval already encodes. Reserving half the bucket keeps the interval as the
// primary allocator — a hot market is due R times as often, which is where the
// value ordering is supposed to come from — and uses the bar only as the
// tiebreaker for a genuinely contended budget.
const valueReserveFraction = 0.5

// requiredWeight is the value a discretionary market must be worth to claim a
// token at the current bucket fill: the baseline while the bucket is above the
// reserve, rising to ValueClampR as the reserve itself is consumed.
//
// This is the priority ordering, expressed so that it can be evaluated on one
// request in isolation. There is no central queue to sort because reads arrive
// from a dozen independent callers; instead the admission BAR rises as the
// reserve is consumed, so an uncontended budget serves everyone due and a
// contended one serves progressively hotter markets only. Over any window the
// effect is the intended one — the markets that earn the most are looked at the
// most — and no caller has to know about any other.
//
// A ValueClampR of 1 collapses the bar to the baseline at every fill, which is
// how the weighting is switched off: the flat-weighting case admits purely on
// dueness and token availability.
func requiredWeight(b Budget, tokensAvailable, capacity float64) float64 {
	clamp := float64(b.ValueClampR)
	if clamp < baselineWeight {
		clamp = baselineWeight
	}
	if capacity <= 0 {
		return clamp
	}
	fill := tokensAvailable / capacity
	if fill < 0 {
		fill = 0
	}
	if fill >= valueReserveFraction {
		return baselineWeight
	}
	// Depth into the reserve, 0 at its top and 1 when the bucket is empty.
	depth := 1 - fill/valueReserveFraction
	return baselineWeight + depth*(clamp-baselineWeight)
}

// discretionaryFloorOneIn is the discretionary class's guaranteed share of the
// allowance, as one admitted read in this many.
//
// IT IS DERIVED, NOT PICKED. A class holding a share s sweeps marketsKnown
// markets in marketsKnown/(s x rate) seconds, and the rotation already promises
// every market comes round within MaxStaleness = marketsKnown x ValueClampR /
// rate. Setting those equal gives s = 1/ValueClampR exactly: one read in
// ValueClampR is the smallest share that lets the class fund the guarantee ON ITS
// OWN, without borrowing from reads that happen to be taken for other reasons.
// Below it the promise depends on trade traffic passing the right waypoints.
//
// Tying it to ValueClampR rather than to a percentage also keeps it honest at
// every setting of the clamp, since the bound it has to fund is measured in the
// same constant. At a clamp of 1 the value bar collapses to the baseline and
// refuses nothing, so the floor is never consulted.
func discretionaryFloorOneIn(b Budget) int {
	if b.ValueClampR < 1 {
		return 1
	}
	return b.ValueClampR
}

// discretionaryFloorDue reports whether the discretionary class has gone a whole
// floor interval without being served, and so may take a token the value bar
// would otherwise have handed to a market that earns more.
func discretionaryFloorDue(b Budget, req Request) bool {
	return req.Class == Discretionary && req.SpendsSinceDiscretionary >= discretionaryFloorOneIn(b)
}

// Decide admits or declines one market read.
//
// The rules, in the order they are applied and for the reason each is where it
// is:
//
//  1. An Earning read is admitted. It is metered but never denied, because the
//     money guard downstream cannot be satisfied by a cached price.
//  2. A market past MaxStaleness is admitted unconditionally — the
//     anti-starvation escape. It sits ABOVE both the budget check and the
//     value check on purpose: those are the two things that could otherwise
//     keep a cold market waiting forever, and a crushed market that recovered
//     is invisible until someone looks.
//  3. A market not yet due for its interval is served from store. This is the
//     rule that makes total cost independent of rotation size: the interval
//     widens as the rotation grows, so a growing rotation converts reads into
//     cache hits rather than into requests. A Paired read skips this rule alone —
//     a fresh cache is its precondition, not a reason to decline it — and remains
//     subject to everything below.
//  4. With no whole token left, serve from store. This is the hard cap.
//  5. A market not worth the current bar is served from store — the priority
//     ordering under contention — UNLESS the discretionary class is below its
//     floor, in which case this due read takes the token instead.
//
// The floor is placed at rule 5 and nowhere else, and the two rules it sits
// BELOW are the reason it is a fairness rule rather than a weakened guard. It
// cannot reach rule 4, so it never spends a token that does not exist; it cannot
// reach rule 3, so it never buys a read of a market with nothing new to say. All
// it changes is WHICH due read gets a token the budget was going to spend anyway.
//
// Without it the ordering stops being an ordering. Earning reads are never
// denied, so a steady trade stream holds the bucket near the bottom of its range
// where the bar sits near the clamp — and the discretionary class earns nothing
// directly, so it loses that comparison every single time. The class that is
// permanently outbid is precisely the one that opens ground the fleet is not
// already trading, which makes its starvation self-sealing: coverage cannot widen,
// so no new market ever becomes valuable enough to outbid anything.
func Decide(b Budget, req Request) Decision {
	if req.Class == Earning {
		return Spend
	}
	if req.Staleness >= MaxStaleness(b, req.MarketsKnown) {
		return Spend
	}
	if req.Class != Paired && req.Staleness < Interval(b, req.Weight, req.TotalWeight) {
		return ServeFromStore
	}
	if req.TokensAvailable < 1 {
		return ServeFromStore
	}
	if req.Weight < requiredWeight(b, req.TokensAvailable, req.BucketCapacity) && !discretionaryFloorDue(b, req) {
		return ServeFromStore
	}
	return Spend
}

// clampDuration converts seconds to a Duration inside the arithmetic ceiling,
// catching the non-finite and overflowing cases a near-zero rate produces.
func clampDuration(seconds float64) time.Duration {
	if math.IsNaN(seconds) || seconds <= 0 {
		return stalenessCeiling
	}
	if seconds >= stalenessCeiling.Seconds() {
		return stalenessCeiling
	}
	return time.Duration(seconds * float64(time.Second))
}

// Weight is this package's one entry point to value weighting, forwarding to
// the parked rotation's existing ScanWeight so both allocate attention by the
// same rule and respond to the same clamp. Named here so callers of the budget
// never reach into parkedsensing themselves.
func Weight(spreadEWMA, fleetMedianSpread float64, clampR int) float64 {
	return parkedsensing.ScanWeight(spreadEWMA, fleetMedianSpread, clampR)
}

// PriorWeight is the weight a market carries before any spread has been
// observed for it: parkedsensing's optimistic prior, above the baseline so a
// market we have never measured is looked at sooner than one we have measured
// and found dull.
//
// This is also where DISCOVERY lives, and why it needs no separate allowance:
// a newly charted market enters the rotation at the prior, ahead of the dull
// tail, and is funded out of the same fixed budget as everything else.
func PriorWeight(clampR int) float64 {
	return parkedsensing.OptimisticPriorWeight(clampR)
}

// UpdateSpread folds a fresh spread observation into a market's running
// estimate, forwarding to the parked rotation's EWMA so one observation means
// the same thing to both.
func UpdateSpread(prev, observed float64) float64 {
	return parkedsensing.UpdateSpreadEWMA(prev, observed)
}

// Quote is one good's two-sided price at a market, named from the MARKET's side
// of the trade.
//
// THE MAPPING FROM THE PERSISTED COLUMNS IS NOT THE ONE FIELD-NAME MATCHING
// SUGGESTS, and getting it wrong is silent:
//
//	Bid ← market_data.sell_price      (what the market PAYS us — its bid)
//	Ask ← market_data.purchase_price  (what the market CHARGES us — its ask)
//
// The persisted columns are named from OUR side, these from the market's, so
// they are renamed rather than swapped. Wire them by name and every quote comes
// back inverted: SpreadOf rejects each good, every market measures a spread of
// zero, the fleet median collapses to its fallback, and every market lands on
// the same prior weight — the rotation keeps running and reports no error, it
// just stops preferring the markets worth watching. That is why inversions are
// counted and surfaced rather than merely documented; the same guard is what
// surfaced sp-en5h7, where the scanner had persisted both prices transposed for
// the project's entire history.
type Quote struct {
	Bid int
	Ask int
}

// SpreadOf is a market's mean relative bid-ask gap across the goods it quotes:
// (ask-bid)/mid per good, averaged. It also reports how many goods were rejected
// as impossible, which is the caller's cue that something is miswired.
//
// This mirrors the parked rotation's RelativeSpread over a market's whole quote
// sheet rather than a watched-good whitelist, because the budget ranks WHOLE
// markets against each other and has no per-slot whitelist to consult. Relative
// rather than absolute so a cheap good's gap is comparable with an expensive
// one's: the number feeds a weighting that ranks markets, and an absolute gap
// would rank them by price level instead.
//
// Unquoted or non-positively-priced goods are skipped rather than averaged in as
// zeros. A market quoting nothing returns a spread of zero, and that IS the
// intended observation — a market that has stopped quoting should decay out of
// the hot rotation rather than keep the weight its history earned.
func SpreadOf(quotes []Quote) (spread float64, inverted int) {
	var sum float64
	var n int
	for _, q := range quotes {
		bid, ask := float64(q.Bid), float64(q.Ask)
		if bid <= 0 || ask <= 0 {
			// A non-positive side is an unquoted good, not a free one. mid is
			// positive whenever both sides are, so it needs no guard of its own.
			continue
		}
		if ask < bid {
			// No real market sells below what it buys at. Counting this as a
			// negative spread would make a miswired adapter look like a merely
			// uninteresting market, which is the one failure this model cannot
			// detect on its own.
			inverted++
			continue
		}
		sum += (ask - bid) / ((bid + ask) / 2)
		n++
	}
	if n == 0 {
		return 0, inverted
	}
	return sum / float64(n), inverted
}

// FleetMedianSpread is the typical observed spread across measured markets,
// which is the yardstick every market's weight is measured against.
//
// Only MEASURED markets count. An unmeasured market carries a zero it never
// earned, and letting those zeros drag the median down would inflate every
// measured market's weight against a median describing nothing. With nothing
// measured at all the median is 1.0 — not a real spread, but the value that
// makes Weight hand every market the optimistic prior, which is the right
// cold-start behaviour since no market has yet earned preference over another.
func FleetMedianSpread(spreads []float64) float64 {
	measured := make([]float64, 0, len(spreads))
	for _, s := range spreads {
		if s > 0 {
			measured = append(measured, s)
		}
	}
	if len(measured) == 0 {
		return 1.0
	}
	sort.Float64s(measured)

	mid := len(measured) / 2
	if len(measured)%2 == 1 {
		return measured[mid]
	}
	return (measured[mid-1] + measured[mid]) / 2
}

package ship

// Unit tests for the fleet's ONE shipyard-read budget, at the seam
// where policy meets state. What is proven here is the four properties the bead
// asks for: total cost does not grow with the charted map, no known yard starves,
// a money guard's read is never served from store, and attention goes to the yards
// the fleet is about to spend at rather than being spread uniformly the way the
// cadence floor spread it.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

const (
	testYardRate  = 0.12
	testYardClamp = 8
)

var testHeavy = shipyard.NewHeavyShipTypeSet([]string{"SHIP_HEAVY_FREIGHTER"})

// countingYardCounter reports a fixed charted-yard count — the denominator the
// fixed-budget invariant is expressed against.
type countingYardCounter struct {
	count int
	err   error
}

func (c *countingYardCounter) ChartedShipyardCount(context.Context) (int, error) {
	return c.count, c.err
}

// stubYardCatalog is the persisted demand picture a restarted daemon rebuilds
// from: rows for yards known to sell a wanted type, priced or not.
type stubYardCatalog struct {
	rows  []shipyard.ShipTypeAvailability
	err   error
	calls int
}

func (s *stubYardCatalog) ListByTypes(context.Context, int, []string) ([]shipyard.ShipTypeAvailability, error) {
	s.calls++
	return s.rows, s.err
}

func newTestYardBudget(t *testing.T, yardsKnown int) (*YardScanBudget, *time.Time) {
	t.Helper()
	now := time.Now()
	b := NewYardScanBudget(testYardRate, testYardClamp, testHeavy)
	b.setClock(func() time.Time { return now })
	if yardsKnown > 0 {
		b.SetChartedYardCounter(&countingYardCounter{count: yardsKnown})
	}
	return b, &now
}

// drain empties the allowance bucket so the next decision faces a fully contended
// budget — the state every "is this read deniable" question has to be asked in.
func drain(b *YardScanBudget) {
	for i := 0; i < yardBurstRequests+2; i++ {
		b.Debit(testPlayerID, "X1-DRAIN-Y")
	}
}

// testPlayerID is the player every fixture below admits for.
const testPlayerID = 1

// prime forces the two TTL-cached reads the admission path refreshes on its own —
// the charted-yard count and the persisted demand picture.
//
// It is not a convenience. Both are refreshed INSIDE Admit, so a fixture that
// snapshots the budget first reasons about an empty map: it derives an interval
// from a handful of in-memory yards, picks a staleness that looks generous against
// it, and then watches the real decision measure that staleness against a
// three-hundred-yard denominator and call it not-yet-due. The test fails for a
// reason that has nothing to do with what it is testing.
func prime(b *YardScanBudget) {
	b.refreshChartedCount(context.Background())
	b.refreshFacts(context.Background(), testPlayerID)
}

// dueButNotStarved is a staleness inside the window where the PRIORITY ORDERING is
// the only thing that can decide: past a baseline yard's own interval, so dueness
// cannot be the discriminator, and comfortably inside the anti-starvation bound, so
// the escape hatch is not what admits it either.
//
// Getting this window wrong is the classic way a priority test proves nothing. A
// staleness past the bound admits EVERY yard unconditionally, so a broken ordering
// still passes; one short of the interval declines every yard, so does a
// backwards one. Both preconditions are asserted rather than assumed.
func dueButNotStarved(t *testing.T, b *YardScanBudget, now time.Time) time.Time {
	t.Helper()
	prime(b)
	snap := b.Snapshot()
	age := snap.WorstCaseStaleness * 3 / 4
	require.Greater(t, age, snap.TypicalInterval,
		"fixture must make even a baseline yard due, or the decline proves nothing")
	require.Less(t, age, snap.WorstCaseStaleness,
		"fixture must stay inside the bound, or the escape admits everything")
	return now.Add(-age)
}

// --- the invariant: cost does not grow with the map ---------------------------

// THE ORGANISING INVARIANT. A budget that let each yard keep its own interval
// would spend yardsKnown ÷ interval and bind harder every era — which is exactly
// what quartermaster_cadence_secs did, and why masking the incident required
// moving it to a full day. Here a map ten times larger must lengthen each yard's
// interval tenfold, leaving the summed rate put.
func TestYardBudget_ScanIntervalWidensInProportionToTheMap(t *testing.T) {
	small, _ := newTestYardBudget(t, 50)
	large, _ := newTestYardBudget(t, 500)
	prime(small)
	prime(large)

	smallSnap := small.Snapshot()
	largeSnap := large.Snapshot()

	require.Equal(t, 50, smallSnap.YardsKnown)
	require.Equal(t, 500, largeSnap.YardsKnown)
	require.InEpsilon(t, 10.0,
		float64(largeSnap.TypicalInterval)/float64(smallSnap.TypicalInterval), 0.01,
		"ten times the yards must mean ten times the interval, so total spend is unchanged")

	// Stated as the rate itself: yards ÷ interval is the same number on both maps.
	smallRate := float64(smallSnap.YardsKnown) / smallSnap.TypicalInterval.Seconds()
	largeRate := float64(largeSnap.YardsKnown) / largeSnap.TypicalInterval.Seconds()
	require.InEpsilon(t, smallRate, largeRate, 0.01)
}

// --- anti-starvation -----------------------------------------------------------

// The escape hatch the bead asks to be proven. A yard worth nothing to the fleet,
// on a fully contended budget, still comes round: past the worst-case bound it is
// admitted regardless of its value, its dueness or the state of the bucket. Without
// this a dull yard on a large map would never be looked at again, and a yard that
// STARTED selling a heavy would be invisible forever — the failure mode is silent,
// which is why it needs a test rather than an argument.
func TestYardBudget_NoKnownYardStarvesPastTheWorstCaseBound(t *testing.T) {
	b, now := newTestYardBudget(t, 200)
	// A yard scanned and found to sell nothing wanted: the bottom of the ordering.
	b.Observe("X1-COLD-Y1", []shipyard.ShipTypeAvailability{{ShipType: "SHIP_PROBE"}})
	drain(b)

	prime(b)
	bound := b.Snapshot().WorstCaseStaleness
	require.Less(t, bound, 30*24*time.Hour, "the bound must be a real duration, not the arithmetic ceiling")

	// One instant before the bound it is correctly declined — the budget is
	// contended and this yard is worth nothing.
	justInside := now.Add(-bound + time.Second)
	require.Equal(t, marketscan.ServeFromStore,
		b.Admit(context.Background(), testPlayerID, "X1-COLD-Y1", justInside, true, marketscan.Discretionary),
		"a worthless yard on an empty bucket is declined right up to the bound")

	// At the bound it is admitted unconditionally, empty bucket and all.
	atBound := now.Add(-bound)
	require.Equal(t, marketscan.Spend,
		b.Admit(context.Background(), testPlayerID, "X1-COLD-Y1", atBound, true, marketscan.Discretionary),
		"past the bound no yard may be held back, whatever its value or the bucket")
}

// A yard nothing is known about has no store row to serve, so declining it would
// bury it permanently. Never-scanned reads as infinitely stale and is always
// admitted — including on an empty bucket, which is the case a naive token check
// would get wrong.
func TestYardBudget_NeverScannedYardIsAlwaysAdmitted(t *testing.T) {
	b, _ := newTestYardBudget(t, 400)
	drain(b)

	require.Equal(t, marketscan.Spend,
		b.Admit(context.Background(), testPlayerID, "X1-NEW-Y1", time.Time{}, false, marketscan.Discretionary))
}

// A row stamped in the future is bad data, not a fresh reading. Treating it as
// fresh would let one bad timestamp mute a yard for as long as the clock skew
// lasts.
func TestYardBudget_FutureStampReadsAsNeverScanned(t *testing.T) {
	b, now := newTestYardBudget(t, 400)
	drain(b)

	require.Equal(t, marketscan.Spend,
		b.Admit(context.Background(), testPlayerID, "X1-SKEW-Y1", now.Add(time.Hour), true, marketscan.Discretionary))
}

// --- the money guard (RULINGS #4) ----------------------------------------------

// AN EARNING READ IS NEVER SERVED FROM STORE. This is the property the pre-buy
// price verification depends on: a cached hull price cannot satisfy a guard whose
// whole job is to check the price has not moved. The read is put in the worst
// possible position — a just-scanned yard (so it is nowhere near due), worth
// nothing to the rotation, on a fully drained bucket — and must still spend.
func TestYardBudget_EarningReadIsAdmittedInEveryDeniableCondition(t *testing.T) {
	b, now := newTestYardBudget(t, 500)
	b.Observe("X1-BUY-Y1", []shipyard.ShipTypeAvailability{{ShipType: "SHIP_PROBE"}})
	drain(b)

	justScanned := now.Add(-time.Second)

	require.Equal(t, marketscan.ServeFromStore,
		b.Admit(context.Background(), testPlayerID, "X1-BUY-Y1", justScanned, true, marketscan.Discretionary),
		"precondition: these are conditions in which a discretionary read IS declined")

	require.Equal(t, marketscan.Spend,
		b.Admit(context.Background(), testPlayerID, "X1-BUY-Y1", justScanned, true, marketscan.Earning),
		"a money guard's read must never be answered from the store")
}

// Never denied is not the same as free. An Earning read draws from the SAME
// allowance, overdrafting the bucket when it is empty, so pre-buy verification
// squeezes discretionary scanning instead of being added on top of it — which is
// what keeps one number the honest total, and what makes a persistently high
// Forced count the signal to RAISE the budget rather than weaken the guards.
func TestYardBudget_EarningReadStillDebitsTheAllowance(t *testing.T) {
	b, now := newTestYardBudget(t, 500)
	drain(b)
	before := b.Snapshot()

	b.Admit(context.Background(), testPlayerID, "X1-BUY-Y1", now.Add(-time.Second), true, marketscan.Earning)

	after := b.Snapshot()
	require.Equal(t, before.Admitted+1, after.Admitted, "the read is counted against the allowance")
	require.Equal(t, before.Forced+1, after.Forced, "an overdraft on an empty bucket is recorded, not hidden")
}

// --- demand-driven priority ----------------------------------------------------

// THE INCIDENT, AS A DECISION. Two yards, same map, same staleness, same contended
// bucket. One is known to sell SHIP_HEAVY_FREIGHTER and has never been priced —
// one of the 80. The other sells probes. The budget must fund the first and
// decline the second; a cadence floor could not tell them apart, which is how the
// fleet came to buy 24 heavies against prices it could not see.
func TestYardBudget_UnpricedHeavyYardIsFundedWhileADullYardIsDeclined(t *testing.T) {
	b, now := newTestYardBudget(t, 300)
	b.Observe("X1-HEAVY-Y1", []shipyard.ShipTypeAvailability{
		{ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 0}, // catalogued, never priced
	})
	b.Observe("X1-DULL-Y1", []shipyard.ShipTypeAvailability{
		{ShipType: "SHIP_PROBE", PurchasePrice: 40_000},
	})

	// A bucket squeezed into the value reserve: enough for some reads, not all.
	// This is the contention the priority ordering exists to resolve.
	for i := 0; i < yardBurstRequests-2; i++ {
		b.Debit(testPlayerID, "X1-DRAIN-Y")
	}

	// Both are equally due — old enough that dueness cannot be the discriminator.
	due := dueButNotStarved(t, b, *now)

	require.Equal(t, marketscan.Spend,
		b.Admit(context.Background(), testPlayerID, "X1-HEAVY-Y1", due, true, marketscan.Discretionary),
		"a yard known to sell a heavy but never priced is what the allowance is for")
	require.Equal(t, marketscan.ServeFromStore,
		b.Admit(context.Background(), testPlayerID, "X1-DULL-Y1", due, true, marketscan.Discretionary),
		"a probe yard must not take the allowance from an unpriced heavy yard under contention")
}

// A yard a money guard just priced at is the fleet's most valuable read while the
// buy is in flight, so the ROTATION keeps it fresh too — not merely the guard's
// own undeniable reads. Without this the counter we are mid-purchase at decays to
// the baseline the moment the buy loop looks away.
func TestYardBudget_TargetedYardOutranksAnEquallyStaleDullYard(t *testing.T) {
	b, now := newTestYardBudget(t, 300)
	b.Observe("X1-TARGET-Y1", []shipyard.ShipTypeAvailability{{ShipType: "SHIP_PROBE"}})
	b.Observe("X1-DULL-Y1", []shipyard.ShipTypeAvailability{{ShipType: "SHIP_PROBE"}})
	b.NoteTarget("X1-TARGET-Y1")

	for i := 0; i < yardBurstRequests-2; i++ {
		b.Debit(testPlayerID, "X1-DRAIN-Y")
	}
	due := dueButNotStarved(t, b, *now)

	require.Equal(t, marketscan.Spend,
		b.Admit(context.Background(), testPlayerID, "X1-TARGET-Y1", due, true, marketscan.Discretionary))
	require.Equal(t, marketscan.ServeFromStore,
		b.Admit(context.Background(), testPlayerID, "X1-DULL-Y1", due, true, marketscan.Discretionary))
}

// Demand is inferred from the budget's own traffic, so a type nothing has shopped
// for in an hour must stop claiming the allowance — otherwise one historical
// purchase would pin a type wanted for the life of the daemon and the ordering
// would ossify.
func TestYardBudget_DemandForANonHeavyTypeExpires(t *testing.T) {
	b, now := newTestYardBudget(t, 300)
	b.NoteDemand("SHIP_SURVEYOR")
	b.Observe("X1-SURVEY-Y1", []shipyard.ShipTypeAvailability{{ShipType: "SHIP_SURVEYOR"}})

	for i := 0; i < yardBurstRequests-2; i++ {
		b.Debit(testPlayerID, "X1-DRAIN-Y")
	}
	due := dueButNotStarved(t, b, *now)
	require.Equal(t, marketscan.Spend,
		b.Admit(context.Background(), testPlayerID, "X1-SURVEY-Y1", due, true, marketscan.Discretionary),
		"precondition: while the type is wanted the yard is funded")

	// Move past the demand window and re-observe, so the yard is re-weighted
	// against a wanted set the expired type has dropped out of.
	*now = now.Add(yardDemandTTL + time.Minute)
	b.Observe("X1-SURVEY-Y1", []shipyard.ShipTypeAvailability{{ShipType: "SHIP_SURVEYOR"}})
	for i := 0; i < yardBurstRequests-2; i++ {
		b.Debit(testPlayerID, "X1-DRAIN-Y")
	}
	require.Equal(t, marketscan.ServeFromStore,
		b.Admit(context.Background(), testPlayerID, "X1-SURVEY-Y1", dueButNotStarved(t, b, *now), true, marketscan.Discretionary),
		"a type nobody has shopped for in an hour stops claiming the allowance")
}

// A HEAVY TYPE IS WANTED WITHOUT ANYONE ASKING. Structural demand is what makes
// the budget correct on a cold daemon: before any buy loop has spoken, the yards
// that sell the hulls the acquisition path exists to buy are already the top of
// the rotation.
func TestYardBudget_HeavyTypesAreWantedWithNoDemandSignal(t *testing.T) {
	b, now := newTestYardBudget(t, 300)
	b.Observe("X1-HEAVY-Y1", []shipyard.ShipTypeAvailability{{ShipType: "SHIP_HEAVY_FREIGHTER"}})

	for i := 0; i < yardBurstRequests-2; i++ {
		b.Debit(testPlayerID, "X1-DRAIN-Y")
	}
	require.Equal(t, marketscan.Spend,
		b.Admit(context.Background(), testPlayerID, "X1-HEAVY-Y1", dueButNotStarved(t, b, *now), true, marketscan.Discretionary))
}

// THE RESTART CASE, which is the one that makes the store refresh load-bearing
// rather than an optimisation. The 80 starved yards were known to sell a heavy
// only in the DATABASE — a daemon that has just restarted has observed nothing. If
// the budget woke believing no yard sells anything wanted it would weight the whole
// map at the prior and rediscover the ordering only as scans happened to land,
// which is the blindness it exists to prevent.
func TestYardBudget_RebuildsTheDemandPictureFromTheStoreAfterARestart(t *testing.T) {
	b, now := newTestYardBudget(t, 300)
	b.SetYardCatalogReader(&stubYardCatalog{rows: []shipyard.ShipTypeAvailability{
		{WaypointSymbol: "X1-STORE-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 0},
	}})
	// Nothing has been Observed: this budget has seen no scan of its own.
	b.Observe("X1-DULL-Y1", []shipyard.ShipTypeAvailability{{ShipType: "SHIP_PROBE"}})

	for i := 0; i < yardBurstRequests-2; i++ {
		b.Debit(testPlayerID, "X1-DRAIN-Y")
	}
	due := dueButNotStarved(t, b, *now)

	require.Equal(t, marketscan.Spend,
		b.Admit(context.Background(), testPlayerID, "X1-STORE-Y1", due, true, marketscan.Discretionary),
		"a yard the STORE says sells an unpriced heavy must be funded on the first tick after a restart")
	require.Equal(t, marketscan.ServeFromStore,
		b.Admit(context.Background(), testPlayerID, "X1-DULL-Y1", due, true, marketscan.Discretionary))

	snap := b.Snapshot()
	require.Equal(t, 1, snap.YardsWanted)
	require.Equal(t, 1, snap.YardsUnpriced, "the operator-facing count IS the incident's own number")
}

// A counter or catalogue hiccup must not silently unpace the budget or collapse
// the denominator to zero, which would shorten every interval at once — the
// direction that turns a store outage into a request storm.
func TestYardBudget_KeepsThePreviousMapSizeWhenTheCounterFails(t *testing.T) {
	b, now := newTestYardBudget(t, 0)
	counter := &countingYardCounter{count: 250}
	b.SetChartedYardCounter(counter)
	// The count is refreshed on the admission path, not by observing the snapshot.
	b.Admit(context.Background(), testPlayerID, "X1-ANY-Y1", time.Time{}, false, marketscan.Discretionary)
	require.Equal(t, 250, b.Snapshot().YardsKnown)

	// A later refresh fails; the count must hold rather than reset.
	counter.err = context.DeadlineExceeded
	*now = now.Add(yardCountTTL + time.Minute)
	b.Admit(context.Background(), testPlayerID, "X1-ANY-Y1", time.Time{}, false, marketscan.Discretionary)

	require.Equal(t, 250, b.Snapshot().YardsKnown,
		"a failed count leaves the denominator where it was; it never collapses to the yards seen in memory")
}

// There is deliberately no argument that turns pacing off. A misconfigured rate
// resolves to the armed default rather than to "unlimited", because an unpaced
// shipyard reader is the defect the budget exists to fix.
func TestNewYardScanBudget_NonPositiveArgumentsResolveToTheArmedDefaults(t *testing.T) {
	for _, rate := range []float64{0, -1} {
		snap := NewYardScanBudget(rate, 0, testHeavy).Snapshot()
		require.Equal(t, defaultYardBudgetReqPerSec, snap.RateReqPerSec)
		require.Equal(t, defaultYardValueClampR, snap.ValueClampR)
	}
}

// --- the hard cap ---------------------------------------------------------------

// THE ACCEPTANCE CRITERION, MEASURED. Fifty yards, every one of them at the top of
// the ordering (known to sell a heavy, never priced) and permanently due, hammering
// the budget every simulated second for an hour. The value bar therefore cannot be
// what limits them and neither can dueness — only the allowance can.
//
// Shipyard reads were measured at 0.844 req/s, 44.7% of a 2.00 req/s ceiling that
// cannot be raised. What this asserts is that the configured rate is a real
// ceiling on the deniable bulk of that traffic rather than a target it drifts
// around: over the window the fleet may spend rate x seconds, plus at most one
// bucket of burst, and not one request more.
func TestYardBudget_SustainedAdmissionsStayInsideTheConfiguredRate(t *testing.T) {
	const yards = 50
	b, now := newTestYardBudget(t, 200)
	for i := 0; i < yards; i++ {
		b.Observe(hotYard(i), []shipyard.ShipTypeAvailability{
			{ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 0},
		})
	}
	prime(b)

	// Permanently due, never starved: staleness is held constant as the clock moves,
	// so the anti-starvation escape (which is deliberately uncapped) can never be
	// what admits a read and inflate the count.
	snap := b.Snapshot()
	age := snap.WorstCaseStaleness * 3 / 4
	require.Greater(t, age, snap.TypicalInterval)
	require.Less(t, age, snap.WorstCaseStaleness)

	const window = time.Hour
	admitted := 0
	for elapsed := time.Duration(0); elapsed < window; elapsed += time.Second {
		*now = now.Add(time.Second)
		for i := 0; i < yards; i++ {
			if b.Admit(context.Background(), testPlayerID, hotYard(i), now.Add(-age), true, marketscan.Discretionary) == marketscan.Spend {
				admitted++
			}
		}
	}

	ceiling := int(testYardRate*window.Seconds()) + yardBurstRequests
	require.LessOrEqual(t, admitted, ceiling,
		"sustained shipyard reads must not exceed the configured allowance plus one bucket of burst")
	require.Greater(t, admitted, ceiling/2,
		"and the allowance must actually be SPENT, or this would pass on a budget that admits nothing")
}

func hotYard(i int) string { return "X1-HOT-Y" + strconv.Itoa(i) }

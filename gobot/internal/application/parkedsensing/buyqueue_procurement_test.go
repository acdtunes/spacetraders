package parkedsensing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
)

// errTestPriceTable stands for the local store refusing the bulk price read.
var errTestPriceTable = errors.New("shipyard_inventory unreadable")

// --- the buy queue buys at the CHEAPEST counter in reach ---------------------
//
// EVERY FIXTURE HERE IS A TWO-COUNTER MAP: an expensive yard in the placement's
// own system, and a cheaper one exactly one gate away holding a hull of ours. One
// gate, because that is the distance the ferry arithmetic has to discriminate at —
// the gate fee plus the per-jump penalty set an indifference threshold, and every
// price pair below is chosen to sit deliberately on one side of it.

// procurementGates is the one-gate map: the cheap system can reach the placement,
// and X1-FAR is charted but connected to nothing, so it can hold the fleet's
// cheapest ask without ever being a candidate.
func procurementGates() *fakeGates {
	return &fakeGates{adjacency: map[string][]string{
		"X1-CHEAP": {"X1-TGT"},
		"X1-TGT":   {},
		"X1-FAR":   {},
	}}
}

// fakeProbeAsks is the stored yard-price snapshot. It records its call count
// because the whole design rests on ONE bulk read per tick rather than one per
// placement.
type fakeProbeAsks struct {
	asks  []ProbeAsk
	err   error
	calls int
}

func (f *fakeProbeAsks) ProbeAsks(_ context.Context, _ int) ([]ProbeAsk, error) {
	f.calls++
	if f.err != nil {
		// Adversarial, like every other fake here: a usable snapshot alongside the
		// error, so a caller that ignores err ranks on prices it never legitimately
		// read.
		return f.asks, f.err
	}
	return f.asks, nil
}

// procurementPorts builds the two-counter map: a WANTED market placement in
// X1-TGT whose own system holds a yard asking `localAsk`, and a yard one gate
// away in X1-CHEAP asking `cheapAsk`. BOTH have a hull of ours standing at them,
// so the choice between them is a choice about PRICE and nothing else.
func procurementPorts(t *testing.T, localAsk, cheapAsk, treasury int64) (BuyPorts, *fakePurchaser, *fakeBuyLedger, *fakeProbeAsks) {
	t.Helper()
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-TGT-M1", System: "X1-TGT", Kind: SlotKindMarket, State: SlotStateWanted, DepthCredits: 900},
			// Our presence in the cheap system: what makes X1-TGT reachable at all, and
			// what a real fleet's frontier yard looks like — a probe already parked on it.
			{Waypoint: "X1-CHEAP-M1", System: "X1-CHEAP", Kind: SlotKindMarket, State: SlotStateParked,
				AssignedShip: "CHEAP-PROBE"},
		},
		systems: []ScreenedSystem{{System: "X1-TGT", DepthCredits: 900}},
	}
	pur := &fakePurchaser{priceAt: map[string]int64{
		"X1-TGT-Y1":   localAsk,
		"X1-CHEAP-Y1": cheapAsk,
	}}
	asks := &fakeProbeAsks{asks: []ProbeAsk{
		freshAsk("X1-TGT-Y1", "X1-TGT", localAsk),
		freshAsk("X1-CHEAP-Y1", "X1-CHEAP", cheapAsk),
	}}
	return BuyPorts{
		Treasury:   &fakeTreasury{credits: treasury},
		CargoSpend: &fakeCargoSpend{},
		Purchaser:  pur,
		Ledger:     led,
		Yards: &fakeYards{yards: map[string][]string{
			"X1-TGT":   {"X1-TGT-Y1"},
			"X1-CHEAP": {"X1-CHEAP-Y1"},
		}},
		Ships: &fakeShipReader{docked: map[string]string{
			"X1-TGT-Y1":   "TGT-BUYER",
			"X1-CHEAP-Y1": "CHEAP-BUYER",
		}},
		Fleet: &fakeFleet{},
		Gates: procurementGates(),
		Asks:  asks,
	}, pur, led, asks
}

// procurementNow is the tick's clock. Every fresh reading is stamped a minute
// before it, comfortably inside the freshness window; the stale fixtures stamp
// theirs a day before.
var procurementNow = time.Unix(1_700_000_000, 0)

func freshAsk(yard, system string, price int64) ProbeAsk {
	return ProbeAsk{Yard: yard, System: system, Price: price, ScannedAt: procurementNow.Add(-time.Minute)}
}

// procurementKnobs leave the buy floor at the immutable 50,000 exactly and take
// both procurement defaults from code, which is the RULINGS #22 configuration:
// no config present at all, and the feature still runs.
var procurementKnobs = BuyKnobs{SpendEnabled: true, ProbeCap: 100, CapexReserve: 0, KMilli: 0}

// drainProcurement runs one tick against the fixture's clock and fails the test
// on any error, so each case below reads as its assertions and nothing else.
func drainProcurement(t *testing.T, ports BuyPorts) BuyReport {
	t.Helper()
	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, procurementKnobs, fixedClock{procurementNow})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	return rep
}

// boughtAt names the single counter this tick transacted at, failing when the
// tick bought a number of hulls other than one.
func boughtAt(t *testing.T, pur *fakePurchaser, rep BuyReport) string {
	t.Helper()
	if len(pur.buys) != 1 {
		t.Fatalf("bought %d probes, want exactly 1 (buys %v, report %+v)", len(pur.buys), pur.buys, rep)
	}
	return pur.buys[0].yard
}

// The acceptance case: the local counter's ask has run away while a yard one gate
// out still sells at the frontier rate. The treasury covers either, so a purchase
// at the local yard can only be the queue failing to look past its own system.
func TestDrain_BuysAtTheCheapCounterOneGateAwayRatherThanTheRunawayYardUnderfoot(t *testing.T) {
	ports, pur, _, asks := procurementPorts(t, 249_000, 25_000, 400_000)

	rep := drainProcurement(t, ports)

	if yard := boughtAt(t, pur, rep); yard != "X1-CHEAP-Y1" {
		t.Fatalf("bought at %s, want X1-CHEAP-Y1 — the local counter asks 249,000 and the one "+
			"gate away asks 25,000 (landed %d). Buying underfoot is the price-blind order "+
			"(report %+v)",
			yard,
			domainSensing.LandedYardCost(25_000, 1, domainSensing.DefaultGateFeeCredits, domainSensing.DefaultJumpPenaltyCredits),
			rep)
	}
	if rep.Ferried != 1 {
		t.Fatalf("Ferried = %d, want 1 — the hull was bought in another system and has to be flown "+
			"to its post, and the accounting must say so (report %+v)", rep.Ferried, rep)
	}
	if asks.calls != 1 {
		t.Fatalf("the stored yard prices were read %d times, want exactly 1 — the snapshot is per "+
			"TICK, and a per-placement read would put the price table on the drain's hot path", asks.calls)
	}
}

// The other half of the tradeoff, and what stops the ranking from becoming "always
// buy far away": the saving is smaller than the cost of crossing for it.
func TestDrain_KeepsTheLocalCounterWhenTheFerryOutweighsTheSaving(t *testing.T) {
	ports, pur, _, _ := procurementPorts(t, 30_000, 25_000, 400_000)

	rep := drainProcurement(t, ports)

	if yard := boughtAt(t, pur, rep); yard != "X1-TGT-Y1" {
		t.Fatalf("bought at %s, want X1-TGT-Y1 — the distant counter is 5,000 cheaper and costs "+
			"7,900 to reach, so crossing loses 2,900 a hull (report %+v)", yard, rep)
	}
	if rep.Ferried != 0 {
		t.Fatalf("Ferried = %d, want 0 — the purchase was made in the placement's own system", rep.Ferried)
	}
}

// The same fixture one step past the indifference point. Paired with the test
// above, the two pin the THRESHOLD rather than a direction — a change to the ferry
// model that zeroed the penalty, or one that priced a hop at a whole probe, breaks
// exactly one of them.
func TestDrain_CrossesAGateWhenTheSavingExceedsTheFerry(t *testing.T) {
	ports, pur, _, _ := procurementPorts(t, 40_000, 25_000, 400_000)

	rep := drainProcurement(t, ports)

	if yard := boughtAt(t, pur, rep); yard != "X1-CHEAP-Y1" {
		t.Fatalf("bought at %s, want X1-CHEAP-Y1 — 15,000 saved for a 7,900 crossing (report %+v)", yard, rep)
	}
}

// --- the walk-away -----------------------------------------------------------

// walkAwayPorts puts the fleet's cheapest ask in X1-FAR, which the gate map
// connects to nothing: the reference the multiple is measured against is real and
// fresh, and entirely out of reach, which is precisely the shape that makes a
// hold correct rather than merely conservative.
func walkAwayPorts(t *testing.T, localAsk, cheapAsk int64) (BuyPorts, *fakePurchaser, *fakeBuyLedger) {
	t.Helper()
	ports, pur, led, asks := procurementPorts(t, localAsk, cheapAsk, 400_000)
	asks.asks = append(asks.asks, freshAsk("X1-FAR-Y1", "X1-FAR", 20_000))
	return ports, pur, led
}

// Both counters in reach ask above the ceiling the default multiple draws over the
// cheapest fresh ask, so the placement is held rather than filled at that price.
// THE TREASURY COVERS BOTH, deliberately: a refusal that could equally be explained
// by the buy floor would prove nothing about this guard.
func TestDrain_HoldsThePlacementWhenEveryCounterInReachBreachesTheWalkAway(t *testing.T) {
	ports, pur, led := walkAwayPorts(t, 100_000, 120_000)

	rep := drainProcurement(t, ports)

	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes above the walk-away ceiling of 60,000 (3 x the fleet's cheapest "+
			"fresh ask, 20,000), want 0: %v", len(pur.buys), pur.buys)
	}
	if rep.WalkAwayHeld != 1 {
		t.Fatalf("WalkAwayHeld = %d, want 1 — a placement held for PRICE must be reported as such, "+
			"or an operator cannot tell it from a placement nothing could serve (report %+v)", rep.WalkAwayHeld, rep)
	}
	if rep.SkippedNoYard != 0 {
		t.Fatalf("SkippedNoYard = %d, want 0 — a counter existed and could have transacted; the "+
			"queue refused its price, which is a different fact (report %+v)", rep.SkippedNoYard, rep)
	}
	if claims := led.transitionsTo(SlotStateQueued); len(claims) != 0 {
		t.Fatalf("the placement was claimed for purchase %d times, want 0 — a held placement must "+
			"stay WANTED so a later tick with a cheaper counter can fill it: %v", len(claims), claims)
	}
}

// The positive control for the hold above: same reference, same ceiling, one
// compliant counter. A guard that held here would be starving the queue.
func TestDrain_BuysAtTheOneCounterInsideTheWalkAway(t *testing.T) {
	ports, pur, _ := walkAwayPorts(t, 100_000, 50_000)

	rep := drainProcurement(t, ports)

	if yard := boughtAt(t, pur, rep); yard != "X1-CHEAP-Y1" {
		t.Fatalf("bought at %s, want X1-CHEAP-Y1 — it is the only counter in reach under the "+
			"60,000 ceiling (report %+v)", yard, rep)
	}
	if rep.WalkAwayHeld != 0 {
		t.Fatalf("WalkAwayHeld = %d, want 0 — the placement was filled (report %+v)", rep.WalkAwayHeld, rep)
	}
}

// The tooth a stale reading cannot fool. Both stored asks are fresh and compliant,
// so the ranking correctly asks the local counter first — and it then bills ten
// times its recorded price. A yard whose ask runs away BETWEEN readings is what the
// stored-price pre-filter structurally cannot catch, so the guard binds on the bill
// and the next candidate on the list serves the placement.
func TestDrain_WalksAwayFromACounterWhoseLIVEQuoteBreachesTheCeiling(t *testing.T) {
	ports, pur, _, _ := procurementPorts(t, 30_000, 25_000, 400_000)
	ports.Purchaser.(*fakePurchaser).priceAt["X1-TGT-Y1"] = 300_000

	rep := drainProcurement(t, ports)

	if yard := boughtAt(t, pur, rep); yard != "X1-CHEAP-Y1" {
		t.Fatalf("bought at %s, want X1-CHEAP-Y1 — the local counter quoted 300,000 against a "+
			"75,000 ceiling and must be walked away from (report %+v)", yard, rep)
	}
	var walkAways int
	for _, refusal := range rep.Refusals {
		if refusal.Step == BuyStepWalkAway && refusal.Yard == "X1-TGT-Y1" {
			walkAways++
		}
	}
	if walkAways != 1 {
		t.Fatalf("the walk-away at X1-TGT-Y1 was not reported as one: refusals %+v. US refusing "+
			"THEM must read differently from a counter refusing us", rep.Refusals)
	}
}

// --- failing open ------------------------------------------------------------

// The fail-open direction. Every reading is a day old, so nothing is comparable,
// and the queue must go on buying as it does with no ranking at all — naming the
// cause once. THE DIRECTION IS THE POINT: this machinery can only refuse or reorder
// a purchase, so its absence weakens no money guard, while failing CLOSED would
// stop probe buying fleet-wide on an unreadable LOCAL table.
func TestDrain_FallsBackToTheNearestCounterWhenEveryStoredPriceIsStale(t *testing.T) {
	ports, pur, _, asks := procurementPorts(t, 249_000, 25_000, 400_000)
	for i := range asks.asks {
		asks.asks[i].ScannedAt = procurementNow.Add(-24 * time.Hour)
	}
	logger := &recordingPlacementLogger{}

	rep, err := DrainBuyQueue(logging.WithLogger(context.Background(), logger), ports, testPlayerID,
		procurementKnobs, fixedClock{procurementNow})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}

	if yard := boughtAt(t, pur, rep); yard != "X1-TGT-Y1" {
		t.Fatalf("bought at %s, want X1-TGT-Y1 — with no comparable price the drain must fall back "+
			"to the nearest-first order it had before this bead, not stop buying (report %+v)", yard, rep)
	}
	if rep.WalkAwayHeld != 0 {
		t.Fatalf("WalkAwayHeld = %d, want 0 — a stale snapshot is not a refusal, and a ceiling "+
			"derived from unread prices would refuse hardest exactly when the fleet knows least", rep.WalkAwayHeld)
	}
	named := logger.withAction("parked_sensing_procurement_unavailable")
	if len(named) != 1 {
		t.Fatalf("the fall-back was named %d times, want exactly 1: a silent degradation to "+
			"nearest-first is indistinguishable from the bug (lines %+v)", len(named), logger.lines)
	}
	if named[0].metadata["cause"] != "all_stale" {
		t.Fatalf("the WARN blames %v, want all_stale — the operator response to a stale price table "+
			"is not the response to an unwired port", named[0].metadata["cause"])
	}
}

// TestDrain_FallsBackWhenTheStoredPricesCannotBeRead is the same fail-open on the
// other cause. The fake hands back a usable snapshot ALONGSIDE its error, so a
// caller that swallowed the error would rank happily on prices it never
// legitimately read — and would pass every assertion above.
func TestDrain_FallsBackWhenTheStoredPricesCannotBeRead(t *testing.T) {
	ports, pur, _, asks := procurementPorts(t, 249_000, 25_000, 400_000)
	asks.err = errTestPriceTable
	logger := &recordingPlacementLogger{}

	rep, err := DrainBuyQueue(logging.WithLogger(context.Background(), logger), ports, testPlayerID,
		procurementKnobs, fixedClock{procurementNow})
	if err != nil {
		t.Fatalf("an unreadable LOCAL price table must not fail the tick: %v", err)
	}
	if yard := boughtAt(t, pur, rep); yard != "X1-TGT-Y1" {
		t.Fatalf("bought at %s, want X1-TGT-Y1 (the nearest-first fallback) — %v", yard, rep)
	}
	named := logger.withAction("parked_sensing_procurement_unavailable")
	if len(named) != 1 || named[0].metadata["cause"] != "prices_unreadable" {
		t.Fatalf("the unreadable price table was not named once as prices_unreadable: %+v", logger.lines)
	}
}

// The nil port. Every other fixture in this package leaves Asks unset, so this
// states as an assertion what those tests prove by staying green: no snapshot, no
// ranking, no ceiling, and the local counter is bought.
func TestDrain_AnUnwiredPriceReaderIsExactlyTheOldBehaviour(t *testing.T) {
	ports, pur, _, _ := procurementPorts(t, 249_000, 25_000, 400_000)
	ports.Asks = nil

	rep := drainProcurement(t, ports)

	if yard := boughtAt(t, pur, rep); yard != "X1-TGT-Y1" {
		t.Fatalf("bought at %s, want X1-TGT-Y1 — an unwired price reader must be byte-identical to "+
			"the ordering that existed before it (report %+v)", yard, rep)
	}
}

// --- the tunables ------------------------------------------------------------

// The RULINGS #22 assertion at the point the values are USED: an all-zero BuyKnobs
// — what a coordinator launched with no config resolves to — must yield the
// documented defaults, not zeros. A zero multiple would refuse every counter
// including the cheapest; a zero window would mark every reading stale.
func TestProcurementKnobs_DefaultsApplyWithNoConfig(t *testing.T) {
	b := newProcurementBroker(BuyKnobs{})

	if got := b.walkAwayMult(); got != domainSensing.DefaultWalkAwayMult {
		t.Fatalf("walk-away multiple with no config = %d, want %d", got, domainSensing.DefaultWalkAwayMult)
	}
	if got := b.jumpPenalty(); got != domainSensing.DefaultJumpPenaltyCredits {
		t.Fatalf("jump penalty with no config = %d, want %d", got, domainSensing.DefaultJumpPenaltyCredits)
	}
	if got := b.freshness(); got != defaultProbeAskFreshness {
		t.Fatalf("ask freshness with no config = %v, want %v", got, defaultProbeAskFreshness)
	}
}

// The SAME fixture the ranking crosses a gate on, with the jump penalty tuned high
// enough to price the crossing out. A knob that reached the struct and not the
// decision would leave this indistinguishable from the default.
func TestProcurementKnobs_OperatorValuesReachTheDecision(t *testing.T) {
	ports, pur, _, _ := procurementPorts(t, 40_000, 25_000, 400_000)
	knobs := procurementKnobs
	// 40,000 − 25,000 = 15,000 of saving; 5,900 of gate fee leaves 9,100, so a
	// 10,000 penalty is the first whole value that makes the crossing a loss.
	knobs.JumpPenaltyCredits = 10_000

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, knobs, fixedClock{procurementNow})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if yard := boughtAt(t, pur, rep); yard != "X1-TGT-Y1" {
		t.Fatalf("bought at %s, want X1-TGT-Y1 — at a 10,000 per-jump penalty the crossing costs "+
			"15,900 to save 15,000, so the tuned value must move the decision (report %+v)", yard, rep)
	}
}

// The same for the refusal half: the fixture the default multiple buys at, refused
// at a multiple of 1, where only the cheapest band is acceptable.
func TestProcurementKnobs_ATighterWalkAwayMultipleBinds(t *testing.T) {
	ports, pur, _ := walkAwayPorts(t, 100_000, 50_000)
	knobs := procurementKnobs
	knobs.WalkAwayMult = 1 // ceiling 20,000: nothing in reach qualifies

	rep, err := DrainBuyQueue(context.Background(), ports, testPlayerID, knobs, fixedClock{procurementNow})
	if err != nil {
		t.Fatalf("DrainBuyQueue returned error: %v", err)
	}
	if len(pur.buys) != 0 {
		t.Fatalf("bought %d probes at a 1x walk-away multiple, want 0: %v", len(pur.buys), pur.buys)
	}
	if rep.WalkAwayHeld != 1 {
		t.Fatalf("WalkAwayHeld = %d, want 1 (report %+v)", rep.WalkAwayHeld, rep)
	}
}

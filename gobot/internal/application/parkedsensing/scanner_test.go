package parkedsensing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- pure pacing -------------------------------------------------------------
//
// Everything below drives nextAction against a MockClock with no goroutines at
// all. The pacer's scheduling decisions are therefore pinned as arithmetic —
// which waypoint comes round, and how long until the next one does — separately
// from the concurrency that executes them.

// scanEpoch is the fixed instant every pacing test starts from.
var scanEpoch = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

func scanClock() *shared.MockClock {
	return &shared.MockClock{CurrentTime: scanEpoch}
}

// parkedMarket is a market slot in the rotation: parked, whitelisted, with a
// spread history.
func parkedMarket(waypoint string, spread float64, lastScan time.Time) SensingSlotView {
	return SensingSlotView{
		Waypoint:   waypoint,
		Kind:       SlotKindMarket,
		State:      SlotStateParked,
		Whitelist:  []string{"FUEL"},
		SpreadEWMA: spread,
		LastScan:   lastScan,
	}
}

// pacingScanner builds a Scanner with no ports at all. nextAction touches none
// of them, so a nil-ported scanner is exactly the right instrument for pinning
// the arithmetic.
func pacingScanner(clock shared.Clock, clampR int) *Scanner {
	return NewScanner(1, ScanPorts{}, clock, ScanKnobs{InflightCap: 1, ClampR: clampR})
}

// TestScannerSyncMembership_AdmitsOnlyParkedMarketsAndYards pins the membership
// rule. SPARE is excluded because a spare hull is a probe we own but is not
// standing anywhere we asked for prices — scanning it would pay for readings
// nothing planned to use.
//
// That exclusion also keeps MarkScanned (a scan-path write) and TransitionSlot (a
// placement-path write) off the same row, which USED to be the only thing
// preventing a lost update between them. It no longer is: the ledger's writers
// own disjoint columns now (sp-wgjb7), so this is defence in depth rather than
// the guard itself.
func TestScannerSyncMembership_AdmitsOnlyParkedMarketsAndYards(t *testing.T) {
	clock := scanClock()
	sc := pacingScanner(clock, 4)

	spare := parkedMarket("X1-AA-SPARE", 0.2, scanEpoch)
	spare.Kind = SlotKindSpare
	inTransit := parkedMarket("X1-AA-FLYING", 0.2, scanEpoch)
	inTransit.State = SlotStateInTransit
	yard := parkedMarket("X1-AA-YARD", 0, scanEpoch)
	yard.Kind = SlotKindYard
	yard.Whitelist = nil

	sc.SyncMembership([]SensingSlotView{
		parkedMarket("X1-AA-M1", 0.2, scanEpoch),
		yard,
		spare,
		inTransit,
	}, 1.0)

	got := sc.pendingWaypoints()
	want := map[string]bool{"X1-AA-M1": true, "X1-AA-YARD": true}
	if len(got) != len(want) {
		t.Fatalf("rotation = %v, want exactly %v", got, want)
	}
	for _, wp := range got {
		if !want[wp] {
			t.Errorf("waypoint %q must not be in the scan rotation", wp)
		}
	}
}

// TestScannerNextAction_WeightedRotationFavoursHotSlots is the headline pacing
// property: a slot whose spread is four times the fleet median comes round four
// times as often. Spreads 0.4/0.1/0.1 give a median of 0.1, so the weights are
// {4,1,1} at clampR 4.
func TestScannerNextAction_WeightedRotationFavoursHotSlots(t *testing.T) {
	clock := scanClock()
	sc := pacingScanner(clock, 4)

	spreads := map[string]float64{"X1-AA-HOT": 0.4, "X1-AA-C1": 0.1, "X1-AA-C2": 0.1}
	sc.SyncMembership([]SensingSlotView{
		parkedMarket("X1-AA-HOT", spreads["X1-AA-HOT"], scanEpoch),
		parkedMarket("X1-AA-C1", spreads["X1-AA-C1"], scanEpoch),
		parkedMarket("X1-AA-C2", spreads["X1-AA-C2"], scanEpoch),
	}, 1.0)

	counts := map[string]int{}
	const wantPops = 100
	for pops, steps := 0, 0; pops < wantPops; steps++ {
		if steps > 10*wantPops {
			t.Fatalf("pacer stalled after %d steps with only %d pops", steps, pops)
		}
		waypoint, sleepFor, ok := sc.nextAction(clock.Now())
		if !ok {
			clock.Advance(sleepFor)
			continue
		}
		counts[waypoint]++
		pops++
		sc.requeue(waypoint, clock.Now(), spreads[waypoint])
	}

	hot, c1, c2 := counts["X1-AA-HOT"], counts["X1-AA-C1"], counts["X1-AA-C2"]
	if c1 == 0 || c2 == 0 {
		t.Fatalf("a cold slot was starved out of the rotation: %v", counts)
	}
	if c1 < c2-1 || c1 > c2+1 {
		t.Errorf("equally-weighted slots must share the rotation evenly: %v", counts)
	}
	if ratio := float64(hot) / float64(c1); ratio < 3.5 || ratio > 4.5 {
		t.Errorf("hot:cold pop ratio = %.2f, want ~4 (weights {4,1,1}): %v", ratio, counts)
	}
}

// TestScannerNextAction_HalvingTheRateDoublesTheInterval pins the other half of
// the pacing contract: the budget the reconcile hands in scales the whole
// rotation. One slot isolates the rate — its weight cancels against the total,
// so the interval is exactly 1/rate.
func TestScannerNextAction_HalvingTheRateDoublesTheInterval(t *testing.T) {
	clock := scanClock()
	sc := pacingScanner(clock, 4)
	only := []SensingSlotView{parkedMarket("X1-AA-M1", 0.2, scanEpoch)}

	sc.SyncMembership(only, 1.0)
	_, full, ok := sc.nextAction(clock.Now())
	if ok {
		t.Fatal("a slot scanned this instant must not be due again immediately")
	}

	sc.SyncMembership(only, 0.5)
	_, halved, ok := sc.nextAction(clock.Now())
	if ok {
		t.Fatal("re-rating must not make a just-scanned slot due")
	}

	if full != time.Second {
		t.Errorf("interval at rate 1.0 = %s, want 1s", full)
	}
	if halved != 2*full {
		t.Errorf("interval at rate 0.5 = %s, want double %s", halved, full)
	}
}

// TestScannerNextAction_ColdSlotIsDueImmediately covers the cold start: a slot
// with no recorded scan is due the moment it enters the rotation, so a restart
// samples everything it is watching before it starts pacing.
func TestScannerNextAction_ColdSlotIsDueImmediately(t *testing.T) {
	clock := scanClock()
	sc := pacingScanner(clock, 4)
	sc.SyncMembership([]SensingSlotView{parkedMarket("X1-AA-M1", 0, time.Time{})}, 1.0)

	waypoint, _, ok := sc.nextAction(clock.Now())
	if !ok || waypoint != "X1-AA-M1" {
		t.Fatalf("nextAction = (%q, ok=%v), want the never-scanned slot due now", waypoint, ok)
	}
}

// TestScannerNextAction_EmptyRotationSleeps pins the degenerate case: with
// nothing to scan the pacer parks on a poll rather than spinning, and waits for
// the next reconcile to hand it members.
func TestScannerNextAction_EmptyRotationSleeps(t *testing.T) {
	clock := scanClock()
	sc := pacingScanner(clock, 4)

	waypoint, sleepFor, ok := sc.nextAction(clock.Now())
	if ok || waypoint != "" {
		t.Fatalf("nextAction on an empty rotation = (%q, ok=%v), want no work", waypoint, ok)
	}
	if sleepFor != emptyRotationPoll {
		t.Errorf("sleep = %s, want the empty-rotation poll %s", sleepFor, emptyRotationPoll)
	}
}

// TestScannerNextAction_YardHoldsTheQuartermasterCadence pins the yard floor. A
// yard earns weight 1 and is additionally held to the quartermaster's cadence,
// so shipyard prices are not re-read faster than the buyer can act on them.
func TestScannerNextAction_YardHoldsTheQuartermasterCadence(t *testing.T) {
	clock := scanClock()
	sc := pacingScanner(clock, 4)

	yard := parkedMarket("X1-AA-YARD", 0, scanEpoch.Add(-2*time.Second))
	yard.Kind = SlotKindYard
	yard.Whitelist = nil
	yard.YardCadence = time.Minute
	sc.SyncMembership([]SensingSlotView{yard}, 1.0)

	waypoint, sleepFor, ok := sc.nextAction(clock.Now())
	if ok {
		t.Fatalf("yard %q scanned inside its cadence floor", waypoint)
	}
	// Weight 1 alone would have made it due 1s after its last scan, i.e. one
	// second ago. The cadence is what holds it back.
	if want := 58 * time.Second; sleepFor != want {
		t.Errorf("sleep = %s, want %s (last scan + 60s cadence)", sleepFor, want)
	}
}

// TestScannerNextAction_YardFloorDoesNotBlockADueMarket guards the pacer's one
// real hazard: the heap orders on the UNFLOORED due time, so a yard can surface
// as the heap minimum while a market behind it is genuinely due. Sleeping on the
// yard would idle the whole rotation for a cadence.
//
// The numbers construct exactly that ordering. Neither slot has a spread, so the
// market carries the optimistic prior (weight 2) and the yard its fixed 1,
// totalling 3 at rate 1.0 — a 3s interval for the yard and 1.5s for the market.
// The yard's unfloored due is therefore 7s BEFORE the market's, which puts it at
// the head of the heap, while its hour-long cadence holds its real due an hour
// out. The market is half a second overdue.
func TestScannerNextAction_YardFloorDoesNotBlockADueMarket(t *testing.T) {
	clock := scanClock()
	sc := pacingScanner(clock, 4)

	yard := parkedMarket("X1-AA-YARD", 0, scanEpoch.Add(-10*time.Second))
	yard.Kind = SlotKindYard
	yard.Whitelist = nil
	yard.YardCadence = time.Hour
	market := parkedMarket("X1-AA-M1", 0, scanEpoch.Add(-2*time.Second))
	sc.SyncMembership([]SensingSlotView{yard, market}, 1.0)

	// Precondition: the floored yard really is the heap minimum, or the test
	// would pass without exercising the sweep at all.
	if head := sc.pendingWaypoints()[0]; head != "X1-AA-YARD" {
		t.Fatalf("heap head = %q, want the yard so the floor is actually in the way", head)
	}

	waypoint, sleepFor, ok := sc.nextAction(clock.Now())
	if !ok || waypoint != "X1-AA-M1" {
		t.Fatalf("nextAction = (%q, %s, ok=%v), want the due market scanned past the floored yard",
			waypoint, sleepFor, ok)
	}
}

// --- concurrency -------------------------------------------------------------
//
// The scanner is the only part of the sensing model with real concurrency: a
// pacer goroutine, a bounded worker pool, and one rotation shared between them.
// Everything below is run under -race.

type fakeScanRunner struct {
	mu    sync.Mutex
	calls []string

	// started signals each scan as it begins, and hold (when non-nil) blocks it
	// there until the test releases it — which is how "in flight" is made
	// observable without sleeping on it.
	started chan string
	hold    chan struct{}

	err     error
	panicAt string
}

func (f *fakeScanRunner) Run(_ context.Context, _ int, waypoint string) error {
	f.mu.Lock()
	f.calls = append(f.calls, waypoint)
	f.mu.Unlock()

	if f.started != nil {
		f.started <- waypoint
	}
	if f.hold != nil {
		<-f.hold
	}
	if f.panicAt == waypoint {
		panic("scan exploded")
	}
	return f.err
}

func (f *fakeScanRunner) scanned() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type markScannedCall struct {
	waypoint string
	at       time.Time
	spread   float64
}

type fakeScanLedger struct {
	mu    sync.Mutex
	calls []markScannedCall
	err   error
}

func (f *fakeScanLedger) MarkScanned(_ context.Context, _ int, waypoint, _ string, at time.Time, spreadEWMA float64) error {
	f.mu.Lock()
	f.calls = append(f.calls, markScannedCall{waypoint, at, spreadEWMA})
	f.mu.Unlock()
	return f.err
}

func (f *fakeScanLedger) marks() []markScannedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]markScannedCall(nil), f.calls...)
}

type fakeSpreadObserver struct {
	mu     sync.Mutex
	calls  []string
	prices []GoodPrice
	err    error
}

func (f *fakeSpreadObserver) MarketPrices(_ context.Context, _ int, waypoint string) ([]GoodPrice, error) {
	f.mu.Lock()
	f.calls = append(f.calls, waypoint)
	f.mu.Unlock()
	return f.prices, f.err
}

func (f *fakeSpreadObserver) reads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// scanFixture wires a scanner over controllable fakes.
func scanFixture(t *testing.T, inflightCap int, runner *fakeScanRunner) (*Scanner, *fakeScanLedger, *fakeSpreadObserver) {
	t.Helper()
	ledger := &fakeScanLedger{}
	spreads := &fakeSpreadObserver{prices: []GoodPrice{{Good: "FUEL", Bid: 90, Ask: 110}}}
	sc := NewScanner(1, ScanPorts{Scan: runner, Ledger: ledger, SpreadOf: spreads}, scanClock(),
		ScanKnobs{InflightCap: inflightCap, ClampR: 4})
	return sc, ledger, spreads
}

// takeAndLaunch performs exactly what the pacer loop does for one slot — pop it
// out of the rotation, then run it on a worker — without a pacer goroutine, so a
// single scan can be driven deterministically.
//
// It aborts with t.Fatal, which is the CORRECT abort for its callers: almost all
// of them run on the main test goroutine and follow the call with a blocking
// `<-started` that only ever feeds because launch succeeded. Reporting
// non-fatally there would let execution fall through to that receive with
// nothing coming, turning a named failure into a package-timeout hang.
//
// The one caller that runs inside a goroutine uses takeAndLaunchAsync instead —
// see there for why Fatal is exactly wrong in that position. The exception gets
// its own entry point rather than the shared helper being rewritten around it.
func takeAndLaunch(t *testing.T, ctx context.Context, sc *Scanner) string {
	t.Helper()
	waypoint, ok := takeAndLaunchAsync(ctx, sc)
	if !ok {
		t.Fatalf("no slot was due, or the popped slot was not a member")
	}
	return waypoint
}

// takeAndLaunchAsync is takeAndLaunch for callers running OFF the main test
// goroutine, where t.Fatal is the wrong abort: it calls runtime.Goexit, so the
// goroutine dies without sending on whatever channel the test is waiting on, the
// receive blocks forever, and a genuine failure surfaces as an unexplained
// package timeout instead of a named assertion.
//
// It reports through the (value, ok) return instead, leaving the caller to
// decide — which for a goroutine means sending something so the test can
// proceed to fail properly.
func takeAndLaunchAsync(ctx context.Context, sc *Scanner) (string, bool) {
	waypoint, _, ok := sc.nextAction(sc.clock.Now())
	if !ok {
		return "", false
	}
	view, member := sc.memberView(waypoint)
	if !member {
		return "", false
	}
	sc.launch(ctx, view)
	return waypoint, true
}

func dueMarket(waypoint string, spread float64) SensingSlotView {
	return parkedMarket(waypoint, spread, time.Time{})
}

// TestScannerLaunch_InFlightSlotLeavesTheRotation is the no-overlap property: a
// slot being scanned is in NEITHER the heap nor anywhere else the pacer can
// reach, so a second scan of the same market cannot be issued while the first is
// outstanding.
func TestScannerLaunch_InFlightSlotLeavesTheRotation(t *testing.T) {
	started, hold := make(chan string, 1), make(chan struct{})
	runner := &fakeScanRunner{started: started, hold: hold}
	sc, _, _ := scanFixture(t, 2, runner)
	sc.SyncMembership([]SensingSlotView{dueMarket("X1-AA-M1", 0.2)}, 1.0)

	ctx := context.Background()
	takeAndLaunch(t, ctx, sc)
	<-started

	if pending := sc.pendingWaypoints(); len(pending) != 0 {
		t.Fatalf("rotation = %v while a scan of it is in flight, want empty", pending)
	}
	if _, _, ok := sc.nextAction(sc.clock.Now()); ok {
		t.Error("the pacer found work while the only slot was being scanned")
	}

	close(hold)
	sc.workers.Wait()

	if pending := sc.pendingWaypoints(); len(pending) != 1 || pending[0] != "X1-AA-M1" {
		t.Errorf("rotation after the scan completed = %v, want the slot back", pending)
	}
}

// TestScannerLaunch_InflightCapHoldsThePacer pins the backpressure reflex. With
// every token held the pacer must BLOCK rather than queue, so scan issuance
// tracks how fast scans actually complete.
func TestScannerLaunch_InflightCapHoldsThePacer(t *testing.T) {
	const cap = 3
	started, hold := make(chan string, 8), make(chan struct{})
	runner := &fakeScanRunner{started: started, hold: hold}
	sc, _, _ := scanFixture(t, cap, runner)
	sc.SyncMembership([]SensingSlotView{
		dueMarket("X1-AA-M1", 0.2), dueMarket("X1-AA-M2", 0.2),
		dueMarket("X1-AA-M3", 0.2), dueMarket("X1-AA-M4", 0.2),
	}, 1.0)

	ctx, cancel := context.WithCancel(context.Background())
	paced := make(chan struct{})
	go func() { defer close(paced); sc.RunPacer(ctx) }()

	for i := 0; i < cap; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d scans started", i, cap)
		}
	}

	select {
	case waypoint := <-started:
		t.Fatalf("scan of %s launched with all %d in-flight tokens held", waypoint, cap)
	case <-time.After(150 * time.Millisecond):
	}

	// Releasing exactly one worker must free exactly one token, and the pacer
	// must then get its fourth scan away.
	hold <- struct{}{}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the fourth scan never launched after a token was returned")
	}

	cancel()
	close(hold)
	select {
	case <-paced:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPacer did not stop and drain after cancellation")
	}
}

// TestScannerSyncMembership_DoesNotDuplicateAnInFlightSlot guards the rebuild
// against the one thing it must not do. SyncMembership rebuilds the whole heap,
// so a slot a worker is holding would be re-added underneath it — and then
// scanned a second time while the first scan was still outstanding.
func TestScannerSyncMembership_DoesNotDuplicateAnInFlightSlot(t *testing.T) {
	started, hold := make(chan string, 1), make(chan struct{})
	runner := &fakeScanRunner{started: started, hold: hold}
	sc, _, _ := scanFixture(t, 2, runner)
	members := []SensingSlotView{dueMarket("X1-AA-M1", 0.2)}
	sc.SyncMembership(members, 1.0)

	takeAndLaunch(t, context.Background(), sc)
	<-started

	sc.SyncMembership(members, 1.0)
	if pending := sc.pendingWaypoints(); len(pending) != 0 {
		t.Fatalf("reconcile re-added an in-flight slot: rotation = %v", pending)
	}

	close(hold)
	sc.workers.Wait()

	if pending := sc.pendingWaypoints(); len(pending) != 1 {
		t.Errorf("rotation = %v, want exactly one copy of the slot", pending)
	}
}

// TestScannerSyncMembership_DropsASlotThatLeftMidScan covers the other side: a
// slot that stops being PARKED while it is being scanned must not be pushed back
// into a rotation it no longer belongs to.
func TestScannerSyncMembership_DropsASlotThatLeftMidScan(t *testing.T) {
	started, hold := make(chan string, 1), make(chan struct{})
	runner := &fakeScanRunner{started: started, hold: hold}
	sc, _, _ := scanFixture(t, 2, runner)
	sc.SyncMembership([]SensingSlotView{dueMarket("X1-AA-M1", 0.2)}, 1.0)

	takeAndLaunch(t, context.Background(), sc)
	<-started
	sc.SyncMembership(nil, 1.0)

	close(hold)
	sc.workers.Wait()

	if pending := sc.pendingWaypoints(); len(pending) != 0 {
		t.Errorf("rotation = %v, want a departed slot to stay out", pending)
	}
}

// TestScannerRunScan_FoldsTheObservationAndRecordsIt walks the whole completion
// path: scan, read what it wrote, smooth it, record it.
func TestScannerRunScan_FoldsTheObservationAndRecordsIt(t *testing.T) {
	runner := &fakeScanRunner{}
	sc, ledger, spreads := scanFixture(t, 2, runner)
	sc.SyncMembership([]SensingSlotView{dueMarket("X1-AA-M1", 0.1)}, 1.0)

	takeAndLaunch(t, context.Background(), sc)
	sc.workers.Wait()

	if got := runner.scanned(); len(got) != 1 || got[0] != "X1-AA-M1" {
		t.Fatalf("scans = %v, want one scan of the slot", got)
	}
	if got := spreads.reads(); len(got) != 1 || got[0] != "X1-AA-M1" {
		t.Fatalf("spread reads = %v, want one read of the slot", got)
	}

	marks := ledger.marks()
	if len(marks) != 1 {
		t.Fatalf("MarkScanned calls = %d, want 1", len(marks))
	}
	// Prices 90/110 observe 0.2; EWMA at alpha 0.3 over a prior of 0.1 gives
	// 0.3*0.2 + 0.7*0.1.
	if want := 0.13; !nearly(marks[0].spread, want) {
		t.Errorf("recorded spread = %v, want the smoothed %v", marks[0].spread, want)
	}
	if marks[0].at != scanEpoch {
		t.Errorf("recorded scan time = %s, want the clock's %s", marks[0].at, scanEpoch)
	}
}

// TestScannerRunScan_LedgerFailureKeepsTheSlotScanning pins the read-path rule:
// a lost freshness write costs one stamp, never the waypoint's place in the
// rotation.
func TestScannerRunScan_LedgerFailureKeepsTheSlotScanning(t *testing.T) {
	runner := &fakeScanRunner{}
	sc, ledger, _ := scanFixture(t, 2, runner)
	ledger.err = errors.New("ledger unavailable")
	sc.SyncMembership([]SensingSlotView{dueMarket("X1-AA-M1", 0.1)}, 1.0)

	takeAndLaunch(t, context.Background(), sc)
	sc.workers.Wait()

	if pending := sc.pendingWaypoints(); len(pending) != 1 {
		t.Errorf("rotation = %v, want the slot still scanning after a failed write", pending)
	}
}

// TestScannerRunScan_FailedScanPacesItsRetry pins the failure path: nothing is
// recorded, but the slot is stamped so it does not come straight back round and
// hammer an API that is already failing.
func TestScannerRunScan_FailedScanPacesItsRetry(t *testing.T) {
	runner := &fakeScanRunner{err: errors.New("api down")}
	sc, ledger, spreads := scanFixture(t, 2, runner)
	sc.SyncMembership([]SensingSlotView{dueMarket("X1-AA-M1", 0.1)}, 1.0)

	takeAndLaunch(t, context.Background(), sc)
	sc.workers.Wait()

	if got := spreads.reads(); len(got) != 0 {
		t.Errorf("spread read %v after a failed scan, want none", got)
	}
	if got := ledger.marks(); len(got) != 0 {
		t.Errorf("MarkScanned called %v for a scan that failed, want none", got)
	}
	if pending := sc.pendingWaypoints(); len(pending) != 1 {
		t.Fatalf("rotation = %v, want the failed slot returned", pending)
	}
	if _, _, ok := sc.nextAction(sc.clock.Now()); ok {
		t.Error("the failed slot came straight back round, which is a retry hot loop")
	}
}

// TestScannerRunScan_SlotWithNothingToMeasureSkipsTheSpreadRead covers yards.
// There is no whitelist to price, so the spread read is not made at all and the
// stored estimate is carried forward rather than decayed toward a zero nobody
// observed.
func TestScannerRunScan_SlotWithNothingToMeasureSkipsTheSpreadRead(t *testing.T) {
	runner := &fakeScanRunner{}
	sc, ledger, spreads := scanFixture(t, 2, runner)

	yard := dueMarket("X1-AA-YARD", 0.42)
	yard.Kind = SlotKindYard
	yard.Whitelist = nil
	sc.SyncMembership([]SensingSlotView{yard}, 1.0)

	takeAndLaunch(t, context.Background(), sc)
	sc.workers.Wait()

	if got := spreads.reads(); len(got) != 0 {
		t.Errorf("spread read %v for a slot with no whitelist, want none", got)
	}
	marks := ledger.marks()
	if len(marks) != 1 {
		t.Fatalf("MarkScanned calls = %d, want 1", len(marks))
	}
	if !nearly(marks[0].spread, 0.42) {
		t.Errorf("recorded spread = %v, want the stored %v carried forward", marks[0].spread, 0.42)
	}
}

// TestScannerRunScan_UnreadableSpreadIsNotAnObservation distinguishes a failed
// read from a measured zero. Only the latter may decay the estimate.
func TestScannerRunScan_UnreadableSpreadIsNotAnObservation(t *testing.T) {
	runner := &fakeScanRunner{}
	sc, ledger, spreads := scanFixture(t, 2, runner)
	spreads.err = errors.New("market read failed")
	sc.SyncMembership([]SensingSlotView{dueMarket("X1-AA-M1", 0.42)}, 1.0)

	takeAndLaunch(t, context.Background(), sc)
	sc.workers.Wait()

	marks := ledger.marks()
	if len(marks) != 1 {
		t.Fatalf("MarkScanned calls = %d, want 1", len(marks))
	}
	if !nearly(marks[0].spread, 0.42) {
		t.Errorf("recorded spread = %v, want the stored %v left alone", marks[0].spread, 0.42)
	}
}

// TestScannerLaunch_PanickingWorkerKeepsTheSlot pins panic isolation. A worker
// that explodes must cost its slot one turn and nothing else — not the pacer,
// and not the slot's place in the rotation.
func TestScannerLaunch_PanickingWorkerKeepsTheSlot(t *testing.T) {
	runner := &fakeScanRunner{panicAt: "X1-AA-M1"}
	sc, _, _ := scanFixture(t, 2, runner)
	sc.SyncMembership([]SensingSlotView{dueMarket("X1-AA-M1", 0.2)}, 1.0)

	takeAndLaunch(t, context.Background(), sc)
	sc.workers.Wait()

	if pending := sc.pendingWaypoints(); len(pending) != 1 {
		t.Errorf("rotation = %v, want a panicking scan's slot returned", pending)
	}
}

// TestScannerLaunch_CancellationWhileBlockedReleasesTheSlot covers shutdown from
// inside the backpressure block: the slot was taken out of the rotation but
// never scanned, so it must not be left marked in flight forever.
func TestScannerLaunch_CancellationWhileBlockedReleasesTheSlot(t *testing.T) {
	started, hold := make(chan string, 1), make(chan struct{})
	runner := &fakeScanRunner{started: started, hold: hold}
	sc, _, _ := scanFixture(t, 1, runner)
	sc.SyncMembership([]SensingSlotView{
		dueMarket("X1-AA-M1", 0.2), dueMarket("X1-AA-M2", 0.2),
	}, 1.0)

	ctx, cancel := context.WithCancel(context.Background())
	takeAndLaunch(t, ctx, sc)
	<-started

	// The single token is held, so this launch blocks until cancellation.
	//
	// No settle before cancelling: the assertion below holds on BOTH orderings.
	// If the goroutine reaches the token select first it blocks and is released
	// by the cancel; if the cancel lands first the select takes its ctx.Done
	// branch immediately. Either way the slot must come back out of the in-flight
	// set, which is the property under test — so a sleep here would only be
	// staging a scenario the assertion does not distinguish.
	// takeAndLaunchAsync, not takeAndLaunch: a t.Fatal in this goroutine would
	// Goexit without sending, and the receive below would block forever — a real
	// failure would read as a package timeout instead of an assertion.
	blocked := make(chan string)
	launched := make(chan bool, 1)
	go func() {
		waypoint, ok := takeAndLaunchAsync(ctx, sc)
		launched <- ok
		blocked <- waypoint
	}()
	cancel()

	taken := <-blocked
	if ok := <-launched; !ok {
		t.Fatal("the second launch never took a slot, so there is nothing to assert about its release")
	}
	close(hold)
	sc.workers.Wait()

	sc.mu.Lock()
	_, stillMarked := sc.scanning[taken]
	sc.mu.Unlock()
	if stillMarked {
		t.Errorf("slot %q stayed marked in flight after a cancelled launch", taken)
	}
}

// TestScannerRunPacer_StopsOnCancellation pins clean shutdown from the idle path.
func TestScannerRunPacer_StopsOnCancellation(t *testing.T) {
	sc, _, _ := scanFixture(t, 2, &fakeScanRunner{})

	ctx, cancel := context.WithCancel(context.Background())
	paced := make(chan struct{})
	go func() { defer close(paced); sc.RunPacer(ctx) }()

	cancel()
	select {
	case <-paced:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPacer did not return after cancellation")
	}
}

// TestScannerRunPacer_RequeueWakesTheSleepingPacer pins throughput on a SMALL
// rotation — the regime this ships into, where the fleet has a handful of probes
// and the largest residual scan budget it will ever have.
//
// Once every member is in flight the heap is empty, so the pacer's sleep is the
// fixed empty-rotation poll: a number with no relationship to the scan rate. A
// pacer that sleeps it out regardless of when its workers finish spends a
// fraction of the budget it was given, silently — nothing errors, nothing logs,
// and the rotation just runs slow. So a completion must re-open the decision.
//
// The rate here gives each slot a 50ms interval against a 5s poll, two orders of
// magnitude apart, so the assertion window cannot be met by the poll firing.
func TestScannerRunPacer_RequeueWakesTheSleepingPacer(t *testing.T) {
	const inflightCap = 3
	started, hold := make(chan string, 8), make(chan struct{})
	runner := &fakeScanRunner{started: started, hold: hold}

	// A real clock: a requeued slot only comes due if time actually passes.
	sc := NewScanner(1, ScanPorts{
		Scan: runner, Ledger: &fakeScanLedger{},
		SpreadOf: &fakeSpreadObserver{prices: []GoodPrice{{Good: "FUEL", Bid: 90, Ask: 110}}},
	}, shared.NewRealClock(), ScanKnobs{InflightCap: inflightCap, ClampR: 4})

	// Three unmeasured slots carry the optimistic prior (weight 2 each, total 6),
	// so at rate 60 each one's interval is 6/(60*2) = 50ms.
	sc.SyncMembership([]SensingSlotView{
		dueMarket("X1-AA-M1", 0), dueMarket("X1-AA-M2", 0), dueMarket("X1-AA-M3", 0),
	}, 60.0)

	ctx, cancel := context.WithCancel(context.Background())
	paced := make(chan struct{})
	go func() { defer close(paced); sc.RunPacer(ctx) }()

	// Every member is now in flight, which empties the heap and parks the pacer
	// on the poll.
	for i := 0; i < inflightCap; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d scans started", i, inflightCap)
		}
	}

	// Release one worker. Its requeue is the only thing that can bring the pacer
	// back before the poll expires.
	begin := time.Now()
	hold <- struct{}{}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the pacer slept through a completed scan; a small rotation under-spends its budget")
	}
	if waited := time.Since(begin); waited >= emptyRotationPoll {
		t.Errorf("next scan took %s, which is the empty-rotation poll rather than the 50ms interval", waited)
	}

	cancel()
	close(hold)
	select {
	case <-paced:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPacer did not stop and drain after cancellation")
	}
}

// TestScannerSyncMembership_WakesAPacerParkedOnAnEmptyRotation closes the twin of
// the requeue wake, on the path a restart actually takes.
//
// A pacer started before its first reconcile has NOTHING in the rotation, so it
// parks on the 5s empty-rotation poll — a fixed number with no relationship to
// the scan rate. The first SyncMembership then admits every slot the ledger
// holds, all of them due immediately, and without a wake they all sit out the
// remainder of that poll. It is not a lost scan, but it is a silent one: nothing
// errors, nothing logs, and the fleet's first market data after every restart
// arrives late by an interval nobody chose.
//
// The rate here gives the slot a 50ms interval against the 5s poll — two orders
// of magnitude apart, so the assertion window cannot be met by the poll firing.
func TestScannerSyncMembership_WakesAPacerParkedOnAnEmptyRotation(t *testing.T) {
	started := make(chan string, 4)
	runner := &fakeScanRunner{started: started}

	sc := NewScanner(1, ScanPorts{
		Scan: runner, Ledger: &fakeScanLedger{},
		SpreadOf: &fakeSpreadObserver{prices: []GoodPrice{{Good: "FUEL", Bid: 90, Ask: 110}}},
	}, shared.NewRealClock(), ScanKnobs{InflightCap: 1, ClampR: 4})

	ctx, cancel := context.WithCancel(context.Background())
	paced := make(chan struct{})
	go func() { defer close(paced); sc.RunPacer(ctx) }()

	// Let the pacer reach the empty-rotation sleep before membership arrives —
	// otherwise it might simply find the slot on its first pass and the wake
	// would never be exercised.
	// The rotation is empty and nothing has been scanned, so the pacer can only
	// be in — or about to enter — the empty-rotation sleep. A short settle makes
	// it the former, so the wake is genuinely what returns it.
	if members, _ := sc.RotationSize(); members != 0 {
		t.Fatalf("rotation should start empty, held %d member(s)", members)
	}
	time.Sleep(100 * time.Millisecond)
	if scanned := runner.scanned(); len(scanned) != 0 {
		t.Fatalf("nothing should have been scanned before membership arrived, got %v", scanned)
	}

	begin := time.Now()
	sc.SyncMembership([]SensingSlotView{dueMarket("X1-AA-M1", 0)}, 60.0)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the pacer slept through a membership refresh; every restart's first scans wait out the poll")
	}
	if waited := time.Since(begin); waited >= emptyRotationPoll {
		t.Errorf("first scan took %s, which is the empty-rotation poll rather than the wake", waited)
	}

	cancel()
	select {
	case <-paced:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPacer did not stop and drain after cancellation")
	}
}

// --- the shipyard under a market sensor's feet -------------------------------
//
// THE BLIND SPOT THIS CLOSES. A waypoint can be two things at once, and the
// engine only ever sensed it as one: a probe-selling yard that is also a
// whitelisted market is placed as a MARKET slot (only that kind carries the goods
// list), so the hull standing there was a market sensor and nothing ever asked it
// about the counter under its feet. Measured live, NINE shipyards had one of our
// hulls parked on them and no recorded inventory at all — while the fleet was
// hunting a hull one of them sells.

// fakeYardReader is the shipyard as the WORLD holds it, not a call log.
//
// It models the production adapter's own first move — read the waypoint's cached
// SHIPYARD trait, and no-op if it is not one — so a waypoint that is only a
// market records nothing here and a waypoint that is both records its catalogue.
// That is what makes the assertion below behavioural: it distinguishes the two
// cases rather than counting calls that would be made either way.
type fakeYardReader struct {
	mu sync.Mutex
	// sells is the world: waypoint → what that shipyard offers. A waypoint absent
	// from this map is not a shipyard at all.
	sells map[string][]string
	// recorded is what the reads persisted, keyed by waypoint.
	recorded map[string][]string
	err      error
}

func newFakeYardReader(sells map[string][]string) *fakeYardReader {
	return &fakeYardReader{sells: sells, recorded: map[string][]string{}}
}

func (f *fakeYardReader) ReadCatalog(_ context.Context, _ int, waypoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	offers, isYard := f.sells[waypoint]
	if !isYard {
		return nil // a cached-trait no-op, exactly as the adapter behaves
	}
	f.recorded[waypoint] = append([]string(nil), offers...)
	return nil
}

func (f *fakeYardReader) catalogueAt(waypoint string) ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	offers, ok := f.recorded[waypoint]
	return offers, ok
}

// THE REGRESSION, reproducing X1-QR78-AE4F exactly: a waypoint that is BOTH a
// whitelisted market AND a shipyard selling a heavy hull. Its slot is a MARKET
// slot — that is the collision contract and it is not changing — and after the
// parked probe takes its turn the SHIPYARD must be recorded too.
//
// This is the read that carries PRICES. The free catalogue sweep can learn what
// AE4F sells from across the map, but only a hull standing at the counter sees
// the `ships` array, and an unpriced heavy yard can never feed a money guard.
func TestScannerRunScan_MarketYardRecordsTheShipyardToo(t *testing.T) {
	runner := &fakeScanRunner{}
	yards := newFakeYardReader(map[string][]string{
		"X1-QR78-AE4F": {"SHIP_PROBE", "SHIP_HEAVY_FREIGHTER"},
	})
	sc := NewScanner(1, ScanPorts{
		Scan: runner, Ledger: &fakeScanLedger{}, SpreadOf: &fakeSpreadObserver{}, Yard: yards,
	}, scanClock(), ScanKnobs{InflightCap: 2, ClampR: 4})

	// A MARKET slot, because that is what the screen plans for a yard that is also
	// a whitelisted market.
	sc.SyncMembership([]SensingSlotView{dueMarket("X1-QR78-AE4F", 0.2)}, 1.0)
	takeAndLaunch(t, context.Background(), sc)
	sc.workers.Wait()

	catalogue, recorded := yards.catalogueAt("X1-QR78-AE4F")
	if !recorded {
		t.Fatal("the shipyard at the market waypoint was never read — a MARKET slot must still sense the yard")
	}
	if !sellsType(catalogue, "SHIP_HEAVY_FREIGHTER") {
		t.Fatalf("recorded catalogue = %v, want the heavy hull the yard sells", catalogue)
	}
}

// The other half of the same rule: a waypoint that is ONLY a market costs no
// shipyard row. The trait check lives in the adapter (a cached local read, no API
// call), so this layer asks about every scanned waypoint and the world answers.
func TestScannerRunScan_APlainMarketRecordsNoShipyard(t *testing.T) {
	runner := &fakeScanRunner{}
	yards := newFakeYardReader(map[string][]string{"X1-QR78-AE4F": {"SHIP_PROBE"}})
	sc := NewScanner(1, ScanPorts{
		Scan: runner, Ledger: &fakeScanLedger{}, SpreadOf: &fakeSpreadObserver{}, Yard: yards,
	}, scanClock(), ScanKnobs{InflightCap: 2, ClampR: 4})

	sc.SyncMembership([]SensingSlotView{dueMarket("X1-QR78-PLAIN", 0.2)}, 1.0)
	takeAndLaunch(t, context.Background(), sc)
	sc.workers.Wait()

	if catalogue, recorded := yards.catalogueAt("X1-QR78-PLAIN"); recorded {
		t.Fatalf("a waypoint that is not a shipyard recorded %v", catalogue)
	}
}

// A refused shipyard read costs the yard's reading and NOTHING ELSE. The market
// scan has already succeeded and its prices are already persisted, so failing the
// slot here would cost the waypoint its whole market rotation to recover a
// reading the free catalogue sweep takes again next tick anyway.
func TestScannerRunScan_AFailedYardReadKeepsTheMarketScan(t *testing.T) {
	runner := &fakeScanRunner{}
	yards := newFakeYardReader(map[string][]string{"X1-QR78-AE4F": {"SHIP_PROBE"}})
	yards.err = errors.New("shipyard read refused")
	ledger := &fakeScanLedger{}
	sc := NewScanner(1, ScanPorts{
		Scan: runner, Ledger: ledger, SpreadOf: &fakeSpreadObserver{prices: []GoodPrice{{Good: "FUEL", Bid: 90, Ask: 110}}}, Yard: yards,
	}, scanClock(), ScanKnobs{InflightCap: 2, ClampR: 4})

	sc.SyncMembership([]SensingSlotView{dueMarket("X1-QR78-AE4F", 0.2)}, 1.0)
	takeAndLaunch(t, context.Background(), sc)
	sc.workers.Wait()

	if marks := ledger.marks(); len(marks) != 1 {
		t.Fatalf("MarkScanned calls = %d, want the market scan still recorded", len(marks))
	}
	if pending := sc.pendingWaypoints(); len(pending) != 1 {
		t.Fatalf("rotation = %v, want the slot still scanning after a refused yard read", pending)
	}
}

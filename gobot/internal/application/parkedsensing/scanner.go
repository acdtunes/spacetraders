package parkedsensing

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

// scanner.go is the data plane of the parked-probe sensing model: the engine
// that spends the scan budget the rest of the model sizes.
//
// ONE pacer goroutine issues every parked-market scan in the fleet, so the rate
// the reconcile hands to SyncMembership is the rate actually spent. Work runs on
// a bounded pool of short-lived workers; when every in-flight token is held the
// pacer BLOCKS rather than queueing, so a slow API throttles issuance at the
// source instead of backlogging scans against prices that have since moved.
//
// Nothing here is durable: SyncMembership replaces the members map and the heap
// wholesale on EVERY reconcile, so the clock requeue keeps lasts one tick and the
// durable pacing stamp is the ledger's last_scan_attempt_at. Any change to what a
// turn records in the ledger is therefore a change to the pacing — see runScan.

// emptyRotationPoll is how long the pacer parks when it has nothing to scan. A
// liveness poll, not a pacing decision: membership only changes at a reconcile.
const emptyRotationPoll = 5 * time.Second

// yardScanWeight is the fixed share a shipyard slot earns. Hull prices move on
// their own schedule, not with market spread, so a yard is neither promoted nor
// demoted by the spread weighting: baseline share plus its own cadence floor.
const yardScanWeight = 1.0

// defaultInflightCap is the in-flight bound used when the caller passes a
// non-positive one: enough to keep the pacer off API latency, small enough that a
// degraded API is felt as backpressure within a few scans.
const defaultInflightCap = 3

// minWeightClamp is the lowest weighting clamp the scanner will operate at. A
// clamp below 1 collapses ScanWeight's optimistic prior to zero, which Interval
// reads as a degenerate weight and degrades that slot to hourly scans — a knob of
// 0 would darken the rotation rather than flatten it, and flattening is what a
// low clamp means.
const minWeightClamp = 1

// scanGuardComponent labels the panic guard around a scan worker.
const scanGuardComponent = "parked-sensing-scan:"

// SensingSlotView is one parked placement as the scan rotation sees it: where to
// scan, what to measure there, and when it was last measured. It is a projection
// of the ledger row handed in wholesale by each reconcile, and the scanner holds
// no other membership state, so a slot that stops being PARKED leaves the
// rotation by ceasing to appear.
type SensingSlotView struct {
	// Waypoint is the market or yard to scan.
	Waypoint string
	// Kind is MARKET, YARD or SPARE. Only the first two are ever scanned.
	Kind string
	// State is the slot's lifecycle position. Only PARKED slots are scanned —
	// there is no probe standing anywhere else to scan from.
	State string
	// Whitelist is the goods this slot exists to watch. Empty means there is
	// nothing to measure here (a yard), and the spread observation is skipped
	// rather than read as a zero.
	Whitelist []string
	// SpreadEWMA is the smoothed relative spread observed so far. Zero means
	// unmeasured, which ScanWeight treats as optimistic rather than as the worst
	// possible reading.
	SpreadEWMA float64
	// LastScan is the stamp the rotation PACES against: when this slot last had
	// its turn, whether or not the turn produced data. The zero time means never
	// attempted, which makes the slot due immediately. It is the ATTEMPT clock,
	// not the freshness claim (see LastDataAt), fed from last_scan_attempt_at.
	LastScan time.Time
	// LastDataAt is when market data was last actually WRITTEN here — the honest
	// freshness stamp, fed from last_scan_at. It paces nothing: a declined turn
	// writes nothing, so one stamp serving both the rotation and the staleness
	// gauge would have to lie to one of them. Zero means never scanned, and the
	// gauge EXCLUDES such a slot rather than reading it as infinitely stale.
	LastDataAt time.Time
	// YardCadence is the quartermaster's re-read interval, YARD slots only. It is
	// a FLOOR, never a target: a yard falls due at the later of its weighted turn
	// and last scan + cadence, so the budget can slow a yard but never speed it.
	YardCadence time.Duration
}

// MarketScanRunner performs one market scan and persists what it found. The
// implementation, not this package, tags the call as scanning-source and
// low-priority: scans must lose a contended rate-limit token to every other
// consumer.
//
// Run reports whether the scan actually WROTE market data. False with a nil error
// is a budget DECLINE: the allowance served this waypoint from the store instead
// of spending a request — a success for the caller, but not a scan. Collapse that
// distinction into the error and the ledger records a freshness claim nothing
// wrote.
type MarketScanRunner interface {
	Run(ctx context.Context, playerID int, waypoint string) (scanned bool, err error)
}

// SpreadObserver reads the prices a completed scan just persisted for ONE
// waypoint. Narrow by design: the scanner's per-scan cost must not grow with how
// many markets the player has visited.
type SpreadObserver interface {
	MarketPrices(ctx context.Context, playerID int, waypoint string) ([]GoodPrice, error)
}

// ScanLedger is the scan path's slice of the placement ledger: the writes that
// record a slot was scanned and what it showed. MarkScanned touches only the
// freshness and spread columns; every state transition belongs to the screen's
// SlotLedger and the queue's BuyLedger. Those write sets are disjoint BY
// CONSTRUCTION, which is what lets the pacer run concurrently with the reconcile
// without either fighting the other for a row. TWO verbs rather than one with a
// flag: MarkScanned advances both scan clocks, MarkScanAttempted only the pacing
// clock — the columns they may touch differ, and ownership here is per-COLUMN.
type ScanLedger interface {
	MarkScanned(ctx context.Context, playerID int, waypoint, kind string, at time.Time, spreadEWMA float64) error
	// MarkScanAttempted records a turn that produced no data — a budget decline.
	// It MUST advance the durable pacing clock: the reconcile rebuilds the rotation
	// from the ledger every tick, so a decline that advanced nothing leaves the slot
	// due immediately and spins the rotation at full speed producing nothing.
	MarkScanAttempted(ctx context.Context, playerID int, waypoint, kind string, at time.Time) error
}

// ScanPorts is everything the scanner needs from the outside world. There is
// deliberately no rate port and no pressure port: the reconcile computes the rate
// from the API budget (including the pressure brake) and hands it to
// SyncMembership, so the rate is decided in one place and the pacer cannot brake
// against pressure a second time.
type ScanPorts struct {
	Scan     MarketScanRunner
	Ledger   ScanLedger
	SpreadOf SpreadObserver
	// Yard reads the SHIPYARD standing at the same waypoint the scan just read, so
	// a waypoint that is both market and shipyard is sensed as BOTH. The slot KIND
	// does not decide — only MARKET slots carry a goods list, so EVERY parked scan
	// also reads the yard, and the adapter's cached SHIPYARD-trait check makes that
	// free where there is no yard. It is the SAME port the free catalogue pass
	// drives (yardcatalog.go); the difference is that a read issued from a hull at
	// the counter also carries PRICES, which is what makes a yard buyable. Nil-safe
	// for the pacing tests; not an arming seam — the coordinator's wired() needs it.
	Yard YardCatalogReader
}

// ScanOutcomes is what a stretch of the rotation produced: turns that persisted
// market data, turns the fleet market-scan budget served from the store, and turns
// that errored writing nothing. IT IS THE ONLY PLACE THAT BUDGET IS VISIBLE FROM
// INSIDE SENSING — the rate handed to SyncMembership is an allowance, how fast turns
// are ISSUED, while each issued turn is separately admitted by the one market-scan
// allowance every reader in the daemon shares. A declined turn spends no request, so
// a rotation at exactly its paced rate can land almost nothing, which from outside is
// indistinguishable from a rotation that has stopped.
type ScanOutcomes struct {
	Scanned  int
	Declined int
	Failed   int
}

// Total is every turn taken — the denominator Scanned is only meaningful against.
func (o ScanOutcomes) Total() int { return o.Scanned + o.Declined + o.Failed }

// ScanKnobs are the operator-set shape of the scanner.
type ScanKnobs struct {
	// InflightCap bounds concurrent scans, and with it how hard a slow API can
	// push back on the pacer.
	InflightCap int
	// ClampR is the ceiling on how much more attention the hottest market may
	// earn than the baseline. A clamp of 1 flattens the weighting entirely.
	ClampR int
}

// Scanner owns the fleet-wide scan rotation. The mutex guards the rotation as a
// whole — heap, membership map, the budget totals it was normalised against, and
// the waypoints currently being scanned. They are one consistent picture and are
// never locked separately: a weight read against one total and a due time against
// another would silently mis-pace the rotation.
type Scanner struct {
	playerID int
	ports    ScanPorts
	clock    shared.Clock
	clampR   int

	// tokens is the in-flight bound. A worker holds one for its whole life, and
	// the pacer blocks acquiring one — see launch.
	tokens chan struct{}
	// wake nudges a sleeping pacer when a worker returns a slot to the rotation.
	// Buffered to one and only ever sent without blocking, so a worker never waits
	// on the pacer and a burst of completions collapses to one re-evaluation.
	wake chan struct{}
	// workers tracks live scans so RunPacer can drain before returning.
	workers sync.WaitGroup

	mu sync.Mutex
	// due is the rotation. A slot is IN the heap or IN FLIGHT, never both.
	due *domainSensing.NextDueHeap
	// members is every slot eligible to be scanned, by waypoint.
	members map[string]SensingSlotView
	// scanning is the waypoints a worker currently holds. They are absent from the
	// heap and are not re-added by a reconcile — only the worker's completion path
	// returns them, which makes two concurrent scans of one market impossible.
	scanning map[string]struct{}
	// outcomes tallies completed turns since the last reconcile read them, under the
	// rotation's own lock so a finishing worker cannot interleave a half-counted turn.
	outcomes ScanOutcomes

	totalWeight float64
	rate        float64
	median      float64
}

// NewScanner builds a scanner for one player. The rotation starts empty:
// membership and rate both arrive with the first reconcile, so a pacer started
// before the first SyncMembership polls until it has members.
func NewScanner(playerID int, p ScanPorts, clock shared.Clock, k ScanKnobs) *Scanner {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	inflight := k.InflightCap
	if inflight <= 0 {
		inflight = defaultInflightCap
	}
	clampR := k.ClampR
	if clampR < minWeightClamp {
		clampR = minWeightClamp
	}
	return &Scanner{
		playerID: playerID,
		ports:    p,
		clock:    clock,
		clampR:   clampR,
		tokens:   make(chan struct{}, inflight),
		wake:     make(chan struct{}, 1),
		due:      domainSensing.NewNextDueHeap(0, 0),
		members:  map[string]SensingSlotView{},
		scanning: map[string]struct{}{},
	}
}

// SyncMembership replaces the rotation with the placements the reconcile just
// read, re-rated to the budget it just computed. The whole heap is rebuilt rather
// than diffed because renormalisation is wholesale by nature — one slot joining
// changes the total weight and so every other slot's interval — and each slot's
// due time is recomputed from its own last scan, so a slot that has been waiting
// keeps the credit for that wait. Slots currently in flight are deliberately NOT
// re-added: they are still members (the worker's completion path reads the
// refreshed view), but the heap must not hold a copy of a slot being scanned.
func (s *Scanner) SyncMembership(slots []SensingSlotView, rate float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	members := make(map[string]SensingSlotView, len(slots))
	rotation := make([]SensingSlotView, 0, len(slots))
	for _, v := range slots {
		if !inRotation(v) {
			continue
		}
		members[v.Waypoint] = v
		rotation = append(rotation, v)
	}

	s.members = members
	s.median = fleetMedianSpread(rotation)
	s.rate = rate

	s.totalWeight = 0
	for _, v := range rotation {
		s.totalWeight += s.weightOf(v)
	}

	s.due = domainSensing.NewNextDueHeap(s.totalWeight, s.rate)
	for _, v := range rotation {
		if _, inFlight := s.scanning[v.Waypoint]; inFlight {
			continue
		}
		heap.Push(s.due, s.scheduleFor(v))
	}

	// Tell the pacer the rotation changed under it, for the same reason requeue
	// does: a pacer asleep on the empty-rotation poll or a distant due time would
	// wait out a sleep computed before these members existed. Non-blocking, and
	// sent under the lock so a pacer mid-way to its select cannot miss it.
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// RunPacer issues scans until ctx is cancelled, then drains its workers. Every
// scheduling decision is nextAction's and every backpressure decision is
// launch's; sleeping selects on ctx, so a cancelled coordinator stops promptly
// instead of at the end of a scan interval.
func (s *Scanner) RunPacer(ctx context.Context) {
	defer s.workers.Wait()

	for {
		if ctx.Err() != nil {
			return
		}

		waypoint, sleepFor, ok := s.nextAction(s.clock.Now())
		if !ok {
			// The wake case is what keeps a SMALL rotation spending its budget:
			// once every member is in flight the heap is empty and sleepFor is
			// the empty-rotation poll, unrelated to the scan rate. A timer-only
			// sleep also oversleeps a scan that completes and falls due sooner.
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			case <-time.After(sleepFor):
			}
			continue
		}

		view, member := s.memberView(waypoint)
		if !member {
			// A reconcile dropped the slot between the pop and this read. Hand
			// the mark back so the waypoint is not stranded as in-flight.
			s.release(waypoint)
			continue
		}
		s.launch(ctx, view)
	}
}

// nextAction is the pacer's whole scheduling decision, as a pure function of the
// rotation and the time: either a waypoint to scan now, or how long to wait.
// Popping is what takes a slot OUT of the rotation and nothing puts it back
// except a worker completing — the no-overlap property, held without a lock
// across the scan itself.
//
// The sweep past a not-yet-due slot exists for the yard cadence: the heap orders
// on the UNFLOORED due time, so a cadence-held yard can surface as the minimum
// while a market behind it is genuinely due, and sleeping on it would idle the
// whole rotation. Slots passed over are held and pushed back unchanged.
func (s *Scanner) nextAction(now time.Time) (string, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := s.due.Len()
	if n == 0 {
		return "", emptyRotationPoll, false
	}

	var held []domainSensing.SlotSchedule
	var earliest time.Time
	for range n {
		sched := heap.Pop(s.due).(domainSensing.SlotSchedule)
		view, ok := s.members[sched.Waypoint]
		if !ok {
			// Dropped from membership since it was scheduled. Popping it is how
			// it leaves; it is not held and not pushed back.
			continue
		}

		when := s.dueAt(sched, view)
		if !when.After(now) {
			s.scanning[sched.Waypoint] = struct{}{}
			s.restore(held)
			return sched.Waypoint, 0, true
		}

		held = append(held, sched)
		if earliest.IsZero() || when.Before(earliest) {
			earliest = when
		}
	}

	s.restore(held)
	if earliest.IsZero() {
		return "", emptyRotationPoll, false
	}
	return "", earliest.Sub(now), false
}

// launch runs one scan on a worker, holding the pacer until there is a token for
// it. Blocking rather than queueing is the point: the in-flight bound is the only
// place scan issuance responds to how fast scans actually complete, so a degraded
// API stalls the pacer — a scan whose result arrives late is a price already moved.
func (s *Scanner) launch(ctx context.Context, slot SensingSlotView) {
	select {
	case s.tokens <- struct{}{}:
	case <-ctx.Done():
		s.release(slot.Waypoint)
		return
	}

	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer func() { <-s.tokens }()
		// A panicking scan costs one slot one turn, never the whole rotation.
		// runScan returns the slot on the way out; the guard absorbs the panic.
		supervise.Guard(scanGuardComponent+slot.Waypoint, func() { s.runScan(ctx, slot) })
	}()
}

// runScan is one worker's whole life: scan, measure, record, and return the slot
// to the rotation. The requeue is DEFERRED, so every exit returns the slot — a
// failed scan, an unreadable market, a refused ledger write, or a panic. A slot
// that failed to leave on its own turn would not scan again until the next
// reconcile rebuilt the heap around it.
//
// A TURN HAS THREE OUTCOMES, not two, differing ONLY in what the ledger is told:
//
//	SCANNED   data written    → MarkScanned:       both clocks advance
//	DECLINED  budget said no  → MarkScanAttempted: pacing clock only
//	FAILED    error           → nothing written:   neither clock advances durably
//
// THE PACING IS IDENTICAL IN ALL THREE: every exit requeues, advancing the LOCAL
// clock, and the first two also advance the DURABLE one — the clock that survives,
// since the reconcile rebuilds the heap from the ledger every tick and a turn that
// advanced only the local view reads as permanently due. A decline that did not
// pace would spin the rotation against the store, and a failing waypoint that did
// not pace would become a hot retry loop against an API that is already failing.
func (s *Scanner) runScan(ctx context.Context, slot SensingSlotView) {
	at := s.clock.Now()
	spread := slot.SpreadEWMA
	wrote := false
	defer func() { s.requeue(slot.Waypoint, at, spread, wrote) }()

	scanned, err := s.ports.Scan.Run(ctx, s.playerID, slot.Waypoint)
	if err != nil {
		at = s.clock.Now()
		s.note(func(o *ScanOutcomes) { o.Failed++ })
		s.warn(ctx, "parked_sensing_scan_failed", slot.Waypoint,
			fmt.Sprintf("parked sensing scan of %s failed: %v", slot.Waypoint, err))
		return
	}
	at = s.clock.Now()

	if observed, ok := s.observe(ctx, slot); ok {
		spread = domainSensing.UpdateSpreadEWMA(slot.SpreadEWMA, observed)
	}

	// If this waypoint is ALSO a shipyard the hull standing here can price it, and
	// nothing else will. IT RUNS ON A DECLINED TURN TOO — the market budget says
	// nothing about the shipyard, and this is the only path that PRICES a yard.
	s.readYard(ctx, slot)

	if !scanned {
		// DECLINED. No market data was written, so the freshness stamp must not
		// move; only the turn is recorded, which is what keeps the rotation paced.
		s.note(func(o *ScanOutcomes) { o.Declined++ })
		if err := s.ports.Ledger.MarkScanAttempted(ctx, s.playerID, slot.Waypoint, slot.Kind, at); err != nil {
			s.warn(ctx, "parked_sensing_mark_attempt_failed", slot.Waypoint,
				fmt.Sprintf("failed to record a declined sensing scan of %s: %v", slot.Waypoint, err))
		}
		return
	}
	wrote = true
	s.note(func(o *ScanOutcomes) { o.Scanned++ })

	if err := s.ports.Ledger.MarkScanned(ctx, s.playerID, slot.Waypoint, slot.Kind, at, spread); err != nil {
		// Logged, never fatal to the slot: losing this write costs one stale
		// stamp, while dropping the slot would cost every future scan.
		s.warn(ctx, "parked_sensing_mark_scanned_failed", slot.Waypoint,
			fmt.Sprintf("failed to record sensing scan of %s: %v", slot.Waypoint, err))
	}
}

// readYard records what the shipyard at this waypoint sells and what it charges,
// riding the turn the market scan just took. IT DECIDES NOTHING ABOUT WHAT THE
// WAYPOINT IS: the SHIPYARD trait is a cached local fact the adapter reads before
// it spends anything, so a market that is only a market costs one map lookup and
// no API call. Faults are logged and swallowed, matching MarkScanned beside it —
// the market prices are already persisted, and failing the slot here would cost
// the waypoint its whole market rotation to recover a reading the free catalogue
// pass takes again next tick.
func (s *Scanner) readYard(ctx context.Context, slot SensingSlotView) {
	if s.ports.Yard == nil {
		return
	}
	if err := s.ports.Yard.ReadCatalog(ctx, s.playerID, slot.Waypoint); err != nil {
		s.warn(ctx, "parked_sensing_yard_read_failed", slot.Waypoint,
			fmt.Sprintf("failed to read the shipyard at %s while scanning it: %v", slot.Waypoint, err))
	}
}

// observe reads the spread the scan just wrote, and reports whether there is an
// observation to fold in at all. A slot with no whitelist has nothing to measure
// and an unreadable market is an unanswered question — neither is a zero. A zero
// IS a meaningful observation (a market that stopped quoting the goods we watch
// should decay out of the hot rotation), so it must not be manufactured.
func (s *Scanner) observe(ctx context.Context, slot SensingSlotView) (float64, bool) {
	if len(slot.Whitelist) == 0 {
		return 0, false
	}
	prices, err := s.ports.SpreadOf.MarketPrices(ctx, s.playerID, slot.Waypoint)
	if err != nil {
		s.warn(ctx, "parked_sensing_spread_read_failed", slot.Waypoint,
			fmt.Sprintf("failed to read spread at %s: %v", slot.Waypoint, err))
		return 0, false
	}

	spread, inverted := RelativeSpread(prices, slot.Whitelist)
	if inverted > 0 {
		// One line per scan, not per good. An inverted quote is impossible market
		// data with two causes — a transposed persisted row, or GoodPrice wired
		// from the persisted columns by name — so the message names both.
		s.warn(ctx, "parked_sensing_inverted_quote", slot.Waypoint,
			fmt.Sprintf("%d good(s) at %s quote an ask below their bid and were skipped; "+
				"check what the scanner persisted (market_data.purchase_price must EXCEED "+
				"sell_price), then the GoodPrice wiring (Bid<-sell_price, Ask<-purchase_price)",
				inverted, slot.Waypoint))
	}
	return spread, true
}

// requeue returns a scanned slot to the rotation with what the scan learned, and
// wrote says whether the turn produced market data — it advances LastDataAt,
// while LastScan (the pacing clock) advances either way, the slot having had its
// turn. The stored view is re-read rather than overwritten wholesale: a reconcile
// may have refreshed this slot mid-scan, and the worker's copy is older for
// everything EXCEPT the two fields it just measured. A slot that left membership
// mid-scan is dropped here, the only place an in-flight slot can leave. The total
// weight is deliberately not recomputed — SyncMembership renormalises wholesale,
// and adjusting it per scan would re-pace every slot on one observation.
func (s *Scanner) requeue(waypoint string, at time.Time, spreadEWMA float64, wrote bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.scanning, waypoint)

	view, ok := s.members[waypoint]
	if !ok {
		return
	}
	view.SpreadEWMA = spreadEWMA
	view.LastScan = at
	if wrote {
		view.LastDataAt = at
	}
	s.members[waypoint] = view

	heap.Push(s.due, s.scheduleFor(view))

	// Tell the pacer the rotation changed under it. Non-blocking: a full buffer
	// means a re-evaluation is already pending. Sent under the lock so a pacer
	// that has computed its sleep but not yet entered the select cannot miss it.
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// note records one completed turn. A closure rather than three methods so the
// counter is named at the call site, beside the branch that decided it.
func (s *Scanner) note(record func(*ScanOutcomes)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record(&s.outcomes)
}

// TakeScanOutcomes reports what the rotation produced since it was last asked, and
// RESETS the tally — so a cycle line describes the interval it covers and a budget
// that has just begun declining is not averaged away against the whole process.
func (s *Scanner) TakeScanOutcomes() ScanOutcomes {
	s.mu.Lock()
	defer s.mu.Unlock()
	taken := s.outcomes
	s.outcomes = ScanOutcomes{}
	return taken
}

// release drops an in-flight mark without returning the slot to the heap. Used
// only where a slot was taken but never scanned.
func (s *Scanner) release(waypoint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.scanning, waypoint)
}

// memberView reads one slot's current view.
func (s *Scanner) memberView(waypoint string) (SensingSlotView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.members[waypoint]
	return v, ok
}

// dueAt is when a slot next falls due: its weighted turn, held back for a yard
// by the quartermaster's cadence. The later of the two wins, so the cadence can
// only ever slow a yard down.
func (s *Scanner) dueAt(sched domainSensing.SlotSchedule, view SensingSlotView) time.Time {
	when := domainSensing.NextDue(sched, s.totalWeight, s.rate)
	if view.Kind == SlotKindYard && view.YardCadence > 0 {
		if floor := sched.LastScan.Add(view.YardCadence); floor.After(when) {
			return floor
		}
	}
	return when
}

// weightOf is a slot's share of the scan budget.
func (s *Scanner) weightOf(v SensingSlotView) float64 {
	if v.Kind == SlotKindYard {
		return yardScanWeight
	}
	return domainSensing.ScanWeight(v.SpreadEWMA, s.median, s.clampR)
}

// scheduleFor projects a slot into the heap's own view of it.
func (s *Scanner) scheduleFor(v SensingSlotView) domainSensing.SlotSchedule {
	return domainSensing.SlotSchedule{
		Waypoint: v.Waypoint,
		Weight:   s.weightOf(v),
		LastScan: v.LastScan,
	}
}

// restore pushes held entries back. Due times are recomputed identically on
// push, so a pop-and-restore leaves the rotation exactly as it found it.
func (s *Scanner) restore(held []domainSensing.SlotSchedule) {
	for _, sched := range held {
		heap.Push(s.due, sched)
	}
}

// RotationSize reports the rotation as the pacer currently holds it: how many
// slots are members, and the rate they are being paced at. Membership is the
// number the reconcile CANNOT derive for itself — it hands in the ledger's parked
// slots, but the rotation keeps only the scannable ones, so a heartbeat reporting
// the ledger count would overstate what is being watched. The rate travels with
// it because N slots at R req/s is the cadence and neither alone is.
func (s *Scanner) RotationSize() (members int, rate float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.members), s.rate
}

// pendingWaypoints lists what is currently IN the rotation — excluding, by
// construction, anything in flight. Test introspection; the heap exposes no
// iteration, so it drains and restores.
func (s *Scanner) pendingWaypoints() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	held := make([]domainSensing.SlotSchedule, 0, s.due.Len())
	for s.due.Len() > 0 {
		held = append(held, heap.Pop(s.due).(domainSensing.SlotSchedule))
	}
	out := make([]string, 0, len(held))
	for _, sched := range held {
		out = append(out, sched.Waypoint)
	}
	s.restore(held)
	return out
}

// warn records a scan-path fault. Every fault here is non-fatal by design — the
// slot returns to the rotation regardless — so the log line is the only trace.
func (s *Scanner) warn(ctx context.Context, action, waypoint, msg string) {
	common.LoggerFromContext(ctx).Log("WARN", msg, map[string]interface{}{
		"action":   action,
		"waypoint": waypoint,
	})
}

// inRotation reports whether a placement is scannable at all. PARKED is required
// because a scan is issued FROM the probe standing there, and MARKET and YARD are
// the only kinds with anything to read. The SPARE exclusion is structural: a spare
// is a hull in reserve whose row the placement machine is actively transitioning,
// and admitting it would put scan writes on a row the placement path owns.
func inRotation(v SensingSlotView) bool {
	if v.State != SlotStateParked {
		return false
	}
	return v.Kind == SlotKindMarket || v.Kind == SlotKindYard
}

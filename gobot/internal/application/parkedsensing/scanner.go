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
// that actually spends the scan budget the rest of the model sizes.
//
// ONE pacer goroutine issues every parked-market scan in the fleet. That is the
// whole design. Per-probe scan loops would each have to guess at a share of the
// rate limiter and would collectively overrun it exactly when the fleet is
// busiest; a single pacer holds the fleet-wide rotation, so the budget the
// reconcile hands in is the rate that is actually spent, whatever the fleet
// looks like.
//
// Work is executed by a bounded pool of short-lived workers. The bound is the
// backpressure reflex: when every in-flight token is held, the pacer BLOCKS on
// acquisition rather than queueing, so a slow or degraded API throttles scan
// issuance at the source instead of building a backlog of scans against prices
// that have since moved.
//
// The rotation is in memory and is rebuilt from the ledger by each reconcile.
// Nothing here is durable and nothing needs to be: a restart re-reads the slots,
// and every slot's last_scan_at survives in the ledger, so the rotation resumes
// with its pacing intact.

// emptyRotationPoll is how long the pacer parks when it has nothing to scan.
// Membership only changes at a reconcile, so this is a liveness poll, not a
// pacing decision — it costs no API call and simply keeps the pacer responsive
// to the next SyncMembership.
const emptyRotationPoll = 5 * time.Second

// yardScanWeight is the fixed share a shipyard slot earns. A yard is watched for
// hull prices, which move on their own schedule and not with market spread, so
// it is neither promoted nor demoted by the spread weighting — it holds the
// baseline share and its own cadence floor.
const yardScanWeight = 1.0

// defaultInflightCap is the in-flight bound used when the caller passes a
// non-positive one. Three concurrent scans is enough to keep the pacer from
// serialising on API latency and small enough that a degraded API is felt as
// backpressure within a few scans.
const defaultInflightCap = 3

// minWeightClamp is the lowest weighting clamp the scanner will operate at. A
// clamp below 1 collapses ScanWeight's optimistic prior to zero, which
// Interval reads as a degenerate weight and degrades that slot to hourly
// scans — so an operator who sets the knob to 0 would silently darken the
// rotation rather than merely flatten it. Flattening is the intended meaning of
// a low clamp, so it is clamped up to it.
const minWeightClamp = 1

// scanGuardComponent labels the panic guard around a scan worker.
const scanGuardComponent = "parked-sensing-scan:"

// SensingSlotView is one parked placement as the scan rotation sees it: where to
// scan, what to measure there, and when it was last measured.
//
// It is a projection of the ledger row, handed in wholesale by each reconcile.
// The scanner holds no other membership state, so a slot that stops being
// PARKED simply stops appearing and leaves the rotation.
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
	// entirely rather than read as a zero.
	Whitelist []string
	// SpreadEWMA is the smoothed relative spread observed so far. Zero means
	// unmeasured, which ScanWeight deliberately treats as optimistic rather
	// than as the worst possible reading.
	SpreadEWMA float64
	// LastScan is the freshness stamp the rotation paces against. The zero time
	// means never scanned, which makes the slot due immediately.
	LastScan time.Time
	// YardCadence is the quartermaster's re-read interval, and applies to YARD
	// slots only. It is a FLOOR on the interval, never a target: a yard falls
	// due at the later of its weighted turn and last scan + cadence, so the
	// budget can slow a yard down but never speed it past the cadence.
	YardCadence time.Duration
}

// MarketScanRunner performs one market scan and persists what it found.
//
// The implementation is an adapter over the existing market scanner, and it —
// not this package — is where the call is tagged as scanning-source, low-priority
// work. Scans are the one class of call that must lose a contended rate-limit
// token to every other consumer, and putting that tag in the adapter keeps this
// layer free of an API-client import.
type MarketScanRunner interface {
	Run(ctx context.Context, playerID int, waypoint string) error
}

// SpreadObserver reads the prices a completed scan just persisted for ONE
// waypoint. It is deliberately a narrow per-waypoint read and not a market
// listing: the scanner's per-scan cost must not grow with how many markets the
// player has visited.
type SpreadObserver interface {
	MarketPrices(ctx context.Context, playerID int, waypoint string) ([]GoodPrice, error)
}

// ScanLedger is the scan path's slice of the placement ledger: one write,
// recording that a slot was scanned and what it showed.
//
// It is a separate, narrower interface from the screen's SlotLedger and the
// queue's BuyLedger for the same reason those are separate from each other, and
// here the separation is stronger than convention: MarkScanned touches only the
// freshness and spread columns, while every state transition belongs to the
// other two. Those write sets are disjoint BY CONSTRUCTION, which is what lets
// the pacer run concurrently with the reconcile without either fighting the
// other for a row.
type ScanLedger interface {
	MarkScanned(ctx context.Context, playerID int, waypoint, kind string, at time.Time, spreadEWMA float64) error
}

// ScanPorts is everything the scanner needs from the outside world.
//
// There is deliberately no rate port and no pressure port. The pacer does not
// size its own budget: the reconcile computes the rate from the API budget
// (including the pressure brake) and hands it to SyncMembership, so there is
// exactly one place the rate is decided and no way for the pacer to brake
// against pressure a second time.
type ScanPorts struct {
	Scan     MarketScanRunner
	Ledger   ScanLedger
	SpreadOf SpreadObserver
	// Yard reads the SHIPYARD standing at the same waypoint the scan just read,
	// which is how a waypoint that is both a market and a shipyard comes to be
	// sensed as BOTH.
	//
	// It closes the blind spot the slot KIND created. A probe-selling yard that is
	// also a whitelisted market is placed as a MARKET slot (that kind carries the
	// goods list; YARD does not), so the hull standing there was a market sensor
	// and nothing ever asked it about the counter under its feet — measured live,
	// nine shipyards had one of our hulls parked on them and no recorded
	// inventory at all. The fix is to stop deciding by kind: EVERY parked scan
	// also reads the yard, and the adapter's cached SHIPYARD-trait check makes
	// that free at the waypoints that are only markets.
	//
	// It is the SAME port the free catalogue pass drives (yardcatalog.go), so a
	// reading taken from a parked hull and one taken from across the map write
	// through one code path. The difference is what they get back: a presence-less
	// read learns only what the yard SELLS, while this one — issued from a hull
	// standing at the counter — also carries the PRICES, which is the only way a
	// yard becomes buyable rather than merely known.
	//
	// Nil-safe so the pacing tests can drive a scanner over no ports at all. It is
	// NOT an arming seam: the coordinator's wired() check requires it, so a
	// production tick cannot run without it.
	Yard YardCatalogReader
}

// ScanKnobs are the operator-set shape of the scanner.
type ScanKnobs struct {
	// InflightCap bounds concurrent scans, and with it how hard a slow API can
	// push back on the pacer.
	InflightCap int
	// ClampR is the ceiling on how much more attention the hottest market may
	// earn than the baseline. A clamp of 1 flattens the weighting entirely.
	ClampR int
}

// Scanner owns the fleet-wide scan rotation.
//
// The mutex guards the rotation as a whole — the heap, the membership map, the
// budget totals it was normalised against, and the set of waypoints currently
// being scanned. They are one consistent picture and are never locked
// separately: a weight read against one total and a due time computed against
// another would silently mis-pace the rotation.
type Scanner struct {
	playerID int
	ports    ScanPorts
	clock    shared.Clock
	clampR   int

	// tokens is the in-flight bound. A worker holds one for its whole life, and
	// the pacer blocks acquiring one — see launch.
	tokens chan struct{}
	// wake nudges a sleeping pacer when a worker returns a slot to the
	// rotation. Buffered to one and only ever sent to without blocking, so a
	// worker never waits on the pacer and a burst of completions collapses to
	// the single re-evaluation it warrants. See requeue for why it exists.
	wake chan struct{}
	// workers tracks live scans so RunPacer can drain before returning.
	workers sync.WaitGroup

	mu sync.Mutex
	// due is the rotation. A slot is IN the heap or IN FLIGHT, never both.
	due *domainSensing.NextDueHeap
	// members is every slot eligible to be scanned, by waypoint.
	members map[string]SensingSlotView
	// scanning is the waypoints a worker currently holds. They are absent from
	// the heap and are not re-added by a reconcile — only the worker's
	// completion path returns them, which is what makes two concurrent scans of
	// one market structurally impossible.
	scanning map[string]struct{}

	totalWeight float64
	rate        float64
	median      float64
}

// NewScanner builds a scanner for one player.
//
// The rotation starts empty: membership arrives from the first reconcile, which
// is also what supplies the rate. A scanner whose pacer is started before its
// first SyncMembership simply polls until it has members.
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
// read, re-rated to the budget it just computed.
//
// The whole heap is rebuilt on every call rather than diffed. Renormalisation is
// wholesale by nature — one slot joining changes the total weight and therefore
// every other slot's interval — and at reconcile cadence over a rotation of this
// size the rebuild is far cheaper than the bookkeeping a diff would need to be
// correct. Each slot's due time is recomputed from its own last scan, so a slot
// that has been waiting keeps the credit for that wait.
//
// Slots currently in flight are deliberately NOT re-added. They are still
// members (the worker's completion path reads the refreshed view), but the heap
// must not hold a copy of a slot a worker is already scanning.
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
	// does. A pacer that went to sleep on an EMPTY rotation is parked on the 5s
	// liveness poll, and one that slept on a distant due time is parked for
	// longer still — either way the members this call just admitted would wait
	// out a sleep computed before they existed. That is most visible on the very
	// first reconcile after a restart, which is exactly when the rotation is
	// being populated from nothing.
	//
	// Non-blocking and sent under the lock, matching requeue: the buffer holds
	// the one signal a sleeping pacer needs, a full buffer means a re-evaluation
	// is already pending, and sending before the unlock closes the window where a
	// pacer that has computed its sleep but not yet entered the select would miss
	// the signal.
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// RunPacer issues scans until ctx is cancelled, then drains its workers.
//
// The loop is deliberately trivial: every scheduling decision is nextAction's,
// and every backpressure decision is launch's. Sleeping selects on ctx rather
// than the clock so a cancelled coordinator stops promptly instead of at the end
// of a scan interval.
func (s *Scanner) RunPacer(ctx context.Context) {
	defer s.workers.Wait()

	for {
		if ctx.Err() != nil {
			return
		}

		waypoint, sleepFor, ok := s.nextAction(s.clock.Now())
		if !ok {
			// The wake case is what keeps a SMALL rotation spending its budget.
			// Once every member is in flight the heap is empty and sleepFor is
			// the empty-rotation poll — a fixed 5s that has nothing to do with
			// the scan rate, and that a fleet of a few probes would otherwise
			// sit through after every pass. Sleeping on the timer ALONE also
			// oversleeps whenever a completing scan falls due sooner than the
			// slot this sleep was computed for. Either way the budget is
			// under-spent silently, so the pacer re-evaluates on every
			// completion instead.
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
			// the mark back so the waypoint is not stranded as permanently
			// in-flight, and take the next one.
			s.release(waypoint)
			continue
		}
		s.launch(ctx, view)
	}
}

// nextAction is the pacer's whole scheduling decision, as a pure function of the
// rotation and the time: either a waypoint to scan now, or how long to wait.
//
// Popping is what takes a slot OUT of the rotation, and nothing puts it back
// except a worker completing. That is the no-overlap property: at most one scan
// per market can be in flight, without a lock held across the scan itself.
//
// The sweep past a not-yet-due slot exists for the yard cadence. The heap orders
// on the unfloored due time, so a yard held back by its cadence can surface as
// the heap minimum while a market behind it is genuinely due — sleeping on the
// yard would idle the entire rotation for a cadence. Slots passed over are held
// and pushed back unchanged, and the sweep is bounded by the rotation size.
func (s *Scanner) nextAction(now time.Time) (string, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := s.due.Len()
	if n == 0 {
		return "", emptyRotationPoll, false
	}

	var held []domainSensing.SlotSchedule
	var earliest time.Time
	for i := 0; i < n; i++ {
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
// it.
//
// Blocking here rather than queueing is the point. The in-flight bound is the
// only place the fleet's scan issuance responds to how fast scans actually
// complete, so a degraded API stalls the pacer — which is precisely the
// behaviour wanted, since a scan whose result arrives late is a price that has
// already moved.
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
		// A panicking scan must cost one slot one turn, never the fleet's whole
		// rotation. runScan returns the slot to the heap on the way out of the
		// panic, and the guard absorbs it here.
		supervise.Guard(scanGuardComponent+slot.Waypoint, func() { s.runScan(ctx, slot) })
	}()
}

// runScan is one worker's whole life: scan, measure, record, and return the slot
// to the rotation.
//
// The requeue is DEFERRED, so every exit returns the slot — a failed scan, an
// unreadable market, a ledger that refused the write, or a panic on the way
// through any of them. A slot that failed to leave the rotation on its own turn
// would never scan again until the next reconcile rebuilt the heap around it.
//
// The scan stamp is refreshed even when the scan FAILED. Leaving it stale would
// leave the slot due immediately and turn a failing waypoint into a hot retry
// loop against the API that is already failing. The ledger is not told about a
// failed scan — a freshness stamp is a claim that data was written — so the
// paced retry is a local one, and the next reconcile re-reads the older stamp
// from the ledger and tries again.
func (s *Scanner) runScan(ctx context.Context, slot SensingSlotView) {
	at := s.clock.Now()
	spread := slot.SpreadEWMA
	defer func() { s.requeue(slot.Waypoint, at, spread) }()

	if err := s.ports.Scan.Run(ctx, s.playerID, slot.Waypoint); err != nil {
		at = s.clock.Now()
		s.warn(ctx, "parked_sensing_scan_failed", slot.Waypoint,
			fmt.Sprintf("parked sensing scan of %s failed: %v", slot.Waypoint, err))
		return
	}
	at = s.clock.Now()

	if observed, ok := s.observe(ctx, slot); ok {
		spread = domainSensing.UpdateSpreadEWMA(slot.SpreadEWMA, observed)
	}

	// The second half of a parked probe's turn: whatever else this waypoint is,
	// if it is ALSO a shipyard the hull standing here can price it, and nothing
	// else in the fleet will. See ScanPorts.Yard.
	s.readYard(ctx, slot)

	if err := s.ports.Ledger.MarkScanned(ctx, s.playerID, slot.Waypoint, slot.Kind, at, spread); err != nil {
		// Logged, never fatal to the slot. MarkScanned records freshness; losing
		// that write costs one stale stamp, while dropping the slot for it would
		// cost the waypoint every future scan.
		s.warn(ctx, "parked_sensing_mark_scanned_failed", slot.Waypoint,
			fmt.Sprintf("failed to record sensing scan of %s: %v", slot.Waypoint, err))
	}
}

// readYard records what the shipyard at this waypoint sells and what it charges,
// riding the turn the market scan just took.
//
// IT DECIDES NOTHING ABOUT WHAT THE WAYPOINT IS, deliberately. Whether the
// waypoint carries a SHIPYARD trait is a cached, era-agnostic local fact the
// adapter already reads before it spends anything, so a market that is only a
// market costs one map lookup here and no API call — while a kind test in this
// layer would reproduce the very mistake this exists to fix, deciding by the
// slot's kind what the waypoint actually is.
//
// FAULTS ARE LOGGED AND SWALLOWED, matching MarkScanned beside it. The market
// scan has already succeeded and its prices are already persisted; failing the
// slot for a shipyard read would cost the waypoint its whole market rotation to
// recover a reading the free catalogue pass will take again next tick anyway.
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
// observation to fold in at all.
//
// A slot with no whitelist has nothing to measure, and an unreadable market is
// an unanswered question — neither is a zero. The distinction matters because a
// zero IS a meaningful observation (a market that stopped quoting the goods we
// watch should decay out of the hot rotation), so it must not be manufactured by
// a slot that was never asked or a read that failed.
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
		// One line per scan, not per good. An inverted quote is impossible
		// market data, and the overwhelmingly likely cause is a GoodPrice wired
		// from the persisted columns without crossing them — a fault whose only
		// other symptom is a rotation that quietly stops preferring anything.
		s.warn(ctx, "parked_sensing_inverted_quote", slot.Waypoint,
			fmt.Sprintf("%d good(s) at %s quote an ask below their bid and were skipped; "+
				"check the GoodPrice wiring (Bid<-sell_price, Ask<-purchase_price)", inverted, slot.Waypoint))
	}
	return spread, true
}

// requeue returns a scanned slot to the rotation with what the scan learned.
//
// The stored view is re-read rather than overwritten wholesale: a reconcile may
// have refreshed this slot while the scan was in flight, and the worker's copy
// is older for everything EXCEPT the two fields it just measured. A slot that
// left membership mid-scan is dropped here, which is the only place an in-flight
// slot can leave.
//
// The rotation's total weight is deliberately not recomputed. Weights are
// renormalised wholesale by SyncMembership; adjusting the total on every scan
// would re-pace every other slot mid-rotation on the strength of a single
// observation.
func (s *Scanner) requeue(waypoint string, at time.Time, spreadEWMA float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.scanning, waypoint)

	view, ok := s.members[waypoint]
	if !ok {
		return
	}
	view.SpreadEWMA = spreadEWMA
	view.LastScan = at
	s.members[waypoint] = view

	heap.Push(s.due, s.scheduleFor(view))

	// Tell the pacer the rotation changed under it. Non-blocking: the buffer
	// holds the one signal a sleeping pacer needs, and a full buffer means a
	// re-evaluation is already pending, so dropping this one loses nothing. It
	// is sent under the lock deliberately — a pacer that has computed its sleep
	// but not yet entered the select still finds the signal buffered, so there
	// is no window where a completion is missed.
	select {
	case s.wake <- struct{}{}:
	default:
	}
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
// slots are members, and the rate they are being paced at.
//
// Membership is the number the reconcile CANNOT derive for itself. It reads the
// ledger's parked slots and hands them in, but the rotation keeps only the ones
// that are actually scannable (SPARE hulls and non-PARKED placements are
// dropped), so a heartbeat reporting the ledger count would overstate what is
// being watched — and would go on overstating it while a mis-shaped slot sat in
// the ledger doing nothing. The rate is reported alongside because the two are
// only meaningful together: N slots at R req/s is the cadence, either alone is
// not.
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
// slot is returned to the rotation regardless — so the log line is the only
// trace a persistent failure leaves.
func (s *Scanner) warn(ctx context.Context, action, waypoint, msg string) {
	common.LoggerFromContext(ctx).Log("WARN", msg, map[string]interface{}{
		"action":   action,
		"waypoint": waypoint,
	})
}

// inRotation reports whether a placement is scannable at all.
//
// PARKED is required because a scan is issued FROM the probe standing there.
// MARKET and YARD are the only kinds with anything to read; SPARE is excluded
// on purpose and the exclusion is structural, not cosmetic — a spare is a hull
// in reserve whose row the placement machine is actively transitioning, and
// admitting it would put the scan path's writes on a row the placement path
// owns.
func inRotation(v SensingSlotView) bool {
	if v.State != SlotStateParked {
		return false
	}
	return v.Kind == SlotKindMarket || v.Kind == SlotKindYard
}

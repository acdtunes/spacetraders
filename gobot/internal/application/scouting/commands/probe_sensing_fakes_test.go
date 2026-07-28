package commands

// probe_sensing_fakes_test.go holds the parked-probe coordinator's test doubles.
//
// They are deliberately in one file and share ONE call counter, because the
// property several of these tests assert is negative — "the cutover made no API
// call at all" — and a negative assertion is only as good as its coverage. A fake
// that forgot to count would make the assertion pass by omission. Every port that
// reaches the network in production increments networkCalls; the two that reach
// only the database (the catalog and the gate store) are named as such at their
// definitions.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// callCounter is the shared network-call tally. Every fake that stands in for an
// API-reaching port holds a pointer to one.
type callCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func newCallCounter() *callCounter { return &callCounter{n: map[string]int{}} }

func (c *callCounter) hit(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n[name]++
}

func (c *callCounter) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[name]
}

// total is every counted call, which is what a "made no API call" assertion
// measures.
func (c *callCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	sum := 0
	for _, v := range c.n {
		sum += v
	}
	return sum
}

func (c *callCounter) names() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int, len(c.n))
	for k, v := range c.n {
		out[k] = v
	}
	return out
}

// --- the ledger ---------------------------------------------------------------

// psLedger is an in-memory sensing ledger: the two tables, and the optimistic
// transition between slot states. DB-only in production, so it counts nothing.
//
// MUTEX-GUARDED because the scan pacer now runs concurrently with the reconcile
// and calls MarkScanned from its worker goroutines.
//
// The lock is an artifact of this fake keeping both tables in one Go map. In
// production the two paths ARE disjoint, by column: every writer of sensing_slots
// names only the columns it owns (sp-wgjb7), so MarkScanned's stamp and spread
// cannot be carried back to a stale value by a transition, a screen
// re-declaration or a stand-down, whatever rows they meet on.
//
// The upsert variants below model that ownership rather than blanket-writing the
// row, for the same reason UpsertSystem preserves the seed columns: a fake that
// overwrote everything would let a test pass over exactly the double-purchase
// bug the contract exists to prevent.
type psLedger struct {
	mu sync.Mutex

	systems map[string]parkedsensing.ExpandSystem
	slots   map[string]parkedsensing.QueuedSlot
	// goods and depth ride alongside the slot rows, which QueuedSlot does not
	// carry — the real adapter reads them from the same row.
	goods map[string][]string
	views map[string]parkedsensing.SensingSlotView

	upserted             []parkedsensing.SlotRecord
	systemsErr, slotsErr error
	// upsertErr fails the slot write, which is how a half-done adoption is
	// driven: the posts are already retired, the hulls are not yet recorded.
	upsertErr error
	// transitionErr fails TransitionSlot for ONE waypoint, which is how a torn
	// two-write sequence is driven: it is the only way to observe which of the
	// two writes a pass performs FIRST, and that order is a money guard.
	transitionErr map[string]error

	// systemUpserts is every SystemRecord handed to UpsertSystem, kept raw so a
	// test can assert the WRITE SET rather than only its effect — which is what
	// pins "this row asserts nothing it does not know".
	systemUpserts []parkedsensing.SystemRecord
	// systemUpsertErr fails the verdict write for ONE system.
	systemUpsertErr map[string]error

	// order is the sequence of ledger calls this tick made, for the tests that
	// pin WHERE a stage runs rather than what it did. Each call is tagged with
	// the arguments that identify its stage: SlotsByState is issued by several
	// stages and it is the state list, not the method name, that tells them
	// apart.
	//
	// THE STATE LIST NO LONGER DISAMBIGUATES EVERY STAGE. The adoption pass and
	// the heartbeat census both read all five states, in the same order, so they
	// emit a byte-identical tag. Nothing binds to it today — the only stage tags
	// any test asserts on are the reaper's ("QUEUED") and the drain's
	// ("WANTED,QUEUED"), both still unique — but a future ordering assertion on
	// the five-state read would silently bind to ADOPTION's, which runs first.
	// That is exactly the "passes by never matching anything" failure the
	// indexOf comment below warns about, so check uniqueness before writing one.
	order []string
}

// record appends one stage-identifying call. The caller already holds mu.
func (f *psLedger) record(event string) { f.order = append(f.order, event) }

// indexOf reports where an event first appears in the tick's call sequence, or
// -1. Ordering assertions compare these, so a stage that never ran reads as
// "before everything" — hence every such test also asserts the stage ran at all.
func (f *psLedger) indexOf(event string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, got := range f.order {
		if got == event {
			return i
		}
	}
	return -1
}

func newPSLedger() *psLedger {
	return &psLedger{
		systems: map[string]parkedsensing.ExpandSystem{},
		slots:   map[string]parkedsensing.QueuedSlot{},
		goods:   map[string][]string{},
		views:   map[string]parkedsensing.SensingSlotView{},
	}
}

func (f *psLedger) ExistingSlots(_ context.Context, _ int, system string) ([]parkedsensing.ExistingSlot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := []parkedsensing.ExistingSlot{}
	for _, slot := range f.slots {
		if slot.System != system {
			continue
		}
		out = append(out, parkedsensing.ExistingSlot{
			Waypoint:       slot.Waypoint,
			WhitelistGoods: f.goods[slot.Waypoint],
			DepthCredits:   slot.DepthCredits,
		})
	}
	return out, nil
}

// UpsertSlotMetadata declares a placement: the screen's measurements only, once
// the waypoint already carries one. It cannot walk the state machine backwards
// or write its empty hull over a placement someone else filled — see the type
// comment for the ownership contract this mirrors.
func (f *psLedger) UpsertSlotMetadata(_ context.Context, _ int, slot parkedsensing.SlotRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserted = append(f.upserted, slot)
	if existing, held := f.slots[slot.Waypoint]; held {
		existing.System = slot.System
		existing.DepthCredits = slot.DepthCredits
		f.slots[slot.Waypoint] = existing
		f.goods[slot.Waypoint] = slot.WhitelistGoods
		return nil
	}
	f.slots[slot.Waypoint] = parkedsensing.QueuedSlot{
		Waypoint:     slot.Waypoint,
		System:       slot.System,
		Kind:         slot.Kind,
		State:        slot.State,
		AssignedShip: slot.AssignedShip,
		DepthCredits: slot.DepthCredits,
	}
	f.goods[slot.Waypoint] = slot.WhitelistGoods
	return nil
}

// UpsertSpareSlot records the hull standing at a waypoint. It re-points an
// existing row at that hull and leaves the screen's measurements alone — a
// stand-down measures nothing, and an emptied goods list would drop the waypoint
// out of the screen's hit set for good.
func (f *psLedger) UpsertSpareSlot(_ context.Context, _ int, slot parkedsensing.SlotRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserted = append(f.upserted, slot)
	if existing, held := f.slots[slot.Waypoint]; held {
		existing.System = slot.System
		existing.Kind = slot.Kind
		existing.State = slot.State
		existing.AssignedShip = slot.AssignedShip
		f.slots[slot.Waypoint] = existing
		return nil
	}
	f.slots[slot.Waypoint] = parkedsensing.QueuedSlot{
		Waypoint:     slot.Waypoint,
		System:       slot.System,
		Kind:         slot.Kind,
		State:        slot.State,
		AssignedShip: slot.AssignedShip,
		DepthCredits: slot.DepthCredits,
	}
	f.goods[slot.Waypoint] = slot.WhitelistGoods
	return nil
}

func (f *psLedger) UpsertSystem(_ context.Context, _ int, record parkedsensing.SystemRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.systemUpserts = append(f.systemUpserts, record)
	if err := f.systemUpsertErr[record.System]; err != nil {
		return err
	}
	existing := f.systems[record.System]
	f.systems[record.System] = parkedsensing.ExpandSystem{
		System:         record.System,
		Verdict:        record.Verdict,
		UnchartedCount: record.UnchartedCount,
		CatalogKnown:   record.CatalogKnown,
		// The seed columns are a DISJOINT write set in the real repository, so
		// the fake preserves them too — a fake that cleared them here would hide
		// exactly the orphaned-hull bug that discipline exists to prevent.
		SeedShip:  existing.SeedShip,
		SeedState: existing.SeedState,
	}
	return nil
}

func (f *psLedger) SlotsByState(_ context.Context, _ int, states ...string) ([]parkedsensing.QueuedSlot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.record("SlotsByState:" + strings.Join(states, ","))
	if f.slotsErr != nil {
		return nil, f.slotsErr
	}
	wanted := map[string]bool{}
	for _, s := range states {
		wanted[s] = true
	}
	out := []parkedsensing.QueuedSlot{}
	for _, slot := range sortedSlots(f.slots) {
		if wanted[slot.State] {
			out = append(out, slot)
		}
	}
	return out, nil
}

func (f *psLedger) SlotsBySystem(_ context.Context, _ int, system string) ([]parkedsensing.QueuedSlot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := []parkedsensing.QueuedSlot{}
	for _, slot := range sortedSlots(f.slots) {
		if slot.System == system {
			out = append(out, slot)
		}
	}
	return out, nil
}

func (f *psLedger) SystemsByVerdict(_ context.Context, _ int, verdict string) ([]parkedsensing.ScreenedSystem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.record("SystemsByVerdict:" + verdict)
	out := []parkedsensing.ScreenedSystem{}
	for _, system := range sortedSystems(f.systems) {
		if system.Verdict == verdict {
			out = append(out, parkedsensing.ScreenedSystem{System: system.System})
		}
	}
	return out, nil
}

func (f *psLedger) CountOwnedProbes(_ context.Context, _ int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var owned int64
	for _, slot := range f.slots {
		switch slot.State {
		case parkedsensing.SlotStateBought, parkedsensing.SlotStateInTransit, parkedsensing.SlotStateParked:
			owned++
		}
	}
	return owned, nil
}

func (f *psLedger) TransitionSlot(_ context.Context, _ int, waypoint, fromState, toState string, set parkedsensing.SlotFields) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.record("TransitionSlot:" + waypoint + ":" + fromState + "→" + toState)
	if err := f.transitionErr[waypoint]; err != nil {
		return err
	}
	slot, ok := f.slots[waypoint]
	if !ok || slot.State != fromState {
		return parkedsensing.ErrSlotClaimed
	}
	slot.State = toState
	if set.AssignedShip != nil {
		slot.AssignedShip = *set.AssignedShip
	}
	if set.PurchaseYard != nil {
		slot.PurchaseYard = *set.PurchaseYard
	}
	f.slots[waypoint] = slot
	return nil
}

func (f *psLedger) MarkScanned(_ context.Context, _ int, waypoint string, at time.Time, spreadEWMA float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	view := f.views[waypoint]
	view.Waypoint, view.LastScan, view.SpreadEWMA = waypoint, at, spreadEWMA
	f.views[waypoint] = view
	return nil
}

func (f *psLedger) Systems(_ context.Context, _ int) ([]parkedsensing.ExpandSystem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.systemsErr != nil {
		return nil, f.systemsErr
	}
	return sortedSystems(f.systems), nil
}

func (f *psLedger) SetSeed(_ context.Context, _ int, system, shipSymbol, seedState string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	entry := f.systems[system]
	entry.System, entry.SeedShip, entry.SeedState = system, shipSymbol, seedState
	f.systems[system] = entry
	return nil
}

func (f *psLedger) StampCatalogSynced(_ context.Context, _ int, system string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	entry := f.systems[system]
	entry.System, entry.CatalogKnown = system, true
	f.systems[system] = entry
	return nil
}

func (f *psLedger) DeleteSlot(_ context.Context, _ int, waypoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.slots, waypoint)
	return nil
}

func (f *psLedger) ParkedSlotViews(_ context.Context, _ int) ([]parkedsensing.SensingSlotView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := []parkedsensing.SensingSlotView{}
	for _, slot := range sortedSlots(f.slots) {
		if slot.State != parkedsensing.SlotStateParked {
			continue
		}
		view := f.views[slot.Waypoint]
		view.Waypoint, view.Kind, view.State = slot.Waypoint, slot.Kind, slot.State
		view.Whitelist = f.goods[slot.Waypoint]
		out = append(out, view)
	}
	return out, nil
}

// --- the map reads (database-only in production; they count nothing) ----------

// fakeCatalog is the persisted waypoint catalog and shipyard inventory.
type fakeCatalog struct {
	markets    map[string][]string // system → charted marketplace waypoints
	uncharted  map[string][]string // system → uncharted waypoints
	yards      map[string][]string // system → probe-selling yards
	heavyYards map[string][]string // system → heavy-selling yards (sp-fwk8z T3)
	known      map[string]bool     // system → is the waypoint list swept?
	knownErr   error
	// marketsErr fails the market list for ONE system, which is how a single
	// system's screen is made to fail while its siblings succeed. It is the
	// first read ScreenSystem makes, so the error propagates out of the whole
	// screen — the shape of a DB read failing mid-census.
	marketsErr map[string]error
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{
		markets:   map[string][]string{},
		uncharted: map[string][]string{},
		yards:     map[string][]string{},
		known:     map[string]bool{},
	}
}

func (f *fakeCatalog) ListMarketWaypoints(_ context.Context, system string) ([]string, error) {
	if err := f.marketsErr[system]; err != nil {
		// Adversarial: the markets are handed over ALONGSIDE the error, so a
		// screen that swallowed it would go on to record a real verdict for a
		// system it could not read — and the tests would catch that rather than
		// be masked by a convenient empty list.
		return f.markets[system], err
	}
	return f.markets[system], nil
}

func (f *fakeCatalog) ListUnchartedCount(_ context.Context, system string) (int, error) {
	return len(f.uncharted[system]), nil
}

func (f *fakeCatalog) UnchartedWaypoints(_ context.Context, system string) ([]string, error) {
	return f.uncharted[system], nil
}

func (f *fakeCatalog) ListProbeYards(_ context.Context, system string) ([]string, error) {
	return f.yards[system], nil
}

// ListHeavyYards is the heavy-selling-yard fallback (sp-fwk8z T3). Empty by default: these
// coordinator tests are about probe yards, and the screen only consults this when no probe
// yard exists, so an empty map keeps every existing expectation intact.
func (f *fakeCatalog) ListHeavyYards(_ context.Context, system string) ([]string, error) {
	return f.heavyYards[system], nil
}

func (f *fakeCatalog) CatalogKnown(_ context.Context, system string) (bool, error) {
	if f.knownErr != nil {
		return false, f.knownErr
	}
	return f.known[system], nil
}

// fakeMarketGoods is the local market cache: goods, depth, and quotes.
type fakeMarketGoods struct {
	goods  map[string][]string
	quotes map[string][]parkedsensing.GoodPrice
}

func newFakeMarketGoods() *fakeMarketGoods {
	return &fakeMarketGoods{goods: map[string][]string{}, quotes: map[string][]parkedsensing.GoodPrice{}}
}

func (f *fakeMarketGoods) GoodsAt(_ context.Context, _ int, waypoint string) ([]string, bool, error) {
	goods, known := f.goods[waypoint]
	return goods, known, nil
}

func (f *fakeMarketGoods) DepthRowsAt(_ context.Context, _ int, waypoint string) ([]domainScouting.MarketDepthRow, error) {
	rows := []domainScouting.MarketDepthRow{}
	for _, good := range f.goods[waypoint] {
		rows = append(rows, domainScouting.MarketDepthRow{
			System: shared.ExtractSystemSymbol(waypoint), Waypoint: waypoint,
			Good: good, TradeVolume: 100, MidPrice: 500,
		})
	}
	return rows, nil
}

func (f *fakeMarketGoods) MarketPrices(_ context.Context, _ int, waypoint string) ([]parkedsensing.GoodPrice, error) {
	return f.quotes[waypoint], nil
}

// fakeGates is the stored gate adjacency — a database read in production.
type fakeGates struct{ edges map[string][]string }

func (f *fakeGates) Neighbours(_ context.Context, system string) ([]string, error) {
	if f.edges == nil {
		return nil, nil
	}
	return f.edges[system], nil
}

// --- the network-reaching ports ----------------------------------------------

// fakeRemoteMarket is the screen's API gap fill. Counted.
type fakeRemoteMarket struct {
	calls *callCounter
	goods map[string][]string
}

func (f *fakeRemoteMarket) FetchGoods(_ context.Context, _ int, _, waypoint string) ([]string, error) {
	f.calls.hit("remote_market_fetch")
	goods, ok := f.goods[waypoint]
	if !ok {
		return nil, errors.New("no remote data")
	}
	return goods, nil
}

// psTreasury is the live-credits read behind the buy floor. Counted.
type psTreasury struct {
	calls   *callCounter
	credits int64
	err     error
}

func (f *psTreasury) LiveCredits(context.Context, int) (int64, error) {
	f.calls.hit("treasury")
	return f.credits, f.err
}

// fakeCargoSpend is the trading fleet's measured cargo outflow. Database-derived
// in production, so it is not counted as a network call.
type fakeCargoSpend struct{ spend int64 }

func (f *fakeCargoSpend) AbsCargoBuySpendSince(context.Context, int, time.Time) (int64, error) {
	return f.spend, nil
}

// psPurchaser prices and buys probes. Counted, per verb.
type psPurchaser struct {
	calls *callCounter
	price int64
	next  int
}

func (f *psPurchaser) Quote(context.Context, int, string) (int64, error) {
	f.calls.hit("quote")
	return f.price, nil
}

func (f *psPurchaser) Buy(context.Context, int, string, string) (parkedsensing.BoughtProbe, error) {
	f.calls.hit("buy")
	f.next++
	return parkedsensing.BoughtProbe{ShipSymbol: "PROBE-BOUGHT-" + string(rune('A'+f.next-1)), Price: f.price}, nil
}

// fakeShipPositions is the ships-table read. Database-only.
type fakeShipPositions struct {
	at     map[string]parkedsensing.ShipPos
	docked map[string]string
}

func (f *fakeShipPositions) DockedProbeAt(_ context.Context, _ int, waypoint string) (string, bool, error) {
	ship, ok := f.docked[waypoint]
	return ship, ok, nil
}

func (f *fakeShipPositions) ShipAt(_ context.Context, _ int, shipSymbol string) (parkedsensing.ShipPos, error) {
	pos, ok := f.at[shipSymbol]
	pos.Found = ok
	return pos, nil
}

// fakeFleetTagger records dedication writes. Database-only.
type fakeFleetTagger struct {
	tagged map[string]string
	err    error
}

func newFakeFleetTagger() *fakeFleetTagger { return &fakeFleetTagger{tagged: map[string]string{}} }

func (f *fakeFleetTagger) AssignFleet(_ context.Context, _ int, shipSymbol, fleet string) error {
	if f.err != nil {
		return f.err
	}
	f.tagged[shipSymbol] = fleet
	return nil
}

// fakeMover issues movement commands. Counted.
type fakeMover struct {
	calls *callCounter
	// moves records "ship→destination" for every movement command issued. The
	// counter alone cannot answer "was THIS hull actually flown to THAT
	// placement?", which is the end-to-end question a claim path has to settle:
	// writing IN_TRANSIT for a hull nothing ever told to move is precisely the
	// bug dispatchClaim exists to fix.
	moves []string
}

func (f *fakeMover) NavigateWithin(_ context.Context, _ int, shipSymbol, destination string) error {
	f.calls.hit("navigate")
	f.moves = append(f.moves, shipSymbol+"→"+destination)
	return nil
}

func (f *fakeMover) RouteAcross(_ context.Context, _ int, shipSymbol, destination string) error {
	f.calls.hit("route")
	f.moves = append(f.moves, shipSymbol+"→"+destination)
	return nil
}

func (f *fakeMover) Dock(context.Context, int, string) error {
	f.calls.hit("dock")
	return nil
}

// fakeSeedCommander drives charting seeds. Every verb is an API call. Counted.
type fakeSeedCommander struct {
	calls   *callCounter
	syncErr error
	synced  []string
	hasMkt  map[string]bool
}

func (f *fakeSeedCommander) JumpTo(context.Context, int, string, string) error {
	f.calls.hit("jump")
	return nil
}

func (f *fakeSeedCommander) NavigateTo(context.Context, int, string, string) error {
	f.calls.hit("seed_navigate")
	return nil
}

func (f *fakeSeedCommander) Chart(context.Context, int, string) error {
	f.calls.hit("chart")
	return nil
}

func (f *fakeSeedCommander) RefreshWaypoint(_ context.Context, _ int, _, waypoint string) (bool, error) {
	f.calls.hit("refresh_waypoint")
	return f.hasMkt[waypoint], nil
}

func (f *fakeSeedCommander) ReadMarketAt(context.Context, int, string) error {
	f.calls.hit("read_market")
	return nil
}

func (f *fakeSeedCommander) SyncWaypoints(_ context.Context, _ int, system string) error {
	f.calls.hit("sync_waypoints")
	if f.syncErr != nil {
		return f.syncErr
	}
	f.synced = append(f.synced, system)
	return nil
}

// fakeScanRunner performs parked market scans. Counted.
type fakeScanRunner struct{ calls *callCounter }

func (f *fakeScanRunner) Run(context.Context, int, string) error {
	f.calls.hit("scan")
	return nil
}

// --- the coordinator's own ports ----------------------------------------------

// fakeHome resolves the headquarters system. Database-only.
type fakeHome struct {
	system string
	err    error
}

func (f *fakeHome) HomeSystem(context.Context, int) (string, error) { return f.system, f.err }

// fakeBudget reports the fleet's observed API spend.
type fakeBudget struct {
	ceiling    float64
	nonSensing float64
	charting   float64
}

func (f *fakeBudget) NonSensingRate(time.Duration) float64 { return f.nonSensing }
func (f *fakeBudget) ChartingRate(time.Duration) float64   { return f.charting }
func (f *fakeBudget) CeilingReqPerSec() float64            { return f.ceiling }

// fakeDepthReader is the cutover's census read.
type fakeDepthReader struct {
	rows  []domainScouting.MarketDepthRow
	calls int
	err   error
}

func (f *fakeDepthReader) MarketDepthRows(context.Context, int) ([]domainScouting.MarketDepthRow, error) {
	f.calls++
	return f.rows, f.err
}

// fakePostRepo is the scout-posts table the cutover retires.
type fakePostRepo struct {
	posts   []*domainScouting.ScoutPost
	removed []string
	listErr error
}

func (f *fakePostRepo) ListActive(context.Context, int) ([]*domainScouting.ScoutPost, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*domainScouting.ScoutPost, 0, len(f.posts))
	for _, post := range f.posts {
		if !containsSystem(f.removed, post.SystemSymbol) {
			out = append(out, post)
		}
	}
	return out, nil
}

func (f *fakePostRepo) Upsert(_ context.Context, post *domainScouting.ScoutPost) error {
	f.posts = append(f.posts, post)
	return nil
}

func (f *fakePostRepo) Remove(_ context.Context, _ int, systemSymbol string) error {
	f.removed = append(f.removed, systemSymbol)
	return nil
}

// fakeFleet is the ships list the cutover adopts orphans from.
type fakeFleet struct {
	ships []*navigation.Ship
	calls int
	err   error
}

func (f *fakeFleet) FindAllByPlayer(context.Context, shared.PlayerID) ([]*navigation.Ship, error) {
	f.calls++
	return f.ships, f.err
}

// fakePressure is the limiter-pressure EWMA the emergency brake reads.
type fakePressure struct{ wait time.Duration }

func (f *fakePressure) Current(time.Time) time.Duration { return f.wait }

// fakePhase is the EXPANSION gate's driven port. Adversarial: on err it still
// reports IN EXPANSION alongside the error, so a gate that reads the bool and
// swallows the error runs the whole tick — and the zero-action assertions catch
// it instead of a convenient false masking the bug.
type fakePhase struct {
	inExpansion bool
	err         error
	calls       int
}

func (f *fakePhase) InExpansion(context.Context, shared.PlayerID) (bool, error) {
	f.calls++
	return f.inExpansion, f.err
}

// fakeRecorder captures the published gauges.
type fakeRecorder struct {
	mu        sync.Mutex
	rate      []float64
	staleness map[string]float64
	slots     map[string]int
}

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{staleness: map[string]float64{}, slots: map[string]int{}}
}

func (f *fakeRecorder) RecordRate(_ int, reqPerSec float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rate = append(f.rate, reqPerSec)
}

func (f *fakeRecorder) RecordStaleness(_ int, tier string, seconds float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staleness[tier] = seconds
}

func (f *fakeRecorder) RecordSlots(_ int, state string, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slots[state] = count
}

// --- helpers ------------------------------------------------------------------

func containsSystem(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func sortedSlots(m map[string]parkedsensing.QueuedSlot) []parkedsensing.QueuedSlot {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]parkedsensing.QueuedSlot, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

func sortedSystems(m map[string]parkedsensing.ExpandSystem) []parkedsensing.ExpandSystem {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]parkedsensing.ExpandSystem, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

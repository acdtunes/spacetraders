package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// expansion.go pushes the sensing map outward: the screen judges systems we can
// already see, and this is what puts new ones in front of it. Three passes in one
// tick — FRONTIER marks never-evaluated gate neighbours PENDING for the screening
// sweep, SEEDS asks for a probe to chart a system that cannot be screened remotely,
// and TOURS drives that probe and stands it down when the system is charted through.
//
// Acquiring a seed is NOT this engine's job: it writes a SPARE placement and the buy
// queue funds it under the same floor and probe cap as everything else, so expansion can
// never open a second, unguarded spending path. FRONTIER is free where the other two are
// not, and that is where the operator's switch cuts (see ExpandKnobs.SeedsEnabled).
//
// Nothing is held between ticks — every pass is driven off the sensing_systems row
// and the hull's row in the ships table — so a restart mid-tour resumes from the
// ledger with no state to rebuild.

// MaxExpansionActions bounds how much this engine may do in one tick: seed steps
// and seed requests together. A plain constant, deliberately not a knob — it paces
// command bursts, not economics. Ledger-only work is free and uncounted: marking a
// neighbour PENDING and claiming a parked spare cost a row write and no API call,
// and both are already bounded by the frontier and by the spare fleet.
const MaxExpansionActions = 20

// MaxSpareGhostReleases bounds the ghost SPARE rows one tick may release: a burst
// of ledger writes paced like DefaultMaxReaps, never an economic choice.
const MaxSpareGhostReleases = 20

// allSlotStates is every state a placement row can be in. The tick reads the whole
// ledger once and derives from it which waypoints are occupied, which placements
// are still wanted, and which spare hulls are available.
var allSlotStates = []string{
	SlotStateWanted, SlotStateQueued, SlotStateBought, SlotStateInTransit, SlotStateParked,
}

// GateNeighbours reads the gate graph's stored adjacency: which systems are ONE
// jump from this one. A PURE STORE read by contract — never a fetch-through
// resolver, because expansion asks this of every judged system on every tick and a
// fetch-through would spend the API budget on topology exactly where topology is
// least cached. A system missing from the store is simply not expanded through.
type GateNeighbours interface {
	Neighbours(ctx context.Context, system string) ([]string, error)
	// Mapped reports whether we hold ANY stored gate adjacency for this system —
	// whether its gate has ever been read, not whether it can be passed. A DIFFERENT
	// QUESTION FROM "Neighbours returned nothing": Neighbours filters
	// under-construction and stale edges out of its answer while the ROWS remain, so
	// a system whose every exit is under construction reports no neighbours and is
	// nonetheless fully mapped. A system with NO rows is unread territory, and the
	// only kind whose charting can add systems to the ledger. A PURE STORE read,
	// like Neighbours.
	Mapped(ctx context.Context, system string) (bool, error)

	// PassableGraph returns the WHOLE topology in one read: the same two questions
	// Neighbours and Mapped answer per system, answered for every system at once. It
	// exists because reachability is transitive — a walk over the per-system readers
	// costs one store round trip per system reached, where these rows arrive in a
	// single query. Callers needing ONE system's neighbours use Neighbours.
	PassableGraph(ctx context.Context) (GateGraph, error)
}

// GateGraph is one snapshot of the gate topology, as the walker sees it. It is a
// VALUE, deliberately: read once and thrown away at the end of the tick that read
// it. Reachability is time-varying — walls refresh on a TTL, gates finish
// construction — so nothing here may be cached across ticks or persisted, or a
// stored answer outlives the evidence that produced it (RULINGS #2).
type GateGraph struct {
	// Passable maps each system to the systems ONE PASSABLE hop away: the same
	// filtering Neighbours applies, so under-construction and condemned-stale edges
	// are already excluded. This is the graph a hull can actually walk.
	Passable map[string][]string
	// Mapped names every system whose gate adjacency we have READ, whether or not
	// any of it can be passed. The distinction separates "we know this system's
	// exits and none reach us" from "we have never looked": only the first is
	// evidence of unreachability, and treating the second as a dead end starves a
	// fleet that has not finished charting.
	Mapped map[string]bool
}

// UnchartedCatalog reads which waypoints in a system are still uncharted.
type UnchartedCatalog interface {
	// UnchartedWaypoints returns the system's uncharted waypoints IN THE ORDER THEY
	// SHOULD BE VISITED. A seed charts the first one it is given, so any
	// deterministic order is correct — the tour is EXHAUSTIVE, and ordering decides
	// only how long the fleet waits for the markets and yards a system holds.
	//
	// EVERY WAYPOINT IS RETURNED, however unrewarding one looks: the same set is what
	// WaypointCatalog.ListUnchartedCount counts, and that count is this tour's
	// completion signal. A list narrower than the count leaves the tour finished
	// while the count never reaches zero — the system never written off, and never
	// stopping being sent seeds.
	UnchartedWaypoints(ctx context.Context, system string) ([]string, error)
}

// SystemScreener re-runs the whitelist screen on ONE system and records its
// verdict. It closes over ScreenSystem's ports, the player and the goods whitelist,
// so this engine can price a system without acquiring the screen's dependencies.
type SystemScreener func(ctx context.Context, system string) (ScreenResult, error)

// ErrSeedStepHeld reports a seed step HELD rather than attempted: the hull is waiting
// out a game timer, no request was spent, and the step must not be charged an action.
var ErrSeedStepHeld = errors.New("charting seed step was held, not attempted")

// SeedCommander drives one charting seed. Every method is a single command with no
// retry and no waiting: the tick issues one and returns, and the next tick reads
// the ships table to see what happened. Implementations tag every call as
// charting-envelope spend and must NOT set a request priority — charting competes
// normally for a rate-limit token, unlike the parked scans, which are the one class
// that yields to everyone.
type SeedCommander interface {
	// JumpTo advances a hull ONE step of the gate hop to targetSystem: the in-system
	// move to the gate, or the jump off it. Two steps rather than one because a gate
	// hop is two physical moves and the first is a flight; collapsing them means
	// waiting out that flight inside the tick, which this interface forbids.
	//
	// fromWaypoint is the hull's CURRENT waypoint, passed rather than re-read because
	// it is the exact discriminator between those two steps. Deriving it from
	// distance instead would be wrong: orbitals share coordinates with the body they
	// orbit, so a hull can be zero distance from a gate it is not on.
	JumpTo(ctx context.Context, playerID int, shipSymbol, fromWaypoint, targetSystem string) error
	// NavigateTo moves a hull to a waypoint inside the system it is in.
	NavigateTo(ctx context.Context, playerID int, shipSymbol, waypoint string) error
	// Dock berths a hull where it stands, so a counter it is orbiting reads as staffed.
	Dock(ctx context.Context, playerID int, shipSymbol string) error
	// Chart publicly charts the waypoint the hull is standing on. A waypoint somebody
	// else already charted is a benign no-op, and implementations swallow it.
	Chart(ctx context.Context, playerID int, shipSymbol string) error
	// RefreshWaypoint re-reads a waypoint and PERSISTS what it found, reporting
	// whether it carries a marketplace. The persistence half is load-bearing: the
	// tour picks its next stop from the stored uncharted set, so an unwritten result
	// leaves the seed re-charting the same waypoint on every tick, forever.
	RefreshWaypoint(ctx context.Context, playerID int, system, waypoint string) (bool, error)
	// ReadMarketAt scans the market the hull is standing at and persists its prices,
	// which is what lets the screen resolve this waypoint from the local cache.
	ReadMarketAt(ctx context.Context, playerID int, waypoint string) error
	// SyncWaypoints sweeps a system's whole waypoint LIST and persists it. It is the
	// one method here that may issue MORE than one API call, since the list is
	// paginated. The engine spends a single action on it anyway — see the call site —
	// because a half-swept catalog is worse than none.
	SyncWaypoints(ctx context.Context, playerID int, system string) error
}

// ExpandSystem is one row of the sensing ledger's system table, as the expansion
// engine reads it: the verdict, how much of the system is still dark, and the
// charting errand (if any) running against it. SeedShip/SeedState ARE the mission
// and the row's own system is its target — there is no target column, which is why
// retargeting a seed is two writes.
type ExpandSystem struct {
	System         string
	Verdict        string
	UnchartedCount int
	SeedShip       string
	SeedState      string
	// CatalogKnown reports whether the system's waypoint LIST has ever been swept —
	// a separate question from UnchartedCount, because an unswept system has no
	// waypoint rows and so reports ZERO uncharted, indistinguishable from one
	// charted end to end. Reading only the count would leave it invisible forever.
	CatalogKnown bool
}

// ExpandLedger is the expansion engine's slice of the sensing ledger — the only
// part of the model that writes BOTH tables, but still narrow where it matters: it
// cannot read the treasury, price a hull, or count the probe fleet, so nothing here
// can authorise a purchase.
type ExpandLedger interface {
	// Systems returns every system row the player holds.
	Systems(ctx context.Context, playerID int) ([]ExpandSystem, error)
	// SlotsByState returns the player's placements in any of the given states.
	SlotsByState(ctx context.Context, playerID int, states ...string) ([]QueuedSlot, error)
	// UpsertSystem records a screening verdict.
	UpsertSystem(ctx context.Context, playerID int, record SystemRecord) error
	// UpsertSlotMetadata declares one placement with whatever the caller measured
	// about the waypoint. On a waypoint that already carries a placement it refreshes
	// those measurements ONLY, so a declaration can never overwrite a hull the ledger
	// is counting.
	UpsertSlotMetadata(ctx context.Context, playerID int, slot SlotRecord) error
	// UpsertSpareSlot records a hull already standing at a waypoint — a finished
	// charting seed standing down as a parked spare. On conflict it re-points the
	// row at that hull and leaves the screen's measurements alone.
	UpsertSpareSlot(ctx context.Context, playerID int, slot SlotRecord) error
	// SetSeed writes a system's charting errand and ONLY that; conversely
	// UpsertSystem cannot touch the errand. The split is enforced in the persistence
	// layer's column list, and it is what lets the screening sweep run concurrently:
	// a seed's target is PENDING for the whole tour and re-screened every tick, and a
	// screen that could clear the errand would orphan the hull it names. Empty
	// strings clear the errand.
	SetSeed(ctx context.Context, playerID int, system, shipSymbol, seedState string) error
	// StampCatalogSynced records that a system's waypoint list has been swept and
	// persisted. Another narrow write with a disjoint column set, for the same reason
	// as SetSeed: it lands mid-tour, while the sweep may be re-screening the row.
	StampCatalogSynced(ctx context.Context, playerID int, system string) error
	// DeleteSlot removes ONE placement row outright: the one of the given kind.
	// A waypoint can carry a MARKET row and a SPARE row at once, so releasing by
	// waypoint alone would take a working placement down with the intended one.
	DeleteSlot(ctx context.Context, playerID int, waypoint, kind string) error
	// TransitionSlot advances one placement, guarded on its current state.
	TransitionSlot(ctx context.Context, playerID int, t SlotTransition, set SlotFields) error
}

// ExpandPorts is everything AdvanceExpansion needs from the outside world.
type ExpandPorts struct {
	Gates       GateNeighbours
	Ledger      ExpandLedger
	Screen      SystemScreener
	SeedShip    SeedCommander
	Ships       ParkedShipReader
	MarketGoods MarketGoodsReader
	Yards       ProbeYardCatalog
	Uncharted   UnchartedCatalog
	// ListingMemo is the STORED shipyard listings — the same read the buy queue's
	// probe-listing memo makes, wired here so staging can prefer a yard we have
	// EVIDENCE sells probes over one the trait fallback merely guessed at. OPTIONAL:
	// a nil memo leaves staging choosing the nearest staffed yard.
	ListingMemo ProbeListingMemo
	// GateRead is the bounded, fetch-through jump-gate read — the pass that learns
	// where a system connects WITHOUT waiting for a hull to fly there (gateread.go).
	// A SECOND port beside Gates rather than a widening of it, because Gates is a
	// pure store read by contract and must stay one. A nil reader is a WIRING GAP,
	// not a feature switch: the pass does nothing and the rest of the tick is
	// unaffected.
	GateRead GateReader
}

// ExpandKnobs are the operator-set controls on expansion.
type ExpandKnobs struct {
	// SeedsEnabled controls whether this engine may raise a NEW charting errand: a
	// SPARE want, a spare claimed onto an errand, or a finished seed retargeted. It does
	// not switch the engine off — paused, it still records frontier systems and reads
	// charted jump gates, both free and both what keep already-bought hulls supplied
	// with somewhere to go.
	//
	// AN ERRAND ALREADY IN FLIGHT IS STILL FLOWN. A hull mid-errand holds no placement
	// row, so an engine that stopped commanding it would strand a probe we own rather
	// than park it; refusing the retarget is what makes that errand terminate.
	//
	// ONE HALF OF A TWO-STATE DERIVATION, the other being BuyKnobs.SpendEnabled off the
	// same operator knob. Neither is the money guard: ProbeBuyFloor and the probe cap
	// are, and they are unchanged in every state (RULINGS #4).
	SeedsEnabled bool
	// MinBudgetRate is the sensing rate below which expansion pauses for the tick, in
	// requests per second. It is compared against the SENSING residual, never the
	// pacer rate: the emergency brake multiplies the residual and can legitimately
	// drive it BELOW the minimum scan rate, while the pacer re-imposes that floor.
	// Expansion is the first thing meant to yield under API pressure, so it is gated
	// on the sub-floor value that can actually express the pressure.
	MinBudgetRate float64
	// Whitelist is the goods set a seed-revealed market is judged against — the same
	// one the screen uses, so a market the seed slots and a market the screen slots
	// mean the same thing.
	Whitelist map[string]bool
}

// ExpandReport is one expansion tick's outcome, for the heartbeat.
type ExpandReport struct {
	// Skipped names the gate that held the whole tick, and is empty when it ran.
	Skipped string
	// SeedingPaused reports that the tick ran its FREE passes, flew the errands already in
	// flight, and stopped before the ones that raise a new one (SeedsEnabled off). A SEPARATE
	// FIELD RATHER THAN A Skipped VALUE: Skipped means "this tick did nothing" and the stall
	// verdict reads it that way, so a pause reported there would file a tick full of
	// discoveries as idle. A paused tick is graded on Discovered instead.
	SeedingPaused bool
	// Discovered counts never-evaluated neighbour systems recorded as PENDING.
	Discovered int
	// SeedsRequested counts SPARE placements enqueued for the buy queue to fund.
	SeedsRequested int
	// SeedsUnstaged counts the targets requestSeeds passed over because no probe
	// counter within reach is staffed by a hull of ours — the cold-start deadlock,
	// made COUNTABLE rather than left as a silent `continue`. It is the trigger for
	// staffCounters as well as the operator's view of it, so a fleet in the deadlock
	// reads as blocked rather than as idle.
	SeedsUnstaged int
	// CountersStaffed counts non-probe hulls lent to a probe counter this tick so the
	// buy queue has somebody to buy through (counterstaff.go). Nonzero beside a
	// nonzero SeedsUnstaged is the escape working; nonzero for many ticks running is
	// the borrowed hulls being taken back faster than a probe can be bought.
	CountersStaffed int
	// SeedsClaimed counts parked spares turned into charting errands.
	SeedsClaimed int
	// SpareGhostsReleased counts SPARE rows deleted because the hull they named was
	// already on an errand. A standing non-zero value is that invariant re-breaking.
	SpareGhostsReleased int
	// Jumped, Navigated and Charted count the seed steps actually commanded. Jumped
	// counts GATE-CROSSING STEPS, not gates crossed: a hop is the move to the gate
	// and then the jump off it, one per tick, so one crossing normally counts two.
	Jumped, Navigated, Charted int
	// MarketsFound counts seed-revealed markets recorded as placements.
	MarketsFound int
	// Retargeted counts finished seeds sent on to another frontier system;
	// Parked counts those stood down, into a placement or as a spare.
	Retargeted, Parked int
	// Actions counts everything charged against MaxExpansionActions.
	Actions int
	// GatesRead counts jump gates this tick READ LIVE and persisted; GatesUnread is the size of the
	// whole outstanding backlog BEFORE the per-tick cap truncated it, so the heartbeat shows how much
	// topology is still unknown rather than only how much this tick absorbed.
	//
	// GatesUnreadable and GatesFailed are kept APART, which is why the pass matches a sentinel rather
	// than any-error: an uncharted gate is the API answering honestly and on a young frontier is the
	// common case, while a store fault or a 5xx is not, and folding the two together would hide a
	// broken dependency inside a number that is SUPPOSED to be large.
	//
	// Gate reads are NOT charged against MaxExpansionActions: that budget paces the seed machinery's
	// hull commands, while this pass commands no hull, spends no credits, and carries its own bound
	// (MaxGateReads). Sharing one budget would let routine seed steps crowd out the one pass that can
	// tell the fleet it is not actually sealed inside a pocket of under-construction exits.
	GatesRead, GatesUnread, GatesUnreadable, GatesFailed int
}

// AdvanceExpansion runs one expansion tick. budgetRate is the sensing residual in
// requests per second (see ExpandKnobs.MinBudgetRate); a budget-starved tick
// returns having touched NOTHING, not one port call, because an engine that still
// reads its ledger to decide it should not run is not really yielding. THAT GATE IS
// THE OUTERMOST ONE: a spend pause is an operator's economic choice, the budget
// floor is the fleet protecting its own API budget, and the second must not be
// reachable past the first.
//
// THE TICK IS IN THREE PARTS, split at NEW purchase intent (see ExpandKnobs.SeedsEnabled):
//
//   - FREE, and always run: frontier marking and the gate read. Both work from facts
//     we hold or can read without a hull, both write nothing but topology, and
//     together they keep the placement machine supplied with somewhere to put the
//     probes we have already bought.
//   - FINISHING, and always run: advanceSeeds, which drives errands already in flight to their
//     end. It raises no new intent, and one the engine stops commanding is a hull we paid for
//     and stranded.
//   - NEW INTENT, run only when SeedsEnabled: the rest of the seed machinery. It spends no credit
//     DIRECTLY but asks engines that can — requestSeeds writes a SPARE want the buy queue funds,
//     and claimSpares deletes a placement row, a deliberate UNDER-count of the probe fleet and
//     therefore cap headroom that authorises a purchase (advanceSeeds sustains that under-count
//     for as long as an errand runs). Under RULINGS #4 the doubt resolves toward NOT spending.
//
// A PAUSED TICK IS NOT A SKIPPED TICK: Skipped stays empty and SeedingPaused
// carries the reason.
func AdvanceExpansion(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	k ExpandKnobs,
	budgetRate float64,
) (ExpandReport, error) {
	var rep ExpandReport
	if budgetRate < k.MinBudgetRate {
		return ExpandReport{Skipped: "budget"}, nil
	}
	// Refuse rather than expand against nothing: a seed screened against an empty
	// whitelist charts a system, finds markets, records none of them, and leaves the
	// reconcile to write the system off on evidence the seed discarded. Same
	// sentinel as the screen's own refusal.
	if len(k.Whitelist) == 0 {
		return ExpandReport{}, fmt.Errorf("expanding the sensing map: %w", ErrEmptyWhitelist)
	}

	systems, err := p.Ledger.Systems(ctx, playerID)
	if err != nil {
		return rep, fmt.Errorf("failed to list screened sensing systems: %w", err)
	}
	slotRows, err := p.Ledger.SlotsByState(ctx, playerID, allSlotStates...)
	if err != nil {
		return rep, fmt.Errorf("failed to list sensing placements: %w", err)
	}
	book := newSlotBook(slotRows, hullsOnErrand(systems))
	known := knownSystems(systems)

	// The neighbour map is read before anything is written, so a gate store that
	// cannot answer stops the tick before a hull has been commanded anywhere.
	neighbours, err := readNeighbours(ctx, p, systems, book)
	if err != nil {
		return rep, err
	}
	if err := markFrontier(ctx, p, playerID, neighbours, known, &rep); err != nil {
		return rep, err
	}

	// How far every system is from every other, out to the reach a seed can fly. One
	// memo for the whole tick, shared by the gate read, supply, the spare claim,
	// staging and the retarget, so none of them can disagree about what is reachable.
	reach := newGateReach(p.Gates, neighbours, SeedFlightUnbounded)

	// Whether the store holds ANY gate adjacency for a system, read at most once per system per tick
	// and shared by the gate-read pass below and orderUnmappedFirst, so the two cannot drift within
	// one tick. See gateMapping.
	mapping := newGateMapping(p.Gates)

	// THE GATE READ, placed here with markFrontier because both are TOPOLOGY: learn the map, then fly
	// it. A charted gate is readable with no hull present, so a system can be asked where it connects
	// without waiting for a probe to arrive. IT TAKES `known`, WHICH markFrontier HAS JUST GROWN, so a
	// neighbour named for the first time this tick is a candidate on this tick rather than the next.
	if err := readUnmappedGates(ctx, p, playerID, known, mapping, reach, book, &rep); err != nil {
		return rep, err
	}

	// The systems needing a hull are resolved ONCE, before anything moves, and every
	// branch that covers one strikes it off — which is what keeps a single system
	// from being sent both a spare and a fresh probe.
	targets := seedlessTargets(systems)
	covered := make(map[string]bool, len(targets))

	// A system's probe-selling yards, resolved at most once per TICK and shared by the
	// finishing seed and by seed staging, so the two never read the same catalog twice
	// or disagree about which waypoints are yards.
	probeYards := map[string][]string{}

	// A gate walk per target, and with the seed gate shut every pass that reads it is
	// unreachable — the retarget included, which refuses on its own.
	if k.SeedsEnabled {
		targets, err = orderTargets(ctx, reach, mapping, targets, book)
		if err != nil {
			return rep, err
		}
	}

	t := &expandTick{
		p: p, playerID: playerID, k: k,
		book: book, reach: reach,
		probeYards: probeYards,
		staffed:    map[string]bool{},
		listings:   map[string]probeStock{},
		serving:    map[string]bool{},
		targets:    targets, covered: covered,
		rep: &rep,
	}

	// Seeds move BEFORE spares are claimed, so an errand stamped this tick is not
	// also flown by it: the ship row has not caught up, and the next tick reads it.
	// It runs whether or not the gate below is open (see ExpandKnobs.SeedsEnabled).
	if err := t.advanceSeeds(ctx, systems); err != nil {
		return rep, err
	}

	// THE PAUSE: everything above it is free or already paid for. It sits here rather than
	// at the top because markFrontier is a ledger write off adjacency we hold and the gate
	// read is what keeps that adjacency growing — cutting either to stop a purchase strands
	// already-bought probes with nowhere to go.
	//
	// EVERYTHING BELOW THIS LINE RAISES A NEW PURCHASE INTENT. That is the invariant to
	// preserve when adding a pass: if it can write a SPARE want, delete a placement row,
	// or emit demand, it goes below. If it only reads stored facts, records topology, or
	// advances an errand already running, it may go above.
	if !k.SeedsEnabled {
		rep.SeedingPaused = true
		return rep, nil
	}

	// BEFORE the claim, so the yards it frees are stageable on THIS tick rather than
	// the next, and the freed rows cannot be mistaken for supply by requestSeeds.
	if err := t.releaseErrandSpares(ctx); err != nil {
		return rep, err
	}
	if err := t.claimSpares(ctx); err != nil {
		return rep, err
	}
	if err := t.requestSeeds(ctx); err != nil {
		return rep, err
	}

	// LAST OF THE SEED PASSES, and only reachable when every one before it came up
	// empty: it reads rep.SeedsUnstaged, which requestSeeds has just filled in. A
	// fleet that could stage a seed the ordinary way never lends a hull (see
	// counterstaff.go).
	if err := t.staffCounters(ctx); err != nil {
		return rep, err
	}

	return rep, nil
}

// orderTargets applies the compound key the seed passes fly by: gate mapping,
// then distance, then the depth ordering seedlessTargets established. The stable
// mapping pass runs SECOND, which is what makes it the outer key.
func orderTargets(
	ctx context.Context,
	reach *gateReach,
	mapping *gateMapping,
	targets []ExpandSystem,
	book *slotBook,
) ([]ExpandSystem, error) {
	byReach, err := reach.orderByDistance(ctx, targets, book)
	if err != nil {
		return nil, err
	}
	return mapping.orderUnmappedFirst(ctx, byReach)
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// expandTick is one AdvanceExpansion pass below the spend pause. Its memos are
// shared so two passes never read the same catalog twice or disagree about it.
type expandTick struct {
	p        ExpandPorts
	playerID int
	k        ExpandKnobs
	book     *slotBook
	reach    *gateReach
	// None of the four can change while the tick runs. serving memoises whether
	// staging would ever choose a yard in a system at all — see originServesATarget —
	// and origins is that test's own index, built lazily because most ticks never
	// reach the pass that asks.
	probeYards map[string][]string
	staffed    map[string]bool
	listings   map[string]probeStock
	serving    map[string]bool
	origins    map[string]bool
	// covered is struck off by every branch that answers a target, which keeps one
	// system from being sent both a spare and a fresh probe.
	targets []ExpandSystem
	covered map[string]bool
	rep     *ExpandReport
}

package parkedsensing

import (
	"context"
	"fmt"
	"sort"
)

// expansion.go pushes the sensing map outward. The screen judges systems we can
// already see; this is what puts new ones in front of it.
//
// Three jobs, in one tick:
//
//   - FRONTIER. Every system whose gate adjacency we have actually MEASURED
//     names its gate neighbours, and the ones we have never evaluated get a
//     PENDING row so the reconcile's screening sweep picks them up. The verdict
//     does not gate this: a charted jump gate is evidence of where a system
//     connects, and waiting for a screening verdict before believing it held the
//     frontier to one fully-charted ring at a time (see readNeighbours).
//
//   - SEEDS. A system with uncharted waypoints cannot be screened remotely: its
//     markets are invisible until someone flies there and charts them. So one
//     probe goes out on an errand. Acquiring that probe is NOT this engine's job
//     — it asks for a SPARE placement and the buy queue funds it under the same
//     floor and probe cap as everything else, so expansion can never open a
//     second, unguarded spending path.
//
//   - TOURS. A seed charts its target's waypoints one per tick and, when the
//     system is charted through, is stood down: into a placement there, onward
//     to the next frontier system, or parked as a spare.
//
// Everything is driven off two durable facts — the sensing_systems row and the
// hull's row in the ships table — and nothing is held between ticks. A restart
// mid-tour resumes from the ledger with no state to rebuild.
//
// FRONTIER IS FREE AND THE OTHER TWO ARE NOT, which is why the operator's switch
// cuts between them rather than around all three. Marking a neighbour PENDING
// costs a row write; SEEDS and TOURS fly hulls, and asking for the hull to fly is
// asking the buy queue to buy one. See ExpandKnobs.SpendEnabled.

// MaxExpansionActions bounds how much this engine may do in one tick: seed steps
// and seed requests together. A plain constant, deliberately not a knob — it
// paces command bursts, not economics, and mirrors the buy queue's
// maxDrainAttempts and the placement machine's DefaultMaxPlacementActions.
//
// Ledger-only work is free and uncounted: marking a neighbour PENDING and
// claiming a parked spare onto a mission cost a row write and no API call, and
// both are already bounded by the size of the frontier and of the spare fleet.
// Counting them would let cheap bookkeeping crowd out the flying.
const MaxExpansionActions = 6

// allSlotStates is every state a placement row can be in. The tick reads the
// whole ledger once and derives from it: which waypoints are occupied (so a
// write can never clobber a placement), which placements are still wanted, and
// which spare hulls are available. One query beats four.
var allSlotStates = []string{
	SlotStateWanted, SlotStateQueued, SlotStateBought, SlotStateInTransit, SlotStateParked,
}

// GateNeighbours reads the gate graph's stored adjacency: which systems are ONE
// jump from this one.
//
// A PURE STORE read by contract — never a fetch-through resolver. Expansion asks
// this of every judged system on every tick, so a fetch-through implementation
// would spend the API budget on topology exactly where topology is least cached,
// and would do it hardest at the frontier. A system missing from the store is
// simply not expanded through.
type GateNeighbours interface {
	Neighbours(ctx context.Context, system string) ([]string, error)
	// Mapped reports whether we hold ANY stored gate adjacency for this system —
	// whether its gate has ever been read, not whether it can be passed.
	//
	// IT IS A DIFFERENT QUESTION FROM "Neighbours returned nothing", and the
	// difference is the whole reason it exists. Neighbours filters
	// under-construction and stale edges out of its answer while the ROWS remain,
	// so a system whose every exit is under construction reports no neighbours and
	// is nonetheless fully mapped: we know exactly where it connects, we simply
	// cannot pass. Charting such a system reveals no adjacency we do not already
	// hold. A system with NO rows is the opposite — genuinely unread territory,
	// and the only kind whose charting can add systems to the ledger.
	//
	// A PURE STORE read, like Neighbours, and for the same reason: the ordering
	// asks this of every candidate on every tick.
	Mapped(ctx context.Context, system string) (bool, error)

	// PassableGraph returns the WHOLE topology in one read: the same two questions
	// Neighbours and Mapped answer per system, answered for every system at once.
	//
	// IT EXISTS BECAUSE REACHABILITY IS TRANSITIVE. Asking "can a hull
	// walk to this system?" needs a walk, and a walk over the per-system readers
	// costs one store round trip per system reached — measured at ~1,070 reads and
	// ~750ms against the live graph, on a 30s tick. The same rows arrive in a
	// single query in ~3ms, because the whole table is only ~10k rows. Callers that
	// need ONE system's neighbours should still use Neighbours; this is for the
	// callers that need the shape of the graph.
	PassableGraph(ctx context.Context) (GateGraph, error)
}

// GateGraph is one snapshot of the gate topology, as the walker sees it.
//
// It is a VALUE, deliberately: a snapshot read once and thrown away at the end of
// the tick that read it. Reachability is time-varying — walls refresh on a 24h TTL
// at ~121 systems/hour and gates finish construction — so nothing here may be
// cached across ticks or persisted. A stored answer outlives the evidence that
// produced it (RULINGS #2).
type GateGraph struct {
	// Passable maps each system to the systems ONE PASSABLE hop away: the same
	// filtering Neighbours applies, so under-construction and condemned-stale edges
	// are already excluded. This is the graph a hull can actually walk.
	Passable map[string][]string
	// Mapped names every system whose gate adjacency we have READ, whether or not
	// any of it can be passed — exactly the question Mapped answers per system.
	//
	// The distinction is what separates "we know this system's exits and none of
	// them reach us" from "we have never looked". Only the first is evidence of
	// unreachability; the second is unread territory, and treating it as a dead end
	// is how a feasibility filter starves a fleet that has not finished charting.
	Mapped map[string]bool
}

// UnchartedCatalog reads which waypoints in a system are still uncharted.
type UnchartedCatalog interface {
	// UnchartedWaypoints returns the system's uncharted waypoints IN THE ORDER
	// THEY SHOULD BE VISITED. A seed charts the first one it is given, so an
	// implementation is free to order them by proximity or by expected value;
	// any deterministic order is correct, because the tour is EXHAUSTIVE —
	// ordering decides WHEN each waypoint is reached, never WHETHER it is.
	//
	// EVERY WAYPOINT IS RETURNED. An implementation must not omit one on the
	// grounds that it looks unrewarding: the same set is what
	// WaypointCatalog.ListUnchartedCount counts, and that count is this tour's
	// completion signal. A list narrower than the count would leave the tour
	// finished while the count never reached zero — the system never written
	// off, and never stopping being sent seeds.
	//
	// The ordering freedom is not cosmetic, though. It decides how long the
	// fleet waits for the shipyards and markets a system holds: a seed working
	// an arbitrary order can spend fifty hours on barren waypoints before
	// revealing the one market a parked scanner could have been sitting on the
	// whole time.
	UnchartedWaypoints(ctx context.Context, system string) ([]string, error)
}

// SystemScreener re-runs the whitelist screen on ONE system and records its
// verdict. It is a closure over ScreenSystem's ports, the player and the goods
// whitelist — the values that are fixed for the life of the coordinator — so
// this engine can ask "what is this system worth?" without acquiring the
// screen's own dependencies.
type SystemScreener func(ctx context.Context, system string) (ScreenResult, error)

// SeedCommander drives one charting seed. Every method is a single command with
// no retry and no waiting: the tick issues one and returns, and the next tick
// reads the ships table to see what happened.
//
// Implementations tag every call as charting-envelope spend. They must NOT set a
// request priority: charting is bounded, deadline-bearing work that competes
// normally for a rate-limit token, unlike the parked scans, which are the one
// class that yields to everyone.
type SeedCommander interface {
	// JumpTo advances a hull ONE step of the gate hop to targetSystem: the
	// in-system move to the gate, or the jump off it.
	//
	// fromWaypoint is the hull's CURRENT waypoint, and it is passed rather than
	// re-read because it is the exact discriminator between those two steps —
	// "is this hull standing on the gate it leaves from?". The tick has already
	// read it from the ships table, and an implementation deriving it from
	// distance instead would be wrong: orbitals share coordinates with the body
	// they orbit, so a hull can be zero distance from a gate it is not on.
	//
	// Two steps rather than one because a gate hop is two physical moves and the
	// first is a flight. Collapsing them means waiting out that flight inside
	// the tick, which is the one thing this interface forbids.
	JumpTo(ctx context.Context, playerID int, shipSymbol, fromWaypoint, targetSystem string) error
	// NavigateTo moves a hull to a waypoint inside the system it is in.
	NavigateTo(ctx context.Context, playerID int, shipSymbol, waypoint string) error
	// Chart publicly charts the waypoint the hull is currently standing on. A
	// waypoint somebody else already charted is a benign no-op, not an error,
	// and implementations swallow it.
	Chart(ctx context.Context, playerID int, shipSymbol string) error
	// RefreshWaypoint re-reads a waypoint and PERSISTS what it found, reporting
	// whether it carries a marketplace.
	//
	// The persistence half is load-bearing, not incidental: the tour picks its
	// next stop from the stored uncharted set, so a chart whose result is never
	// written back leaves the waypoint uncharted in the database and the seed
	// charting it again on every tick, forever.
	RefreshWaypoint(ctx context.Context, playerID int, system, waypoint string) (bool, error)
	// ReadMarketAt scans the market the hull is standing at and persists its
	// prices, which is what lets the screen resolve this waypoint from the local
	// cache instead of paying the API to rediscover it.
	ReadMarketAt(ctx context.Context, playerID int, waypoint string) error
	// SyncWaypoints sweeps a system's whole waypoint LIST and persists it.
	//
	// It is the one method here that may issue MORE than one API call: the list
	// is paginated and the implementation walks its pages. The engine spends a
	// single action on it anyway — see the call site — because a half-swept
	// catalog is worse than none, and the sweep happens once per system.
	SyncWaypoints(ctx context.Context, playerID int, system string) error
}

// ExpandSystem is one row of the sensing ledger's system table, as the expansion
// engine reads it: the verdict, how much of the system is still dark, and the
// charting errand (if any) running against it.
//
// SeedShip/SeedState ARE the mission, and the row's own system is its target —
// there is no target column. That is why retargeting a seed is two writes.
type ExpandSystem struct {
	System         string
	Verdict        string
	UnchartedCount int
	SeedShip       string
	SeedState      string
	// CatalogKnown reports whether the system's waypoint LIST has ever been
	// swept. It is a separate question from UnchartedCount, and the difference
	// is the whole reason the field exists: an unswept system has no waypoint
	// rows at all, so it reports ZERO uncharted waypoints — indistinguishable
	// from one charted end to end. A system we have never looked at needs a seed
	// exactly as much as one full of uncharted waypoints does, and reading only
	// the count would leave it invisible forever.
	CatalogKnown bool
}

// ExpandLedger is the expansion engine's slice of the sensing ledger. It is
// wider than the other halves' because expansion is the only part of the model
// that writes BOTH tables — but it is still narrow in the direction that
// matters: it cannot read the treasury, price a hull, or count the probe fleet,
// so nothing here can authorise a purchase.
type ExpandLedger interface {
	// Systems returns every system row the player holds.
	Systems(ctx context.Context, playerID int) ([]ExpandSystem, error)
	// SlotsByState returns the player's placements in any of the given states.
	SlotsByState(ctx context.Context, playerID int, states ...string) ([]QueuedSlot, error)
	// UpsertSystem records a screening verdict.
	UpsertSystem(ctx context.Context, playerID int, record SystemRecord) error
	// UpsertSlotMetadata declares one placement, with whatever the caller
	// measured about the waypoint. On a waypoint that already carries a
	// placement it refreshes those measurements ONLY, so a declaration can
	// never overwrite a hull the ledger is counting.
	UpsertSlotMetadata(ctx context.Context, playerID int, slot SlotRecord) error
	// UpsertSpareSlot records a hull already standing at a waypoint — a finished
	// charting seed standing down as a parked spare. On conflict it re-points the
	// row at that hull and leaves the screen's measurements alone.
	UpsertSpareSlot(ctx context.Context, playerID int, slot SlotRecord) error
	// SetSeed writes a system's charting errand, and ONLY that — the verdict,
	// the uncharted count and the depth are left exactly as the screen wrote
	// them, and conversely UpsertSystem cannot touch the errand. That split is
	// enforced in the persistence layer's column list, not merely intended
	// here, and it is what lets the screening sweep run concurrently with this
	// engine: a seed's target is PENDING for the whole tour, so it is re-screened
	// on every tick, and a screen that could clear the errand would orphan the
	// hull it names. Empty strings clear the errand.
	SetSeed(ctx context.Context, playerID int, system, shipSymbol, seedState string) error
	// StampCatalogSynced records that a system's waypoint list has been swept
	// and persisted. Another narrow write with a disjoint set, for the same
	// reason as SetSeed: it lands mid-tour, while the screening sweep may be
	// re-screening the very same row.
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
	// EVIDENCE sells probes over one the trait fallback merely guessed at.
	//
	// OPTIONAL: a nil memo leaves staging choosing the nearest staffed yard.
	ListingMemo ProbeListingMemo
	// GateRead is the DELIBERATE, bounded, fetch-through jump-gate read — the pass that learns
	// where a system connects WITHOUT waiting for a hull to fly there. See gateread.go.
	//
	// It is a SECOND port beside Gates rather than a widening of it, because Gates is a pure store
	// read by contract and must stay one. A nil reader is a WIRING GAP, not a feature switch: the
	// pass does nothing and the rest of the tick is unaffected, in the same spirit as OffGatePorts.
	// The daemon wires it.
	GateRead GateReader
	// OffGate is the warp-expansion slice: the ports that raise explorer demand and warp an
	// explorer past a sealed gate frontier. See offgate.go.
	OffGate OffGatePorts
}

// ExpandKnobs are the operator-set controls on expansion.
type ExpandKnobs struct {
	// SpendEnabled controls whether this engine may ASK FOR MONEY. It does not
	// switch the engine off, and the difference is the whole point of the field.
	//
	// THE SAME OPERATOR SWITCH ALSO REACHES THE BUY QUEUE, and it has to: this
	// field only ever stopped the REQUESTS, and the queue that pays them was the
	// larger spender. With it off here alone the tick correctly reported "spending
	// paused: no seed purchase, no explorer demand" while the drain bought six
	// probes a cycle for hours (sp-com1h). See BuyKnobs.SpendEnabled, which is fed
	// from the same resolved value on the same tick.
	//
	// WHAT A WHOLE-ENGINE SWITCH COSTS INSTEAD. Returning the tick before the first
	// port call also stops markFrontier, which is a ledger write off stored
	// adjacency and costs neither a credit nor an API call, so an operator who
	// wanted to stop BUYING PROBES stops discovery too. Measured live: with the
	// switch off, new systems priced per hour fell to 1, 1, 5 across three hours
	// against 20 in the first hour back on, while 308 idle probes we already owned
	// stood unused. They were not blocked from moving — dispatchIdleOrphans and the
	// placement machine never read this knob at all — they had simply run out of
	// DESTINATIONS, because the one pass that names new ones had been switched off
	// alongside the one that spends.
	//
	// SO THE LINE IS DRAWN AT PURCHASE INTENT, not at activity. Paused, the engine
	// still records frontier systems and reads charted jump gates: both are free,
	// and both are what keep already-bought hulls supplied with somewhere to go. It
	// commands no hull, writes no SPARE want and emits no off-gate demand — the
	// three ways this engine can reach the treasury, all of them through another
	// engine (see AdvanceExpansion's own gate).
	//
	// It is NOT the money guard. ProbeBuyFloor and the probe cap are, they are
	// applied by the buy queue and the autosizer, and they are unchanged either way
	// (RULINGS #4). This only ever narrows what may be requested of them.
	SpendEnabled bool
	// MinBudgetRate is the sensing rate below which expansion pauses for the
	// tick, in requests per second.
	//
	// It is compared against the SENSING residual, never the pacer rate. The two
	// diverge exactly when it matters: the emergency brake multiplies the
	// residual and can legitimately drive it BELOW the minimum scan rate, while
	// the pacer re-imposes that floor so parked-market data never goes fully
	// dark. Expansion is the first thing meant to yield under API pressure, so
	// it is gated on the sub-floor value that can actually express the pressure.
	// Gating on the pacer rate would make the brake invisible here and leave
	// expansion charting away at full tilt through a rate-limit storm.
	MinBudgetRate float64
	// Whitelist is the goods set a seed-revealed market is judged against — the
	// same one the screen uses, so a market the seed slots and a market the
	// screen slots mean the same thing.
	Whitelist map[string]bool
}

// ExpandReport is one expansion tick's outcome, for the heartbeat.
type ExpandReport struct {
	// Skipped names the gate that held the whole tick, and is empty when it ran.
	Skipped string
	// SpendingPaused reports that the tick ran its FREE passes and stopped before
	// the ones that ask another engine for money (SpendEnabled off).
	//
	// A SEPARATE FIELD RATHER THAN A Skipped VALUE, and that is not cosmetic. Skipped
	// means "this tick did nothing", and the stall verdict reads it that way — so
	// reporting a pause there would file a tick that discovered twenty systems as
	// idle, which is precisely the silent-stall shape the verdict exists to catch. A
	// paused tick reports its real work in Discovered and GatesRead and is graded on
	// it; this flag says only why it stopped where it did.
	SpendingPaused bool
	// Discovered counts never-evaluated neighbour systems recorded as PENDING.
	Discovered int
	// SeedsRequested counts SPARE placements enqueued for the buy queue to fund.
	SeedsRequested int
	// SeedsClaimed counts parked spares turned into charting errands.
	SeedsClaimed int
	// Jumped, Navigated and Charted count the seed steps actually commanded.
	//
	// Jumped counts GATE-CROSSING steps, not gates crossed: a hop is the move to
	// the gate and then the jump off it, one per tick, so one crossing normally
	// counts two. Stated rather than corrected because the counter's job is to
	// show the engine doing work, and both steps are work.
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
	// topology is still unknown rather than only how much this tick happened to absorb.
	//
	// GatesUnreadable and GatesFailed are kept APART, and the split is the whole point of matching a
	// sentinel rather than any-error. An uncharted gate is the API answering honestly — this one
	// genuinely needs a hull — and on a young frontier it is the common case; a store fault, an
	// expired token or a 5xx is not, and folding the two into one counter would hide a broken
	// dependency inside a number that is SUPPOSED to be large.
	//
	// Gate reads are NOT charged against MaxExpansionActions. That budget paces the seed machinery's
	// hull commands; this pass commands no hull, spends no credits, and carries its own bound
	// (MaxGateReads). Sharing one budget would let routine seed steps crowd out the one pass that can
	// tell the fleet it is not actually sealed in — which is exactly how the fleet came to sit inside
	// a 57-system pocket believing every exit was under construction.
	GatesRead, GatesUnread, GatesUnreadable, GatesFailed int
	// OffGateDemanded reports that the gate-reachable frontier was exhausted this tick and
	// explorer demand was raised; OffGateTarget names the system selected to warp to (empty when
	// none was reachable), and OffGateWarped counts warps actually dispatched.
	//
	// A warp is NOT charged against MaxExpansionActions: that budget paces API command bursts, and
	// a dispatch is one command handed to a background goroutine at most once per tick, gated on
	// owning an idle explorer at all. Counting it would let the rarest and most valuable action in
	// the engine be crowded out by routine seed steps.
	OffGateDemanded bool
	OffGateTarget   string
	OffGateWarped   int
}

// AdvanceExpansion runs one expansion tick.
//
// budgetRate is the sensing residual in requests per second (see
// ExpandKnobs.MinBudgetRate). A budget-starved tick returns immediately having
// touched NOTHING — not one port call — because this is the engine that yields
// first when the API is under pressure, and an engine that still reads its
// ledger to decide it should not run is not really yielding. THAT GATE IS THE
// OUTERMOST ONE and stays so: a spend pause is an operator's economic choice,
// while the budget floor is the fleet protecting its own API budget, and the
// second must not be reachable past the first.
//
// THE TICK IS IN TWO HALVES, split at purchase intent (see ExpandKnobs.SpendEnabled):
//
//   - FREE, and always run. Frontier marking and the deliberate gate read. Both
//     work from facts we already hold or can read without a hull, both write
//     nothing but topology, and together they are what keeps the placement
//     machine supplied with somewhere to put the probes we have already bought.
//   - SPEND-INTENT, run only when SpendEnabled. The seed machinery and the
//     off-gate fallback. None of them spends a credit DIRECTLY — this engine
//     cannot price a hull or read the treasury — but each asks an engine that
//     can: requestSeeds writes a SPARE want the buy queue funds by buying a
//     probe, advanceOffGate raises the explorer demand the autosizer funds by
//     buying a 769k explorer, and claimSpares deletes a placement row, which is
//     a deliberate UNDER-count of the probe fleet and therefore cap headroom
//     that authorises a purchase. advanceSeeds sustains that under-count for as
//     long as an errand runs. Under RULINGS #4 the doubt resolves toward NOT
//     spending, so all four sit on the far side of the pause.
//
// A PAUSED TICK IS NOT A SKIPPED TICK. It does real work, reports it in
// Discovered and GatesRead, and is graded on it — Skipped stays empty and
// SpendingPaused carries the reason.
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
	// Refuse rather than expand against nothing. A seed screened against an
	// empty whitelist charts a system, finds markets, records none of them, and
	// leaves the reconcile to write the system off on evidence the seed
	// deliberately discarded. Same reasoning, and the same sentinel, as the
	// screen's own refusal.
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

	// How far every system is from every other, out to the reach a seed can
	// actually fly. One memo for the whole tick, shared by all five consumers —
	// the gate read, supply, the spare claim, staging and the retarget — so none
	// of them can disagree about what is reachable. See gateReach.
	reach := newGateReach(p.Gates, neighbours, SeedFlightUnbounded)

	// Whether the store holds ANY gate adjacency for a system, read at most once per system per tick
	// and shared by its two consumers: the gate-read pass below, which uses it to find the systems
	// whose adjacency we lack, and orderUnmappedFirst, which uses it to rank unknown territory first.
	// One read, one answer, no drift within a tick. See gateMapping.
	mapping := newGateMapping(p.Gates)

	// THE DELIBERATE GATE READ, placed here with markFrontier because both are TOPOLOGY: learn the
	// map, then fly it. A charted gate is readable with no hull present, so waiting for a probe to
	// physically arrive before asking a system where it connects was never necessary — and it is what
	// left the fleet sealed inside a 57-system pocket with a built, passable exit nobody had read.
	//
	// IT TAKES `known`, WHICH markFrontier HAS JUST GROWN, so a neighbour named for the first time this
	// tick is a candidate on this tick rather than the next. See gateread.go.
	if err := readUnmappedGates(ctx, p, playerID, known, mapping, reach, book, &rep); err != nil {
		return rep, err
	}

	// THE PAUSE, and everything above it is free while everything below asks for
	// money. Placed HERE rather than at the top of the function because the two
	// halves were welded together and the weld is the defect: markFrontier is a
	// ledger write off adjacency we have already measured, and switching it off to
	// stop a purchase starved 308 already-bought probes of anywhere to go.
	//
	// AFTER THE GATE READ, DELIBERATELY. That pass spends API budget and no
	// credits, and it is what keeps the adjacency store growing — without it
	// markFrontier can only ever re-walk the neighbours we already hold, so the
	// discovery half would drain and stall.
	//
	// EVERYTHING BELOW THIS LINE EITHER COMMANDS A HULL OR RAISES A PURCHASE
	// INTENT. That is the invariant to preserve when adding a pass: if it can move
	// a ship, write a SPARE want, delete a placement row, or emit demand, it goes
	// below. If it only reads stored facts and records topology, it may go above.
	if !k.SpendEnabled {
		// RETRACTED, NOT MERELY UNRAISED. The explorer-demand bridge latches, so a
		// pause that just stopped calling advanceOffGate would leave a `Demanded:
		// true` from the tick before the switch standing forever, and the autosizer
		// would buy against it (see retractOffGateDemand).
		retractOffGateDemand(p, playerID)
		rep.SpendingPaused = true
		return rep, nil
	}

	// The systems needing a hull are resolved ONCE, before anything moves, and
	// every branch that covers one strikes it off. That is what keeps a seed
	// retargeted onto a system from also being sent a spare, and a spare claimed
	// for it from also being ordered a fresh probe.
	targets := seedlessTargets(systems)
	covered := make(map[string]bool, len(targets))

	// A system's probe-selling yards, resolved at most once per TICK and shared
	// by both consumers: the finishing seed picking which placement to fill, and
	// seed staging picking where a neighbour's probe can be bought. The two ask
	// about overlapping sets of systems, and sharing the memo is what keeps them
	// from reading the same catalog twice — and from ever disagreeing about which
	// waypoints are yards.
	probeYards := map[string][]string{}

	targets, err = orderTargets(ctx, reach, mapping, targets, book)
	if err != nil {
		return rep, err
	}

	t := &expandTick{
		p: p, playerID: playerID, k: k,
		book: book, reach: reach,
		probeYards: probeYards,
		staffed:    map[string]bool{},
		listings:   map[string]probeStock{},
		targets:    targets, covered: covered,
		rep: &rep,
	}

	// Seeds move BEFORE spares are claimed, so an errand stamped this tick is
	// not also flown by it: the ship row has not caught up yet, and the next
	// tick reads it. Same discipline as the placement machine's single pass.
	if err := t.advanceSeeds(ctx, systems); err != nil {
		return rep, err
	}
	if err := t.claimSpares(ctx); err != nil {
		return rep, err
	}
	if err := t.requestSeeds(ctx); err != nil {
		return rep, err
	}

	// OFF-GATE, LAST, and only once the gate passes have had their turn. Warp is the expensive
	// fallback: a 769k hull and a multi-system flight against a probe that walks a gate for free, so
	// a single gate-reachable target suppresses it entirely. A read failure here fails the tick
	// rather than being read as "the frontier is exhausted" — that reading would raise explorer
	// demand off a database hiccup.
	gateReachable, err := reach.reachesAny(ctx, targets, book)
	if err != nil {
		return rep, err
	}
	advanceOffGate(ctx, p, playerID, targets, gateReachable, &rep)
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
	// None of the three can change while the tick runs.
	probeYards map[string][]string
	staffed    map[string]bool
	listings   map[string]probeStock
	// covered is struck off by every branch that answers a target, which keeps one
	// system from being sent both a spare and a fresh probe.
	targets []ExpandSystem
	covered map[string]bool
	rep     *ExpandReport
}

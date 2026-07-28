package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// expansion.go pushes the sensing map outward. The screen judges systems we can
// already see; this is what puts new ones in front of it.
//
// Three jobs, in one tick:
//
//   - FRONTIER. Every system we have actually judged names gate neighbours, and
//     the ones we have never evaluated get a PENDING row so the reconcile's
//     screening sweep picks them up. Only JUDGED systems propagate: expanding
//     through an unscreened neighbour would flood the ledger — and then the
//     screening sweep's API budget — with rows for a galaxy we have no reason to
//     believe is worth anything yet.
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
	TransitionSlot(ctx context.Context, playerID int, waypoint, kind, fromState, toState string, set SlotFields) error
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
}

// ExpandKnobs are the operator-set controls on expansion.
type ExpandKnobs struct {
	// Enabled switches the whole engine off.
	Enabled bool
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
}

// AdvanceExpansion runs one expansion tick.
//
// budgetRate is the sensing residual in requests per second (see
// ExpandKnobs.MinBudgetRate). A disabled or budget-starved tick returns
// immediately having touched NOTHING — not one port call — because this is the
// engine that yields first when the API is under pressure, and an engine that
// still reads its ledger to decide it should not run is not really yielding.
func AdvanceExpansion(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	k ExpandKnobs,
	budgetRate float64,
) (ExpandReport, error) {
	var rep ExpandReport
	if !k.Enabled {
		return ExpandReport{Skipped: "disabled"}, nil
	}
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

	// The systems needing a hull are resolved ONCE, before anything moves, and
	// every branch that covers one strikes it off. That is what keeps a seed
	// retargeted onto a system from also being sent a spare, and a spare claimed
	// for it from also being ordered a fresh probe.
	targets := seedlessTargets(systems)
	covered := make(map[string]bool, len(targets))

	// Seeds move BEFORE spares are claimed, so an errand stamped this tick is
	// not also flown by it: the ship row has not caught up yet, and the next
	// tick reads it. Same discipline as the placement machine's single pass.
	if err := advanceSeeds(ctx, p, playerID, k, systems, targets, covered, book, neighbours, &rep); err != nil {
		return rep, err
	}
	if err := claimSpares(ctx, p, playerID, targets, covered, book, neighbours, &rep); err != nil {
		return rep, err
	}
	return rep, requestSeeds(ctx, p, playerID, targets, covered, book, neighbours, &rep)
}

// --- the ledger's working view ----------------------------------------------

// slotKey addresses ONE placement row, and it mirrors the ledger's primary key
// exactly (sp-dpfp8). A waypoint on its own stopped being an address the moment a
// yard could be scanning as a MARKET placement and staging a seed as a SPARE at
// the same time; keyed on the waypoint alone this book collapsed the two into
// one entry and reported whichever row it read last.
type slotKey struct {
	waypoint string
	kind     string
}

// slotBook is the tick's picture of the placement ledger, mutated as it writes.
// Two seeds finishing in the same system must not claim the same placement, and
// two seed requests must not land on the same yard, so every write is reflected
// here immediately rather than being re-read.
type slotBook struct {
	// state holds every occupied (waypoint, KIND) placement's state. Occupancy is
	// what keeps a write MEANINGFUL: a declaration aimed at a placement that is
	// already there is a write with nothing to say.
	// It is no longer what keeps a write SAFE — the ledger's per-column
	// ownership (sp-wgjb7) is what prevents a declaration reassigning a hull.
	//
	// KEYED ON THE PAIR, and that is the whole fix for the expansion freeze. A
	// waypoint-keyed occupancy test answered "something is here" when the question
	// the caller was actually asking is "is there a placement OF MY KIND here" —
	// and for the seed-staging caller the answer to the second was always no while
	// the answer to the first was always yes.
	state map[slotKey]string
	// wanted lists the unfilled placements in each system.
	wanted map[string][]QueuedSlot
	// spares lists every SPARE placement in any state — the seed SUPPLY. A spare
	// still being bought or flown is a seed already on order, so it suppresses a
	// duplicate request; parkedSpares is the subset that can be claimed as an
	// errand right now.
	//
	// Both pools are consumed as the tick allocates them, so one spare can only
	// ever answer one target WITHIN a tick. Across ticks the pools are rebuilt
	// from the ledger, and consuming one here says nothing about the next — what
	// holds the invariant there is newSlotBook's onErrand filter, which keeps a
	// hull that is already out on a mission out of parkedSpares entirely.
	spares       []QueuedSlot
	parkedSpares []QueuedSlot
}

// newSlotBook builds the tick's view of the placement ledger. onErrand names the
// hulls a system row already has out on a charting mission, and it is what keeps
// ONE HULL TO ONE ERRAND across ticks — see the parkedSpares filter below.
func newSlotBook(rows []QueuedSlot, onErrand map[string]bool) *slotBook {
	b := &slotBook{
		state:  make(map[slotKey]string, len(rows)),
		wanted: make(map[string][]QueuedSlot),
	}
	for _, row := range rows {
		b.state[slotKey{row.Waypoint, row.Kind}] = row.State
		if row.State == SlotStateWanted {
			b.wanted[row.System] = append(b.wanted[row.System], row)
		}
		if row.Kind != SlotKindSpare {
			continue
		}
		// The row stays in the SUPPLY pool even when the hull is away. Supply
		// answers "is a seed already on order for this neighbourhood?", and a
		// stale row saying yes only ever suppresses a purchase — the safe
		// direction, and the same one the claim's write order is chosen for.
		b.spares = append(b.spares, row)
		// But it is NOT claimable. A placement row naming a hull that a system
		// row already has on an errand is a hull the ledger has lost track of,
		// not a spare standing by, and claiming it stamps a SECOND mission on a
		// probe that can only fly one.
		//
		// THE ROW OUTLIVING THE CLAIM IS NORMAL, NOT AN ANOMALY. Two ways it
		// comes back, and neither is a bug this engine can fix from here:
		//
		//   - The claim's own documented crash window. The errand is stamped
		//     first and the row released second, so a failure between them
		//     leaves the hull named by both. That is the deliberate, money-safe
		//     failure direction (it over-counts, which buys FEWER probes), and
		//     it is only transient if the NEXT tick declines to re-claim.
		//   - Probe adoption re-parks it. The adoption pass indexes hulls by
		//     placement row alone and never reads the seed columns, so a hull
		//     whose row we just deleted still looks like an unrecorded probe
		//     standing at a waypoint — it has not physically left yet, because
		//     the mission so far is only a ledger stamp — and gets a fresh
		//     SPARE/PARKED row written for it. Every tick, until it departs.
		//
		// Without this filter the second case is a loop: re-park, re-claim, one
		// hull stamped onto system after system while the errands it already
		// holds mark those systems covered and the idle hulls that could have
		// served them stay parked.
		if row.State == SlotStateParked && row.AssignedShip != "" && !onErrand[row.AssignedShip] {
			b.parkedSpares = append(b.parkedSpares, row)
		}
	}
	return b
}

// hullsOnErrand indexes the hulls that system rows already have out charting.
//
// Keyed on the hull rather than the system because that is the invariant being
// protected: a system may be re-targeted, but a probe cannot be in two places.
// DONE is deliberately absent — hasActiveSeed treats a finished errand as over,
// and a hull whose mission ended is a spare again the moment one is recorded
// for it.
func hullsOnErrand(systems []ExpandSystem) map[string]bool {
	hulls := make(map[string]bool, len(systems))
	for _, s := range systems {
		if hasActiveSeed(s) {
			hulls[s.SeedShip] = true
		}
	}
	return hulls
}

// takeSupplyFor consumes a spare that could serve target — one parked in a
// system BORDERING it — and reports whether it found one. A match means a seed
// for that target already exists somewhere in the pipeline, so no second one
// should be ordered.
//
// The adjacency test is the whole point. A blanket count of every SPARE row in
// the ledger suppresses demand it cannot possibly serve: a spare parked three
// systems from the frontier is not a seed for that frontier, and nothing will
// ever turn it into one (the buy queue's spare re-task only scans within a
// single system). Counted bluntly, one such idle hull stalls expansion
// permanently, and so does any stale row — a placement reverted to WANTED by a
// re-task, or one left QUEUED by a purchase that could not be afforded.
//
// Reading a QUEUED spare as supply is NOT reading it as a purchase in flight.
// The two questions are different: the probe cap asks "does a hull exist?" —
// where QUEUED must answer no, because it may equally be a claim the treasury
// refused — while this asks "is a request already outstanding?", and a QUEUED
// row is exactly that. The buy queue re-drains QUEUED placements every tick, so
// writing a second row for the same target would only duplicate an intent it is
// already working.
func (b *slotBook) takeSupplyFor(target string, neighbours map[string][]string) bool {
	for i, spare := range b.spares {
		if !contains(neighbours[spare.System], target) {
			continue
		}
		b.spares = append(b.spares[:i], b.spares[i+1:]...)
		return true
	}
	return false
}

// occupied reports whether a waypoint already carries a placement row OF THIS
// KIND.
//
// The kind is the whole question (sp-dpfp8). A probe-selling yard is very often
// already a parked MARKET placement — that is what a yard worth buying at looks
// like — and under a waypoint-only test that made it permanently ineligible to
// stage a SPARE. The fleet's only two probe yards were both in exactly that
// state, so requestSeeds found no free yard on any tick and expansion sat at two
// charting seeds with no way to ever order a third. A scanning yard and a staging
// yard are two different claims on the same waypoint, and they do not conflict.
func (b *slotBook) occupied(waypoint, kind string) bool {
	_, held := b.state[slotKey{waypoint, kind}]
	return held
}

// wantedIn returns the system's unfilled placements, the one the hull is already
// standing on first. A seed that can fill the placement under its own feet costs
// no movement at all.
func (b *slotBook) wantedIn(system, standingOn string) []QueuedSlot {
	rows := b.wanted[system]
	out := make([]QueuedSlot, 0, len(rows))
	for _, row := range rows {
		if row.Waypoint == standingOn {
			out = append(out, row)
		}
	}
	for _, row := range rows {
		if row.Waypoint != standingOn {
			out = append(out, row)
		}
	}
	return out
}

// wantedAt returns the unfilled placement on one waypoint, if there is one.
func (b *slotBook) wantedAt(system, waypoint string) (QueuedSlot, bool) {
	for _, row := range b.wanted[system] {
		if row.Waypoint == waypoint {
			return row, true
		}
	}
	return QueuedSlot{}, false
}

// take marks a placement as filled by this tick.
//
// The wanted list is pruned by WAYPOINT AND KIND for the same reason the state
// map is keyed on both: two unfilled placements can share a waypoint, and filling
// the market one does not fill the spare one.
func (b *slotBook) take(system, waypoint, kind, state string) {
	b.state[slotKey{waypoint, kind}] = state
	remaining := b.wanted[system][:0]
	for _, row := range b.wanted[system] {
		if row.Waypoint != waypoint || row.Kind != kind {
			remaining = append(remaining, row)
		}
	}
	b.wanted[system] = remaining
}

// addSpare records a SPARE placement written by this tick. It joins the supply
// pool so a second target in the same neighbourhood does not order another.
//
// It writes the SPARE half of the waypoint only: a MARKET placement standing at
// the same yard is untouched, still occupied, and still scanning.
func (b *slotBook) addSpare(system, waypoint, state string) {
	b.state[slotKey{waypoint, SlotKindSpare}] = state
	b.spares = append(b.spares, QueuedSlot{
		Waypoint: waypoint, System: system, Kind: SlotKindSpare, State: state,
	})
}

// dropSpare records a SPARE placement this tick handed to a mission: the hull
// belongs to the errand now, so the row is gone and the supply it represented is
// spent.
//
// Only the SPARE half is dropped, mirroring the kind-scoped DeleteSlot this
// shadows. Forgetting the whole waypoint here would let the tick's later writes
// re-declare a MARKET placement that is still very much on the books.
func (b *slotBook) dropSpare(waypoint string) {
	delete(b.state, slotKey{waypoint, SlotKindSpare})
	for i, spare := range b.spares {
		if spare.Waypoint == waypoint {
			b.spares = append(b.spares[:i], b.spares[i+1:]...)
			return
		}
	}
}

// --- frontier ----------------------------------------------------------------

// knownSystems is the set of systems the ledger already holds a row for.
func knownSystems(systems []ExpandSystem) map[string]bool {
	known := make(map[string]bool, len(systems))
	for _, s := range systems {
		known[s.System] = true
	}
	return known
}

// readNeighbours resolves the gate neighbours of every system expansion may
// propagate from: the ones we have actually JUDGED, plus any holding a parked
// spare (whose neighbours decide which frontier that spare can reach).
//
// A PENDING system is deliberately not expanded through. It is a system we have
// recorded but not yet decided about, and propagating from it would let one tick
// of discovery seed the next, walking the ledger — and the screening sweep's API
// budget — across a galaxy we have no evidence is worth anything.
func readNeighbours(ctx context.Context, p ExpandPorts, systems []ExpandSystem, book *slotBook) (map[string][]string, error) {
	origins := make(map[string]bool, len(systems))
	for _, s := range systems {
		if s.Verdict == VerdictInScope || s.Verdict == VerdictNoWhitelist {
			origins[s.System] = true
		}
	}
	for _, spare := range book.parkedSpares {
		origins[spare.System] = true
	}

	ordered := make([]string, 0, len(origins))
	for system := range origins {
		ordered = append(ordered, system)
	}
	sort.Strings(ordered)

	neighbours := make(map[string][]string, len(ordered))
	for _, system := range ordered {
		adjacent, err := p.Gates.Neighbours(ctx, system)
		if err != nil {
			return nil, fmt.Errorf("failed to read gate neighbours of %q: %w", system, err)
		}
		neighbours[system] = adjacent
	}
	return neighbours, nil
}

// markFrontier records a PENDING row for every neighbour we have never
// evaluated, which is what puts it in front of the screening sweep. The verdict
// written is PENDING and nothing else: this engine has looked at nothing and
// must not appear to have judged anything.
func markFrontier(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	neighbours map[string][]string,
	known map[string]bool,
	rep *ExpandReport,
) error {
	for _, system := range sortedKeys(neighbours) {
		for _, neighbour := range neighbours[system] {
			if neighbour == "" || known[neighbour] {
				continue
			}
			if err := p.Ledger.UpsertSystem(ctx, playerID, SystemRecord{
				System:  neighbour,
				Verdict: VerdictPending,
			}); err != nil {
				return fmt.Errorf("failed to record frontier system %q: %w", neighbour, err)
			}
			known[neighbour] = true
			rep.Discovered++
		}
	}
	return nil
}

// --- seed supply -------------------------------------------------------------

// seedlessTargets are the systems with charting work and no hull on the way.
//
// Charting work is uncharted waypoints OR a waypoint list we have never swept.
// The second is not a lesser case of the first: an unswept system reports zero
// uncharted waypoints precisely BECAUSE we have never looked, so a count-only
// rule would leave every genuinely unexplored system permanently invisible —
// which is the entire population expansion exists to reach.
//
// Ordered deepest-dark first so the biggest KNOWN unknown is resolved soonest,
// then by symbol for a queue that is reproducible tick to tick. An unswept
// system carries an honest count of zero and therefore sorts last, which is
// deliberate: it might hold thirty uncharted waypoints or none at all, and
// ranking a guess above a measured thirty would be inventing evidence.
func seedlessTargets(systems []ExpandSystem) []ExpandSystem {
	var out []ExpandSystem
	for _, s := range systems {
		if (s.UnchartedCount > 0 || !s.CatalogKnown) && !hasActiveSeed(s) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UnchartedCount != out[j].UnchartedCount {
			return out[i].UnchartedCount > out[j].UnchartedCount
		}
		return out[i].System < out[j].System
	})
	return out
}

// hasActiveSeed reports whether a system already has a hull on the errand. DONE
// is not active: the errand is over and the row is only still naming its hull so
// the probe stays attributable.
func hasActiveSeed(s ExpandSystem) bool {
	return s.SeedShip != "" && (s.SeedState == SeedStateDispatched || s.SeedState == SeedStateCharting)
}

// claimSpares turns parked spare hulls into charting errands.
//
// A spare is only usable for a target it can actually REACH: a seed crosses to
// its target in one gate hop, so the spare must be sitting in a system that
// borders it. A spare parked three systems away is left where it is for the
// placement machinery to re-task.
//
// THE WRITE ORDER IS A MONEY GUARD. One hull is named by two rows for an
// instant, and choosing which instant decides which way a crash miscounts. The
// errand is stamped FIRST, so a failure between the writes leaves the hull named
// by both the spare row and the mission — an over-count, which only ever buys
// FEWER probes. Releasing the row first would leave a failure with the hull
// named by NEITHER, and the probe cap would authorise buying a replacement for a
// hull we already own (RULINGS #4).
func claimSpares(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	targets []ExpandSystem,
	covered map[string]bool,
	book *slotBook,
	neighbours map[string][]string,
	rep *ExpandReport,
) error {
	for _, target := range targets {
		if covered[target.System] {
			continue
		}
		spare, found := takeReachableSpare(book, target.System, neighbours)
		if !found {
			continue
		}
		if err := p.Ledger.SetSeed(ctx, playerID, target.System, spare.AssignedShip, SeedStateDispatched); err != nil {
			return fmt.Errorf("failed to send spare %s to chart %q: %w", spare.AssignedShip, target.System, err)
		}
		// The hull now belongs to the errand rather than to the ledger, so its
		// placement row goes away. It is invisible to the probe-cap count until
		// the seed parks again — an UNDER-count, and the one place this engine
		// accepts one. It is bounded (a seed exists only while an errand runs,
		// and errands are capped per tick) and self-healing (every terminal
		// branch of a tour ends in a placement row naming the hull again), and
		// the alternative — leaving a stale spare row behind — would have the
		// buy queue re-task a hull that has already left.
		//
		// RELEASED BY KIND, not by waypoint (sp-dpfp8). The spare was very likely
		// staged AT A YARD that is also a parked market — that co-location is the
		// entire point of the wider key — and a waypoint-wide delete would take
		// the MARKET row with it, dropping the probe scanning there out of the cap
		// while it is still on station. This engine's under-count is deliberate
		// and bounded; that one would be neither.
		if err := p.Ledger.DeleteSlot(ctx, playerID, spare.Waypoint, spare.Kind); err != nil {
			return fmt.Errorf(
				"spare %s sent to chart %q but its placement %s was not released (hull now double-counted, probe cap reads high): %w",
				spare.AssignedShip, target.System, spare.Waypoint, err)
		}
		book.dropSpare(spare.Waypoint)
		covered[target.System] = true
		rep.SeedsClaimed++
	}
	return nil
}

// takeReachableSpare removes and returns a parked spare that borders target.
func takeReachableSpare(book *slotBook, target string, neighbours map[string][]string) (QueuedSlot, bool) {
	for i, spare := range book.parkedSpares {
		if !contains(neighbours[spare.System], target) {
			continue
		}
		book.parkedSpares = append(book.parkedSpares[:i], book.parkedSpares[i+1:]...)
		return spare, true
	}
	return QueuedSlot{}, false
}

// requestSeeds enqueues SPARE placements for the targets no hull covers yet.
//
// This engine never buys. It records a want at a yard the buy queue can
// realistically fund — one in a system we already hold, since the queue only
// buys where a hull of ours is standing at the counter — and the queue applies
// the treasury floor and the probe cap to it exactly as it does to every other
// placement. That is the whole point of asking rather than spending: expansion
// gets no second, unguarded path to the treasury.
func requestSeeds(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	targets []ExpandSystem,
	covered map[string]bool,
	book *slotBook,
	neighbours map[string][]string,
	rep *ExpandReport,
) error {
	// Yards are resolved once per origin and reused: several frontier targets
	// usually border the same system of ours.
	yardsByOrigin := map[string][]string{}
	for _, target := range targets {
		if rep.Actions >= MaxExpansionActions {
			return nil
		}
		if covered[target.System] {
			continue
		}
		if book.takeSupplyFor(target.System, neighbours) {
			// A seed for this target is already somewhere in the pipeline. That
			// is what keeps a frontier several ticks away from being re-ordered
			// on every one of them.
			continue
		}

		yard, system, err := stagingYardFor(ctx, p, target.System, neighbours, book, yardsByOrigin)
		if err != nil {
			return err
		}
		if yard == "" {
			// Nowhere to stage a purchase this tick: no bordering system of ours
			// has a probe-selling yard free of a placement. Expected while the
			// map is thin, and it costs nothing — the target simply waits, and
			// takes nothing from the targets we CAN reach on its way past.
			continue
		}
		if err := p.Ledger.UpsertSlotMetadata(ctx, playerID, SlotRecord{
			Waypoint: yard,
			System:   system,
			Kind:     SlotKindSpare,
			State:    SlotStateWanted,
		}); err != nil {
			return fmt.Errorf("failed to request a charting seed for %q: %w", target.System, err)
		}
		book.addSpare(system, yard, SlotStateWanted)
		rep.SeedsRequested++
		rep.Actions++
	}
	return nil
}

// stagingYardFor picks where a seed for target should be bought: a probe-selling
// yard, in one of our own systems bordering the target, that carries no
// placement of its own.
//
// The free-waypoint requirement started as a money guard: placement rows are
// keyed on the waypoint, and writing a SPARE want over the yard's existing row
// used to overwrite whatever state it held — dropping a parked probe out of the
// cap count and authorising the purchase of a replacement standing right there.
//
// The ledger now refuses that write on its own (UpsertSlotMetadata cannot touch
// state or assigned_ship — sp-wgjb7), so the guard is defence in depth rather
// than the only thing standing between a miss and a double purchase. It still
// earns its place: staging a seed on an occupied yard would be pointless work,
// and picking a free one is how two seed requests avoid the same waypoint.
func stagingYardFor(
	ctx context.Context,
	p ExpandPorts,
	target string,
	neighbours map[string][]string,
	book *slotBook,
	yardsByOrigin map[string][]string,
) (string, string, error) {
	for _, origin := range sortedKeys(neighbours) {
		if !contains(neighbours[origin], target) {
			continue
		}
		yards, cached := yardsByOrigin[origin]
		if !cached {
			var err error
			if yards, err = p.Yards.ListProbeYards(ctx, origin); err != nil {
				return "", "", fmt.Errorf("failed to list probe yards in %q: %w", origin, err)
			}
			yardsByOrigin[origin] = yards
		}
		for _, yard := range yards {
			// Asked of the SPARE half only. A yard that is also a parked market is
			// a perfectly good place to stage a seed — it is, in fact, the normal
			// case, since the yards worth buying at are the ones we already watch.
			if !book.occupied(yard, SlotKindSpare) {
				return yard, origin, nil
			}
		}
	}
	return "", "", nil
}

// --- seed lifecycle ----------------------------------------------------------

// advanceSeeds moves every running errand one step, up to the tick's budget.
//
// One step per seed per tick, and a step is a step whether it commands a ship or
// merely advances the record: both end the seed's turn. That is what keeps the
// engine from reading a position its own command has just invalidated — the next
// tick reads the ships table instead.
func advanceSeeds(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	k ExpandKnobs,
	systems []ExpandSystem,
	targets []ExpandSystem,
	covered map[string]bool,
	book *slotBook,
	neighbours map[string][]string,
	rep *ExpandReport,
) error {
	active := make([]ExpandSystem, 0, len(systems))
	for _, s := range systems {
		if hasActiveSeed(s) {
			active = append(active, s)
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].System < active[j].System })

	for _, s := range active {
		if rep.Actions >= MaxExpansionActions {
			return nil
		}
		acted, err := advanceSeed(ctx, p, playerID, k, s, targets, covered, book, neighbours, rep)
		if err != nil {
			return err
		}
		if acted {
			rep.Actions++
		}
	}
	return nil
}

// advanceSeed applies the one step available to a single errand and reports
// whether it consumed the tick's budget.
func advanceSeed(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	k ExpandKnobs,
	s ExpandSystem,
	targets []ExpandSystem,
	covered map[string]bool,
	book *slotBook,
	neighbours map[string][]string,
	rep *ExpandReport,
) (bool, error) {
	pos, err := p.Ships.ShipAt(ctx, playerID, s.SeedShip)
	if err != nil || !pos.Found {
		// Never command a hull we cannot locate. An unreadable row and an absent
		// one leave the errand exactly as it is, to be retried once the ships
		// table can answer.
		return false, nil
	}
	if pos.NavStatus == navigation.NavStatusInTransit {
		return false, nil // already flying, under this or an earlier tick's command
	}

	if s.SeedState == SeedStateDispatched {
		return dispatchSeed(ctx, p, playerID, s, pos, rep)
	}
	return chartSeed(ctx, p, playerID, k, s, pos, targets, covered, book, neighbours, rep)
}

// dispatchSeed gets a hull to its target system, one gate hop at a time.
func dispatchSeed(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	s ExpandSystem,
	pos ShipPos,
	rep *ExpandReport,
) (bool, error) {
	if shared.ExtractSystemSymbol(pos.Waypoint) == s.System {
		// Arrived. Sweep the waypoint LIST before the tour begins, because the
		// tour is driven entirely off the stored uncharted set and, for a system
		// nobody has visited, that set is EMPTY — not because the system is
		// charted but because we have never asked. Without this the seed would
		// arrive, read nothing to do, and immediately "finish" a tour during
		// which it charted precisely nothing.
		//
		// This ONE action may issue several API calls: the waypoint list is
		// paginated, so the adapter walks its pages. That is deliberate rather
		// than a leak in the budget — it is bounded by a single system's page
		// count, happens once per system for the life of the era, and splitting
		// it across ticks would leave a half-known catalog that the very next
		// step would misread as a finished tour.
		if err := p.SeedShip.SyncWaypoints(ctx, playerID, s.System); err != nil {
			// The catalog is not written, so it is not stamped either. The seed
			// stays DISPATCHED and the next tick retries from the same place.
			//
			// Logged rather than swallowed because the retry is UNBOUNDED and
			// not cheap: each attempt may walk several pages of the waypoint
			// list, every tick, for as long as the sweep keeps failing — and
			// this is the engine that is supposed to yield first when the API is
			// under pressure. A silent version of this loop is invisible until
			// it shows up as rate-limit pressure somewhere else entirely.
			logging.LoggerFromContext(ctx).Log("WARN", "charting seed could not sweep its target's waypoint catalog; retrying next tick", map[string]interface{}{
				"action":      "parked_sensing_catalog_sweep_failed",
				"ship_symbol": s.SeedShip,
				"system":      s.System,
				"error":       err.Error(),
			})
			return true, nil
		}
		if err := p.Ledger.StampCatalogSynced(ctx, playerID, s.System); err != nil {
			return false, fmt.Errorf("failed to record the swept waypoint catalog of %q: %w", s.System, err)
		}

		// The state advances on the SHIP ROW, never on a jump command returning
		// — a command that succeeded and a hull that is actually there are
		// different facts, and only the second one licenses charting.
		if err := p.Ledger.SetSeed(ctx, playerID, s.System, s.SeedShip, SeedStateCharting); err != nil {
			return false, fmt.Errorf("failed to start the charting tour of %q: %w", s.System, err)
		}
		return true, nil
	}

	// ONE STEP of the gate hop — the move to the gate, or the jump off it. Which
	// one is decided by the hull's own position, which is why it is handed down
	// rather than re-derived. A hull mid-move is never here at all: advanceSeed
	// has already returned on the IN_TRANSIT reading above, so the step issued
	// is always the next one actually available.
	if err := p.SeedShip.JumpTo(ctx, playerID, s.SeedShip, pos.Waypoint, s.System); err != nil {
		// The hull did not leave. Holding the errand at DISPATCHED is what makes
		// the retry free: the next tick re-reads the position and re-issues.
		return true, nil
	}
	rep.Jumped++
	return true, nil
}

// chartSeed works one waypoint of a tour, or ends it.
func chartSeed(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	k ExpandKnobs,
	s ExpandSystem,
	pos ShipPos,
	targets []ExpandSystem,
	covered map[string]bool,
	book *slotBook,
	neighbours map[string][]string,
	rep *ExpandReport,
) (bool, error) {
	remaining, err := p.Uncharted.UnchartedWaypoints(ctx, s.System)
	if err != nil {
		return false, nil // unreadable: leave the tour alone and retry next tick
	}
	if len(remaining) == 0 {
		return finishTour(ctx, p, playerID, s, pos, targets, covered, book, neighbours, rep)
	}

	if !contains(remaining, pos.Waypoint) {
		if err := p.SeedShip.NavigateTo(ctx, playerID, s.SeedShip, remaining[0]); err != nil {
			return true, nil // retried next tick from the same position
		}
		rep.Navigated++
		return true, nil
	}
	return chartHere(ctx, p, playerID, k, s, pos, book, rep)
}

// chartHere charts the waypoint under the hull and reads what it revealed. The
// whole bundle — chart, refresh, and the market read behind it — is ONE step:
// it is a single indivisible piece of progress on one waypoint, and splitting it
// across ticks would leave a charted waypoint whose market we then have to fly
// back to.
func chartHere(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	k ExpandKnobs,
	s ExpandSystem,
	pos ShipPos,
	book *slotBook,
	rep *ExpandReport,
) (bool, error) {
	if err := p.SeedShip.Chart(ctx, playerID, s.SeedShip); err != nil {
		return true, nil // nothing charted, so nothing to read back
	}
	isMarket, err := p.SeedShip.RefreshWaypoint(ctx, playerID, s.System, pos.Waypoint)
	if err != nil {
		// The chart landed but the waypoint was not written back, so the stored
		// uncharted set still names it and the next tick charts it again — a
		// benign no-op at the API. Nothing further can be trusted about it here.
		return true, nil
	}
	rep.Charted++

	if !isMarket {
		return true, nil
	}
	if err := p.SeedShip.ReadMarketAt(ctx, playerID, pos.Waypoint); err != nil {
		return true, nil // prices unread; the screen will resolve the market later
	}
	return true, recordSeedMarket(ctx, p, playerID, k, s.System, pos.Waypoint, book, rep)
}

// recordSeedMarket writes the placement a seed-revealed market earns.
//
// The write goes STRAIGHT to the ledger rather than waiting for the next screen,
// and the screen's slot cache is built on that: for a system still marked
// PENDING, market_data is the only record that the seed was ever here, and a
// placement row is what lets the screen resolve this waypoint from the cache
// instead of paying the API to rediscover it.
func recordSeedMarket(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	k ExpandKnobs,
	system, waypoint string,
	book *slotBook,
	rep *ExpandReport,
) error {
	if book.occupied(waypoint, SlotKindMarket) {
		return nil // already placed; the existing row holds the live state
	}
	goods, known, err := p.MarketGoods.GoodsAt(ctx, playerID, waypoint)
	if err != nil || !known {
		return nil
	}
	matched := matchWhitelist(goods, k.Whitelist)
	if len(matched) == 0 {
		return nil // a market dealing in nothing we want earns no probe
	}

	// Depth is measured, not assumed: the seed is standing at the counter, so
	// the scan it just took carries real prices — unlike a market discovered
	// remotely, which is slotted with a blind zero.
	rows, err := p.MarketGoods.DepthRowsAt(ctx, playerID, waypoint)
	if err != nil {
		return nil
	}
	if err := p.Ledger.UpsertSlotMetadata(ctx, playerID, SlotRecord{
		Waypoint:       waypoint,
		System:         system,
		Kind:           SlotKindMarket,
		State:          SlotStateWanted,
		WhitelistGoods: matched,
		DepthCredits:   depthOf(rows, k.Whitelist),
	}); err != nil {
		return fmt.Errorf("failed to record the market a seed found at %q: %w", waypoint, err)
	}
	book.take(system, waypoint, SlotKindMarket, SlotStateWanted)
	rep.MarketsFound++
	return nil
}

// finishTour stands a seed down once its target is charted through.
//
// The system is re-screened first, because everything the tour learned has been
// written to the local caches and the verdict it produces is what decides the
// hull's fate. Then, in order of what the hull is worth where it is:
//
//  1. fill a placement in this system — the errand ended somewhere we want
//     watched, and a hull already standing there is the cheapest probe we will
//     ever place;
//  2. otherwise push on to the next frontier system reachable from here, which
//     is a free extension of an errand already paid for;
//  3. otherwise stand down as a spare, staying on the books for the buy queue to
//     re-task.
func finishTour(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	s ExpandSystem,
	pos ShipPos,
	targets []ExpandSystem,
	covered map[string]bool,
	book *slotBook,
	neighbours map[string][]string,
	rep *ExpandReport,
) (bool, error) {
	result, err := p.Screen(ctx, s.System)
	if err != nil {
		// No verdict, no decision. Standing the hull down on a system we failed
		// to judge would either strand it or write off a system we never read.
		return true, nil
	}

	current := shared.ExtractSystemSymbol(pos.Waypoint)
	if result.Verdict == VerdictInScope {
		filled, err := fillPlacement(ctx, p, playerID, s, current, book.wantedIn(current, pos.Waypoint), book, rep)
		if err != nil || filled {
			return true, err
		}
	}

	retargeted, err := retargetSeed(ctx, p, playerID, s, current, targets, covered, neighbours, rep)
	if err != nil || retargeted {
		return true, err
	}
	return true, standDownAsSpare(ctx, p, playerID, s, pos, current, book, rep)
}

// fillPlacement hands the seed hull to the first of the offered placements it
// can claim, and reports whether one was taken.
func fillPlacement(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	s ExpandSystem,
	current string,
	wants []QueuedSlot,
	book *slotBook,
	rep *ExpandReport,
) (bool, error) {
	for _, want := range wants {
		// IN_TRANSIT rather than PARKED even when the hull is already standing
		// on the waypoint: PARKED is recorded only on a CONFIRMED docked
		// reading, and the placement machine is the only thing that takes it.
		hull := s.SeedShip
		err := p.Ledger.TransitionSlot(ctx, playerID, want.Waypoint, want.Kind, SlotStateWanted, SlotStateInTransit,
			SlotFields{AssignedShip: &hull})
		switch {
		case errors.Is(err, ErrSlotClaimed):
			continue // another writer took this placement; the next one may be free
		case err != nil:
			// Not contention: the ledger is refusing writes. Reading that as a
			// lost race would have the seed fall through to standing itself
			// down as a spare — writing to the very ledger that just failed.
			return false, fmt.Errorf("failed to hand seed %s to placement %s: %w", hull, want.Waypoint, err)
		}
		book.take(current, want.Waypoint, want.Kind, SlotStateInTransit)
		if err := p.Ledger.SetSeed(ctx, playerID, s.System, "", ""); err != nil {
			return true, fmt.Errorf(
				"seed %s filled placement %s but its errand on %q was not cleared: %w",
				hull, want.Waypoint, s.System, err)
		}
		rep.Parked++
		return true, nil
	}
	return false, nil
}

// retargetSeed sends a finished seed on to the next system reachable from where
// it stands that still needs charting, and reports whether it found one.
//
// Reachability is ONE gate hop, because that is what a dispatched seed executes.
// The candidate must also be a system the ledger says has charting work: this is
// the same seedless-target list the spare claim and the purchase request draw
// from, so a system covered by a retarget cannot also be sent a hull it does not
// need — and, just as importantly, "needs charting" means one thing across the
// whole engine rather than one thing here and another there.
//
// Retargeting is two writes because the row IS the target, and neither order is
// safe on its own:
//
//   - Stamp first, and a failure between the writes leaves TWO systems naming
//     one hull. Both would drive it as their own seed, double-stepping it every
//     tick and sending it two conflicting places.
//   - Clear first, and a failure leaves it named by NEITHER. A mid-tour hull has
//     no placement row — it was deleted when the seed was claimed — so the
//     errand is the only thing naming it at all, and losing that orphans a probe
//     we paid for: invisible to the probe cap, and re-bought. That is the
//     direction claimSpares refuses for exactly the same reason.
//
// So it clears first (the single-driver invariant is preserved unconditionally)
// and then RESTORES the old errand if the stamp fails, which closes the orphan
// window in the only case that opens it. A restore that itself fails is logged
// loudly with both systems and the hull named, because at that point the probe
// can only be recovered by hand.
func retargetSeed(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	s ExpandSystem,
	current string,
	targets []ExpandSystem,
	covered map[string]bool,
	neighbours map[string][]string,
	rep *ExpandReport,
) (bool, error) {
	adjacent, err := neighboursOf(ctx, p, current, neighbours)
	if err != nil {
		return false, err
	}
	for _, target := range targets {
		if covered[target.System] || target.System == s.System || !contains(adjacent, target.System) {
			continue
		}
		if err := p.Ledger.SetSeed(ctx, playerID, s.System, "", ""); err != nil {
			return false, fmt.Errorf("failed to end the charting tour of %q: %w", s.System, err)
		}
		if err := p.Ledger.SetSeed(ctx, playerID, target.System, s.SeedShip, SeedStateDispatched); err != nil {
			return true, restoreErrand(ctx, p, playerID, s, target.System, err)
		}
		covered[target.System] = true
		rep.Retargeted++
		return true, nil
	}
	return false, nil
}

// restoreErrand puts a half-retargeted seed back where it was, and returns the
// error the caller should surface.
//
// The hull is unnamed at this instant: the old errand is cleared and the new one
// did not land. Since a mid-tour hull has no placement row either, nothing in
// the ledger knows it exists — so restoring the ORIGINAL errand (rather than
// leaving it, or retrying the new one) is what keeps a probe we paid for
// attributable. The seed simply finishes its tour again next tick and retries
// the move; a repeated no-op tour is cheap, an orphaned probe is not.
func restoreErrand(ctx context.Context, p ExpandPorts, playerID int, s ExpandSystem, target string, cause error) error {
	if restoreErr := p.Ledger.SetSeed(ctx, playerID, s.System, s.SeedShip, s.SeedState); restoreErr != nil {
		logging.LoggerFromContext(ctx).Log("ERROR", "charting seed is named by no errand after a failed retarget; the hull is unattributable until an operator restores it", map[string]interface{}{
			"action":        "parked_sensing_seed_orphaned",
			"ship_symbol":   s.SeedShip,
			"from_system":   s.System,
			"target_system": target,
			"error":         restoreErr.Error(),
		})
		return fmt.Errorf(
			"failed to retarget seed %s onto %q AND could not restore its errand on %q (hull now unattributable): %w",
			s.SeedShip, target, s.System, cause)
	}
	return fmt.Errorf("failed to retarget seed %s onto %q (errand on %q restored): %w",
		s.SeedShip, target, s.System, cause)
}

// neighboursOf reads a system's gate neighbours from the tick's map, falling
// back to the gate store for a system the map does not cover — a tour usually
// ends in a system that was still PENDING when the map was built, and PENDING
// systems are deliberately not walked.
func neighboursOf(ctx context.Context, p ExpandPorts, system string, neighbours map[string][]string) ([]string, error) {
	if adjacent, resolved := neighbours[system]; resolved {
		return adjacent, nil
	}
	adjacent, err := p.Gates.Neighbours(ctx, system)
	if err != nil {
		return nil, fmt.Errorf("failed to read gate neighbours of %q: %w", system, err)
	}
	return adjacent, nil
}

// standDownAsSpare parks a finished seed where it stands, as a reserve hull the
// buy queue can re-task for free.
//
// The placement it writes is what puts the probe back on the books: for the
// length of the errand the hull was named only by its system row, and this is
// where that ends.
//
// An unfilled placement on the very waypoint the hull is standing on is taken
// INSTEAD, and takes precedence over the whole-system verdict that got us here.
// A system can be rejected as a whole and still hold one market worth watching
// — the seed's own market reads write wants directly, before any verdict — and
// a hull already berthed on one is the cheapest probe we will ever place.
//
// A waypoint carrying any OTHER placement is left strictly alone: overwriting it
// would reassign the hull that row names. The seed is stood down DONE instead,
// keeping this hull named by the system row rather than by nothing at all. That
// branch should be unreachable — a waypoint the seed had to CHART cannot already
// hold a filled placement, which the screen only ever writes for charted
// waypoints — so it is logged rather than handled silently.
func standDownAsSpare(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	s ExpandSystem,
	pos ShipPos,
	current string,
	book *slotBook,
	rep *ExpandReport,
) error {
	if want, wanted := book.wantedAt(current, pos.Waypoint); wanted {
		filled, err := fillPlacement(ctx, p, playerID, s, current, []QueuedSlot{want}, book, rep)
		if err != nil || filled {
			return err
		}
	}

	// Asked of the SPARE half only (sp-dpfp8). This branch strands a hull — it
	// stands the seed down with NO placement row, so the probe cap stops counting
	// a probe we own, which is the money-unsafe direction and is why it is logged
	// rather than passed over. Under the old waypoint-wide test a seed finishing
	// on any placement at all landed here, including the common case of a market
	// it had just charted. Now only a genuine SPARE-on-SPARE collision does, and
	// everything else parks properly and stays counted.
	if book.occupied(pos.Waypoint, SlotKindSpare) {
		logging.LoggerFromContext(ctx).Log("WARN", "charting seed finished on a waypoint that already holds a spare placement; standing it down without a slot", map[string]interface{}{
			"action":      "parked_sensing_seed_standdown_blocked",
			"ship_symbol": s.SeedShip,
			"waypoint":    pos.Waypoint,
			"system":      s.System,
		})
		if err := p.Ledger.SetSeed(ctx, playerID, s.System, s.SeedShip, SeedStateDone); err != nil {
			return fmt.Errorf("failed to stand seed %s down on %q: %w", s.SeedShip, s.System, err)
		}
		return nil
	}

	if err := p.Ledger.UpsertSpareSlot(ctx, playerID, SlotRecord{
		Waypoint:     pos.Waypoint,
		System:       current,
		Kind:         SlotKindSpare,
		State:        SlotStateParked,
		AssignedShip: s.SeedShip,
	}); err != nil {
		return fmt.Errorf("failed to park seed %s as a spare at %q: %w", s.SeedShip, pos.Waypoint, err)
	}
	book.addSpare(current, pos.Waypoint, SlotStateParked)
	if err := p.Ledger.SetSeed(ctx, playerID, s.System, "", ""); err != nil {
		return fmt.Errorf(
			"seed %s parked as a spare at %s but its errand on %q was not cleared (hull double-counted, probe cap reads high): %w",
			s.SeedShip, pos.Waypoint, s.System, err)
	}
	rep.Parked++
	return nil
}

// --- small helpers -----------------------------------------------------------

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
	// ListingMemo is the STORED shipyard listings — the same read the buy queue's
	// probe-listing memo makes, wired here so staging can prefer a yard we have
	// EVIDENCE sells probes over one the trait fallback merely guessed at.
	//
	// OPTIONAL: a nil memo leaves staging choosing the nearest staffed yard exactly
	// as it did before this port existed.
	ListingMemo ProbeListingMemo
	// OffGate is the warp-expansion slice: the ports that raise explorer demand and warp an
	// explorer past a sealed gate frontier. See offgate.go.
	OffGate OffGatePorts
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

	// A system's probe-selling yards, resolved at most once per TICK and shared
	// by both consumers: the finishing seed picking which placement to fill, and
	// seed staging picking where a neighbour's probe can be bought. The two ask
	// about overlapping sets of systems, and sharing the memo is what keeps them
	// from reading the same catalog twice — and from ever disagreeing about which
	// waypoints are yards.
	probeYards := map[string][]string{}

	// How far every system is from every other, out to the reach a seed can
	// actually fly. One memo for the whole tick, shared by all four consumers —
	// supply, the spare claim, staging and the retarget — so none of them can
	// disagree about what is reachable. See gateReach.
	reach := newGateReach(p.Gates, neighbours, SeedFlightUnbounded)

	// Nearest first: a one-hop errand is one flight, a two-hop errand two, and
	// the probe is held for the whole of it. Ordering here rather than in each
	// consumer is what makes the choice consistent across all three of them.
	targets, err = orderByReach(ctx, reach, targets, book)
	if err != nil {
		return rep, err
	}

	// THE OUTER KEY: systems whose gate we have never read come first. Applied as a
	// second stable pass rather than folded into orderByReach so each sort stays one
	// idea, and so the compound key reads in the order it is written — gate mapping,
	// then distance, then the depth ordering seedlessTargets established.
	targets, err = orderByGateMapping(ctx, p, targets)
	if err != nil {
		return rep, err
	}

	// Seeds move BEFORE spares are claimed, so an errand stamped this tick is
	// not also flown by it: the ship row has not caught up yet, and the next
	// tick reads it. Same discipline as the placement machine's single pass.
	if err := advanceSeeds(ctx, p, playerID, k, systems, targets, covered, book, reach, probeYards, &rep); err != nil {
		return rep, err
	}
	if err := claimSpares(ctx, p, playerID, targets, covered, book, reach, &rep); err != nil {
		return rep, err
	}
	if err := requestSeeds(ctx, p, playerID, targets, covered, book, reach, probeYards, &rep); err != nil {
		return rep, err
	}

	// OFF-GATE, LAST, and only once the gate passes have had their turn. Warp is the expensive
	// fallback: a 769k hull and a multi-system flight against a probe that walks a gate for free, so
	// a single gate-reachable target suppresses it entirely. A read failure here fails the tick
	// rather than being read as "the frontier is exhausted" — that reading would raise explorer
	// demand off a database hiccup.
	gateReachable, err := anyGateReachable(ctx, reach, targets, book)
	if err != nil {
		return rep, err
	}
	advanceOffGate(ctx, p, playerID, targets, gateReachable, &rep)
	return rep, nil
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
	// staffed names every waypoint where a hull of ours is STANDING — a PARKED
	// placement naming a ship. It is what lets seed staging tell a system we have
	// merely SCREENED from one we actually HOLD.
	//
	// KEYED ON WAYPOINT ALONE, NEVER ON KIND, and that is the whole point of the
	// index rather than a reuse of `state`. The question is "is one of our hulls
	// standing at this counter?", which is exactly what buyerAt asks in the buy
	// queue, and states.go is explicit that it must ignore slot_kind: a
	// probe-selling yard that is also a whitelisted market is slotted MARKET, so
	// the hull standing on a yard is normally recorded under a MARKET row. A
	// kind-filtered read would call the fleet's best staging yards empty.
	//
	// PARKED AND NAMING A HULL, both. A row in any earlier state names a hull
	// that has not arrived — it cannot be bought through — and a PARKED row with
	// no ship is a torn or released row, not a presence. Either read the other
	// way would stage a purchase that can never happen, which is the bug this
	// index exists to prevent.
	staffed map[string]bool
}

// newSlotBook builds the tick's view of the placement ledger. onErrand names the
// hulls a system row already has out on a charting mission, and it is what keeps
// ONE HULL TO ONE ERRAND across ticks — see the parkedSpares filter below.
func newSlotBook(rows []QueuedSlot, onErrand map[string]bool) *slotBook {
	b := &slotBook{
		state:   make(map[slotKey]string, len(rows)),
		wanted:  make(map[string][]QueuedSlot),
		staffed: make(map[string]bool),
	}
	for _, row := range rows {
		b.state[slotKey{row.Waypoint, row.Kind}] = row.State
		if row.State == SlotStateWanted {
			b.wanted[row.System] = append(b.wanted[row.System], row)
		}
		if row.State == SlotStateParked && row.AssignedShip != "" {
			// Recorded for EVERY kind, before the SPARE-only narrowing below.
			b.staffed[row.Waypoint] = true
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

// takeSupplyFor consumes a spare that could serve target — one parked WITHIN
// GATE REACH of it — and reports whether it found one. A match means a seed for
// that target already exists somewhere in the pipeline, so no second one should
// be ordered.
//
// THE REACH TEST HERE AND THE ONE IN stagingYardFor MUST MOVE TOGETHER, and this
// is the failure if they do not. Widen staging alone and tick 1 writes a want at
// a yard two hops from the target; tick 2 rebuilds the book, this test still
// asks for direct adjacency, does not recognise that want as supply, falls
// through to staging — which now DOES reach — finds the first yard taken and
// stages at the NEXT one. A second probe, bought for a target already served,
// every tick until the yards run out. They share gateReach so there is no second
// notion of reach to drift.
//
// The reach test is the whole point. A blanket count of every SPARE row in
// the ledger suppresses demand it cannot possibly serve: a spare parked five
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
//
// BUT A WANT NOTHING CAN FUND IS NOT AN OUTSTANDING REQUEST. That is the second
// half of the mis-staging bug, and the half that made the first half permanent:
// a SPARE want written at a yard we do not hold is refused by the buy queue on
// every tick forever and is retired by nothing, so counting it as supply blocked
// the correct request for the very target it was meant to serve — indefinitely,
// and precisely for the targets that most needed one. Such a row is skipped here
// and left in the pool, because it was never supply to consume.
//
// THE HULL TEST COMES FIRST, AND IT IS A MONEY GUARD. A SPARE row that already
// NAMES a hull is a seed genuinely on order — bought, flying, or parked — and its
// waypoint is frequently not staffed by anything else, so a fundability test
// applied to it would stop counting a hull already on its way and order a SECOND
// one for a target already served. Naming a hull therefore ends the question
// before fundability is ever asked (RULINGS #4: the doubt resolves toward NOT
// spending).
func takeSupplyFor(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	book *slotBook,
	target string,
	reach *gateReach,
	staffed map[string]bool,
) (bool, error) {
	for i, spare := range book.spares {
		within, err := reach.canReach(ctx, spare.System, target)
		if err != nil {
			return false, err
		}
		if !within {
			continue
		}
		if spare.AssignedShip == "" {
			// No hull behind it, so it is only supply if the queue could still buy one
			// for it — and "could buy" now means BOTH halves of what staging requires.
			//
			// THE THIRD CALLER OF ONE RULE. staffedAt alone was the whole definition of
			// fundable when this was written; staging has since learned that a yard must
			// also not be known to sell no probe, and this is the third consumer of that
			// rule. Left on the old half it reads a want at a staffed, probe-less yard as
			// a seed already on order, skips the target, and so the row that blocks the
			// target is the row that can never be filled — the suppression loop closed in
			// 554878e2, reopened because the DEFINITION of fundable moved underneath it.
			//
			// Reached through the trait pass rather than through a mistake: staging
			// deliberately still stages at never-priced yards (that is how the fleet
			// learns), the queue scans one, and the memo records a probe-less answer.
			fundable, err := staffedAt(ctx, p, playerID, book, spare.Waypoint, staffed)
			if err != nil {
				return false, err
			}
			if fundable {
				// DELEGATED, never re-derived: the same readProbeStock the buy queue's
				// skipKnownProbeless and staging's own pass read, so there is no fourth
				// notion of buyable and no second copy of the TTL. A NEVER-PRICED yard
				// stays supply — the queue will scan it and may well buy there, and
				// discounting it would order a duplicate on a guess.
				stock, _, stockErr := readProbeStock(ctx, p.ListingMemo, playerID, spare.Waypoint, time.Now())
				if stockErr != nil {
					return false, stockErr
				}
				fundable = stock != probeStockNone
			}
			if !fundable {
				continue
			}
		}
		book.spares = append(book.spares[:i], book.spares[i+1:]...)
		return true, nil
	}
	return false, nil
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

// staffedYard reports whether a hull of ours is STANDING at this waypoint, and
// therefore whether the buy queue could actually buy through it.
//
// It answers the same question buyerAt answers in the buy queue, from the ledger
// rows this tick has already read — no extra call, and no second definition of
// "we are here". It is deliberately the STRICTER half of buyerAt: that verb will
// also accept a probe the ships table shows docked at the waypoint but which no
// placement row accounts for, and this does not. Missing such a hull only ever
// DELAYS a seed request by a tick or two (the placement machine parks the hull
// and records the row), whereas accepting a presence we cannot prove writes a
// permanent want nothing can fund — so the conservative reading is the safe one.
func (b *slotBook) staffedYard(waypoint string) bool {
	return b.staffed[waypoint]
}

// staffedAt reports whether a hull of ours is STANDING at waypoint, answering
// exactly the question buyerAt answers in the buy queue — and answering it the
// same two ways, in the same order.
//
// ONE PREDICATE, TWO CALLERS. Seed staging must not write a want the buy queue
// will refuse, and takeSupplyFor must not treat such a want as a seed on order.
// Both reduce to "could the queue buy here?", so both ask this, and there is no
// second definition of "we are here" to drift.
//
// The ledger half is free — the tick has already read every slot row — and the
// ships half is consulted only when it misses, because a hull can genuinely be
// standing at a counter before this engine has written a row for it. That is the
// same fallback buyerAt makes, and skipping it would decline yards the queue
// would in fact have funded.
//
// IT STOPS AT DOCKED, deliberately. PurchaseShipCommand will dock a hull it
// finds in orbit, so the purchase itself would tolerate one — but buyerAt is
// what SELECTS the buyer, and it reads DOCKED only. Staging on an orbiting hull
// would therefore write a want the queue still refuses, which is the exact bug
// this predicate exists to prevent, one layer down. Widening buyerAt is a
// separate change with its own blocking-wait question to answer first.
//
// A READ FAILURE PROPAGATES rather than being read as "not here". Fail-closed on
// a per-yard basis would silently stop staging seeds for as long as the ships
// table was unhappy, and silence is the failure mode this whole area has been
// bitten by; the tick is idempotent and re-derived from scratch, so failing it
// loudly costs one cycle and nothing else.
//
// Memoised for the TICK: several targets share a bordering system, and the
// answer cannot change while the tick runs.
func staffedAt(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	book *slotBook,
	waypoint string,
	memo map[string]bool,
) (bool, error) {
	if known, cached := memo[waypoint]; cached {
		return known, nil
	}
	staffed := book.staffedYard(waypoint)
	if !staffed {
		_, found, err := p.Ships.DockedProbeAt(ctx, playerID, waypoint)
		if err != nil {
			return false, fmt.Errorf("failed to look for a hull standing at %q: %w", waypoint, err)
		}
		staffed = found
	}
	memo[waypoint] = staffed
	return staffed, nil
}

// wantedIn returns the system's unfilled placements in the order a finishing
// seed should try to claim them: the system's SHIPYARDS first, and within each
// half the placement the hull is already standing on.
//
// YARD-FIRST IS THE OUTER KEY, and it is what makes expansion compound. A probe
// standing at a system's shipyard is the whole difference between a system we
// merely watch and one that can seed its NEIGHBOURS: stagingYardFor stages a
// seed only at a yard staffedAt says we hold, and buyerAt buys only through a
// hull already standing at the counter. So a system with ten probes spread over
// its markets and none at its yard is a dead end — it can neither extend the
// frontier nor buy its own next probe — while a system with one probe on its
// yard does both. Nothing else in the engine prioritises the yard, so before
// this ordering existed staffing one was coincidental.
//
// It therefore outranks the standing-on preference rather than tying with it.
// That trade is deliberate and it is not close: the cost is ONE intra-system
// flight, which the placement machine was going to make for some other placement
// anyway, and the gain is the system becoming a staging origin at all. The
// standing-on rule is kept as the INNER key, so a hull already berthed on the
// yard still fills it for free and a system with no yard behaves exactly as it
// did before.
//
// yards is the system's probe-selling shipyards. MATCHED ON WAYPOINT, NEVER ON
// KIND, which is the one way to get this wrong: planSlots emits a YARD-kind slot
// only for a yard that is not already a placed market, and in practice every
// probe-selling yard we screen is also a whitelisted market, so the MARKET slot
// wins and there are no YARD-kind rows at all. A `Kind == SlotKindYard` test
// would order an empty set and change nothing. states.go says the same thing as
// a contract: probe presence at a yard is waypoint-wise.
func (b *slotBook) wantedIn(system, standingOn string, yards []string) []QueuedSlot {
	rows := b.wanted[system]
	isYard := make(map[string]bool, len(yards))
	for _, yard := range yards {
		isYard[yard] = true
	}
	// Four passes rather than a sort, so the ledger's own order survives inside
	// each tier and the result is reproducible tick to tick — the same stability
	// the rest of this engine's ordering depends on.
	out := make([]QueuedSlot, 0, len(rows))
	for _, tier := range []struct{ yard, underfoot bool }{
		{true, true}, {true, false}, {false, true}, {false, false},
	} {
		for _, row := range rows {
			if isYard[row.Waypoint] == tier.yard && (row.Waypoint == standingOn) == tier.underfoot {
				out = append(out, row)
			}
		}
	}
	return out
}

// probeYardsIn lists a system's probe-selling shipyards, memoised for the TICK.
//
// One map is shared by every consumer in the tick — the finishing seed's
// placement choice and seed staging both ask about the systems we hold, and they
// overlap heavily — so a system's yards are read at most once however many times
// they are wanted. The read is a local catalog query, never an API call, and the
// answer cannot change while the tick runs.
func probeYardsIn(ctx context.Context, p ExpandPorts, system string, memo map[string][]string) ([]string, error) {
	if yards, cached := memo[system]; cached {
		return yards, nil
	}
	yards, err := p.Yards.ListProbeYards(ctx, system)
	if err != nil {
		return nil, fmt.Errorf("failed to list probe yards in %q: %w", system, err)
	}
	memo[system] = yards
	return yards, nil
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
// propagate from: EVERY system in the ledger, whatever its verdict, plus any
// holding a parked spare (whose neighbours decide which frontier that spare can
// reach).
//
// PROPAGATION IS GATED ON MEASURED ADJACENCY, NOT ON JUDGEMENT, and the gate
// store is what enforces it. That store is populated from a system's jump-gate
// waypoint, so it answers only for gates we have actually charted; a system it
// does not know returns no neighbours and propagates nothing. Nothing here has
// to test for that, because "we have charted the gate" and "the store has rows"
// are the same fact.
//
// This replaces a judged-only rule, whose stated objection was that expanding
// through an unscreened neighbour would flood the ledger — and then the
// screening sweep's API budget — with a galaxy we have no reason to believe is
// worth anything. Both halves have been re-decided:
//
//   - The flood is the goal. Judging needs screening, screening needs charting,
//     and charting is flight-bound, so judged-only advanced the frontier one
//     FULLY-CHARTED RING at a time — chart ~50 waypoints, judge, discover,
//     repeat. Gating on the gate alone advances it at the speed of charting ONE
//     waypoint, which is the difference between a ledger that grows and one that
//     sits at sixteen systems while twelve charting seeds fly.
//   - The API budget is not spent here, and does not grow with the frontier.
//     Marking a neighbour PENDING is a ledger write. The screening sweep that
//     consumes those rows is bounded to screenSweepBatch systems per tick no
//     matter how many are waiting, so a larger frontier lengthens that QUEUE
//     rather than widening its per-tick spend. Both stores this tick reads to
//     expand — the gate adjacency here and the yard catalog in stagingYardFor —
//     are local database reads that cost no request token at all.
//
// Widening the origins widens stagingYardFor's search with it, deliberately: a
// probe yard in a charted-but-unjudged system is a measured fact about where a
// seed can be bought, and the purchase it stages is still funded by the buy
// queue under the same floor and probe cap as every other. More places to stage
// from is the direct cure for a frontier target that no judged system borders.
func readNeighbours(ctx context.Context, p ExpandPorts, systems []ExpandSystem, book *slotBook) (map[string][]string, error) {
	origins := make(map[string]bool, len(systems))
	for _, s := range systems {
		origins[s.System] = true
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

// --- gate reach ---------------------------------------------------------------

// SeedFlightUnbounded means a charting seed's reach is bounded by THE GRAPH, not by a number: the
// search walks until the traversable component is exhausted.
//
// WHY THERE IS NO LONGER A NUMBER. This was MaxSeedFlightHops = 9, and 9 was honestly derived — the
// traversable graph saturated there, so a bound of 10 served no additional target. It went stale in
// under a day. X1-TD22 was discovered at TEN hops, reachable only through X1-KP42 at nine, and it is
// the last system in the fleet with an unmapped jump gate — the only one whose charting can add
// systems at all. A bound tuned to today's furthest system is guaranteed to be wrong tomorrow,
// because charting an unmapped gate is precisely the act that reveals systems beyond it. The bound
// was re-tuning itself into staleness by succeeding.
//
// SO THE BOUND IS THE COMPONENT, AND IT IS SELF-LIMITING. A breadth-first search over stored
// adjacency terminates when it runs out of frontier — a destination in another component is simply
// never found, which is the same refusal the ring bound gave, reached by the graph's own shape
// instead of a guess. It cannot go stale because there is nothing to tune: the answer is "can a seed
// actually get there", which is the question that always mattered.
//
// IT STAYS CHEAP AS THE MAP GROWS because it is bounded by OUR OWN LEDGER, not the universe. The
// walk visits each known system at most once — 57 today, 300 at the owner's target — reading a map
// the tick has already built, with a memoised store fallback for the few systems it does not cover.
// That is linear in the map we hold and it does not grow with anything we have not charted.
//
// WHAT IT COSTS IS TICKS, NOT CREDITS. A gate jump burns no fuel; a crossing is two dispatch steps,
// so TD22 at ten hops is roughly twenty ticks of transit. The honest ceiling is the graph's
// eccentricity from our staffed systems — 10 today, measured — and it grows only as the frontier
// does. That is inherent in charting a distant system at all, not a cost this choice introduces.
//
// SELECTION AND THE ROUTER READ THE SAME RULE, which is the invariant that makes this safe rather
// than merely permissive. A destination past the ROUTER's reach fails silently: nextHopToward names
// no next system, the step errors, and the slot stays IN_TRANSIT still naming a hull that counts
// against the probe cap and never arrives. The adapter's resolver therefore takes its bound from
// this same declaration (see adapters/parkedsensing), so selection can never outrun delivery — and
// with both unbounded-within-the-component, "reachable" means the same thing on both sides by
// construction rather than by two numbers agreeing.
const SeedFlightUnbounded = 0

// gateReach answers "how many gate hops is it from here to there?", bounded by
// MaxWalkRings, from STORED adjacency alone.
//
// WHY IT EXISTS. Seed supply used to require DIRECT adjacency at both gates —
// stagingYardFor would only stage at a yard in a system BORDERING the target,
// and takeReachableSpare would only claim a spare parked in one. Gate
// connectivity is sparse, so that exhausted almost immediately: measured on the
// live fleet, 33 unseeded systems carried uncharted waypoints and exactly ONE
// was a direct neighbour of a system we occupied. Seven are within MaxWalkRings.
// The frontier had run out of ring, and no amount of money, hulls or per-tick
// budget could buy another one.
//
// THE BOUND IS THE WALK'S, NOT A PREFERENCE. A seed further out than
// MaxWalkRings is not merely expensive, it is UNROUTABLE: the adapter's
// next-hop search gives up at the same ring, so the errand's every step fails,
// the hull holds probe-cap headroom, and it charts nothing — strictly worse
// than never dispatching it. Reading the bound from the same declaration the
// walk reads is what keeps the two from drifting into handing out that stall.
//
// FORWARD, AND THAT IS A CORRECTNESS PROPERTY RATHER THAN A DETAIL. The search
// follows Neighbours(x) in the same direction the walk traverses it. The stored
// graph is genuinely asymmetric — measured live, 617 of 5463 edges have no
// reverse row, because a gate charted from one end names a system whose own gate
// we have not charted yet — so a search that assumed symmetry would report
// routes the walk cannot resolve, and every one of them would strand a probe.
// Physical gates are two-way; our KNOWLEDGE of them is not, and this reads the
// knowledge.
//
// PURE STORE READS, and no more of them than the tick already makes. Neighbours
// is a store read by contract, never a fetch-through resolver, so widening reach
// spends no API budget at all. Each origin is walked at most once per tick and
// memoised, so a second target in the same neighbourhood is free — which is what
// keeps the cost from growing with the frontier exactly as the frontier
// succeeds.
type gateReach struct {
	// maxHops is THIS walker's reach, per-instance rather than a package constant
	// because the two engines that walk this graph ask different questions. Seed
	// staging asks "how far may a CHARTING SEED be flown" (MaxSeedFlightHops); the
	// foothold pass asks "how far may a surplus SCANNING HULL be drawn to fill a
	// placement" (MaxWalkRings), which is deliberately much shorter. Sharing one
	// number would mean widening the seed's reach silently lengthened every
	// placement draw too — which it did, until this field existed.
	maxHops int
	// gates is the gate-adjacency STORE read, narrowed from the ports struct it
	// used to hold so this walker can serve any caller that has one — the buy
	// queue's foothold path reaches for it through BuyPorts.Gates. Narrowing is
	// what makes reuse possible without a second traversal of the same graph.
	gates GateNeighbours
	// known is the tick's neighbour map, already read by readNeighbours. It
	// covers every system in the ledger, which is nearly everything the search
	// touches.
	known map[string][]string
	// fetched memoises the systems `known` does not cover. It is kept SEPARATE
	// rather than written back into the tick's map because markFrontier iterates
	// that map to decide what to record as PENDING, and quietly growing it here
	// would change which systems this tick claims to have discovered.
	fetched map[string][]string
	// hopsFrom memoises one BFS per origin: system -> hops, for the systems
	// within maxHops. The origin itself is absent, so any entry present is
	// both reachable AND at least one hop away.
	hopsFrom map[string]map[string]int
}

func newGateReach(gates GateNeighbours, neighbours map[string][]string, maxHops int) *gateReach {
	return &gateReach{
		maxHops:  maxHops,
		gates:    gates,
		known:    neighbours,
		fetched:  map[string][]string{},
		hopsFrom: map[string]map[string]int{},
	}
}

// origins lists the systems the tick may propagate from, in symbol order — the
// same set and the same order readNeighbours built.
func (r *gateReach) origins() []string { return sortedKeys(r.known) }

// adjacent reads one system's gate neighbours, from the tick's map where it can
// and the gate store where it cannot.
//
// The fallback is load-bearing rather than defensive: a two-hop search passes
// THROUGH intermediate systems, and an intermediate only entered the ledger when
// this tick's own markFrontier recorded it — after the neighbour map was built.
// Without the fallback the second ring would be empty on exactly the ticks that
// first open a new neighbourhood.
func (r *gateReach) adjacent(ctx context.Context, system string) ([]string, error) {
	if known, ok := r.known[system]; ok {
		return known, nil
	}
	if cached, ok := r.fetched[system]; ok {
		return cached, nil
	}
	adjacent, err := r.gates.Neighbours(ctx, system)
	if err != nil {
		return nil, fmt.Errorf("failed to read gate neighbours of %q: %w", system, err)
	}
	r.fetched[system] = adjacent
	return adjacent, nil
}

// from returns every system reachable from origin within MaxWalkRings, mapped to
// the number of hops it takes. Breadth-first, so the recorded hop count is the
// SHORTEST one — which is what makes "prefer the nearer" mean anything.
func (r *gateReach) from(ctx context.Context, origin string) (map[string]int, error) {
	if cached, ok := r.hopsFrom[origin]; ok {
		return cached, nil
	}
	hops := map[string]int{}
	seen := map[string]bool{origin: true}
	frontier := []string{origin}

	// A NON-POSITIVE maxHops means "until the component is exhausted": the frontier
	// empties on its own when there is nowhere further to go, which is the same
	// refusal a ring bound gave, reached from the graph rather than from a guess.
	for ring := 1; (r.maxHops <= 0 || ring <= r.maxHops) && len(frontier) > 0; ring++ {
		var next []string
		for _, system := range frontier {
			adjacent, err := r.adjacent(ctx, system)
			if err != nil {
				// Propagated, never swallowed. An empty reach read permissively
				// is indistinguishable from a genuinely isolated system, and
				// this is the read that decides whether a hull is dispatched at
				// all. The tick is idempotent, so failing loudly costs a cycle.
				return nil, err
			}
			for _, neighbour := range adjacent {
				if neighbour == "" || seen[neighbour] {
					continue
				}
				seen[neighbour] = true
				hops[neighbour] = ring
				next = append(next, neighbour)
			}
		}
		sort.Strings(next) // deterministic within a ring
		frontier = next
	}
	r.hopsFrom[origin] = hops
	return hops, nil
}

// hops reports how far target is from origin, and whether it is within reach at
// all. A system that is not reachable — or IS origin — reports false.
func (r *gateReach) hops(ctx context.Context, origin, target string) (int, bool, error) {
	reachable, err := r.from(ctx, origin)
	if err != nil {
		return 0, false, err
	}
	distance, within := reachable[target]
	return distance, within, nil
}

// canReach reports whether a hull in origin could be walked to target.
func (r *gateReach) canReach(ctx context.Context, origin, target string) (bool, error) {
	_, within, err := r.hops(ctx, origin, target)
	return within, err
}

// beyondReach sorts after every reachable distance for THIS walker, so an
// unreachable target keeps its place at the back of the queue rather than
// jumping it. Derived from the walker's own bound, not a package constant, for
// the same reason maxHops is.
func (r *gateReach) beyondReach() int {
	if r.maxHops <= 0 {
		// Unbounded: no real hop count can reach this, and it is far below overflow.
		return math.MaxInt32
	}
	return r.maxHops + 1
}

// orderByReach puts the CHEAP frontier first: targets nearest the systems we
// actually hold, since a one-hop errand is one flight and a two-hop errand two,
// and the probe is held for the whole of it.
//
// IT IS A RE-SORT, NOT A REPLACEMENT. seedlessTargets' deepest-dark-first order
// is preserved WITHIN each ring, so the rule it encodes — resolve the biggest
// known unknown soonest — still decides between targets that cost the same to
// reach. Distance only outranks it across rings, where the comparison is
// genuinely between different prices rather than different prizes.
//
// Distance is measured from the systems a seed could actually set out from: the
// waypoints where a hull of ours is standing. That is the same index staffedAt
// reads, so "the frontier we hold" means one thing here and at the yard. With
// nothing held — a cold fleet — every target measures the same and the order is
// left exactly as it was.
func orderByReach(
	ctx context.Context,
	reach *gateReach,
	targets []ExpandSystem,
	book *slotBook,
) ([]ExpandSystem, error) {
	held := heldSystems(book)
	if len(held) == 0 || len(targets) < 2 {
		return targets, nil
	}

	distance := make(map[string]int, len(targets))
	for _, target := range targets {
		nearest := reach.beyondReach()
		for _, origin := range held {
			hops, within, err := reach.hops(ctx, origin, target.System)
			if err != nil {
				return nil, err
			}
			if within && hops < nearest {
				nearest = hops
			}
		}
		distance[target.System] = nearest
	}

	ordered := append([]ExpandSystem(nil), targets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return distance[ordered[i].System] < distance[ordered[j].System]
	})
	return ordered, nil
}

// orderByGateMapping puts the targets whose gate adjacency we have NEVER READ at the front.
//
// IT IS THE ONLY PROPERTY THAT GROWS THE LEDGER. Charting a system whose gate we already hold rows
// for fills in its markets — real income, and worth doing — but the map stays the size it was: its
// neighbours are already recorded and already chased. A system with no rows is the only kind that
// can name somewhere we do not hold, and markFrontier turns exactly those names into the PENDING
// rows the seed machinery works through.
//
// MEASURED LIVE: of 21 unseeded targets, 2 had an unmapped gate and they ranked TWENTIETH and
// TWENTY-FIRST. The mechanism was distance — orderByReach makes hop count the primary key and both
// sat nine hops out — so the two systems that could actually extend the map sorted behind every
// nearer one and never got a seed. One of them carried the second-deepest uncharted count in the set
// and it still came last.
//
// A WEIGHT, NOT A FILTER, and that distinction is load-bearing rather than stylistic. This reorders
// and never truncates: with no unmapped-gate target in play the queue is exactly the order
// orderByReach produced, and once the unmapped ones are covered the mapped ones are served
// unchanged. A filter would trade the income side away for the growth side; the fleet needs both.
//
// STABLE, so everything already decided survives inside each tier: distance first, then the deepest
// dark, then symbol. A deep unmapped-gate target still beats a shallow one.
//
// A READ FAILURE FAILS THE TICK rather than defaulting either way. Read as "mapped" it would demote
// genuine frontier territory to the back of the queue and the fleet would quietly stop growing; read
// as "unmapped" it would promote every ordinary target at once. The tick is idempotent and
// re-derived from scratch, so failing loudly costs one cycle.
func orderByGateMapping(ctx context.Context, p ExpandPorts, targets []ExpandSystem) ([]ExpandSystem, error) {
	if len(targets) < 2 {
		return targets, nil
	}
	// RESOLVED ONCE PER TARGET, UP FRONT, and never from inside the comparator. It is a pure store
	// read, but sort calls its less function O(n log n) times — asking the store there would turn a
	// linear pass into a superlinear one that grows with the frontier exactly as the frontier
	// succeeds. (A same-system short-circuit lived here briefly; it was removed because targets are
	// distinct systems by construction, so it could never fire — dead code with a plausible-sounding
	// rationale is worse than none.)
	unmapped := make(map[string]bool, len(targets))
	for _, target := range targets {
		mapped, err := p.Gates.Mapped(ctx, target.System)
		if err != nil {
			return nil, fmt.Errorf("failed to read whether the gate of %q has been mapped: %w", target.System, err)
		}
		unmapped[target.System] = !mapped
	}

	ordered := append([]ExpandSystem(nil), targets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return unmapped[ordered[i].System] && !unmapped[ordered[j].System]
	})
	return ordered, nil
}

// heldSystems names the systems a seed could set out from — those holding a
// waypoint one of our hulls is standing at — in symbol order.
func heldSystems(book *slotBook) []string {
	systems := make(map[string]bool, len(book.staffed))
	for waypoint := range book.staffed {
		if system := shared.ExtractSystemSymbol(waypoint); system != "" {
			systems[system] = true
		}
	}
	out := make([]string, 0, len(systems))
	for system := range systems {
		out = append(out, system)
	}
	sort.Strings(out)
	return out
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
// A spare is only usable for a target it can actually REACH: a seed walks to its
// target one gate hop per tick, so the spare must be sitting within MaxWalkRings
// of it. A spare parked further out is left where it is for the placement
// machinery to re-task — an errand it could never complete would hold the hull
// out of the probe cap while charting nothing.
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
	reach *gateReach,
	rep *ExpandReport,
) error {
	for _, target := range targets {
		if covered[target.System] {
			continue
		}
		spare, found, err := takeReachableSpare(ctx, reach, book, target.System)
		if err != nil {
			return err
		}
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

// takeReachableSpare removes and returns the parked spare NEAREST to target,
// among those within gate reach of it.
//
// Nearest rather than first: a spare one hop out reaches the target in a single
// crossing, one two hops out takes two, and both the flying time and the risk of
// the walk being interrupted scale with it. The ledger's own order breaks a tie,
// so the choice stays reproducible tick to tick.
func takeReachableSpare(
	ctx context.Context,
	reach *gateReach,
	book *slotBook,
	target string,
) (QueuedSlot, bool, error) {
	best, nearest := -1, reach.beyondReach()
	for i, spare := range book.parkedSpares {
		hops, within, err := reach.hops(ctx, spare.System, target)
		if err != nil {
			return QueuedSlot{}, false, err
		}
		if !within || hops >= nearest {
			continue
		}
		best, nearest = i, hops
	}
	if best < 0 {
		return QueuedSlot{}, false, nil
	}
	spare := book.parkedSpares[best]
	book.parkedSpares = append(book.parkedSpares[:best], book.parkedSpares[best+1:]...)
	return spare, true, nil
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
	reach *gateReach,
	probeYards map[string][]string,
	rep *ExpandReport,
) error {
	// Yards are resolved once per origin and reused: several frontier targets
	// usually sit within reach of the same system of ours. The memo arrives from
	// the tick rather than being made here, so a system whose yards the finishing
	// seed already read is not read again.
	// Whether a hull of ours stands at a waypoint, memoised for the same reason
	// and with the same lifetime: several targets border the same system of
	// ours, and the answer cannot change while the tick runs. Shared by BOTH
	// consumers below so supply and staging can never disagree about it.
	staffed := map[string]bool{}
	// What the stored listings say about each candidate yard, memoised for the same reason and with
	// the same lifetime: several targets share a yard, and the answer cannot change while the tick runs.
	listings := map[string]probeStock{}
	for _, target := range targets {
		if rep.Actions >= MaxExpansionActions {
			return nil
		}
		if covered[target.System] {
			continue
		}
		supplied, err := takeSupplyFor(ctx, p, playerID, book, target.System, reach, staffed)
		if err != nil {
			return err
		}
		if supplied {
			// A seed for this target is already somewhere in the pipeline. That
			// is what keeps a frontier several ticks away from being re-ordered
			// on every one of them.
			continue
		}

		yard, system, err := stagingYardFor(ctx, p, playerID, target.System, reach, book, probeYards, staffed, listings)
		if err != nil {
			return err
		}
		if yard == "" {
			// Nowhere to stage a purchase this tick: no system of ours within
			// gate reach holds a probe-selling yard that is both STAFFED by one
			// of our hulls and free of a SPARE placement. Expected while the map
			// is thin, and
			// it costs nothing — the target simply waits, and takes nothing from
			// the targets we CAN reach on its way past.
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
// yard, in any of our own systems FROM WHICH THE TARGET IS ROUTABLE, that carries
// no SPARE placement of its own.
//
// WHERE WE CAN TRANSACT AND WHAT IS NEAR THE TARGET ARE TWO DIFFERENT QUESTIONS,
// and this used to weld them: a yard had to be staffed AND sit within the
// placement walk's couple of rings OF THE TARGET. The second half produced a
// structural dead zone rather than a mere inefficiency — a target whose in-reach
// systems all happen to lack a shipyard could never be seeded, however many
// staffed yards the fleet owned elsewhere, and because a system with no shipyard
// can never itself be staffed the dead zone propagated outward. Measured live it
// left 18 of 23 unseeded targets unservable, including the only two systems whose
// charting could add anything to the ledger.
//
// SO THE COUPLING IS GONE AND THE MONEY CONSTRAINT IS NOT. Eligibility is
// routability — can a seed bought here actually fly to that target, bounded by
// MaxSeedFlightHops, which is the bound the seed's own walk resolves under — and
// candidates are ordered NEAREST FIRST, because a shorter flight is fewer ticks
// holding probe-cap headroom. The staffedAt test below is untouched: the buy
// queue can only transact where a hull of ours stands, so a nearer yard we do not
// hold is still skipped for a further one we do.
//
// "OUR OWN" IS ENFORCED, not merely intended. It used to be neither: the origins
// this walks are every system carrying a screening VERDICT, and a verdict says
// "screened and worth trading with", never "we have a hull there". So a seed was
// happily staged at a yard in a system we had never visited, the buy queue —
// which only buys where one of our hulls is already at the counter — refused it
// on that tick and every tick after, and the target never got eyes. Measured on
// the live fleet, every outstanding SPARE want sat in a system with zero probes.
//
// The fix is the staffedYard test below, and it belongs HERE rather than in the
// origin set. `neighbours` is shared with frontier propagation, which needs
// every system whose gate adjacency we have measured precisely BECAUSE it has no
// verdict yet; narrowing that map to occupied systems would silently collapse
// the frontier back to one fully-charted ring at a time. Occupancy is a
// requirement of STAGING A PURCHASE, not of believing a gate edge, so it is
// applied at the yard and the map is left whole.
//
// Writing nothing is strictly better than writing an unfundable want: nothing
// retires a WANTED SPARE row, and through takeSupplyFor a stale one goes on
// suppressing the correct request that could be made once a bordering system
// finally is occupied. One bad row poisons its target indefinitely.
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
	playerID int,
	target string,
	reach *gateReach,
	book *slotBook,
	yardsByOrigin map[string][]string,
	staffed map[string]bool,
	listings map[string]probeStock,
) (string, string, error) {
	origins, err := originsWithinReach(ctx, reach, reach.origins(), target)
	if err != nil {
		return "", "", err
	}
	// TWO PASSES, EVIDENCE FIRST. Within each pass the origins keep the nearest-first
	// order above, so this adds a condition to the routability selection rather than
	// replacing it. See stagedProbeStockAccepts for what each pass admits.
	for _, wantEvidence := range []bool{true, false} {
		yard, origin, err := stagingYardPass(ctx, p, playerID, origins, book, yardsByOrigin, staffed, listings, wantEvidence)
		if err != nil || yard != "" {
			return yard, origin, err
		}
	}
	return "", "", nil
}

// stagingYardPass walks the reachable origins once, admitting only yards whose stored listings match
// this pass — evidenced first, then the never-priced trait guesses.
func stagingYardPass(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	origins []string,
	book *slotBook,
	yardsByOrigin map[string][]string,
	staffed map[string]bool,
	listings map[string]probeStock,
	wantEvidence bool,
) (string, string, error) {
	for _, origin := range origins {
		yards, err := probeYardsIn(ctx, p, origin, yardsByOrigin)
		if err != nil {
			return "", "", err
		}
		for _, yard := range yards {
			// EVIDENCE THAT THIS COUNTER SELLS THE HULL WE NEED. ListProbeYards falls
			// back to every shipyard-TRAIT waypoint when a system holds no SHIP_PROBE
			// row, so a yard we have already priced and found probe-less comes back
			// looking exactly like one we have never looked at. Staging one of those
			// writes a want the buy queue scans, learns nothing from, and then
			// correctly refuses for the memo's whole TTL — measured live, 14 of the
			// outstanding wants sat on such yards while 8 evidenced ones existed
			// elsewhere in the fleet.
			stock, err := stagedProbeStock(ctx, p, playerID, yard, listings)
			if err != nil {
				return "", "", err
			}
			if !stagedProbeStockAccepts(stock, wantEvidence) {
				continue
			}
			// A hull of ours must be STANDING at this counter. Without it the
			// buy queue cannot fund the want at all — it only ever buys where one
			// of our hulls is already docked — so staging here would write a row
			// that is refused every tick forever. See staffedAt, which is the
			// same predicate the supply test above applies.
			manned, err := staffedAt(ctx, p, playerID, book, yard, staffed)
			if err != nil {
				return "", "", err
			}
			if !manned {
				continue
			}
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

// stagedProbeStock reads what the stored listings say about one yard, memoised for the TICK.
//
// The same yard is offered for several targets in a neighbourhood, and the answer cannot change
// while the tick runs, so a re-read per target would scale with the frontier exactly as the frontier
// succeeds. It is a local store read either way — zero API calls — but the memo is what keeps it one
// read per yard rather than one per (yard, target) pair.
//
// A READ FAILURE PROPAGATES. Staging onto a yard we could not read is how the unfundable want gets
// written in the first place; the tick is idempotent and re-derived, so failing loudly costs a cycle.
func stagedProbeStock(ctx context.Context, p ExpandPorts, playerID int, yard string, memo map[string]probeStock) (probeStock, error) {
	if stock, cached := memo[yard]; cached {
		return stock, nil
	}
	stock, _, err := readProbeStock(ctx, p.ListingMemo, playerID, yard, time.Now())
	if err != nil {
		return probeStockUnread, err
	}
	memo[yard] = stock
	return stock, nil
}

// stagedProbeStockAccepts reports whether a yard belongs to this staging pass.
//
// THREE ANSWERS, TWO PASSES, AND ONE YARD ADMITTED BY NEITHER:
//
//   - SELLS — evidence. Taken on the FIRST pass, ahead of any trait guess however near, because a
//     want written here can actually be funded.
//   - UNREAD — never priced. Taken on the SECOND pass only. It is a guess, but it is also how the
//     fleet LEARNS where probes are sold, so ranking it last must not mean removing it.
//   - NONE — priced, and it sells no probe. Admitted by NEITHER pass. This is the standing fact the
//     buy queue's memo already refuses on; staging onto it writes a want that is scanned, learns
//     nothing, and is refused for the whole TTL. (A STALE reading is not this case — readProbeStock
//     degrades it to UNREAD, so a restocked counter is reconsidered.)
func stagedProbeStockAccepts(stock probeStock, wantEvidence bool) bool {
	if wantEvidence {
		return stock == probeStockSells
	}
	return stock == probeStockUnread
}

// originsWithinReach filters candidates down to the systems a hull could be
// flown FROM to arrive at target, NEAREST RING FIRST and in symbol order inside
// each ring.
//
// THE DIRECTION IS THE POINT, and it is why every candidate is tested with its
// OWN forward walk rather than one walk out of the target. Those two agree only
// on a symmetric graph, and the gate map is not one — measured live, 624 of 5,488
// edges (11.4%) have no reverse row. Walking forward out of the target answers
// "where could a hull AT the target go", which for a one-way edge into the target
// is the exact opposite of the question, and a hull staged on that answer is
// dispatched onto a route nextHopToward cannot resolve: it sits IN_TRANSIT
// forever, holding probe-cap headroom and doing no work.
//
// Testing each candidate forward — the same direction the placement machine will
// actually traverse — means a system is offered only if the walk it will really
// fly exists. That is the discipline sp-9fdc258d established for the seed reach,
// applied to the same graph by the same walker.
//
// CALLERS SUPPLY THEIR OWN CANDIDATE SET, which keeps the cost proportional to
// the work: seed staging passes the tick's neighbour map (every system whose
// adjacency we have measured), while the foothold path passes only the systems
// actually holding a surplus hull. Symbol order is kept as the tie-break so two
// origins the same distance out are chosen between reproducibly, tick after tick.
func originsWithinReach(ctx context.Context, reach *gateReach, candidates []string, target string) ([]string, error) {
	type candidate struct {
		system string
		hops   int
	}
	var found []candidate
	for _, origin := range candidates {
		hops, within, err := reach.hops(ctx, origin, target)
		if err != nil {
			return nil, err
		}
		if within {
			found = append(found, candidate{origin, hops})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].hops < found[j].hops })

	out := make([]string, 0, len(found))
	for _, c := range found {
		out = append(out, c.system)
	}
	return out, nil
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
	reach *gateReach,
	probeYards map[string][]string,
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
		acted, err := advanceSeed(ctx, p, playerID, k, s, targets, covered, book, reach, probeYards, rep)
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
	reach *gateReach,
	probeYards map[string][]string,
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
	return chartSeed(ctx, p, playerID, k, s, pos, targets, covered, book, reach, probeYards, rep)
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
	reach *gateReach,
	probeYards map[string][]string,
	rep *ExpandReport,
) (bool, error) {
	remaining, err := p.Uncharted.UnchartedWaypoints(ctx, s.System)
	if err != nil {
		return false, nil // unreadable: leave the tour alone and retry next tick
	}
	if len(remaining) == 0 {
		return finishTour(ctx, p, playerID, s, pos, targets, covered, book, reach, probeYards, rep)
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
//     ever place. The system's SHIPYARD is taken ahead of any market, because
//     that is the placement which turns the system into a staging origin and
//     lets it buy its own next probe; see wantedIn;
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
	reach *gateReach,
	probeYards map[string][]string,
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
		// A catalog that cannot answer stops the tick rather than falling back to
		// an unordered fill. Reading the failure as "no yards here" would silently
		// hand the hull to a market and leave the system unable to seed its
		// neighbours — for good, since the seed is consumed by the placement it
		// takes and no later tick revisits the choice. The tick is idempotent, so
		// failing loudly costs one cycle.
		yards, err := probeYardsIn(ctx, p, current, probeYards)
		if err != nil {
			return true, err
		}
		filled, err := fillPlacement(ctx, p, playerID, s, current, book.wantedIn(current, pos.Waypoint, yards), book, rep)
		if err != nil || filled {
			return true, err
		}
	}

	retargeted, err := retargetSeed(ctx, p, playerID, s, current, targets, covered, reach, rep)
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
// Reachability is MaxWalkRings gate hops, because that is what a dispatched seed
// now executes — the errand is re-stamped onto the new target and the seed walks
// there a hop per tick, exactly as it walked to this one. It was one hop while
// JumpTo was single-hop by construction, and leaving it there afterwards would
// have made this the one place in the engine that refused reach the rest of it
// grants: a finished hull standing two hops from a dark system would be parked
// as a spare and a FRESH probe bought to cover the very target it was already
// next to.
//
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
	reach *gateReach,
	rep *ExpandReport,
) (bool, error) {
	for _, target := range targets {
		if covered[target.System] || target.System == s.System {
			continue
		}
		within, err := reach.canReach(ctx, current, target.System)
		if err != nil {
			return false, err
		}
		if !within {
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

// neighboursOf's job — reading a system's gate neighbours from the tick's map
// and falling back to the store for one the map does not cover — is now
// gateReach.adjacent, which does the same thing and memoises the fallback. A
// tour still usually ends in a system that was PENDING when the map was built,
// so the fallback is as load-bearing as it ever was; it just lives beside the
// search that needs it.

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

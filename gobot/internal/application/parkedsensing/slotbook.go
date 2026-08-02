package parkedsensing

import (
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// slotKey addresses ONE placement row, and it mirrors the ledger's primary key
// exactly. A waypoint on its own stopped being an address the moment a
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
	// It is NOT what keeps a write SAFE — the ledger's per-column
	// ownership is what prevents a declaration reassigning a hull.
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

// occupied reports whether a waypoint already carries a placement row OF THIS
// KIND.
//
// The kind is the whole question. A probe-selling yard is very often
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

// heldSystems names the systems a seed could set out from — those holding a
// waypoint one of our hulls is standing at — in symbol order.
func (b *slotBook) heldSystems() []string {
	systems := make(map[string]bool, len(b.staffed))
	for waypoint := range b.staffed {
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

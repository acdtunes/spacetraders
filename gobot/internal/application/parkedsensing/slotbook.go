package parkedsensing

import (
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// slotKey addresses ONE placement row and mirrors the ledger's primary key exactly.
// A waypoint on its own is not an address: one yard can be scanning as a MARKET
// placement and staging a seed as a SPARE at the same time, and a waypoint-keyed
// book collapses the two into one entry.
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
	// what keeps a write MEANINGFUL — a declaration aimed at a placement already
	// there has nothing to say — and NOT what keeps it SAFE: the ledger's
	// per-column ownership is what prevents a declaration reassigning a hull.
	// KEYED ON THE PAIR, because the question a caller asks is "is there a
	// placement OF MY KIND here", not "is something here".
	state map[slotKey]string
	// wanted lists the unfilled placements in each system.
	wanted map[string][]QueuedSlot
	// spares lists every SPARE placement in any state — the seed SUPPLY. A spare
	// still being bought or flown is a seed already on order, so it suppresses a
	// duplicate request; parkedSpares is the subset claimable as an errand now.
	// Both pools are consumed as the tick allocates them, so one spare answers at
	// most one target WITHIN a tick; across ticks they are rebuilt from the
	// ledger, and what holds the invariant there is newSlotBook's onErrand filter,
	// which keeps a hull already out on a mission out of parkedSpares entirely.
	spares       []QueuedSlot
	parkedSpares []QueuedSlot
	// staffed names every waypoint where a hull of ours is STANDING — a PARKED
	// placement naming a ship. It is what lets seed staging tell a system we have
	// merely SCREENED from one we actually HOLD.
	//
	// KEYED ON WAYPOINT ALONE, NEVER ON KIND, which is why it is not a reuse of
	// `state`: states.go requires presence to ignore slot_kind, because a
	// probe-selling yard that is also a whitelisted market is slotted MARKET and a
	// kind-filtered read would call the fleet's best staging yards empty. PARKED
	// AND NAMING A HULL, both — an earlier state names a hull that has not arrived
	// and cannot be bought through, and a PARKED row with no ship is a torn row,
	// so either read the other way stages a purchase that can never happen.
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
		// The row stays in the SUPPLY pool even when the hull is away: supply
		// answers "is a seed already on order for this neighbourhood?", and a
		// stale yes only ever suppresses a purchase — the safe direction.
		b.spares = append(b.spares, row)
		// But it is NOT claimable. A placement row naming a hull that a system row
		// already has on an errand is a hull the ledger has lost track of, not a
		// spare standing by, and claiming it stamps a SECOND mission on a probe
		// that can only fly one. The row outliving the claim is NORMAL: the claim
		// stamps the errand first and releases the row second (the money-safe
		// order — it over-counts and buys FEWER probes), and probe adoption reads
		// placement rows without the seed columns, so a hull whose row was just
		// deleted still looks like an unrecorded probe standing at a waypoint and
		// is re-parked every tick until it departs. Without this filter that is a
		// loop: re-park, re-claim, one hull stamped onto system after system while
		// the errands it holds mark those systems covered.
		if row.State == SlotStateParked && row.AssignedShip != "" && !onErrand[row.AssignedShip] {
			b.parkedSpares = append(b.parkedSpares, row)
		}
	}
	return b
}

// hullsOnErrand indexes the hulls that system rows already have out charting.
//
// Keyed on the hull rather than the system because that is the invariant being
// protected: a system may be re-targeted, but a probe cannot be in two places. DONE
// is deliberately absent — hasActiveSeed treats a finished errand as over, and a
// hull whose mission ended is a spare again.
func hullsOnErrand(systems []ExpandSystem) map[string]bool {
	hulls := make(map[string]bool, len(systems))
	for _, s := range systems {
		if hasActiveSeed(s) {
			hulls[s.SeedShip] = true
		}
	}
	return hulls
}

// occupied reports whether a waypoint already carries a placement row OF THIS KIND.
//
// The kind is the whole question. A probe-selling yard is very often already a
// parked MARKET placement, and a waypoint-only test would make it permanently
// ineligible to stage a SPARE, so requestSeeds would find no free yard on any tick.
// A scanning yard and a staging yard are two claims on one waypoint, not a conflict.
func (b *slotBook) occupied(waypoint, kind string) bool {
	_, held := b.state[slotKey{waypoint, kind}]
	return held
}

// staffedYard reports whether a hull of ours is STANDING at this waypoint, and
// therefore whether the buy queue could actually buy through it. It answers
// buyerAt's question from the ledger rows this tick has already read — no extra
// call, no second definition of "we are here" — and is deliberately the STRICTER
// half of buyerAt, which also accepts a probe the ships table shows docked but no
// placement row accounts for. Missing such a hull only DELAYS a seed request by a
// tick or two, whereas accepting a presence we cannot prove writes a permanent want
// nothing can fund.
func (b *slotBook) staffedYard(waypoint string) bool {
	return b.staffed[waypoint]
}

// wantedIn returns the system's unfilled placements in the order a finishing seed
// should try to claim them: the system's SHIPYARDS first, and within each half the
// placement the hull is already standing on.
//
// YARD-FIRST IS THE OUTER KEY, and it is what makes expansion compound. A probe
// standing at a system's shipyard is the difference between a system we merely watch
// and one that can seed its NEIGHBOURS: stagingYardFor stages a seed only at a yard
// staffedAt says we hold, and buyerAt buys only through a hull already standing at
// the counter, so a system with ten probes spread over its markets and none at its
// yard can neither extend the frontier nor buy its own next probe. Nothing else in
// the engine prioritises the yard. The standing-on rule is the INNER key, so a hull
// already berthed on the yard still fills it for free.
//
// yards is the system's probe-selling shipyards, MATCHED ON WAYPOINT AND NEVER ON
// KIND, which is the one way to get this wrong: planSlots emits a YARD-kind slot
// only for a yard that is not already a placed market, and every probe-selling yard
// we screen is also a whitelisted market, so a `Kind == SlotKindYard` test would
// order an empty set. states.go says the same as a contract: probe presence at a
// yard is waypoint-wise.
func (b *slotBook) wantedIn(system, standingOn string, yards []string) []QueuedSlot {
	rows := b.wanted[system]
	isYard := make(map[string]bool, len(yards))
	for _, yard := range yards {
		isYard[yard] = true
	}
	// Four passes rather than a sort, so the ledger's own order survives inside
	// each tier and the result is reproducible tick to tick.
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

// addSpare records a SPARE placement written by this tick. It joins the supply pool
// so a second target in the same neighbourhood does not order another, and it
// writes the SPARE half of the waypoint only: a MARKET placement standing at the
// same yard is untouched, still occupied, and still scanning.
func (b *slotBook) addSpare(system, waypoint, state string) {
	b.state[slotKey{waypoint, SlotKindSpare}] = state
	b.spares = append(b.spares, QueuedSlot{
		Waypoint: waypoint, System: system, Kind: SlotKindSpare, State: state,
	})
}

// dropSpare records a SPARE placement this tick handed to a mission: the hull
// belongs to the errand now, so the row is gone and the supply it represented is
// spent. Only the SPARE half is dropped, mirroring the kind-scoped DeleteSlot this
// shadows — forgetting the whole waypoint would let the tick's later writes
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

package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
)

// gateread.go reads the jump gate of every in-scope system whose adjacency we do not yet hold.
//
// WHAT IT FIXES. The fleet believed it was sealed inside a pocket of 57 gate-reachable systems with
// all 53 exits under construction. It was not. X1-TD22 was the single in-scope system whose jump gate
// had never been read, its gate was built and passable, and beyond it lay 33 systems we had never
// heard of — seven of them previously written off as "unreachable, behind the wall". Nothing in the
// engine ever asked X1-TD22 what it connected to.
//
// WHY NOTHING ASKED. Every gate-related consideration in this engine ran over seedlessTargets, whose
// membership rule is `(UnchartedCount > 0 || !CatalogKnown) && !hasActiveSeed`. orderByGateMapping is
// the only caller of Gates.Mapped and it iterates that set, so a system dropped out of the gate
// question entirely once EITHER
//
//   - a seed was dispatched to it (hasActiveSeed), or
//   - its waypoint catalogue was swept and fully charted.
//
// X1-TD22 hit the first: a probe had been sent hours earlier and was still in flight several hops
// away. From the moment that errand was stamped the engine stopped asking anything about the system's
// gate and simply waited for the hull to arrive. An entire region sat behind one in-flight probe.
//
// THE MISS UNDERNEATH BOTH. A CHARTED jump gate is readable from the API WITH NO SHIP PRESENT AT ALL
// — gategraph.ChartPresentGate says it exactly: CreateChart "makes the gate GetJumpGate-readable
// forever without a ship present". Waiting for a hull to reach a system before learning where it
// connects was never necessary for a charted gate. It is necessary only for an UNCHARTED one, and
// that case announces itself (see below) rather than needing to be guessed at.
//
// SO THIS PASS ASKS DIRECTLY, and its eligibility rule is deliberately blind to everything the seed
// machinery cares about: seed state, uncharted count, catalogue state, verdict and hull presence are
// all irrelevant to "do we know where this system connects?". The only question is whether the store
// holds adjacency for it.
//
// IT IS RECURSIVE BY CONSTRUCTION AND HOLDS NOTHING BETWEEN TICKS (RULINGS #2). A read persists the
// system's edges; the next tick's readNeighbours sees them, markFrontier records the newly-named
// neighbours as PENDING sensing rows, and because this pass takes its scope from the map markFrontier
// has just grown, those neighbours are candidates on that same tick. Nothing is remembered — the
// whole chain is re-derived from the store every tick, so a restart mid-region resumes it for free.

// MaxGateReads bounds how many jump gates this pass may READ LIVE in one tick.
//
// A plain constant in the same spirit as MaxExpansionActions (RULINGS #5): it paces an API burst, not
// an economic decision, so there is nothing here an operator could usefully tune and every knob is
// another arming step.
//
// THREE, NOT SIX, and the difference is fan-out. A seed step charged against MaxExpansionActions is
// ONE API call. A gate read is one GetJumpGate plus one per-edge GetWaypoint construction probe —
// measured on the live pocket, X1-TD22 named 2 systems and X1-RV70 named 5, so a read costs 3-6 calls.
// Three reads is therefore the same order of per-tick spend as six seed steps, which is the pacing
// this constant is meant to mirror.
//
// IT BOUNDS A BURST AND NOT THE STEADY STATE. The candidate set is the BACKLOG of systems whose
// adjacency the store lacks, so once the map is read the pass makes zero calls on most ticks; what it
// has to absorb is the two bursts that matter — a cold cache, and the moment a gate read opens a
// region nobody had heard of. At three per tick a 33-system region is fully mapped in about eleven
// ticks, and the fleet spent HOURS sealed instead.
const MaxGateReads = 3

// GateReader performs the DELIBERATE, BOUNDED, FETCH-THROUGH read of one system's jump gate: the live
// GetJumpGate plus the persistence of what it found.
//
// IT IS A SEPARATE PORT FROM GateNeighbours ON PURPOSE, and the separation is the whole design. That
// type is a PURE STORE READ by contract — expansion asks Neighbours and Mapped of every known system
// on every tick, so a fetch-through implementation there "would spend the API budget on topology
// exactly where topology is least cached, and would do it hardest at the frontier". That reasoning is
// correct and this pass does not weaken it: the cheap per-tick question stays a store read, and the
// expensive deliberate one gets its own seam, asked of a bounded, ordered handful of systems that the
// store has already told us it cannot answer for.
//
// THE ANSWER ARRIVES THROUGH THE STORE, NOT THROUGH THE RETURN VALUE, which is why this returns only
// an error. A read that persisted its edges but handed them back here would invite a second,
// tick-local notion of adjacency living alongside the store's — and markFrontier, which owns turning
// neighbour names into PENDING rows, reads the store. One source, one definition of discovery.
//
// AN UNCHARTED GATE IS ORDINARY, NOT AN ERROR. A gate nobody has charted 400s without a hull standing
// on it, and that is the API telling us the truth: this one genuinely needs a probe. Implementations
// report it as gategraph.ErrGateUnreadable — the existing sentinel, matched with errors.Is and never
// by string — and the pass skips it, counts it, and carries on with the rest of the tick.
type GateReader interface {
	ReadGate(ctx context.Context, playerID int, system string) error
}

// gateMapping memoises "does the store hold ANY gate adjacency for this system?" for the TICK.
//
// TWO CONSUMERS, ONE READ. This pass asks it of every system in scope to build its candidate set, and
// orderByGateMapping asks it of every seed target to rank unknown territory first. Both are asking
// the identical question of the identical store, and two unshared reads would double a per-system
// store read every tick and let the two consumers observe different answers within one tick.
//
// The answer cannot change while the tick runs. A gate READ this tick does not update the memo, and
// that is deliberate rather than an oversight: the tick's remaining passes work from the neighbour map
// readNeighbours already built, so pretending mid-tick that the topology has grown would make this
// tick's ordering disagree with the map it is ordering over. The store has the new rows; the NEXT tick
// re-derives everything from them (RULINGS #2).
type gateMapping struct {
	gates GateNeighbours
	held  map[string]bool
}

func newGateMapping(gates GateNeighbours) *gateMapping {
	return &gateMapping{gates: gates, held: map[string]bool{}}
}

// mapped reports whether the store holds adjacency for a system, reading it at most once per tick.
//
// A READ FAILURE PROPAGATES rather than defaulting either way. Read as "mapped" it would silently
// drop a genuine unknown out of the candidate set — the failure this whole file exists to prevent,
// arriving through a database hiccup instead of an ordering bug. Read as "unmapped" it would spend
// the tick's whole read budget re-reading gates we already hold. The tick is idempotent and
// re-derived from scratch, so failing loudly costs one cycle.
func (m *gateMapping) mapped(ctx context.Context, system string) (bool, error) {
	if held, cached := m.held[system]; cached {
		return held, nil
	}
	held, err := m.gates.Mapped(ctx, system)
	if err != nil {
		return false, fmt.Errorf("failed to read whether the gate of %q has been mapped: %w", system, err)
	}
	m.held[system] = held
	return held, nil
}

// unreadGates names every system in scope whose adjacency the store cannot answer for.
//
// UNORDERED, deliberately. orderUnreadGatesByFrontier owns the order and owns it TOTALLY — distance
// then symbol — and sorting here as well would leave that function's tiebreak unable to decide
// anything, since preserving a symbol-sorted input IS the symbol order. A guard that can never be the
// thing that decides is dead code with a plausible-sounding rationale, which this engine already
// judges worse than none (see orderByGateMapping's removed short-circuit). One owner, one sort.
//
// SCOPE IS THE LEDGER, whatever the verdict — the same set readNeighbours propagates from, and for
// the same stated reason: "we have charted the gate" and "the store has rows" are the same fact, and
// a verdict says what a system is WORTH, never what we KNOW about it. Narrowing this to IN_SCOPE
// systems would exclude the PENDING rows markFrontier writes for freshly-named neighbours, which are
// precisely the systems whose gates have never been read.
//
// IT TAKES THE MAP markFrontier HAS ALREADY GROWN, not the ledger rows read at the top of the tick.
// That one choice is what makes the recursion single-tick at the near end: a neighbour named by a gate
// read on tick N is recorded PENDING by markFrontier on tick N+1 and has its OWN gate read on that
// same tick N+1, rather than waiting for N+2.
//
// THE PREDICATE IS THE STORE'S OWN CACHE-MISS CONDITION, which is what keeps this from ever spending a
// call it did not need. GateNeighbourPort.Mapped and gategraph's Connections both branch on the `ok`
// the edge store returns, so a system this admits is exactly a system Connections would fetch live,
// and a system it skips is exactly one Connections would answer from cache. There is no second notion
// of "already known" to drift.
func unreadGates(ctx context.Context, mapping *gateMapping, scope map[string]bool) ([]string, error) {
	unread := make([]string, 0, len(scope))
	for system := range scope {
		if system == "" {
			continue
		}
		held, err := mapping.mapped(ctx, system)
		if err != nil {
			return nil, err
		}
		if held {
			continue
		}
		unread = append(unread, system)
	}
	return unread, nil
}

// orderUnreadGatesByFrontier puts the CHEAPEST, most immediately useful reads first: the gates
// nearest the systems we actually hold, with a symbol tiebreak.
//
// NEAREST-FIRST IS ABOUT WHAT THE ANSWER IS WORTH, not about what the read costs — a gate read is the
// same one API call however far away the system is. A gate one hop from a system we occupy names
// neighbours a parked spare could be walked to almost immediately; a gate nine hops out names
// neighbours nothing can act on for twenty ticks of transit. Reading the near ones first is what turns
// a newly-opened region into placements soonest, and it is the same rule and the same walker
// (gateReach, over stored adjacency) that orderByReach already applies to seed targets, so the two can
// never disagree about what "near" means.
//
// A SYSTEM WE HOLD IS ZERO HOPS FROM ITSELF, and it is stated rather than derived because the walker
// will not say so: gateReach.from excludes its own origin, so a system we are standing in would
// otherwise rank as unreachable and sort dead last. That is the most valuable read on the board — a
// hull is already there, and its gate is the one whose neighbours we could act on this tick — so
// leaving it at the back would be exactly backwards.
//
// THE SYMBOL TIEBREAK IS NOT COSMETIC. The cap truncates this queue, so without a total order the
// tick's choice of WHICH gates to read would depend on Go's map iteration order and differ every tick:
// the same three systems would never be retried, and a gate could sit unread indefinitely while the
// pass looked busy. Distance then symbol is a total order over distinct systems, so the queue is
// reproducible tick to tick and the truncated tail is served in a predictable order as the head
// drains.
func orderUnreadGatesByFrontier(
	ctx context.Context,
	reach *gateReach,
	book *slotBook,
	unread []string,
) ([]string, error) {
	held := heldSystems(book)
	occupied := make(map[string]bool, len(held))
	for _, system := range held {
		occupied[system] = true
	}

	// Resolved ONCE PER CANDIDATE, up front, never from inside the comparator — the same discipline
	// orderByGateMapping documents. The walk is memoised per origin, but sort calls its less function
	// O(n log n) times and a miss there would walk the graph again.
	distance := make(map[string]int, len(unread))
	for _, system := range unread {
		nearest := reach.beyondReach()
		if occupied[system] {
			nearest = 0
		}
		for _, origin := range held {
			hops, within, err := reach.hops(ctx, origin, system)
			if err != nil {
				return nil, err
			}
			if within && hops < nearest {
				nearest = hops
			}
		}
		distance[system] = nearest
	}

	ordered := append([]string(nil), unread...)
	sort.Slice(ordered, func(i, j int) bool {
		if distance[ordered[i]] != distance[ordered[j]] {
			return distance[ordered[i]] < distance[ordered[j]]
		}
		return ordered[i] < ordered[j]
	})
	return ordered, nil
}

// readUnmappedGates is the pass: read the jump gate of every in-scope system whose adjacency the store
// cannot answer for, nearest-first, up to MaxGateReads in one tick.
//
// A NIL READER IS A WIRING GAP, NOT A SWITCH, and the pass then costs literally nothing — not even the
// per-system mapping sweep, because with nothing to read there is no candidate set worth building.
// That is the same contract OffGatePorts carries.
//
// NO FAILURE HERE FAILS THE TICK, and that is a deliberate asymmetry with the rest of the engine. The
// passes around this one command hulls and write ledger rows, so a bad read there can strand a probe
// or double-count a purchase and must fail loudly. THIS pass writes nothing, moves nothing and spends
// nothing: a read that does not happen costs information alone, and the next tick re-derives the whole
// candidate set and retries for free. Failing the tick on one bad gate would stop the seed machinery,
// the spare claim and the off-gate fallback in order to protect a lookup — strictly worse than the
// problem.
//
// THE MAPPING READ IS THE ONE EXCEPTION and it propagates, because it is not a failure to LEARN, it is
// a failure to know what we already learned. Swallowed, it reports the entire map as already known,
// reads nothing, and returns success — the fleet quietly stops growing and the heartbeat says
// everything is fine, which is the exact silent failure this file exists to prevent.
//
// NOTHING HERE RETRY-STORMS. Each candidate is attempted at most once per tick, and an unreadable gate
// is not re-queued: it stays in the backlog, sorted where it always was, and the next tick asks again.
// What keeps THAT from becoming a per-tick storm of doomed 400s is gategraph's own persisted
// negative-result backoff (5m -> 30m -> 2h), which short-circuits the live call before it is made and
// answers ErrGateUnreadable from the store. Reusing that resolver rather than writing a second gate
// fetcher is what buys the property; a hand-rolled read would have had to re-invent it.
func readUnmappedGates(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	scope map[string]bool,
	mapping *gateMapping,
	reach *gateReach,
	book *slotBook,
	rep *ExpandReport,
) error {
	if p.GateRead == nil {
		return nil
	}
	unread, err := unreadGates(ctx, mapping, scope)
	if err != nil {
		return err
	}
	// The WHOLE backlog, recorded before the cap truncates it: the heartbeat's job is to show the
	// frontier draining, and a count of what one tick happened to absorb cannot.
	rep.GatesUnread = len(unread)
	if len(unread) == 0 {
		return nil
	}
	ordered, err := orderUnreadGatesByFrontier(ctx, reach, book, unread)
	if err != nil {
		return err
	}

	for attempts := 0; attempts < MaxGateReads && attempts < len(ordered); attempts++ {
		system := ordered[attempts]
		recordGateRead(ctx, p.GateRead.ReadGate(ctx, playerID, system), system, rep)
	}
	return nil
}

// recordGateRead classifies one gate read's outcome into the tick's report.
//
// THE SENTINEL IS THE WHOLE POINT. An uncharted gate 400s without a hull standing on it, and that is
// the API answering honestly — "this one genuinely needs a probe" — rather than anything going wrong.
// On a young frontier it is the COMMON outcome. gategraph already draws that line for us:
// ErrGateUnreadable marks exactly the per-system read the API refuses, and it is distinct by
// construction from a store, token or transport fault. Matching it with errors.Is rather than by
// status code or message is what keeps the classification honest as the wording moves, and what stops
// the rare real fault being buried in the count that is supposed to be large.
//
// THE UNCHARTED CASE IS SILENT. gategraph emits ONE line per backoff transition when a probe actually
// fails, which is the operator signal; logging the same gate here on every tick is the per-tick spam
// that signal exists to replace. A genuine fault is logged, because nothing else will.
func recordGateRead(ctx context.Context, err error, system string, rep *ExpandReport) {
	switch {
	case err == nil:
		rep.GatesRead++
	case errors.Is(err, gategraph.ErrGateUnreadable):
		rep.GatesUnreadable++
	default:
		rep.GatesFailed++
		logging.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf(
			"gate read of %s failed for a reason that is not 'this gate needs a hull'; the next tick retries", system),
			map[string]interface{}{
				"action": "parked_sensing_gate_read_failed",
				"system": system,
				"error":  err.Error(),
			})
	}
}

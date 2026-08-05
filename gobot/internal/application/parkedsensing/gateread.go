package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
)

// gateread.go reads the jump gate of every in-scope system whose adjacency the store does not hold.
//
// A CHARTED jump gate is readable from the API WITH NO SHIP PRESENT (gategraph.ChartPresentGate);
// only an UNCHARTED one needs a hull, and it reports that itself as gategraph.ErrGateUnreadable. So
// eligibility here is deliberately blind to seed state, uncharted count, catalogue state, verdict and
// hull presence — every OTHER gate consideration in this engine runs over seedlessTargets, whose
// `(UnchartedCount > 0 || !CatalogKnown) && !hasActiveSeed` rule drops a system out of the gate
// question the moment a seed is dispatched to it or its catalogue is swept. The only question here is
// whether the store holds adjacency.
//
// IT IS RECURSIVE BY CONSTRUCTION AND HOLDS NOTHING BETWEEN TICKS (RULINGS #2): a read persists the
// system's edges, markFrontier turns the newly-named neighbours into PENDING sensing rows on the next
// tick, and because this pass scopes off the map markFrontier just grew they are candidates on that
// same tick.

// MaxGateReads bounds how many jump gates this pass may READ LIVE in one tick. Like
// MaxExpansionActions (RULINGS #5) it paces an API burst rather than expressing an economic decision,
// so it is a constant and not a knob. It sits lower than that budget because of fan-out: a seed step
// is ONE API call, while a gate read is one GetJumpGate plus one per-edge GetWaypoint construction
// probe.
//
// IT BOUNDS A BURST AND NOT THE STEADY STATE. The candidate set is the BACKLOG of systems whose
// adjacency the store lacks, so once the map is read the pass makes zero calls on most ticks; what it
// has to absorb is a cold cache, and the moment a gate read opens a region nobody had heard of.
const MaxGateReads = 3

// GateReader performs the DELIBERATE, BOUNDED, FETCH-THROUGH read of one system's jump gate: the live
// GetJumpGate plus the persistence of what it found. It is a SEPARATE PORT from GateNeighbours
// because that type is a PURE STORE READ by contract — expansion asks Neighbours and Mapped of every
// known system on every tick, and a fetch-through there would spend the API budget on topology
// hardest at the frontier, where it is least cached.
//
// THE ANSWER ARRIVES THROUGH THE STORE, NOT THROUGH THE RETURN VALUE, which is why this returns only
// an error: markFrontier owns turning neighbour names into PENDING rows and reads the store, so
// handing edges back here would create a second, tick-local notion of adjacency beside it.
//
// AN UNCHARTED GATE IS ORDINARY, NOT AN ERROR: it 400s without a hull standing on it. Implementations
// report that as gategraph.ErrGateUnreadable, matched with errors.Is and never by string, and the
// pass skips it, counts it, and carries on with the rest of the tick.
type GateReader interface {
	ReadGate(ctx context.Context, playerID int, system string) error
}

// gateMapping memoises "does the store hold ANY gate adjacency for this system?" for the TICK. Two
// consumers share the one read — this pass, over every system in scope, and orderUnmappedFirst, over
// every seed target — so neither pays a duplicate per-system store read nor observes a different
// answer within one tick.
//
// A gate READ this tick does NOT update the memo, deliberately: the tick's remaining passes work from
// the neighbour map readNeighbours already built, so growing the topology mid-tick would make this
// tick's ordering disagree with the map it is ordering over. The NEXT tick re-derives it (RULINGS #2).
type gateMapping struct {
	gates GateNeighbours
	held  map[string]bool
}

func newGateMapping(gates GateNeighbours) *gateMapping {
	return &gateMapping{gates: gates, held: map[string]bool{}}
}

// mapped reports whether the store holds adjacency for a system, reading it at most once per tick.
//
// A READ FAILURE PROPAGATES rather than defaulting either way: read as "mapped" it would silently
// drop a genuine unknown out of the candidate set, read as "unmapped" it would spend the tick's whole
// read budget re-reading gates we already hold. The tick is idempotent, so failing loudly costs one
// cycle.
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

// unread names every system in scope whose adjacency the store cannot answer for.
//
// UNORDERED, deliberately: orderUnreadGatesByFrontier owns the order TOTALLY — distance then symbol —
// and sorting here as well would leave that function's tiebreak unable to decide anything, since
// preserving a symbol-sorted input IS the symbol order. One owner, one sort.
//
// SCOPE IS THE LEDGER, WHATEVER THE VERDICT — the same set readNeighbours propagates from, and for
// the same reason: a verdict says what a system is WORTH, never what we KNOW about it. Narrowing this
// to IN_SCOPE systems would exclude the PENDING rows markFrontier writes for freshly-named
// neighbours, which are precisely the systems whose gates have never been read.
//
// THE PREDICATE IS THE STORE'S OWN CACHE-MISS CONDITION, which is what keeps this from ever spending a
// call it did not need: GateNeighbourPort.Mapped and gategraph's Connections both branch on the `ok`
// the edge store returns, so a system this admits is exactly one Connections would fetch live.
func (m *gateMapping) unread(ctx context.Context, scope map[string]bool) ([]string, error) {
	unread := make([]string, 0, len(scope))
	for system := range scope {
		if system == "" {
			continue
		}
		held, err := m.mapped(ctx, system)
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

// orderUnreadGatesByFrontier puts the most immediately useful reads first: the gates nearest the
// systems we actually hold, with a symbol tiebreak.
//
// NEAREST-FIRST IS ABOUT WHAT THE ANSWER IS WORTH, not about what the read costs — a gate read is the
// same one API call however far away the system is. It uses the same walker (gateReach, over stored
// adjacency) that orderByDistance applies to seed targets, so the two can never disagree about what
// "near" means.
//
// A SYSTEM WE HOLD IS ZERO HOPS FROM ITSELF, stated rather than derived because gateReach.from
// excludes its own origin: a system we are standing in would otherwise rank as unreachable and sort
// dead last, exactly backwards for the read whose neighbours this tick could act on.
//
// THE SYMBOL TIEBREAK IS NOT COSMETIC. The cap truncates this queue, so without a total order the
// tick's choice of WHICH gates to read would follow Go's map iteration order and differ every tick — a
// gate could sit unread indefinitely while the pass looked busy.
func orderUnreadGatesByFrontier(
	ctx context.Context,
	reach *gateReach,
	book *slotBook,
	unread []string,
) ([]string, error) {
	held := book.heldSystems()
	occupied := make(map[string]bool, len(held))
	for _, system := range held {
		occupied[system] = true
	}

	// Resolved ONCE PER CANDIDATE, never from inside the comparator: the walk is memoised per origin,
	// but sort calls its less function O(n log n) times and a miss there would walk the graph again.
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
// per-system mapping sweep. That is the same contract OffGatePorts carries.
//
// NO FAILURE HERE FAILS THE TICK, a deliberate asymmetry with the passes around it: those command
// hulls and write ledger rows, while this one writes nothing, moves nothing and spends nothing, so a
// read that does not happen costs information alone and the next tick retries for free. THE MAPPING
// READ IS THE ONE EXCEPTION and it propagates, because it is not a failure to LEARN but a failure to
// know what we already learned — swallowed, it reports the whole map as already known, reads nothing
// and returns success while the heartbeat says everything is fine.
//
// NOTHING HERE RETRY-STORMS. Each candidate is attempted at most once per tick, and an unreadable gate
// is not re-queued: it stays in the backlog and the next tick asks again. What keeps THAT from
// becoming a per-tick storm of doomed 400s is gategraph's own persisted negative-result backoff, which
// short-circuits the live call and answers ErrGateUnreadable from the store — a property that comes
// from reusing that resolver rather than writing a second gate fetcher.
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
	unread, err := mapping.unread(ctx, scope)
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
// THE SENTINEL IS THE WHOLE POINT. An uncharted gate 400s without a hull standing on it, which on a
// young frontier is the COMMON outcome rather than anything going wrong. ErrGateUnreadable marks
// exactly that read and is distinct by construction from a store, token or transport fault; matching
// it with errors.Is rather than by status code or message keeps the classification honest as the
// wording moves, and stops a rare real fault being buried in the count that is supposed to be large.
//
// THE UNCHARTED CASE IS SILENT: gategraph already emits one line per backoff transition when a probe
// actually fails, so logging the same gate here every tick is the spam that signal exists to replace.
// A genuine fault is logged, because nothing else will.
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

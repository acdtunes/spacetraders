package parkedsensing

// gate_read_port.go is the one place in the sensing adapters that DELIBERATELY reaches for the API to
// learn topology. Everything beside it — GateNeighbourPort especially — is a pure store read by
// contract, and that contract is preserved rather than bent: this is a SECOND, separate port, asked of
// a bounded and ordered handful of systems per tick, never of every system on every tick.
//
// See the application-side doc on parkedsensing.GateReader for why the two must not be the same seam,
// and gateread.go for why the pass exists at all.

import (
	"context"

	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Compile-time proof that the fetch-through read still satisfies the port the engine declares for it.
var _ appSensing.GateReader = (*GateReadPort)(nil)

// gateConnectionResolver is the fetch-through gate resolver, narrowed to the one verb this port
// needs. *gategraph.Service satisfies it.
//
// NARROWED ON PURPOSE, and to the verb the `spacetraders system gates --system X1-...` CLI already
// drives. Connections is the codebase's ONE live-gate-read-and-persist path: it resolves the system's
// own gate waypoint (from a neighbour's stored edge, or from the system graph), skips the call
// entirely when that gate is still UNCHARTED and would only 400, reads the live connections with a
// bounded re-read against the API's intermittent empty-200, probes each named gate's build state, and
// Replaces the system's stored edge set atomically. Reaching for the API here directly would mean
// re-implementing all six of those, and the one that matters most is invisible: the persisted
// negative-result backoff (5m -> 30m -> 2h) that stops a doomed gate being re-probed on every tick.
type gateConnectionResolver interface {
	Connections(ctx context.Context, systemSymbol string, playerID int) ([]domainSystem.GateEdge, error)
}

// GateReadPort reads ONE system's jump gate live and persists what it found.
type GateReadPort struct{ gates gateConnectionResolver }

// NewGateReadPort wires the deliberate fetch-through gate read.
func NewGateReadPort(gates gateConnectionResolver) *GateReadPort {
	return &GateReadPort{gates: gates}
}

// ReadGate fetches and persists the system's gate adjacency.
//
// THE EDGES ARE DISCARDED, and that is the contract rather than laziness. Connections has already
// persisted them, and the engine learns what the read found by re-deriving from the store on the next
// tick — so there is exactly one definition of "where this system connects" and no tick-local copy to
// disagree with it. markFrontier, which turns neighbour names into PENDING sensing rows, reads that
// same store.
//
// THE CALLER'S GUARD AND THIS CALL'S CACHE BEHAVIOUR ARE THE SAME PREDICATE, which is what keeps the
// pass from ever spending a request it did not need. The engine only asks for a system whose
// GateNeighbourPort.Mapped answered false, and both that method and Connections branch on the `ok` the
// edge store returns — so a system the pass admits is exactly one Connections would fetch live, and a
// system it skips is exactly one Connections would answer from cache. There is no second notion of
// "already known" for the two sides to drift apart on.
//
// TAGGED AS CHARTING SPEND, like every other API call the sensing engine makes, so this shows up in
// the budget where the work belongs — it IS charting, done from a distance instead of with a hull.
//
// THE ERROR IS RETURNED VERBATIM so gategraph.ErrGateUnreadable survives errors.Is at the call site.
// Wrapping it in a message of our own would still satisfy errors.Is, but re-classifying it — mapping
// it to nil, or to a bespoke sentinel — would erase the one distinction the pass depends on: an
// uncharted gate genuinely needs a hull, and everything else is a fault worth logging.
func (p *GateReadPort) ReadGate(ctx context.Context, playerID int, system string) error {
	_, err := p.gates.Connections(sensingCtx(ctx), system, playerID)
	return err
}

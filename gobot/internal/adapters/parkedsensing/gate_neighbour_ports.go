package parkedsensing

import (
	"context"
	"fmt"
	"sort"

	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// gateEdgeReader is the stored jump-gate adjacency, narrowed to the one
// per-system read. *persistence.GormGateEdgeRepository satisfies it.
type gateEdgeReader interface {
	Edges(ctx context.Context, systemSymbol string) ([]domainSystem.GateEdge, bool, error)
	// AllEdges returns every era-scoped system's edges at once, keyed by system, with a
	// condemned or unread system simply ABSENT — presence is the `ok` Edges returns.
	AllEdges(ctx context.Context) (map[string][]domainSystem.GateEdge, error)
}

// GateNeighbourPort reads which systems are one jump from a given one.
//
// PER-SYSTEM, never the whole-map Adjacency read. Expansion asks this of every
// judged system on every tick, so a whole-map query would make one tick's
// topology cost scale with everything the fleet has ever charted — and it would
// grow fastest exactly as the frontier succeeds.
//
// A PURE STORE read by contract: a system missing from the store, or whose edge
// set is stale, is simply not expanded through. Wiring a fetch-through resolver
// here would spend the API budget on topology precisely where topology is least
// cached.
type GateNeighbourPort struct{ edges gateEdgeReader }

// NewGateNeighbourPort wires the stored gate adjacency.
func NewGateNeighbourPort(edges gateEdgeReader) *GateNeighbourPort {
	return &GateNeighbourPort{edges: edges}
}

// Neighbours returns the system's stored gate neighbours.
//
// A miss returns no neighbours and no error: expansion propagating through an
// unknown system is speculative work, and refusing to guess is the same rule the
// stored-distance walk follows. A genuinely old set never arrives here at all — the
// store condemns it and reports a miss.
//
// Every unusable edge is EXCLUDED FROM THE ANSWER, never allowed to erase the answer.
// An under-construction edge is impassable (a jump into an unbuilt gate fails at hop
// time) and a stale edge's build state is past verification, so both are skipped — but
// only that edge, not its siblings.
//
// Dropping the set WHOLE on any stale row is what walled off the map. Its reasoning was
// that a system's edges are written in one replace under a single timestamp, so one stale
// row condemns all — true of age, false of the SHORTER window an under-construction row is
// chased on. That row is stale on a 2h schedule while its built siblings stay perfectly
// current, so every system holding one still-building exit reported ZERO neighbours every
// 2h, became a WALL in every BFS, and made routing refuse provably-existing routes: 173 of
// 1,168 live systems, freezing 266 probes that were bought, assigned and never dispatched.
// PassableGraph returns the whole topology in one read, applying EXACTLY the filter Neighbours
// applies per system — an under-construction or per-row-stale edge is not passable — and taking
// the map's key set as the mapped set, which is exactly the `ok` Neighbours discards.
//
// Sharing the two rules with the per-system path rather than restating them is the point: a
// second copy of "passable" would drift the first time one of them learned a new exclusion, and
// the copy that got missed would be the one deciding where hulls can be sent.
func (p *GateNeighbourPort) PassableGraph(ctx context.Context) (appSensing.GateGraph, error) {
	all, err := p.edges.AllEdges(ctx)
	if err != nil {
		return appSensing.GateGraph{}, fmt.Errorf("failed to read the gate graph: %w", err)
	}
	graph := appSensing.GateGraph{
		Passable: make(map[string][]string, len(all)),
		Mapped:   make(map[string]bool, len(all)),
	}
	for symbol, edges := range all {
		// Present in AllEdges means the system's adjacency HAS been read and is not condemned,
		// which is the whole of "mapped" — including a system whose every exit is impassable.
		graph.Mapped[symbol] = true
		out := make([]string, 0, len(edges))
		for _, edge := range edges {
			if edge.ConnectedSystem == "" || edge.UnderConstruction || edge.Stale {
				continue
			}
			out = append(out, edge.ConnectedSystem)
		}
		sort.Strings(out)
		graph.Passable[symbol] = out
	}
	return graph, nil
}

func (p *GateNeighbourPort) Neighbours(ctx context.Context, system string) ([]string, error) {
	edges, ok, err := p.edges.Edges(ctx, system)
	if err != nil {
		return nil, fmt.Errorf("failed to read the gate neighbours of %q: %w", system, err)
	}
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(edges))
	for _, edge := range edges {
		if edge.ConnectedSystem == "" || edge.UnderConstruction || edge.Stale {
			continue
		}
		out = append(out, edge.ConnectedSystem)
	}
	sort.Strings(out)
	return out, nil
}

// Mapped reports whether we hold ANY stored gate adjacency for this system.
//
// IT READS THE `ok` THE EDGE STORE ALREADY RETURNS and Neighbours discards — the same round trip,
// answering the different question. Neighbours filters under-construction and stale edges out of its
// ANSWER while the rows remain, so "Neighbours returned nothing" cannot distinguish a system whose
// exits are all under construction (fully mapped: we know where it connects and simply cannot pass)
// from one we have never read at all. Only the second can add systems to the ledger when charted.
//
// A read failure PROPAGATES rather than defaulting. Read as mapped it would demote genuine frontier
// territory to the back of the seed queue and the fleet would quietly stop growing; read as unmapped
// it would promote every ordinary target at once.
func (p *GateNeighbourPort) Mapped(ctx context.Context, system string) (bool, error) {
	_, ok, err := p.edges.Edges(ctx, system)
	if err != nil {
		return false, fmt.Errorf("failed to read whether the gate of %q has been mapped: %w", system, err)
	}
	return ok, nil
}

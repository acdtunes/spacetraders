package gategraph

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Two questions share the stored-adjacency walk and must NOT share its answer.
//
// "Can this hull fly there?" is a commitment: an unverified route that turns out to be
// unflyable strands a laden hull, so topology past its freshness window is refused
// (StoredHopDistances).
//
// "How far is that, for ranking?" is not a commitment — the crossing gets a price either way,
// and the executor still resolves the real route at flight time. There, refusing a stale set
// does not make the answer safer, only less accurate: a gate's build state moves only from
// under-construction to built, so a stale "built" edge is still built, and a stale set that has
// since GAINED an edge yields an over-estimate, which is the harmless direction for a price.
// Refusing it instead charges a near crossing as if it were beyond the horizon.

// staleMiddle wires ORIGIN—MID—TARGET with MID's own edge set past its freshness window.
func staleMiddle() map[string][]system.GateEdge {
	return map[string][]system.GateEdge{
		"X1-ORIGIN": repoEdgesTo("X1-MID"),
		"X1-MID":    staleEdgesTo("X1-ORIGIN", "X1-TARGET"),
		"X1-TARGET": repoEdgesTo("X1-MID"),
	}
}

// The ranking walk reads a stale-but-cached set as usable topology and resolves the true
// distance, where the proof-grade walk stops.
func TestStoredRankingDistances_ResolvesThroughStaleTopology(t *testing.T) {
	svc := NewService(&adjStore{adjacency: staleMiddle()}, nil, nil, nil) // nil API: a fetch would panic

	got, err := svc.StoredRankingDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, MaxJumpPath)
	if err != nil {
		t.Fatalf("a stored-adjacency walk must resolve with no fetch, got %v", err)
	}
	if got["X1-TARGET"] != 2 {
		t.Fatalf("the target is 2 gate hops out over edges we have read; ranking must price it at 2, got %v", got)
	}
}

// The proof-grade walk is UNCHANGED on the very fixture the ranking walk now resolves. The two
// verdicts differing here is the whole point: reach proof stays strict while pricing gets
// accurate, and neither borrows the other's rule.
func TestStoredHopDistances_StillRefusesStaleTopologyTheRankingWalkAccepts(t *testing.T) {
	svc := NewService(&adjStore{adjacency: staleMiddle()}, nil, nil, nil)

	got, err := svc.StoredHopDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, MaxJumpPath)
	if err != nil {
		t.Fatalf("unverified topology is a clean refusal, not an error; got %v", err)
	}
	if _, resolved := got["X1-TARGET"]; resolved {
		t.Fatalf("the reach proof must still refuse a route through stale topology; got %v", got)
	}
}

// Ranking is laxer about FRESHNESS and about nothing else. An unbuilt gate is impassable at hop
// time whatever the row's age, and a system never read at all has no topology to be lax about —
// inventing one would be exactly the "route past what you cannot read" weakening that strands a
// hull. Each case is the staleMiddle geometry with one edge changed, so a refusal here is the
// rule firing rather than an inert fixture.
func TestStoredRankingDistances_StaysStrictOnPassabilityAndOnWhatWasNeverRead(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(adjacency map[string][]system.GateEdge)
	}{
		{name: "the only route crosses an unbuilt gate", mutate: func(a map[string][]system.GateEdge) {
			a["X1-MID"] = []system.GateEdge{{ConnectedSystem: "X1-TARGET", UnderConstruction: true}}
		}},
		{name: "the only route crosses an unbuilt gate whose row is also stale", mutate: func(a map[string][]system.GateEdge) {
			a["X1-MID"] = []system.GateEdge{{ConnectedSystem: "X1-TARGET", UnderConstruction: true, Stale: true}}
		}},
		{name: "the intermediate was never cached", mutate: func(a map[string][]system.GateEdge) {
			delete(a, "X1-MID")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			adjacency := staleMiddle()
			c.mutate(adjacency)
			svc := NewService(&adjStore{adjacency: adjacency}, nil, nil, nil)

			got, err := svc.StoredRankingDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, MaxJumpPath)
			if err != nil {
				t.Fatalf("%s: an unusable route is a clean refusal, not an error; got %v", c.name, err)
			}
			if _, resolved := got["X1-TARGET"]; resolved {
				t.Fatalf("%s: the route is not usable and the target must be absent, got %v", c.name, got)
			}
		})
	}
}

// The ranking walk inherits the properties that made the store-only walk worth having: exact
// within its bound whatever the local branching, and bounded by depth alone.
func TestStoredRankingDistances_ExactRegardlessOfLocalBranchingAndBound(t *testing.T) {
	svc := NewService(&adjStore{adjacency: spurredChain("X1-ORIGIN", 500, 4)}, nil, nil, nil)

	got, err := svc.StoredRankingDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET", "X1-HOP2"}, MaxJumpPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["X1-TARGET"] != 4 || got["X1-HOP2"] != 2 {
		t.Fatalf("a 500-wide neighbourhood must not exhaust any budget; want TARGET=4 HOP2=2, got %v", got)
	}

	tight, err := svc.StoredRankingDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, resolved := tight["X1-TARGET"]; resolved {
		t.Fatalf("a 4-hop target under a 3-jump bound must be absent, got %v", tight)
	}
}

// An unreadable store is an ERROR for ranking too. A price derived from a read that failed is
// not a cheaper price, it is a fabricated one.
func TestStoredRankingDistances_StoreFailureFailsClosed(t *testing.T) {
	svc := NewService(&adjStore{adjErr: errors.New("db down")}, nil, nil, nil)

	got, err := svc.StoredRankingDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, MaxJumpPath)
	if err == nil {
		t.Fatalf("an unreadable adjacency must surface as an error, got distances %v", got)
	}
	if got != nil {
		t.Fatalf("a failed read must return no distances, got %v", got)
	}
}

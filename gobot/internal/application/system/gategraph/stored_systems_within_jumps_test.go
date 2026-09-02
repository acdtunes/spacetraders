package gategraph

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Every service below is built with a nil API client, so any fetch-through would panic rather
// than pass: the discovery walk must be a pure store read.

// chainWithSpur wires ORIGIN—A—B—C in a line plus ORIGIN—D, so a bound of 2 cuts the chain at B.
func chainWithSpur() map[string][]system.GateEdge {
	return map[string][]system.GateEdge{
		"X1-ORIGIN": repoEdgesTo("X1-A", "X1-D"),
		"X1-A":      repoEdgesTo("X1-ORIGIN", "X1-B"),
		"X1-B":      repoEdgesTo("X1-A", "X1-C"),
		"X1-C":      repoEdgesTo("X1-B"),
		"X1-D":      repoEdgesTo("X1-ORIGIN"),
	}
}

func TestStoredSystemsWithinJumps_ListsTheNeighbourhoodByHopWithNoFetch(t *testing.T) {
	svc := NewService(&adjStore{adjacency: chainWithSpur()}, nil, nil, nil)

	got, err := svc.StoredSystemsWithinJumps(context.Background(), "X1-ORIGIN", 2)
	if err != nil {
		t.Fatalf("a stored-adjacency walk must resolve with no fetch, got %v", err)
	}
	want := map[string]int{"X1-A": 1, "X1-D": 1, "X1-B": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("within 2 hops = %v, want %v (X1-C is 3 out; the origin is the caller's own system, not a candidate)", got, want)
	}
}

// Laxer about FRESHNESS and about nothing else, exactly as the ranking distances are: a stale set
// is walked through (a built gate stays built), an unbuilt gate stays impassable whatever its
// row's age, and a system never cached has no topology to be lax about. Arriving AT a system is
// resolved by the edge into it in every case — only expansion THROUGH it needs its own set.
func TestStoredSystemsWithinJumps_RanksThroughStaleButNeverThroughAnUnbuiltGateOrWhatWasNeverRead(t *testing.T) {
	for _, c := range []struct {
		name       string
		mutate     func(adjacency map[string][]system.GateEdge)
		wantTarget bool
	}{
		{name: "MID's own set is stale: TARGET is still priced at 2", mutate: func(map[string][]system.GateEdge) {}, wantTarget: true},
		{name: "the only route crosses an unbuilt gate", mutate: func(a map[string][]system.GateEdge) {
			a["X1-MID"] = []system.GateEdge{{ConnectedSystem: "X1-TARGET", UnderConstruction: true}}
		}},
		{name: "the intermediate was never cached", mutate: func(a map[string][]system.GateEdge) {
			delete(a, "X1-MID")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			adjacency := staleMiddle()
			c.mutate(adjacency)
			svc := NewService(&adjStore{adjacency: adjacency}, nil, nil, nil)

			got, err := svc.StoredSystemsWithinJumps(context.Background(), "X1-ORIGIN", MaxJumpPath)
			if err != nil {
				t.Fatalf("unverified topology is a refusal, not an error; got %v", err)
			}
			if got["X1-MID"] != 1 {
				t.Fatalf("X1-MID is one built hop out and must be listed at 1 whatever its own set holds; got %v", got)
			}
			if _, listed := got["X1-TARGET"]; listed != c.wantTarget {
				t.Fatalf("X1-TARGET listed=%v, want %v; got %v", listed, c.wantTarget, got)
			}
		})
	}
}

func TestStoredSystemsWithinJumps_FailsClosedOnAnUnreadableStore(t *testing.T) {
	svc := NewService(&adjStore{adjErr: errors.New("gate_edges unreadable")}, nil, nil, nil)

	if got, err := svc.StoredSystemsWithinJumps(context.Background(), "X1-ORIGIN", 2); err == nil {
		t.Fatalf("an unreadable store must be an error, never an empty neighbourhood; got %v", got)
	}
}

// One traversal, two questions: every system discovery lists is priced at the distance the ranking
// resolver returns when asked for it by name, stale interior included — there is no second walk to
// fall out of step with.
func TestStoredSystemsWithinJumps_AgreesWithTheRankingDistancesItShares(t *testing.T) {
	adjacency := chainWithSpur()
	adjacency["X1-A"] = staleEdgesTo("X1-ORIGIN", "X1-B")
	svc := NewService(&adjStore{adjacency: adjacency}, nil, nil, nil)

	found, err := svc.StoredSystemsWithinJumps(context.Background(), "X1-ORIGIN", 3)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if len(found) != 4 {
		t.Fatalf("A, D, B and C are all within 3 hops through the stale interior; got %v", found)
	}
	targets := make([]string, 0, len(found))
	for sys := range found {
		targets = append(targets, sys)
	}
	sort.Strings(targets)
	ranked, err := svc.StoredRankingDistances(context.Background(), "X1-ORIGIN", targets, 3)
	if err != nil {
		t.Fatalf("ranking distances: %v", err)
	}
	if !reflect.DeepEqual(found, ranked) {
		t.Fatalf("discovery %v and ranking %v must agree on every distance", found, ranked)
	}
}

package gategraph

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// StoredHopDistances answers "how far is this system from each of those?" from the STORE ALONE.
// Two properties carry it, and both are load-bearing for a caller that must prove reach without
// spending API: the walk is EXACT within its jump bound (no breadth budget that a dense
// neighbourhood can exhaust), and a topology gap is a REFUSAL rather than a fetch.

// spurredChain wires origin -> `spurs` marketless dead ends PLUS a chain of `depth` hops to
// X1-TARGET, with the spur fan listed first. A walk that budgets its breadth meets the fan
// before the chain and never reaches the target, though the target never moved.
func spurredChain(origin string, spurs, depth int) map[string][]system.GateEdge {
	adjacency := map[string][]system.GateEdge{}
	link := func(a, b string) {
		adjacency[a] = append(adjacency[a], system.GateEdge{ConnectedSystem: b, GateWaypoint: b + "-GATE"})
		adjacency[b] = append(adjacency[b], system.GateEdge{ConnectedSystem: a, GateWaypoint: a + "-GATE"})
	}
	for i := 0; i < spurs; i++ {
		link(origin, fmt.Sprintf("X1-SPUR%03d", i))
	}
	prev := origin
	for hop := 1; hop < depth; hop++ {
		next := fmt.Sprintf("X1-HOP%d", hop)
		link(prev, next)
		prev = next
	}
	link(prev, "X1-TARGET")
	return adjacency
}

// A target four hops out resolves at four hops however dense the origin's own neighbourhood is.
// Depth is held constant and only the fan size varies, so branching is provably the only
// variable — a walk that answered differently across these cases would be reporting its own
// budget rather than the graph.
func TestStoredHopDistances_ExactRegardlessOfLocalBranching(t *testing.T) {
	for _, spurs := range []int{0, 8, 500} {
		t.Run(fmt.Sprintf("%d spurs", spurs), func(t *testing.T) {
			store := &adjStore{adjacency: spurredChain("X1-ORIGIN", spurs, 4)}
			svc := NewService(store, nil, nil, nil) // nil API: a fetch-through would panic

			got, err := svc.StoredHopDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET", "X1-HOP2"}, MaxJumpPath)
			if err != nil {
				t.Fatalf("a stored-adjacency walk must resolve with no fetch, got %v", err)
			}
			if got["X1-TARGET"] != 4 {
				t.Fatalf("X1-TARGET is 4 gate hops out and must resolve at 4 whatever the local branching; got %v", got)
			}
			if got["X1-HOP2"] != 2 {
				t.Fatalf("every requested target resolves in the SAME walk; X1-HOP2 must be 2 hops, got %v", got)
			}
		})
	}
}

// Beyond the bound is absent — the caller's refusal signal. The same graph under a bound that
// clears the distance resolves it, so the bound (not the walk) is what refuses.
func TestStoredHopDistances_BoundRefusesByAbsence(t *testing.T) {
	store := &adjStore{adjacency: spurredChain("X1-ORIGIN", 4, 5)}
	svc := NewService(store, nil, nil, nil)

	tight, err := svc.StoredHopDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, resolved := tight["X1-TARGET"]; resolved {
		t.Fatalf("a 5-hop target under a 4-jump bound must be absent, got %v", tight)
	}

	wide, err := svc.StoredHopDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wide["X1-TARGET"] != 5 {
		t.Fatalf("the same target under a 5-jump bound must resolve at 5, got %v", wide)
	}
}

// Unverified topology is not routed THROUGH. An uncached system was never read and a stale one's
// onward gates are past their freshness window — in both cases the fetch-through resolver the
// executor flies would go back to the API and may then refuse, so trusting either here would
// prove a route that cannot actually be resolved at flight time.
func TestStoredHopDistances_RefusesToExpandThroughUnverifiedTopology(t *testing.T) {
	built := func() map[string][]system.GateEdge {
		return map[string][]system.GateEdge{
			"X1-ORIGIN": repoEdgesTo("X1-MID"),
			"X1-MID":    repoEdgesTo("X1-ORIGIN", "X1-TARGET"),
			"X1-TARGET": repoEdgesTo("X1-MID"),
		}
	}
	for _, c := range []struct {
		name   string
		mutate func(adjacency map[string][]system.GateEdge)
	}{
		{name: "the intermediate was never cached", mutate: func(a map[string][]system.GateEdge) {
			delete(a, "X1-MID")
		}},
		{name: "the intermediate's cached topology is stale", mutate: func(a map[string][]system.GateEdge) {
			a["X1-MID"] = staleEdgesTo("X1-ORIGIN", "X1-TARGET")
		}},
		{name: "the only edge out crosses an unbuilt gate", mutate: func(a map[string][]system.GateEdge) {
			a["X1-MID"] = []system.GateEdge{{ConnectedSystem: "X1-TARGET", UnderConstruction: true}}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			adjacency := built()
			c.mutate(adjacency)
			svc := NewService(&adjStore{adjacency: adjacency}, nil, nil, nil)

			got, err := svc.StoredHopDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, MaxJumpPath)
			if err != nil {
				t.Fatalf("unverified topology is a clean refusal, not an error; got %v", err)
			}
			if _, resolved := got["X1-TARGET"]; resolved {
				t.Fatalf("%s: the route is unproven and the target must be absent, got %v", c.name, got)
			}
		})
	}
}

// Arriving AT a stale system still resolves. Only the edge INTO a system must be verified to
// reach it — its own onward gates matter only for routing THROUGH. Conflating the two would
// refuse every far sink whose market is the thing going stale, which is most of them.
func TestStoredHopDistances_ResolvesATargetWhoseOwnTopologyIsStale(t *testing.T) {
	svc := NewService(&adjStore{adjacency: map[string][]system.GateEdge{
		"X1-ORIGIN": repoEdgesTo("X1-MID"),
		"X1-MID":    repoEdgesTo("X1-ORIGIN", "X1-TARGET"),
		"X1-TARGET": staleEdgesTo("X1-MID"),
	}}, nil, nil, nil)

	got, err := svc.StoredHopDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, MaxJumpPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["X1-TARGET"] != 2 {
		t.Fatalf("a target reached over verified edges resolves even though its OWN onward topology is stale; got %v", got)
	}
}

// An unreadable store is an ERROR, never an empty "nothing is reachable" — the caller must be
// able to tell a proven-unreachable graph from one it could not read (RULINGS #4).
func TestStoredHopDistances_StoreFailureFailsClosed(t *testing.T) {
	svc := NewService(&adjStore{adjErr: errors.New("db down")}, nil, nil, nil)

	got, err := svc.StoredHopDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, MaxJumpPath)
	if err == nil {
		t.Fatalf("an unreadable adjacency must surface as an error, got distances %v", got)
	}
	if got != nil {
		t.Fatalf("a failed read must return no distances, got %v", got)
	}
}

// A non-positive bound degrades to MaxJumpPath rather than searching zero jumps, mirroring the
// other bounded resolvers — a mis-wired caller gets the strict default, never silent blindness.
func TestStoredHopDistances_NonPositiveBoundDegradesToMaxJumpPath(t *testing.T) {
	svc := NewService(&adjStore{adjacency: spurredChain("X1-ORIGIN", 2, MaxJumpPath)}, nil, nil, nil)

	got, err := svc.StoredHopDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["X1-TARGET"] != MaxJumpPath {
		t.Fatalf("a zero bound must search the strict MaxJumpPath=%d, got %v", MaxJumpPath, got)
	}
}

package gategraph

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// --- pure BFS (bfsPath) ---

// mapNeighbors turns a plain adjacency map into the neighbor function bfsPath
// walks. An absent key is a dead-end node (no neighbors), never an error — the
// pure search must not depend on a store or the network.
func mapNeighbors(adj map[string][]string) func(string) ([]string, error) {
	return func(s string) ([]string, error) { return adj[s], nil }
}

// The incident route: JP61 is THREE jumps from KA42, reachable only through
// UQ16 (KA42→PA3→UQ16→JP61). travel() assumed one edge and crashed laden at the
// home gate; BFS must return the full ordered hop path instead. The fake
// adjacency makes PA3→UQ16 the unique route to JP61, so the shortest path is
// deterministic.
func TestBFSPath_KA42ToJP61_ThreeJumpPath(t *testing.T) {
	adj := map[string][]string{
		"X1-KA42": {"X1-GQ92", "X1-PA3", "X1-NK36"},
		"X1-PA3":  {"X1-KA42", "X1-UQ16", "X1-ZC66"},
		"X1-UQ16": {"X1-JP61", "X1-PA3"},
		"X1-GQ92": {"X1-KA42", "X1-NK36"},
		"X1-NK36": {"X1-GQ92", "X1-KA42"},
		"X1-JP61": {"X1-UQ16"},
	}

	got, err := bfsPath("X1-KA42", "X1-JP61", MaxJumpPath, mapNeighbors(adj))
	if err != nil {
		t.Fatalf("expected a route, got error: %v", err)
	}

	want := []string{"X1-KA42", "X1-PA3", "X1-UQ16", "X1-JP61"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected the 3-jump path %v, got %v", want, got)
	}
}

// A directly-connected destination must resolve to the two-element single-jump
// path [from, to] — the legacy single-edge case, unchanged.
func TestBFSPath_DirectEdge_SingleJump(t *testing.T) {
	adj := map[string][]string{"X1-AAA": {"X1-BBB"}}

	got, err := bfsPath("X1-AAA", "X1-BBB", MaxJumpPath, mapNeighbors(adj))
	if err != nil {
		t.Fatalf("expected a direct route, got error: %v", err)
	}
	want := []string{"X1-AAA", "X1-BBB"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected single-jump path %v, got %v", want, got)
	}
}

// from == to is a zero-jump path (the same-system case travel() short-circuits
// before ever calling BFS, but the search must still be correct).
func TestBFSPath_SameSystem_ZeroJumps(t *testing.T) {
	got, err := bfsPath("X1-AAA", "X1-AAA", MaxJumpPath, mapNeighbors(nil))
	if err != nil {
		t.Fatalf("expected a zero-jump path, got error: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"X1-AAA"}) {
		t.Fatalf("expected [X1-AAA], got %v", got)
	}
}

// An unreachable destination must return an ErrUnroutable-wrapped error naming
// BOTH systems (the acceptance criterion: a clear, greppable refusal), not a
// panic or an empty path.
func TestBFSPath_Unroutable_NamedError(t *testing.T) {
	adj := map[string][]string{
		"X1-AAA": {"X1-BBB"},
		"X1-BBB": {"X1-AAA"}, // a closed pocket that never reaches ZZZ
	}

	got, err := bfsPath("X1-AAA", "X1-ZZZ", MaxJumpPath, mapNeighbors(adj))
	if got != nil {
		t.Fatalf("expected no path, got %v", got)
	}
	if !errors.Is(err, ErrUnroutable) {
		t.Fatalf("expected ErrUnroutable, got %v", err)
	}
	if !strings.Contains(err.Error(), "X1-AAA") || !strings.Contains(err.Error(), "X1-ZZZ") {
		t.Fatalf("unroutable error must name both systems, got %q", err.Error())
	}
}

// The jump bound caps traversal depth: a linear chain reachable in exactly
// maxJumps resolves, but one node deeper (maxJumps+1) is unroutable — the guard
// against a pathological fetch storm over the uncharted frontier.
func TestBFSPath_RespectsMaxJumpBound(t *testing.T) {
	// A→B→C→D→E→F→G: 6 hops end to end.
	adj := map[string][]string{
		"A": {"B"}, "B": {"C"}, "C": {"D"}, "D": {"E"}, "E": {"F"}, "F": {"G"},
	}

	// F is exactly 5 jumps from A — at the bound, so reachable.
	got, err := bfsPath("A", "F", 5, mapNeighbors(adj))
	if err != nil {
		t.Fatalf("A→F is 5 jumps (at the bound) and must resolve, got error: %v", err)
	}
	if len(got)-1 != 5 {
		t.Fatalf("expected a 5-jump path, got %d jumps (%v)", len(got)-1, got)
	}

	// G is 6 jumps — beyond the bound, so unroutable.
	if _, err := bfsPath("A", "G", 5, mapNeighbors(adj)); !errors.Is(err, ErrUnroutable) {
		t.Fatalf("A→G is 6 jumps (beyond the 5 bound) and must be unroutable, got %v", err)
	}
}

// A neighbor-function error (a store/fetch failure mid-search) must abort the
// search and surface — it must NOT be swallowed and reported as "unroutable",
// which would let a transient failure masquerade as a definitive no-route.
func TestBFSPath_NeighborError_Propagates(t *testing.T) {
	boom := errors.New("store exploded")
	_, err := bfsPath("A", "Z", MaxJumpPath, func(string) ([]string, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("expected the neighbor error to propagate, got %v", err)
	}
	if errors.Is(err, ErrUnroutable) {
		t.Fatal("a fetch error must not be reported as ErrUnroutable")
	}
}

// --- Service.Routable / Path (fake store, no API) ---

// freshStore is an in-memory GateEdgeRepository that reports every system as a
// FRESH hit (an absent system is a charted dead-end with no connections), so
// Service.Path/Routable never falls through to the (nil) API client. edgesErr
// forces Edges to fail, exercising the fail-closed propagation.
type freshStore struct {
	adjacency map[string][]system.GateEdge
	edgesErr  error
}

func (f *freshStore) Edges(ctx context.Context, systemSymbol string) ([]system.GateEdge, bool, error) {
	if f.edgesErr != nil {
		return nil, false, f.edgesErr
	}
	return f.adjacency[systemSymbol], true, nil
}
func (f *freshStore) GateWaypointOf(ctx context.Context, s string) (string, bool, error) {
	return "", false, nil
}
func (f *freshStore) Replace(ctx context.Context, s string, e []system.GateEdge) error { return nil }
func (f *freshStore) Adjacency(ctx context.Context) (map[string][]string, error)       { return nil, nil }

func edgesTo(systems ...string) []system.GateEdge {
	edges := make([]system.GateEdge, 0, len(systems))
	for _, s := range systems {
		edges = append(edges, system.GateEdge{ConnectedSystem: s, GateWaypoint: s + "-GATE"})
	}
	return edges
}

// Routable reports a real multi-hop route as reachable without any API call
// (every hop is a fresh store hit).
func TestService_Routable_MultiHop_TrueNoFetch(t *testing.T) {
	store := &freshStore{adjacency: map[string][]system.GateEdge{
		"X1-KA42": edgesTo("X1-PA3"),
		"X1-PA3":  edgesTo("X1-UQ16"),
		"X1-UQ16": edgesTo("X1-JP61"),
	}}
	svc := NewService(store, nil, nil, nil) // nil API/graph/player: a fetch would panic, proving none happens

	ok, err := svc.Routable(context.Background(), "X1-KA42", "X1-JP61", 1)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatal("KA42→JP61 is a 3-hop route and must be reported routable")
	}
}

// A definitively unreachable destination is (false, nil): the caller refuses the
// spend, but this is a clean verdict, NOT an operational error.
func TestService_Routable_Unreachable_FalseNilError(t *testing.T) {
	store := &freshStore{adjacency: map[string][]system.GateEdge{
		"X1-KA42": edgesTo("X1-PA3"),
		"X1-PA3":  edgesTo("X1-KA42"), // closed pocket
	}}
	svc := NewService(store, nil, nil, nil)

	ok, err := svc.Routable(context.Background(), "X1-KA42", "X1-JP61", 1)
	if err != nil {
		t.Fatalf("a definitive unroutable verdict must be (false, nil), got err %v", err)
	}
	if ok {
		t.Fatal("JP61 is unreachable here and must be reported not routable")
	}
}

// A store failure must surface as (false, err) so the pre-buy guard fails
// CLOSED — it must never be mistaken for a clean "not routable".
func TestService_Routable_StoreError_FailsClosed(t *testing.T) {
	boom := errors.New("db down")
	svc := NewService(&freshStore{edgesErr: boom}, nil, nil, nil)

	ok, err := svc.Routable(context.Background(), "X1-KA42", "X1-JP61", 1)
	if err == nil {
		t.Fatal("a store error must surface (fail closed), got nil error")
	}
	if ok {
		t.Fatal("a store error must not report routable")
	}
}

// Same-system is trivially routable with no traversal at all.
func TestService_Routable_SameSystem_True(t *testing.T) {
	svc := NewService(&freshStore{}, nil, nil, nil)
	ok, err := svc.Routable(context.Background(), "X1-KA42", "X1-KA42", 1)
	if err != nil || !ok {
		t.Fatalf("same-system must be routable with no error, got ok=%v err=%v", ok, err)
	}
}

package gategraph

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
func (f *freshStore) Adjacency(ctx context.Context) (map[string][]system.GateEdge, error) {
	return nil, nil
}

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

// --- sp-8qhu: construction-aware routing ---

// The incident, encoded as a Service-level route: from KA42 BOTH the AF2 leg and
// the PA3 leg reach UQ16→JP61 at EQUAL hop count, but AF2 is UNDER CONSTRUCTION.
// The old BFS picked KA42→AF2(unbuilt)→UQ16→JP61 and the laden frigate crashed at
// hop 1; the fix must return the PA3 route and never traverse the unbuilt gate.
func TestService_Path_SkipsUnderConstructionGate_IncidentRoute(t *testing.T) {
	store := &freshStore{adjacency: map[string][]system.GateEdge{
		"X1-KA42": {
			{ConnectedSystem: "X1-AF2", GateWaypoint: "X1-AF2-GATE", UnderConstruction: true},
			{ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-GATE"},
		},
		"X1-AF2":  edgesTo("X1-UQ16"), // an equal-hop route to UQ16 — usable ONLY if AF2 were built
		"X1-PA3":  edgesTo("X1-UQ16"),
		"X1-UQ16": edgesTo("X1-JP61"),
	}}
	svc := NewService(store, nil, nil, nil)

	got, err := svc.Path(context.Background(), "X1-KA42", "X1-JP61", 1)
	if err != nil {
		t.Fatalf("expected the PA3 route, got error: %v", err)
	}
	want := []string{"X1-KA42", "X1-PA3", "X1-UQ16", "X1-JP61"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BFS must take the built PA3 route %v, never the unbuilt AF2 leg, got %v", want, got)
	}
}

// When EVERY forward path to the destination crosses an under-construction gate,
// the destination is unroutable — the pre-buy guard then refuses the spend (never
// buys cargo it cannot deliver).
func TestService_Path_AllRoutesUnbuilt_Unroutable(t *testing.T) {
	store := &freshStore{adjacency: map[string][]system.GateEdge{
		"X1-KA42": {
			{ConnectedSystem: "X1-AF2", GateWaypoint: "X1-AF2-GATE", UnderConstruction: true},
			{ConnectedSystem: "X1-QJ93", GateWaypoint: "X1-QJ93-GATE", UnderConstruction: true},
		},
		"X1-AF2":  edgesTo("X1-JP61"),
		"X1-QJ93": edgesTo("X1-JP61"),
	}}
	svc := NewService(store, nil, nil, nil)

	if _, err := svc.Path(context.Background(), "X1-KA42", "X1-JP61", 1); !errors.Is(err, ErrUnroutable) {
		t.Fatalf("every route crosses an unbuilt gate → must be ErrUnroutable, got %v", err)
	}
}

// --- sp-8qhu: fetch-through construction resolution (fake API) ---

// missStore forces the fetch-through path: Edges always misses, GateWaypointOf
// resolves the origin's own gate (so no system graph is needed), and Replace
// captures what construction state the fetch resolved onto each edge.
type missStore struct {
	originGate string
	replaced   map[string][]system.GateEdge
}

func (m *missStore) Edges(ctx context.Context, s string) ([]system.GateEdge, bool, error) {
	return nil, false, nil
}
func (m *missStore) GateWaypointOf(ctx context.Context, s string) (string, bool, error) {
	return m.originGate, true, nil
}
func (m *missStore) Replace(ctx context.Context, s string, e []system.GateEdge) error {
	if m.replaced == nil {
		m.replaced = map[string][]system.GateEdge{}
	}
	m.replaced[s] = e
	return nil
}
func (m *missStore) Adjacency(ctx context.Context) (map[string][]system.GateEdge, error) {
	return nil, nil
}

// fakeGateAPI serves a fixed connections list and per-gate construction state; a
// gate whose waypoint is in waypointErr returns that error (the read failure).
type fakeGateAPI struct {
	connections       []string
	underConstruction map[string]bool
	waypointErr       map[string]error
}

func (f *fakeGateAPI) GetJumpGate(ctx context.Context, sys, wp, tok string) (*ports.JumpGateData, error) {
	return &ports.JumpGateData{Symbol: wp, Connections: f.connections}, nil
}
func (f *fakeGateAPI) GetWaypoint(ctx context.Context, sys, wp, tok string) (*ports.WaypointDetail, error) {
	if err := f.waypointErr[wp]; err != nil {
		return nil, err
	}
	return &ports.WaypointDetail{Symbol: wp, IsUnderConstruction: f.underConstruction[wp]}, nil
}

type stubPlayerRepo struct{ token string }

func (s *stubPlayerRepo) FindByID(ctx context.Context, id shared.PlayerID) (*player.Player, error) {
	return &player.Player{Token: s.token}, nil
}
func (s *stubPlayerRepo) FindByAgentSymbol(ctx context.Context, a string) (*player.Player, error) {
	return nil, nil
}
func (s *stubPlayerRepo) ListAll(ctx context.Context) ([]*player.Player, error) { return nil, nil }
func (s *stubPlayerRepo) Add(ctx context.Context, p *player.Player) error       { return nil }

// findEdge returns the stored edge to connSystem, or a zero edge if absent.
func findEdge(edges []system.GateEdge, connSystem string) system.GateEdge {
	for _, e := range edges {
		if e.ConnectedSystem == connSystem {
			return e
		}
	}
	return system.GateEdge{}
}

// A successful fetch-through stamps each edge with the CONNECTED gate's real build
// state: the unbuilt neighbor's edge is UnderConstruction, the built one is not.
func TestService_FetchThrough_ReflectsWaypointConstructionState(t *testing.T) {
	store := &missStore{originGate: "X1-KA42-GATE"}
	api := &fakeGateAPI{
		connections:       []string{"X1-AF2-GATE", "X1-PA3-GATE"},
		underConstruction: map[string]bool{"X1-AF2-GATE": true}, // AF2 unbuilt, PA3 open
	}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"})

	edges, err := svc.Connections(context.Background(), "X1-KA42", 1)
	if err != nil {
		t.Fatalf("fetch-through must succeed, got %v", err)
	}
	if !findEdge(edges, "X1-AF2").UnderConstruction {
		t.Fatal("the AF2 edge must be marked under construction from the waypoint probe")
	}
	if findEdge(edges, "X1-PA3").UnderConstruction {
		t.Fatal("the PA3 edge must be marked open (not under construction)")
	}
	// And the same state is what got persisted (Replace saw the resolved edges).
	if !findEdge(store.replaced["X1-KA42"], "X1-AF2").UnderConstruction {
		t.Fatal("the persisted AF2 edge must carry the under-construction flag")
	}
}

// A waypoint-read FAILURE fails CLOSED: the edge is treated as under construction
// so an unknown-state gate is never routed through (the whole point of sp-8qhu).
func TestService_FetchThrough_ReadFailure_FailsClosedUnbuilt(t *testing.T) {
	store := &missStore{originGate: "X1-KA42-GATE"}
	api := &fakeGateAPI{
		connections: []string{"X1-AF2-GATE"},
		waypointErr: map[string]error{"X1-AF2-GATE": errors.New("api 500 reading waypoint")},
	}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"})

	edges, err := svc.Connections(context.Background(), "X1-KA42", 1)
	if err != nil {
		t.Fatalf("a per-gate probe failure must not fail the whole fetch — it fails the edge closed; got %v", err)
	}
	if !findEdge(edges, "X1-AF2").UnderConstruction {
		t.Fatal("a waypoint-read failure must mark the edge under construction (fail closed)")
	}
}

// The fail-closed decision in isolation: a probe error yields true regardless of
// what a (nil) detail would say.
func TestService_GateUnderConstruction_ReadError_True(t *testing.T) {
	svc := &Service{apiClient: &fakeGateAPI{waypointErr: map[string]error{"X1-AF2-GATE": errors.New("boom")}}}
	got := svc.gateUnderConstruction(context.Background(), "X1-AF2", "X1-AF2-GATE", "tok", logging.LoggerFromContext(context.Background()))
	if !got {
		t.Fatal("a probe error must fail closed (true)")
	}
}

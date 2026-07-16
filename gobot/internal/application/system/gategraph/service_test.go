package gategraph

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
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

// freshStore never misses, so the backoff path is never consulted — these satisfy the
// interface and assert (by never being exercised) that a fresh hit needs no backoff read.
func (f *freshStore) UnreadableState(ctx context.Context, s string) (int, time.Time, bool, error) {
	return 0, time.Time{}, false, nil
}
func (f *freshStore) MarkUnreadable(ctx context.Context, s, gate string, now time.Time) (int, error) {
	return 0, nil
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

// missStore's fetch-through tests all SUCCEED on GetJumpGate, so backoff is never
// entered — a no-op backoff (never backed off) keeps every miss flowing to the probe.
func (m *missStore) UnreadableState(ctx context.Context, s string) (int, time.Time, bool, error) {
	return 0, time.Time{}, false, nil
}
func (m *missStore) MarkUnreadable(ctx context.Context, s, gate string, now time.Time) (int, error) {
	return 0, nil
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

// --- sp-8qhu deploy-gap: Path re-probes an INVALIDATED origin before trusting it ---

// The exact post-deploy live scenario, end-to-end over the PRODUCTION store: KA42's
// edges were invalidated by the migration (synced_at="", under_construction=f
// default). A Path query from KA42 must NOT trust the stale OPEN default — the
// empty synced_at makes the set read as a MISS, forcing Connections()→fetchAndStore
// to re-fetch and RE-PROBE AF2's real (under construction) state before the BFS
// routes. Uses the real GormGateEdgeRepository (not a fake), because the staleness
// enforcement that makes this safe lives in the store.
func TestService_Path_InvalidatedOriginRow_ReprobesBeforeRouting(t *testing.T) {
	db, err := database.NewTestConnection()
	if err != nil {
		t.Fatalf("test db: %v", err)
	}
	if err := db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error; err != nil {
		t.Fatalf("seed era: %v", err)
	}
	ptr := func(i int) *int { return &i }
	fresh := time.Now().Format(time.RFC3339)

	// INVALIDATED KA42 edges (empty synced_at + pre-tracking OPEN default) — exactly
	// the rows the deploy left behind — plus a neighbor row so GateWaypointOf(KA42)
	// resolves KA42's own gate on re-fetch, and FRESH downstream hops so only KA42
	// needs a re-fetch.
	seed := []persistence.GateEdgeModel{
		{SystemSymbol: "X1-KA42", ConnectedSystem: "X1-AF2", GateWaypoint: "X1-AF2-GATE", EraID: ptr(1), SyncedAt: "", UnderConstruction: false},
		{SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-GATE", EraID: ptr(1), SyncedAt: "", UnderConstruction: false},
		{SystemSymbol: "X1-GQ92", ConnectedSystem: "X1-KA42", GateWaypoint: "X1-KA42-GATE", EraID: ptr(1), SyncedAt: fresh, UnderConstruction: false},
		{SystemSymbol: "X1-PA3", ConnectedSystem: "X1-UQ16", GateWaypoint: "X1-UQ16-GATE", EraID: ptr(1), SyncedAt: fresh, UnderConstruction: false},
		{SystemSymbol: "X1-UQ16", ConnectedSystem: "X1-JP61", GateWaypoint: "X1-JP61-GATE", EraID: ptr(1), SyncedAt: fresh, UnderConstruction: false},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed edge %d: %v", i, err)
		}
	}

	store := persistence.NewGormGateEdgeRepository(db)
	// On the KA42 re-fetch the live gate reports AF2 still under construction, PA3 open.
	api := &fakeGateAPI{
		connections:       []string{"X1-AF2-GATE", "X1-PA3-GATE"},
		underConstruction: map[string]bool{"X1-AF2-GATE": true},
	}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"})

	got, err := svc.Path(context.Background(), "X1-KA42", "X1-JP61", 1)
	if err != nil {
		t.Fatalf("expected the re-probed PA3 route, got error: %v", err)
	}
	want := []string{"X1-KA42", "X1-PA3", "X1-UQ16", "X1-JP61"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("an invalidated origin must re-probe and route via PA3 %v, got %v", want, got)
	}

	// The invalidated AF2 row was re-probed and PERSISTED with the real state —
	// proof the stale OPEN default was never trusted for routing.
	edges, ok, err := store.Edges(context.Background(), "X1-KA42")
	if err != nil || !ok {
		t.Fatalf("KA42 edges must now be a fresh hit after the re-probe, ok=%v err=%v", ok, err)
	}
	if !findEdge(edges, "X1-AF2").UnderConstruction {
		t.Fatal("the re-probe must persist AF2 as under construction, not the stale open default")
	}
}

// --- sp-qxa4: one unreadable gate must not abort an unrelated route ---

// perSystemMissStore forces fetch-through for every EDGE read (Edges always misses),
// resolves each system's own gate as "<system>-GATE" (so GateWaypointOf never needs a
// system graph), and carries a real in-memory negative-result backoff (backoff map) so a
// test can drive the persisted-backoff behavior with a MockClock exactly as the GORM store
// would. replaced records Replace calls (edge writes); backoff records MarkUnreadable
// (marker writes) — the two are separate, matching production, so a test can prove an
// unreadable system is MARKED (backoff) rather than written as an edge set.
type perSystemMissStore struct {
	replaced map[string][]system.GateEdge
	backoff  map[string]backoffEntry
}

// backoffEntry is one system's in-memory marker: consecutive failures + last-probe time.
type backoffEntry struct {
	attempts  int
	lastProbe time.Time
}

func (m *perSystemMissStore) Edges(ctx context.Context, s string) ([]system.GateEdge, bool, error) {
	return nil, false, nil
}
func (m *perSystemMissStore) GateWaypointOf(ctx context.Context, s string) (string, bool, error) {
	return s + "-GATE", true, nil
}
func (m *perSystemMissStore) Replace(ctx context.Context, s string, e []system.GateEdge) error {
	if m.replaced == nil {
		m.replaced = map[string][]system.GateEdge{}
	}
	m.replaced[s] = e
	// A successful edge write clears any backoff marker — a gate that becomes readable
	// again self-heals off the backoff clock (matches GormGateEdgeRepository.Replace).
	delete(m.backoff, s)
	return nil
}
func (m *perSystemMissStore) Adjacency(ctx context.Context) (map[string][]system.GateEdge, error) {
	return nil, nil
}
func (m *perSystemMissStore) UnreadableState(ctx context.Context, s string) (int, time.Time, bool, error) {
	e, ok := m.backoff[s]
	if !ok {
		return 0, time.Time{}, false, nil
	}
	return e.attempts, e.lastProbe, true, nil
}
func (m *perSystemMissStore) MarkUnreadable(ctx context.Context, s, gate string, now time.Time) (int, error) {
	if m.backoff == nil {
		m.backoff = map[string]backoffEntry{}
	}
	attempts := m.backoff[s].attempts + 1
	m.backoff[s] = backoffEntry{attempts: attempts, lastProbe: now}
	return attempts, nil
}

// perSystemGateAPI serves each origin system's own connection list and can fail
// GetJumpGate for a specific gate waypoint (the unreadable frontier gate). Every
// probed waypoint reads as built unless listed under construction. jumpGateCalls counts
// GetJumpGate invocations per gate waypoint so a test can prove the backoff actually
// SUPPRESSES probes (the sp-ikx1 storm-stop), not merely that routing still works.
type perSystemGateAPI struct {
	connectionsBySystem map[string][]string // origin system -> connected gate waypoints
	jumpGateErr         map[string]error    // gate waypoint -> live-fetch error (unreadable)
	underConstruction   map[string]bool
	jumpGateCalls       map[string]int // gate waypoint -> number of GetJumpGate probes
}

func (f *perSystemGateAPI) GetJumpGate(ctx context.Context, sys, wp, tok string) (*ports.JumpGateData, error) {
	if f.jumpGateCalls == nil {
		f.jumpGateCalls = map[string]int{}
	}
	f.jumpGateCalls[wp]++
	if err := f.jumpGateErr[wp]; err != nil {
		return nil, err
	}
	return &ports.JumpGateData{Symbol: wp, Connections: f.connectionsBySystem[sys]}, nil
}
func (f *perSystemGateAPI) GetWaypoint(ctx context.Context, sys, wp, tok string) (*ports.WaypointDetail, error) {
	return &ports.WaypointDetail{Symbol: wp, IsUnderConstruction: f.underConstruction[wp]}, nil
}

// captureLogger records log messages so a test can assert the honest skip line.
type captureLogger struct{ messages []string }

func (l *captureLogger) Log(_, message string, _ map[string]interface{}) {
	l.messages = append(l.messages, message)
}

// apiErr400 is the typed terminal client error the real SpaceTraders adapter now returns for a
// non-2xx GetJumpGate response (sp-4bm3): a *ports.APIError carrying StatusCode 400. The fakes
// use it so they model a GENUINE HTTP 400 (uncharted / no ship present / not a gate) — the
// permanent verdict the negative-result backoff is meant to suppress — rather than an untyped
// stand-in a transient blip could masquerade as.
func apiErr400(body string) error {
	return &ports.APIError{StatusCode: 400, Body: body}
}

// The incident (TORWIND-21 DP51→KA42): DP51 gates to BOTH X1-XX56 (an unswept
// frontier system whose live gate fetch 400s — "no ship present") and X1-MID (which
// reaches KA42). One unreadable sibling gate must NOT abort the route — the BFS
// excludes XX56 and routes DP51→MID→KA42. XX56 is not written as an edge set (it is
// recorded as a backoff MARKER instead, sp-ikx1), and the honest enter-backoff line
// names the excluded gate.
func TestService_Path_UnreadableSiblingGate_RoutesAround(t *testing.T) {
	store := &perSystemMissStore{}
	api := &perSystemGateAPI{
		connectionsBySystem: map[string][]string{
			"X1-DP51": {"X1-XX56-GATE", "X1-MID-GATE"}, // XX56 listed first → expanded first
			"X1-MID":  {"X1-KA42-GATE"},
		},
		jumpGateErr: map[string]error{
			"X1-XX56-GATE": apiErr400("waypoint X1-XX56-GATE not accessible, no ship present"),
		},
	}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"})

	logger := &captureLogger{}
	ctx := logging.WithLogger(context.Background(), logger)
	got, err := svc.Path(ctx, "X1-DP51", "X1-KA42", 1)
	if err != nil {
		t.Fatalf("one unreadable sibling gate must not abort the route, got error: %v", err)
	}
	want := []string{"X1-DP51", "X1-MID", "X1-KA42"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected the route around the unreadable gate %v, got %v", want, got)
	}
	// XX56 was NOT written as an edge set (it is a backoff marker, not a readable system).
	if _, persisted := store.replaced["X1-XX56"]; persisted {
		t.Fatal("the unreadable system must not be written as an edge set")
	}
	// It IS recorded as a backoff marker so the next tick does not re-probe it (sp-ikx1).
	if _, backedOff, _ := backoffOf(store, "X1-XX56"); !backedOff {
		t.Fatal("the unreadable system must be recorded as backed off")
	}
	// The skip is logged honestly (the enter-backoff line), naming the excluded gate.
	found := false
	for _, m := range logger.messages {
		if strings.Contains(m, "X1-XX56") && strings.Contains(m, "unreadable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the excluded gate must be logged honestly, got messages: %v", logger.messages)
	}
}

// backoffOf is a tiny helper reading the in-memory store's backoff for assertions.
func backoffOf(store *perSystemMissStore, system string) (attempts int, ok bool, lastProbe time.Time) {
	a, lp, present, _ := store.UnreadableState(context.Background(), system)
	return a, present, lp
}

// When the ONLY path to the destination runs through the unreadable gate, fail-closed
// holds: the route is an honest ErrUnroutable, never silently rerouted through the
// unverified gate.
func TestService_Path_UnreadableGateRequired_Unroutable(t *testing.T) {
	store := &perSystemMissStore{}
	api := &perSystemGateAPI{
		connectionsBySystem: map[string][]string{
			"X1-DP51": {"X1-XX56-GATE"}, // the only way out of DP51 is the unreadable gate
		},
		jumpGateErr: map[string]error{
			"X1-XX56-GATE": apiErr400("not accessible, no ship present"),
		},
	}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"})

	if _, err := svc.Path(context.Background(), "X1-DP51", "X1-KA42", 1); !errors.Is(err, ErrUnroutable) {
		t.Fatalf("a route that requires the unreadable gate must be ErrUnroutable (fail-closed), got %v", err)
	}
}

// A live GetJumpGate failure surfaces as ErrGateUnreadable — distinct from a store
// error — is NOT written as an edge set, and records a backoff marker.
func TestService_Connections_GetJumpGateFailure_UnreadableAndMarked(t *testing.T) {
	store := &perSystemMissStore{}
	api := &perSystemGateAPI{
		jumpGateErr: map[string]error{"X1-XX56-GATE": apiErr400("no ship present")},
	}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"})

	_, err := svc.Connections(context.Background(), "X1-XX56", 1)
	if !errors.Is(err, ErrGateUnreadable) {
		t.Fatalf("a live gate-fetch failure must be ErrGateUnreadable, got %v", err)
	}
	if _, persisted := store.replaced["X1-XX56"]; persisted {
		t.Fatal("an unreadable system must not be written as an edge set")
	}
	if _, backedOff, _ := backoffOf(store, "X1-XX56"); !backedOff {
		t.Fatal("an unreadable system must be recorded as backed off")
	}
}

// --- sp-ikx1: unreadable-gate negative-result backoff ---

// The storm stop: once an unreadable gate has been probed and 400'd, it is NOT
// re-probed on the next tick — the persisted backoff suppresses the API call until the
// window elapses. This is the 1 req/s of guaranteed 400s the fix reclaims.
func TestService_Connections_UnreadableGate_BacksOffNoReprobe(t *testing.T) {
	store := &perSystemMissStore{}
	api := &perSystemGateAPI{jumpGateErr: map[string]error{"X1-XX56-GATE": apiErr400("no ship present")}}
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	clock := &shared.MockClock{CurrentTime: base}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"}, WithClock(clock))
	ctx := context.Background()

	// First fetch: a real probe that 400s.
	if _, err := svc.Connections(ctx, "X1-XX56", 1); !errors.Is(err, ErrGateUnreadable) {
		t.Fatalf("first fetch must be ErrGateUnreadable, got %v", err)
	}
	if got := api.jumpGateCalls["X1-XX56-GATE"]; got != 1 {
		t.Fatalf("first fetch must probe exactly once, got %d", got)
	}

	// 30s later (well within the 5m initial window): still unreadable, but NO new probe.
	clock.Advance(30 * time.Second)
	if _, err := svc.Connections(ctx, "X1-XX56", 1); !errors.Is(err, ErrGateUnreadable) {
		t.Fatalf("a backed-off gate must still report unreadable, got %v", err)
	}
	if got := api.jumpGateCalls["X1-XX56-GATE"]; got != 1 {
		t.Fatalf("storm-stop: a backed-off gate must NOT be re-probed (want 1 probe, got %d)", got)
	}
}

// The distinction (sp-4bm3): a TRANSIENT gate-fetch failure — a 5xx / network / retry-exhausted
// error, which never surfaces as a *ports.APIError — must NOT be negative-cached. It still
// reports ErrGateUnreadable (so the BFS routes around it this tick), but because it was not
// backed off it is RE-PROBED on the very next miss, so a momentary API blip never suppresses a
// real gate for the whole 5m→30m→2h window. Contrast the 400 above, which IS suppressed. This is
// also the mutation guard for the carve-out: drop the 4xx classification (back off every error
// again) and the second miss stops re-probing, failing this test.
func TestService_Connections_TransientError_NotCached_Reprobes(t *testing.T) {
	store := &perSystemMissStore{}
	// The shape a retry-exhausted transient surfaces as (doWithRetry wraps *retryableError):
	// a plain error, NOT a *ports.APIError — exactly what the classifier must decline to cache.
	api := &perSystemGateAPI{jumpGateErr: map[string]error{"X1-XX56-GATE": errors.New("max retries exceeded: server error (503)")}}
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	clock := &shared.MockClock{CurrentTime: base}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"}, WithClock(clock))
	ctx := context.Background()

	// First miss: a real probe that fails transiently → still ErrGateUnreadable (route around).
	if _, err := svc.Connections(ctx, "X1-XX56", 1); !errors.Is(err, ErrGateUnreadable) {
		t.Fatalf("a transient failure must still surface ErrGateUnreadable, got %v", err)
	}
	if got := api.jumpGateCalls["X1-XX56-GATE"]; got != 1 {
		t.Fatalf("first miss must probe once, got %d", got)
	}
	// It must NOT have been negative-cached (no backoff marker recorded).
	if _, backedOff, _ := backoffOf(store, "X1-XX56"); backedOff {
		t.Fatal("a transient failure must NOT enter the negative-result backoff")
	}

	// Next miss only 1s later (a 400 would still be suppressed here): because it was not cached,
	// the gate is RE-PROBED — transient failures keep retrying.
	clock.Advance(time.Second)
	if _, err := svc.Connections(ctx, "X1-XX56", 1); !errors.Is(err, ErrGateUnreadable) {
		t.Fatalf("second miss still unreadable, got %v", err)
	}
	if got := api.jumpGateCalls["X1-XX56-GATE"]; got != 2 {
		t.Fatalf("a transient (uncached) gate must be re-probed on the next miss (want 2, got %d)", got)
	}
}

// The re-probe interval follows the ruled 5m → 30m → 2h schedule, driven end-to-end
// through the service and store with a controllable clock: a probe fires only once its
// window has elapsed, and the window escalates then caps.
func TestService_Connections_BackoffEscalatesThenCaps(t *testing.T) {
	store := &perSystemMissStore{}
	api := &perSystemGateAPI{jumpGateErr: map[string]error{"X1-XX56-GATE": apiErr400("no ship present")}}
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	clock := &shared.MockClock{CurrentTime: base}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"}, WithClock(clock))
	ctx := context.Background()
	probe := func() { _, _ = svc.Connections(ctx, "X1-XX56", 1) }
	calls := func() int { return api.jumpGateCalls["X1-XX56-GATE"] }

	probe() // t0: attempt 1 → next probe at t0+5m
	if calls() != 1 {
		t.Fatalf("want 1 probe after first fetch, got %d", calls())
	}

	// Just before 5m: no probe. At 5m: probe (attempt 2 → next +30m).
	clock.Advance(5*time.Minute - time.Second)
	probe()
	if calls() != 1 {
		t.Fatalf("before the 5m window elapses the gate must not be re-probed, got %d", calls())
	}
	clock.Advance(time.Second)
	probe()
	if calls() != 2 {
		t.Fatalf("at 5m the gate must be re-probed once (want 2 total, got %d)", calls())
	}

	// Just before +30m: no probe. At +30m: probe (attempt 3 → next +2h).
	clock.Advance(30*time.Minute - time.Second)
	probe()
	if calls() != 2 {
		t.Fatalf("the 30m window must hold, got %d probes", calls())
	}
	clock.Advance(time.Second)
	probe()
	if calls() != 3 {
		t.Fatalf("at +30m the gate must be re-probed (want 3 total, got %d)", calls())
	}

	// Just before +2h: no probe. At +2h: probe (attempt 4 → still capped at +2h).
	clock.Advance(2*time.Hour - time.Second)
	probe()
	if calls() != 3 {
		t.Fatalf("the 2h cap window must hold, got %d probes", calls())
	}
	clock.Advance(time.Second)
	probe()
	if calls() != 4 {
		t.Fatalf("at +2h the capped window must re-probe (want 4 total, got %d)", calls())
	}
}

// The schedule is a pure function of the config, so the ruled default and an arbitrary
// custom schedule both compute correctly — RULINGS #5: the windows are knobs, not
// constants baked into the code.
func TestBackoffSchedule_DurationFor(t *testing.T) {
	def := DefaultBackoffSchedule // 5m, ×6, cap 2h
	for _, c := range []struct {
		attempts int
		want     time.Duration
	}{
		{1, 5 * time.Minute}, {2, 30 * time.Minute}, {3, 2 * time.Hour}, {4, 2 * time.Hour}, {50, 2 * time.Hour},
	} {
		if got := def.durationFor(c.attempts); got != c.want {
			t.Fatalf("default schedule attempt %d: want %v, got %v", c.attempts, c.want, got)
		}
	}

	custom := BackoffSchedule{Initial: time.Minute, Multiplier: 2, Max: 10 * time.Minute} // 1,2,4,8,cap 10
	for _, c := range []struct {
		attempts int
		want     time.Duration
	}{
		{1, time.Minute}, {2, 2 * time.Minute}, {3, 4 * time.Minute}, {4, 8 * time.Minute}, {5, 10 * time.Minute}, {6, 10 * time.Minute},
	} {
		if got := custom.durationFor(c.attempts); got != c.want {
			t.Fatalf("custom schedule attempt %d: want %v, got %v", c.attempts, c.want, got)
		}
	}
}

// A READABLE gate is untouched by the backoff: it fetches cleanly, is written as an edge
// set, and never records a backoff marker.
func TestService_Connections_ReadableGate_NeverBacksOff(t *testing.T) {
	store := &perSystemMissStore{}
	api := &perSystemGateAPI{connectionsBySystem: map[string][]string{"X1-OK": {"X1-NBR-GATE"}}}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"})

	edges, err := svc.Connections(context.Background(), "X1-OK", 1)
	if err != nil {
		t.Fatalf("a readable gate must fetch cleanly, got %v", err)
	}
	if len(edges) != 1 || edges[0].ConnectedSystem != "X1-NBR" {
		t.Fatalf("expected the one real edge to X1-NBR, got %v", edges)
	}
	if _, backedOff, _ := backoffOf(store, "X1-OK"); backedOff {
		t.Fatal("a readable gate must never be backed off")
	}
	if _, persisted := store.replaced["X1-OK"]; !persisted {
		t.Fatal("a readable gate's edges must be written as an edge set")
	}
}

// The operator signal: exactly ONE enter-backoff INFO line per probe failure, carrying
// the next-probe time; the skipped re-checks between windows are silent (this is what
// replaces the old ~23k-line per-tick spam).
func TestService_Backoff_LogsOnceWithNextProbe_SilentBetween(t *testing.T) {
	store := &perSystemMissStore{}
	api := &perSystemGateAPI{jumpGateErr: map[string]error{"X1-XX56-GATE": apiErr400("no ship present")}}
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	clock := &shared.MockClock{CurrentTime: base}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"}, WithClock(clock))
	logger := &captureLogger{}
	ctx := logging.WithLogger(context.Background(), logger)

	// First probe fails → one enter-backoff line carrying the next-probe time (t0+5m).
	_, _ = svc.Connections(ctx, "X1-XX56", 1)
	wantNext := base.Add(5 * time.Minute).Format(time.RFC3339)
	enterLines := 0
	for _, m := range logger.messages {
		if strings.Contains(m, "backing off") && strings.Contains(m, wantNext) {
			enterLines++
		}
	}
	if enterLines != 1 {
		t.Fatalf("expected exactly one enter-backoff line carrying next-probe %s, got messages: %v", wantNext, logger.messages)
	}

	// A skipped re-check within the window logs nothing new (silence between).
	countAfterEnter := len(logger.messages)
	clock.Advance(30 * time.Second)
	_, _ = svc.Connections(ctx, "X1-XX56", 1)
	if len(logger.messages) != countAfterEnter {
		t.Fatalf("a backed-off skip must be silent, new messages: %v", logger.messages[countAfterEnter:])
	}
}

// --- RepositionPath: the expendable-probe stored-adjacency resolver (sp-8k9m) ---

// adjStore serves a fixed stored adjacency and nothing else. RepositionPath must resolve
// entirely from Adjacency() — a store-only read — so Edges/UnreadableState are never
// consulted and no live fetch is triggered (the ikx1 backoff stays fully honored).
type adjStore struct {
	adjacency map[string][]system.GateEdge
	adjErr    error
}

func (a *adjStore) Adjacency(ctx context.Context) (map[string][]system.GateEdge, error) {
	return a.adjacency, a.adjErr
}
func (a *adjStore) Edges(ctx context.Context, s string) ([]system.GateEdge, bool, error) {
	return nil, false, errors.New("RepositionPath must not read per-system Edges (no fetch-through)")
}
func (a *adjStore) GateWaypointOf(ctx context.Context, s string) (string, bool, error) {
	return "", false, errors.New("RepositionPath must not resolve gate waypoints")
}
func (a *adjStore) Replace(ctx context.Context, s string, e []system.GateEdge) error { return nil }
func (a *adjStore) UnreadableState(ctx context.Context, s string) (int, time.Time, bool, error) {
	return 0, time.Time{}, false, errors.New("RepositionPath must not consult the unreadable backoff")
}
func (a *adjStore) MarkUnreadable(ctx context.Context, s, gate string, now time.Time) (int, error) {
	return 0, nil
}

func repoEdgesTo(systems ...string) []system.GateEdge {
	edges := make([]system.GateEdge, 0, len(systems))
	for _, s := range systems {
		edges = append(edges, system.GateEdge{ConnectedSystem: s, GateWaypoint: s + "-GATE"})
	}
	return edges
}

// A 6-jump frontier route (deeper than MaxJumpPath=5) resolves under a raised bound —
// this is the whole point of the expendable-probe resolver: reach a post the strict
// 5-jump cap rejects. The SAME route under a 5-jump bound is unroutable, proving the
// bound (not luck) is what admits it.
func TestRepositionPath_ReachesBeyondMaxJumpPath(t *testing.T) {
	store := &adjStore{adjacency: map[string][]system.GateEdge{
		"X1-A": repoEdgesTo("X1-B"),
		"X1-B": repoEdgesTo("X1-C"),
		"X1-C": repoEdgesTo("X1-D"),
		"X1-D": repoEdgesTo("X1-E"),
		"X1-E": repoEdgesTo("X1-F"),
		"X1-F": repoEdgesTo("X1-G"), // G is 6 jumps from A
	}}
	svc := NewService(store, nil, nil, nil) // nil API: any fetch-through would panic, proving none happens

	got, err := svc.RepositionPath(context.Background(), "X1-A", "X1-G", 12)
	if err != nil {
		t.Fatalf("a 6-jump route under a 12-jump bound must resolve, got %v", err)
	}
	want := []string{"X1-A", "X1-B", "X1-C", "X1-D", "X1-E", "X1-F", "X1-G"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected the 6-jump path %v, got %v", want, got)
	}

	if _, err := svc.RepositionPath(context.Background(), "X1-A", "X1-G", 5); !errors.Is(err, ErrUnroutable) {
		t.Fatalf("the same route under a 5-jump bound must be ErrUnroutable, got %v", err)
	}
}

// The resolver reads ONLY the stored adjacency: with a nil API client (a fetch would
// panic) and a store whose per-system Edges/UnreadableState error out, a multi-hop route
// still resolves. This is stipulation 2 — route PAST unreadable gates over the persisted
// edges, never re-probe them.
func TestRepositionPath_StoredAdjacencyOnly_NoFetchNoReprobe(t *testing.T) {
	store := &adjStore{adjacency: map[string][]system.GateEdge{
		"X1-KN67": repoEdgesTo("X1-PA62"),
		"X1-PA62": repoEdgesTo("X1-GD32"),
		"X1-GD32": repoEdgesTo("X1-SN21"),
	}}
	svc := NewService(store, nil, nil, nil)

	got, err := svc.RepositionPath(context.Background(), "X1-KN67", "X1-SN21", 12)
	if err != nil {
		t.Fatalf("a stored-adjacency route must resolve with no fetch, got %v", err)
	}
	want := []string{"X1-KN67", "X1-PA62", "X1-GD32", "X1-SN21"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

// An under-construction gate is still excluded (stipulation 3): a jump into an unbuilt
// gate crashes at hop time (sp-8qhu) — a hazard just as real for a probe. When the only
// route runs through one, the resolver refuses; a built alternate is taken instead.
func TestRepositionPath_ExcludesUnderConstruction(t *testing.T) {
	store := &adjStore{adjacency: map[string][]system.GateEdge{
		"X1-A": {
			{ConnectedSystem: "X1-BAD", GateWaypoint: "X1-BAD-GATE", UnderConstruction: true},
			{ConnectedSystem: "X1-OK", GateWaypoint: "X1-OK-GATE"},
		},
		"X1-BAD": repoEdgesTo("X1-DEST"),
		"X1-OK":  repoEdgesTo("X1-DEST"),
	}}
	svc := NewService(store, nil, nil, nil)

	got, err := svc.RepositionPath(context.Background(), "X1-A", "X1-DEST", 12)
	if err != nil {
		t.Fatalf("the built alternate must resolve, got %v", err)
	}
	want := []string{"X1-A", "X1-OK", "X1-DEST"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("must route through the BUILT gate, got %v", got)
	}

	// With ONLY the under-construction route, the resolver refuses (never into an unbuilt gate).
	onlyUnbuilt := &adjStore{adjacency: map[string][]system.GateEdge{
		"X1-A":   {{ConnectedSystem: "X1-BAD", GateWaypoint: "X1-BAD-GATE", UnderConstruction: true}},
		"X1-BAD": repoEdgesTo("X1-DEST"),
	}}
	svc2 := NewService(onlyUnbuilt, nil, nil, nil)
	if _, err := svc2.RepositionPath(context.Background(), "X1-A", "X1-DEST", 12); !errors.Is(err, ErrUnroutable) {
		t.Fatalf("an only-under-construction route must be ErrUnroutable, got %v", err)
	}
}

// An unreachable destination is an ErrUnroutable-wrapped error naming both systems — the
// honest refusal the worker surfaces, never a panic or empty path.
func TestRepositionPath_Unroutable_NamedError(t *testing.T) {
	store := &adjStore{adjacency: map[string][]system.GateEdge{
		"X1-A": repoEdgesTo("X1-B"),
		"X1-B": repoEdgesTo("X1-A"), // closed pocket
	}}
	svc := NewService(store, nil, nil, nil)

	_, err := svc.RepositionPath(context.Background(), "X1-A", "X1-ZZZ", 12)
	if !errors.Is(err, ErrUnroutable) {
		t.Fatalf("expected ErrUnroutable, got %v", err)
	}
	if !strings.Contains(err.Error(), "X1-A") || !strings.Contains(err.Error(), "X1-ZZZ") {
		t.Fatalf("the refusal must name both systems, got %q", err.Error())
	}
}

// A store read failure fails CLOSED (a real error), never a clean unroutable verdict —
// the worker must not strand a hull on a transient DB hiccup dressed up as "no path".
func TestRepositionPath_StoreError_FailsClosed(t *testing.T) {
	store := &adjStore{adjErr: errors.New("db down")}
	svc := NewService(store, nil, nil, nil)

	_, err := svc.RepositionPath(context.Background(), "X1-A", "X1-B", 12)
	if err == nil {
		t.Fatal("a store error must surface, not be swallowed as unroutable")
	}
	if errors.Is(err, ErrUnroutable) {
		t.Fatalf("a store failure must NOT be reported as ErrUnroutable, got %v", err)
	}
}

// A zero/absent bound falls back to MaxJumpPath (the documented default), so a caller that
// forgets to pass one still gets the strict cap rather than an unbounded walk.
func TestRepositionPath_ZeroBound_FallsBackToMaxJumpPath(t *testing.T) {
	// A 5-jump chain resolves at the fallback; a 6-jump one does not.
	store := &adjStore{adjacency: map[string][]system.GateEdge{
		"X1-A": repoEdgesTo("X1-B"),
		"X1-B": repoEdgesTo("X1-C"),
		"X1-C": repoEdgesTo("X1-D"),
		"X1-D": repoEdgesTo("X1-E"),
		"X1-E": repoEdgesTo("X1-F"), // F is 5 jumps from A
	}}
	svc := NewService(store, nil, nil, nil)

	if _, err := svc.RepositionPath(context.Background(), "X1-A", "X1-F", 0); err != nil {
		t.Fatalf("a 5-jump route must resolve at the MaxJumpPath fallback, got %v", err)
	}
	store.adjacency["X1-F"] = repoEdgesTo("X1-G") // now 6 jumps
	if _, err := svc.RepositionPath(context.Background(), "X1-A", "X1-G", 0); !errors.Is(err, ErrUnroutable) {
		t.Fatalf("a 6-jump route must exceed the MaxJumpPath fallback, got %v", err)
	}
}

// --- sp-bcsu: ChartPresentGate (the present-ship gate read that heals the frontier) ---

// A hull physically on a system's jump gate is the ONE moment its outbound connections
// are readable. ChartPresentGate BYPASSES the sp-ikx1 negative-result backoff — a plain
// Connections would skip an already-latched system even with a ship standing on its gate
// — so a now-succeeding present read HEALS the latch: it probes past the backoff, persists
// the edges, and store.Replace clears the marker. MUTATION GUARD: implement ChartPresentGate
// as a plain Connections (honoring the latch) and this fails — the probe never fires
// (jumpGateCalls stays 0), the edges are never written, and the latch is never cleared.
func TestService_ChartPresentGate_BypassesBackoff_HealsLatchedSystem(t *testing.T) {
	store := &perSystemMissStore{}
	api := &perSystemGateAPI{connectionsBySystem: map[string][]string{"X1-QF75": {"X1-NBR-GATE"}}}
	base := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	clock := &shared.MockClock{CurrentTime: base}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"}, WithClock(clock))
	ctx := context.Background()

	// Seed QF75 as backed off with the last probe JUST now — the latch is live (well
	// within the 5m window), so the plain path is suppressed and never probes.
	if _, err := store.MarkUnreadable(ctx, "X1-QF75", "X1-QF75-GATE", base); err != nil {
		t.Fatalf("seed backoff: %v", err)
	}
	if _, err := svc.Connections(ctx, "X1-QF75", 1); !errors.Is(err, ErrGateUnreadable) {
		t.Fatalf("precondition: a live latch must suppress Connections, got %v", err)
	}
	if got := api.jumpGateCalls["X1-QF75-GATE"]; got != 0 {
		t.Fatalf("precondition: the suppressed Connections must NOT probe, got %d", got)
	}

	// ChartPresentGate bypasses the live latch: it probes, succeeds, persists the edges.
	edges, err := svc.ChartPresentGate(ctx, "X1-QF75", 1)
	if err != nil {
		t.Fatalf("a present-ship read must succeed and heal, got %v", err)
	}
	if len(edges) != 1 || edges[0].ConnectedSystem != "X1-NBR" {
		t.Fatalf("expected the one real edge to X1-NBR, got %v", edges)
	}
	if got := api.jumpGateCalls["X1-QF75-GATE"]; got != 1 {
		t.Fatalf("ChartPresentGate must probe past the latch exactly once, got %d", got)
	}
	if _, persisted := store.replaced["X1-QF75"]; !persisted {
		t.Fatal("the healed system's edges must be written as an edge set")
	}
	if _, backedOff, _ := backoffOf(store, "X1-QF75"); backedOff {
		t.Fatal("a successful present-ship read must clear the backoff latch (self-heal via Replace)")
	}
}

// ChartPresentGate is idempotent (GUARD 1): a system that is ALREADY charted (Edges is a
// fresh, non-empty hit) short-circuits with ZERO API — an arrival on a known system costs
// one store lookup, never a GetJumpGate. MUTATION GUARD: drop the len>0 early return and
// the nil API client panics on the needless fetch.
func TestService_ChartPresentGate_AlreadyCharted_NoAPI(t *testing.T) {
	store := &freshStore{adjacency: map[string][]system.GateEdge{"X1-QF75": edgesTo("X1-NBR")}}
	svc := NewService(store, nil, nil, nil) // nil API: any fetch-through would panic, proving none happens

	edges, err := svc.ChartPresentGate(context.Background(), "X1-QF75", 1)
	if err != nil {
		t.Fatalf("an already-charted system must early-return cleanly, got %v", err)
	}
	if len(edges) != 1 || edges[0].ConnectedSystem != "X1-NBR" {
		t.Fatalf("expected the stored edge to X1-NBR returned as-is, got %v", edges)
	}
}

// GUARD 2 (the negative cache stays intact): a present-ship read that STILL 400s (the gate
// genuinely refuses even with the ship there) must re-enter the sp-4bm3 negative-result
// backoff unchanged — ChartPresentGate must not defeat the negative cache. It surfaces
// ErrGateUnreadable and records the marker, exactly as an ordinary fetch would.
func TestService_ChartPresentGate_StillFailing_ReentersBackoff(t *testing.T) {
	store := &perSystemMissStore{}
	api := &perSystemGateAPI{jumpGateErr: map[string]error{"X1-QF75-GATE": apiErr400("no ship present")}}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"})

	_, err := svc.ChartPresentGate(context.Background(), "X1-QF75", 1)
	if !errors.Is(err, ErrGateUnreadable) {
		t.Fatalf("a still-failing present read must surface ErrGateUnreadable, got %v", err)
	}
	if _, backedOff, _ := backoffOf(store, "X1-QF75"); !backedOff {
		t.Fatal("a still-failing present read must (re-)enter the negative-result backoff")
	}
}

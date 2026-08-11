package gategraph

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// gate_sibling_staleness_test.go guards the SURVIVAL of the build-completion re-probe after
// the sibling-staleness fix moved its trigger.
//
// The 2h under-construction window used to reach the fetch-through resolver by condemning the
// whole edge set: Edges returned ok=false, and every consumer of that miss re-fetched. The fix
// stops the condemnation — a still-building exit no longer erases its built siblings — so the
// set now reads as a HIT with the expired row flagged Stale. If the resolver kept early-
// returning on the bare hit, a "still building" verdict would be held for a full 24h and a
// route that opened in between would be refused: exactly the failure the shorter window exists
// to prevent. The re-probe therefore keys on the PER-ROW flag, and these tests hold it there.

// reprobeStore reports a MIXED set as a fresh HIT with the under-construction row flagged
// Stale — precisely what the fixed repository returns for a 3h-old production set. Fetches
// are counted so "did the resolver still go and look?" is directly observable.
type reprobeStore struct {
	edges    []system.GateEdge
	replaced int
}

func (s *reprobeStore) Edges(ctx context.Context, systemSymbol string) ([]system.GateEdge, bool, error) {
	// ok=TRUE: the set is trustworthy for routing. One row is merely due a re-probe.
	return s.edges, true, nil
}
func (s *reprobeStore) GateWaypointOf(ctx context.Context, sys string) (string, bool, error) {
	return "X1-RJ93-GATE", true, nil
}
func (s *reprobeStore) Replace(ctx context.Context, sys string, e []system.GateEdge) error {
	s.replaced++
	return nil
}
func (s *reprobeStore) Adjacency(ctx context.Context) (map[string][]system.GateEdge, error) {
	return nil, nil
}
func (s *reprobeStore) UnreadableState(ctx context.Context, sys string) (int, time.Time, bool, error) {
	return 0, time.Time{}, false, nil
}
func (s *reprobeStore) MarkUnreadable(ctx context.Context, sys, gate string, now time.Time) (int, error) {
	return 0, nil
}

// reprobeCountingAPI counts GetJumpGate calls: the observable proof a re-probe actually fired.
// Distinct from countingGateAPI, which counts the per-edge GetWaypoint CONSTRUCTION probe — the
// question here is whether the fetch-through went back for the system's connection list at all.
type reprobeCountingAPI struct {
	gateReads   int
	connections []string
}

func (a *reprobeCountingAPI) GetJumpGate(ctx context.Context, sys, wp, tok string) (*ports.JumpGateData, error) {
	a.gateReads++
	return &ports.JumpGateData{Symbol: wp, Connections: a.connections}, nil
}
func (a *reprobeCountingAPI) GetWaypoint(ctx context.Context, sys, wp, tok string) (*ports.WaypointDetail, error) {
	return &ports.WaypointDetail{Symbol: wp, IsUnderConstruction: false}, nil
}
func (a *reprobeCountingAPI) CreateChart(ctx context.Context, shipSymbol, token string) (*ports.ChartResult, error) {
	return &ports.ChartResult{}, nil
}

// mixedReprobeSet is the production shape: one under-construction row past its 2h window
// (flagged Stale by the store) among built rows that are current.
func mixedReprobeSet() []system.GateEdge {
	return []system.GateEdge{
		{ConnectedSystem: "X1-XX80", GateWaypoint: "X1-XX80-I1", UnderConstruction: true, Stale: true},
		{ConnectedSystem: "X1-AX76", GateWaypoint: "X1-AX76-I2"},
	}
}

// THE PRESERVED SIGNAL. Connections must still go to the API for a set carrying a row past
// its own window, even though the set is now a routing-trustworthy HIT. Without this the 2h
// build clock would be dead and a completed gate would go unnoticed for a day.
func TestService_Connections_StillReprobesASetWithARowPastItsOwnWindow(t *testing.T) {
	store := &reprobeStore{edges: mixedReprobeSet()}
	api := &reprobeCountingAPI{connections: []string{"X1-XX80-I1", "X1-AX76-I2"}}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"}, WithSkipUnchartedFetch(false))

	if _, err := svc.Connections(context.Background(), "X1-RJ93", 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if api.gateReads != 1 {
		t.Fatalf("the under-construction row is past its 2h window and MUST drive a live re-probe; "+
			"got %d gate reads — the build-completion clock is dead and the verdict is held 24h", api.gateReads)
	}
	if store.replaced != 1 {
		t.Fatalf("a re-probe must persist its fresh verdict (Replace), got %d", store.replaced)
	}
}

// The same signal on the PRESENT-SHIP path. A hull standing on the gate is the one moment the
// read is guaranteed to succeed, so it is the best possible moment to settle a pending build —
// its idempotence guard must not swallow a set that is due a re-probe.
func TestService_ChartPresentGate_StillReprobesASetWithARowPastItsOwnWindow(t *testing.T) {
	store := &reprobeStore{edges: mixedReprobeSet()}
	api := &reprobeCountingAPI{connections: []string{"X1-XX80-I1", "X1-AX76-I2"}}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"})

	if _, err := svc.ChartPresentGate(context.Background(), "X1-RJ93", "SHIP-1", 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if api.gateReads != 1 {
		t.Fatalf("a present hull is the one guaranteed-readable moment to settle a pending build; "+
			"the stale row must still drive the read, got %d gate reads", api.gateReads)
	}
}

// ZERO-API on a fully-current set. The re-probe must key on the stale ROW, not merely on the
// set being returned, or every routing lookup would re-fetch and the cache would be pointless.
func TestService_Connections_FullyCurrentSet_CostsNoAPI(t *testing.T) {
	store := &reprobeStore{edges: []system.GateEdge{
		{ConnectedSystem: "X1-XX80", GateWaypoint: "X1-XX80-I1", UnderConstruction: true}, // building but CURRENT
		{ConnectedSystem: "X1-AX76", GateWaypoint: "X1-AX76-I2"},
	}}
	api := &reprobeCountingAPI{connections: []string{"X1-XX80-I1"}}
	svc := NewService(store, api, nil, &stubPlayerRepo{token: "tok"})

	edges, err := svc.Connections(context.Background(), "X1-RJ93", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if api.gateReads != 0 {
		t.Fatalf("every row is inside its own freshness window — an under-construction row that is "+
			"CURRENT is already a probe's verdict and must cost zero API; got %d reads", api.gateReads)
	}
	if len(edges) != 2 {
		t.Fatalf("the cached set must be returned as-is, got %v", edges)
	}
}

// --- the stored-adjacency walk: the same sibling rule, the second wall site ---

// The proof-grade walk refused to expand THROUGH any system with a stale row, on the same
// one-timestamp reasoning. An under-construction row is chased on a shorter clock and is
// already impassable, so it was condemning a system over a row the walk would never traverse.
func TestStoredHopDistances_ExpandsThroughASystemWhoseOnlyStaleRowIsUnderConstruction(t *testing.T) {
	buildingSpur := append(repoEdgesTo("X1-ORIGIN", "X1-TARGET"),
		// X1-MID's third exit is still building and past its own 2h window.
		system.GateEdge{ConnectedSystem: "X1-DEADEND", GateWaypoint: "X1-DEADEND-GATE", UnderConstruction: true, Stale: true})

	svc := NewService(&adjStore{adjacency: map[string][]system.GateEdge{
		"X1-ORIGIN": repoEdgesTo("X1-MID"),
		"X1-MID":    buildingSpur,
		"X1-TARGET": repoEdgesTo("X1-MID"),
	}}, nil, nil, nil)

	got, err := svc.StoredHopDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, MaxJumpPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["X1-TARGET"] != 2 {
		t.Fatalf("X1-MID's built exits are current and its ONE stale exit is impassable anyway, so the "+
			"route through it is proven; the walk refused it and walled the system off — got %v", got)
	}
}

// PRESERVED: a system whose BUILT topology is past verification is still not expanded through.
// Those rows ARE routing candidates and their onward gates are genuinely unverified, so the
// proof-grade refusal must stand — the fix narrows which rows condemn, not the standard of proof.
func TestStoredHopDistances_StillRefusesThroughASystemWhoseBuiltRowsAreStale(t *testing.T) {
	svc := NewService(&adjStore{adjacency: map[string][]system.GateEdge{
		"X1-ORIGIN": repoEdgesTo("X1-MID"),
		"X1-MID":    staleEdgesTo("X1-ORIGIN", "X1-TARGET"), // BUILT rows, past verification
		"X1-TARGET": repoEdgesTo("X1-MID"),
	}}, nil, nil, nil)

	got, err := svc.StoredHopDistances(context.Background(), "X1-ORIGIN", []string{"X1-TARGET"}, MaxJumpPath)
	if err != nil {
		t.Fatalf("unverified topology is a clean refusal, not an error; got %v", err)
	}
	if _, resolved := got["X1-TARGET"]; resolved {
		t.Fatalf("X1-MID's BUILT rows are past verification, so a route through it is unproven and the "+
			"target must be absent; got %v", got)
	}
}

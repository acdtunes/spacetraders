package gategraph

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// --- the chosen-path construction verify must cost the same at 3 hops as at 12 ---

// linearChain builds a straight-line stored topology X1-H0 → X1-H1 → … → X1-H{hops}: one route,
// no alternates, many gates deep — the shape of the exotic long-haul lane. stale marks every row
// UNVERIFIED (the worst case for the verify step), matching what Adjacency reports for a row whose
// synced_at is missing or past its freshness window.
func linearChain(hops int, stale bool) (map[string][]system.GateEdge, []string) {
	path := make([]string, 0, hops+1)
	for i := 0; i <= hops; i++ {
		path = append(path, fmt.Sprintf("X1-H%d", i))
	}
	adjacency := make(map[string][]system.GateEdge, hops)
	for i := 0; i < hops; i++ {
		adjacency[path[i]] = []system.GateEdge{{
			ConnectedSystem: path[i+1],
			GateWaypoint:    path[i+1] + "-GATE",
			Stale:           stale,
		}}
	}
	return adjacency, path
}

// slowProbeGateAPI models a construction probe under live rate-limit pressure: each GetWaypoint
// costs real wall-clock before answering. It is the cost that made the verify scale with hop
// count. The probe honors the context, so a probe still in flight when the pathfind budget expires
// dies on the deadline exactly as the live client does.
type slowProbeGateAPI struct {
	adjacency map[string][]system.GateEdge
	delay     time.Duration
	calls     int
}

func (s *slowProbeGateAPI) GetJumpGate(ctx context.Context, sys, wp, tok string) (*ports.JumpGateData, error) {
	conns := make([]string, 0, len(s.adjacency[sys]))
	for _, e := range s.adjacency[sys] {
		conns = append(conns, e.GateWaypoint)
	}
	return &ports.JumpGateData{Symbol: wp, Connections: conns}, nil
}

func (s *slowProbeGateAPI) GetWaypoint(ctx context.Context, sys, wp, tok string) (*ports.WaypointDetail, error) {
	s.calls++
	select {
	case <-time.After(s.delay):
		return &ports.WaypointDetail{Symbol: wp, IsUnderConstruction: false}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *slowProbeGateAPI) CreateChart(ctx context.Context, shipSymbol, token string) (*ports.ChartResult, error) {
	return &ports.ChartResult{}, nil
}

// HEADLINE: the 7–12-hop lanes long-haul exists to fly must resolve INSIDE the pathfind budget.
// The budget here (100ms) is far shorter than a probe-per-hop verify could ever meet (11 hops ×
// 30ms = 330ms), so a resolver whose verify cost tracks hop count self-vetoes the lane — the live
// failure this reproduces. The route is fully built and stored FRESH; nothing about it is
// unroutable except the cost of re-verifying it.
func TestPathWithinJumpsStoredThenVerify_LongLane_ResolvesInsideBudget(t *testing.T) {
	adjacency, want := linearChain(11, false)
	api := &slowProbeGateAPI{adjacency: adjacency, delay: 30 * time.Millisecond}
	svc := NewService(&verifyStore{adjacency: adjacency}, api, nil, &stubPlayerRepo{token: "tok"}, WithPathfindBudget(100*time.Millisecond))

	got, err := svc.PathWithinJumpsStoredThenVerify(context.Background(), want[0], want[len(want)-1], 1, 25)
	if err != nil {
		t.Fatalf("an 11-hop lane over a fresh, fully-built stored topology must resolve inside the pathfind budget, got %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("long-haul path = %v, want %v", got, want)
	}
}

// The invariant behind the headline, stated so it cannot be satisfied by a wider budget: the live
// probe count is INDEPENDENT of how many hops the route has. Both lanes below are the worst case —
// every stored row UNVERIFIED, so every hop is probe-eligible — yet the deep lane must cost no more
// than the shallow one. Comparing two lengths (rather than asserting a constant) keeps this
// falsifiable no matter what ceiling the implementation picks.
func TestPathWithinJumpsStoredThenVerify_ProbeCostIndependentOfHopCount(t *testing.T) {
	probesFor := func(t *testing.T, hops int) int {
		t.Helper()
		adjacency, want := linearChain(hops, true)
		api := &countingGateAPI{adjacency: adjacency}
		svc := NewService(&verifyStore{adjacency: adjacency}, api, nil, &stubPlayerRepo{token: "tok"})

		got, err := svc.PathWithinJumpsStoredThenVerify(context.Background(), want[0], want[len(want)-1], 1, 25)
		if err != nil {
			t.Fatalf("the %d-hop lane must resolve, got %v", hops, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%d-hop path = %v, want %v", hops, got, want)
		}
		return api.getWaypointCalls
	}

	shallow := probesFor(t, 3)
	deep := probesFor(t, 12)
	if deep != shallow {
		t.Fatalf("verify cost must not scale with hop count: 3-hop lane spent %d probes, 12-hop lane spent %d", shallow, deep)
	}
	if deep >= 12 {
		t.Fatalf("a 12-hop lane must not spend a probe per hop, got %d", deep)
	}
}

// shiftingVerifyStore serves one topology to the PLAN and another to the VERIFY — an adjacency that
// changed under a plan already chosen. It is how a chosen hop can carry a stored under-construction
// flag at all: the plan step excludes such edges, so only a shift between the two reads exposes one.
type shiftingVerifyStore struct {
	plan   map[string][]system.GateEdge
	verify map[string][]system.GateEdge
	reads  int
}

func (s *shiftingVerifyStore) Adjacency(ctx context.Context) (map[string][]system.GateEdge, error) {
	s.reads++
	if s.reads == 1 {
		return s.plan, nil
	}
	return s.verify, nil
}
func (s *shiftingVerifyStore) Edges(ctx context.Context, sys string) ([]system.GateEdge, bool, error) {
	return nil, false, nil
}
func (s *shiftingVerifyStore) GateWaypointOf(ctx context.Context, sys string) (string, bool, error) {
	return sys + "-GATE", true, nil
}
func (s *shiftingVerifyStore) Replace(ctx context.Context, sys string, e []system.GateEdge) error {
	return nil
}
func (s *shiftingVerifyStore) UnreadableState(ctx context.Context, sys string) (int, time.Time, bool, error) {
	return 0, time.Time{}, false, nil
}
func (s *shiftingVerifyStore) MarkUnreadable(ctx context.Context, sys, gate string, now time.Time) (int, error) {
	return 0, nil
}

// The guard, on the signal the verify now leads with: a hop the STORE already knows is building is
// refused outright — no live call needed to decide it, and no route planned through it. Trusting a
// stored row must never mean trusting it in the permissive direction only.
func TestPathWithinJumpsStoredThenVerify_StoredUnderConstruction_FailsClosedWithoutProbing(t *testing.T) {
	clean := map[string][]system.GateEdge{
		"X1-A": repoEdgesTo("X1-B"),
		"X1-B": repoEdgesTo("X1-C"),
	}
	building := map[string][]system.GateEdge{
		"X1-A": {{ConnectedSystem: "X1-B", GateWaypoint: "X1-B-GATE", UnderConstruction: true}},
		"X1-B": repoEdgesTo("X1-C"),
	}
	api := &countingGateAPI{adjacency: clean} // every live probe here would answer "built"
	svc := NewService(&shiftingVerifyStore{plan: clean, verify: building}, api, nil, &stubPlayerRepo{token: "tok"})

	path, err := svc.PathWithinJumpsStoredThenVerify(context.Background(), "X1-A", "X1-C", 1, 25)
	if !errors.Is(err, ErrUnroutable) {
		t.Fatalf("a hop the store reports under construction must fail the plan closed, got path=%v err=%v", path, err)
	}
	if path != nil {
		t.Fatalf("a refused route must return no path, got %v", path)
	}
	if api.getWaypointCalls != 0 {
		t.Fatalf("a stored under-construction verdict needs no live probe to act on, got %d", api.getWaypointCalls)
	}
}

package commands

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// gatedNeighborMediator records every LIVE GetJumpGateConnectionsQuery dispatch (the
// uncached per-tick GetJumpGate call that must STOP issuing when a durable gate graph is
// wired). liveResp is what the live query would return, so a test can
// prove the wired path serves the DURABLE neighbors instead of this live set.
type gatedNeighborMediator struct {
	liveCalls int
	liveResp  []string
}

func (m *gatedNeighborMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	if _, ok := request.(*shipQuery.GetJumpGateConnectionsQuery); ok {
		m.liveCalls++
		return &shipQuery.GetJumpGateConnectionsResponse{ConnectedSystems: m.liveResp}, nil
	}
	return nil, nil
}
func (m *gatedNeighborMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *gatedNeighborMediator) RegisterMiddleware(middleware common.Middleware) {}

// The prong-2 win: with a durable gate graph wired, the per-tick lane neighbor scan is served
// from the persisted cache (gate_edges, 24h TTL) and the uncached live GetJumpGate query is
// NOT issued. The 0-live-calls assertion is the mutation-sensitive guard: revert the scan to
// the old live neighborSystems and the count flips to 1 (plus the returned set would become the
// live response), failing this test.
func TestGatedNeighborSystems_WiredGraph_ServesFromCache_NoLiveQuery(t *testing.T) {
	med := &gatedNeighborMediator{liveResp: []string{"X1-SHOULD-NOT-APPEAR"}}
	h := NewRunTradeRouteCoordinatorHandler(med, nil, nil, nil, nil, nil)
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-HOME": {{ConnectedSystem: "X1-NBR1"}, {ConnectedSystem: "X1-NBR2"}},
	}})

	got := h.gatedNeighborSystems(context.Background(), "X1-HOME", 1)

	want := []string{"X1-NBR1", "X1-NBR2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("a wired gate graph must serve neighbors from the durable cache, got %v want %v", got, want)
	}
	if med.liveCalls != 0 {
		t.Fatalf("a wired gate graph must bypass the uncached live GetJumpGate query (0 live calls), got %d", med.liveCalls)
	}
}

// The no-graph fallback: a graph-less handler (most tests, and any daemon before the gate graph
// is wired) keeps the exact legacy behavior — the live GetJumpGateConnections query IS the
// neighbor source. This preserves every existing caller and is the reversibility floor.
func TestGatedNeighborSystems_NoGraph_FallsBackToLiveQuery(t *testing.T) {
	med := &gatedNeighborMediator{liveResp: []string{"X1-NBR-LIVE"}}
	h := NewRunTradeRouteCoordinatorHandler(med, nil, nil, nil, nil, nil)
	// no SetGateGraph → h.gateGraph == nil

	got := h.gatedNeighborSystems(context.Background(), "X1-HOME", 1)

	if med.liveCalls != 1 {
		t.Fatalf("with no gate graph wired, the live query is the neighbor source (1 call), got %d", med.liveCalls)
	}
	if !reflect.DeepEqual(got, []string{"X1-NBR-LIVE"}) {
		t.Fatalf("expected the live neighbor set, got %v", got)
	}
}

// The fail-open-WITHOUT-reissue guard: a durable read error (an uncharted origin skipped by
// the durable-read precondition, or a gate in backoff) must degrade the scan to no neighbors
// (home-only lanes) — and must NOT fall back to re-issuing the doomed live query, which would
// defeat both the precondition and the backoff. This is what keeps the reclaim real.
func TestGatedNeighborSystems_DurableError_FailsOpenNoLiveReissue(t *testing.T) {
	med := &gatedNeighborMediator{liveResp: []string{"X1-SHOULD-NOT-APPEAR"}}
	h := NewRunTradeRouteCoordinatorHandler(med, nil, nil, nil, nil, nil)
	h.SetGateGraph(&fakeGateGraph{connErr: errors.New("jump-gate connections unreadable (backing off)")})

	got := h.gatedNeighborSystems(context.Background(), "X1-FRONT", 1)

	if len(got) != 0 {
		t.Fatalf("a durable read error must fail open to no neighbors, got %v", got)
	}
	if med.liveCalls != 0 {
		t.Fatalf("a durable error (backoff/uncharted skip) must NOT re-issue the live query, got %d live calls", med.liveCalls)
	}
}

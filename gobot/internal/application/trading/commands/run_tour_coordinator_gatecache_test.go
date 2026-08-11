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

// The tour graph's 1-hop neighbour scan, driven through tourSystemsFrom at the default depth
// against its two driven ports: the durable gate graph and the live jump-gate query.

// tourLiveGateMediator answers the LIVE GetJumpGateConnectionsQuery and counts each dispatch.
// liveResp differs from the durable adjacency, so a test can tell which port answered.
type tourLiveGateMediator struct {
	liveCalls int
	liveResp  []string
}

func (m *tourLiveGateMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	if _, ok := request.(*shipQuery.GetJumpGateConnectionsQuery); ok {
		m.liveCalls++
		return &shipQuery.GetJumpGateConnectionsResponse{ConnectedSystems: m.liveResp}, nil
	}
	return nil, nil
}

func (m *tourLiveGateMediator) Register(reflect.Type, common.RequestHandler) error { return nil }

func (m *tourLiveGateMediator) RegisterMiddleware(common.Middleware) {}

// newGateCacheTourHandler wires only the two ports the neighbour scan touches; the rest of the
// tour machinery is unreachable from tourSystemsFrom at the default depth.
func newGateCacheTourHandler(med common.Mediator) *RunTourCoordinatorHandler {
	return NewRunTourCoordinatorHandler(med, nil, nil, nil, nil, nil, nil, nil, nil)
}

func armedTourCmd() *RunTourCoordinatorCommand {
	return &RunTourCoordinatorCommand{PlayerID: 1, TourNeighborsDurableFirst: true}
}

// THE RECLAIM. Armed, a home system whose gate edges are already held is answered from the
// durable cache and costs ZERO live Get Jump Gate — the call the tour graph re-issued on every
// plan for topology that does not move within an era.
func TestTourOneHopSystems_Armed_ServesFromDurableCacheWithNoLiveQuery(t *testing.T) {
	med := &tourLiveGateMediator{liveResp: []string{"X1-SHOULD-NOT-APPEAR"}}
	h := newGateCacheTourHandler(med)
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-HOME": {{ConnectedSystem: "X1-NBR1"}, {ConnectedSystem: "X1-NBR2"}},
	}})

	got := h.tourSystemsFrom(context.Background(), "X1-HOME", armedTourCmd())

	assertExactOrder(t, got, []string{"X1-HOME", "X1-NBR1", "X1-NBR2"})
	if med.liveCalls != 0 {
		t.Fatalf("an armed tour graph must be served from the durable gate cache with zero live Get Jump Gate; got %d live calls", med.liveCalls)
	}
}

// THE REACH GUARD. A durable read that yields nothing usable must not shrink a hull's trading
// reach to home: the armed path falls back to live and produces the set the unarmed path would.
// Always EXACTLY one live call — a re-dispatching fallback would spend more than the bug it fixes.
func TestTourOneHopSystems_ArmedUnusableDurableRead_FallsBackToLiveReach(t *testing.T) {
	liveReach := []string{"X1-NBR1", "X1-NBR2"}
	for _, c := range []struct {
		name     string
		graph    GateGraph
		liveResp []string
		want     []string
	}{
		{"durable read fails (uncharted origin, or a gate backing off)", &fakeGateGraph{connErr: errors.New("jump-gate connections unreadable")}, liveReach, []string{"X1-HOME", "X1-NBR1", "X1-NBR2"}},
		{"durable graph holds no adjacency for home", &fakeGateGraph{edges: map[string][]system.GateEdge{"X1-OTHER": {{ConnectedSystem: "X1-ELSE"}}}}, liveReach, []string{"X1-HOME", "X1-NBR1", "X1-NBR2"}},
		{"no durable graph wired", nil, liveReach, []string{"X1-HOME", "X1-NBR1", "X1-NBR2"}},
		{"no durable graph wired and the live scan answers nothing either", nil, nil, []string{"X1-HOME"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			med := &tourLiveGateMediator{liveResp: c.liveResp}
			h := newGateCacheTourHandler(med)
			if c.graph != nil {
				h.SetGateGraph(c.graph)
			}

			got := h.tourSystemsFrom(context.Background(), "X1-HOME", armedTourCmd())

			assertExactOrder(t, got, c.want)
			if med.liveCalls != 1 {
				t.Fatalf("an unusable durable read must fall back to exactly one live query; got %d live calls", med.liveCalls)
			}
		})
	}
}

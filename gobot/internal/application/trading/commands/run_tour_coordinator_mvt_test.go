package commands

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

type mvtFakeClaims struct {
	mu         sync.Mutex
	rows       map[string]mvt.Claim
	fail       bool
	failUpsert bool
	// released counts Release calls, so a test can tell a release-then-restamp from an overwrite.
	released int
}

func newMVTFakeClaims() *mvtFakeClaims { return &mvtFakeClaims{rows: map[string]mvt.Claim{}} }
func (c *mvtFakeClaims) Upsert(_ context.Context, _ int, hull, system string, at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failUpsert {
		return context.DeadlineExceeded
	}
	c.rows[hull] = mvt.Claim{Hull: hull, System: system, ClaimedAt: at}
	return nil
}
func (c *mvtFakeClaims) MarkArrived(_ context.Context, _ int, hull string, at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.rows[hull]
	r.ArrivedAt = &at
	c.rows[hull] = r
	return nil
}
func (c *mvtFakeClaims) Release(_ context.Context, _ int, hull string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.released++
	delete(c.rows, hull)
	return nil
}
func (c *mvtFakeClaims) Get(_ context.Context, _ int, hull string) (mvt.Claim, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.rows[hull]
	return r, ok, nil
}
func (c *mvtFakeClaims) InTransit(_ context.Context, _ int) (map[string]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return nil, context.DeadlineExceeded
	}
	out := map[string]int{}
	for _, r := range c.rows {
		if r.ArrivedAt == nil {
			out[r.System]++
		}
	}
	return out, nil
}

type mvtFakeDepth struct {
	lanes map[string][]mvt.LaneDepth
	fail  bool
}

func (d *mvtFakeDepth) SystemDepths(_ context.Context, _ int, systems []string) (map[string][]mvt.LaneDepth, error) {
	if d.fail {
		return nil, context.DeadlineExceeded
	}
	out := map[string][]mvt.LaneDepth{}
	for _, s := range systems {
		if l, ok := d.lanes[s]; ok {
			out[s] = l
		}
	}
	return out, nil
}

type mvtFakeTransitions struct {
	mu   sync.Mutex
	rows []mvt.Transition
}

func (r *mvtFakeTransitions) Record(_ context.Context, t mvt.Transition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, t)
	return nil
}

// last is the newest recorded transition; an absent row is a named failure, not an index panic.
func (r *mvtFakeTransitions) last(t *testing.T) mvt.Transition {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.rows) == 0 {
		t.Fatal("shadow must record a transition after the productive home tour")
	}
	return r.rows[len(r.rows)-1]
}

type mvtFakeTolls struct{ seconds int }

func (t mvtFakeTolls) PerHopTollSeconds(context.Context, int) int { return t.seconds }

type mvtFakeFees struct{ fees map[string]int64 }

func (f mvtFakeFees) GateFees(context.Context, int) map[string]int64 { return f.fees }

// mvtLane builds a fresh EXPORT/IMPORT pair for good in system with the given depth and spread.
func mvtLane(system, good string, depth, ask, bid int, now time.Time) []mvt.LaneDepth {
	mk := func(wp, tt string, price int) trading.GoodListing {
		return trading.GoodListing{Good: good, Waypoint: wp, TradeType: tt, Bid: price, Ask: price,
			Supply: "MODERATE", Activity: "STRONG", Volume: depth, ObservedAt: now}
	}
	return []mvt.LaneDepth{{Listing: mk(system+"-SRC", "EXPORT", ask)}, {Listing: mk(system+"-SNK", "IMPORT", bid)}}
}

func mvtCaps() trading.RankerAgeCaps {
	h := 24 * time.Hour
	return trading.RankerAgeCaps{Weak: h, Restricted: h, Growing: h, Strong: h}
}

// mvtStoredGraph is a persisted gate adjacency with each listed pair gated both ways — the stored
// gate_edges rows the shadow's discovery walks. Its Connections is the fetch-through seam and is
// recorded, so a test can prove the shadow never reaches it.
func mvtStoredGraph(pairs ...[2]string) *fakeGateGraph {
	edges := map[string][]system.GateEdge{}
	for _, p := range pairs {
		edges[p[0]] = append(edges[p[0]], system.GateEdge{ConnectedSystem: p[1]})
		edges[p[1]] = append(edges[p[1]], system.GateEdge{ConnectedSystem: p[0]})
	}
	return &fakeGateGraph{edges: edges}
}

// mvtShadowCommand is a continuous old-path run with every rescue path isolated, so the only
// thing that happens after the productive home tour is the shadow.
func mvtShadowCommand(t *testing.T) *RunTourCoordinatorCommand {
	t.Helper()
	return &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-SH", PlayerID: 1, ContainerID: "ctr-sh", Iterations: -1,
		RepositionMinMargin: isolateLegacyReposition, PlacementDisabled: true,
		ModelArtifactPath: writeTourArtifact(t),
		YieldWindowSells:  8, YieldMinSells: 3, ClaimReachHops: 2, SpecialistCadenceMinutes: 60,
	}
}

// mvtShadowHandler wires a shadow-ready old-path handler over the reposition fixture: the given
// depths, an empty claim registry, a transition recorder, toll/fee readers and 24h age caps.
func mvtShadowHandler(t *testing.T, fx *tourFixture, depth mvt.SystemDepthReader) (*RunTourCoordinatorHandler, *mvtFakeClaims, *mvtFakeTransitions) {
	t.Helper()
	h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), &seededTelemetry{rows: rfSeed("TOUR-SH", 100000)})
	claims, trans := newMVTFakeClaims(), &mvtFakeTransitions{}
	h.SetMVTPorts(claims, depth, trans)
	h.SetJumpTollReader(mvtFakeTolls{seconds: 361})
	h.SetGateFeeReader(mvtFakeFees{fees: map[string]int64{"X1-S1": 100}})
	h.SetRankerAgeCaps(mvtCaps())
	return h, claims, trans
}

func TestMVTShadow_RecordsWouldBeDecisionOnOldPath(t *testing.T) {
	fx := repositionFixture()
	now := time.Now()
	h, claims, trans := mvtShadowHandler(t, fx, &mvtFakeDepth{lanes: map[string][]mvt.LaneDepth{
		"X1-S1": mvtLane("X1-S1", "IRON", 10, 100, 110, now),   // 100 credits of depth at home
		"X1-S2": mvtLane("X1-S2", "IRON", 500, 100, 1000, now), // 450k next door
	}})
	graph := mvtStoredGraph([2]string{"X1-S1", "X1-S2"})
	h.SetGateGraph(graph)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	_, err := h.Handle(ctx, mvtShadowCommand(t))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := trans.last(t)
	if got.Reason != mvtReasonShadow || got.From != mvt.StateTrade || got.To != mvt.StateClaim || got.System != "X1-S1" || got.BestAlternative <= 0 {
		t.Fatalf("shadow row = %+v", got)
	}
	if len(claims.rows) != 0 {
		t.Fatal("shadow must never write a claim")
	}
	if len(fx.jumps) != 0 {
		t.Fatal("shadow must never move the hull")
	}
}

// mvtTwoHopDepths prices home thinly and X1-S3 richly; X1-S2, the only system the fixture's live
// jump-gate scan knows, has nothing priced at all.
func mvtTwoHopDepths(now time.Time) *mvtFakeDepth {
	return &mvtFakeDepth{lanes: map[string][]mvt.LaneDepth{
		"X1-S1": mvtLane("X1-S1", "IRON", 10, 100, 110, now),   // 100 credits of depth at home
		"X1-S3": mvtLane("X1-S3", "IRON", 500, 100, 1000, now), // 450k two hops out
	}}
}

// The shadow's candidate discovery walks the PERSISTED gate adjacency to ClaimReachHops. The
// 2-hop system is gated only in the stored graph — the fixture's live jump-gate scan knows X1-S2
// alone, and X1-S2 has nothing priced — so a CLAIM verdict here can only have come from a 2-hop
// stored walk.
func TestMVTShadow_DiscoveryWalksTheStoredGraphToTwoHops(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtShadowHandler(t, fx, mvtTwoHopDepths(time.Now()))
	h.SetGateGraph(mvtStoredGraph([2]string{"X1-S1", "X1-S2"}, [2]string{"X1-S2", "X1-S3"}))
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, mvtShadowCommand(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := trans.last(t)
	if got.Reason != mvtReasonShadow || got.To != mvt.StateClaim || got.BestAlternative <= 0 {
		t.Fatalf("the 2-hop system is the only profitable ground and is reachable only through the stored graph: %+v", got)
	}
	if len(claims.rows) != 0 || len(fx.jumps) != 0 {
		t.Fatal("shadow must never write a claim or move the hull")
	}
}

// Arming the shadow adds no fetch-through topology read to the old path (final review, finding
// 1: the shadow's 2-hop walk used to read Connections on the origin and every 1-hop neighbour
// after every productive tour, which in production is a live GetJumpGate and a gate_edges write
// per missing or stale set). This fixture's own old path reads the origin's 1-hop set through
// Connections once — the margins-death reposition scan on the continuous run's second, planless
// iteration, isolated from jumping by RepositionMinMargin — and that read must be the ONLY one
// whether the shadow is silent (no transition recorder) or armed: same calls, same order.
func TestMVTShadow_ArmingAddsNoFetchThroughReadsToTheOldPath(t *testing.T) {
	run := func(armed bool) []string {
		t.Helper()
		fx := repositionFixture()
		depth := mvtTwoHopDepths(time.Now())
		h, claims, trans := mvtShadowHandler(t, fx, depth)
		if !armed {
			h.SetMVTPorts(claims, depth, nil)
		}
		graph := mvtStoredGraph([2]string{"X1-S1", "X1-S2"}, [2]string{"X1-S2", "X1-S3"})
		h.SetGateGraph(graph)
		ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
		if _, err := h.Handle(ctx, mvtShadowCommand(t)); err != nil {
			t.Fatalf("handle (armed=%v): %v", armed, err)
		}
		if armed {
			if got := trans.last(t); got.To != mvt.StateClaim {
				t.Fatalf("the armed shadow must have ranked the 2-hop ground: %+v", got)
			}
		} else if len(trans.rows) != 0 {
			t.Fatalf("a silent shadow records nothing; got %+v", trans.rows)
		}
		return graph.connCalls
	}
	silent, armed := run(false), run(true)
	if !reflect.DeepEqual(silent, armed) {
		t.Fatalf("arming the shadow changed the old path's fetch-through reads: silent=%v armed=%v", silent, armed)
	}
}

// With no gate graph wired there is no stored adjacency to walk, and the shadow must NOT fall
// back to the live jump-gate scan the fixture answers (X1-S2, with 450k of depth): it ranks the
// hull's own system alone and records a stay. Discovery is stored-only or nothing.
func TestMVTShadow_NoGateGraphRanksHomeOnlyNeverTheLiveScan(t *testing.T) {
	fx := repositionFixture() // the live scan knows X1-S1 -> X1-S2
	now := time.Now()
	h, _, trans := mvtShadowHandler(t, fx, &mvtFakeDepth{lanes: map[string][]mvt.LaneDepth{
		"X1-S1": mvtLane("X1-S1", "IRON", 10, 100, 110, now),
		"X1-S2": mvtLane("X1-S2", "IRON", 500, 100, 1000, now),
	}})
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, mvtShadowCommand(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := trans.last(t)
	if got.Reason != mvtReasonShadow || got.To != mvt.StateTrade || got.BestAlternative != 0 {
		t.Fatalf("no stored graph means no candidate but home; the live scan must stay unread: %+v", got)
	}
}

func TestMVTShadow_UnreadableLedgerRecordsStay(t *testing.T) {
	h, _, trans := mvtShadowHandler(t, repositionFixture(), &mvtFakeDepth{fail: true})
	h.SetGateGraph(mvtStoredGraph([2]string{"X1-S1", "X1-S2"}))
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, mvtShadowCommand(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := trans.last(t)
	if got.Reason != mvtReasonRankerUnreadable || got.To != mvt.StateTrade {
		t.Fatalf("unreadable ledger must record a stay: %+v", got)
	}
}

// An unreadable gate store is the same verdict as an unreadable ledger: the ranker fails closed
// and the hull stays put, rather than ranking a home-only neighbourhood as if nothing were near.
func TestMVTShadow_UnreadableGateStoreRecordsStay(t *testing.T) {
	now := time.Now()
	h, _, trans := mvtShadowHandler(t, repositionFixture(), &mvtFakeDepth{lanes: map[string][]mvt.LaneDepth{
		"X1-S1": mvtLane("X1-S1", "IRON", 10, 100, 110, now),
	}})
	graph := mvtStoredGraph([2]string{"X1-S1", "X1-S2"})
	graph.storedHopErr = errors.New("gate_edges unreadable")
	h.SetGateGraph(graph)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, mvtShadowCommand(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := trans.last(t)
	if got.Reason != mvtReasonRankerUnreadable || got.To != mvt.StateTrade {
		t.Fatalf("unreadable gate store must record a stay: %+v", got)
	}
}

func TestMVTFleetStats_KeyedByPlayer(t *testing.T) {
	fx := repositionFixture()
	// Player 1's telemetry: one hull earning; player 2 has none.
	h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), &seededTelemetry{rows: rfSeed("TOUR-P1", 100000)})
	h.SetMVTPorts(newMVTFakeClaims(), &mvtFakeDepth{}, &mvtFakeTransitions{})
	ctx := context.Background()
	p1 := h.mvtFleetStats(ctx, &RunTourCoordinatorCommand{ShipSymbol: "TOUR-P1", PlayerID: 1, SpecialistCadenceMinutes: 60})
	p2 := h.mvtFleetStats(ctx, &RunTourCoordinatorCommand{ShipSymbol: "TOUR-P2", PlayerID: 2, SpecialistCadenceMinutes: 60})
	if p1.Hulls == 0 {
		t.Fatal("player 1 has seeded legs and must have stats")
	}
	if p2.Hulls != 0 {
		t.Fatalf("player 2 has no legs but got player 1's stats: %+v", p2)
	}
}

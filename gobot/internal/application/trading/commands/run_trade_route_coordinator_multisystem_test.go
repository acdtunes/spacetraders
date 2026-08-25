package commands

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// scanLanes looks one jump-gate hop beyond the home system so the ranker can surface
// gate-crossing lanes alongside home-system ones, RATE-ranked via rankLanesByCircuitRate
// so the jump+cooldown time cost RankSpreads' pure per-unit-spread view can't see enters
// as circuit hours. These tests
// exercise that aggregation through scanLanes itself (not the already-covered
// pure functions), because aggregation only pays off when BOTH the
// neighbor-discovery wiring and the rate ranking are actually applied end to end.

// msMediator answers GetJumpGateConnectionsQuery for the multi-system scanLanes
// tests; scanLanes never dispatches anything else.
type msMediator struct {
	connections map[string][]string // systemSymbol -> connected systems
	queryErr    error
	queries     []*shipQuery.GetJumpGateConnectionsQuery
}

func (m *msMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipQuery.GetJumpGateConnectionsQuery:
		m.queries = append(m.queries, cmd)
		if m.queryErr != nil {
			return nil, m.queryErr
		}
		return &shipQuery.GetJumpGateConnectionsResponse{
			ConnectedSystems: m.connections[cmd.SystemSymbol],
		}, nil
	default:
		return nil, nil
	}
}

func (m *msMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *msMediator) RegisterMiddleware(middleware common.Middleware) {}

// msGood is one waypoint's single-good listing for the multi-system fixtures.
//
// ask is what WE PAY to buy from this market (the market_data.purchase_price column)
// and bid is what WE RECEIVE selling to it (sell_price), so ask > bid at every market
// — the sp-en5h7 convention.
type msGood struct {
	symbol    string
	bid, ask  int
	volume    int
	tradeType market.TradeType
	// activity optionally sets the market activity level (WEAK/RESTRICTED/GROWING/
	// STRONG) so the activity-conditioned age-cap tests can pick a listing's freshness
	// window. Empty defaults to "STRONG" (the fixture's historical hardcode), so every
	// existing fixture that omits it is unchanged.
	activity string
}

// msMarketRepo serves a fixed set of waypoints per system, each with at most
// one good listing, so multi-system tests can control exactly which (system,
// good, side) combinations exist without pulling in the full trFixture machinery.
type msMarketRepo struct {
	market.MarketRepository
	waypointsBySystem map[string][]string
	goods             map[string]msGood // waypoint -> its listing
	// observedAt optionally overrides a waypoint's market LastUpdated timestamp so
	// age-cap tests can mark a market stale. Nil / missing key -> time.Now()
	// (fresh), so every existing fixture that omits it ranks unchanged.
	observedAt map[string]time.Time
}

func (r *msMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return r.waypointsBySystem[systemSymbol], nil
}

func (r *msMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	g, ok := r.goods[waypointSymbol]
	if !ok {
		return nil, nil
	}
	supply := "MODERATE"
	activity := g.activity
	if activity == "" {
		activity = "STRONG"
	}
	// purchasePrice = the ask (what we pay), sellPrice = the bid (what we receive) — sp-en5h7.
	good, err := market.NewTradeGood(g.symbol, &supply, &activity, g.ask, g.bid, g.volume, g.tradeType)
	if err != nil {
		return nil, err
	}
	updated := time.Now()
	if ts, ok := r.observedAt[waypointSymbol]; ok {
		updated = ts
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, updated)
}

// Neither system alone carries both sides of WIDGET (X1-HOME only exports it,
// X1-NEAR only imports it) — a lane can only emerge if scanLanes actually
// aggregates the neighbor system's listings into the same ranking pass.
func TestScanLanes_MultiSystem_AggregatesNeighborListings(t *testing.T) {
	marketRepo := &msMarketRepo{
		waypointsBySystem: map[string][]string{
			"X1-HOME": {"X1-HOME-A"},
			"X1-NEAR": {"X1-NEAR-B"},
		},
		goods: map[string]msGood{
			"X1-HOME-A": {symbol: "WIDGET", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport},
			"X1-NEAR-B": {symbol: "WIDGET", bid: 900, ask: 950, volume: 60, tradeType: market.TradeTypeImport},
		},
	}
	mediator := &msMediator{connections: map[string][]string{"X1-HOME": {"X1-NEAR"}}}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, nil, marketRepo, nil, nil, nil)

	lanes, err := handler.scanLanes(context.Background(), "X1-HOME", 1, 0, "")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(lanes) != 1 {
		t.Fatalf("expected exactly 1 cross-system lane (neither system alone has both sides of WIDGET), got %d: %+v", len(lanes), lanes)
	}
	lane := lanes[0]
	if lane.SourceWaypoint != "X1-HOME-A" || lane.DestWaypoint != "X1-NEAR-B" {
		t.Fatalf("expected source=X1-HOME-A dest=X1-NEAR-B, got source=%s dest=%s", lane.SourceWaypoint, lane.DestWaypoint)
	}
	if len(mediator.queries) != 1 || mediator.queries[0].SystemSymbol != "X1-HOME" {
		t.Fatalf("expected scanLanes to query jump connections FROM the home system, got %+v", mediator.queries)
	}
}

// A neighbor-discovery failure (no jump gate in the system, an API error, etc.)
// must fail OPEN: the circuit still ranks whatever the home system offers on
// its own, rather than aborting the whole scan over an unrelated lookup.
func TestScanLanes_NeighborQueryFails_FailsOpenToHomeSystemOnly(t *testing.T) {
	marketRepo := &msMarketRepo{
		waypointsBySystem: map[string][]string{
			"X1-HOME": {"X1-HOME-A", "X1-HOME-B"},
		},
		goods: map[string]msGood{
			"X1-HOME-A": {symbol: "WIDGET", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport},
			"X1-HOME-B": {symbol: "WIDGET", bid: 600, ask: 650, volume: 60, tradeType: market.TradeTypeImport},
		},
	}
	mediator := &msMediator{queryErr: fmt.Errorf("no jump gate in this system")}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, nil, marketRepo, nil, nil, nil)

	lanes, err := handler.scanLanes(context.Background(), "X1-HOME", 1, 0, "")
	if err != nil {
		t.Fatalf("a neighbor-discovery failure must fail OPEN, not abort the scan: %v", err)
	}
	if len(lanes) != 1 || lanes[0].SourceWaypoint != "X1-HOME-A" {
		t.Fatalf("expected the home-system lane to still be returned, got %+v", lanes)
	}
}

// ── Horizon lock: this scanner stays at ONE gate hop ─────────────────────────
//
// The tour planner discovers across a WIDER horizon (candidate_hop_depth) than this
// scanner does, and that divergence is deliberate, not drift. The tour path earned
// its width by first carrying a per-pair gate-hop distance map, so its solver prices
// a crossing at its REAL hop count. This scanner has no such model: its cross-system
// premium is charged off a BOOLEAN (laneCircuitRatePerHour's `crossSystem`), and an
// ArbitrageLane carries no hop-distance field at all — so every crossing, near or
// far, pays one flat round-trip surcharge. Widening discovery here would therefore
// price an N-hop lane as a 1-hop one and rank it ~N times too well, i.e. relax a
// money guard as a side effect of a reach change. At exactly one hop the flat charge
// IS the honest price, which is what makes today's horizon sound rather than merely
// narrow.
//
// The two tests below are a PAIR and are meant to be read together: the first pins
// the horizon, the second pins the hop-blindness that is the REASON for it. Teach the
// surcharge real distance and the second test fails — that failure is the signal that
// the first may then be revisited, and not before.

// The horizon bound itself: a system two gate hops out is never scanned, so its
// markets cannot form a lane no matter how rich they are. The 2-hop sink here is
// priced absurdly high on purpose — were it ever in scope it would rank first, so
// its ABSENCE from the result isolates the discovery horizon rather than the ranking.
func TestScanLanes_HorizonStopsAtOneGateHop(t *testing.T) {
	marketRepo := &msMarketRepo{
		waypointsBySystem: map[string][]string{
			"X1-HOME": {"X1-HOME-A"},
			"X1-NEAR": {"X1-NEAR-B"},
			"X1-FAR":  {"X1-FAR-C"},
		},
		goods: map[string]msGood{
			// WIDGET is sourceable at home, but its only sink sits two hops out.
			"X1-HOME-A": {symbol: "WIDGET", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport},
			// The 1-hop neighbor trades an unrelated good, so it pairs with nothing.
			"X1-NEAR-B": {symbol: "GADGET", bid: 20, ask: 40, volume: 60, tradeType: market.TradeTypeExport},
			"X1-FAR-C":  {symbol: "WIDGET", bid: 9000, ask: 9050, volume: 60, tradeType: market.TradeTypeImport},
		},
	}
	// X1-HOME -> X1-NEAR -> X1-FAR: the chain the scan must NOT walk transitively.
	mediator := &msMediator{connections: map[string][]string{
		"X1-HOME": {"X1-NEAR"},
		"X1-NEAR": {"X1-FAR"},
	}}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, nil, marketRepo, nil, nil, nil)

	lanes, err := handler.scanLanes(context.Background(), "X1-HOME", 1, 0, "")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(lanes) != 0 {
		t.Fatalf("the 2-hop sink X1-FAR-C must be outside the scan horizon, so WIDGET has no sell side and no lane may form; got %d lane(s): %+v", len(lanes), lanes)
	}
	// Discovery is a single hop off home, never a recursive walk: the neighbor's own
	// connections are not consulted. This is the mechanism half of the lock — it fails
	// on a transitive widening even if the fixture's markets happened to form no lane.
	if len(mediator.queries) != 1 || mediator.queries[0].SystemSymbol != "X1-HOME" {
		t.Fatalf("expected exactly one jump-gate query, from the home system only (no transitive walk); got %+v", mediator.queries)
	}
}

// The reason half of the lock: the cross-system premium is hop-blind. Two lanes that
// are identical in every economic field differ only in which system their endpoints
// name — one crossing a single gate hop, one notionally far deeper — and they price
// at the SAME rate, because the rate function has no distance input to consult. The
// same-system control proves the surcharge is real (and therefore that it is binary,
// not absent). Make the premium distance-aware and this test fails by design.
func TestLaneCircuitRate_CrossSystemSurchargeIsHopBlind(t *testing.T) {
	const capacity = 60
	lane := func(source, dest string) trading.ArbitrageLane {
		return trading.ArbitrageLane{
			Good: "WIDGET", SourceWaypoint: source, DestWaypoint: dest,
			SourceAsk: 100, DestBid: 600, SpreadPerUnit: 500,
			VolumeCap: capacity, CappedSpread: 500 * capacity,
		}
	}
	// One gate hop out versus notionally several — indistinguishable to the ranker.
	near := laneCircuitRatePerHour(lane("X1-HOME-A", "X1-NEAR-B"), capacity, "", laneImpactModel{}, 0)
	far := laneCircuitRatePerHour(lane("X1-HOME-A", "X1-FAR-C"), capacity, "", laneImpactModel{}, 0)
	if near != far {
		t.Fatalf("the cross-system surcharge is charged off a boolean, so distance cannot change it: near %.6f != far %.6f — if the premium is now distance-aware, revisit the one-hop discovery horizon in TestScanLanes_HorizonStopsAtOneGateHop", near, far)
	}
	// Control: the premium exists at all, so the equality above is hop-blindness and
	// not simply a surcharge that never fires.
	sameSystem := laneCircuitRatePerHour(lane("X1-HOME-A", "X1-HOME-B"), capacity, "", laneImpactModel{}, 0)
	if !(sameSystem > near) {
		t.Fatalf("a same-system lane must out-rate an otherwise identical gate-crossing one (%.6f > %.6f) — without a live surcharge the hop-blindness assertion proves nothing", sameSystem, near)
	}
}

// End-to-end proof that scanLanes actually applies rankLanesByCircuitRate (not
// just RankSpreads) to its aggregated output: GOOD_B's raw cross-system spread
// (550) beats GOOD_A's same-system spread (500), but its +10% value lead is
// smaller than the gate's ~17.6% circuit-time premium (33,000/1.3067h ≈
// 25,255/hr < 30,000/1.111h = 27,000/hr), so GOOD_A must rank first.
func TestScanLanes_CrossSystemLane_RateCanLoseToSameSystemLane(t *testing.T) {
	marketRepo := &msMarketRepo{
		waypointsBySystem: map[string][]string{
			"X1-HOME": {"X1-HOME-A1", "X1-HOME-A2", "X1-HOME-B1"},
			"X1-NEAR": {"X1-NEAR-B2"},
		},
		goods: map[string]msGood{
			// GOOD_A: same-system lane, spread 500 (600-100), volume 60.
			"X1-HOME-A1": {symbol: "GOOD_A", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport},
			"X1-HOME-A2": {symbol: "GOOD_A", bid: 600, ask: 650, volume: 60, tradeType: market.TradeTypeImport},
			// GOOD_B: cross-system lane, raw spread 550 (beats GOOD_A's 500) but loses
			// on RATE once its circuit pays the round-trip jump surcharge.
			"X1-HOME-B1": {symbol: "GOOD_B", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport},
			"X1-NEAR-B2": {symbol: "GOOD_B", bid: 650, ask: 700, volume: 60, tradeType: market.TradeTypeImport},
		},
	}
	mediator := &msMediator{connections: map[string][]string{"X1-HOME": {"X1-NEAR"}}}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, nil, marketRepo, nil, nil, nil)

	lanes, err := handler.scanLanes(context.Background(), "X1-HOME", 1, 0, "")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(lanes) != 2 {
		t.Fatalf("expected both lanes ranked, got %d: %+v", len(lanes), lanes)
	}
	if lanes[0].Good != "GOOD_A" {
		t.Fatalf("expected the same-system lane GOOD_A first on rate (27,000/hr vs the surcharged 25,255/hr), got %q first", lanes[0].Good)
	}
	// The cross-system lane's REAL spread must survive unmutated for downstream
	// reporting (e.g. FirstDisciplinedLane, the response's Good).
	if lanes[1].Good != "GOOD_B" || lanes[1].SpreadPerUnit != 550 {
		t.Fatalf("expected GOOD_B second with its real spread=550 intact, got %+v", lanes[1])
	}
}

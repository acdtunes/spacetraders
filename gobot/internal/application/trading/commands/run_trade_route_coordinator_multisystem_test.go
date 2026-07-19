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
type msGood struct {
	symbol    string
	bid, ask  int
	volume    int
	tradeType market.TradeType
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
	activity := "STRONG"
	good, err := market.NewTradeGood(g.symbol, &supply, &activity, g.bid, g.ask, g.volume, g.tradeType)
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

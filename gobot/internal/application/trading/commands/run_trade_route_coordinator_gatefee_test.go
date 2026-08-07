package commands

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// Lane selection prices the gates a circuit crosses. These tests pin the four properties that
// make the bound safe: it charges only what a lane provably crosses, it leaves same-system lanes
// exactly as they were, it can only narrow, and the shared per-visit floor is untouched.

// gfGateBill is what one cross-system circuit owes: out with the cargo, back to re-buy.
var gfGateBill = int64(crossSystemCircuitCrossings) * domainSensing.DefaultGateFeeCredits

// gfCrossLane is the defect in one lane: it clears the per-unit floor four times over, so the
// selector took it, while its whole trip grosses less than the floor plus the two crossings.
func gfCrossLane() trading.ArbitrageLane {
	return trading.ArbitrageLane{
		Good: "GOLD", SourceWaypoint: "X1-HOME-EX", DestWaypoint: "X1-NEAR-IM",
		SourceAsk: 500, DestBid: 2500, SpreadPerUnit: 2000, VolumeCap: 10, CappedSpread: 20000,
	}
}

// gfRichCrossLane crosses the same two gates off a spread deep enough to pay for them. A guard
// that simply dropped cross-system lanes would refuse this one too.
func gfRichCrossLane() trading.ArbitrageLane {
	return trading.ArbitrageLane{
		Good: "PLATINUM", SourceWaypoint: "X1-HOME-EX", DestWaypoint: "X1-NEAR-IM",
		SourceAsk: 500, DestBid: 4500, SpreadPerUnit: 4000, VolumeCap: 10, CappedSpread: 40000,
	}
}

// A same-system lane crosses nothing, so the charge must not reach it — including the awkward
// shapes, where a lane grossing far less than a single gate fee is still good work at zero
// travel. Charging gates unconditionally would take the fleet's only zero-travel margin.
func TestLanesClearingGateFees_WithinSystemLanesPassThroughUntouched(t *testing.T) {
	withinSystem := []trading.ArbitrageLane{
		// Grosses a fraction of one 5,900 crossing, and is still flyable.
		{Good: "FUEL", SourceWaypoint: "X1-HOME-EX", DestWaypoint: "X1-HOME-IM",
			SpreadPerUnit: 1200, VolumeCap: 1, CappedSpread: 1200},
		// Exactly at the floor: the boundary ClearsFloor admits.
		{Good: "ICE_WATER", SourceWaypoint: "X1-HOME-EX", DestWaypoint: "X1-HOME-IM",
			SpreadPerUnit: trading.MinBidMargin, VolumeCap: 60, CappedSpread: trading.MinBidMargin * 60},
		// A sink absorbing nothing: ClearsFloorAfterGates refuses VolumeCap <= 0 outright, so
		// routing same-system lanes through it would change this verdict.
		{Good: "DIAMONDS", SourceWaypoint: "X1-HOME-EX", DestWaypoint: "X1-HOME-IM",
			SpreadPerUnit: 9000, VolumeCap: 0, CappedSpread: 0},
		// Sub-floor: refused downstream by ClearsFloor, and this filter must not be what did it.
		{Good: "FOOD", SourceWaypoint: "X1-HOME-EX", DestWaypoint: "X1-HOME-IM",
			SpreadPerUnit: 780, VolumeCap: 60, CappedSpread: 46800},
		{Good: "FABRICS", SourceWaypoint: "X1-HOME-A", DestWaypoint: "X1-HOME-B",
			SpreadPerUnit: 1500, VolumeCap: 4, CappedSpread: 6000},
	}

	kept, pricedOut := lanesClearingGateFees(withinSystem)

	if pricedOut != 0 {
		t.Fatalf("a same-system lane crosses no gate: expected 0 priced out, got %d", pricedOut)
	}
	if !reflect.DeepEqual(kept, withinSystem) {
		t.Fatalf("same-system candidates must survive unchanged and in order.\n got: %+v\nwant: %+v", kept, withinSystem)
	}
	for _, l := range withinSystem {
		if got := laneGateCrossings(l); got != 0 {
			t.Fatalf("%s runs %s -> %s inside one system; expected 0 crossings, got %d",
				l.Good, l.SourceWaypoint, l.DestWaypoint, got)
		}
	}

	// The whole point of leaving them alone: what survives is decided by ClearsFloor and
	// nothing else, so selection over the filtered set picks exactly what it always picked.
	want, wantOK := trading.FirstDisciplinedLane(withinSystem)
	got, gotOK := selectLane(kept, "")
	if gotOK != wantOK || got.Good != want.Good {
		t.Fatalf("filtering must not move the undirected same-system pick: got %q (ok=%v), want %q (ok=%v)",
			got.Good, gotOK, want.Good, wantOK)
	}
}

// The lane the bead is about: it clears the per-unit floor, so the OLD predicate selected it,
// and its whole trip cannot pay the two crossings it makes.
func TestLanesClearingGateFees_RefusesACrossSystemLaneThatCannotPayItsCrossings(t *testing.T) {
	poor := gfCrossLane()

	// The arithmetic the refusal rests on, stated independently of the code under test.
	gross := int64(poor.SpreadPerUnit) * int64(poor.VolumeCap)
	floor := int64(trading.MinBidMargin) * int64(poor.VolumeCap)
	if gross-gfGateBill >= floor {
		t.Fatalf("fixture no longer describes an unaffordable lane: gross %d - gates %d >= floor %d",
			gross, gfGateBill, floor)
	}

	kept, pricedOut := lanesClearingGateFees([]trading.ArbitrageLane{poor})

	if pricedOut != 1 || len(kept) != 0 {
		t.Fatalf("a lane grossing %d that owes %d in gates against a %d floor must be refused; kept %d, priced out %d",
			gross, gfGateBill, floor, len(kept), pricedOut)
	}
	if _, ok := selectLane(kept, ""); ok {
		t.Fatal("with its only candidate priced out, selection must report no disciplined lane")
	}

	// ClearsFloor is the executor's PER-VISIT discipline and is shared with the `market
	// spreads` scan. It must still admit this lane: the gate charge is an additional bound at
	// selection, not a redefinition of the floor every other caller reads.
	if !poor.ClearsFloor() {
		t.Fatal("ClearsFloor must be untouched and still admit this lane - the new bound is additive, not a rewrite")
	}
	if _, ok := trading.FirstDisciplinedLane([]trading.ArbitrageLane{poor}); !ok {
		t.Fatal("the shared ranked-order walk must be untouched and still return this lane")
	}
}

// A cross-system lane deep enough to pay its crossings must still fly. Without this, a filter
// that refused every gate crossing would pass every other test here.
func TestLanesClearingGateFees_KeepsACrossSystemLaneThatPaysItsCrossings(t *testing.T) {
	rich := gfRichCrossLane()

	kept, pricedOut := lanesClearingGateFees([]trading.ArbitrageLane{rich})

	if pricedOut != 0 || len(kept) != 1 {
		t.Fatalf("a lane grossing %d against a %d gate bill and a %d floor must survive; kept %d, priced out %d",
			int64(rich.SpreadPerUnit)*int64(rich.VolumeCap), gfGateBill,
			trading.MinBidMargin*rich.VolumeCap, len(kept), pricedOut)
	}
	if lane, ok := selectLane(kept, ""); !ok || lane.Good != rich.Good {
		t.Fatalf("the affordable cross-system lane must remain selectable, got %q (ok=%v)", lane.Good, ok)
	}
}

// RULINGS #4: the bound may only NARROW. The filter returns an ordered SUBSET of its input, so
// no lane can be added, re-ordered, or altered — and every lane it does remove is one whose own
// arithmetic, recomputed here rather than taken from the code under test, cannot pay its
// crossings. A guard that widened anything would have to break one of those two.
func TestLanesClearingGateFees_OnlyNarrows(t *testing.T) {
	corpus := []trading.ArbitrageLane{
		gfCrossLane(),
		{Good: "FUEL", SourceWaypoint: "X1-HOME-EX", DestWaypoint: "X1-HOME-IM",
			SpreadPerUnit: 1200, VolumeCap: 10, CappedSpread: 12000},
		gfRichCrossLane(),
		{Good: "FOOD", SourceWaypoint: "X1-HOME-EX", DestWaypoint: "X1-HOME-IM",
			SpreadPerUnit: 780, VolumeCap: 60, CappedSpread: 46800},
		// Deep spread, sink of one: 8,000 gross cannot cover 11,800 of gates.
		{Good: "ANTIMATTER", SourceWaypoint: "X1-NEAR-EX", DestWaypoint: "X1-HOME-IM",
			SpreadPerUnit: 9000, VolumeCap: 1, CappedSpread: 9000},
		// Wide sink, thin spread: 60 units at 1,100 gross 66,000 but owe a 60,000 floor.
		{Good: "GRAIN", SourceWaypoint: "X1-HOME-EX", DestWaypoint: "X1-FAR-IM",
			SpreadPerUnit: 1100, VolumeCap: 60, CappedSpread: 66000},
		{Good: "ICE_WATER", SourceWaypoint: "X1-NEAR-EX", DestWaypoint: "X1-NEAR-IM",
			SpreadPerUnit: 3000, VolumeCap: 2, CappedSpread: 6000},
	}
	before := append([]trading.ArbitrageLane(nil), corpus...)

	kept, pricedOut := lanesClearingGateFees(corpus)

	if !reflect.DeepEqual(corpus, before) {
		t.Fatal("the filter must not mutate its input slice")
	}
	if len(kept)+pricedOut != len(corpus) {
		t.Fatalf("every candidate is either kept or priced out: %d + %d != %d", len(kept), pricedOut, len(corpus))
	}

	// Ordered-subset walk: each kept lane must appear in the input, at or after the previous
	// one's position. Nothing invented, nothing promoted.
	at := 0
	for _, k := range kept {
		for at < len(corpus) && corpus[at] != k {
			at++
		}
		if at == len(corpus) {
			t.Fatalf("lane %+v is not an in-order member of the input - the filter widened or reordered", k)
		}
		at++
	}

	// Every REMOVAL must be justified by the lane's own economics.
	keptSet := map[string]bool{}
	for _, k := range kept {
		keptSet[k.Good] = true
	}
	for _, l := range corpus {
		if keptSet[l.Good] {
			continue
		}
		crossings := int64(laneGateCrossings(l))
		if crossings == 0 {
			t.Fatalf("%s crosses no gate and must never be refused here", l.Good)
		}
		net := int64(l.SpreadPerUnit)*int64(l.VolumeCap) - crossings*domainSensing.DefaultGateFeeCredits
		if net >= int64(trading.MinBidMargin)*int64(l.VolumeCap) {
			t.Fatalf("%s was refused but its trip nets %d over a %d floor after %d crossings - the guard refused a lane that COULD pay",
				l.Good, net, trading.MinBidMargin*l.VolumeCap, crossings)
		}
	}
	if pricedOut == 0 {
		t.Fatal("this corpus contains unaffordable crossings; a run that refuses nothing proves nothing")
	}
}

// An operator's --dest picks WHICH lane, never whether the money guards run. The directed path
// already respects the bid floor and the working-capital floor; the gate charge joins them.
func TestLanesClearingGateFees_DirectedTargetIsChargedToo(t *testing.T) {
	poor := gfCrossLane()
	target := poor.DestWaypoint

	if _, ok := selectLane([]trading.ArbitrageLane{poor}, target); !ok {
		t.Fatal("fixture check: the unfiltered directed path must still find this lane, or the test proves nothing")
	}

	kept, _ := lanesClearingGateFees([]trading.ArbitrageLane{poor})
	if _, ok := selectLane(kept, target); ok {
		t.Fatal("a directed lane that cannot pay its crossings must be refused, not flown because an operator named it")
	}
}

// --- end-to-end: the wiring, and the fallback the bead predicts ---

const (
	gfHomeSystem = "X1-GFH"
	gfNearSystem = "X1-GFN"
	gfExport     = "X1-GFH-EX" // exporter: both goods are bought here
	gfHomeImport = "X1-GFH-IM" // same-system sink for FUEL
	gfNearImport = "X1-GFN-IM" // one gate away: the GOLD sink

	gfGoldGood = "GOLD"
	gfFuelGood = "FUEL"
)

// gfMarketRepo serves a home system and one gated neighbour. GOLD pairs across the gate, FUEL
// inside the home system over the same depth. GOLD therefore RANKS FIRST (its bigger
// per-circuit value outweighs the rate model's time surcharge) and clears the per-unit floor —
// so the old selector committed to it — while its trip cannot cover its crossings.
type gfMarketRepo struct {
	market.MarketRepository
}

func (r *gfMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	switch systemSymbol {
	case gfHomeSystem:
		return []string{gfExport, gfHomeImport}, nil
	case gfNearSystem:
		return []string{gfNearImport}, nil
	}
	return nil, nil
}

func (r *gfMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	supply, activity := "MODERATE", "STRONG"
	build := func(symbol string, ask, bid, volume int, tradeType market.TradeType) (market.TradeGood, error) {
		g, err := market.NewTradeGood(symbol, &supply, &activity, ask, bid, volume, tradeType)
		if err != nil {
			return market.TradeGood{}, err
		}
		return *g, nil
	}

	var goods []market.TradeGood
	switch waypointSymbol {
	case gfExport:
		gold, err := build(gfGoldGood, 500, 480, 10, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		fuel, err := build(gfFuelGood, 300, 280, 10, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		goods = []market.TradeGood{gold, fuel}
	case gfHomeImport:
		fuel, err := build(gfFuelGood, 1600, 1500, 10, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		goods = []market.TradeGood{fuel}
	case gfNearImport:
		gold, err := build(gfGoldGood, 2600, 2500, 10, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		goods = []market.TradeGood{gold}
	default:
		return nil, nil
	}
	return market.NewMarket(waypointSymbol, goods, time.Now())
}

// gfMediator answers the gate-neighbour discovery scanLanes runs, prices buys and sells from
// the same numbers the markets quote, and no-ops navigation.
type gfMediator struct {
	mu        sync.Mutex
	purchases []*shipCargo.PurchaseCargoCommand
	sells     []*shipCargo.SellCargoCommand
}

func (m *gfMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipQuery.GetJumpGateConnectionsQuery:
		if cmd.SystemSymbol == gfHomeSystem {
			return &shipQuery.GetJumpGateConnectionsResponse{ConnectedSystems: []string{gfNearSystem}}, nil
		}
		return &shipQuery.GetJumpGateConnectionsResponse{}, nil
	case *shipCargo.PurchaseCargoCommand:
		m.mu.Lock()
		m.purchases = append(m.purchases, cmd)
		m.mu.Unlock()
		ask := 300
		if cmd.GoodSymbol == gfGoldGood {
			ask = 500
		}
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * ask, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		m.sells = append(m.sells, cmd)
		m.mu.Unlock()
		bid := 1500
		if cmd.GoodSymbol == gfGoldGood {
			bid = 2500
		}
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * bid, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil
	}
}

func (m *gfMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *gfMediator) RegisterMiddleware(middleware common.Middleware) {}

func (m *gfMediator) traded(good string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.purchases {
		if p.GoodSymbol == good {
			return true
		}
	}
	for _, s := range m.sells {
		if s.GoodSymbol == good {
			return true
		}
	}
	return false
}

// The bead's predicted effect, end to end through Handle: the gate-crossing lane the ranker
// seats first is priced out, and the hull falls back to the within-system lane instead of
// paying a crossing to earn one crossing's margin. This is also the wiring pin — the filter is
// only worth anything if execute() actually runs it.
func TestTradeRouteCoordinator_PricesGateFees_FallsBackToTheWithinSystemLane(t *testing.T) {
	ship := newDiscHauler(t, "GATEFEE-1", gfExport)
	mediator := &gfMediator{}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, &trFakeShipRepo{ship: ship}, &gfMarketRepo{}, nil, nil, nil)

	// The ranker must seat GOLD first, or the fallback below is vacuous.
	lanes, err := handler.scanLanes(context.Background(), gfHomeSystem, 1, ship.CargoCapacity(), "")
	if err != nil {
		t.Fatalf("scanning lanes: %v", err)
	}
	if len(lanes) == 0 || lanes[0].Good != gfGoldGood {
		t.Fatalf("fixture check: the cross-system GOLD lane must rank first for this test to mean anything, got %+v", lanes)
	}
	if !lanes[0].ClearsFloor() {
		t.Fatal("fixture check: GOLD must clear the per-unit floor - that is what made the old selector take it")
	}

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: gfHomeSystem,
		PlayerID:     1,
		MaxVisits:    1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	if coord.Good != gfFuelGood {
		t.Fatalf("expected the fleet to fall back to the within-system %s lane, got %q: the gate charge is not reaching selection",
			gfFuelGood, coord.Good)
	}
	if coord.DestWaypoint != gfHomeImport {
		t.Fatalf("expected a circuit that stays in %s, got destination %q", gfHomeSystem, coord.DestWaypoint)
	}
	if mediator.traded(gfGoldGood) {
		t.Fatalf("%s cannot pay its two crossings and must never be bought or sold", gfGoldGood)
	}
	if coord.Visits < 1 {
		t.Fatalf("the surviving within-system lane must still fly: got %d visits", coord.Visits)
	}
	if coord.NoDisciplinedLane {
		t.Fatal("a within-system lane cleared the floor; pricing gates must not report the board empty")
	}
}

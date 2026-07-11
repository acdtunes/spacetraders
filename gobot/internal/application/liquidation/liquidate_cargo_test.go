package liquidation

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- fakes -------------------------------------------------------------------

// fakeSyncShipRepo returns a canned ship from SyncShipFromAPI (the server-truth
// reconcile the liquidation worker does before touching cargo, mirroring the
// manufacturing orphaned-cargo handler's phantom-desync guard, cluster L47).
type fakeSyncShipRepo struct {
	navigation.ShipRepository
	ship    *navigation.Ship
	syncErr error
}

func (r *fakeSyncShipRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	if r.syncErr != nil {
		return nil, r.syncErr
	}
	return r.ship, nil
}

// fakeMarketRepo answers FindBestMarketBuying from a per-good table. An absent
// good models "no in-system market bids this good".
type fakeMarketRepo struct {
	market.MarketRepository
	byGood map[string]*market.BestMarketBuyingResult
	err    error
}

func (r *fakeMarketRepo) FindBestMarketBuying(_ context.Context, good, _ string, _ int) (*market.BestMarketBuyingResult, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.byGood[good], nil
}

// recordingMediator records every dispatched command in order and returns canned
// results/errors keyed by command type, so a test asserts exactly which ship I/O
// the worker issued (navigate/dock/sell/jettison) and in what order.
type recordingMediator struct {
	sent             []common.Request
	navErr           error
	dockErr          error
	sellErr          error
	jettisonErr      error
	sellPricePerUnit int
	sellUnitsCap     int // 0 => sell the full requested units
}

func (m *recordingMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	m.sent = append(m.sent, request)
	switch cmd := request.(type) {
	case *navCmd.NavigateRouteCommand:
		return nil, m.navErr
	case *shipTypes.DockShipCommand:
		return nil, m.dockErr
	case *shipCargo.SellCargoCommand:
		if m.sellErr != nil {
			return nil, m.sellErr
		}
		units := cmd.Units
		if m.sellUnitsCap > 0 && m.sellUnitsCap < units {
			units = m.sellUnitsCap
		}
		return &shipCargo.SellCargoResponse{UnitsSold: units, TotalRevenue: units * m.sellPricePerUnit}, nil
	case *shipCargo.JettisonCargoCommand:
		return nil, m.jettisonErr
	default:
		return nil, fmt.Errorf("recordingMediator: unexpected command %T", request)
	}
}

func (m *recordingMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }
func (m *recordingMediator) RegisterMiddleware(_ common.Middleware)                 {}

func (m *recordingMediator) countOf(target interface{}) int {
	want := reflect.TypeOf(target)
	n := 0
	for _, r := range m.sent {
		if reflect.TypeOf(r) == want {
			n++
		}
	}
	return n
}

func (m *recordingMediator) firstSell() *shipCargo.SellCargoCommand {
	for _, r := range m.sent {
		if s, ok := r.(*shipCargo.SellCargoCommand); ok {
			return s
		}
	}
	return nil
}

func (m *recordingMediator) firstNavigate() *navCmd.NavigateRouteCommand {
	for _, r := range m.sent {
		if n, ok := r.(*navCmd.NavigateRouteCommand); ok {
			return n
		}
	}
	return nil
}

func (m *recordingMediator) firstJettison() *shipCargo.JettisonCargoCommand {
	for _, r := range m.sent {
		if j, ok := r.(*shipCargo.JettisonCargoCommand); ok {
			return j
		}
	}
	return nil
}

// shipWithCargo builds a docked hull at waypointSymbol holding the given inventory.
func shipWithCargo(t *testing.T, symbol, waypointSymbol string, inventory []*shared.CargoItem) *navigation.Ship {
	t.Helper()
	units := 0
	for _, item := range inventory {
		units += item.Units
	}
	cargo, err := shared.NewCargo(200, units, inventory)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	wp, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), wp, fuel, 400, 200, cargo, 30, "FRAME_FRIGATE", "HAULER", nil, navigation.NavStatusDocked)
	require.NoError(t, err)
	return ship
}

func item(t *testing.T, symbol string, units int) *shared.CargoItem {
	t.Helper()
	ci, err := shared.NewCargoItem(symbol, symbol, "", units)
	require.NoError(t, err)
	return ci
}

// --- tests -------------------------------------------------------------------

// The acceptance core at the worker level: a parked hull holding leftover cargo
// sells it at the best in-system bid (navigating there first) and reports the
// recovered revenue — the value-recovery path the captain wants for the 3 valuable
// PLASTICS/SILICON/FABRICS holds (sp-39oi).
func TestLiquidateCargo_SellsAtBestInSystemBid_NavigatingWhenElsewhere(t *testing.T) {
	ship := shipWithCargo(t, "TORWIND-7", "X1-KA42-A1", []*shared.CargoItem{item(t, "SILICON_CRYSTALS", 66)})
	repo := &fakeSyncShipRepo{ship: ship}
	mkt := &fakeMarketRepo{byGood: map[string]*market.BestMarketBuyingResult{
		"SILICON_CRYSTALS": {WaypointSymbol: "X1-KA42-B7", TradeSymbol: "SILICON_CRYSTALS", PurchasePrice: 2200},
	}}
	med := &recordingMediator{sellPricePerUnit: 2200}
	h := NewLiquidateCargoHandler(repo, mkt, med)

	resp, err := h.Handle(context.Background(), &LiquidateCargoCommand{
		PlayerID:   shared.MustNewPlayerID(1),
		ShipSymbol: "TORWIND-7",
	})

	require.NoError(t, err)
	r := resp.(*LiquidateCargoResponse)
	require.Equal(t, 66, r.UnitsSold, "the full leftover lot is sold")
	require.Equal(t, 66*2200, r.TotalRevenue, "recovered value lands on the response ledger")
	require.Equal(t, 0, r.UnitsHeld)
	require.Equal(t, 0, r.UnitsJettisoned)

	nav := med.firstNavigate()
	require.NotNil(t, nav, "the worker navigates to the best in-system sell market")
	require.Equal(t, "X1-KA42-B7", nav.Destination)
	sell := med.firstSell()
	require.NotNil(t, sell)
	require.Equal(t, "SILICON_CRYSTALS", sell.GoodSymbol)
	require.Equal(t, 66, sell.Units)
	require.Equal(t, 0, sell.MinBidPerUnit, "liquidation recovers sunk cost — no sell floor blocks it (RULINGS #4: floors don't block a revenue event)")
}

// When the current waypoint already IS the best sink, the worker sells in place —
// no navigation, no fuel spent (the ladder's first rung).
func TestLiquidateCargo_SellsInPlace_WhenCurrentWaypointIsBest(t *testing.T) {
	ship := shipWithCargo(t, "TORWIND-3", "X1-KA42-B7", []*shared.CargoItem{item(t, "PLASTICS", 67)})
	repo := &fakeSyncShipRepo{ship: ship}
	mkt := &fakeMarketRepo{byGood: map[string]*market.BestMarketBuyingResult{
		"PLASTICS": {WaypointSymbol: "X1-KA42-B7", PurchasePrice: 1800},
	}}
	med := &recordingMediator{sellPricePerUnit: 1800}
	h := NewLiquidateCargoHandler(repo, mkt, med)

	resp, err := h.Handle(context.Background(), &LiquidateCargoCommand{PlayerID: shared.MustNewPlayerID(1), ShipSymbol: "TORWIND-3"})

	require.NoError(t, err)
	r := resp.(*LiquidateCargoResponse)
	require.Equal(t, 67, r.UnitsSold)
	require.Equal(t, 0, med.countOf(&navCmd.NavigateRouteCommand{}), "no navigation when already at the best sink")
	require.Equal(t, 1, med.countOf(&shipCargo.SellCargoCommand{}))
}

// An empty hold (or a phantom hold the server reconcile clears) is an idempotent
// no-op success: the worker touches nothing and the hull re-enters candidacy.
func TestLiquidateCargo_EmptyHold_IdempotentNoop(t *testing.T) {
	ship := shipWithCargo(t, "TORWIND-9", "X1-KA42-A1", nil)
	repo := &fakeSyncShipRepo{ship: ship}
	med := &recordingMediator{}
	h := NewLiquidateCargoHandler(repo, &fakeMarketRepo{}, med)

	resp, err := h.Handle(context.Background(), &LiquidateCargoCommand{PlayerID: shared.MustNewPlayerID(1), ShipSymbol: "TORWIND-9"})

	require.NoError(t, err)
	r := resp.(*LiquidateCargoResponse)
	require.Equal(t, 0, r.UnitsSold)
	require.Equal(t, 0, r.UnitsHeld)
	require.Empty(t, med.sent, "no ship I/O on an already-empty hull")
}

// Default posture (min_jettison_value=0): a good with NO in-system bid is HELD, not
// destroyed — nothing is jettisoned without an explicit threshold (sp-39oi RULINGS
// #5: value is never dumped by default).
func TestLiquidateCargo_NoInSystemBid_JettisonDisabled_HoldsAndProtectsValue(t *testing.T) {
	ship := shipWithCargo(t, "TORWIND-5", "X1-KA42-A1", []*shared.CargoItem{item(t, "FABRICS", 54)})
	repo := &fakeSyncShipRepo{ship: ship}
	mkt := &fakeMarketRepo{byGood: map[string]*market.BestMarketBuyingResult{}} // no market bids FABRICS
	med := &recordingMediator{}
	h := NewLiquidateCargoHandler(repo, mkt, med)

	resp, err := h.Handle(context.Background(), &LiquidateCargoCommand{PlayerID: shared.MustNewPlayerID(1), ShipSymbol: "TORWIND-5", MinJettisonValue: 0})

	require.NoError(t, err)
	r := resp.(*LiquidateCargoResponse)
	require.Equal(t, 54, r.UnitsHeld, "unsellable cargo is held when jettison is disabled")
	require.Equal(t, 0, r.UnitsJettisoned)
	require.Equal(t, 0, med.countOf(&shipCargo.JettisonCargoCommand{}), "nothing destroyed without an explicit threshold")
}

// With an explicit threshold, genuinely stuck cargo (no in-system bid, value below
// the floor) is jettisoned as the LAST resort — the captain opts in to clear junk.
func TestLiquidateCargo_NoInSystemBid_JettisonEnabled_JettisonsAsLastResort(t *testing.T) {
	ship := shipWithCargo(t, "TORWIND-6", "X1-KA42-A1", []*shared.CargoItem{item(t, "QUARTZ_SAND", 66)})
	repo := &fakeSyncShipRepo{ship: ship}
	mkt := &fakeMarketRepo{byGood: map[string]*market.BestMarketBuyingResult{}} // no market
	med := &recordingMediator{}
	h := NewLiquidateCargoHandler(repo, mkt, med)

	resp, err := h.Handle(context.Background(), &LiquidateCargoCommand{PlayerID: shared.MustNewPlayerID(1), ShipSymbol: "TORWIND-6", MinJettisonValue: 5000})

	require.NoError(t, err)
	r := resp.(*LiquidateCargoResponse)
	require.Equal(t, 66, r.UnitsJettisoned)
	require.Equal(t, 0, r.UnitsSold)
	j := med.firstJettison()
	require.NotNil(t, j, "unsellable below-threshold cargo is jettisoned as last resort")
	require.Equal(t, "QUARTZ_SAND", j.GoodSymbol)
	require.Equal(t, 66, j.Units)
}

// A valuable lot is ALWAYS sold, never jettisoned, even with a threshold set: the
// recoverable value (bid*units) clears the floor, so it takes the sell path. This is
// the direct guard for the incident's 3 hulls (~150k PLASTICS/SILICON/FABRICS).
func TestLiquidateCargo_ValuableLot_SoldNotJettisoned_EvenWithThreshold(t *testing.T) {
	ship := shipWithCargo(t, "TORWIND-11", "X1-KA42-A1", []*shared.CargoItem{item(t, "PLASTICS", 67)})
	repo := &fakeSyncShipRepo{ship: ship}
	mkt := &fakeMarketRepo{byGood: map[string]*market.BestMarketBuyingResult{
		"PLASTICS": {WaypointSymbol: "X1-KA42-B7", PurchasePrice: 2300}, // 67*2300 = 154,100
	}}
	med := &recordingMediator{sellPricePerUnit: 2300}
	h := NewLiquidateCargoHandler(repo, mkt, med)

	resp, err := h.Handle(context.Background(), &LiquidateCargoCommand{PlayerID: shared.MustNewPlayerID(1), ShipSymbol: "TORWIND-11", MinJettisonValue: 10000})

	require.NoError(t, err)
	r := resp.(*LiquidateCargoResponse)
	require.Equal(t, 67, r.UnitsSold, "value above the jettison floor is recovered, never dumped")
	require.Equal(t, 0, r.UnitsJettisoned)
}

// Movement guard (RULINGS #4): if the worker cannot reach the sell market (fuel/route
// guard fails the navigate), it HOLDS the cargo rather than destroying value, and
// reports success so the container exits cleanly and the coordinator re-evaluates later.
func TestLiquidateCargo_NavigateFails_HoldsNeverDumps(t *testing.T) {
	ship := shipWithCargo(t, "TORWIND-2", "X1-KA42-A1", []*shared.CargoItem{item(t, "ALUMINUM", 56)})
	repo := &fakeSyncShipRepo{ship: ship}
	mkt := &fakeMarketRepo{byGood: map[string]*market.BestMarketBuyingResult{
		"ALUMINUM": {WaypointSymbol: "X1-KA42-Z9", PurchasePrice: 900},
	}}
	med := &recordingMediator{navErr: fmt.Errorf("insufficient fuel and no affordable refuel"), sellPricePerUnit: 900}
	h := NewLiquidateCargoHandler(repo, mkt, med)

	resp, err := h.Handle(context.Background(), &LiquidateCargoCommand{PlayerID: shared.MustNewPlayerID(1), ShipSymbol: "TORWIND-2", MinJettisonValue: 5000})

	require.NoError(t, err, "an unreachable sink is a hold, not a container failure")
	r := resp.(*LiquidateCargoResponse)
	require.Equal(t, 56, r.UnitsHeld, "cargo is held when the sink is unreachable")
	require.Equal(t, 0, r.UnitsSold)
	require.Equal(t, 0, r.UnitsJettisoned, "a valuable-but-unreachable lot is never jettisoned to make the movement guard 'succeed'")
	require.Equal(t, 0, med.countOf(&shipCargo.SellCargoCommand{}), "no sell attempted after the move failed")
}

// A hull holding several leftover lots liquidates each in turn.
func TestLiquidateCargo_MultipleGoods_LiquidatesEach(t *testing.T) {
	ship := shipWithCargo(t, "TORWIND-1", "X1-KA42-B7", []*shared.CargoItem{
		item(t, "SILICON_CRYSTALS", 20),
		item(t, "FABRICS", 30),
	})
	repo := &fakeSyncShipRepo{ship: ship}
	mkt := &fakeMarketRepo{byGood: map[string]*market.BestMarketBuyingResult{
		"SILICON_CRYSTALS": {WaypointSymbol: "X1-KA42-B7", PurchasePrice: 1000},
		"FABRICS":          {WaypointSymbol: "X1-KA42-B7", PurchasePrice: 1000},
	}}
	med := &recordingMediator{sellPricePerUnit: 1000}
	h := NewLiquidateCargoHandler(repo, mkt, med)

	resp, err := h.Handle(context.Background(), &LiquidateCargoCommand{PlayerID: shared.MustNewPlayerID(1), ShipSymbol: "TORWIND-1"})

	require.NoError(t, err)
	r := resp.(*LiquidateCargoResponse)
	require.Equal(t, 50, r.UnitsSold, "both lots are sold")
	require.Equal(t, 2, med.countOf(&shipCargo.SellCargoCommand{}), "one sell per good")
}

// A server-reconcile failure fails the container honestly rather than acting on an
// unverifiable cargo snapshot (sp-39oi: never sell/dump on a state we cannot confirm).
func TestLiquidateCargo_SyncFailure_FailsHonestly(t *testing.T) {
	repo := &fakeSyncShipRepo{syncErr: fmt.Errorf("api 503")}
	h := NewLiquidateCargoHandler(repo, &fakeMarketRepo{}, &recordingMediator{})

	_, err := h.Handle(context.Background(), &LiquidateCargoCommand{PlayerID: shared.MustNewPlayerID(1), ShipSymbol: "TORWIND-4"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "api 503")
}

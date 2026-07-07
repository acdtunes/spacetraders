package cargo

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests pin the manual "ship buy" behavior (bead sp-71bj) at the handler
// boundary the CLI verb dispatches to. They mirror the acceptance matrix of the
// existing sell path: a docked ship purchases at its waypoint market, records a
// PURCHASE_CARGO ledger transaction, and errors clearly when the ship is not
// docked, the good is not traded, credits are insufficient, or cargo capacity is
// exceeded. The purchase pipeline itself is the shared CargoTransactionHandler
// reused by manufacturing and contract flows; these tests lock its buy behavior.

const (
	testBuyWaypoint = "X1-TEST-A1"
	testBuyGood     = "IRON_ORE"
	testBuyShip     = "BUYER-1"
)

// buyFakeShipRepo embeds the interface so unused methods panic, keeping the fake
// honest about what the purchase handler actually touches.
type buyFakeShipRepo struct {
	navigation.ShipRepository
	ship  *navigation.Ship
	saved bool
}

func (r *buyFakeShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

func (r *buyFakeShipRepo) Save(ctx context.Context, ship *navigation.Ship) error {
	r.saved = true
	return nil
}

type buyFakePlayerRepo struct {
	player.PlayerRepository
	player *player.Player
}

func (r *buyFakePlayerRepo) FindByID(ctx context.Context, playerID shared.PlayerID) (*player.Player, error) {
	return r.player, nil
}

type purchaseCall struct {
	shipSymbol string
	goodSymbol string
	units      int
}

type buyFakeAPIClient struct {
	domainPorts.APIClient
	result *domainPorts.PurchaseResult
	err    error
	calls  []purchaseCall
}

func (c *buyFakeAPIClient) PurchaseCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*domainPorts.PurchaseResult, error) {
	c.calls = append(c.calls, purchaseCall{shipSymbol: shipSymbol, goodSymbol: goodSymbol, units: units})
	if c.err != nil {
		return nil, c.err
	}
	return c.result, nil
}

// buyFakeMarketRepo returns no market data so GetTransactionLimit falls back to a
// single transaction of the requested size — the simplest deterministic limit.
type buyFakeMarketRepo struct {
	scoutingQuery.MarketRepository
}

func (r *buyFakeMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	return nil, nil
}

// buyRecordingMediator captures ledger RecordTransactionCommand dispatches so the
// PURCHASE_CARGO transaction can be asserted; every other command is a no-op.
type buyRecordingMediator struct {
	recorded []*ledgerCommands.RecordTransactionCommand
}

func (m *buyRecordingMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	if cmd, ok := request.(*ledgerCommands.RecordTransactionCommand); ok {
		m.recorded = append(m.recorded, cmd)
	}
	return nil, nil
}

func (m *buyRecordingMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}

func (m *buyRecordingMediator) RegisterMiddleware(middleware common.Middleware) {}

func newDockedBuyer(t *testing.T, cargoCapacity, usedUnits int, status navigation.NavStatus) *navigation.Ship {
	t.Helper()

	var inventory []*shared.CargoItem
	if usedUnits > 0 {
		filler, err := shared.NewCargoItem("FILLER", "FILLER", "", usedUnits)
		require.NoError(t, err)
		inventory = append(inventory, filler)
	}
	cargo, err := shared.NewCargo(cargoCapacity, usedUnits, inventory)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	waypoint, err := shared.NewWaypoint(testBuyWaypoint, 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		testBuyShip,
		shared.MustNewPlayerID(1),
		waypoint,
		fuel,
		100,
		cargoCapacity,
		cargo,
		30,
		"FRAME_LIGHT_FREIGHTER",
		"HAULER",
		nil,
		status,
	)
	require.NoError(t, err)
	return ship
}

func newBuyHandler(ship *navigation.Ship, api *buyFakeAPIClient, med *buyRecordingMediator) (*PurchaseCargoHandler, *buyFakeShipRepo) {
	shipRepo := &buyFakeShipRepo{ship: ship}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "ENDURANCE", "test-token")}
	marketRepo := &buyFakeMarketRepo{}
	handler := NewPurchaseCargoHandler(shipRepo, playerRepo, api, marketRepo, med, nil)
	return handler, shipRepo
}

func buyCtx() context.Context {
	return auth.WithPlayerToken(context.Background(), "test-token")
}

func TestPurchaseCargoHappyPathBuysAtWaypointAndRecordsLedger(t *testing.T) {
	ship := newDockedBuyer(t, 40, 0, navigation.NavStatusDocked)
	api := &buyFakeAPIClient{result: &domainPorts.PurchaseResult{TotalCost: 500, UnitsAdded: 10}}
	med := &buyRecordingMediator{}
	handler, shipRepo := newBuyHandler(ship, api, med)

	resp, err := handler.Handle(buyCtx(), &PurchaseCargoCommand{
		ShipSymbol: testBuyShip,
		GoodSymbol: testBuyGood,
		Units:      10,
		PlayerID:   shared.MustNewPlayerID(1),
	})
	require.NoError(t, err)

	result, ok := resp.(*PurchaseCargoResponse)
	require.True(t, ok)
	require.Equal(t, 500, result.TotalCost)
	require.Equal(t, 10, result.UnitsAdded)
	require.Equal(t, 1, result.TransactionCount)

	// Bought from the market at the ship's docked waypoint.
	require.Len(t, api.calls, 1)
	require.Equal(t, testBuyShip, api.calls[0].shipSymbol)
	require.Equal(t, testBuyGood, api.calls[0].goodSymbol)
	require.Equal(t, 10, api.calls[0].units)
	require.True(t, shipRepo.saved, "ship cargo change should be persisted")

	// Recorded a PURCHASE_CARGO ledger transaction with a post-transaction balance.
	require.Len(t, med.recorded, 1)
	tx := med.recorded[0]
	require.Equal(t, "PURCHASE_CARGO", tx.TransactionType)
	require.Equal(t, -500, tx.Amount, "purchase is an expense (negative amount)")
	require.Equal(t, -500, tx.BalanceAfter, "post-transaction balance reflects the spend")
	require.Equal(t, testBuyGood, tx.Metadata["good_symbol"])
	require.Equal(t, 10, tx.Metadata["units"])
	require.Equal(t, testBuyWaypoint, tx.Metadata["waypoint"])
}

func TestPurchaseCargoErrorsWhenNotDocked(t *testing.T) {
	ship := newDockedBuyer(t, 40, 0, navigation.NavStatusInOrbit)
	api := &buyFakeAPIClient{result: &domainPorts.PurchaseResult{TotalCost: 500, UnitsAdded: 10}}
	med := &buyRecordingMediator{}
	handler, _ := newBuyHandler(ship, api, med)

	_, err := handler.Handle(buyCtx(), &PurchaseCargoCommand{
		ShipSymbol: testBuyShip,
		GoodSymbol: testBuyGood,
		Units:      10,
		PlayerID:   shared.MustNewPlayerID(1),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be docked")
	require.Empty(t, api.calls, "no purchase API call when not docked")
	require.Empty(t, med.recorded, "no ledger entry when not docked")
}

func TestPurchaseCargoErrorsWhenGoodNotTraded(t *testing.T) {
	ship := newDockedBuyer(t, 40, 0, navigation.NavStatusDocked)
	api := &buyFakeAPIClient{err: errors.New("trade good IRON_ORE is not sold at this market")}
	med := &buyRecordingMediator{}
	handler, _ := newBuyHandler(ship, api, med)

	_, err := handler.Handle(buyCtx(), &PurchaseCargoCommand{
		ShipSymbol: testBuyShip,
		GoodSymbol: testBuyGood,
		Units:      10,
		PlayerID:   shared.MustNewPlayerID(1),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not sold at this market")
	require.Empty(t, med.recorded, "no ledger entry when purchase fails")
}

func TestPurchaseCargoErrorsWhenInsufficientCredits(t *testing.T) {
	ship := newDockedBuyer(t, 40, 0, navigation.NavStatusDocked)
	api := &buyFakeAPIClient{err: errors.New("insufficient credits: need 500, have 100")}
	med := &buyRecordingMediator{}
	handler, _ := newBuyHandler(ship, api, med)

	_, err := handler.Handle(buyCtx(), &PurchaseCargoCommand{
		ShipSymbol: testBuyShip,
		GoodSymbol: testBuyGood,
		Units:      10,
		PlayerID:   shared.MustNewPlayerID(1),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient credits")
	require.Empty(t, med.recorded, "no ledger entry when purchase fails")
}

func TestPurchaseCargoErrorsWhenExceedsCargoCapacity(t *testing.T) {
	// Capacity 40, 35 already used -> only 5 free, requesting 10.
	ship := newDockedBuyer(t, 40, 35, navigation.NavStatusDocked)
	api := &buyFakeAPIClient{result: &domainPorts.PurchaseResult{TotalCost: 500, UnitsAdded: 10}}
	med := &buyRecordingMediator{}
	handler, _ := newBuyHandler(ship, api, med)

	_, err := handler.Handle(buyCtx(), &PurchaseCargoCommand{
		ShipSymbol: testBuyShip,
		GoodSymbol: testBuyGood,
		Units:      10,
		PlayerID:   shared.MustNewPlayerID(1),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient cargo space")
	require.Empty(t, api.calls, "capacity is validated before any purchase API call")
	require.Empty(t, med.recorded, "no ledger entry when capacity is exceeded")
}

package cargo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// These tests run the real CargoTransactionHandler through the real ledger recorder
// onto a FK-enforcing SQLite DB (sp-lgnh). They prove the end result the bead exists
// for: a cargo transaction dispatched under the tour operation context lands an
// operation_type='tour' row in the transactions table, for BOTH a buy and a sell —
// and that an unrelated caller's tag (trade_route) is byte-identical to before, so
// the additive marker re-tagged nothing else.

const optypeGood = "IRON_ORE"

// ledgerRoutingMediator wires the cargo handler's ledger dispatch to the real
// RecordTransactionHandler so the row is actually persisted (embeds the interface so
// the unused Register/RegisterMiddleware methods are never implemented).
type ledgerRoutingMediator struct {
	common.Mediator
	record *ledgerCommands.RecordTransactionHandler
}

func (m *ledgerRoutingMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	if _, ok := request.(*ledgerCommands.RecordTransactionCommand); ok {
		return m.record.Handle(ctx, request)
	}
	return nil, nil
}

// optypeFakeAPI returns deterministic purchase/sell results so a single 10-unit
// transaction records exactly one non-zero-amount ledger row.
type optypeFakeAPI struct {
	domainPorts.APIClient
}

func (c *optypeFakeAPI) PurchaseCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*domainPorts.PurchaseResult, error) {
	return &domainPorts.PurchaseResult{TotalCost: units * 10, UnitsAdded: units}, nil
}

func (c *optypeFakeAPI) SellCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*domainPorts.SellResult, error) {
	return &domainPorts.SellResult{TotalRevenue: units * 20, UnitsSold: units}, nil
}

func newDockedShipWithCargo(t *testing.T, playerID int, good string, units int) *navigation.Ship {
	t.Helper()
	inv := []*shared.CargoItem{{Symbol: good, Units: units}}
	cargo, err := shared.NewCargo(40, units, inv)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	waypoint, err := shared.NewWaypoint(testBuyWaypoint, 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip("OPTYPE-1", shared.MustNewPlayerID(playerID), waypoint, fuel, 100, 40,
		cargo, 30, "FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked)
	require.NoError(t, err)
	return ship
}

// persistedCargoOpType runs one cargo transaction (kind "purchase"/"sell") under the
// given raw operation context and returns the operation_type persisted on the single
// resulting ledger row. rawOpType "" dispatches with no operation context.
func persistedCargoOpType(t *testing.T, kind, rawOpType string) string {
	t.Helper()

	db, err := database.NewTestConnection()
	require.NoError(t, err)

	// Seed the FK parent: transactions.player_id references players.id, and the test
	// harness enforces foreign keys (a bare write would 23503 like production).
	p := persistence.PlayerModel{AgentSymbol: "AGT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	playerID := p.ID

	med := &ledgerRoutingMediator{record: ledgerCommands.NewRecordTransactionHandler(persistence.NewGormTransactionRepository(db), nil)}
	api := &optypeFakeAPI{}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(playerID), "ENDURANCE", "tok")}
	marketRepo := &buyFakeMarketRepo{}
	shipRepo := &buyFakeShipRepo{ship: newDockedShipWithCargo(t, playerID, optypeGood, 20)}

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	if rawOpType != "" {
		ctx = shared.WithOperationContext(ctx, shared.NewOperationContext("ctr-optype", rawOpType))
	}

	switch kind {
	case "purchase":
		h := NewPurchaseCargoHandler(shipRepo, playerRepo, api, marketRepo, med, nil)
		_, err = h.Handle(ctx, &PurchaseCargoCommand{ShipSymbol: "OPTYPE-1", GoodSymbol: optypeGood, Units: 10, PlayerID: shared.MustNewPlayerID(playerID)})
	case "sell":
		h := NewSellCargoHandler(shipRepo, playerRepo, api, marketRepo, med, nil)
		_, err = h.Handle(ctx, &SellCargoCommand{ShipSymbol: "OPTYPE-1", GoodSymbol: optypeGood, Units: 10, PlayerID: shared.MustNewPlayerID(playerID)})
	default:
		t.Fatalf("unknown kind %q", kind)
	}
	require.NoError(t, err)

	var rows []persistence.TransactionModel
	require.NoError(t, db.Where("player_id = ?", playerID).Find(&rows).Error)
	require.Len(t, rows, 1, "expected exactly one persisted ledger row")
	return rows[0].OperationType
}

func TestCargoTransaction_TourContextPersistsOperationTypeTour(t *testing.T) {
	if got := persistedCargoOpType(t, "purchase", "tour_run"); got != "tour" {
		t.Errorf("tour buy persisted operation_type=%q, want \"tour\"", got)
	}
	if got := persistedCargoOpType(t, "sell", "tour_run"); got != "tour" {
		t.Errorf("tour sell persisted operation_type=%q, want \"tour\"", got)
	}
}

// Regression: a trade-route cargo write is unchanged by the tour tagging — its
// operation_type is still "trade_route" (the additive marker touches only tour_run).
func TestCargoTransaction_TradeRouteContextUnchanged(t *testing.T) {
	if got := persistedCargoOpType(t, "purchase", "trade_route"); got != "trade_route" {
		t.Errorf("trade-route buy persisted operation_type=%q, want \"trade_route\" (unchanged)", got)
	}
}

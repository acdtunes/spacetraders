package manufacturing

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ledgerCountingMediator counts how many ledger rows a manufacturing money event
// produces. It models the two ways a row can be written for one buy/sell:
//
//  1. Via the cargo command (PurchaseCargoCommand/SellCargoCommand). That command
//     is the single AUTHORITATIVE recorder: PurchaseCargoHandler/SellCargoHandler
//     delegate to CargoTransactionHandler, which self-records exactly one ledger row
//     per successful batch with the in-band agent.credits
//     (internal/application/ship/commands/cargo/cargo_transaction.go
//     recordCargoTransaction, made authoritative by sp-sc6u).
//
//  2. Via a RecordTransactionCommand issued directly by the manufacturing layer
//     (the ManufacturingLedgerRecorder). This is the REDUNDANT second record that
//     double-counts the transaction — the bug under investigation (sp-uytq).
//
// Exactly one manufacturing buy/sell must yield exactly one ledger row.
type ledgerCountingMediator struct {
	common.Mediator // embedded; only Send is overridden

	ledgerRows int // total ledger rows written for the flow
	cargoSends int // buy/sell commands dispatched (the canonical, self-recording path)
}

func (m *ledgerCountingMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	switch req := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.cargoSends++
		m.ledgerRows++ // the cargo path self-records exactly once (the canonical row)
		return &shipCargo.PurchaseCargoResponse{
			UnitsAdded:       req.Units,
			TotalCost:        req.Units * 10,
			TransactionCount: 1,
		}, nil
	case *shipCargo.SellCargoCommand:
		m.cargoSends++
		m.ledgerRows++ // the cargo path self-records exactly once (the canonical row)
		return &shipCargo.SellCargoResponse{
			UnitsSold:        req.Units,
			TotalRevenue:     req.Units * 20,
			TransactionCount: 1,
		}, nil
	case *ledgerCommands.RecordTransactionCommand:
		m.ledgerRows++ // a row written directly by the manufacturing layer (the redundant double)
		return &ledgerCommands.RecordTransactionResponse{}, nil
	default:
		return nil, nil
	}
}

// stubShipRepo returns a fixed ship from FindBySymbol for the purchase loop.
type stubShipRepo struct {
	navigation.ShipRepository
	ship *navigation.Ship
}

func (s *stubShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return s.ship, nil
}

// stubMarketRepo returns a fixed market from GetMarketData for the purchase loop.
type stubMarketRepo struct {
	market.MarketRepository
	mkt *market.Market
}

func (s *stubMarketRepo) GetMarketData(_ context.Context, _ string, _ int) (*market.Market, error) {
	return s.mkt, nil
}

func newExportMarket(t *testing.T, waypoint, good string) *market.Market {
	t.Helper()
	supply := "ABUNDANT"
	tg, err := market.NewTradeGood(good, &supply, nil, 10, 8, 100, market.TradeTypeExport)
	if err != nil {
		t.Fatalf("NewTradeGood: %v", err)
	}
	mkt, err := market.NewMarket(waypoint, []market.TradeGood{*tg}, time.Now())
	if err != nil {
		t.Fatalf("NewMarket: %v", err)
	}
	return mkt
}

// A manufacturing SELL routes through the cargo command (which self-records the
// authoritative row) AND then calls the ManufacturingLedgerRecorder, producing a
// second row for the same event. The ledger must gain exactly one row per sale.
func TestManufacturingSeller_SellCargo_WritesOneLedgerRowPerSale(t *testing.T) {
	spy := &ledgerCountingMediator{}
	seller := &ManufacturingSeller{mediator: spy}

	if _, err := seller.SellCargo(context.Background(), SellParams{
		ShipSymbol: "SHIP-1",
		PlayerID:   shared.MustNewPlayerID(1),
		Good:       "IRON_ORE",
		Quantity:   10,
		TaskID:     "task-1",
		Market:     "X1-TEST-A1",
	}); err != nil {
		t.Fatalf("SellCargo: %v", err)
	}

	if spy.cargoSends != 1 {
		t.Fatalf("expected exactly 1 sell command dispatched, got %d", spy.cargoSends)
	}
	if spy.ledgerRows != 1 {
		t.Fatalf("manufacturing sell double-counted the ledger: got %d rows for one sale, want 1", spy.ledgerRows)
	}
}

// DeliverToFactory is a sell-to-factory; it must likewise write exactly one row.
func TestManufacturingSeller_DeliverToFactory_WritesOneLedgerRowPerDelivery(t *testing.T) {
	spy := &ledgerCountingMediator{}
	seller := &ManufacturingSeller{mediator: spy}

	if _, err := seller.DeliverToFactory(context.Background(), SellParams{
		ShipSymbol: "SHIP-1",
		PlayerID:   shared.MustNewPlayerID(1),
		Good:       "IRON_ORE",
		Quantity:   10,
		TaskID:     "task-1",
		Market:     "X1-FACTORY-A1",
	}); err != nil {
		t.Fatalf("DeliverToFactory: %v", err)
	}

	if spy.cargoSends != 1 {
		t.Fatalf("expected exactly 1 sell command dispatched, got %d", spy.cargoSends)
	}
	if spy.ledgerRows != 1 {
		t.Fatalf("manufacturing delivery double-counted the ledger: got %d rows for one delivery, want 1", spy.ledgerRows)
	}
}

// A manufacturing BUY routes through PurchaseCargoCommand (which self-records the
// authoritative row) AND then calls the ManufacturingLedgerRecorder, producing a
// second row for the same event. The ledger must gain exactly one row per buy.
func TestManufacturingPurchaser_ExecutePurchaseLoop_WritesOneLedgerRowPerBuy(t *testing.T) {
	spy := &ledgerCountingMediator{}
	purchaser := &ManufacturingPurchaser{
		mediator:   spy,
		shipRepo:   &stubShipRepo{ship: newCargoHauler(t, "SHIP-1", 0)}, // empty hold, 80 free
		marketRepo: &stubMarketRepo{mkt: newExportMarket(t, "X1-TEST-A1", "IRON_ORE")},
	}

	if _, err := purchaser.ExecutePurchaseLoop(context.Background(), PurchaseLoopParams{
		ShipSymbol: "SHIP-1",
		PlayerID:   shared.MustNewPlayerID(1),
		Good:       "IRON_ORE",
		TaskID:     "task-1",
		DesiredQty: 5, // one round: buy 5, then remaining hits 0 and the loop breaks
		Market:     "X1-TEST-A1",
		Factory:    "X1-FACTORY-A1",
	}); err != nil {
		t.Fatalf("ExecutePurchaseLoop: %v", err)
	}

	if spy.cargoSends != 1 {
		t.Fatalf("expected exactly 1 purchase command dispatched, got %d", spy.cargoSends)
	}
	if spy.ledgerRows != 1 {
		t.Fatalf("manufacturing buy double-counted the ledger: got %d rows for one buy, want 1", spy.ledgerRows)
	}
}

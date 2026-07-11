package services

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// C1 (sp-64je) — PlannerStockDepositor: deposit harvested output into a
// co-located warehouse at basis, respecting the capital ceiling, and fail SAFE
// (decline -> caller sells) whenever it cannot/should not deposit.

type confirmedDeposit struct {
	ship  string
	good  string
	units int
	basis int
}

type fakeDepositCoord struct {
	shipsByOp map[string][]*storage.StorageShip
	basis     map[string]map[string]int
	available map[string]map[string]int
	confirmed []confirmedDeposit
}

func newFakeDepositCoord() *fakeDepositCoord {
	return &fakeDepositCoord{
		shipsByOp: map[string][]*storage.StorageShip{},
		basis:     map[string]map[string]int{},
		available: map[string]map[string]int{},
	}
}

func (f *fakeDepositCoord) GetStorageShipsForOperation(operationID string) []*storage.StorageShip {
	return f.shipsByOp[operationID]
}
func (f *fakeDepositCoord) GetTotalCargoAvailable(operationID, good string) int {
	return f.available[operationID][good]
}
func (f *fakeDepositCoord) GetCostBasis(operationID, good string) (int, bool) {
	if byGood, ok := f.basis[operationID]; ok {
		if b, ok := byGood[good]; ok {
			return b, true
		}
	}
	return 0, false
}
func (f *fakeDepositCoord) ReserveSpaceForDeposit(operationID string, units int) (*storage.StorageShip, int, bool) {
	for _, ship := range f.shipsByOp[operationID] {
		space := ship.AvailableSpace()
		if space <= 0 {
			continue
		}
		reserve := units
		if reserve > space {
			reserve = space
		}
		if err := ship.ReserveSpace(reserve); err != nil {
			continue
		}
		return ship, reserve, true
	}
	return nil, 0, false
}
func (f *fakeDepositCoord) ReleaseReservedSpace(shipSymbol string, units int) {
	for _, ships := range f.shipsByOp {
		for _, s := range ships {
			if s.ShipSymbol() == shipSymbol {
				s.ReleaseReservedSpace(units)
			}
		}
	}
}
func (f *fakeDepositCoord) ConfirmDepositWithBasis(shipSymbol, good string, units, basis int) {
	f.confirmed = append(f.confirmed, confirmedDeposit{shipSymbol, good, units, basis})
	for _, ships := range f.shipsByOp {
		for _, s := range ships {
			if s.ShipSymbol() == shipSymbol {
				_ = s.ConfirmDeposit(good, units)
			}
		}
	}
}

type fakeOpLister struct {
	ops []*storage.StorageOperation
	err error
}

func (l *fakeOpLister) FindRunning(_ context.Context, _ int) ([]*storage.StorageOperation, error) {
	return l.ops, l.err
}

type fakeDepositMediator struct {
	sent []*gasCmd.TransferCargoCommand
	err  error
}

func (m *fakeDepositMediator) Send(_ context.Context, req common.Request) (common.Response, error) {
	if tc, ok := req.(*gasCmd.TransferCargoCommand); ok {
		m.sent = append(m.sent, tc)
	}
	return nil, m.err
}
func (m *fakeDepositMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *fakeDepositMediator) RegisterMiddleware(common.Middleware)               {}

// depositTestFixture wires a depositor with one co-located warehouse (one storage
// hull with `capacity` free space) at `waypoint` supporting `good`.
func depositTestFixture(t *testing.T, waypoint, good string, capacity, credits, ceilingPct int) (*PlannerStockDepositor, *fakeDepositCoord, *fakeDepositMediator, context.Context) {
	t.Helper()
	ship, err := storage.NewStorageShip("HULL-STORE-1", waypoint, "wh-op-1", capacity, nil)
	if err != nil {
		t.Fatalf("NewStorageShip: %v", err)
	}
	op, err := storage.NewWarehouseOperation("wh-op-1", 1, waypoint, []string{"HULL-STORE-1"}, []string{good}, nil)
	if err != nil {
		t.Fatalf("NewWarehouseOperation: %v", err)
	}

	coord := newFakeDepositCoord()
	coord.shipsByOp["wh-op-1"] = []*storage.StorageShip{ship}
	lister := &fakeOpLister{ops: []*storage.StorageOperation{op}}
	med := &fakeDepositMediator{}
	api := &spendFloorFakeAPIClient{credits: credits}

	dep := NewPlannerStockDepositor(coord, lister, med, api, ceilingPct)
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOK"), &dwellCapturingLogger{})
	return dep, coord, med, ctx
}

// Happy path: a co-located warehouse with space, treasury above the ceiling.
func TestPlannerStockDepositor_DepositsAtBasis(t *testing.T) {
	dep, coord, med, ctx := depositTestFixture(t, "X1-FAC-A1", "CLOTHING", 120, 1_000_000, 15)

	deposited, err := dep.DepositOutput(ctx, 1, "FACTORY-SHIP", "X1-FAC-A1", "CLOTHING", 40, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deposited {
		t.Fatal("expected deposited=true")
	}
	if len(coord.confirmed) != 1 || coord.confirmed[0].units != 40 || coord.confirmed[0].basis != 100 {
		t.Fatalf("expected one basis deposit of 40@100, got %+v", coord.confirmed)
	}
	if len(med.sent) != 1 || med.sent[0].GoodSymbol != "CLOTHING" || med.sent[0].Units != 40 {
		t.Fatalf("expected one transfer of 40 CLOTHING, got %+v", med.sent)
	}
	if med.sent[0].FromShip != "FACTORY-SHIP" || med.sent[0].ToShip != "HULL-STORE-1" {
		t.Fatalf("transfer endpoints wrong: %+v", med.sent[0])
	}
	if med.sent[0].PlayerID != shared.MustNewPlayerID(1) {
		t.Fatalf("transfer PlayerID wrong: %+v", med.sent[0].PlayerID)
	}
}

// No co-located warehouse at the factory waypoint -> decline (caller sells).
func TestPlannerStockDepositor_NoWarehouse_Declines(t *testing.T) {
	dep, coord, _, ctx := depositTestFixture(t, "X1-FAC-A1", "CLOTHING", 120, 1_000_000, 15)

	// Deposit at a DIFFERENT waypoint than the warehouse -> no co-located group.
	deposited, err := dep.DepositOutput(ctx, 1, "FACTORY-SHIP", "X1-OTHER-B2", "CLOTHING", 40, 100)
	if err != nil || deposited {
		t.Fatalf("expected decline (false,nil), got deposited=%v err=%v", deposited, err)
	}
	if len(coord.confirmed) != 0 {
		t.Fatalf("expected no deposit, got %+v", coord.confirmed)
	}
}

// Over the capital ceiling -> decline. credits=100k, pct=15 -> ceiling 15k;
// deposit 40@500 = 20k > 15k.
func TestPlannerStockDepositor_OverCeiling_Declines(t *testing.T) {
	dep, coord, _, ctx := depositTestFixture(t, "X1-FAC-A1", "CLOTHING", 120, 100_000, 15)

	deposited, err := dep.DepositOutput(ctx, 1, "FACTORY-SHIP", "X1-FAC-A1", "CLOTHING", 40, 500)
	if err != nil || deposited {
		t.Fatalf("expected decline over ceiling, got deposited=%v err=%v", deposited, err)
	}
	if len(coord.confirmed) != 0 {
		t.Fatalf("expected no deposit over ceiling, got %+v", coord.confirmed)
	}
}

// A full warehouse (no free space) -> decline.
func TestPlannerStockDepositor_NoSpace_Declines(t *testing.T) {
	dep, coord, _, ctx := depositTestFixture(t, "X1-FAC-A1", "CLOTHING", 0, 1_000_000, 15)

	deposited, err := dep.DepositOutput(ctx, 1, "FACTORY-SHIP", "X1-FAC-A1", "CLOTHING", 40, 100)
	if err != nil || deposited {
		t.Fatalf("expected decline (no space), got deposited=%v err=%v", deposited, err)
	}
	if len(coord.confirmed) != 0 {
		t.Fatalf("expected no deposit, got %+v", coord.confirmed)
	}
}

// Unreadable treasury -> fail closed (decline).
func TestPlannerStockDepositor_UnreadableTreasury_Declines(t *testing.T) {
	dep, coord, _, ctx := depositTestFixture(t, "X1-FAC-A1", "CLOTHING", 120, 1_000_000, 15)
	dep.apiClient = &spendFloorFakeAPIClient{err: errors.New("treasury read failed")}

	deposited, err := dep.DepositOutput(ctx, 1, "FACTORY-SHIP", "X1-FAC-A1", "CLOTHING", 40, 100)
	if err != nil || deposited {
		t.Fatalf("expected decline on unreadable treasury, got deposited=%v err=%v", deposited, err)
	}
	if len(coord.confirmed) != 0 {
		t.Fatalf("expected no deposit, got %+v", coord.confirmed)
	}
}

// A non-positive ceiling percentage disables deposits (fail closed).
func TestPlannerStockDepositor_ZeroCeilingPct_Declines(t *testing.T) {
	dep, coord, _, ctx := depositTestFixture(t, "X1-FAC-A1", "CLOTHING", 120, 1_000_000, 0)

	deposited, err := dep.DepositOutput(ctx, 1, "FACTORY-SHIP", "X1-FAC-A1", "CLOTHING", 40, 100)
	if err != nil || deposited {
		t.Fatalf("expected decline with ceiling pct 0, got deposited=%v err=%v", deposited, err)
	}
	if len(coord.confirmed) != 0 {
		t.Fatalf("expected no deposit, got %+v", coord.confirmed)
	}
}

// A transfer failure with nothing deposited surfaces as an error (the caller can
// then decide; the factory branch logs and sells).
func TestPlannerStockDepositor_TransferFails_ReturnsError(t *testing.T) {
	dep, coord, med, ctx := depositTestFixture(t, "X1-FAC-A1", "CLOTHING", 120, 1_000_000, 15)
	med.err = errors.New("transfer boom")

	deposited, err := dep.DepositOutput(ctx, 1, "FACTORY-SHIP", "X1-FAC-A1", "CLOTHING", 40, 100)
	if err == nil {
		t.Fatal("expected a transfer error")
	}
	if deposited {
		t.Fatal("expected deposited=false on transfer failure with nothing stocked")
	}
	if len(coord.confirmed) != 0 {
		t.Fatalf("expected no confirmed deposit on transfer failure, got %+v", coord.confirmed)
	}
}

// Outstanding factory-stock capital counts toward the ceiling: an existing 140k
// of stock (basis known) plus a new 20k deposit exceeds a 150k ceiling.
func TestPlannerStockDepositor_OutstandingCountsTowardCeiling(t *testing.T) {
	dep, coord, _, ctx := depositTestFixture(t, "X1-FAC-A1", "CLOTHING", 400, 1_000_000, 15)
	// 1,000,000 * 15% = 150,000 ceiling. Pre-existing 1400 units @ 100 = 140,000
	// outstanding; a new 40@500 = 20,000 deposit -> 160,000 > 150,000 -> decline.
	coord.available["wh-op-1"] = map[string]int{"CLOTHING": 1400}
	coord.basis["wh-op-1"] = map[string]int{"CLOTHING": 100}

	deposited, err := dep.DepositOutput(ctx, 1, "FACTORY-SHIP", "X1-FAC-A1", "CLOTHING", 40, 500)
	if err != nil || deposited {
		t.Fatalf("expected decline when outstanding+deposit exceeds ceiling, got deposited=%v err=%v", deposited, err)
	}
}

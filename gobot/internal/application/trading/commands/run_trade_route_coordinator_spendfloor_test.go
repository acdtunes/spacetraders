package commands

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// sfFakeAPIClient is a minimal live-treasury fake for the working-capital spend
// floor. It embeds domainPorts.APIClient (left nil) so only
// GetAgent needs overriding, mirroring the stubAPIClient/wpRefreshFakeAPIClient
// pattern already used by the player-queries and shipyard-batch-purchase tests.
// A non-nil err simulates a live-read failure (GetAgent itself erroring),
// distinct from "no apiClient wired at all" - that nil-port fail-open contract
// is already proven by the package's pre-existing disciplined-circuit test,
// which passes nil for apiClient and completes 3 profitable visits with no
// guard active.
type sfFakeAPIClient struct {
	domainPorts.APIClient
	credits int
	err     error
}

func (c *sfFakeAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &player.AgentData{Credits: c.credits}, nil
}

// newSpendFloorHandler builds a coordinator wired with a REAL (fake) apiClient,
// unlike newTradeHarness (which always passes nil, leaving the guard disabled).
// It reuses the exact same trFixture/trFakeMediator/trFakeMarketRepo lane
// economics as the base disciplined-circuit test, so only the treasury guard's
// behavior differs from TestTradeRouteCoordinator_RunsDisciplinedCircuitUntilMarginDies.
func newSpendFloorHandler(ship *navigation.Ship, apiClient domainPorts.APIClient) (*RunTradeRouteCoordinatorHandler, *trFakeMediator) {
	fixture := &trFixture{}
	mediator := &trFakeMediator{fixture: fixture}
	marketRepo := &trFakeMarketRepo{fixture: fixture}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, nil, nil, apiClient)
	return handler, mediator
}

func TestTradeRouteCoordinator_SpendFloor_AbortsFailClosedWhenTokenMissing(t *testing.T) {
	ship := newTradeHauler(t, "TRADER-SF1")
	// Ample credits - if the guard evaluated them, no breach would occur. The
	// abort must come from the missing token, not the treasury figure.
	apiClient := &sfFakeAPIClient{credits: 1000000}
	handler, mediator := newSpendFloorHandler(ship, apiClient)

	// No auth.WithPlayerToken: PlayerTokenFromContext must fail, and the guard's
	// documented contract is fail-CLOSED on every live-read failure.
	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	if !coord.SpendFloorAbort {
		t.Fatalf("expected SpendFloorAbort on a missing token, got %+v", coord)
	}
	if coord.ExitReason != exitReasonSpendFloor {
		t.Fatalf("expected exit reason %q, got %q", exitReasonSpendFloor, coord.ExitReason)
	}
	if coord.ReserveFloor != defaultWorkingCapitalReserve {
		t.Fatalf("expected reserve floor %d, got %d", defaultWorkingCapitalReserve, coord.ReserveFloor)
	}
	// A blind failure (token unresolved) must never populate a live figure it
	// never actually observed.
	if coord.TreasuryAtAbort != 0 {
		t.Fatalf("expected TreasuryAtAbort 0 on a blind fail-closed abort, got %d", coord.TreasuryAtAbort)
	}
	if coord.Visits != 0 {
		t.Fatalf("expected 0 visits - the guard must trip before Leg 1, got %d", coord.Visits)
	}
	if len(mediator.purchases) != 0 || len(mediator.sells) != 0 {
		t.Fatalf("expected zero trades, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

func TestTradeRouteCoordinator_SpendFloor_AbortsFailClosedWhenLiveReadErrors(t *testing.T) {
	ship := newTradeHauler(t, "TRADER-SF2")
	apiClient := &sfFakeAPIClient{err: errors.New("agent API unavailable")}
	handler, mediator := newSpendFloorHandler(ship, apiClient)

	ctx := auth.WithPlayerToken(context.Background(), "TOKEN-SF2")
	resp, err := handler.Handle(ctx, &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	if !coord.SpendFloorAbort {
		t.Fatalf("expected SpendFloorAbort when GetAgent errors, got %+v", coord)
	}
	if coord.ExitReason != exitReasonSpendFloor {
		t.Fatalf("expected exit reason %q, got %q", exitReasonSpendFloor, coord.ExitReason)
	}
	if coord.ReserveFloor != defaultWorkingCapitalReserve {
		t.Fatalf("expected reserve floor %d, got %d", defaultWorkingCapitalReserve, coord.ReserveFloor)
	}
	if coord.TreasuryAtAbort != 0 {
		t.Fatalf("expected TreasuryAtAbort 0 - the live read failed, so no figure was ever obtained, got %d", coord.TreasuryAtAbort)
	}
	if coord.Visits != 0 {
		t.Fatalf("expected 0 visits, got %d", coord.Visits)
	}
	if len(mediator.purchases) != 0 || len(mediator.sells) != 0 {
		t.Fatalf("expected zero trades, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

// The first visit's projected buy is deterministic from the shared fixture:
// VisitTranche(sourceVolume=60, cargoSpace=40) = 18 units at basis (live source
// ask) 2000/unit = 36000 projected cost. Both cases below are computed against
// this exact number so the breach boundary is exact, not approximate.
func TestTradeRouteCoordinator_SpendFloor_AbortsBeforeBreachingBuy(t *testing.T) {
	tests := []struct {
		name        string
		credits     int
		reserve     int // 0 -> defaultWorkingCapitalReserve
		wantReserve int
	}{
		{
			// 80000 - 36000 = 44000 < 50000 (default reserve): breach.
			name:        "default reserve",
			credits:     80000,
			reserve:     0,
			wantReserve: defaultWorkingCapitalReserve,
		},
		{
			// 130000 - 36000 = 94000 < 100000 (custom reserve): breach. Proves the
			// per-run WorkingCapitalReserve knob (the daemon's working_capital_reserve
			// launch-config key) actually reaches the guard.
			name:        "custom reserve override",
			credits:     130000,
			reserve:     100000,
			wantReserve: 100000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ship := newTradeHauler(t, "TRADER-SF3")
			apiClient := &sfFakeAPIClient{credits: tc.credits}
			handler, mediator := newSpendFloorHandler(ship, apiClient)

			ctx := auth.WithPlayerToken(context.Background(), "TOKEN-SF3")
			resp, err := handler.Handle(ctx, &RunTradeRouteCoordinatorCommand{
				ShipSymbol:            ship.ShipSymbol(),
				SystemSymbol:          trSystem,
				PlayerID:              1,
				WorkingCapitalReserve: tc.reserve,
			})
			if err != nil {
				t.Fatalf("coordinator returned error: %v", err)
			}
			coord := resp.(*RunTradeRouteCoordinatorResponse)

			if !coord.SpendFloorAbort {
				t.Fatalf("expected SpendFloorAbort, got %+v", coord)
			}
			if coord.ExitReason != exitReasonSpendFloor {
				t.Fatalf("expected exit reason %q, got %q", exitReasonSpendFloor, coord.ExitReason)
			}
			if coord.TreasuryAtAbort != tc.credits {
				t.Fatalf("expected TreasuryAtAbort %d (the live figure that revealed the breach), got %d", tc.credits, coord.TreasuryAtAbort)
			}
			if coord.ReserveFloor != tc.wantReserve {
				t.Fatalf("expected ReserveFloor %d, got %d", tc.wantReserve, coord.ReserveFloor)
			}
			if coord.Visits != 0 {
				t.Fatalf("expected 0 visits - the guard must trip before Leg 1 (before any travel/dock/buy), got %d", coord.Visits)
			}
			if len(mediator.purchases) != 0 || len(mediator.sells) != 0 {
				t.Fatalf("expected zero trades, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
			}
		})
	}
}

// A comfortably-clearing treasury must never trip the guard: the circuit should
// trade with the exact same economics as the pre-existing (no-apiClient)
// disciplined-circuit test, proving the guard is not overly aggressive once wired.
func TestTradeRouteCoordinator_SpendFloor_DoesNotAbortWhenTreasuryClearsReserve(t *testing.T) {
	ship := newTradeHauler(t, "TRADER-SF4")
	apiClient := &sfFakeAPIClient{credits: 500000}
	handler, mediator := newSpendFloorHandler(ship, apiClient)

	ctx := auth.WithPlayerToken(context.Background(), "TOKEN-SF4")
	resp, err := handler.Handle(ctx, &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	if coord.SpendFloorAbort {
		t.Fatalf("did not expect SpendFloorAbort with a comfortable treasury, got %+v", coord)
	}
	if coord.TreasuryAtAbort != 0 || coord.ReserveFloor != 0 {
		t.Fatalf("abort fields must stay zero when the guard never trips, got treasury=%d reserve=%d", coord.TreasuryAtAbort, coord.ReserveFloor)
	}
	// Same bid-decay economics as TestTradeRouteCoordinator_RunsDisciplinedCircuitUntilMarginDies:
	// 3 visits x 18u, revenue 189000 - cost 108000 = net 81000.
	if coord.Visits != 3 {
		t.Fatalf("expected 3 visits, got %d", coord.Visits)
	}
	if coord.NetProfit != 81000 {
		t.Fatalf("expected net 81000, got %d", coord.NetProfit)
	}
	if len(mediator.purchases) != 3 || len(mediator.sells) != 3 {
		t.Fatalf("expected 3 buys and 3 sells, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

// sfVariableMediator returns caller-scripted per-visit buy/sell unit economics
// (indexed by call order), so a test can construct a circuit whose REALIZED
// fills lose money even though the underlying trFakeMarketRepo's cached ranked
// spread stays positive throughout: repeated tranches walk the source ask up and
// crush the destination bid in
// reality, while the stale ranked basis the lane was selected on keeps
// reporting a healthy spread. trFakeMediator cannot model this - its buy/sell
// prices are fixed constants that are always profitable by construction.
type sfVariableMediator struct {
	mu               sync.Mutex
	fixture          *trFixture
	buyUnitCosts     []int // buyUnitCosts[N] prices the (N+1)th purchase
	sellUnitRevenues []int // sellUnitRevenues[N] prices the (N+1)th sell
	purchases        []*shipCargo.PurchaseCargoCommand
	sells            []*shipCargo.SellCargoCommand
}

func (m *sfVariableMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.mu.Lock()
		idx := len(m.purchases)
		m.purchases = append(m.purchases, cmd)
		unitCost := m.buyUnitCosts[idx]
		m.mu.Unlock()
		return &shipCargo.PurchaseCargoResponse{
			TotalCost:        cmd.Units * unitCost,
			UnitsAdded:       cmd.Units,
			TransactionCount: 1,
		}, nil
	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		idx := len(m.sells)
		m.sells = append(m.sells, cmd)
		unitRevenue := m.sellUnitRevenues[idx]
		m.mu.Unlock()
		m.fixture.recordSell() // keeps the shared importer-bid decay identical to trFakeMediator
		return &shipCargo.SellCargoResponse{
			TotalRevenue:     cmd.Units * unitRevenue,
			UnitsSold:        cmd.Units,
			TransactionCount: 1,
		}, nil
	default:
		return nil, nil
	}
}

func (m *sfVariableMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *sfVariableMediator) RegisterMiddleware(middleware common.Middleware) {}

// newNegativeMarginHandler isolates fix #2 (negative-margin abort) from fix #1
// (spend floor, covered on its own above): apiClient is nil, so the treasury
// guard never interferes here.
func newNegativeMarginHandler(ship *navigation.Ship, buyUnitCosts, sellUnitRevenues []int) (*RunTradeRouteCoordinatorHandler, *sfVariableMediator) {
	fixture := &trFixture{}
	mediator := &sfVariableMediator{fixture: fixture, buyUnitCosts: buyUnitCosts, sellUnitRevenues: sellUnitRevenues}
	marketRepo := &trFakeMarketRepo{fixture: fixture}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, nil, nil, nil)
	return handler, mediator
}

// Three visits, each losing 18 x (3000-2500) = 9000, must abort with the exact
// cumulative realized loss - not the cached ranked spread, which stays positive
// throughout since it is untouched by the mediator's actual fill prices.
func TestTradeRouteCoordinator_NegativeMargin_AbortsAfterThreeLossyVisits(t *testing.T) {
	ship := newTradeHauler(t, "TRADER-NM1")
	buyUnitCosts := []int{3000, 3000, 3000}
	sellUnitRevenues := []int{2500, 2500, 2500}
	handler, mediator := newNegativeMarginHandler(ship, buyUnitCosts, sellUnitRevenues)

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	if !coord.NegativeMarginAbort {
		t.Fatalf("expected NegativeMarginAbort after 3 lossy visits, got %+v", coord)
	}
	if coord.ExitReason != exitReasonNegativeMargin {
		t.Fatalf("expected exit reason %q, got %q", exitReasonNegativeMargin, coord.ExitReason)
	}
	// -9000/visit x 3 = -27000.
	if coord.RealizedCircuitMargin != -27000 {
		t.Fatalf("expected RealizedCircuitMargin -27000, got %d", coord.RealizedCircuitMargin)
	}
	if coord.Visits != 3 {
		t.Fatalf("expected exactly 3 visits (abort checked immediately after the 3rd), got %d", coord.Visits)
	}
	// The abort must stop the circuit - no 4th visit is attempted.
	if len(mediator.purchases) != 3 || len(mediator.sells) != 3 {
		t.Fatalf("expected exactly 3 buys and 3 sells, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
	if coord.TotalCost != 3*18*3000 || coord.TotalRevenue != 3*18*2500 {
		t.Fatalf("unexpected ledger totals: cost=%d revenue=%d", coord.TotalCost, coord.TotalRevenue)
	}
}

// A deeply lossy FIRST visit, fully offset by two strongly profitable later
// visits, must NOT trip the abort: negativeMarginAbortVisits gates the check on
// visit COUNT (i+1 >= 3), so it never even evaluates until visit 3, by which
// point the cumulative circuit margin has recovered to positive. This proves
// the guard tolerates one noisy early fill rather than reacting to it in
// isolation - exactly the tolerance documented on negativeMarginAbortVisits.
func TestTradeRouteCoordinator_NegativeMargin_ToleratesSingleNoisyVisitThenRecovers(t *testing.T) {
	ship := newTradeHauler(t, "TRADER-NM2")
	// Visit 1: 18 x (1000-5000) = -72000. Visits 2-3: 18 x (5000-1000) = +72000 each.
	// Cumulative at each i+1>=3 checkpoint (only reached at visit 3): +72000 - never negative.
	buyUnitCosts := []int{5000, 1000, 1000}
	sellUnitRevenues := []int{1000, 5000, 5000}
	handler, mediator := newNegativeMarginHandler(ship, buyUnitCosts, sellUnitRevenues)

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	if coord.NegativeMarginAbort {
		t.Fatalf("did not expect NegativeMarginAbort - cumulative margin recovered to positive by visit 3, got %+v", coord)
	}
	if coord.RealizedCircuitMargin != 0 {
		t.Fatalf("RealizedCircuitMargin must stay unset (0) when the abort never trips, got %d", coord.RealizedCircuitMargin)
	}
	// The circuit completes normally: bid-floor decay (4000/3600/3200 alive,
	// 2800 dead) still ends it at exactly 3 visits, same as the base fixture -
	// proving the negative-margin guard did not cut it short.
	if coord.Visits != 3 {
		t.Fatalf("expected 3 visits (ended by bid-floor decay, not the margin guard), got %d", coord.Visits)
	}
	if coord.ExitReason != exitReasonMarginExhausted {
		t.Fatalf("expected exit reason %q (clean margin-death on rescan), got %q", exitReasonMarginExhausted, coord.ExitReason)
	}
	if len(mediator.purchases) != 3 || len(mediator.sells) != 3 {
		t.Fatalf("expected exactly 3 buys and 3 sells, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
	// Revenue 18x(1000+5000+5000)=198000, cost 18x(5000+1000+1000)=126000, net +72000.
	if coord.NetProfit != 72000 {
		t.Fatalf("expected net profit 72000, got %d", coord.NetProfit)
	}
}

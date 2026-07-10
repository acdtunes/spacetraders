package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// buildFulfillVerifyShip builds a docked hauler holding `units` of `good` in a
// `capacity`-hold cargo bay. The delivery-leg loop rebuilds the ship after every
// purchase/deliver, so this is called repeatedly with the new unit count.
func buildFulfillVerifyShip(t *testing.T, symbol, good string, units, capacity int) *navigation.Ship {
	t.Helper()

	waypoint, err := shared.NewWaypoint("X1-TEST-A1", 1, 1)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	var inventory []*shared.CargoItem
	if units > 0 {
		item, err := shared.NewCargoItem(good, good, "ore", units)
		if err != nil {
			t.Fatalf("cargo item: %v", err)
		}
		inventory = append(inventory, item)
	}
	cargo, err := shared.NewCargo(capacity, units, inventory)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, capacity, cargo, 30,
		"FRAME_FRIGATE", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

// statefulShipRepo returns whatever ship the fake mediator has most recently
// rebuilt (after a purchase adds cargo, or a delivery removes it). Both the
// cache read (FindBySymbol) and the authoritative sync (SyncShipFromAPI) return
// the same live ship, which is exactly the invariant the reconcile path relies
// on once the cache is healed.
type statefulShipRepo struct {
	navigation.ShipRepository
	ship *navigation.Ship
}

func (r *statefulShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

func (r *statefulShipRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

// fulfillVerifyMediator models a full contract delivery cycle end-to-end so the
// deliver->fulfill sequencing in executeWorkflow is exercised against real
// cargo/contract state, not a stub that always reports "done". Purchases add
// cargo; deliveries drive the domain DeliverCargo (incrementing UnitsFulfilled)
// and drain the aboard cargo; fulfill drives the domain Fulfill (whose
// CanFulfill guard is the exact "deliveries not complete" source). Every
// deliver and fulfill send is recorded in `calls` so the test can assert order
// AND that fulfill is never sent while the contract is still partial.
type fulfillVerifyMediator struct {
	common.Mediator

	contractRepo *workflowStubContractRepo
	shipRepo     *statefulShipRepo
	good         string
	capacity     int
	perUnit      int // realized purchase price per unit
	projectedAsk int // basis handed to the ladder cap (0 disables it)

	cargoUnits int
	calls      []string

	purchaseCalls int
	deliverCalls  int
	fulfillCalls  int
}

func (m *fulfillVerifyMediator) rebuild(t *testing.T, units int) *navigation.Ship {
	return buildFulfillVerifyShip(t, "TORWIND-7", m.good, units, m.capacity)
}

func (m *fulfillVerifyMediator) send(t *testing.T, ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *contractQueries.EvaluateContractProfitabilityQuery:
		return &contractQueries.ProfitabilityResult{
			IsProfitable:           true,
			PurchaseCost:           1000,
			CheapestMarketWaypoint: "X1-TEST-M1",
			MarketPrices:           map[string]int{m.good: m.projectedAsk},
		}, nil

	case *shipNav.NavigateRouteCommand:
		return &shipNav.NavigateRouteResponse{Status: "completed", Ship: m.shipRepo.ship}, nil

	case *shipTypes.DockShipCommand:
		return nil, nil

	case *shipCargo.PurchaseCargoCommand:
		m.purchaseCalls++
		m.cargoUnits += cmd.Units
		m.shipRepo.ship = m.rebuild(t, m.cargoUnits)
		return &shipCargo.PurchaseCargoResponse{
			TotalCost:        cmd.Units * m.perUnit,
			UnitsAdded:       cmd.Units,
			TransactionCount: 1,
		}, nil

	case *DeliverContractCommand:
		m.deliverCalls++
		m.calls = append(m.calls, "deliver")
		c, err := m.contractRepo.FindByID(ctx, cmd.ContractID)
		if err != nil {
			return nil, err
		}
		if err := c.DeliverCargo(cmd.TradeSymbol, cmd.Units); err != nil {
			return nil, fmt.Errorf("API error: %w", err)
		}
		if err := m.contractRepo.Add(ctx, c); err != nil {
			return nil, err
		}
		m.cargoUnits -= cmd.Units
		if m.cargoUnits < 0 {
			m.cargoUnits = 0
		}
		m.shipRepo.ship = m.rebuild(t, m.cargoUnits)
		return &DeliverContractResponse{Contract: c, UnitsDelivered: cmd.Units}, nil

	case *FulfillContractCommand:
		m.fulfillCalls++
		m.calls = append(m.calls, "fulfill")
		c, err := m.contractRepo.FindByID(ctx, cmd.ContractID)
		if err != nil {
			return nil, err
		}
		// Drive the real domain guard: a fulfill sent on a partial contract
		// returns "deliveries not complete" here, exactly as production does.
		if err := c.Fulfill(); err != nil {
			return nil, fmt.Errorf("failed to fulfill contract: %w", err)
		}
		return &FulfillContractResponse{Contract: c}, nil

	case *NegotiateContractCommand:
		// The best-effort next-contract claim after a successful fulfill: a
		// benign error is swallowed by negotiateNextContractBestEffort, keeping
		// these tests focused on the fulfill decision.
		return nil, fmt.Errorf("no contract available in test")

	default:
		return nil, fmt.Errorf("unexpected mediator command in test: %T", request)
	}
}

// fulfillVerifyMediatorAdapter binds *testing.T into the mediator's Send so the
// ship rebuilds (which need t for constructor error handling) stay in the fake.
type fulfillVerifyMediatorAdapter struct {
	*fulfillVerifyMediator
	t *testing.T
}

func (a *fulfillVerifyMediatorAdapter) Send(ctx context.Context, request common.Request) (common.Response, error) {
	return a.send(a.t, ctx, request)
}

func newFulfillVerifyHarness(t *testing.T, seed *contract.Contract, good string, capacity, perUnit, projectedAsk, startCargo int) (*fulfillVerifyMediatorAdapter, *RunWorkflowHandler) {
	t.Helper()
	contractRepo := newWorkflowStubContractRepo(seed)
	shipRepo := &statefulShipRepo{}
	med := &fulfillVerifyMediator{
		contractRepo: contractRepo,
		shipRepo:     shipRepo,
		good:         good,
		capacity:     capacity,
		perUnit:      perUnit,
		projectedAsk: projectedAsk,
		cargoUnits:   startCargo,
	}
	shipRepo.ship = med.rebuild(t, startCargo)
	adapter := &fulfillVerifyMediatorAdapter{fulfillVerifyMediator: med, t: t}
	handler := NewRunWorkflowHandler(adapter, shipRepo, contractRepo, nil)
	return adapter, handler
}

func fulfillVerifyContract(t *testing.T, id, good string, required, delivered int) *contract.Contract {
	t.Helper()
	terms := contract.Terms{
		Payment: contract.Payment{OnAccepted: 5000, OnFulfilled: 20000},
		Deliveries: []contract.Delivery{
			{TradeSymbol: good, DestinationSymbol: "X1-TEST-A1", UnitsRequired: required, UnitsFulfilled: delivered},
		},
		DeadlineToAccept: "2026-01-01T00:00:00Z",
		Deadline:         "2027-01-01T00:00:00Z",
	}
	c, err := contract.NewContract(id, shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT", terms, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	if err := c.Accept(); err != nil {
		t.Fatalf("seed Accept: %v", err)
	}
	return c
}

func runFulfillVerify(t *testing.T, handler *RunWorkflowHandler) (*RunWorkflowResponse, error) {
	t.Helper()
	logger := &verifyLogger{}
	ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "test-token"), logger)
	resp, err := handler.Handle(ctx, &RunWorkflowCommand{ShipSymbol: "TORWIND-7", PlayerID: shared.MustNewPlayerID(1)})
	if err != nil {
		return nil, err
	}
	result, ok := resp.(*RunWorkflowResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	return result, nil
}

type verifyLogger struct{}

func (verifyLogger) Log(_, _ string, _ map[string]interface{}) {}

// A contract needing two cargo-loads (80 units, 40-hold) must NOT fulfill after
// the first partial delivery: the delivery leg loops the sourcing path for the
// remainder and fulfill fires exactly once, only after every unit has
// registered. This is the sp-2ei3 livelock: pre-fix, executeWorkflow fulfilled
// straight after ProcessAllDeliveries returned a 40/80 contract and crashed on
// "deliveries not complete".
func TestRunWorkflow_MultiLoadContract_LoopsSourcingThenFulfillsOnce(t *testing.T) {
	seed := fulfillVerifyContract(t, "contract-multiload", "IRON_ORE", 80, 0)
	_, handler := newFulfillVerifyHarness(t, seed, "IRON_ORE", 40, 100, 100, 0)

	result, err := runFulfillVerify(t, handler)
	if err != nil {
		t.Fatalf("multi-load contract must complete without crashing, got: %v", err)
	}
	if !result.Fulfilled {
		t.Fatalf("expected contract to be fulfilled after the delivery leg sourced both loads, got %+v", result)
	}
}

func TestRunWorkflow_MultiLoadContract_DeliversTwiceFulfillsAfter(t *testing.T) {
	seed := fulfillVerifyContract(t, "contract-multiload", "IRON_ORE", 80, 0)
	adapter, handler := newFulfillVerifyHarness(t, seed, "IRON_ORE", 40, 100, 100, 0)

	if _, err := runFulfillVerify(t, handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if adapter.deliverCalls != 2 {
		t.Fatalf("expected 2 deliveries for an 80-unit contract in a 40-hold ship, got %d", adapter.deliverCalls)
	}
	if adapter.fulfillCalls != 1 {
		t.Fatalf("expected exactly one fulfill, got %d", adapter.fulfillCalls)
	}
	// Fulfill must be the LAST call, and both deliveries must precede it.
	if got := adapter.calls; len(got) != 3 || got[0] != "deliver" || got[1] != "deliver" || got[2] != "fulfill" {
		t.Fatalf("expected call order [deliver deliver fulfill], got %v", got)
	}
}

// When sourcing halts mid-contract (the ladder cap stops buying a runaway ask),
// the delivery leg delivers what is aboard and the worker PARKS: no fulfill on
// the partial state, a clean nil-error exit, and Fulfilled=false. The remainder
// re-projects through the coordinator's defer gate. Never a skip (RULING #1).
func TestRunWorkflow_PartialAfterLadderHalt_ParksWithoutFulfilling(t *testing.T) {
	seed := fulfillVerifyContract(t, "contract-ladder", "IRON_ORE", 80, 0)
	// perUnit 200 vs projected 100 breaches the 1.5x ladder cap on the first trip.
	adapter, handler := newFulfillVerifyHarness(t, seed, "IRON_ORE", 40, 200, 100, 0)

	result, err := runFulfillVerify(t, handler)
	if err != nil {
		t.Fatalf("a ladder-halted partial must park cleanly (nil error), got: %v", err)
	}
	if result.Fulfilled {
		t.Fatalf("expected NOT fulfilled on a partial contract, got Fulfilled=true")
	}
	if adapter.deliverCalls != 1 {
		t.Fatalf("expected the aboard load to be delivered once, got %d deliveries", adapter.deliverCalls)
	}
	if adapter.fulfillCalls != 0 {
		t.Fatalf("fulfill must NOT be sent on a partial contract, got %d fulfill calls", adapter.fulfillCalls)
	}
}

// The livelock case, test-locked: a crash-respawn resumes a contract already
// partially delivered (40/120 persisted) and must re-read that state, source
// ONLY the remaining 80 units (two more loads), deliver them, and fulfill.
// Pre-fix it delivered one 40-unit load (to 80/120) and crashed on fulfill.
func TestRunWorkflow_CrashRespawn_ResumesPartialAndSourcesRemainder(t *testing.T) {
	seed := fulfillVerifyContract(t, "contract-resume", "IRON_ORE", 120, 40) // 40 already delivered
	adapter, handler := newFulfillVerifyHarness(t, seed, "IRON_ORE", 40, 100, 100, 0)

	result, err := runFulfillVerify(t, handler)
	if err != nil {
		t.Fatalf("resume must source the remainder and fulfill, got: %v", err)
	}
	if !result.Fulfilled {
		t.Fatalf("expected the resumed contract to be fulfilled, got %+v", result)
	}
	// Remaining 80 units in a 40-hold ship = exactly two more deliveries.
	if adapter.deliverCalls != 2 {
		t.Fatalf("expected 2 deliveries to source the remaining 80 units, got %d", adapter.deliverCalls)
	}
	if adapter.purchaseCalls != 2 {
		t.Fatalf("expected 2 purchases for the remaining 80 units, got %d", adapter.purchaseCalls)
	}
	if adapter.fulfillCalls != 1 {
		t.Fatalf("expected exactly one fulfill after the remainder registered, got %d", adapter.fulfillCalls)
	}
}

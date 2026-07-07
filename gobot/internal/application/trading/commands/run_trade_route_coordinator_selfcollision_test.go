package commands

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-2sam ROOT CAUSE reproduction — the ship-assignment SELF-COLLISION.
//
// Live daemon.log: "[navigate-TORWIND-8-c4b5509c] ERROR: failed to assign ship
// TORWIND-8: ship TORWIND-8 is already assigned to container trade-route-…". The
// coordinator claims the hull into its trade-route container, then its navigate leg
// went through the daemon's NavigateShip RPC, which spawns a CHILD navigate container
// that RE-CLAIMS the same hull. The domain refuses the double-claim (Ship.
// AssignToContainer errors when already assigned), the navigate leg fails, and the
// circuit exits at the first navigate with zero visits — regardless of hull position
// or --max-visits. The fix routes each leg through NavigateDirect (assigns no
// container), so there is nothing to collide with.
//
// This fake reproduces the collision at the coordinator boundary: its navigate branch
// spawns a re-claiming navigate container exactly as the daemon RPC did. With
// reclaim=true the run flies zero (RED, the captain's symptom); with reclaim=false
// (legs routed under the parent claim, no re-assignment) it flies (GREEN).

type collisionMediator struct {
	mu          sync.Mutex
	ship        *navigation.Ship
	clock       shared.Clock
	fixture     *zvFixture
	reclaim     bool // true: navigate spawns a re-claiming child container (the bug)
	navAttempts int
	purchases   int
	sells       int
}

func (m *collisionMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *navCmd.NavigateRouteCommand:
		m.mu.Lock()
		m.navAttempts++
		m.mu.Unlock()
		if m.reclaim {
			// Model the daemon's NavigateShip RPC: it creates a navigate container and
			// assigns the ship to it. The hull is already in the trade-route container,
			// so this is the exact double-claim the live run hit.
			navContainerID := fmt.Sprintf("navigate-%s-c4b5509c", cmd.ShipSymbol)
			if err := m.ship.AssignToContainer(navContainerID, m.clock); err != nil {
				return nil, fmt.Errorf("failed to assign ship %s: %w", cmd.ShipSymbol, err)
			}
		}
		return &navCmd.NavigateRouteResponse{Status: "completed", CurrentLocation: cmd.Destination}, nil
	case *shipCargo.PurchaseCargoCommand:
		m.mu.Lock()
		m.purchases++
		m.mu.Unlock()
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * zvSrcAsk, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		m.sells++
		m.mu.Unlock()
		bid := m.fixture.destBid()
		m.fixture.recordSell(cmd.Units)
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * bid, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil // dock etc. succeed silently
	}
}

func (m *collisionMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *collisionMediator) RegisterMiddleware(middleware common.Middleware) {}

func newCollisionHarness(t *testing.T, ship *navigation.Ship, reclaim bool) (*RunTradeRouteCoordinatorHandler, *collisionMediator) {
	t.Helper()
	clock := &trFakeClock{}
	fixture := &zvFixture{capacity: ship.CargoCapacity(), onboard: 0}
	mediator := &collisionMediator{ship: ship, clock: clock, fixture: fixture, reclaim: reclaim}
	marketRepo := &zvMarketRepo{fixture: fixture}
	containerRepo := newTrFakeContainerRepo()
	shipRepo := &trFakeShipRepo{ship: ship, containers: containerRepo}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, containerRepo, clock, nil)
	return handler, mediator
}

// RED: when navigating the claimed hull re-claims it into a child navigate container,
// the very first navigate leg fails with the daemon's exact assignment error and the
// circuit flies zero visits — the captain's live symptom. The hull is docked at a
// NEUTRAL waypoint (not the source), so the first leg is a real move: this proves the
// failure is position-independent (it is the re-claim, not the hop).
func TestTradeRouteCoordinator_NavigateReclaimsClaimedHull_FliesZero(t *testing.T) {
	ship := newDiscHauler(t, "TORWIND-8", "X1-ZV-DOCK") // idle, empty, AWAY from the source
	handler, mediator := newCollisionHarness(t, ship, true)

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: zvSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("a navigate self-collision must be a clean exit, not a handler error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	// Selection succeeded (RIFLES clears the floor) — the failure is at execution.
	if coord.Good != zvGood {
		t.Fatalf("expected the ranked lane %q selected, got %q", zvGood, coord.Good)
	}
	if coord.Visits != 0 || coord.UnitsTraded != 0 {
		t.Fatalf("expected the self-collision to fly zero, got %d visits / %d units", coord.Visits, coord.UnitsTraded)
	}
	if mediator.purchases != 0 {
		t.Fatalf("the circuit must not reach the buy leg — navigate collided first, got %d purchases", mediator.purchases)
	}
	// The self-diagnosing reason must name the failed navigate leg AND carry the exact
	// daemon assignment collision, so this can never again need a live re-run to explain.
	if coord.AbortReason == "" {
		t.Fatal("a selected lane that flew zero MUST report AbortReason (sp-2sam)")
	}
	if !strings.Contains(coord.AbortReason, "navigation to source") ||
		!strings.Contains(coord.AbortReason, "already assigned to container") {
		t.Fatalf("AbortReason must surface the navigate self-collision, got %q", coord.AbortReason)
	}
	if !ship.IsIdle() {
		t.Fatalf("the hull must be released after the aborted run, still on %q", ship.ContainerID())
	}
}

// GREEN: routing the legs under the parent claim (navigate assigns NO child
// container, as NavigateDirect does) removes the collision, and the same coordinator
// on the same hull flies its profitable visits.
func TestTradeRouteCoordinator_NavigateUnderParentClaim_Flies(t *testing.T) {
	ship := newDiscHauler(t, "TORWIND-8", "X1-ZV-DOCK")
	handler, mediator := newCollisionHarness(t, ship, false)

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: zvSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)
	if coord.AbortReason != "" {
		t.Fatalf("navigating under the parent claim must not abort, got reason %q", coord.AbortReason)
	}
	if coord.Visits < 1 || mediator.purchases < 1 {
		t.Fatalf("routing under the parent claim must let the circuit fly, got %d visits / %d purchases", coord.Visits, mediator.purchases)
	}
	if coord.NetProfit <= 0 {
		t.Fatalf("expected a net-positive circuit, got net %d", coord.NetProfit)
	}
	if !ship.IsIdle() {
		t.Fatalf("expected the ship released to idle, still on %q", ship.ContainerID())
	}
}

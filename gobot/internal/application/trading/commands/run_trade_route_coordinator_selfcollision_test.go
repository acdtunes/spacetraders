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
)

// sp-2sam lasting guard — a failed navigate leg must be SELF-DIAGNOSING.
//
// The original bug: the CLI runner claimed the hull into its trade-route container,
// then its navigate leg went through the daemon's NavigateShip RPC, which spawned a
// CHILD navigate container that RE-CLAIMED the same hull; the domain refused the
// double-claim and the circuit flew zero, with only a bare "Visits: 0" to explain it.
// sp-zewt eliminates that self-collision class entirely: as a daemon container the
// circuit's NavigateRouteCommand resolves to the RouteExecutor-backed handler, which
// moves the already-claimed hull in-process (orbit → NavigateDirect → arrival events)
// and never spawns a re-claiming child. So the collision can no longer happen.
//
// What still matters — and what these tests pin — is the self-diagnosing abort: if any
// navigate leg fails for ANY reason, the selected lane must surface AbortReason naming
// the failed leg (never a silent zero-visit run), and when navigation succeeds the same
// hull flies its profitable visits. The mediator's failNav flag models a navigate leg
// that errors (a RouteExecutor leg failure, an API 4236, etc.).

type collisionMediator struct {
	mu          sync.Mutex
	fixture     *zvFixture
	failNav     bool // true: the navigate leg returns an error (models any nav failure)
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
		if m.failNav {
			// Model a navigate leg that fails (in the container world this is a
			// RouteExecutor leg error, not the old re-claim). The circuit must abort
			// cleanly and surface WHY, not return a handler error.
			return nil, fmt.Errorf("navigate leg to %s failed: simulated route failure", cmd.Destination)
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

func newCollisionHarness(t *testing.T, ship *navigation.Ship, failNav bool) (*RunTradeRouteCoordinatorHandler, *collisionMediator) {
	t.Helper()
	fixture := &zvFixture{capacity: ship.CargoCapacity(), onboard: 0}
	mediator := &collisionMediator{fixture: fixture, failNav: failNav}
	marketRepo := &zvMarketRepo{fixture: fixture}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, nil)
	return handler, mediator
}

// RED value (sp-2sam): a selected lane whose first navigate leg fails must fly zero
// visits AND report AbortReason naming the failed navigate — never a silent zero. The
// hull is docked AWAY from the source, so the first leg is a real move, proving the
// abort is surfaced regardless of position.
func TestTradeRouteCoordinator_NavigateLegFails_FliesZeroWithAbortReason(t *testing.T) {
	ship := newDiscHauler(t, "TORWIND-8", "X1-ZV-DOCK") // idle, empty, AWAY from the source
	handler, mediator := newCollisionHarness(t, ship, true)

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: zvSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("a failed navigate leg must be a clean exit, not a handler error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	// Selection succeeded (RIFLES clears the floor) — the failure is at execution.
	if coord.Good != zvGood {
		t.Fatalf("expected the ranked lane %q selected, got %q", zvGood, coord.Good)
	}
	if coord.Visits != 0 || coord.UnitsTraded != 0 {
		t.Fatalf("expected the failed nav to fly zero, got %d visits / %d units", coord.Visits, coord.UnitsTraded)
	}
	if mediator.purchases != 0 {
		t.Fatalf("the circuit must not reach the buy leg — navigate failed first, got %d purchases", mediator.purchases)
	}
	// The self-diagnosing reason must name the failed navigate leg, so a zero-visit run
	// can never again need a live re-run to explain.
	if coord.AbortReason == "" {
		t.Fatal("a selected lane that flew zero MUST report AbortReason (sp-2sam)")
	}
	if !strings.Contains(coord.AbortReason, "navigation to source") {
		t.Fatalf("AbortReason must name the failed navigate leg, got %q", coord.AbortReason)
	}
}

// GREEN: when navigation succeeds, the same coordinator on the same hull flies its
// profitable visits. (In the daemon this is RouteExecutor moving the already-claimed
// hull with no re-claiming child container — the sp-2sam collision cannot recur.)
func TestTradeRouteCoordinator_CircuitFliesWhenNavSucceeds(t *testing.T) {
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
		t.Fatalf("a successful circuit must not abort, got reason %q", coord.AbortReason)
	}
	if coord.Visits < 1 || mediator.purchases < 1 {
		t.Fatalf("a successful nav must let the circuit fly, got %d visits / %d purchases", coord.Visits, mediator.purchases)
	}
	if coord.NetProfit <= 0 {
		t.Fatalf("expected a net-positive circuit, got net %d", coord.NetProfit)
	}
}

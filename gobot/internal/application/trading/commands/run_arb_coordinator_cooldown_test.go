package commands

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- sp-yjrs: arb-run waits out jump cooldowns between hops (the wlev pattern) ---
//
// The incident: an arb-run bought 60u, jumped hop 1 (KA42→GQ92) fine, then the NEXT
// hop hit the ship's own 352s jump cooldown; the API client retried the 409 at its
// 2s backoff (attempt 1,2,3...), exhausted retries, and the run FAILED LADEN mid-path.
// trade-route's circuit executor waits the cooldown out ("Waiting out jump cooldown
// before continuing"); the fix is that arb-run does the SAME — and it does so by
// delegating its cross-system movement to the exact shared travel() the circuit uses
// (h.legs.travel), so the sp-wlev per-hop cooldown wait is inherited, not re-derived.
//
// These tests lock that inheritance at the ARB level: the trade-route travel_test
// already proves travel() waits between hops, but nothing proved the ARB coordinator
// actually routes cross-system movement through it. A regression that gave arb its own
// jump loop (the copy-paste the DRY judgment call warns against) would strand a laden
// hull exactly as the incident did, and every existing arb test — all same-system —
// would stay green. This is the guard that would go red.

const (
	arbCdSource   = "X1-KA42-EXPORT" // buy here (system X1-KA42) — hull starts docked
	arbCdSrcGate  = "X1-KA42-GATE"   // the source system's jump gate (departure hop)
	arbCdDest     = "X1-DP51-MARKET" // sell here (system X1-DP51) — two jumps away
	arbCdMidSys   = "X1-GQ92"        // the intermediate system (hop 1 lands here)
	arbCdDestSys  = "X1-DP51"        // the destination system (hop 2 lands here)
	arbCdCooldown = 352              // the incident's jump cooldown, in seconds
)

// arbCooldownMarketRepo serves the two-market CROSS-SYSTEM lane the incident flew: an
// exporter in one system (ask 2000) and an importer two jumps away (bid 4000). The
// stock trFakeMarketRepo can't stand in — both its markets sit in one system, so it
// never exercises a jump at all.
type arbCooldownMarketRepo struct {
	market.MarketRepository
}

func (r *arbCooldownMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "STRONG"
	switch waypointSymbol {
	case arbCdSource:
		// Exporter: SellPrice (the ask we pay) is trSourceAsk (2000).
		good, err := market.NewTradeGood(trGood, &supply, &activity, 1900, trSourceAsk, 60, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	case arbCdDest:
		// Importer: PurchasePrice (the bid we receive) is trStartDestBid (4000).
		good, err := market.NewTradeGood(trGood, &supply, &activity, trStartDestBid, 4100, 30, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	}
	return nil, fmt.Errorf("no market fixture for %s", waypointSymbol)
}

// arbCooldownMediator drives a full cross-system arb run: buy, the departure-hop gate
// lookup + navigate, each cross-system JUMP (reporting a cooldown), the arrival-hop
// navigate, and the sell. It records the jumps/navigates and — crucially — MUTATES the
// shared hull's location to the destination system's gate on each jump, exactly as the
// real jump handler persists the post-jump nav state, so travel()'s post-loop reload
// sees the hull in the destination system and flies the arrival hop. jumpErrOnHop
// injects a NON-cooldown jump failure on a chosen hop (1-based) to prove a real travel
// error still fails the run (and never reaches the cooldown wait).
type arbCooldownMediator struct {
	ship            *navigation.Ship
	gateResp        *shipQueries.FindNearestJumpGateResponse
	cooldownSeconds int
	jumpErrOnHop    int
	jumpErr         error

	jumps       []*navCmd.JumpShipCommand
	navigates   []*navCmd.NavigateRouteCommand
	gateQueries []*shipQueries.FindNearestJumpGateQuery
	purchases   []*shipCargo.PurchaseCargoCommand
	sells       []*shipCargo.SellCargoCommand
}

func (m *arbCooldownMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.purchases = append(m.purchases, cmd)
		return &shipCargo.PurchaseCargoResponse{
			TotalCost:        cmd.Units * trSourceAsk,
			UnitsAdded:       cmd.Units,
			TransactionCount: 1,
		}, nil
	case *shipCargo.SellCargoCommand:
		m.sells = append(m.sells, cmd)
		return &shipCargo.SellCargoResponse{
			TotalRevenue:     cmd.Units * trSellRevenue,
			UnitsSold:        cmd.Units,
			TransactionCount: 1,
		}, nil
	case *shipQueries.FindNearestJumpGateQuery:
		m.gateQueries = append(m.gateQueries, cmd)
		return m.gateResp, nil
	case *navCmd.NavigateRouteCommand:
		m.navigates = append(m.navigates, cmd)
		return nil, nil
	case *navCmd.JumpShipCommand:
		m.jumps = append(m.jumps, cmd)
		if m.jumpErr != nil && len(m.jumps) == m.jumpErrOnHop {
			return nil, m.jumpErr
		}
		// Mirror the real jump handler: persist the hull onto the destination
		// system's gate so the post-loop reload sees the new system.
		if wp, err := shared.NewWaypoint(cmd.DestinationSystem+"-GATE", 0, 0); err == nil {
			wp.Type = "JUMP_GATE"
			m.ship.SetLocation(wp)
		}
		return &navCmd.JumpShipResponse{
			Success:           true,
			DestinationSystem: cmd.DestinationSystem,
			CooldownSeconds:   m.cooldownSeconds,
		}, nil
	default:
		return nil, nil // dock, etc. succeed silently
	}
}

func (m *arbCooldownMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *arbCooldownMediator) RegisterMiddleware(common.Middleware)               {}

// THE acceptance criterion (sp-yjrs): a two-hop cross-system arb-run WAITS the jump
// cooldown between hops and completes — no cooldown-409 retry storm, no laden strand.
// The proof the wait fired is the injected clock: one cooldown-budget sleep per jump.
// If arb ever stopped delegating to the cooldown-aware shared travel(), the clock would
// record zero sleeps and the second real jump would 409-retry into the incident.
func TestArbCoordinator_CrossSystemMultiHop_WaitsCooldownBetweenHops_CompletesNoStrand(t *testing.T) {
	ship := newTravelShipAt(t, "ARB-YJRS", arbCdSource) // docked-market origin, empty 40u hold
	mediator := &arbCooldownMediator{
		ship:            ship,
		gateResp:        gateResponseAt(t, arbCdSrcGate),
		cooldownSeconds: arbCdCooldown,
	}
	clock := &travelFakeClock{}
	handler := NewRunArbCoordinatorHandler(mediator, &travelShipRepo{ship: ship}, &arbCooldownMarketRepo{}, nil, clock, nil)
	// Two jumps: KA42→GQ92→DP51 — the shape of the incident (hop 1 fine, hop 2 hit the
	// cooldown). The gate graph also makes the pre-buy routability guard pass.
	handler.SetGateGraph(&fakeGateGraph{path: []string{"X1-KA42", arbCdMidSys, arbCdDestSys}})

	resp, err := handler.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      arbCdSource,
		SellAt:     arbCdDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a cooldown between hops must be WAITED, not failed: got error %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Completed || arb.Aborted {
		t.Fatalf("expected a completed, non-aborted cross-system run, got %+v", arb)
	}

	// The core guard: a cooldown-budget sleep fired for EVERY jump (the wait between
	// hops is precisely what lets the next jump proceed instead of 409-retrying).
	wantBudget := calculateCooldownWaitBudget(arbCdCooldown*time.Second, DefaultCooldownMarginFactor, DefaultCooldownMinMargin)
	if len(clock.slept) != 2 {
		t.Fatalf("expected two cooldown waits (one per jump) — the arb path must inherit the sp-wlev wait, got %d: %v", len(clock.slept), clock.slept)
	}
	for i, slept := range clock.slept {
		if slept != wantBudget {
			t.Fatalf("cooldown wait %d: expected the ETA-scaled budget %v, got %v", i+1, wantBudget, slept)
		}
	}

	// Two jumps in path order, each opting out of its own claim (the container runner
	// holds it). Exactly two — no 409 retry re-firing a jump.
	if len(mediator.jumps) != 2 {
		t.Fatalf("expected exactly two JumpShipCommands (KA42→GQ92→DP51), got %d", len(mediator.jumps))
	}
	wantDest := []string{arbCdMidSys, arbCdDestSys}
	for i, jump := range mediator.jumps {
		if jump.DestinationSystem != wantDest[i] {
			t.Fatalf("hop %d: expected jump to %s, got %s", i+1, wantDest[i], jump.DestinationSystem)
		}
		if !jump.SkipClaim {
			t.Fatalf("hop %d: expected SkipClaim=true (the coordinator already holds the claim)", i+1)
		}
	}

	// One-shot economics: the full 40u hold bought at 2000 and sold at 3500, delivered
	// in full with nothing stranded.
	if arb.UnitsTraded != 40 || arb.TotalCost != 80000 || arb.TotalRevenue != 140000 || arb.NetProfit != 60000 {
		t.Fatalf("unexpected economics: units=%d cost=%d revenue=%d net=%d", arb.UnitsTraded, arb.TotalCost, arb.TotalRevenue, arb.NetProfit)
	}
	if len(mediator.purchases) != 1 || len(mediator.sells) != 1 {
		t.Fatalf("expected exactly one buy and one sell (one-shot), got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}

	// The travel legs the wait sits between: one departure hop (source waypoint→gate)
	// and one arrival hop (destination gate→waypoint).
	if len(mediator.navigates) != 2 {
		t.Fatalf("expected two navigates (departure + arrival), got %d", len(mediator.navigates))
	}
	if mediator.navigates[0].Destination != arbCdSrcGate {
		t.Fatalf("departure hop must target the source gate %s, got %q", arbCdSrcGate, mediator.navigates[0].Destination)
	}
	if mediator.navigates[1].Destination != arbCdDest {
		t.Fatalf("arrival hop must target the destination waypoint %s, got %q", arbCdDest, mediator.navigates[1].Destination)
	}
}

// Regression guard (sp-yjrs, the money-safe half): teaching the travel path that a
// cooldown is a WAIT must NOT swallow a genuine, NON-cooldown jump failure. A real
// jump error still fails the run — it never reaches the cooldown wait — and the tranche
// bought this attempt stays aboard for the runner's retry-safe resume (never re-bought,
// never falsely reported complete). This preserves the arb guards the fix must not relax.
func TestArbCoordinator_CrossSystemJumpError_FailsWithoutWaiting_CargoStaysAboard(t *testing.T) {
	ship := newTravelShipAt(t, "ARB-YJRS-ERR", arbCdSource)
	mediator := &arbCooldownMediator{
		ship:            ship,
		gateResp:        gateResponseAt(t, arbCdSrcGate),
		cooldownSeconds: arbCdCooldown,
		jumpErrOnHop:    1, // the FIRST hop fails with a non-cooldown error
		jumpErr:         errors.New("jump rejected: destination gate unreachable"),
	}
	clock := &travelFakeClock{}
	handler := NewRunArbCoordinatorHandler(mediator, &travelShipRepo{ship: ship}, &arbCooldownMarketRepo{}, nil, clock, nil)
	handler.SetGateGraph(&fakeGateGraph{path: []string{"X1-KA42", arbCdMidSys, arbCdDestSys}})

	resp, err := handler.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      arbCdSource,
		SellAt:     arbCdDest,
		PlayerID:   1,
	})
	if err == nil {
		t.Fatal("a non-cooldown jump failure must fail the run, not be swallowed as a wait")
	}
	arb := arbResponse(t, resp)

	if arb.Completed {
		t.Fatalf("a failed travel leg must not report Completed, got %+v", arb)
	}
	if len(clock.slept) != 0 {
		t.Fatalf("a failed jump must never reach the cooldown wait, got %d sleeps: %v", len(clock.slept), clock.slept)
	}
	// The buy already committed this attempt; the tranche stays aboard for the runner's
	// retry-safe resume (sp-5nqx) — it is neither re-bought nor sold here.
	if len(mediator.purchases) != 1 {
		t.Fatalf("expected the single pre-travel buy to have committed, got %d purchases", len(mediator.purchases))
	}
	if len(mediator.sells) != 0 {
		t.Fatalf("a run that failed mid-travel must not sell, got %d sells", len(mediator.sells))
	}
}

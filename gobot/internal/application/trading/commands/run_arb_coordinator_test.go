package commands

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newArbHandler wires the one-shot arb coordinator onto the SAME lane economics the
// trade-route tests use (trFakeMarketRepo: source ask 2000, dest bid 4000 before any
// fill; trFakeMediator: buy at 2000/unit, sell at 3500/unit). apiClient is caller-
// supplied so the spend-floor cases can inject a live-treasury fake; nil leaves the
// guard disabled, exactly as the base happy-path/caps/margin cases want it.
func newArbHandler(ship *navigation.Ship, apiClient domainPorts.APIClient) (*RunArbCoordinatorHandler, *trFakeMediator) {
	fixture := &trFixture{}
	mediator := &trFakeMediator{fixture: fixture}
	marketRepo := &trFakeMarketRepo{fixture: fixture}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunArbCoordinatorHandler(mediator, shipRepo, marketRepo, nil, nil, apiClient)
	return handler, mediator
}

func arbResponse(t *testing.T, resp interface{}) *RunArbCoordinatorResponse {
	t.Helper()
	arb, ok := resp.(*RunArbCoordinatorResponse)
	if !ok || arb == nil {
		t.Fatalf("unexpected response %T", resp)
	}
	return arb
}

// The core acceptance: ONE command buys at the source, travels to the destination,
// sells ONCE, and stops — no loop, no second visit. The hull's full 40u hold is
// bought at 2000 and sold at 3500 for a clean +60000.
func TestArbCoordinator_HappyOneShot_BuysTravelsSellsStops(t *testing.T) {
	ship := newTradeHauler(t, "ARB-1")
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("arb returned error: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Completed || arb.Aborted {
		t.Fatalf("expected a completed, non-aborted run, got %+v", arb)
	}
	if arb.Good != trGood || arb.SourceWaypoint != trSource || arb.DestWaypoint != trDest {
		t.Fatalf("wrong lane: good=%q source=%q dest=%q", arb.Good, arb.SourceWaypoint, arb.DestWaypoint)
	}
	// Full 40u hold, bought at 2000 and sold at 3500.
	if arb.UnitsTraded != 40 {
		t.Fatalf("expected 40 units traded (full hold), got %d", arb.UnitsTraded)
	}
	if arb.TotalCost != 80000 || arb.TotalRevenue != 140000 || arb.NetProfit != 60000 {
		t.Fatalf("unexpected economics: cost=%d revenue=%d net=%d", arb.TotalCost, arb.TotalRevenue, arb.NetProfit)
	}
	// The margin gate saw the live 2000 ask vs the 4000 dest bid.
	if arb.SourceAsk != 2000 || arb.DestBid != 4000 || arb.MarginPerUnit != 2000 {
		t.Fatalf("unexpected gate prices: ask=%d bid=%d margin=%d", arb.SourceAsk, arb.DestBid, arb.MarginPerUnit)
	}
	// ONE-SHOT: exactly one buy and one sell — never a loop.
	if len(mediator.purchases) != 1 || len(mediator.sells) != 1 {
		t.Fatalf("expected exactly 1 buy and 1 sell (one-shot, no loop), got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

// --max-units caps the tranche below the hull's hold.
func TestArbCoordinator_MaxUnitsCapHonored(t *testing.T) {
	ship := newTradeHauler(t, "ARB-2")
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		MaxUnits:   15,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("arb returned error: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Completed {
		t.Fatalf("expected completion, got %+v", arb)
	}
	if arb.UnitsTraded != 15 {
		t.Fatalf("expected 15 units (max-units cap), got %d", arb.UnitsTraded)
	}
	if len(mediator.purchases) != 1 || mediator.purchases[0].Units != 15 {
		t.Fatalf("expected a single 15u purchase, got %+v", mediator.purchases)
	}
	if arb.TotalCost != 30000 {
		t.Fatalf("expected cost 30000 (15 x 2000), got %d", arb.TotalCost)
	}
}

// --max-spend caps the tranche by working capital: 50000 / 2000 ask = 25 units, so the
// buy never spends more than the cap.
func TestArbCoordinator_MaxSpendCapHonored(t *testing.T) {
	ship := newTradeHauler(t, "ARB-3")
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		MaxSpend:   50000,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("arb returned error: %v", err)
	}
	arb := arbResponse(t, resp)

	if arb.UnitsTraded != 25 {
		t.Fatalf("expected 25 units (50000 / 2000 ask), got %d", arb.UnitsTraded)
	}
	if arb.TotalCost != 50000 {
		t.Fatalf("expected cost exactly at the 50000 cap, got %d", arb.TotalCost)
	}
	if arb.TotalCost > 50000 {
		t.Fatalf("max-spend cap breached: cost %d > 50000", arb.TotalCost)
	}
	if len(mediator.purchases) != 1 || mediator.purchases[0].Units != 25 {
		t.Fatalf("expected a single 25u purchase, got %+v", mediator.purchases)
	}
}

// --min-margin refuses the buy pre-flight when the spread misses the floor: the live
// margin is 2000/unit (4000 bid − 2000 ask), so a 2500 floor aborts before any buy.
func TestArbCoordinator_MinMarginAbortsBeforeBuy(t *testing.T) {
	ship := newTradeHauler(t, "ARB-4")
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		MinMargin:  2500,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Aborted || !arb.MarginAbort {
		t.Fatalf("expected a margin abort, got %+v", arb)
	}
	if arb.Completed {
		t.Fatalf("a refused run must not report Completed, got %+v", arb)
	}
	if arb.MarginPerUnit != 2000 || arb.MinMarginFloor != 2500 {
		t.Fatalf("expected margin 2000 vs floor 2500, got margin=%d floor=%d", arb.MarginPerUnit, arb.MinMarginFloor)
	}
	if arb.AbortReason == "" {
		t.Fatalf("a margin abort must report why")
	}
	if len(mediator.purchases) != 0 || len(mediator.sells) != 0 {
		t.Fatalf("expected zero trades on a margin abort, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

// The spend-floor guard refuses a buy that would drop live treasury below the reserve:
// 100000 credits − (40u x 2000) 80000 projected = 20000 < 50000 default reserve.
func TestArbCoordinator_SpendFloorAbortsBeforeBreachingBuy(t *testing.T) {
	ship := newTradeHauler(t, "ARB-5")
	apiClient := &sfFakeAPIClient{credits: 100000}
	h, mediator := newArbHandler(ship, apiClient)

	ctx := auth.WithPlayerToken(context.Background(), "TOKEN-ARB5")
	resp, err := h.Handle(ctx, &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Aborted || !arb.SpendFloorAbort {
		t.Fatalf("expected a spend-floor abort, got %+v", arb)
	}
	if arb.TreasuryAtAbort != 100000 {
		t.Fatalf("expected the live treasury figure 100000 that revealed the breach, got %d", arb.TreasuryAtAbort)
	}
	if arb.ReserveFloor != defaultWorkingCapitalReserve {
		t.Fatalf("expected the default reserve floor %d, got %d", defaultWorkingCapitalReserve, arb.ReserveFloor)
	}
	if len(mediator.purchases) != 0 || len(mediator.sells) != 0 {
		t.Fatalf("expected zero trades on a spend-floor abort, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

// Fail-closed: a wired apiClient with NO player token in context must abort the buy
// rather than spend blind, even with ample credits.
func TestArbCoordinator_SpendFloorFailsClosedWhenTokenMissing(t *testing.T) {
	ship := newTradeHauler(t, "ARB-6")
	apiClient := &sfFakeAPIClient{credits: 1000000} // ample — the abort must come from the missing token
	h, mediator := newArbHandler(ship, apiClient)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Aborted || !arb.SpendFloorAbort {
		t.Fatalf("expected a fail-closed spend-floor abort on the missing token, got %+v", arb)
	}
	if arb.TreasuryAtAbort != 0 {
		t.Fatalf("a blind fail-closed abort must not populate a live figure it never observed, got %d", arb.TreasuryAtAbort)
	}
	if len(mediator.purchases) != 0 {
		t.Fatalf("expected zero buys on a fail-closed abort, got %d", len(mediator.purchases))
	}
}

// A comfortable treasury must NOT trip the guard: the run trades with the same
// economics as the no-apiClient happy path, proving the guard is not overly aggressive.
func TestArbCoordinator_SpendFloorDoesNotAbortWhenTreasuryClears(t *testing.T) {
	ship := newTradeHauler(t, "ARB-7")
	apiClient := &sfFakeAPIClient{credits: 500000}
	h, mediator := newArbHandler(ship, apiClient)

	ctx := auth.WithPlayerToken(context.Background(), "TOKEN-ARB7")
	resp, err := h.Handle(ctx, &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("arb returned error: %v", err)
	}
	arb := arbResponse(t, resp)

	if arb.SpendFloorAbort || arb.Aborted {
		t.Fatalf("did not expect an abort with a comfortable treasury, got %+v", arb)
	}
	if !arb.Completed || arb.UnitsTraded != 40 || arb.NetProfit != 60000 {
		t.Fatalf("expected the full +60000 trade, got %+v", arb)
	}
	if len(mediator.purchases) != 1 || len(mediator.sells) != 1 {
		t.Fatalf("expected exactly 1 buy and 1 sell, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

// The location guard refuses to buy when the hull is not actually at the source: the
// hull is docked at trSource, so a run targeting BuyAt=trDest aborts before buying.
func TestArbCoordinator_LocationGuardAbortsWhenNotAtSource(t *testing.T) {
	ship := newTradeHauler(t, "ARB-8") // docked at trSource
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trDest,   // hull is NOT here
		SellAt:     trSource, // must differ from BuyAt
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Aborted || !arb.LocationAbort {
		t.Fatalf("expected a location abort, got %+v", arb)
	}
	if arb.ExpectedLocation != trDest || arb.ActualLocation != trSource {
		t.Fatalf("expected location %s but ship at %s; got expected=%q actual=%q", trDest, trSource, arb.ExpectedLocation, arb.ActualLocation)
	}
	if len(mediator.purchases) != 0 {
		t.Fatalf("expected zero buys on a location abort, got %d", len(mediator.purchases))
	}
}

// --- sp-7gr2: routability-check-before-spend ---

// The acceptance criterion: an unroutable cross-system --sell-at refuses the buy
// BEFORE spending, with a clear message naming both systems. This inverts the
// incident order (buy at C37, fly to the home gate, THEN discover no route to
// JP61 and crash laden): with the gate graph wired, a sell leg in a system we
// cannot reach is refused pre-buy, and ZERO cargo is purchased.
func TestArbCoordinator_UnroutableSellAt_RefusesBeforeBuy(t *testing.T) {
	ship := newTradeHauler(t, "ARB-9") // docked at trSource (system X1-TR)
	h, mediator := newArbHandler(ship, nil)
	// The gate graph reports NO path (empty) from X1-TR to X1-JP61.
	h.SetGateGraph(&fakeGateGraph{path: nil})

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,          // the hull IS here, so only routability can abort
		SellAt:     "X1-JP61-MARKET",  // a different, unreachable system
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Aborted || !arb.RoutabilityAbort {
		t.Fatalf("expected a routability abort, got %+v", arb)
	}
	if len(mediator.purchases) != 0 {
		t.Fatalf("expected ZERO buys when the sell leg is unroutable, got %d", len(mediator.purchases))
	}
	if !strings.Contains(arb.AbortReason, "X1-TR") || !strings.Contains(arb.AbortReason, "X1-JP61") {
		t.Fatalf("the refusal must name both systems, got %q", arb.AbortReason)
	}
	if arb.Completed {
		t.Fatal("an unroutable refusal is a did-not-trade abort, not a completed run")
	}
}

// A cross-system lane that IS routable must clear the routability guard — the
// guard refuses only the genuinely unreachable, it does not veto every
// cross-system buy. Here the graph reports a route, so the run proceeds past
// Guard 0 to the location guard (which aborts, since ARB-9 is not at the far
// buy-at) — proving routability did NOT abort.
func TestArbCoordinator_RoutableCrossSystem_PassesRoutabilityGuard(t *testing.T) {
	ship := newTradeHauler(t, "ARB-9") // docked at trSource (system X1-TR)
	h, mediator := newArbHandler(ship, nil)
	h.SetGateGraph(&fakeGateGraph{path: []string{"X1-KA42", "X1-JP61"}}) // a real route exists

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      "X1-KA42-DOCK",   // hull is NOT here → location guard will catch it
		SellAt:     "X1-JP61-MARKET", // routable per the graph above
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("guarded refusal must not be a Go error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if arb.RoutabilityAbort {
		t.Fatalf("a routable cross-system lane must NOT trip the routability guard, got %+v", arb)
	}
	if !arb.LocationAbort {
		t.Fatalf("expected the run to proceed to the location guard, got %+v", arb)
	}
	if len(mediator.purchases) != 0 {
		t.Fatalf("the location guard should still prevent any buy, got %d", len(mediator.purchases))
	}
}

// --- sp-5nqx: retry never re-buys, stranded cargo is a failure ---

// arbFaultMediator drives the buy→travel→sell legs for the retry-safety cases. Unlike
// trFakeMediator it MUTATES the repo hull's cargo on buy/sell — mirroring the real
// PurchaseCargo/SellCargo handlers persisting the hold — so a reload on the NEXT Handle
// sees exactly what a prior attempt physically left aboard: the cumulative ACTUAL the
// sp-5nqx resume guard reads. navFailsRemaining forces the first N travel legs to fail
// AFTER the buy has already persisted, reproducing the incident's post-buy jump failure
// (here a same-system navigate stands in for the cross-gate jump; the resume guard keys
// on cargo-aboard, not on which downstream leg failed).
type arbFaultMediator struct {
	ship              *navigation.Ship
	purchases         []*shipCargo.PurchaseCargoCommand
	sells             []*shipCargo.SellCargoCommand
	navFailsRemaining int
}

func (m *arbFaultMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.purchases = append(m.purchases, cmd)
		_ = m.ship.ReceiveCargo(&shared.CargoItem{Symbol: cmd.GoodSymbol, Units: cmd.Units})
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * trSourceAsk, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *navCmd.NavigateRouteCommand:
		if m.navFailsRemaining > 0 {
			m.navFailsRemaining--
			return nil, fmt.Errorf("injected post-buy travel failure (stands in for the sp-5nqx jump rejection)")
		}
		return nil, nil
	case *shipCargo.SellCargoCommand:
		m.sells = append(m.sells, cmd)
		_ = m.ship.RemoveCargo(cmd.GoodSymbol, cmd.Units)
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * trSellRevenue, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil // dock, etc. succeed silently
	}
}

func (m *arbFaultMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *arbFaultMediator) RegisterMiddleware(common.Middleware)               {}

// arbPartialSellMediator lets the destination absorb at most sellCap units per sell, so a
// run that bought a full tranche ends still holding the remainder — the sp-5nqx
// stranded-cargo case. The buy is recorded and reported in full (the tranche the run
// committed to), so the coordinator's stranded check sees bought > sold.
type arbPartialSellMediator struct {
	purchases []*shipCargo.PurchaseCargoCommand
	sells     []*shipCargo.SellCargoCommand
	sellCap   int
}

func (m *arbPartialSellMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.purchases = append(m.purchases, cmd)
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * trSourceAsk, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.sells = append(m.sells, cmd)
		sold := cmd.Units
		if sold > m.sellCap {
			sold = m.sellCap
		}
		return &shipCargo.SellCargoResponse{TotalRevenue: sold * trSellRevenue, UnitsSold: sold, TransactionCount: 1}, nil
	default:
		return nil, nil
	}
}

func (m *arbPartialSellMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *arbPartialSellMediator) RegisterMiddleware(common.Middleware)               {}

// arbHandlerWith wires the arb coordinator onto a caller-supplied mediator (so a test can
// inject faults) but the SAME market economics and single-hull repo the other cases use.
func arbHandlerWith(mediator common.Mediator, ship *navigation.Ship) *RunArbCoordinatorHandler {
	fixture := &trFixture{}
	marketRepo := &trFakeMarketRepo{fixture: fixture}
	shipRepo := &trFakeShipRepo{ship: ship}
	return NewRunArbCoordinatorHandler(mediator, shipRepo, marketRepo, nil, nil, nil)
}

// THE incident, fixed: the container runner retries a failed run by re-running the whole
// Handle. Before sp-5nqx that re-ran the buy every time (the live drill bought 3× =
// 118,872 against a 40k cap). Now attempt 1 buys then fails at travel (post-buy), and the
// retry finds the tranche already aboard, SKIPS the buy, and delivers it — so the buy
// fires EXACTLY ONCE and total spend stays under the cap no matter how many retries run.
func TestArbCoordinator_RetryAfterPostBuyFailure_NeverRebuys_SpendStaysUnderCap(t *testing.T) {
	const maxSpend = 100000
	ship := newTradeHauler(t, "ARB-RETRY") // at trSource, empty 40u hold
	mediator := &arbFaultMediator{ship: ship, navFailsRemaining: 1}
	h := arbHandlerWith(mediator, ship)

	cmd := &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		MaxSpend:   maxSpend,
		PlayerID:   1,
	}

	// Attempt 1: buys the tranche, then the travel leg fails (the missing-departure-hop
	// jump rejection in the live incident) — an operational failure, not a guarded refusal.
	if _, err := h.Handle(context.Background(), cmd); err == nil {
		t.Fatal("attempt 1 must fail at the post-buy travel leg")
	}
	if len(mediator.purchases) != 1 {
		t.Fatalf("attempt 1 must buy exactly once, got %d", len(mediator.purchases))
	}

	// Attempt 2 (the runner's retry): the tranche is physically aboard, so the buy is
	// skipped and the run resumes at travel→sell and completes.
	resp, err := h.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("the retry must resume and complete, got error: %v", err)
	}
	arb := arbResponse(t, resp)
	if !arb.Completed || arb.Aborted {
		t.Fatalf("the retry must complete the delivery, got %+v", arb)
	}

	// THE cap contract: across BOTH attempts the buy fired exactly once and total spend
	// never exceeded --max-spend. Under the old blind re-buy this would be 2 purchases /
	// 160,000 spend — over the cap, the exact defect sp-5nqx closes.
	if len(mediator.purchases) != 1 {
		t.Fatalf("the retry must NEVER re-buy: expected exactly 1 purchase across all attempts, got %d", len(mediator.purchases))
	}
	totalSpend := 0
	for _, p := range mediator.purchases {
		totalSpend += p.Units * trSourceAsk
	}
	if totalSpend > maxSpend {
		t.Fatalf("total spend across all retries (%d) breached --max-spend %d", totalSpend, maxSpend)
	}
	if len(mediator.sells) != 1 || arb.UnitsTraded != 40 {
		t.Fatalf("the resumed tranche must be delivered once in full: got %d sells, %d units", len(mediator.sells), arb.UnitsTraded)
	}
}

// The resume guard in isolation: a run that STARTS holding the good (as a prior attempt's
// persisted buy would leave the hull) must not buy again — it resumes straight to the
// sell. Uses the default fakes, so the only thing under test is the cargo-aboard skip.
func TestArbCoordinator_ResumeCargoAboard_SkipsBuyAndDelivers(t *testing.T) {
	ship := newTradeHauler(t, "ARB-RESUME")
	if err := ship.ReceiveCargo(&shared.CargoItem{Symbol: trGood, Units: 12}); err != nil {
		t.Fatalf("preload cargo: %v", err)
	}
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("resume must not error, got: %v", err)
	}
	arb := arbResponse(t, resp)

	if !arb.Completed {
		t.Fatalf("a resumed run that delivers its held tranche must complete, got %+v", arb)
	}
	if len(mediator.purchases) != 0 {
		t.Fatalf("a hull already holding the good must NEVER re-buy, got %d purchases", len(mediator.purchases))
	}
	if len(mediator.sells) != 1 || mediator.sells[0].Units != 12 {
		t.Fatalf("expected exactly one sell of the 12u already aboard, got %+v", mediator.sells)
	}
	if arb.UnitsTraded != 12 {
		t.Fatalf("expected the 12 held units delivered, got %d", arb.UnitsTraded)
	}
}

// sp-5nqx fix (c): a run that buys a tranche it cannot fully offload ends holding unsold
// units of the good — that is a FAILURE, not the false success=true the incident logged
// with 36 units stranded. It must surface a non-nil error (→ the runner's
// signalCompletionWithStatus(false)) whose message carries good, unsold count, and location.
func TestArbCoordinator_StrandedCargo_ReportsFailure(t *testing.T) {
	ship := newTradeHauler(t, "ARB-STRAND") // buys the full 40u hold
	mediator := &arbPartialSellMediator{sellCap: 30}
	h := arbHandlerWith(mediator, ship)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err == nil {
		t.Fatal("a run ending with unsold bought cargo must return an error, not a silent success")
	}
	arb := arbResponse(t, resp)

	if arb.Completed {
		t.Fatalf("a stranded-cargo run must NOT report Completed, got %+v", arb)
	}
	if arb.Error == "" {
		t.Fatalf("the failure must be surfaced on the response, got %+v", arb)
	}
	// 40 bought, 30 sold → 10 stranded; the message must name the strand so it is greppable
	// and hand-recoverable (good, unsold units, and where they are stuck).
	for _, want := range []string{"stranded", trGood, trDest} {
		if !strings.Contains(arb.AbortReason, want) {
			t.Fatalf("stranded failure message %q must mention %q", arb.AbortReason, want)
		}
	}
	if len(mediator.purchases) != 1 {
		t.Fatalf("expected the single tranche buy, got %d purchases", len(mediator.purchases))
	}
}

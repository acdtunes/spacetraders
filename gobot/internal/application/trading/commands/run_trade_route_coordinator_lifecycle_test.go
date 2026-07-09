package commands

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-1hj5 — the container lifecycle contract's first enforcement (sp-7yej
// invariants 1+2) on trade-route. The live incident (22:43Z,
// trade-route-TORWIND-19, --max-visits 12): circuit leg 1 completed profitably,
// the ranker selected the lab_instruments lane, the buy filled 18 units, the
// cross-system jump failed INSTANTLY — and the run ended at 'Iteration 1
// completed', success=true, the hull released DOCKED holding all 18 units.
// Three contract violations in one exit:
//
//	(a) --max-visits was a per-lane bound, not the run's own budget;
//	(b) the run ended BETWEEN a leg's buy and its sell (no finish-current-leg);
//	(c) the laden exit reported success=true (5nqx rule absent here).

// legFailureMediator wraps zvMediator and fails navigate legs whose destination
// is the lane's IMPORT waypoint — the exact leg the incident's jump failure
// interrupted (buy done, delivery failed). failCount = -1 fails every attempt
// (a persistent breakage); N > 0 fails the first N attempts then recovers (the
// incident's transient class).
type legFailureMediator struct {
	*zvMediator
	mu            sync.Mutex
	failCount     int
	destAttempts  int
	failSignature string
}

func (m *legFailureMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	if nav, ok := request.(*navCmd.NavigateRouteCommand); ok && nav.Destination == zvDst {
		m.mu.Lock()
		m.destAttempts++
		fail := m.failCount == -1 || m.destAttempts <= m.failCount
		m.mu.Unlock()
		if fail {
			return nil, fmt.Errorf("jump to %s failed: %s", zvDst, m.failSignature)
		}
	}
	return m.zvMediator.Send(ctx, request)
}

func newLegFailureHarness(t *testing.T, capacity, failCount int) (*RunTradeRouteCoordinatorHandler, *legFailureMediator, *metaCapturingLogger, context.Context) {
	t.Helper()
	ship := newResidualHauler(t, "T19", capacity, 0)
	handler, inner := newZvHarness(t, ship, 0)
	mediator := &legFailureMediator{zvMediator: inner, failCount: failCount, failSignature: "simulated gate error (4236)"}
	handler.mediator = mediator
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	return handler, mediator, logger, ctx
}

// (b) FINISH-CURRENT-LEG, the incident's transient class: the delivery leg
// fails once after the buy, then recovers. The run must FINISH the leg — sell
// the whole tranche at the destination — and exit EMPTY, not surrender the run
// laden the way the incident did. RED before the fix: the coordinator returned
// on the first travel error with 18 units aboard and no recovery attempt.
func TestTradeRouteCoordinator_TransientDeliveryFailure_FinishesLegBeforeRunEnds(t *testing.T) {
	handler, mediator, logger, ctx := newLegFailureHarness(t, 40, 1)

	resp, err := handler.Handle(ctx, &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   "T19",
		SystemSymbol: zvSystem,
		PlayerID:     1,
		MaxVisits:    12, // the incident's grant
	})
	if err != nil {
		t.Fatalf("a recovered leg is a clean exit, not an error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	// The leg was finished: the whole bought tranche was sold at the destination.
	if mediator.fixture.onboard != 0 {
		t.Fatalf("finish-current-leg must empty the hold before the run ends; %d units still aboard", mediator.fixture.onboard)
	}
	if coord.UnitsTraded != 18 {
		t.Fatalf("the bought tranche (18) must be fully sold by the leg-finishing liquidation, sold %d", coord.UnitsTraded)
	}
	if coord.TotalRevenue <= 0 || coord.NetProfit <= 0 {
		t.Fatalf("the finished leg's sell must land on the ledger (revenue %d, net %d)", coord.TotalRevenue, coord.NetProfit)
	}

	// The run is honest: nothing stranded, completion not vetoed.
	if coord.CargoStranded {
		t.Fatalf("a recovered leg must not be marked stranded: %+v", coord)
	}
	if ok, reason := coord.CompletionOutcome(); !ok {
		t.Fatalf("a recovered leg must not veto completion, vetoed with %q", reason)
	}
	if entry := logger.findByAction("cargo_aboard_exit"); entry != nil {
		t.Fatalf("a recovered leg must not emit the stranded cargo_aboard_exit record: %+v", entry)
	}

	// The abort is still self-diagnosing (the leg DID fail once).
	if !strings.Contains(coord.AbortReason, "travel to destination") || !strings.Contains(coord.AbortReason, "4236") {
		t.Fatalf("AbortReason must carry the failed leg's verbatim cause, got %q", coord.AbortReason)
	}
	if coord.ExitReason != exitReasonError {
		t.Fatalf("a leg-failure exit reports exit_reason=error, got %q", coord.ExitReason)
	}
}

// (c) HONEST COMPLETION, the persistent class: the delivery leg fails on every
// attempt, including the bounded finish-current-leg retries. The run must end
// as a STRANDED FAILURE — CargoStranded set, the structured cargo_aboard_exit
// record emitted, and CompletionOutcome vetoing the runner's success=true —
// never the incident's laden success=true. RED before the fix: the laden exit
// was a WARN log on an otherwise successful run.
func TestTradeRouteCoordinator_PersistentDeliveryFailure_IsStrandedFailure(t *testing.T) {
	handler, mediator, logger, ctx := newLegFailureHarness(t, 40, -1)

	resp, err := handler.Handle(ctx, &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   "T19",
		SystemSymbol: zvSystem,
		PlayerID:     1,
		MaxVisits:    12,
	})
	// Still a nil Go error BY DESIGN: the runner must not restart-retry a
	// stranded run (a re-run cannot resume the ranked lane and would trade
	// around the stranded cargo); the failure is threaded via the response.
	if err != nil {
		t.Fatalf("a stranded run threads failure via the response, not a handler error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	if !coord.CargoStranded {
		t.Fatal("a run ending with cargo bought this run aboard must set CargoStranded (sp-1hj5)")
	}
	if coord.CargoStrandedUnits != 18 {
		t.Fatalf("CargoStrandedUnits must report the 18 units aboard, got %d", coord.CargoStrandedUnits)
	}
	if mediator.fixture.onboard != 18 {
		t.Fatalf("fixture sanity: the hold should still carry the 18 bought units, has %d", mediator.fixture.onboard)
	}

	// The runner-facing veto (sp-7yej invariant 2).
	ok, reason := coord.CompletionOutcome()
	if ok {
		t.Fatal("a stranded run must veto the runner's success=true via CompletionOutcome")
	}
	if !strings.Contains(reason, "stranded") || !strings.Contains(reason, "18") || !strings.Contains(reason, zvGood) {
		t.Fatalf("the veto reason must name the strand (units+good), got %q", reason)
	}
	// The failure cause travels inside the veto reason so the container's
	// failure signature is self-diagnosing.
	if !strings.Contains(reason, "4236") {
		t.Fatalf("the veto reason must embed the failed leg's verbatim cause, got %q", reason)
	}

	if coord.ExitReason != exitReasonCargoStranded {
		t.Fatalf("a stranded exit reports exit_reason=%q, got %q", exitReasonCargoStranded, coord.ExitReason)
	}

	// The one structured strand record (sp-149h contract, now epilogue-owned).
	entry := logger.findByAction("cargo_aboard_exit")
	if entry == nil {
		t.Fatal("a stranded exit must emit the structured cargo_aboard_exit record")
	}
	if held, _ := entry.metadata["held"].(int); held != 18 {
		t.Fatalf("cargo_aboard_exit held must be 18, got %v", entry.metadata["held"])
	}

	// The bounded retry actually retried: 1 in-visit attempt + liquidation attempts.
	if mediator.destAttempts < 1+liquidationMaxFailures {
		t.Fatalf("expected the finish-current-leg engine to retry the delivery %d+ times, saw %d", 1+liquidationMaxFailures, mediator.destAttempts)
	}
}

// (a) ONE RUN OWNS ITS --max-visits: the grant is the RUN's total budget across
// every lane the outer loop commits to, and consuming it is a clean, EMPTY exit
// at a leg boundary. RED before the fix: MaxVisits bounded each circuit
// separately, so --max-visits 1 flew one visit per lane until margin death
// (2 visits on this fixture) instead of one visit total.
func TestTradeRouteCoordinator_MaxVisitsIsRunBudget(t *testing.T) {
	ship := newResidualHauler(t, "T20", 40, 0)
	handler, mediator := newZvHarness(t, ship, 0)

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   "T20",
		SystemSymbol: zvSystem,
		PlayerID:     1,
		MaxVisits:    1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	if coord.Visits != 1 {
		t.Fatalf("--max-visits 1 grants the RUN one visit total, flew %d (per-circuit budget leak)", coord.Visits)
	}
	if got := len(mediator.purchases); got != 1 {
		t.Fatalf("one granted visit means exactly one buy, saw %d", got)
	}
	if coord.ExitReason != exitReasonMaxVisits {
		t.Fatalf("a consumed budget is its own clean exit reason %q, got %q", exitReasonMaxVisits, coord.ExitReason)
	}
	if coord.CargoStranded || mediator.fixture.onboard != 0 {
		t.Fatalf("a budget exit must land at a leg boundary EMPTY (stranded=%v, aboard=%d)", coord.CargoStranded, mediator.fixture.onboard)
	}
	if ok, _ := coord.CompletionOutcome(); !ok {
		t.Fatal("a clean budget exit must not veto completion")
	}
}

// lowVolMarketRepo serves the zv lane with a LOW-volume importer and a static
// alive bid: each sell tranche is capped at dstVol units, so a full 18u buy
// leaves carryover aboard at the sell — the partial-sell shape that used to
// leak laden cargo out of orderly exits.
type lowVolMarketRepo struct {
	market.MarketRepository
	dstVolume int
}

func (r *lowVolMarketRepo) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	return []string{zvSrc, zvDst}, nil
}

func (r *lowVolMarketRepo) GetMarketData(_ context.Context, waypointSymbol string, _ int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "STRONG"
	switch waypointSymbol {
	case zvSrc:
		good, err := market.NewTradeGood(zvGood, &supply, &activity, zvSrcAsk-20, zvSrcAsk, zvSrcVol, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	case zvDst:
		good, err := market.NewTradeGood(zvGood, &supply, &activity, zvStartBid, zvDstAsk, r.dstVolume, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	}
	return nil, nil
}

// (a)+(b) combined: a budget exit that lands while PARTIAL-SELL CARRYOVER is
// still aboard (the importer absorbs only 10 units a tick against 18-unit
// buys) must liquidate the remainder at the destination before the run ends —
// budget exits are EMPTY exits. RED before the fix: the visit loop returned
// with the carryover aboard and the run completed laden.
func TestTradeRouteCoordinator_BudgetExitWithCarryover_LiquidatesAndExitsEmpty(t *testing.T) {
	ship := newResidualHauler(t, "T21", 40, 0)
	handler, mediator := newZvHarness(t, ship, 0)
	handler.marketRepo = &lowVolMarketRepo{dstVolume: 10}

	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := handler.Handle(ctx, &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   "T21",
		SystemSymbol: zvSystem,
		PlayerID:     1,
		MaxVisits:    2,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	if coord.Visits != 2 {
		t.Fatalf("the run's budget grants 2 visits, flew %d", coord.Visits)
	}
	if coord.ExitReason != exitReasonMaxVisits {
		t.Fatalf("expected the clean budget exit %q, got %q", exitReasonMaxVisits, coord.ExitReason)
	}
	// THE invariant: nothing bought this run is still aboard at the exit.
	if mediator.fixture.onboard != 0 {
		t.Fatalf("budget exit left %d units aboard - orderly exits must liquidate carryover (sp-1hj5)", mediator.fixture.onboard)
	}
	if coord.CargoStranded {
		t.Fatal("a liquidated carryover is not a strand")
	}
	// Everything bought was eventually sold: 2 buys of 18 = 36 units through the ledger.
	if coord.UnitsTraded != 36 {
		t.Fatalf("all 36 bought units must be sold (visits + liquidation), sold %d", coord.UnitsTraded)
	}
	if entry := logger.findByAction("cargo_aboard_exit"); entry != nil {
		t.Fatalf("an emptied run must not emit the stranded record: %+v", entry)
	}
}

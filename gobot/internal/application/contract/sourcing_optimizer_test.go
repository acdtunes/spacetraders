package contract

import (
	"context"
	"strings"
	"testing"
	"time"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- fakes ---------------------------------------------------------------

// fakeMarketRepo satisfies market.MarketRepository with only the cheapest-
// selling paths wired; everything else is unused by the optimizer.
type fakeMarketRepo struct {
	inSystem map[string]*market.CheapestMarketResult // keyed by systemSymbol
	inErr    error
}

func (f *fakeMarketRepo) GetMarketData(context.Context, string, int) (*market.Market, error) {
	return nil, nil
}
func (f *fakeMarketRepo) FindCheapestMarketSelling(_ context.Context, _ string, systemSymbol string, _ int) (*market.CheapestMarketResult, error) {
	if f.inErr != nil {
		return nil, f.inErr
	}
	return f.inSystem[systemSymbol], nil
}
func (f *fakeMarketRepo) FindCheapestMarketSellingWithSupply(context.Context, string, string, int, string) (*market.CheapestMarketResult, error) {
	return nil, nil
}
func (f *fakeMarketRepo) FindBestMarketBuying(context.Context, string, string, int) (*market.BestMarketBuyingResult, error) {
	return nil, nil
}
func (f *fakeMarketRepo) FindBestMarketForBuying(context.Context, string, string, int) (*market.BestBuyingMarketResult, error) {
	return nil, nil
}
func (f *fakeMarketRepo) FindAllMarketsInSystem(context.Context, string, int) ([]string, error) {
	return nil, nil
}
func (f *fakeMarketRepo) FindFactoryForGood(context.Context, string, string, int) (*market.FactoryResult, error) {
	return nil, nil
}

// fakeCrossSystemRepo additionally implements CrossSystemMarketFinder.
type fakeCrossSystemRepo struct {
	fakeMarketRepo
	allSystems []market.CheapestMarketResult // cheapest-first, as the SQL returns
	allErr     error
}

func (f *fakeCrossSystemRepo) FindCheapestMarketsSellingAllSystems(context.Context, string, int, int) ([]market.CheapestMarketResult, error) {
	if f.allErr != nil {
		return nil, f.allErr
	}
	return f.allSystems, nil
}

// --- helpers -------------------------------------------------------------

func testContract(t *testing.T, payout int, deadline string, units int) *domainContract.Contract {
	t.Helper()
	terms := domainContract.Terms{
		Payment: domainContract.Payment{OnAccepted: payout / 2, OnFulfilled: payout - payout/2},
		Deliveries: []domainContract.Delivery{{
			TradeSymbol:       "ELECTRONICS",
			DestinationSymbol: "X1-HOME-D39",
			UnitsRequired:     units,
			UnitsFulfilled:    0,
		}},
		Deadline: deadline,
	}
	c, err := domainContract.NewContract("ct-test", shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT", terms, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	return c
}

func homeAsk(price int) *market.CheapestMarketResult {
	return &market.CheapestMarketResult{
		WaypointSymbol: "X1-HOME-H51",
		TradeSymbol:    "ELECTRONICS",
		SellPrice:      price,
		Supply:         "SCARCE",
	}
}

// allReachable is the "consumer can route to any scanned system" reachability —
// exercises the cross-gate weighting logic. Production callers pass nil
// (in-system only, sp-9hu8) until the worker adopts multi-jump travel().
func allReachable(string) bool { return true }

// --- PlanSourcing: market selection & weighting ---------------------------

func TestPlanSourcing_InSystemOnly_PicksInSystemMarket(t *testing.T) {
	repo := &fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{
		"X1-HOME": homeAsk(2500),
	}}
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, nil)
	if err != nil {
		t.Fatalf("PlanSourcing: %v", err)
	}
	if plan.Market != "X1-HOME-H51" || plan.UnitAsk != 2500 || plan.CrossSystem {
		t.Fatalf("expected in-system X1-HOME-H51 @2500, got %+v", plan)
	}
	if plan.EffectiveCost != 2500*100 {
		t.Fatalf("expected effective cost %d (no travel penalty in-system), got %d", 2500*100, plan.EffectiveCost)
	}
}

func TestPlanSourcing_CrossGateWins_WhenSavingExceedsPenalty(t *testing.T) {
	// Home asks 6000 post-crush; GQ92 exports at 2367 (the sp-5bmq evidence
	// shape). Saving on 804 units ≈ 2.9M >> the flat gate penalty.
	repo := &fakeCrossSystemRepo{
		fakeMarketRepo: fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{
			"X1-HOME": homeAsk(6000),
		}},
		allSystems: []market.CheapestMarketResult{
			{WaypointSymbol: "X1-GQ92-F44", TradeSymbol: "ELECTRONICS", SellPrice: 2367},
			{WaypointSymbol: "X1-HOME-H51", TradeSymbol: "ELECTRONICS", SellPrice: 6000},
		},
	}
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 804)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, allReachable)
	if err != nil {
		t.Fatalf("PlanSourcing: %v", err)
	}
	if plan.Market != "X1-GQ92-F44" || !plan.CrossSystem {
		t.Fatalf("expected cross-gate X1-GQ92-F44 to win, got %+v", plan)
	}
	wantEffective := 2367*804 + CrossGateSourcingPenalty
	if plan.EffectiveCost != wantEffective || plan.TravelPenalty != CrossGateSourcingPenalty {
		t.Fatalf("expected effective %d with penalty %d, got %+v", wantEffective, CrossGateSourcingPenalty, plan)
	}
}

func TestPlanSourcing_InSystemWins_WhenSavingBelowPenalty(t *testing.T) {
	// Foreign market is 100/unit cheaper on 100 units = 10k saving < the flat
	// gate penalty — a hull must never cross a gate to save pocket change.
	repo := &fakeCrossSystemRepo{
		fakeMarketRepo: fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{
			"X1-HOME": homeAsk(2500),
		}},
		allSystems: []market.CheapestMarketResult{
			{WaypointSymbol: "X1-GQ92-F44", TradeSymbol: "ELECTRONICS", SellPrice: 2400},
			{WaypointSymbol: "X1-HOME-H51", TradeSymbol: "ELECTRONICS", SellPrice: 2500},
		},
	}
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, allReachable)
	if err != nil {
		t.Fatalf("PlanSourcing: %v", err)
	}
	if plan.Market != "X1-HOME-H51" || plan.CrossSystem {
		t.Fatalf("expected in-system market to win (saving 10k < penalty %d), got %+v", CrossGateSourcingPenalty, plan)
	}
}

func TestPlanSourcing_NoInSystemMarket_FallsThroughToCrossGate(t *testing.T) {
	repo := &fakeCrossSystemRepo{
		fakeMarketRepo: fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{}},
		allSystems: []market.CheapestMarketResult{
			{WaypointSymbol: "X1-GQ92-F44", TradeSymbol: "ELECTRONICS", SellPrice: 2367},
		},
	}
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 200)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, allReachable)
	if err != nil {
		t.Fatalf("PlanSourcing: %v", err)
	}
	if plan.Market != "X1-GQ92-F44" || !plan.CrossSystem {
		t.Fatalf("expected cross-gate market when in-system has none, got %+v", plan)
	}
}

func TestPlanSourcing_CrossScanError_FallsBackToInSystem(t *testing.T) {
	repo := &fakeCrossSystemRepo{
		fakeMarketRepo: fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{
			"X1-HOME": homeAsk(2500),
		}},
		allErr: context.DeadlineExceeded,
	}
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, allReachable)
	if err != nil {
		t.Fatalf("PlanSourcing should fall back on scan error, got: %v", err)
	}
	if plan.Market != "X1-HOME-H51" {
		t.Fatalf("expected in-system fallback, got %+v", plan)
	}
}

func TestPlanSourcing_NoMarketAnywhere_ErrorsLikeTheOldMiss(t *testing.T) {
	repo := &fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{}}
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)

	_, err := PlanSourcing(context.Background(), c, repo, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "no market found selling") {
		t.Fatalf("expected the wait-for-scouts error class, got: %v", err)
	}
}

// --- sp-9hu8: nil reachability excludes worker-unreachable sources ----------

// The failure class: the optimizer picked a cheaper CROSS-system source, the
// worker's in-system navigation could not reach it, and the container crashed
// on 'waypoint not found in cache'. In nil (in-system-only) mode a cheaper
// foreign source must be UNSELECTABLE — the in-system market wins even though it
// is dearer.
func TestPlanSourcing_NilReachability_ExcludesCheaperForeignSource(t *testing.T) {
	repo := &fakeCrossSystemRepo{
		fakeMarketRepo: fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{
			"X1-HOME": homeAsk(6000),
		}},
		allSystems: []market.CheapestMarketResult{
			{WaypointSymbol: "X1-GQ92-F44", TradeSymbol: "ELECTRONICS", SellPrice: 2367}, // cheaper, unreachable
			{WaypointSymbol: "X1-HOME-H51", TradeSymbol: "ELECTRONICS", SellPrice: 6000},
		},
	}
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 804)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, nil)
	if err != nil {
		t.Fatalf("PlanSourcing: %v", err)
	}
	if plan.CrossSystem || plan.Market != "X1-HOME-H51" || plan.UnitAsk != 6000 {
		t.Fatalf("in-system-only mode must exclude the cheaper foreign source, got %+v", plan)
	}
}

// The other half of the fix: when there is NO in-system market but a cheaper
// foreign one exists, nil mode must ERROR (wait-for-scouts / re-project next
// pass) rather than return the unreachable foreign market. Returning it is what
// crashed the worker; erroring parks the contract without skipping it (RULINGS
// #1 — the coordinator retries every pass).
func TestPlanSourcing_NilReachability_NoInSystemMarket_ErrorsRatherThanReturnForeign(t *testing.T) {
	repo := &fakeCrossSystemRepo{
		fakeMarketRepo: fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{}}, // none in-system
		allSystems: []market.CheapestMarketResult{
			{WaypointSymbol: "X1-GQ92-F44", TradeSymbol: "ELECTRONICS", SellPrice: 2367},
		},
	}
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 200)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "no market found selling") {
		t.Fatalf("in-system-only with no reachable market must error like the old miss, got plan=%+v err=%v", plan, err)
	}
}

// The defer projection must re-project on the ask the worker can actually pay —
// the in-system ask — not the excluded (cheaper) foreign one. Composing
// PlanSourcing(nil) → EvaluateSourcingDefer proves the projection basis.
func TestPlanSourcing_NilReachability_DeferReprojectsOnInSystemAsk(t *testing.T) {
	repo := &fakeCrossSystemRepo{
		fakeMarketRepo: fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{
			"X1-HOME": homeAsk(6000),
		}},
		allSystems: []market.CheapestMarketResult{
			{WaypointSymbol: "X1-GQ92-F44", TradeSymbol: "ELECTRONICS", SellPrice: 2367}, // excluded in nil mode
			{WaypointSymbol: "X1-HOME-H51", TradeSymbol: "ELECTRONICS", SellPrice: 6000},
		},
	}
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 804)
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC) // 7 days runway

	plan, err := PlanSourcing(context.Background(), c, repo, 1, nil)
	if err != nil {
		t.Fatalf("PlanSourcing: %v", err)
	}
	if plan.EffectiveCost != 6000*804 {
		t.Fatalf("projection basis must be the in-system ask (6000), not the excluded foreign 2367: %+v", plan)
	}
	d := EvaluateSourcingDefer(plan, c, now)
	if d.ProjectedNet != 120_000-6000*804 {
		t.Fatalf("defer must re-project on the in-system ask, got net %d", d.ProjectedNet)
	}
	if !d.Defer {
		t.Fatalf("deep-negative in-system projection with runway must defer, got %+v", d)
	}
}

// Per-system gating (the shape the feature's GateGraph-routable predicate will
// use): a reachability that admits one foreign system but not another selects
// the reachable candidate and excludes the cheaper unreachable one.
func TestPlanSourcing_Reachability_GatesPerSystem(t *testing.T) {
	repo := &fakeCrossSystemRepo{
		fakeMarketRepo: fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{
			"X1-HOME": homeAsk(6000),
		}},
		allSystems: []market.CheapestMarketResult{
			{WaypointSymbol: "X1-BADSYS-B1", TradeSymbol: "ELECTRONICS", SellPrice: 1000},  // cheapest, unreachable
			{WaypointSymbol: "X1-GOODSYS-G1", TradeSymbol: "ELECTRONICS", SellPrice: 2000}, // reachable
			{WaypointSymbol: "X1-HOME-H51", TradeSymbol: "ELECTRONICS", SellPrice: 6000},
		},
	}
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 100)

	reachable := func(sys string) bool { return sys == "X1-GOODSYS" }
	plan, err := PlanSourcing(context.Background(), c, repo, 1, reachable)
	if err != nil {
		t.Fatalf("PlanSourcing: %v", err)
	}
	if plan.Market != "X1-GOODSYS-G1" || !plan.CrossSystem {
		t.Fatalf("must select the reachable foreign market, never the cheaper unreachable one, got %+v", plan)
	}
}

// --- EvaluateSourcingDefer: decision table ---------------------------------

func deferPlan(effectiveCost int) *SourcingPlan {
	return &SourcingPlan{
		Good:           "ELECTRONICS",
		Market:         "X1-HOME-H51",
		UnitAsk:        6000,
		UnitsRemaining: 804,
		GoodsCost:      effectiveCost,
		EffectiveCost:  effectiveCost,
	}
}

func TestEvaluateSourcingDefer_ProjectionClearsLine_Proceeds(t *testing.T) {
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 804)
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)

	d := EvaluateSourcingDefer(deferPlan(90_000), c, now) // net +10k
	if d.Defer || d.Overridden {
		t.Fatalf("positive projection must proceed, got %+v", d)
	}
}

func TestEvaluateSourcingDefer_ExactlyAtThreshold_Proceeds(t *testing.T) {
	// payout 100k → threshold −20k; effective cost 120k → net exactly −20k.
	// The line is "worse than −20%", not "at −20%".
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 804)
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)

	d := EvaluateSourcingDefer(deferPlan(120_000), c, now)
	if d.Defer || d.Overridden {
		t.Fatalf("net == threshold must proceed, got %+v", d)
	}
}

func TestEvaluateSourcingDefer_NegativeWithRunway_Defers(t *testing.T) {
	// The −891k shape: payout 120k, sourcing 6000×804 ≈ 4.8M.
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 804)
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC) // 7 days of runway

	d := EvaluateSourcingDefer(deferPlan(6000*804), c, now)
	if !d.Defer || d.Overridden {
		t.Fatalf("deep-negative projection with runway must defer, got %+v", d)
	}
	if d.ProjectedNet != 120_000-6000*804 {
		t.Fatalf("projected net wrong: %d", d.ProjectedNet)
	}
}

func TestEvaluateSourcingDefer_DeadlineInsideWindow_OverridesNeverSkips(t *testing.T) {
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 804)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) // 12h runway < 24h window

	d := EvaluateSourcingDefer(deferPlan(6000*804), c, now)
	if d.Defer {
		t.Fatalf("inside the safety window the engine must SOURCE, never park: %+v", d)
	}
	if !d.Overridden {
		t.Fatalf("override must be flagged so it logs loudly: %+v", d)
	}
}

func TestEvaluateSourcingDefer_UnparseableDeadline_OverridesNeverDefers(t *testing.T) {
	c := testContract(t, 120_000, "not-a-timestamp", 804)
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)

	d := EvaluateSourcingDefer(deferPlan(6000*804), c, now)
	if d.Defer || !d.Overridden {
		t.Fatalf("no parseable runway → fail toward fulfilling (override), got %+v", d)
	}
}

// --- Guard-1-style messages: the numbers live in the TEXT ------------------

func TestDeferMessage_CarriesTheNumbersInMessageText(t *testing.T) {
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 804)
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	plan := deferPlan(6000 * 804)

	d := EvaluateSourcingDefer(plan, c, now)
	msg := d.DeferMessage(plan)

	for _, want := range []string{
		"-4704000",     // projected net (120000 − 4824000)
		"120000",       // payout
		"6000",         // best ask
		"X1-HOME-H51",  // market
		"never-skip",   // the ruling, spelled out
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("defer message must carry %q in the text, got: %s", want, msg)
		}
	}
}

func TestOverrideMessage_CarriesTheNumbersInMessageText(t *testing.T) {
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 804)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	plan := deferPlan(6000 * 804)

	d := EvaluateSourcingDefer(plan, c, now)
	msg := d.OverrideMessage(plan)

	for _, want := range []string{"-4704000", "120000", "6000", "X1-HOME-H51", "RULINGS #1"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("override message must carry %q in the text, got: %s", want, msg)
		}
	}
}

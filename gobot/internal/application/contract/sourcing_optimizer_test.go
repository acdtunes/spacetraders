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

// fakeCrossSystemRepo additionally exposes an all-systems cheapest-market scan
// (the shape the trade engine's demand miner uses). Contract sourcing must NEVER
// consult it — sourcing is HOME-system only (RULINGS #14). The fake exists purely
// to prove the optimizer ignores foreign markets even when a cheaper one is
// readily discoverable.
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

// --- PlanSourcing: HOME-system-only market selection ----------------------

func TestPlanSourcing_PicksHomeSystemMarket(t *testing.T) {
	repo := &fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{
		"X1-HOME": homeAsk(2500),
	}}
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)

	plan, err := PlanSourcing(context.Background(), c, repo, 1)
	if err != nil {
		t.Fatalf("PlanSourcing: %v", err)
	}
	if plan.Market != "X1-HOME-H51" || plan.UnitAsk != 2500 {
		t.Fatalf("expected home-system X1-HOME-H51 @2500, got %+v", plan)
	}
	if plan.EffectiveCost != 2500*100 {
		t.Fatalf("expected effective cost %d (goods only, no travel term), got %d", 2500*100, plan.EffectiveCost)
	}
}

// The core of CORRECTION #1: a cheaper source in ANOTHER system must never be
// selected. Even when the repository can enumerate all-systems markets and a
// foreign one is far cheaper, contract sourcing considers ONLY the delivery's
// HOME system (RULINGS #14) — the in-system worker cannot reach a foreign source
// (sp-9hu8), so it must be UNSELECTABLE, not dispatched-then-crashed.
func TestPlanSourcing_NeverSelectsCrossSystemSource_EvenWhenCheaper(t *testing.T) {
	repo := &fakeCrossSystemRepo{
		fakeMarketRepo: fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{
			"X1-HOME": homeAsk(6000), // dearer home ask
		}},
		allSystems: []market.CheapestMarketResult{
			{WaypointSymbol: "X1-GQ92-F44", TradeSymbol: "ELECTRONICS", SellPrice: 2367}, // far cheaper, FOREIGN
			{WaypointSymbol: "X1-HOME-H51", TradeSymbol: "ELECTRONICS", SellPrice: 6000},
		},
	}
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 804)

	plan, err := PlanSourcing(context.Background(), c, repo, 1)
	if err != nil {
		t.Fatalf("PlanSourcing: %v", err)
	}
	if plan.Market != "X1-HOME-H51" || plan.UnitAsk != 6000 {
		t.Fatalf("HOME-system-only sourcing must pick the home market and never the cheaper foreign one, got %+v", plan)
	}
	if plan.EffectiveCost != 6000*804 {
		t.Fatalf("projection basis must be the home ask (6000), not the excluded foreign 2367: %+v", plan)
	}
}

// The other half: when NO market in the home system sells the good, sourcing must
// ERROR (wait-for-scouts / re-project next pass) rather than return a reachable-
// looking foreign market. Returning a foreign waypoint is what crashed the worker
// ('waypoint not found in cache'); erroring parks the contract without skipping it
// (RULINGS #1 — the coordinator retries every pass).
func TestPlanSourcing_NoHomeMarket_ErrorsRatherThanReturnForeign(t *testing.T) {
	repo := &fakeCrossSystemRepo{
		fakeMarketRepo: fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{}}, // none in home system
		allSystems: []market.CheapestMarketResult{
			{WaypointSymbol: "X1-GQ92-F44", TradeSymbol: "ELECTRONICS", SellPrice: 2367},
		},
	}
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 200)

	plan, err := PlanSourcing(context.Background(), c, repo, 1)
	if err == nil || !strings.Contains(err.Error(), "no market found selling") {
		t.Fatalf("home-system-only with no home market must error like the old miss, got plan=%+v err=%v", plan, err)
	}
}

func TestPlanSourcing_NoMarketAnywhere_ErrorsLikeTheOldMiss(t *testing.T) {
	repo := &fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{}}
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)

	_, err := PlanSourcing(context.Background(), c, repo, 1)
	if err == nil || !strings.Contains(err.Error(), "no market found selling") {
		t.Fatalf("expected the wait-for-scouts error class, got: %v", err)
	}
}

// The negative projection must re-project on the home ask (the only ask the worker
// can actually pay), and the run SOURCES on it (never parks — sp-x8ck). Composing
// PlanSourcing → EvaluateSourcingDefer proves the projection basis even when a
// cheaper foreign market exists in the repository.
func TestPlanSourcing_NegativeReprojectsOnHomeAsk(t *testing.T) {
	repo := &fakeCrossSystemRepo{
		fakeMarketRepo: fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{
			"X1-HOME": homeAsk(6000),
		}},
		allSystems: []market.CheapestMarketResult{
			{WaypointSymbol: "X1-GQ92-F44", TradeSymbol: "ELECTRONICS", SellPrice: 2367}, // excluded — foreign
			{WaypointSymbol: "X1-HOME-H51", TradeSymbol: "ELECTRONICS", SellPrice: 6000},
		},
	}
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 804)
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC) // 7 days runway

	plan, err := PlanSourcing(context.Background(), c, repo, 1)
	if err != nil {
		t.Fatalf("PlanSourcing: %v", err)
	}
	if plan.EffectiveCost != 6000*804 {
		t.Fatalf("projection basis must be the home ask (6000), not the excluded foreign 2367: %+v", plan)
	}
	d := EvaluateSourcingDefer(plan, c, now)
	if d.ProjectedNet != 120_000-6000*804 {
		t.Fatalf("must re-project on the home ask, got net %d", d.ProjectedNet)
	}
	if d.Defer {
		t.Fatalf("negative home projection must SOURCE, never park (RULINGS #1): %+v", d)
	}
	if !d.Overridden {
		t.Fatalf("negative home projection must be flagged Overridden (source at a loss): %+v", d)
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

func TestEvaluateSourcingDefer_NegativeWithRunway_SourcesNeverParks(t *testing.T) {
	// The −891k shape: payout 120k, sourcing 6000×804 ≈ 4.8M, a full 7 days of
	// runway. Before sp-x8ck this DEFERRED (parked) until asks reverted; the
	// Admiral override makes never-skip govern the contract path, so it now
	// SOURCES at the loss (Overridden) and never parks — runway no longer gates
	// the decision.
	c := testContract(t, 120_000, "2026-07-16T00:00:00Z", 804)
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC) // 7 days of runway

	d := EvaluateSourcingDefer(deferPlan(6000*804), c, now)
	if d.Defer {
		t.Fatalf("deep-negative projection must SOURCE, never park (RULINGS #1 never-skip): %+v", d)
	}
	if !d.Overridden {
		t.Fatalf("deep-negative projection must be flagged Overridden (source at a loss): %+v", d)
	}
	if d.ProjectedNet != 120_000-6000*804 {
		t.Fatalf("projected net wrong: %d", d.ProjectedNet)
	}
}

// TestSourcing_UnsourceableAtProfit_StillSources is the sp-x8ck regression: the
// live deadlock. A contract whose ONLY in-system source is priced above payout
// (6 ANTIMATTER, sole market an IMPORT @ 14912 → 89472 effective vs payout
// 70140; net −19332, worse than the −20%-of-payout line −14028) with a full
// ~7-day runway used to DEFER/park FOREVER, deadlocking the serial one-active-
// contract pipeline for the whole deadline window (zero contract income).
// RULINGS #1 never-skip GOVERNS the contract sourcing path over the profit guard
// (Admiral override, sp-x8ck): the run must SOURCE at the negative margin
// (Overridden = source-and-log-the-loss), NEVER park (Defer). No defer/parking
// decision may be produced.
func TestSourcing_UnsourceableAtProfit_StillSources(t *testing.T) {
	c := testContract(t, 70_140, "2026-07-19T00:00:00Z", 6) // ~7 days runway, well outside any window
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	plan := &SourcingPlan{
		Good:           "ANTIMATTER",
		Market:         "X1-VB74-A1",
		UnitAsk:        14912,
		UnitsRemaining: 6,
		GoodsCost:      6 * 14912,
		EffectiveCost:  6 * 14912, // 89472 > payout 70140 → deep negative
	}

	d := EvaluateSourcingDefer(plan, c, now)

	if d.Defer {
		t.Fatalf("unsourceable-at-profit contract must SOURCE, never park (RULINGS #1 never-skip): %+v", d)
	}
	if !d.Overridden {
		t.Fatalf("negative-margin sourcing must be flagged Overridden so the loss logs loudly and the run proceeds: %+v", d)
	}
	if d.ProjectedNet != 70_140-6*14912 {
		t.Fatalf("projection basis wrong: got net %d, want %d", d.ProjectedNet, 70_140-6*14912)
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
		"-4704000",    // projected net (120000 − 4824000)
		"120000",      // payout
		"6000",        // best ask
		"X1-HOME-H51", // market
		"never-skip",  // the ruling, spelled out
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

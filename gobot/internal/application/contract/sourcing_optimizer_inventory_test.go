package contract

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// fakeInventoryFinder is a directly-controlled InventorySourceFinder: each test
// dictates the InventorySource it returns (or nil for "no stock") and can assert
// it was consulted. It reuses the package's existing market fakes/helpers
// (fakeMarketRepo, testContract, homeAsk) so an inventory plan is compared
// against exactly the market plan it displaces.
type fakeInventoryFinder struct {
	src   *InventorySource
	calls int
}

func (f *fakeInventoryFinder) FindInSystemInventory(_ context.Context, _ int, _ string, _ string) *InventorySource {
	f.calls++
	return f.src
}

func TestPlanSourcing_InventoryFirst_PreferredOverMarket(t *testing.T) {
	// Market sells ELECTRONICS at 2500, but a home warehouse holds enough — the
	// plan sources from inventory at zero ask, not the market.
	repo := &fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{"X1-HOME": homeAsk(2500)}}
	finder := &fakeInventoryFinder{src: &InventorySource{OperationID: "wh-1", StorageWaypoint: "X1-HOME-WH9", UnitsAvailable: 200}}
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, nil, WithInventoryFinder(finder))
	require.NoError(t, err)
	require.Equal(t, SourceInventory, plan.Source)
	require.Equal(t, "X1-HOME-WH9", plan.Market, "inventory plan points at the storage waypoint")
	require.Equal(t, 0, plan.UnitAsk)
	require.Equal(t, 0, plan.GoodsCost)
	require.Equal(t, 0, plan.EffectiveCost, "sunk cost — zero-ask projection")
	require.Equal(t, "wh-1", plan.StorageOperationID)
	require.Equal(t, 100, plan.UnitsRemaining)
	require.False(t, plan.CrossSystem)
}

func TestPlanSourcing_InventoryFirst_PartialStock_StillInventoryPlan(t *testing.T) {
	// Warehouse holds fewer units than the contract needs. The plan is STILL
	// inventory (zero-ask for the covered part); the delivery executor withdraws
	// what is there and sources the remainder two-phase (sp-2ei3), so the plan
	// carries the full remaining count.
	repo := &fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{"X1-HOME": homeAsk(2500)}}
	finder := &fakeInventoryFinder{src: &InventorySource{OperationID: "wh-1", StorageWaypoint: "X1-HOME-WH9", UnitsAvailable: 40}}
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, nil, WithInventoryFinder(finder))
	require.NoError(t, err)
	require.Equal(t, SourceInventory, plan.Source)
	require.Equal(t, 100, plan.UnitsRemaining)
}

func TestPlanSourcing_NoInventory_FallsThroughToMarketByteIdentical(t *testing.T) {
	// Finder returns nil (no stock) — the plan must be the exact market plan the
	// pre-feature code produced, and the finder must have been consulted.
	repo := &fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{"X1-HOME": homeAsk(2500)}}
	finder := &fakeInventoryFinder{src: nil}
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, nil, WithInventoryFinder(finder))
	require.NoError(t, err)
	require.Equal(t, SourceMarket, plan.Source)
	require.Equal(t, "X1-HOME-H51", plan.Market)
	require.Equal(t, 2500, plan.UnitAsk)
	require.Equal(t, 2500*100, plan.EffectiveCost)
	require.Equal(t, 1, finder.calls)
}

func TestPlanSourcing_NilInventoryFinder_MarketPath(t *testing.T) {
	// WithInventoryFinder(nil) is a no-op — market-only, byte-identical.
	repo := &fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{"X1-HOME": homeAsk(2500)}}
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, nil, WithInventoryFinder(nil))
	require.NoError(t, err)
	require.Equal(t, SourceMarket, plan.Source)
	require.Equal(t, "X1-HOME-H51", plan.Market)
	require.Equal(t, 2500, plan.UnitAsk)
}

func TestPlanSourcing_InventoryWhenNoMarketSells_StillSources(t *testing.T) {
	// No market sells the good (FindCheapestMarketSelling returns nil), which
	// would normally error — but inventory short-circuits before the market
	// lookup, so a stocked good still sources.
	repo := &fakeMarketRepo{inSystem: map[string]*market.CheapestMarketResult{}}
	finder := &fakeInventoryFinder{src: &InventorySource{OperationID: "wh-1", StorageWaypoint: "X1-HOME-WH9", UnitsAvailable: 200}}
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)

	plan, err := PlanSourcing(context.Background(), c, repo, 1, nil, WithInventoryFinder(finder))
	require.NoError(t, err)
	require.Equal(t, SourceInventory, plan.Source)
	require.Equal(t, "X1-HOME-WH9", plan.Market)
}

func TestEvaluateSourcingDefer_InventoryPlan_AlwaysProceeds(t *testing.T) {
	// An inventory plan costs 0, so projected net == payout, which always clears
	// the −20%-of-payout defer line: a stocked contract is never parked. This is
	// the whole point of feeding the zero-ask plan into the defer gate.
	c := testContract(t, 100_000, "2026-07-16T00:00:00Z", 100)
	plan := &SourcingPlan{
		Good: "ELECTRONICS", Market: "X1-HOME-WH9", UnitAsk: 0,
		UnitsRemaining: 100, EffectiveCost: 0, Source: SourceInventory,
	}

	d := EvaluateSourcingDefer(plan, c, time.Now())

	require.False(t, d.Defer)
	require.False(t, d.Overridden)
	require.Equal(t, 100_000, d.ProjectedNet)
}

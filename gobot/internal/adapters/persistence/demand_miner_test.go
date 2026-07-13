package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

type fakeDemandSource struct {
	rows              []ContractGoodDemand
	gotDeliverySystem *string
}

func (f *fakeDemandSource) ContractGoodDemand(ctx context.Context, eraID *int, deliverySystem *string) ([]ContractGoodDemand, error) {
	f.gotDeliverySystem = deliverySystem
	return f.rows, nil
}

type fakeMarketAsks struct {
	// crossByGood entries must be cheapest-first, mirroring the real SQL (sell_price ASC).
	// The cross scan spans ALL systems INCLUDING the home system — the miner treats the
	// cheapest of them (home or foreign) as the source, so home markets belong here too.
	crossByGood map[string][]market.CheapestMarketResult
	homeByGood  map[string]*market.CheapestMarketResult
}

func (f *fakeMarketAsks) FindCheapestMarketsSellingAllSystems(ctx context.Context, good string, playerID, limit int) ([]market.CheapestMarketResult, error) {
	return f.crossByGood[good], nil
}

func (f *fakeMarketAsks) FindCheapestMarketSelling(ctx context.Context, good, system string, playerID int) (*market.CheapestMarketResult, error) {
	return f.homeByGood[good], nil
}

func demand(good string, count, units int, first, last time.Time) ContractGoodDemand {
	return ContractGoodDemand{Good: good, ContractCount: count, UnitsRequired: units, FirstSeen: first, LastSeen: last}
}

// TestDemandMiner_CarriesMaxContractUnits pins that the largest single-contract size (the
// auto-cap knapsack's s_G, sp-5n7v) flows from the aggregated demand row through to the
// mined candidate. UnitsRequired is the SUMMED demand across contracts; MaxContractUnits is
// the biggest single contract — the amount the warehouse buffers FULLY or not at all.
func TestDemandMiner_CarriesMaxContractUnits(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	row := ContractGoodDemand{Good: "DRUGS", ContractCount: 3, UnitsRequired: 90, MaxContractUnits: 40, FirstSeen: now.Add(-24 * time.Hour), LastSeen: now}
	src := &fakeDemandSource{rows: []ContractGoodDemand{row}}
	markets := &fakeMarketAsks{
		crossByGood: map[string][]market.CheapestMarketResult{"DRUGS": {{WaypointSymbol: "X1-J58-SRC", SellPrice: 300}}},
		homeByGood:  map[string]*market.CheapestMarketResult{"DRUGS": {WaypointSymbol: "X1-VB74-M", SellPrice: 500}},
	}
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-VB74", 7, nil, DemandMinerOptions{MinRecurrence: 2})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, 40, got[0].MaxContractUnits, "the largest single-contract size (s_G) must reach the candidate")
	require.Equal(t, 90, got[0].DemandUnits, "summed demand is unchanged")
}

// TestDemandMiner_SingleSystem_IncludesHomeExport is the sp-layd defect pin. Post-weekly-
// reset the home system is the ONLY scanned system (0 foreign markets), so the old
// foreign-only miner dropped EVERY good and returned zero rows — the stocker then refused
// with "nothing to stock miner_rows=0". The reframed miner sources the cheapest market
// ANYWHERE (home included): a good the home system exports is retained AND stock-eligible,
// its pre-positioning value coming from the buy-leg the contract worker skips.
func TestDemandMiner_SingleSystem_IncludesHomeExport(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	src := &fakeDemandSource{rows: []ContractGoodDemand{demand("FUEL", 3, 120, now.Add(-48*time.Hour), now)}}
	// Only home-system markets exist for FUEL — no foreign source anywhere.
	markets := &fakeMarketAsks{
		crossByGood: map[string][]market.CheapestMarketResult{
			"FUEL": {{WaypointSymbol: "X1-VB74-EXPORT", SellPrice: 40}},
		},
		homeByGood: map[string]*market.CheapestMarketResult{
			"FUEL": {WaypointSymbol: "X1-VB74-EXPORT", SellPrice: 40},
		},
	}
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-VB74", 7, nil, DemandMinerOptions{MinRecurrence: 2, BuyLegSavingsPerUnit: 5})
	require.NoError(t, err)

	// The home-sourceable good is NOT dropped — miner_rows > 0 (the whole point of sp-layd).
	require.Len(t, got, 1)
	fuel := got[0]
	require.Equal(t, "FUEL", fuel.Good)
	require.Equal(t, "X1-VB74-EXPORT", fuel.ForeignMarket) // the source is the home export
	require.Equal(t, "X1-VB74", fuel.ForeignSystem)        // in the home system itself
	require.Equal(t, 40, fuel.ForeignAsk)
	require.True(t, fuel.HomeAskKnown)
	require.Equal(t, 40, fuel.HomeAsk)
	// Savings vs the contract-source alternative: (home ask 40 + buy-leg 5) − source ask 40 = 5.
	require.Equal(t, 5, fuel.ProjectedSavingsPerUnit)
	require.True(t, fuel.StockEligible)
}

// TestDemandMiner_SavingsVsContractSourceAlternative pins the reframed economics: savings
// is measured against the CONTRACT-SOURCE ALTERNATIVE — what the worker would otherwise pay
// to source in-system (the home ask) PLUS the buy-leg it would fly — not against a mandatory
// foreign ask. So savings = (home_ask + buy_leg) − cheapest_source_ask, which exceeds the
// old home_ask − foreign_ask by exactly the buy-leg.
func TestDemandMiner_SavingsVsContractSourceAlternative(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	src := &fakeDemandSource{rows: []ContractGoodDemand{demand("IRON_ORE", 3, 200, now.Add(-24*time.Hour), now)}}
	markets := &fakeMarketAsks{
		crossByGood: map[string][]market.CheapestMarketResult{
			// cheapest-first: a foreign source @40 undercuts the home ask @90.
			"IRON_ORE": {{WaypointSymbol: "X1-FOREIGN-B1", SellPrice: 40}, {WaypointSymbol: "X1-VB74-A9", SellPrice: 90}},
		},
		homeByGood: map[string]*market.CheapestMarketResult{
			"IRON_ORE": {WaypointSymbol: "X1-VB74-A9", SellPrice: 90},
		},
	}
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-VB74", 7, nil, DemandMinerOptions{MinRecurrence: 2, BuyLegSavingsPerUnit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)

	iron := got[0]
	require.Equal(t, 40, iron.ForeignAsk)
	require.Equal(t, 90, iron.HomeAsk)
	// (home 90 + buy-leg 10) − source 40 = 60 — NOT the old differential-only 50.
	require.Equal(t, 60, iron.ProjectedSavingsPerUnit)
	require.True(t, iron.StockEligible)
}

// TestDemandMiner_ForeignStillPreferredWhenCheaper: sourcing the CHEAPEST anywhere still
// picks a foreign market when it undercuts the home ask — the reframe adds in-system
// sourcing, it does not abandon the cross-system win when one exists.
func TestDemandMiner_ForeignStillPreferredWhenCheaper(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	src := &fakeDemandSource{rows: []ContractGoodDemand{demand("ALUMINUM", 2, 60, now.Add(-12*time.Hour), now)}}
	markets := &fakeMarketAsks{
		crossByGood: map[string][]market.CheapestMarketResult{
			// foreign @40 is cheaper than the home ask @50 → foreign is the source.
			"ALUMINUM": {{WaypointSymbol: "X1-FOREIGN-Z1", SellPrice: 40}, {WaypointSymbol: "X1-VB74-Y1", SellPrice: 50}},
		},
		homeByGood: map[string]*market.CheapestMarketResult{
			"ALUMINUM": {WaypointSymbol: "X1-VB74-Y1", SellPrice: 50},
		},
	}
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-VB74", 7, nil, DemandMinerOptions{MinRecurrence: 2, BuyLegSavingsPerUnit: 1})
	require.NoError(t, err)
	require.Len(t, got, 1)

	al := got[0]
	require.Equal(t, "X1-FOREIGN-Z1", al.ForeignMarket) // cheaper foreign chosen over home @50
	require.Equal(t, "X1-FOREIGN", al.ForeignSystem)
	require.NotEqual(t, "X1-VB74", al.ForeignSystem)
	require.Equal(t, 40, al.ForeignAsk)
	require.True(t, al.StockEligible)
}

// TestDemandMiner_BuyLegDefaultApplied: when the buy-leg is unset (<=0) the miner falls
// back to DefaultBuyLegSavingsPerUnit so a home-sourceable good still clears the "savings
// must be positive" guard by default — fail OPEN for the in-system case (sp-layd RULINGS).
func TestDemandMiner_BuyLegDefaultApplied(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	src := &fakeDemandSource{rows: []ContractGoodDemand{demand("COPPER_ORE", 2, 40, now.Add(-6*time.Hour), now)}}
	markets := &fakeMarketAsks{
		crossByGood: map[string][]market.CheapestMarketResult{
			"COPPER_ORE": {{WaypointSymbol: "X1-VB74-C1", SellPrice: 30}},
		},
		homeByGood: map[string]*market.CheapestMarketResult{
			"COPPER_ORE": {WaypointSymbol: "X1-VB74-C1", SellPrice: 30},
		},
	}
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-VB74", 7, nil, DemandMinerOptions{MinRecurrence: 2}) // BuyLeg unset
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, DefaultBuyLegSavingsPerUnit, got[0].ProjectedSavingsPerUnit)
	require.True(t, got[0].StockEligible)
}

// TestDemandMiner_HomeUnknownForeignKnownRetainedNotEligible preserves the sp-dchv Q5
// signal: a good the home market does not sell at all cannot be priced against a
// contract-source alternative (no home ask), so it is RETAINED for captain visibility but
// flagged not stock-eligible — it is informative, never speculatively stocked (RULINGS #6).
func TestDemandMiner_HomeUnknownForeignKnownRetainedNotEligible(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	src := &fakeDemandSource{rows: []ContractGoodDemand{demand("PLATINUM", 2, 15, now.Add(-6*time.Hour), now)}}
	markets := &fakeMarketAsks{
		crossByGood: map[string][]market.CheapestMarketResult{
			"PLATINUM": {{WaypointSymbol: "X1-FOREIGN-P1", SellPrice: 20}},
		},
		homeByGood: map[string]*market.CheapestMarketResult{}, // home does not sell PLATINUM
	}
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-VB74", 7, nil, DemandMinerOptions{MinRecurrence: 2, BuyLegSavingsPerUnit: 5})
	require.NoError(t, err)
	require.Len(t, got, 1)

	plat := got[0]
	require.Equal(t, 20, plat.ForeignAsk)
	require.False(t, plat.HomeAskKnown)
	require.Equal(t, 0, plat.HomeAsk)
	require.Equal(t, 0, plat.ProjectedSavingsPerUnit)
	require.False(t, plat.StockEligible)
}

// TestDemandMiner_NoMarketAnywhereDropped is the legitimate fail-closed drop that survives
// the reframe: a good NO market sells anywhere (not even home) has nothing to source and
// nowhere to buy, so it cannot be pre-positioned and is dropped. This is distinct from the
// in-system case the reframe protects (home DOES sell it) — fail-open never means stocking
// a good with no source.
func TestDemandMiner_NoMarketAnywhereDropped(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	src := &fakeDemandSource{rows: []ContractGoodDemand{
		demand("UNOBTANIUM", 3, 10, now.Add(-6*time.Hour), now), // no market anywhere
		demand("FUEL", 2, 50, now.Add(-6*time.Hour), now),       // home-sourceable
	}}
	markets := &fakeMarketAsks{
		crossByGood: map[string][]market.CheapestMarketResult{
			"FUEL": {{WaypointSymbol: "X1-VB74-EXPORT", SellPrice: 40}},
		},
		homeByGood: map[string]*market.CheapestMarketResult{
			"FUEL": {WaypointSymbol: "X1-VB74-EXPORT", SellPrice: 40},
		},
	}
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-VB74", 7, nil, DemandMinerOptions{MinRecurrence: 2, BuyLegSavingsPerUnit: 5})
	require.NoError(t, err)

	byGood := map[string]DemandCandidate{}
	for _, c := range got {
		byGood[c.Good] = c
	}
	_, hasUnobtanium := byGood["UNOBTANIUM"]
	require.False(t, hasUnobtanium) // dropped — no source anywhere
	_, hasFuel := byGood["FUEL"]
	require.True(t, hasFuel) // retained — home-sourceable
}

// TestDemandMiner_RanksEligibleFirstThenBySavings: stock-eligible rows rank ahead of
// retained-but-ineligible ones, and eligible rows order by total projected savings desc.
func TestDemandMiner_RanksEligibleFirstThenBySavings(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	src := &fakeDemandSource{rows: []ContractGoodDemand{
		demand("IRON_ORE", 3, 200, now.Add(-24*time.Hour), now), // eligible, big savings
		demand("FUEL", 2, 50, now.Add(-6*time.Hour), now),       // eligible, small savings
		demand("PLATINUM", 2, 15, now.Add(-6*time.Hour), now),   // retained, not eligible (home unknown)
	}}
	markets := &fakeMarketAsks{
		crossByGood: map[string][]market.CheapestMarketResult{
			"IRON_ORE": {{WaypointSymbol: "X1-FOREIGN-B1", SellPrice: 40}},
			"FUEL":     {{WaypointSymbol: "X1-VB74-EXPORT", SellPrice: 40}},
			"PLATINUM": {{WaypointSymbol: "X1-FOREIGN-P1", SellPrice: 20}},
		},
		homeByGood: map[string]*market.CheapestMarketResult{
			"IRON_ORE": {WaypointSymbol: "X1-VB74-A9", SellPrice: 90}, // savings (90+5)-40=55 ×200
			"FUEL":     {WaypointSymbol: "X1-VB74-EXPORT", SellPrice: 40}, // savings 5 ×50
		},
	}
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-VB74", 7, nil, DemandMinerOptions{MinRecurrence: 2, BuyLegSavingsPerUnit: 5})
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, "IRON_ORE", got[0].Good) // eligible, highest total savings
	require.Equal(t, "FUEL", got[1].Good)     // eligible, lower total savings
	require.Equal(t, "PLATINUM", got[2].Good) // retained, not eligible → last
	require.True(t, got[0].StockEligible)
	require.True(t, got[1].StockEligible)
	require.False(t, got[2].StockEligible)
}

// TestDemandMiner_RespectsMinRecurrence: goods demanded by fewer than minRecurrence
// distinct contracts are dropped (never speculative, RULINGS #6).
func TestDemandMiner_RespectsMinRecurrence(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	src := &fakeDemandSource{rows: []ContractGoodDemand{
		demand("IRON_ORE", 3, 200, now.Add(-24*time.Hour), now),
		demand("FUEL", 2, 50, now.Add(-6*time.Hour), now),
	}}
	markets := &fakeMarketAsks{
		crossByGood: map[string][]market.CheapestMarketResult{
			"IRON_ORE": {{WaypointSymbol: "X1-VB74-A9", SellPrice: 40}},
			"FUEL":     {{WaypointSymbol: "X1-VB74-EXPORT", SellPrice: 40}},
		},
		homeByGood: map[string]*market.CheapestMarketResult{
			"IRON_ORE": {WaypointSymbol: "X1-VB74-A9", SellPrice: 40},
			"FUEL":     {WaypointSymbol: "X1-VB74-EXPORT", SellPrice: 40},
		},
	}
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-VB74", 7, nil, DemandMinerOptions{MinRecurrence: 3, BuyLegSavingsPerUnit: 5})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "IRON_ORE", got[0].Good)
}

// TestDemandMiner_AppliesTopNCap: the ranked candidate list is capped at TopN.
func TestDemandMiner_AppliesTopNCap(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	src := &fakeDemandSource{rows: []ContractGoodDemand{
		demand("IRON_ORE", 3, 200, now.Add(-24*time.Hour), now),
		demand("FUEL", 2, 50, now.Add(-6*time.Hour), now),
	}}
	markets := &fakeMarketAsks{
		crossByGood: map[string][]market.CheapestMarketResult{
			"IRON_ORE": {{WaypointSymbol: "X1-VB74-A9", SellPrice: 40}},
			"FUEL":     {{WaypointSymbol: "X1-VB74-EXPORT", SellPrice: 40}},
		},
		homeByGood: map[string]*market.CheapestMarketResult{
			"IRON_ORE": {WaypointSymbol: "X1-VB74-A9", SellPrice: 90},
			"FUEL":     {WaypointSymbol: "X1-VB74-EXPORT", SellPrice: 40},
		},
	}
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-VB74", 7, nil, DemandMinerOptions{MinRecurrence: 2, TopN: 1, BuyLegSavingsPerUnit: 5})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "IRON_ORE", got[0].Good) // highest-savings eligible row retained under the cap
}

// TestDemandMiner_PushesHomeScopeIntoDemandSource: the home system is pushed into the
// demand query so only contracts delivering to the home system are counted.
func TestDemandMiner_PushesHomeScopeIntoDemandSource(t *testing.T) {
	src := &fakeDemandSource{rows: nil}
	markets := &fakeMarketAsks{}
	miner := &DemandMiner{demand: src, markets: markets}

	_, err := miner.Mine(context.Background(), "X1-VB74", 7, nil, DemandMinerOptions{MinRecurrence: 2})
	require.NoError(t, err)
	require.NotNil(t, src.gotDeliverySystem)
	require.Equal(t, "X1-VB74", *src.gotDeliverySystem)
}

// TestDemandMiner_RequiresHomeSystem: there is no global home anchor, so the caller must
// supply one.
func TestDemandMiner_RequiresHomeSystem(t *testing.T) {
	miner := &DemandMiner{demand: &fakeDemandSource{}, markets: &fakeMarketAsks{}}
	_, err := miner.Mine(context.Background(), "", 7, nil, DemandMinerOptions{})
	require.Error(t, err)
}

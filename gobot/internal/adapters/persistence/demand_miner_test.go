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
	crossByGood map[string][]market.CheapestMarketResult
	homeByGood  map[string]*market.CheapestMarketResult
}

func (f *fakeMarketAsks) FindCheapestMarketsSellingAllSystems(ctx context.Context, good string, playerID, limit int) ([]market.CheapestMarketResult, error) {
	return f.crossByGood[good], nil
}

func (f *fakeMarketAsks) FindCheapestMarketSelling(ctx context.Context, good, system string, playerID int) (*market.CheapestMarketResult, error) {
	return f.homeByGood[good], nil
}

func newMinerFixture() (*fakeDemandSource, *fakeMarketAsks) {
	first := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeDemandSource{rows: []ContractGoodDemand{
		{Good: "IRON_ORE", ContractCount: 3, UnitsRequired: 300, FirstSeen: first, LastSeen: first.Add(96 * time.Hour)},
		{Good: "GOLD", ContractCount: 2, UnitsRequired: 20, FirstSeen: first, LastSeen: first},
		{Good: "COPPER_ORE", ContractCount: 2, UnitsRequired: 40, FirstSeen: first, LastSeen: first},
		{Good: "NICKEL", ContractCount: 2, UnitsRequired: 10, FirstSeen: first, LastSeen: first},
		{Good: "TIN", ContractCount: 1, UnitsRequired: 500, FirstSeen: first, LastSeen: first},
	}}
	markets := &fakeMarketAsks{
		crossByGood: map[string][]market.CheapestMarketResult{
			// cheapest overall is a HOME market (30) but the miner must skip it and take
			// the cheapest FOREIGN market (X1-FOREIGN-B1 @ 40).
			"IRON_ORE": {
				{WaypointSymbol: "X1-HOME-A9", SellPrice: 30},
				{WaypointSymbol: "X1-FOREIGN-B1", SellPrice: 40},
				{WaypointSymbol: "X1-FOREIGN-C1", SellPrice: 55},
			},
			"GOLD":       {{WaypointSymbol: "X1-GALA-G1", SellPrice: 100}},
			"COPPER_ORE": {{WaypointSymbol: "X1-ORE-C1", SellPrice: 20}},
			// NICKEL sells only in the home system => no foreign source => dropped.
			"NICKEL": {{WaypointSymbol: "X1-HOME-N1", SellPrice: 15}},
		},
		homeByGood: map[string]*market.CheapestMarketResult{
			"IRON_ORE": {WaypointSymbol: "X1-HOME-A9", SellPrice: 90}, // savings 50 => eligible
			"GOLD":     {WaypointSymbol: "X1-HOME-G9", SellPrice: 80}, // savings -20 => not eligible
			// COPPER_ORE: home does not sell it (nil) => retained, not eligible.
		},
	}
	return src, markets
}

func TestMineContractDemandJoinsRanksAndFailsClosed(t *testing.T) {
	src, markets := newMinerFixture()
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-HOME", 7, nil, DemandMinerOptions{MinRecurrence: 2})
	require.NoError(t, err)

	// Home-scoping is pushed into the demand source.
	require.NotNil(t, src.gotDeliverySystem)
	require.Equal(t, "X1-HOME", *src.gotDeliverySystem)

	// TIN dropped (below minRecurrence); NICKEL dropped (no foreign source).
	// Kept: IRON_ORE (eligible), COPPER_ORE (home ask unknown), GOLD (negative savings).
	require.Len(t, got, 3)

	// Eligible row ranks first.
	iron := got[0]
	require.Equal(t, "IRON_ORE", iron.Good)
	require.Equal(t, 3, iron.ContractCount)
	require.Equal(t, 300, iron.DemandUnits)
	require.Equal(t, "X1-FOREIGN-B1", iron.ForeignMarket) // home @30 skipped, cheapest foreign taken
	require.Equal(t, "X1-FOREIGN", iron.ForeignSystem)
	require.Equal(t, 40, iron.ForeignAsk)
	require.True(t, iron.HomeAskKnown)
	require.Equal(t, 90, iron.HomeAsk)
	require.Equal(t, 50, iron.ProjectedSavingsPerUnit)
	require.True(t, iron.StockEligible)
	require.InDelta(t, 4.0, iron.RecurrenceWindowDays, 0.01)

	byGood := map[string]DemandCandidate{}
	for _, c := range got {
		byGood[c.Good] = c
	}

	// COPPER_ORE: foreign known, home ask unknown => retained, flagged not eligible.
	copper := byGood["COPPER_ORE"]
	require.Equal(t, 20, copper.ForeignAsk)
	require.False(t, copper.HomeAskKnown)
	require.Equal(t, 0, copper.HomeAsk)
	require.Equal(t, 0, copper.ProjectedSavingsPerUnit)
	require.False(t, copper.StockEligible)

	// GOLD: home cheaper-to-buy-abroad fails (foreign 100 > home 80) => negative savings, not eligible.
	gold := byGood["GOLD"]
	require.Equal(t, -20, gold.ProjectedSavingsPerUnit)
	require.False(t, gold.StockEligible)

	_, hasTin := byGood["TIN"]
	require.False(t, hasTin)
	_, hasNickel := byGood["NICKEL"]
	require.False(t, hasNickel)

	// Non-eligible ordering: COPPER (total savings 0) ranks above GOLD (total -400).
	require.Equal(t, "COPPER_ORE", got[1].Good)
	require.Equal(t, "GOLD", got[2].Good)
}

func TestMineContractDemandRespectsMinRecurrence(t *testing.T) {
	src, markets := newMinerFixture()
	miner := &DemandMiner{demand: src, markets: markets}

	// Raising the floor to 3 drops GOLD/COPPER/NICKEL (count 2) as well; only IRON_ORE (3) survives.
	got, err := miner.Mine(context.Background(), "X1-HOME", 7, nil, DemandMinerOptions{MinRecurrence: 3})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "IRON_ORE", got[0].Good)
}

func TestMineContractDemandAppliesTopNCap(t *testing.T) {
	src, markets := newMinerFixture()
	miner := &DemandMiner{demand: src, markets: markets}

	got, err := miner.Mine(context.Background(), "X1-HOME", 7, nil, DemandMinerOptions{MinRecurrence: 2, TopN: 2})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "IRON_ORE", got[0].Good) // eligible row retained under the cap
}

func TestMineContractDemandRequiresHomeSystem(t *testing.T) {
	src, markets := newMinerFixture()
	miner := &DemandMiner{demand: src, markets: markets}

	_, err := miner.Mine(context.Background(), "", 7, nil, DemandMinerOptions{})
	require.Error(t, err)
}

package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"gorm.io/gorm"
)

func seedTwoEraHistoryFixture(t *testing.T, db *gorm.DB) {
	t.Helper()

	era1Closed := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	era1Registered := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	era2Registered := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	final1 := int64(1_000_000)

	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1,
		RegisteredAt: &era1Registered, ClosedAt: &era1Closed, FinalCredits: &final1,
	}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "orion", AgentSymbol: "ORION", PlayerID: 2,
		RegisteredAt: &era2Registered,
	}).Error)

	scarce := "SCARCE"
	abundant := "ABUNDANT"
	require.NoError(t, db.Create(&persistence.MarketPriceHistoryModel{
		WaypointSymbol: "X1-A1", GoodSymbol: "ADVANCED_CIRCUITRY", PlayerID: 1,
		PurchasePrice: 100, SellPrice: 90, Supply: &scarce, TradeVolume: 5,
		RecordedAt: era1Registered,
	}).Error)
	require.NoError(t, db.Create(&persistence.MarketPriceHistoryModel{
		WaypointSymbol: "X1-A2", GoodSymbol: "ADVANCED_CIRCUITRY", PlayerID: 1,
		PurchasePrice: 120, SellPrice: 110, Supply: &scarce, TradeVolume: 7,
		RecordedAt: era1Registered.Add(time.Hour),
	}).Error)
	require.NoError(t, db.Create(&persistence.MarketPriceHistoryModel{
		WaypointSymbol: "X2-B1", GoodSymbol: "ADVANCED_CIRCUITRY", PlayerID: 2,
		PurchasePrice: 40, SellPrice: 35, Supply: &abundant, TradeVolume: 500,
		RecordedAt: era2Registered,
	}).Error)

	deliveries1 := `[{"TradeSymbol":"IRON_ORE","DestinationSymbol":"X1-A1","UnitsRequired":10,"UnitsFulfilled":10}]`
	deliveries2 := `[{"TradeSymbol":"COPPER_ORE","DestinationSymbol":"X2-B1","UnitsRequired":5,"UnitsFulfilled":0}]`
	require.NoError(t, db.Create(&persistence.ContractModel{
		ID: "c1-1", PlayerID: 1, FactionSymbol: "COSMIC", Type: "PROCUREMENT",
		Accepted: true, Fulfilled: true,
		DeadlineToAccept: "2026-05-01T00:00:00Z", Deadline: "2026-05-03T00:00:00Z",
		PaymentOnAccepted: 1000, PaymentOnFulfilled: 4000,
		DeliveriesJSON: deliveries1, LastUpdated: "2026-05-02T00:00:00Z",
	}).Error)
	require.NoError(t, db.Create(&persistence.ContractModel{
		ID: "c2-1", PlayerID: 2, FactionSymbol: "VOID", Type: "PROCUREMENT",
		Accepted: true, Fulfilled: false,
		DeadlineToAccept: "2026-06-02T00:00:00Z", Deadline: "2026-06-10T00:00:00Z",
		PaymentOnAccepted: 2000, PaymentOnFulfilled: 8000,
		DeliveriesJSON: deliveries2, LastUpdated: "2026-06-02T00:00:00Z",
	}).Error)

	tradeMeta := `{"good_symbol":"IRON_ORE"}`
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t1-1", PlayerID: 1, Timestamp: era1Registered, TransactionType: "SELL_CARGO",
		Category: "TRADING_REVENUE", Amount: 3000, BalanceBefore: 0, BalanceAfter: 3000,
		Metadata: tradeMeta, OperationType: "arbitrage",
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t1-2", PlayerID: 1, Timestamp: era1Registered.Add(24 * time.Hour), TransactionType: "CONTRACT_FULFILLED",
		Category: "CONTRACT_REVENUE", Amount: 4000, BalanceBefore: 3000, BalanceAfter: 7000,
		OperationType: "contract",
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t2-1", PlayerID: 2, Timestamp: era2Registered, TransactionType: "SELL_CARGO",
		Category: "TRADING_REVENUE", Amount: 9_000_000, BalanceBefore: 0, BalanceAfter: 9_000_000,
		OperationType: "arbitrage",
	}).Error)

	require.NoError(t, db.Create(&persistence.CaptainEventModel{
		PlayerID: 1, Type: "CRASH_BURST", Ship: "S1", CreatedAt: era1Registered,
	}).Error)
	require.NoError(t, db.Create(&persistence.CaptainEventModel{
		PlayerID: 2, Type: "CRASH_BURST", Ship: "S2", CreatedAt: era2Registered,
	}).Error)

	require.NoError(t, db.Create(&persistence.ManufacturingPipelineModel{
		ID: "p1-1", PlayerID: 1, ProductGood: "ADVANCED_CIRCUITRY", SellMarket: "X1-A1",
		Status: "COMPLETED", TotalCost: 1000, TotalRevenue: 2000, NetProfit: 1000,
	}).Error)
	require.NoError(t, db.Create(&persistence.ManufacturingPipelineModel{
		ID: "p2-1", PlayerID: 2, ProductGood: "ADVANCED_CIRCUITRY", SellMarket: "X2-B1",
		Status: "FAILED", TotalCost: 500, TotalRevenue: 0, NetProfit: -500,
	}).Error)
}

func TestGoodsStatsIsolatesEachEraAndComputesAggregates(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedTwoEraHistoryFixture(t, db)

	repo := persistence.NewHistoryRepository(db)
	stats, err := repo.GoodsStats(context.Background(), "ADVANCED_CIRCUITRY", nil)
	require.NoError(t, err)
	require.Len(t, stats, 2)

	require.Equal(t, "torwind", stats[0].EraName)
	require.Equal(t, 2, stats[0].MarketCount)
	require.Equal(t, 2, stats[0].SampleCount)
	require.Equal(t, 100.0, stats[0].MedianSellPrice)
	require.Equal(t, 2, stats[0].SupplyDistribution["SCARCE"])
	require.Equal(t, 0, stats[0].SupplyDistribution["ABUNDANT"])

	require.Equal(t, "orion", stats[1].EraName)
	require.Equal(t, 1, stats[1].MarketCount)
	require.Equal(t, 1, stats[1].SupplyDistribution["ABUNDANT"])
	require.Equal(t, 0, stats[1].SupplyDistribution["SCARCE"])
}

func TestGoodsStatsFiltersToSingleEra(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedTwoEraHistoryFixture(t, db)

	repo := persistence.NewHistoryRepository(db)
	eraID := 1
	stats, err := repo.GoodsStats(context.Background(), "ADVANCED_CIRCUITRY", &eraID)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	require.Equal(t, "torwind", stats[0].EraName)
}

func TestContractsStatsComputesPerEraAggregatesWithGoodFilterAndEraIsolation(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedTwoEraHistoryFixture(t, db)

	repo := persistence.NewHistoryRepository(db)
	stats, err := repo.ContractsStats(context.Background(), nil, nil)
	require.NoError(t, err)
	require.Len(t, stats, 2)
	require.Equal(t, 1, stats[0].TotalCount)
	require.Equal(t, 5000.0, stats[0].AvgTotalPayout)
	require.Equal(t, 1.0, stats[0].FulfillmentRate)
	require.Equal(t, 500.0, stats[0].PayoutPerDeliveredUnit)
	require.Equal(t, 1, stats[0].ByGood["IRON_ORE"])
	require.Equal(t, 1, stats[1].TotalCount)
	require.Equal(t, 0.0, stats[1].FulfillmentRate)
	require.Equal(t, 0.0, stats[1].PayoutPerDeliveredUnit)
	require.Equal(t, 1, stats[1].ByGood["COPPER_ORE"])

	good := "IRON_ORE"
	filtered, err := repo.ContractsStats(context.Background(), nil, &good)
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, "torwind", filtered[0].EraName)
	require.Equal(t, 1, filtered[0].ByGood["IRON_ORE"])
}

func TestPnLGroupsByCategoryAndByOperationWithEraIsolation(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedTwoEraHistoryFixture(t, db)

	repo := persistence.NewHistoryRepository(db)
	eraID := 1
	byCategory, err := repo.PnL(context.Background(), &eraID, false)
	require.NoError(t, err)
	require.Equal(t, 7000, byCategory.NetTotal)

	all, err := repo.PnL(context.Background(), nil, false)
	require.NoError(t, err)
	require.Equal(t, 9_007_000, all.NetTotal)

	byOperation, err := repo.PnL(context.Background(), &eraID, true)
	require.NoError(t, err)
	found := map[string]int{}
	for _, b := range byOperation.Breakdown {
		found[b.Key] = b.Net
	}
	require.Equal(t, 3000, found["arbitrage"])
	require.Equal(t, 4000, found["contract"])
}

func TestManufacturingStatsComputesSuccessRateWithEraIsolation(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedTwoEraHistoryFixture(t, db)

	repo := persistence.NewHistoryRepository(db)
	eraID := 1
	stats, err := repo.ManufacturingStats(context.Background(), &eraID, nil)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	require.Equal(t, "ADVANCED_CIRCUITRY", stats[0].Good)
	require.Equal(t, 1.0, stats[0].SuccessRate)
	require.Equal(t, 1000.0, stats[0].AvgNetProfit)

	all, err := repo.ManufacturingStats(context.Background(), nil, nil)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, 2, all[0].Count)
	require.Equal(t, 0.5, all[0].SuccessRate)
}

func TestEventStatsCountsByTypeWithEraIsolation(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedTwoEraHistoryFixture(t, db)

	repo := persistence.NewHistoryRepository(db)
	eraID := 1
	report, err := repo.EventStats(context.Background(), &eraID, nil)
	require.NoError(t, err)
	require.Equal(t, 1, report.Total)
	require.Len(t, report.ByType, 1)
	require.Equal(t, "CRASH_BURST", report.ByType[0].Type)
	require.Equal(t, 1, report.ByType[0].Count)
}

func TestListErasReturnsOrderedRegistryWithDuration(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedTwoEraHistoryFixture(t, db)

	repo := persistence.NewHistoryRepository(db)
	eras, err := repo.ListEras(context.Background())
	require.NoError(t, err)
	require.Len(t, eras, 2)
	require.Equal(t, "torwind", eras[0].Name)
	require.Equal(t, int64(1_000_000), eras[0].FinalCredits)
	require.InDelta(t, 31.0, eras[0].DurationDays, 0.01)
	require.Equal(t, "orion", eras[1].Name)
}

func TestSummaryDefaultsToLatestClosedEra(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedTwoEraHistoryFixture(t, db)

	repo := persistence.NewHistoryRepository(db)
	summary, err := repo.Summary(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "torwind", summary.EraName)
	require.Equal(t, int64(1_000_000), summary.FinalCredits)
	require.Equal(t, 1, summary.ContractCount)
	require.Equal(t, 1.0, summary.ContractFulfillmentRate)
	require.Contains(t, summary.ThinGoods, "ADVANCED_CIRCUITRY")
	require.Len(t, summary.TopGoodsByTradingProfit, 1)
	require.Equal(t, "IRON_ORE", summary.TopGoodsByTradingProfit[0].Good)
}

func TestSummaryUsesExplicitEraWhenGiven(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedTwoEraHistoryFixture(t, db)

	repo := persistence.NewHistoryRepository(db)
	eraID := 2
	summary, err := repo.Summary(context.Background(), &eraID)
	require.NoError(t, err)
	require.Equal(t, "orion", summary.EraName)
	require.NotContains(t, summary.ThinGoods, "ADVANCED_CIRCUITRY")
}

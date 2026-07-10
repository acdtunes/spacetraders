package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-pf60: GormMarketPriceHistoryRepository has round-tripped supply/activity
// since market_price_history's creation (migration 016), but had no
// dedicated repository test - RecordPriceChange/GetPriceHistory's tier
// handling was only ever exercised incidentally through HistoryRepository's
// era-stats tests (history_repository_test.go), which read the model
// directly and never went through modelToHistory. This locks in both
// directions of the actual repository under test.
func TestRecordPriceChangeAndGetPriceHistory_RoundTripsTier(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormMarketPriceHistoryRepository(db)
	seedPlayer(t, db, 1, "TEST-AGENT")

	playerID := shared.MustNewPlayerID(1)
	supply := "LIMITED"
	activity := "WEAK"
	history, err := market.NewMarketPriceHistory(
		"X1-NK36-D39", "MEDICINE", playerID, 1900, 1950, &supply, &activity, 20,
	)
	require.NoError(t, err)

	require.NoError(t, repo.RecordPriceChange(context.Background(), history))

	got, err := repo.GetPriceHistory(context.Background(), "X1-NK36-D39", "MEDICINE", time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].Supply())
	require.Equal(t, "LIMITED", *got[0].Supply())
	require.NotNil(t, got[0].Activity())
	require.Equal(t, "WEAK", *got[0].Activity())
}

// TestGetPriceHistory_LegacyRowsWithNullTierReadBackCleanly covers the
// "backfill not possible" case: rows recorded before a scan ever captured
// tier data (or, historically, any row - since this column has existed since
// the table's creation, this is now purely a "the scan observed no
// supply/activity for this good" case) have NULL supply/activity. The read
// path must tolerate that rather than erroring or fabricating a tier.
func TestGetPriceHistory_LegacyRowsWithNullTierReadBackCleanly(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedPlayer(t, db, 1, "TEST-AGENT")

	require.NoError(t, db.Create(&persistence.MarketPriceHistoryModel{
		WaypointSymbol: "X1-NK36-D39", GoodSymbol: "MEDICINE", PlayerID: 1,
		PurchasePrice: 1900, SellPrice: 1950, TradeVolume: 20,
		RecordedAt: time.Now(),
	}).Error)

	repo := persistence.NewGormMarketPriceHistoryRepository(db)
	got, err := repo.GetPriceHistory(context.Background(), "X1-NK36-D39", "MEDICINE", time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Nil(t, got[0].Supply())
	require.Nil(t, got[0].Activity())
}

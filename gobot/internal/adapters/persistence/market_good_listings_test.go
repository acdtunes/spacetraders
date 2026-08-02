package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func seedListingsFixture(t *testing.T) (*persistence.MarketRepositoryGORM, int, int) {
	t.Helper()

	db, err := database.NewTestConnection()
	require.NoError(t, err)
	mine := persistence.PlayerModel{AgentSymbol: "SP-LISTINGS", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&mine).Error)
	rival := persistence.PlayerModel{AgentSymbol: "SP-LIST-RIVAL", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&rival).Error)

	seed := func(playerID int, waypoint, good, tradeType, supply, activity string) {
		row := persistence.MarketData{
			WaypointSymbol: waypoint, GoodSymbol: good, PurchasePrice: 100, SellPrice: 90,
			TradeVolume: 10, LastUpdated: time.Now(), PlayerID: playerID,
		}
		if tradeType != "" {
			row.TradeType = &tradeType
		}
		if supply != "" {
			row.Supply = &supply
		}
		if activity != "" {
			row.Activity = &activity
		}
		require.NoError(t, db.Create(&row).Error)
	}
	seed(mine.ID, "X1-AA-M1", "CLOTHING", "EXPORT", "HIGH", "STRONG")
	seed(mine.ID, "X1-AA-M2", "CLOTHING", "", "", "")
	seed(mine.ID, "X1-AA-M2", "FUEL", "EXCHANGE", "ABUNDANT", "WEAK")
	seed(mine.ID, "X1-BB-M9", "CLOTHING", "IMPORT", "LIMITED", "GROWING")
	seed(rival.ID, "X1-AA-M7", "CLOTHING", "EXPORT", "ABUNDANT", "STRONG")

	return persistence.NewMarketRepository(db), mine.ID, rival.ID
}

// FindMarketsTradingGood selects no good_symbol column: the good is the caller's input.
// NULL trade_type/supply/activity must come back as empty strings, not a scan error.
func TestMarketRepo_FindMarketsTradingGood(t *testing.T) {
	repo, playerID, _ := seedListingsFixture(t)

	listings, err := repo.FindMarketsTradingGood(context.Background(), "CLOTHING", "X1-AA", playerID)
	require.NoError(t, err)
	require.Len(t, listings, 2)

	byWaypoint := map[string]persistence.MarketGoodListing{}
	for _, l := range listings {
		byWaypoint[l.WaypointSymbol] = l
	}
	require.Equal(t, "EXPORT", byWaypoint["X1-AA-M1"].TradeType)
	require.Equal(t, "HIGH", byWaypoint["X1-AA-M1"].Supply)
	require.Equal(t, "STRONG", byWaypoint["X1-AA-M1"].Activity)
	require.Equal(t, 100, byWaypoint["X1-AA-M1"].PurchasePrice)
	require.Equal(t, 90, byWaypoint["X1-AA-M1"].SellPrice)
	require.Equal(t, 10, byWaypoint["X1-AA-M1"].TradeVolume)
	require.False(t, byWaypoint["X1-AA-M1"].LastUpdated.IsZero())
	require.Empty(t, byWaypoint["X1-AA-M2"].TradeType)
	require.Empty(t, byWaypoint["X1-AA-M2"].Supply)
	require.Empty(t, byWaypoint["X1-AA-M2"].Activity)

	unscoped, err := repo.FindMarketsTradingGood(context.Background(), "CLOTHING", "", playerID)
	require.NoError(t, err)
	require.Len(t, unscoped, 3)
}

func TestMarketRepo_FindAllGoodListingsInSystem(t *testing.T) {
	repo, playerID, _ := seedListingsFixture(t)

	listings, err := repo.FindAllGoodListingsInSystem(context.Background(), "X1-AA", playerID)
	require.NoError(t, err)
	require.Len(t, listings, 3)

	goods := map[string]int{}
	for _, l := range listings {
		goods[l.GoodSymbol]++
		require.Equal(t, 100, l.PurchasePrice)
		require.Equal(t, 90, l.SellPrice)
		require.False(t, l.LastUpdated.IsZero())
	}
	require.Equal(t, map[string]int{"CLOTHING": 2, "FUEL": 1}, goods)
}

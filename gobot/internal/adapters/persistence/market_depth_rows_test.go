package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// MarketDepthRows is the sensing census read: one row per (waypoint, good) for
// THIS player, with the system derived from the waypoint symbol and the
// side-neutral integer mid-price. No filtering happens here — the domain
// whitelist/floor does the filtering — but the player scope is absolute: the
// tables are multi-player, and a competitor's rows would poison the census.
func TestMarketRepo_MarketDepthRows(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	mine := persistence.PlayerModel{AgentSymbol: "SP-DEPTH", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&mine).Error)
	rival := persistence.PlayerModel{AgentSymbol: "SP-RIVAL", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&rival).Error)
	repo := persistence.NewMarketRepository(db)
	ctx := context.Background()

	now := time.Now()
	seed := func(playerID int, waypoint, good string, purchase, sell, tradeVolume int) {
		require.NoError(t, db.Create(&persistence.MarketData{
			WaypointSymbol: waypoint, GoodSymbol: good, PurchasePrice: purchase, SellPrice: sell,
			TradeVolume: tradeVolume, LastUpdated: now, PlayerID: playerID,
		}).Error)
	}
	seed(mine.ID, "X1-AA-M1", "CLOTHING", 100, 120, 10)
	seed(mine.ID, "X1-AA-M1", "FOOD", 7, 10, 20) // odd sum: integer mid truncates to 8
	seed(mine.ID, "X1-BB-M9", "FUEL", 50, 60, 5)
	// Adversarial: the RIVAL player's row carries enormous depth in the same
	// system — if player scoping leaks, this dwarfs everything and the census
	// would declare posts on a competitor's markets.
	seed(rival.ID, "X1-AA-M2", "CLOTHING", 999_999, 999_999, 999_999)

	rows, err := repo.MarketDepthRows(ctx, mine.ID)
	require.NoError(t, err)
	require.Len(t, rows, 3, "exactly this player's three (waypoint,good) rows — the rival's row must not leak in")

	byKey := map[string]domainScouting.MarketDepthRow{}
	for _, row := range rows {
		byKey[row.Waypoint+"/"+row.Good] = row
	}
	require.NotContains(t, byKey, "X1-AA-M2/CLOTHING", "the rival player's row leaked into this player's census")

	clothing := byKey["X1-AA-M1/CLOTHING"]
	require.Equal(t, "X1-AA", clothing.System, "system derived from the waypoint symbol")
	require.Equal(t, 110, clothing.MidPrice, "(100+120)/2")
	require.Equal(t, 10, clothing.TradeVolume)

	food := byKey["X1-AA-M1/FOOD"]
	require.Equal(t, 8, food.MidPrice, "(7+10)/2 truncates: integer mid-price")
	require.Equal(t, 20, food.TradeVolume)

	fuel := byKey["X1-BB-M9/FUEL"]
	require.Equal(t, "X1-BB", fuel.System)
	require.Equal(t, 55, fuel.MidPrice)
	require.Equal(t, 5, fuel.TradeVolume)
}

// A player with no market rows gets an empty census, not an error — the
// cold-start path the coordinator's empty-census fail-safe keys on.
func TestMarketRepo_MarketDepthRows_EmptyForUnknownPlayer(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewMarketRepository(db)

	rows, err := repo.MarketDepthRows(context.Background(), 424242)
	require.NoError(t, err)
	require.Empty(t, rows)
}

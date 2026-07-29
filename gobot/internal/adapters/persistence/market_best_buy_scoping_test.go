package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// FindBestMarketForBuying is a SPEND path: the buy-side chooser must consume
// only THIS player's market rows. The table is multi-player, so the fixture is
// adversarial — the rival player's row in the same system+good is strictly
// BETTER on every scoring axis (EXPORT/ABUNDANT/WEAK vs IMPORT/SCARCE/STRONG).
// If player scoping leaks, the chooser picks the rival's market and this test
// fails; the rival row sits at a different waypoint because the table's
// primary key is (waypoint_symbol, good_symbol).
func TestMarketRepo_FindBestMarketForBuying_ScopedToPlayer(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	mine := persistence.PlayerModel{AgentSymbol: "SP-BUY", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&mine).Error)
	rival := persistence.PlayerModel{AgentSymbol: "SP-BUYRIVAL", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&rival).Error)
	repo := persistence.NewMarketRepository(db)
	ctx := context.Background()

	strPtr := func(s string) *string { return &s }
	// My only market for the good: the worst possible score. purchase_price is the ASK
	// (what we PAY, the larger); sell_price the BID (sp-en5h7).
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-BUY-M1", GoodSymbol: "CLOTHING",
		PurchasePrice: 500, SellPrice: 400, TradeVolume: 10,
		TradeType: strPtr("IMPORT"), Supply: strPtr("SCARCE"), Activity: strPtr("STRONG"),
		LastUpdated: time.Now(), PlayerID: mine.ID,
	}).Error)
	// The rival's market: the best possible score, and a far cheaper ask.
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-BUY-M2", GoodSymbol: "CLOTHING",
		PurchasePrice: 10, SellPrice: 5, TradeVolume: 100,
		TradeType: strPtr("EXPORT"), Supply: strPtr("ABUNDANT"), Activity: strPtr("WEAK"),
		LastUpdated: time.Now(), PlayerID: rival.ID,
	}).Error)

	best, err := repo.FindBestMarketForBuying(ctx, "CLOTHING", "X1-BUY", mine.ID)
	require.NoError(t, err)
	require.NotNil(t, best)
	require.Equal(t, "X1-BUY-M1", best.WaypointSymbol,
		"the chooser consumed the rival player's row — the buy path must see only this player's markets")
	require.Equal(t, 500, best.Ask)
}

// A player with NO cached row for the good must get nil ("not available"),
// never a market some OTHER player scanned: a spend decision may not be made
// on a competitor's observation.
func TestMarketRepo_FindBestMarketForBuying_NoRowsForPlayerIsNil(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	rival := persistence.PlayerModel{AgentSymbol: "SP-ONLYRIVAL", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&rival).Error)
	ghost := persistence.PlayerModel{AgentSymbol: "SP-GHOST", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&ghost).Error)
	repo := persistence.NewMarketRepository(db)

	strPtr := func(s string) *string { return &s }
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: "X1-BUY-M9", GoodSymbol: "FUEL",
		PurchasePrice: 10, SellPrice: 5, TradeVolume: 100,
		TradeType: strPtr("EXPORT"), Supply: strPtr("ABUNDANT"), Activity: strPtr("WEAK"),
		LastUpdated: time.Now(), PlayerID: rival.ID,
	}).Error)

	best, err := repo.FindBestMarketForBuying(context.Background(), "FUEL", "X1-BUY", ghost.ID)
	require.NoError(t, err)
	require.Nil(t, best, "a good this player never observed must read as unavailable, not as the rival's market")
}

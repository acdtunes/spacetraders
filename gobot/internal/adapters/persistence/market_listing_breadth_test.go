package persistence_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// seedListings gives a waypoint a cached market of `listings` distinct goods.
func seedListings(t *testing.T, db *gorm.DB, playerID int, waypoint string, listings int) {
	t.Helper()
	now := time.Now()
	for i := 0; i < listings; i++ {
		require.NoError(t, db.Create(&persistence.MarketData{
			PlayerID: playerID, WaypointSymbol: waypoint, GoodSymbol: fmt.Sprintf("GOOD-%02d", i),
			PurchasePrice: 100, SellPrice: 90, TradeVolume: 20, LastUpdated: now,
		}).Error)
	}
}

func newBreadthPlayer(t *testing.T, db *gorm.DB, symbol string) int {
	t.Helper()
	player := persistence.PlayerModel{AgentSymbol: symbol, Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return player.ID
}

// The signal the pacing prior reads: how many goods a market trades, counted off the cached
// listings, so a deep hub and a single-listing droplet are distinguishable without a new scan.
func TestMarketListingBreadth_CountsTheGoodsAMarketLists(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	playerID := newBreadthPlayer(t, db, "SP-BREADTH-COUNT")
	seedListings(t, db, playerID, "WP-HUB", 12)
	seedListings(t, db, playerID, "WP-DROPLET", 1)

	reader := persistence.NewMarketListingBreadth(db, playerID)

	hub, ok := reader.ListingBreadth(context.Background(), "WP-HUB")
	require.True(t, ok)
	require.Equal(t, 12, hub)

	droplet, ok := reader.ListingBreadth(context.Background(), "WP-DROPLET")
	require.True(t, ok)
	require.Equal(t, 1, droplet)
}

// A market nobody has scanned is UNKNOWN, never zero-and-therefore-thin: the caller reads "not
// ok" as the uniform prior, which is the cautious direction.
func TestMarketListingBreadth_UncachedMarketIsUnknown(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	playerID := newBreadthPlayer(t, db, "SP-BREADTH-UNCACHED")

	got, ok := persistence.NewMarketListingBreadth(db, playerID).ListingBreadth(context.Background(), "WP-NEVER-SCANNED")

	require.False(t, ok, "an uncached market must report unknown so the caller keeps full caution")
	require.Zero(t, got)
}

// PLAYER-SCOPED. Another fleet's cache of the same waypoint may never widen ours — a breadth
// borrowed across players would discount a source this fleet has never seen.
func TestMarketListingBreadth_IsPlayerScoped(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	mine := newBreadthPlayer(t, db, "SP-BREADTH-MINE")
	theirs := newBreadthPlayer(t, db, "SP-BREADTH-THEIRS")
	seedListings(t, db, mine, "WP-SHARED", 2)
	seedListings(t, db, theirs, "WP-SHARED", 30)

	got, ok := persistence.NewMarketListingBreadth(db, mine).ListingBreadth(context.Background(), "WP-SHARED")

	require.True(t, ok)
	require.Equal(t, 2, got, "another player's rows must not count toward this fleet's breadth")
}

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

// SystemsFreshness derives, per system, the market count, worst-case age, and MEASURED
// per-market scan cycle from the market_data scan timestamps — collapsing the per-good
// rows to one scan per market, and reading the cycle as the median gap between consecutive
// market scans. This is the auto-sizer's live census and closed-loop ground truth.
func TestMarketRepo_SystemsFreshness(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-FRESH", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	repo := persistence.NewMarketRepository(db)
	ctx := context.Background()

	now := time.Now()
	// System X1-AA: three markets scanned 120s apart → a measured 120s cycle over 2
	// consecutive-interval samples; the oldest market is ~300s stale. Each market carries
	// TWO goods (two rows) that must collapse to ONE market, proving the count is over
	// distinct WAYPOINTS not rows.
	addMarket := func(waypoint, good string, scannedAt time.Time) {
		require.NoError(t, db.Create(&persistence.MarketData{
			WaypointSymbol: waypoint, GoodSymbol: good, PurchasePrice: 10, SellPrice: 12,
			TradeVolume: 100, LastUpdated: scannedAt, PlayerID: player.ID,
		}).Error)
	}
	addMarket("X1-AA-M1", "FUEL", now.Add(-300*time.Second))
	addMarket("X1-AA-M1", "FOOD", now.Add(-300*time.Second))
	addMarket("X1-AA-M2", "FUEL", now.Add(-180*time.Second))
	addMarket("X1-AA-M2", "FOOD", now.Add(-180*time.Second))
	addMarket("X1-AA-M3", "FUEL", now.Add(-60*time.Second))
	addMarket("X1-AA-M3", "FOOD", now.Add(-60*time.Second))
	// System X1-BB: a single market, no interval measurable yet.
	addMarket("X1-BB-M1", "FUEL", now.Add(-500*time.Second))

	got, err := repo.SystemsFreshness(ctx, player.ID)
	require.NoError(t, err)
	require.Len(t, got, 2, "one snapshot per market-bearing system")

	bySystem := map[string]domainScouting.SystemFreshnessSnapshot{}
	for _, s := range got {
		bySystem[s.SystemSymbol] = s
	}

	aa := bySystem["X1-AA"]
	require.Equal(t, 3, aa.MarketCount, "three distinct markets despite six rows")
	require.Equal(t, 120.0, aa.MeasuredCycleSeconds, "median of the {120,120} consecutive scan intervals")
	require.Equal(t, 2, aa.CycleSamples, "two consecutive-interval samples from three scans")
	require.InDelta(t, 300, aa.OldestAgeSeconds, 5, "worst-case age is the oldest market (~300s)")

	bb := bySystem["X1-BB"]
	require.Equal(t, 1, bb.MarketCount)
	require.Equal(t, 0, bb.CycleSamples, "a single market has no interval to measure")
	require.Equal(t, 0.0, bb.MeasuredCycleSeconds)
	require.InDelta(t, 500, bb.OldestAgeSeconds, 5)
}

// UpdateHulls resizes a live standing post's budget WITHOUT clobbering its manning: the
// assignment and tour columns the scout reconciler wrote survive the resize (sp-orgp).
func TestScoutPostRepo_UpdateHullsPreservesManning(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-RESIZE", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	era := persistence.EraModel{Name: "SP-ERA", AgentSymbol: "SP-RESIZE", PlayerID: player.ID}
	require.NoError(t, db.Create(&era).Error)
	repo := persistence.NewGormScoutPostRepository(db)
	ctx := context.Background()

	post := &domainScouting.ScoutPost{
		PlayerID: player.ID, SystemSymbol: "X1-VB74", Kind: domainScouting.PostKindStanding,
		Hulls: 1, AssignedHull: "SAT-1", TourContainerID: "tour-1",
		FreshnessTarget: time.Hour, CreatedAt: time.Now(),
	}
	require.NoError(t, repo.Upsert(ctx, post))

	require.NoError(t, repo.UpdateHulls(ctx, player.ID, "X1-VB74", 4))

	posts, err := repo.ListActive(ctx, player.ID)
	require.NoError(t, err)
	require.Len(t, posts, 1)
	require.Equal(t, 4, posts[0].Hulls, "the budget is resized")
	require.Equal(t, "SAT-1", posts[0].AssignedHull, "manning is preserved through the resize")
	require.Equal(t, "tour-1", posts[0].TourContainerID, "the running tour is preserved through the resize")
}

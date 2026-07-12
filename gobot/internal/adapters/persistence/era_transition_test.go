package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// TransitionEra is the era-flip half of `universe transition` (sp-nax3): it closes
// the currently-open era and opens a new one for a freshly-registered player WITHOUT
// truncating the player-partitioned market_data / system_graphs caches (migration 032).
func TestTransitionEraClosesOpenAndOpensNewWithoutTruncatingCaches(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	// Prior era: player 1, open, with a final-credits anchor transaction and
	// player-partitioned cache rows that MUST survive the transition.
	require.NoError(t, db.Create(&persistence.PlayerModel{ID: 1, AgentSymbol: "TORWIND", Token: "prior-token", CreatedAt: time.Now()}).Error)
	priorReset := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind-2026-07-05", AgentSymbol: "TORWIND", PlayerID: 1, UniverseResetDate: &priorReset}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{ID: "t1", PlayerID: 1, Timestamp: time.Now(), TransactionType: "SALE", Category: "TRADING", Amount: 500, BalanceBefore: 0, BalanceAfter: 500, CreatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.MarketData{WaypointSymbol: "W1", GoodSymbol: "IRON", PurchasePrice: 1, SellPrice: 1, TradeVolume: 1, LastUpdated: time.Now(), PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.SystemGraphModel{SystemSymbol: "X1-SYS", GraphData: "{}", UpdatedAt: time.Now()}).Error)

	newReset := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	now := time.Now().UTC()
	faction := "COSMIC"
	newPlayer := &persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "new-validated-token", CreatedAt: now}
	newEra := &persistence.EraModel{
		Name:              "torwind-2026-07-12",
		AgentSymbol:       "TORWIND",
		Faction:           &faction,
		RegisteredAt:      &now,
		UniverseResetDate: &newReset,
	}

	repo := persistence.NewEraRepository(db)
	report, err := repo.TransitionEra(context.Background(), newPlayer, newEra)
	require.NoError(t, err)

	// Prior era closed with best-effort final_credits stamped.
	require.NotNil(t, report.ClosedEra)
	require.Equal(t, "torwind-2026-07-05", report.ClosedEra.Name)
	var closed persistence.EraModel
	require.NoError(t, db.Where("name = ?", "torwind-2026-07-05").First(&closed).Error)
	require.NotNil(t, closed.ClosedAt)
	require.NotNil(t, closed.FinalCredits)
	require.Equal(t, int64(500), *closed.FinalCredits)

	// New era opened, linked to the freshly-created player, and now the ONLY open era.
	require.NotZero(t, report.NewPlayerID)
	var opened persistence.EraModel
	require.NoError(t, db.Where("name = ?", "torwind-2026-07-12").First(&opened).Error)
	require.Nil(t, opened.ClosedAt)
	require.Equal(t, report.NewPlayerID, opened.PlayerID)
	require.NotNil(t, opened.UniverseResetDate)
	require.Equal(t, "2026-07-12", opened.UniverseResetDate.Format("2006-01-02"))

	open, err := repo.FindOpenEra(context.Background())
	require.NoError(t, err)
	require.NotNil(t, open)
	require.Equal(t, "torwind-2026-07-12", open.Name)

	// Caches are player-partitioned history — they MUST NOT be truncated (crit 5).
	var marketCount, graphCount int64
	require.NoError(t, db.Model(&persistence.MarketData{}).Count(&marketCount).Error)
	require.NoError(t, db.Model(&persistence.SystemGraphModel{}).Count(&graphCount).Error)
	require.Equal(t, int64(1), marketCount, "market_data must survive the era transition")
	require.Equal(t, int64(1), graphCount, "system_graphs must survive the era transition")

	// The prior player's token is left intact (drain, not a token blank, retires it).
	var prior persistence.PlayerModel
	require.NoError(t, db.First(&prior, 1).Error)
	require.Equal(t, "prior-token", prior.Token)
}

func TestTransitionEraWithNoOpenEraJustOpensNew(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	newReset := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	now := time.Now().UTC()
	newPlayer := &persistence.PlayerModel{AgentSymbol: "ORION", Token: "tok", CreatedAt: now}
	newEra := &persistence.EraModel{Name: "orion-2026-07-12", AgentSymbol: "ORION", RegisteredAt: &now, UniverseResetDate: &newReset}

	repo := persistence.NewEraRepository(db)
	report, err := repo.TransitionEra(context.Background(), newPlayer, newEra)
	require.NoError(t, err)

	require.Nil(t, report.ClosedEra, "no prior open era to close")
	require.NotZero(t, report.NewPlayerID)

	open, err := repo.FindOpenEra(context.Background())
	require.NoError(t, err)
	require.NotNil(t, open)
	require.Equal(t, "orion-2026-07-12", open.Name)
	require.Equal(t, report.NewPlayerID, open.PlayerID)
}

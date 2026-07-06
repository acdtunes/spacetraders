package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	captain "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestCloseEraRefusesWhenEraNotFound(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewEraRepository(db)
	_, err = repo.CloseEra(context.Background(), "ghost")
	require.Error(t, err)
}

func TestCloseEraOnAlreadyClosedEraIsIdempotentNoOp(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	closedAt := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.PlayerModel{ID: 1, AgentSymbol: "TORWIND", Token: "dead-token", CreatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)
	require.NoError(t, db.Create(&persistence.MarketData{WaypointSymbol: "W1", GoodSymbol: "IRON", PurchasePrice: 1, SellPrice: 1, TradeVolume: 1, LastUpdated: time.Now(), PlayerID: 1}).Error)

	repo := persistence.NewEraRepository(db)
	report, err := repo.CloseEra(context.Background(), "torwind")

	require.NoError(t, err)
	require.True(t, report.AlreadyClosed)

	var marketCount int64
	require.NoError(t, db.Model(&persistence.MarketData{}).Count(&marketCount).Error)
	require.Equal(t, int64(1), marketCount)

	var player persistence.PlayerModel
	require.NoError(t, db.First(&player, 1).Error)
	require.Equal(t, "dead-token", player.Token)
}

func TestCloseEraStampsClosureTruncatesCachesBlanksTokenAndBackfillsWaypoints(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	require.NoError(t, db.Create(&persistence.PlayerModel{ID: 1, AgentSymbol: "TORWIND", Token: "dead-token", CreatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1}).Error)

	require.NoError(t, db.Create(&persistence.MarketData{WaypointSymbol: "W1", GoodSymbol: "IRON", PurchasePrice: 1, SellPrice: 1, TradeVolume: 1, LastUpdated: time.Now(), PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.SystemGraphModel{SystemSymbol: "X1-SYS", GraphData: "{}", UpdatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.WaypointModel{WaypointSymbol: "W1", SystemSymbol: "X1-SYS", Type: "PLANET", X: 1, Y: 1}).Error)

	txAmount := 500
	require.NoError(t, db.Create(&persistence.TransactionModel{ID: "t1", PlayerID: 1, Timestamp: time.Now(), TransactionType: "SALE", Category: "TRADING", Amount: txAmount, BalanceBefore: 0, BalanceAfter: txAmount, CreatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.ContractModel{ID: "c1", PlayerID: 1, FactionSymbol: "COSMIC", Type: "PROCUREMENT", DeadlineToAccept: "x", Deadline: "x", DeliveriesJSON: "[]", LastUpdated: "x"}).Error)

	repo := persistence.NewEraRepository(db)
	report, err := repo.CloseEra(context.Background(), "torwind")
	require.NoError(t, err)
	require.False(t, report.AlreadyClosed)

	var era persistence.EraModel
	require.NoError(t, db.Where("name = ?", "torwind").First(&era).Error)
	require.NotNil(t, era.ClosedAt)
	require.NotNil(t, era.FinalCredits)
	require.Equal(t, int64(txAmount), *era.FinalCredits)

	var player persistence.PlayerModel
	require.NoError(t, db.First(&player, 1).Error)
	require.Equal(t, "", player.Token)

	var marketCount, graphCount int64
	require.NoError(t, db.Model(&persistence.MarketData{}).Count(&marketCount).Error)
	require.NoError(t, db.Model(&persistence.SystemGraphModel{}).Count(&graphCount).Error)
	require.Equal(t, int64(0), marketCount)
	require.Equal(t, int64(0), graphCount)

	var wp persistence.WaypointModel
	require.NoError(t, db.Where("waypoint_symbol = ?", "W1").First(&wp).Error)
	require.NotNil(t, wp.EraID)
	require.Equal(t, era.EraID, *wp.EraID)

	var txCount, contractCount int64
	require.NoError(t, db.Model(&persistence.TransactionModel{}).Count(&txCount).Error)
	require.NoError(t, db.Model(&persistence.ContractModel{}).Count(&contractCount).Error)
	require.Equal(t, int64(1), txCount)
	require.Equal(t, int64(1), contractCount)
}

func TestCloseEraFinalCreditsMatchCaptainAnchorSemantics(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	require.NoError(t, db.Create(&persistence.PlayerModel{ID: 7, AgentSymbol: "TORWIND", Token: "t", CreatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 7}).Error)

	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.TransactionModel{ID: "a", PlayerID: 7, Timestamp: base, TransactionType: "SALE", Category: "TRADING", Amount: 100, BalanceAfter: 100, CreatedAt: base}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{ID: "b", PlayerID: 7, Timestamp: base.Add(time.Hour), TransactionType: "CONTRACT_FULFILL", Category: "CONTRACT", Amount: 900, BalanceAfter: 1000, CreatedAt: base.Add(time.Hour)}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{ID: "c", PlayerID: 7, Timestamp: base.Add(2 * time.Hour), TransactionType: "PURCHASE", Category: "FUEL", Amount: -250, BalanceAfter: 750, CreatedAt: base.Add(2 * time.Hour)}).Error)

	expected, err := captain.CurrentCredits(context.Background(), db, 7)
	require.NoError(t, err)

	repo := persistence.NewEraRepository(db)
	report, err := repo.CloseEra(context.Background(), "torwind")
	require.NoError(t, err)
	require.Equal(t, int64(expected), report.FinalCredits)
}

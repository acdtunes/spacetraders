package captainsup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"gorm.io/gorm"
)

func setupDB(t *testing.T) (*gorm.DB, int, *persistence.GormCaptainEventRepository) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := persistence.PlayerModel{AgentSymbol: "AGT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	return db, p.ID, persistence.NewGormCaptainEventRepository(db)
}

func TestDetectStaleHeartbeat(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	stale := now.Add(-10 * time.Minute)
	fresh := now.Add(-1 * time.Minute)
	started := now.Add(-1 * time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "c-stale", PlayerID: playerID, Status: "RUNNING", HeartbeatAt: &stale, StartedAt: &started,
	}).Error)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "c-fresh", PlayerID: playerID, Status: "RUNNING", HeartbeatAt: &fresh, StartedAt: &started,
	}).Error)

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: 5 * time.Minute,
		ShipIdle: time.Hour, CreditsThresholds: nil}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, captain.EventHeartbeatLost, events[0].Type)
	require.Contains(t, events[0].Payload, "c-stale")

	// Running detectors again must not duplicate the event.
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestDetectCreditsThresholdCrossing(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-1", PlayerID: playerID, Timestamp: now, TransactionType: "SELL",
		Category: "TRADING_REVENUE", Amount: 5000, BalanceBefore: 98000, BalanceAfter: 103000,
	}).Error)

	credits, err := CurrentCredits(context.Background(), db, playerID)
	require.NoError(t, err)
	require.Equal(t, 103000, credits)

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		CreditsThresholds: []int{100000, 250000}, LastCredits: 98000}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, captain.EventCreditsThreshold, events[0].Type)
	require.Contains(t, events[0].Payload, "100000")
}

func TestIdleShipCooldownPreventsSessionBurnLoop(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "IDLE-1", PlayerID: playerID, NavStatus: "DOCKED",
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: 30 * time.Minute}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)

	// Processing the event must NOT allow an immediate re-emit.
	require.NoError(t, store.MarkProcessed(context.Background(), []int64{events[0].ID}, now))
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "idle event re-emitted within cooldown: session-burn loop")

	// After the cooldown window the reminder is legitimate again.
	require.NoError(t, db.Exec("UPDATE captain_events SET created_at = ?", now.Add(-2*time.Hour)).Error)
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

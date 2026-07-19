package watchkeeper

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// --- Trading engine command_type scope during tour relaunch churn (sp-lyc3) ---
//
// trade_fleet_coordinator (sp-1278) continuously relaunches a fresh tour_run
// container per idle 'trade'-dedicated hull after every honest tour exit, so
// tour_run/TRADING containers churning through stop/start cycles is the
// fleet's PERMANENT steady state, not an occasional captain-driven burst. The
// 'trading' incomeEngine's activity gate must therefore key on BOTH
// container_type="TRADING" AND commandType="trade_route" — tour_run containers
// share the same container_type as genuine trade_route containers but post
// income under operation_type="tour", never "trade_route", so a
// container_type-only gate reads a healthy tour-only fleet as a stalled trading
// engine.

// TestDetectEngineIncomeStallSilentForTradingDuringTourRelaunchChurn is the
// sp-lyc3 RED case: reproduces the relaunch-churn shape (containers cycling
// through stop/start, ledger flowing under operation_type="tour") with zero
// trade_route containers ever having run. The 'trading' line must never even
// activate.
func TestDetectEngineIncomeStallSilentForTradingDuringTourRelaunchChurn(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	staleStart := now.Add(-3 * time.Hour)
	midStart := now.Add(-90 * time.Minute)
	recentRelaunch := now.Add(-5 * time.Minute)
	stopped1 := now.Add(-100 * time.Minute)
	stopped2 := now.Add(-40 * time.Minute)

	// Two honest-exit relaunch cycles: earlier tour_run containers that ran
	// their course and stopped, each replaced by trade_fleet_coordinator
	// after its cooldown (sp-1278). STOPPED containers never satisfy the
	// gate's "status = RUNNING" clause - included here purely for the churn
	// shape, not because they affect the count.
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "tour-coord-1", PlayerID: playerID, Status: "STOPPED",
		ContainerType: "TRADING", CommandType: "tour_run",
		StartedAt: &staleStart, StoppedAt: &stopped1,
	}).Error)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "tour-coord-2", PlayerID: playerID, Status: "STOPPED",
		ContainerType: "TRADING", CommandType: "tour_run",
		StartedAt: &midStart, StoppedAt: &stopped2,
	}).Error)
	// Still mid-tour, up well past the stall window - shares container_type
	// with a genuine trade_route container but must not satisfy the 'trading'
	// gate.
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "tour-coord-3", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "TRADING", CommandType: "tour_run", StartedAt: &staleStart,
	}).Error)
	// Just relaunched by trade_fleet_coordinator's continuous cooldown cycle -
	// too fresh to satisfy either gate, kept for shape only.
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "tour-coord-4", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "TRADING", CommandType: "tour_run", StartedAt: &recentRelaunch,
	}).Error)

	// Healthy, flowing tour income throughout the churn window.
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-tour-1", PlayerID: playerID, Timestamp: now.Add(-75 * time.Minute),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE", OperationType: "tour",
		Amount: 1030000, BalanceBefore: 100000, BalanceAfter: 1130000,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-tour-2", PlayerID: playerID, Timestamp: now.Add(-45 * time.Minute),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE", OperationType: "tour",
		Amount: 980000, BalanceBefore: 1130000, BalanceAfter: 2110000,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-tour-3", PlayerID: playerID, Timestamp: now.Add(-10 * time.Minute),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE", OperationType: "tour",
		Amount: 1105000, BalanceBefore: 2110000, BalanceAfter: 3215000,
	}).Error)
	// No trade_route container has EVER run in this fixture, and no
	// operation_type="trade_route" income exists either.

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "tour_run relaunch churn must never satisfy the 'trading' engine's activity gate - no trade_route container is running, and tour income is healthy")
}

// TestDetectEngineIncomeStallFiresForTradingWhenGenuineTradeRouteStalls is the
// sp-lyc3 GREEN true-positive case: a real trade_route container is running
// and has earned nothing in-window, WHILE tour_run containers churn healthily
// alongside it. The fix must not blind the 'trading' line to a genuine stall -
// only the false-positive caused by tour_run activity is suppressed. Also
// verifies the existing HasSince dedup window still applies to this line.
func TestDetectEngineIncomeStallFiresForTradingWhenGenuineTradeRouteStalls(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	staleStart := now.Add(-3 * time.Hour)

	// Genuine trade_route container, up well past the window, earning
	// nothing.
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "trade-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "TRADING", CommandType: "trade_route", StartedAt: &staleStart,
	}).Error)
	// A tour_run container churns alongside it, healthy and selling - must
	// not mask or interfere with the trading line's independent stall.
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "tour-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "TRADING", CommandType: "tour_run", StartedAt: &staleStart,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-tour", PlayerID: playerID, Timestamp: now.Add(-15 * time.Minute),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE", OperationType: "tour",
		Amount: 1200000, BalanceBefore: 100000, BalanceAfter: 1300000,
	}).Error)
	// No operation_type="trade_route" income anywhere -> the trade route is
	// genuinely dead.

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "the trade_route container earned nothing in-window and must still stall independently, even with tour income healthy alongside it")
	require.Equal(t, captain.EventIncomeStalled, events[0].Type)
	require.Equal(t, "income:trading", events[0].Ship)
	require.Contains(t, events[0].Payload, "trading")

	// Dedup: a second run inside the same IncomeStall window must not re-fire.
	require.NoError(t, store.MarkProcessed(context.Background(), []int64{events[0].ID}, now))
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "re-running inside the IncomeStall dedup window must not re-fire income:trading")
}

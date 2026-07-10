package watchkeeper

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"gorm.io/gorm"
)

// runningFactory seeds a RUNNING goods_factory_coordinator container the way
// StartGoodsFactory persists one: container_type/command_type
// "goods_factory_coordinator", config carrying target_good, id encoding the good.
func runningFactory(t *testing.T, db *gorm.DB, playerID int, id, good string, started time.Time) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: id, PlayerID: playerID, Status: "RUNNING",
		ContainerType: "goods_factory_coordinator", CommandType: "goods_factory_coordinator",
		Config: `{"target_good":"` + good + `","container_id":"` + id + `"}`, StartedAt: &started,
	}).Error)
}

// factorySale seeds a positive ledger row attributed to a factory container the
// way CargoTransactionHandler.recordCargoTransaction does: related_entity_id =
// the factory coordinator's container id (its operation context), amount > 0.
func factorySale(t *testing.T, db *gorm.DB, playerID int, id, containerID string, at time.Time) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: id, PlayerID: playerID, Timestamp: at,
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE", OperationType: "factory_workflow",
		RelatedEntityType: "container", RelatedEntityID: containerID,
		Amount: 50000, BalanceBefore: 100000, BalanceAfter: 150000,
	}).Error)
}

// --- Tour engine line (sp-7vos gap A) ---

// TestDetectEngineIncomeStallFiresForTourWhileTradingHealthy is the tour-fleet
// masking case, RED against pre-sp-7vos code: a tour_run container is up but
// selling nothing, while the trade route beside it keeps TRADING_REVENUE
// flowing. The aggregate detector stays silent (income exists) and the 'trading'
// line stays silent (its trade_route income is healthy) — so before the tour
// line existed, a dead tour fleet produced ZERO income events. It must now stall
// independently.
func TestDetectEngineIncomeStallFiresForTourWhileTradingHealthy(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-3 * time.Hour)

	// Tour container running well past the window. tour_run and trade_route both
	// persist container_type="TRADING"; the command_type is what distinguishes them.
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "tour-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "TRADING", CommandType: "tour_run", StartedAt: &started,
	}).Error)
	// Trade route income keeps both the aggregate AND the 'trading' line healthy
	// (the tour container also satisfies the trading gate on container_type).
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-trade", PlayerID: playerID, Timestamp: now.Add(-30 * time.Minute),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE", OperationType: "trade_route",
		Amount: 50000, BalanceBefore: 100000, BalanceAfter: 150000,
	}).Error)
	// No operation_type="tour" income anywhere -> the tour fleet is dead.

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "trading income is healthy, but the tour line must stall independently")
	require.Equal(t, captain.EventIncomeStalled, events[0].Type)
	require.Equal(t, "income:tour", events[0].Ship)
	require.Contains(t, events[0].Payload, "tour")
}

// TestDetectEngineIncomeStallSilentForTourWhenSelling verifies tour income
// counts: with operation_type="tour" revenue flowing, the tour line stays quiet
// (and the trade_route income keeps the trading line quiet too).
func TestDetectEngineIncomeStallSilentForTourWhenSelling(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-3 * time.Hour)

	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "tour-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "TRADING", CommandType: "tour_run", StartedAt: &started,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-tour", PlayerID: playerID, Timestamp: now.Add(-20 * time.Minute),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE", OperationType: "tour",
		Amount: 40000, BalanceBefore: 100000, BalanceAfter: 140000,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-trade", PlayerID: playerID, Timestamp: now.Add(-20 * time.Minute),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE", OperationType: "trade_route",
		Amount: 30000, BalanceBefore: 140000, BalanceAfter: 170000,
	}).Error)

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "tour income is flowing under operation_type=tour - must not stall")
}

// --- Per-factory granularity (sp-7vos gap B) ---

// TestDetectFactoryIncomeStallFiresForSilentFactoryWhileSiblingsSell is the core
// granularity acceptance criterion: three goods factories run, the MEDICINE one
// is dead, the others sell. The aggregate income detector is HEALTHY (siblings
// keep money flowing), yet the dead factory must be named and its siblings must
// stay silent — the exact masking that hid the real 100-min MEDICINE outage.
func TestDetectFactoryIncomeStallFiresForSilentFactoryWhileSiblingsSell(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-4 * time.Hour)

	runningFactory(t, db, playerID, "goods_factory-MEDICINE-1", "MEDICINE", started)
	runningFactory(t, db, playerID, "goods_factory-FABRICS-1", "FABRICS", started)
	runningFactory(t, db, playerID, "goods_factory-CLOTHING-1", "CLOTHING", started)
	// Siblings sell recently (attributed by container id); MEDICINE sells nothing.
	factorySale(t, db, playerID, "s-fabrics", "goods_factory-FABRICS-1", now.Add(-30*time.Minute))
	factorySale(t, db, playerID, "s-clothing", "goods_factory-CLOTHING-1", now.Add(-30*time.Minute))

	// Aggregate income detection is ON (IncomeStall) and sees healthy income from
	// the siblings, so it must NOT fire; only the per-factory detector should.
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour, FactoryIncomeStall: 3 * time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "aggregate income is healthy (siblings), but the dead MEDICINE factory must stall")
	require.Equal(t, captain.EventIncomeStalled, events[0].Type)
	require.Equal(t, "income:factory:goods_factory-MEDICINE-1", events[0].Ship)
	require.Contains(t, events[0].Payload, "MEDICINE")
	require.Contains(t, events[0].Payload, `"engine":"factory"`)
}

// TestDetectFactoryIncomeStallSilentWhenAllFactoriesEarn: every running factory
// sold within the window -> no stall for any of them.
func TestDetectFactoryIncomeStallSilentWhenAllFactoriesEarn(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-4 * time.Hour)

	runningFactory(t, db, playerID, "goods_factory-MEDICINE-1", "MEDICINE", started)
	runningFactory(t, db, playerID, "goods_factory-FABRICS-1", "FABRICS", started)
	factorySale(t, db, playerID, "s-med", "goods_factory-MEDICINE-1", now.Add(-30*time.Minute))
	factorySale(t, db, playerID, "s-fab", "goods_factory-FABRICS-1", now.Add(-30*time.Minute))

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour, FactoryIncomeStall: 3 * time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "all factories earned within the window - none should stall")
}

// TestDetectFactoryIncomeStallDistinguishesSameGoodFactories is why attribution
// and dedup are per CONTAINER, not per good: two FOOD factories run
// concurrently (a real live configuration), one dead. A good-keyed join would
// let the live FOOD factory's sales mask the dead one; container-id attribution
// names exactly the dead container.
func TestDetectFactoryIncomeStallDistinguishesSameGoodFactories(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-4 * time.Hour)

	runningFactory(t, db, playerID, "goods_factory-FOOD-alive", "FOOD", started)
	runningFactory(t, db, playerID, "goods_factory-FOOD-dead", "FOOD", started)
	factorySale(t, db, playerID, "s-food-alive", "goods_factory-FOOD-alive", now.Add(-20*time.Minute))

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		FactoryIncomeStall: 3 * time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "the dead FOOD factory must not be masked by its same-good sibling's sales")
	require.Equal(t, "income:factory:goods_factory-FOOD-dead", events[0].Ship)
}

// TestDetectFactoryIncomeStallExemptsRecentlyStartedFactory: a factory younger
// than the window has not had a full window to sell, so its silence is not a
// stall (RULINGS #2 - a just-restarted factory must not false-alarm).
func TestDetectFactoryIncomeStallExemptsRecentlyStartedFactory(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-30 * time.Minute) // younger than the 3h window

	runningFactory(t, db, playerID, "goods_factory-MEDICINE-1", "MEDICINE", started)

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		FactoryIncomeStall: 3 * time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "a factory younger than the window must be exempt, not stalled")
}

// TestDetectFactoryIncomeStallWindowIsParametrized proves the window is a knob
// (RULINGS #5): the SAME state (a lone sale 100 minutes ago) reads as healthy
// under a 3h window and as stalled under a 90-minute one.
func TestDetectFactoryIncomeStallWindowIsParametrized(t *testing.T) {
	seed := func(t *testing.T) (*gorm.DB, int, *persistence.GormCaptainEventRepository, time.Time) {
		db, playerID, store := setupDB(t)
		now := time.Now()
		runningFactory(t, db, playerID, "goods_factory-MEDICINE-1", "MEDICINE", now.Add(-5*time.Hour))
		factorySale(t, db, playerID, "s-med", "goods_factory-MEDICINE-1", now.Add(-100*time.Minute))
		return db, playerID, store, now
	}

	// Wide window: the 100-minute-old sale is inside it -> silent.
	db, playerID, store, now := seed(t)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		FactoryIncomeStall: 3 * time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "sale is within the 3h window - must not stall")

	// Tight window: the same sale falls outside it -> stall.
	db2, playerID2, store2, now2 := seed(t)
	cfg2 := DetectorConfig{PlayerID: playerID2, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		FactoryIncomeStall: 90 * time.Minute}
	require.NoError(t, RunDetectors(context.Background(), db2, store2, cfg2, now2))
	events2, err := store2.FindUnprocessed(context.Background(), playerID2, 10)
	require.NoError(t, err)
	require.Len(t, events2, 1, "under a 90-minute window the 100-minute-old sale is stale - must stall")
	require.Equal(t, "income:factory:goods_factory-MEDICINE-1", events2[0].Ship)
}

// TestDetectFactoryIncomeStallDisabledWhenZero: FactoryIncomeStall <= 0 turns the
// detector off entirely, even for a factory that has never earned.
func TestDetectFactoryIncomeStallDisabledWhenZero(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	runningFactory(t, db, playerID, "goods_factory-MEDICINE-1", "MEDICINE", now.Add(-5*time.Hour))

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		FactoryIncomeStall: 0}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "FactoryIncomeStall=0 disables the detector")
}

// TestDetectFactoryIncomeStallCooldownPreventsSessionBurnLoop: a persistently
// dead factory fires once, is suppressed while the interrupt is fresh, and
// re-fires only after the cooldown window elapses (mirrors the sibling income
// detectors' edge-trigger dedup).
func TestDetectFactoryIncomeStallCooldownPreventsSessionBurnLoop(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	runningFactory(t, db, playerID, "goods_factory-MEDICINE-1", "MEDICINE", now.Add(-5*time.Hour))

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		FactoryIncomeStall: 3 * time.Hour}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)

	require.NoError(t, store.MarkProcessed(context.Background(), []int64{events[0].ID}, now))
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "factory income-stall re-emitted within cooldown: session-burn loop")

	require.NoError(t, db.Exec("UPDATE captain_events SET created_at = ?", now.Add(-4*time.Hour)).Error)
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "after the cooldown window elapses the persistent stall must re-fire")
}

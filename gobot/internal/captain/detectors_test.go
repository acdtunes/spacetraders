package watchkeeper

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

func TestStaleHeartbeatExemptsInTransitShipButFiresForFrozen(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	stale := now.Add(-10 * time.Minute)
	started := now.Add(-1 * time.Hour)

	// A slow solar scout: its worker container's heartbeat is stale because the
	// transit leg exceeds the window, but the ship is IN_TRANSIT (position
	// advancing) — proof the workflow is alive, not a real failure. Exempt.
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "c-scout", PlayerID: playerID, Status: "RUNNING",
		Config: `{"ship_symbol":"SCOUT-1"}`, HeartbeatAt: &stale, StartedAt: &started,
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SCOUT-1", PlayerID: playerID, NavStatus: "IN_TRANSIT",
	}).Error)

	// A genuinely dead worker: stale heartbeat AND its ship is frozen (DOCKED).
	// A frozen position is the real death signal — must still fire.
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "c-frozen", PlayerID: playerID, Status: "RUNNING",
		Config: `{"ship_symbol":"FROZEN-1"}`, HeartbeatAt: &stale, StartedAt: &started,
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "FROZEN-1", PlayerID: playerID, NavStatus: "DOCKED",
	}).Error)

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: 5 * time.Minute, ShipIdle: time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "in-transit scout must be exempt; only the frozen worker fires heartbeat_lost")
	require.Equal(t, captain.EventHeartbeatLost, events[0].Type)
	require.Contains(t, events[0].Payload, "c-frozen")
}

// crashLoopEvents returns only the container.crashloop events for a player,
// filtering out the underlying container.crashed rows used to seed the loop.
func crashLoopEvents(t *testing.T, store *persistence.GormCaptainEventRepository, playerID int) []*captain.Event {
	t.Helper()
	all, err := store.FindUnprocessed(context.Background(), playerID, 100)
	require.NoError(t, err)
	var loops []*captain.Event
	for _, e := range all {
		if e.Type == captain.EventContainerCrashLoop {
			loops = append(loops, e)
		}
	}
	return loops
}

func TestCrashLoopEmitsOneInterruptForRapidDeathsNotForSingle(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	ctx := context.Background()

	// Three true deaths of the SAME container inside the window = a crash loop.
	for i := 0; i < 3; i++ {
		require.NoError(t, store.Record(ctx, &captain.Event{
			Type: captain.EventContainerCrashed, PlayerID: playerID,
			Payload: `{"container_id":"c-loop","error":"boom"}`,
		}))
	}
	// A single death of a different container is self-healing — not a loop.
	require.NoError(t, store.Record(ctx, &captain.Event{
		Type: captain.EventContainerCrashed, PlayerID: playerID,
		Payload: `{"container_id":"c-single","error":"blip"}`,
	}))

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		CrashLoopWindow: 30 * time.Minute, CrashLoopThreshold: 3}
	require.NoError(t, RunDetectors(ctx, db, store, cfg, now))

	loops := crashLoopEvents(t, store, playerID)
	require.Len(t, loops, 1, "exactly one crashloop for the looping container; none for the single death")
	require.Equal(t, "c-loop", loops[0].Ship)
	require.Contains(t, loops[0].Payload, "c-loop")

	// Re-running while the deaths are still inside the window must NOT emit a
	// second crashloop — one interrupt per loop, not per death (cooldown).
	require.NoError(t, RunDetectors(ctx, db, store, cfg, now.Add(time.Minute)))
	loops = crashLoopEvents(t, store, playerID)
	require.Len(t, loops, 1, "crashloop re-emitted within cooldown: session-burn loop")
}

func TestDetectCreditsThresholdCrossing(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-1", PlayerID: playerID, Timestamp: now, TransactionType: "SELL",
		Category: "TRADING_REVENUE", Amount: 5000, BalanceBefore: 98000, BalanceAfter: 103000,
	}).Error)

	// CurrentCredits reconstructs 103000 from the ledger; post sp-sk68 D4 the
	// supervisor supplies that value to the detector via CurrentCreditsValue
	// instead of the detector re-deriving it, so both it and the wake gate
	// evaluate the same number.
	credits, err := CurrentCredits(context.Background(), db, playerID)
	require.NoError(t, err)
	require.Equal(t, 103000, credits)

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		CreditsThresholds: []int{100000, 250000}, LastCredits: 98000, CurrentCreditsValue: credits}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, captain.EventCreditsThreshold, events[0].Type)
	require.Contains(t, events[0].Payload, "100000")
}

func TestCurrentCreditsAnchorsToContractIgnoringCorruptBalance(t *testing.T) {
	db, playerID, _ := setupDB(t)
	base := time.Now()
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-contract", PlayerID: playerID, Timestamp: base,
		TransactionType: "CONTRACT_FULFILLED", Category: "CONTRACT_INCOME",
		Amount: 40000, BalanceBefore: 60000, BalanceAfter: 100000,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-sell", PlayerID: playerID, Timestamp: base.Add(time.Minute),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE",
		Amount: 3000, BalanceBefore: 100000, BalanceAfter: 103000,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-corrupt", PlayerID: playerID, Timestamp: base.Add(2 * time.Minute),
		TransactionType: "PURCHASE_CARGO", Category: "TRADING_EXPENSE",
		Amount: -5000, BalanceBefore: 103000, BalanceAfter: 999999999,
	}).Error)

	credits, err := CurrentCredits(context.Background(), db, playerID)
	require.NoError(t, err)
	require.Equal(t, 98000, credits)
}

func TestCurrentCreditsFallsBackToLatestBalanceWithoutContractAnchor(t *testing.T) {
	db, playerID, _ := setupDB(t)
	base := time.Now()
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-1", PlayerID: playerID, Timestamp: base,
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE",
		Amount: 5000, BalanceBefore: 5000, BalanceAfter: 10000,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-2", PlayerID: playerID, Timestamp: base.Add(time.Minute),
		TransactionType: "PURCHASE_CARGO", Category: "TRADING_EXPENSE",
		Amount: -2000, BalanceBefore: 10000, BalanceAfter: 8000,
	}).Error)

	credits, err := CurrentCredits(context.Background(), db, playerID)
	require.NoError(t, err)
	require.Equal(t, 8000, credits)
}

func TestDetectIncomeStallEmitsWhenLedgerFrozen(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-3 * time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "gas-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "gas_coordinator", StartedAt: &started,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, captain.EventIncomeStalled, events[0].Type)

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestDetectIncomeStallSilentWhenIncomeFlowing(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-3 * time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "gas-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "gas_coordinator", StartedAt: &started,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-in", PlayerID: playerID, Timestamp: now.Add(-30 * time.Minute),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE",
		Amount: 1500, BalanceBefore: 1000, BalanceAfter: 2500,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events)
}

// TestDetectEngineIncomeStallFiresForContractWhileTradingHealthy is the sp-2cdu
// acceptance criterion verbatim: kill the contract coordinator with factories/
// trading running -> a contract-line stall fires within one detection window,
// even though the aggregate ledger looks healthy (real 2026-07-09 incident: the
// contract engine flatlined at 42k/4h while TRADING_REVENUE kept flowing at
// +1.13M/4h, and the old aggregate-only detectIncomeStall never fired because
// SOME income existed system-wide).
func TestDetectEngineIncomeStallFiresForContractWhileTradingHealthy(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-3 * time.Hour)

	// Contract engine's coordinator is running, established well past the
	// stall window, but has fulfilled nothing recently.
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "contract-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "CONTRACT_FLEET_COORDINATOR", StartedAt: &started,
	}).Error)
	// Trading engine is running and healthy -> masks the collapse in the
	// aggregate (any amount>0 transaction silences the old detector).
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "trade-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "TRADING", CommandType: "trade_route", StartedAt: &started,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-trade", PlayerID: playerID, Timestamp: now.Add(-30 * time.Minute),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE", OperationType: "trade_route",
		Amount: 50000, BalanceBefore: 100000, BalanceAfter: 150000,
	}).Error)

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "aggregate income is healthy (trading), but the contract line must still stall independently")
	require.Equal(t, captain.EventIncomeStalled, events[0].Type)
	require.Equal(t, "income:contract", events[0].Ship)
	require.Contains(t, events[0].Payload, "contract")
}

// TestDetectEngineIncomeStallSilentForManufacturingViaWorkerPath guards the
// engine's operation_type filter against a normalization mismatch: the
// manufacturing task worker tags its ctx with the RAW operation type
// "manufacturing_worker" (run_manufacturing_task_worker.go), but that raw
// string is never what lands in the transactions table. OperationContext.
// NormalizedOperationType() has an explicit switch case for it
// ("manufacturing_worker" -> "manufacturing"), and cargo_transaction.go
// persists the NORMALIZED value (recordCmd.OperationType =
// opCtx.NormalizedOperationType()) - so every real sale a manufacturing
// worker makes (via ManufacturingSeller.SellCargo, e.g. the COLLECT_SELL
// task type) is recorded with operation_type="manufacturing", never
// "manufacturing_worker". A detector that filters on the raw string would
// never see this income and would cry wolf on every healthy manufacturing
// engine once it has run longer than the stall window - the false-positive
// failure mode, not the false-negative one this bead targets.
func TestDetectEngineIncomeStallSilentForManufacturingViaWorkerPath(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-3 * time.Hour)

	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "mfg-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "MANUFACTURING_COORDINATOR", StartedAt: &started,
	}).Error)
	// The real, normalized operation_type a manufacturing worker's cargo sale
	// persists as - NOT the raw "manufacturing_worker" context string.
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-mfg", PlayerID: playerID, Timestamp: now.Add(-30 * time.Minute),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE", OperationType: "manufacturing",
		Amount: 75000, BalanceBefore: 100000, BalanceAfter: 175000,
	}).Error)

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "manufacturing income is flowing under its real normalized operation_type - must not stall")
}

func TestDetectStreamDownEmitsPerMissingStream(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-3 * time.Hour)
	stopped := now.Add(-45 * time.Minute)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "gas-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "gas_coordinator", StartedAt: &started,
	}).Error)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "mfg-old", PlayerID: playerID, Status: "STOPPED",
		ContainerType: "manufacturing_coordinator", StartedAt: &started, StoppedAt: &stopped,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		StreamDown:      30 * time.Minute,
		ExpectedStreams: []string{"gas_coordinator", "manufacturing_coordinator"}}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, captain.EventStreamDown, events[0].Type)
	require.Contains(t, events[0].Payload, "manufacturing_coordinator")

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestIncomeStallCooldownPreventsSessionBurnLoop(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-3 * time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "gas-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "gas_coordinator", StartedAt: &started,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)

	require.NoError(t, store.MarkProcessed(context.Background(), []int64{events[0].ID}, now))
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "income-stall re-emitted within cooldown: session-burn loop")

	require.NoError(t, db.Exec("UPDATE captain_events SET created_at = ?", now.Add(-4*time.Hour)).Error)
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestStreamDownCooldownPreventsSessionBurnLoop(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-3 * time.Hour)
	stopped := now.Add(-45 * time.Minute)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "gas-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "gas_coordinator", StartedAt: &started,
	}).Error)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "mfg-old", PlayerID: playerID, Status: "STOPPED",
		ContainerType: "manufacturing_coordinator", StartedAt: &started, StoppedAt: &stopped,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		StreamDown:      30 * time.Minute,
		ExpectedStreams: []string{"gas_coordinator", "manufacturing_coordinator"}}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)

	require.NoError(t, store.MarkProcessed(context.Background(), []int64{events[0].ID}, now))
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "stream-down re-emitted within cooldown: session-burn loop")

	require.NoError(t, db.Exec("UPDATE captain_events SET created_at = ?", now.Add(-2*time.Hour)).Error)
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestDetectStreamDownSilentForNeverRunStream(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-3 * time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "gas-coord", PlayerID: playerID, Status: "RUNNING",
		ContainerType: "gas_coordinator", StartedAt: &started,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		StreamDown:      30 * time.Minute,
		ExpectedStreams: []string{"gas_coordinator", "manufacturing_coordinator"}}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "stream.down fired for a stream that never ran: fresh-universe false positive")
}

func TestDetectorsSilentWhenFleetDown(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	started := now.Add(-4 * time.Hour)
	stopped := now.Add(-2 * time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "mfg-old", PlayerID: playerID, Status: "STOPPED",
		ContainerType: "manufacturing_coordinator", StartedAt: &started, StoppedAt: &stopped,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		IncomeStall: 2 * time.Hour, StreamDown: 30 * time.Minute,
		ExpectedStreams: []string{"manufacturing_coordinator"}}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "detectors fired while fleet down: spurious wake")
}

// TestIdleShipEpisodeDedup pins the sp-6g96 replacement for the old rolling-
// time-window cooldown: ship.idle now fires once per CONTINUOUS idle episode
// (state-change dedup), not once per elapsed-time window. The exact matrix
// required by sp-6g96: enter emits, staying does not (no matter how much time
// passes without a real change), and leaving-then-re-entering emits again
// immediately (no matter how LITTLE time has passed) — proving the dedup is
// keyed on state, not the clock.
func TestIdleShipEpisodeDedup(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "IDLE-1", PlayerID: playerID, NavStatus: "DOCKED",
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, IdleEpisodes: &episodeTracker{}}

	// Enter: the first tick while idle emits.
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NoError(t, store.MarkProcessed(context.Background(), []int64{events[0].ID}, now))

	// Stay: the ship never left idle, so it must NOT re-emit even hours later
	// with no state change — the old cooldown WOULD have re-fired here purely
	// because a time window lapsed; that is exactly the behavior sp-6g96 removes.
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(4*time.Hour)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "idle event re-emitted while ship never left idle: not an episode boundary")

	// Leave: a RUNNING container claims the ship — the episode closes.
	claim := persistence.ContainerModel{
		ID: "c-claim", PlayerID: playerID, Status: "RUNNING",
		Config: `{"ship_symbol":"IDLE-1"}`,
	}
	require.NoError(t, db.Create(&claim).Error)
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(5*time.Hour)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "ship claimed by a running container: must not emit idle")

	// Re-enter: the container goes away — the ship is idle again, a NEW
	// episode, so it must emit again immediately regardless of elapsed time.
	require.NoError(t, db.Delete(&claim).Error)
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(5*time.Hour+time.Second)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "re-entering idle after leaving must emit again immediately")
}

func TestStaleHeartbeatCooldownPreventsSessionBurnLoop(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	stale := now.Add(-10 * time.Minute)
	started := now.Add(-1 * time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "c-dead", PlayerID: playerID, Status: "RUNNING", HeartbeatAt: &stale, StartedAt: &started,
	}).Error)
	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: 5 * time.Minute, ShipIdle: time.Hour}

	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)

	// Captain acks the event; the container is still dead on the next poll.
	// The detector must NOT re-emit within the StaleHeartbeat window —
	// heartbeat_lost is interrupt-class, so each re-emit burns a session.
	require.NoError(t, store.MarkProcessed(context.Background(), []int64{events[0].ID}, now))
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(30*time.Second)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Empty(t, events, "heartbeat_lost re-emitted within cooldown: session-burn loop")

	// Past the window it may fire again (still dead = still noteworthy).
	require.NoError(t, db.Exec("UPDATE captain_events SET created_at = ?", now.Add(-6*time.Minute)).Error)
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now.Add(time.Minute)))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

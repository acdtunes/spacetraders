package watchkeeper

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// detectorErrStore fails on the detector-only store paths (HasUnprocessed /
// HasSince) while leaving FindUnprocessed healthy, so RunDetectors errors but
// the wake gate can still read the event batch.
type detectorErrStore struct{}

func (detectorErrStore) Record(context.Context, *captain.Event) error { return nil }
func (detectorErrStore) FindUnprocessed(context.Context, int, int) ([]*captain.Event, error) {
	return nil, nil
}
func (detectorErrStore) MarkProcessed(context.Context, []int64, time.Time) error { return nil }
func (detectorErrStore) HasUnprocessed(context.Context, int, captain.EventType, string) (bool, error) {
	return false, errors.New("detector store down")
}
func (detectorErrStore) HasSince(context.Context, int, captain.EventType, string, time.Time) (bool, error) {
	return false, errors.New("detector store down")
}
func (detectorErrStore) LatestByType(context.Context, int, captain.EventType) (*captain.Event, error) {
	return nil, nil
}

// D4 (1): a transient detector/DB error must NOT abort the whole tick. The old
// `return false, fmt.Errorf("detectors: %w", err)` skipped cadence/interrupt/
// credits wake evaluation entirely, so repeated transient errors could freeze
// wake decisions with only a generic 'tick error' line. Synthetic events are
// best-effort enrichment; wake evaluation must survive them.
func TestTickContinuesToWakeEvaluationWhenDetectorsError(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	// A fake store whose FindUnprocessed ignores the DB, so the wake gate still
	// gets its (empty) batch even after we break the DB out from under the
	// detectors' own queries below.
	sup.store = detectorErrStore{}
	sup.lastSession = time.Now().Add(-2 * time.Hour) // cadence due

	// Break the DB: the detectors' state queries now error (a transient DB
	// failure), which the old code turned into an aborted tick.
	sqlDB, err := sup.db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	out := captureOutput(t, func() {
		ran, err := sup.Tick(context.Background(), time.Now())
		require.NoError(t, err, "a detector error must not abort the tick")
		require.True(t, ran, "wake evaluation still runs and delivers the overdue heartbeat")
	})

	require.Len(t, gw.nudges, 1, "bridgeWake was reached despite the detector error")
	require.Contains(t, out, "detectors error (continuing", "the detector error is logged, not swallowed")
}

// D4 (2): detectCreditsCrossing must use the credits value the gate uses
// (supplied via cfg.CurrentCreditsValue), not re-derive its own with a
// divergent DB read. Seeding a ledger that reconstructs to a DIFFERENT number
// proves the supplied value — not the DB — drives detection.
func TestDetectCreditsCrossingUsesSuppliedValueNotDBReconstruction(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-1", PlayerID: playerID, Timestamp: now, TransactionType: "SELL_CARGO",
		Category: "TRADING_REVENUE", Amount: 1, BalanceBefore: 499999, BalanceAfter: 500000,
	}).Error)

	cfg := DetectorConfig{
		PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		CreditsThresholds:   []int{1100000},
		LastCredits:         1000000,
		CurrentCreditsValue: 1150000,
	}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, captain.EventCreditsThreshold, events[0].Type)
	require.Contains(t, events[0].Payload, `"direction":"up"`)
	require.Contains(t, events[0].Payload, `"credits":1150000`,
		"the crossing event carries the supplied live value, not the 500,000 DB reconstruction")
}

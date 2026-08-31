package persistence_test

// Integration tests (real GORM/sqlite) for the durable side of the unreadable-hull repair.
// The behaviour that matters is not round-tripping rows: it is that the attempt bound
// SURVIVES, since a bound that resets is not a bound and the repair spends credits.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/hullrepair"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func newUnreadableHullLedger(t *testing.T) *persistence.UnreadableHullLedger {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	return persistence.NewUnreadableHullLedger(db)
}

func TestUnreadableHullLedger_OpensAndClosesAnEpisode(t *testing.T) {
	ledger := newUnreadableHullLedger(t)
	ctx := context.Background()
	at := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, ledger.Observe(ctx, 10, "SHIP-1", at))

	due, err := ledger.Due(ctx, 10, at)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, "SHIP-1", due[0].ShipSymbol)
	require.Equal(t, at.UTC(), due[0].FirstSeenAt.UTC())

	require.NoError(t, ledger.Clear(ctx, 10, "SHIP-1"))
	due, err = ledger.Due(ctx, 10, at)
	require.NoError(t, err)
	require.Empty(t, due)
}

// Re-observing is what every fleet read does; it must never hand a doomed repair a fresh
// attempt budget.
func TestUnreadableHullLedger_ReObservingKeepsTheAttemptBound(t *testing.T) {
	ledger := newUnreadableHullLedger(t)
	ctx := context.Background()
	at := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, ledger.Observe(ctx, 10, "SHIP-1", at))
	require.NoError(t, ledger.Save(ctx, hullrepair.Record{
		PlayerID: 10, ShipSymbol: "SHIP-1", Attempts: 2,
		NextAttemptAt: at.Add(time.Hour), LastOutcome: "write_failed",
	}))

	require.NoError(t, ledger.Observe(ctx, 10, "SHIP-1", at.Add(time.Minute)))

	rec, found, err := ledger.Find(ctx, 10, "SHIP-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 2, rec.Attempts)
	require.Equal(t, at.UTC(), rec.FirstSeenAt.UTC(), "the episode's age drives the stall deadline and must not move")
}

func TestUnreadableHullLedger_BackoffAndEscalationHideAnEpisodeFromTheSweep(t *testing.T) {
	ledger := newUnreadableHullLedger(t)
	ctx := context.Background()
	at := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, ledger.Observe(ctx, 10, "DEFERRED", at))
	require.NoError(t, ledger.Save(ctx, hullrepair.Record{
		PlayerID: 10, ShipSymbol: "DEFERRED", Attempts: 1, NextAttemptAt: at.Add(time.Hour),
	}))
	require.NoError(t, ledger.Observe(ctx, 10, "ESCALATED", at))
	escalatedAt := at
	require.NoError(t, ledger.Save(ctx, hullrepair.Record{
		PlayerID: 10, ShipSymbol: "ESCALATED", Attempts: 3, NextAttemptAt: at, EscalatedAt: &escalatedAt,
	}))

	due, err := ledger.Due(ctx, 10, at.Add(time.Minute))
	require.NoError(t, err)
	require.Empty(t, due, "neither a deferred nor an abandoned episode may be worked")

	due, err = ledger.Due(ctx, 10, at.Add(2*time.Hour))
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, "DEFERRED", due[0].ShipSymbol, "an abandoned episode is never picked up again by the clock")

	open, err := ledger.ListOpen(ctx, 10)
	require.NoError(t, err)
	require.Len(t, open, 2, "but it stays visible: it is what an operator has to act on")
}

func TestUnreadableHullLedger_IsScopedPerPlayer(t *testing.T) {
	ledger := newUnreadableHullLedger(t)
	ctx := context.Background()
	at := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, ledger.Observe(ctx, 10, "SHIP-1", at))
	require.NoError(t, ledger.Observe(ctx, 11, "SHIP-1", at))

	due, err := ledger.Due(ctx, 11, at)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, 11, due[0].PlayerID)
}

func TestUnreadableHullLedger_FindReportsAnAbsentEpisode(t *testing.T) {
	ledger := newUnreadableHullLedger(t)

	_, found, err := ledger.Find(context.Background(), 10, "SHIP-1")
	require.NoError(t, err)
	require.False(t, found)
}

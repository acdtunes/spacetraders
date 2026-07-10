package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// Two recorded legs read back in execution order with every field intact — the
// report's median price-error math depends on planned vs realized surviving the
// round-trip exactly.
func TestTourTelemetryRepository_RecordsAndListsInOrder(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewTourTelemetryRepository(db)
	ctx := context.Background()

	base := time.Date(2026, 7, 9, 22, 0, 0, 0, time.UTC)
	leg0 := trading.TourLegTelemetry{
		TourID: "ctr-tour-1", ShipSymbol: "TORWIND-19", LegIndex: 0,
		Waypoint: "X1-NK36-D39", Good: "MEDICINE", IsBuy: true,
		PlannedUnits: 40, RealizedUnits: 40, PlannedUnitPrice: 1200, RealizedUnitPrice: 1230,
		PlannedAt: base, RealizedAt: base.Add(30 * time.Second), PlayerID: 1,
	}
	leg1 := trading.TourLegTelemetry{
		TourID: "ctr-tour-1", ShipSymbol: "TORWIND-19", LegIndex: 1,
		Waypoint: "X1-GQ92-A1", Good: "MEDICINE", IsBuy: false,
		PlannedUnits: 40, RealizedUnits: 38, PlannedUnitPrice: 1800, RealizedUnitPrice: 1720,
		PlannedAt: base.Add(5 * time.Minute), RealizedAt: base.Add(6 * time.Minute), PlayerID: 1,
	}
	require.NoError(t, repo.RecordLeg(ctx, leg0))
	require.NoError(t, repo.RecordLeg(ctx, leg1))

	rows, err := repo.ListByPlayer(ctx, 1, time.Time{})
	require.NoError(t, err)
	require.Len(t, rows, 2)

	require.Equal(t, 0, rows[0].LegIndex, "legs must read back in execution order")
	require.Equal(t, 1, rows[1].LegIndex)
	require.Equal(t, "X1-NK36-D39", rows[0].Waypoint)
	require.True(t, rows[0].IsBuy)
	require.Equal(t, 1230, rows[0].RealizedUnitPrice)
	require.Equal(t, 38, rows[1].RealizedUnits)
	require.False(t, rows[1].IsBuy)
	require.True(t, rows[1].RealizedAt.Equal(base.Add(6*time.Minute)))
}

// ListByPlayer scopes to the player and honors the since window (the report's
// --since flag bounds the graduation measurement).
func TestTourTelemetryRepository_ScopesByPlayerAndSince(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewTourTelemetryRepository(db)
	ctx := context.Background()

	old := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)

	require.NoError(t, repo.RecordLeg(ctx, trading.TourLegTelemetry{
		TourID: "ctr-old", ShipSymbol: "H1", Waypoint: "W", Good: "G", PlannedAt: old, PlayerID: 1}))
	require.NoError(t, repo.RecordLeg(ctx, trading.TourLegTelemetry{
		TourID: "ctr-new", ShipSymbol: "H1", Waypoint: "W", Good: "G", PlannedAt: recent, PlayerID: 1}))
	require.NoError(t, repo.RecordLeg(ctx, trading.TourLegTelemetry{
		TourID: "ctr-other", ShipSymbol: "H2", Waypoint: "W", Good: "G", PlannedAt: recent, PlayerID: 2}))

	// since excludes the old row; player scoping excludes player 2.
	rows, err := repo.ListByPlayer(ctx, 1, time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "ctr-new", rows[0].TourID)
}

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

// The engine a path declares must survive the round-trip, because that column is the whole
// point of sp-fzt09: it is what lets a SQL reader say `WHERE engine = 'solver'` instead of
// recognising the planner's legs by the accident that theirs are the ones with a non-zero
// planned_unit_price.
//
// All three engines are written in one tour so a mapping that dropped the column, or pinned
// it to a constant, cannot pass by getting one case right.
func TestTourTelemetryRepository_RoundTripsEngine(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewTourTelemetryRepository(db)
	ctx := context.Background()

	base := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	legs := []trading.TourLegTelemetry{
		{
			TourID: "ctr-mixed", ShipSymbol: "H-1", Engine: trading.LegEngineLookback,
			LegIndex: trading.LookbackLegIndex, Waypoint: "X1-A-1", Good: "PARTS", IsBuy: true,
			PlannedUnits: 20, RealizedUnits: 20, PlannedUnitPrice: 100, RealizedUnitPrice: 100,
			PlannedAt: base, RealizedAt: base.Add(time.Second), PlayerID: 5,
		},
		{
			TourID: "ctr-mixed", ShipSymbol: "H-1", Engine: trading.LegEngineSolver,
			LegIndex: 0, Waypoint: "X1-A-2", Good: "FOOD", IsBuy: true,
			PlannedUnits: 60, RealizedUnits: 61, PlannedUnitPrice: 300, RealizedUnitPrice: 302,
			PlannedAt: base.Add(time.Minute), RealizedAt: base.Add(2 * time.Minute), PlayerID: 5,
		},
		{
			TourID: "ctr-mixed", ShipSymbol: "H-1", Engine: trading.LegEngineLiquidation,
			LegIndex: trading.LiquidationLegIndexBase, Waypoint: "X1-A-3", Good: "ORE", IsBuy: false,
			PlannedUnits: 0, RealizedUnits: 63, PlannedUnitPrice: 0, RealizedUnitPrice: 90,
			PlannedAt: base.Add(3 * time.Minute), RealizedAt: base.Add(4 * time.Minute), PlayerID: 5,
		},
	}
	for _, leg := range legs {
		require.NoError(t, repo.RecordLeg(ctx, leg))
	}

	rows, err := repo.ListByPlayer(ctx, 5, time.Time{})
	require.NoError(t, err)
	require.Len(t, rows, len(legs), "every recorded leg must read back")

	for i, want := range legs {
		require.Equal(t, want.Engine, rows[i].Engine,
			"leg %d (%s at index %d) lost its engine on the round-trip", i, want.Good, want.LegIndex)
	}

	// The liquidation row is the one that matters most: it is the row that looked like a
	// solver leg with a missing plan. It must still carry a zero basis (inventing one would
	// corrupt planned-vs-realized) AND say it is a liquidation.
	liquidation := rows[2]
	require.Equal(t, trading.LegEngineLiquidation, liquidation.Engine)
	require.Zero(t, liquidation.PlannedUnitPrice, "a liquidation must not gain a fabricated basis")
	require.Equal(t, 63, liquidation.RealizedUnits, "the realized cargo is real and must persist")
}

// A leg recorded without a declared engine is attributed from its LegIndex class rather than
// stored blank. The acceptance bar is that EVERY leg with realized cargo is attributable; a
// row with an empty engine would be a new unattributable population, which is the exact
// defect this column was added to close.
func TestTourTelemetryRepository_UnsetEngineIsAttributedNotBlank(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewTourTelemetryRepository(db)
	ctx := context.Background()

	base := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		legIdx int
		want   trading.LegEngine
	}{
		{"plan position", 2, trading.LegEngineSolver},
		{"lookback sentinel", trading.LookbackLegIndex, trading.LegEngineLookback},
		{"liquidation base", trading.LiquidationLegIndexBase, trading.LegEngineLiquidation},
		{"liquidation second sink", trading.LiquidationLegIndexBase + 1, trading.LegEngineLiquidation},
	}
	for i, tc := range cases {
		require.NoError(t, repo.RecordLeg(ctx, trading.TourLegTelemetry{
			TourID: "ctr-unset", ShipSymbol: "H-2", LegIndex: tc.legIdx,
			Waypoint: "X1-B-1", Good: "G", RealizedUnits: 10,
			PlannedAt: base.Add(time.Duration(i) * time.Minute), PlayerID: 6,
		}), tc.name)
	}

	rows, err := repo.ListByPlayer(ctx, 6, time.Time{})
	require.NoError(t, err)
	require.Len(t, rows, len(cases))

	for i, tc := range cases {
		require.NotEmpty(t, rows[i].Engine,
			"%s: a leg with realized cargo must never persist unattributable", tc.name)
		require.Equal(t, tc.want, rows[i].Engine, "%s: wrong fallback attribution", tc.name)
	}
}

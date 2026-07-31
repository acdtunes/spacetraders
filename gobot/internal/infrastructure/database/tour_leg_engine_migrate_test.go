package database

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// AutoMigrate must attribute rows that were written before the engine column existed
// (sp-fzt09), or `WHERE engine = 'solver'` silently answers for post-migration rows only —
// which is the same class of bug as the one the column was added to fix, just with a
// different silent exclusion.
//
// This drives the REAL trigger, not just the UPDATE: the column is dropped so
// Migrator().HasColumn reports it absent exactly as it would on a production database that
// has never seen it, and AutoMigrate is then asked to reconcile.
func TestAutoMigrate_BackfillsTourLegEngineForPreExistingRows(t *testing.T) {
	db, err := NewTestConnection()
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}

	// Rows as they existed before the column: their engine lives only in the LegIndex
	// sentinel. The liquidation rows are the population that made a quarter of realized
	// sells unattributable, so both a first and a second sink visit are represented.
	type seed struct {
		legIndex     int
		plannedUnits int
		plannedPrice int
		isBuy        bool
		want         trading.LegEngine
	}
	seeds := []seed{
		{legIndex: 0, plannedUnits: 60, plannedPrice: 300, isBuy: true, want: trading.LegEngineSolver},
		{legIndex: 3, plannedUnits: 51, plannedPrice: 900, isBuy: false, want: trading.LegEngineSolver},
		{legIndex: trading.LookbackLegIndex, plannedUnits: 20, plannedPrice: 100, isBuy: true, want: trading.LegEngineLookback},
		{legIndex: trading.LiquidationLegIndexBase, want: trading.LegEngineLiquidation},
		{legIndex: trading.LiquidationLegIndexBase + 1, want: trading.LegEngineLiquidation},
		// The boundary. An off-by-one in the backfill's `>=` would misfile this last plan
		// position as a liquidation and quietly drop it from planner accuracy.
		{legIndex: trading.LiquidationLegIndexBase - 1, plannedUnits: 40, plannedPrice: 120, isBuy: false, want: trading.LegEngineSolver},
	}
	for i, s := range seeds {
		row := persistence.TourLegTelemetryModel{
			TourID: "ctr-historic", ShipSymbol: "H-OLD", LegIndex: s.legIndex,
			Waypoint: "X1-Z-1", Good: "G", IsBuy: s.isBuy,
			PlannedUnits: s.plannedUnits, RealizedUnits: 63,
			PlannedUnitPrice: s.plannedPrice, RealizedUnitPrice: 95,
			PlayerID: 5,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
		seeds[i].legIndex = s.legIndex // keep index stable for the assertion loop
	}

	// Return the table to its pre-column shape.
	if err := db.Migrator().DropColumn(&persistence.TourLegTelemetryModel{}, "engine"); err != nil {
		t.Fatalf("drop engine column to simulate a pre-migration database: %v", err)
	}
	if db.Migrator().HasColumn(&persistence.TourLegTelemetryModel{}, "engine") {
		t.Fatal("premise broken — the engine column is still present, so AutoMigrate would skip the backfill and this test would pass vacuously")
	}

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	var rows []persistence.TourLegTelemetryModel
	if err := db.Where("tour_id = ?", "ctr-historic").Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(rows) != len(seeds) {
		t.Fatalf("premise broken — expected %d seeded rows to survive the migration, got %d", len(seeds), len(rows))
	}

	for i, s := range seeds {
		if rows[i].Engine == "" {
			t.Errorf("row %d (leg_index=%d): engine still empty after backfill — a historical leg with "+
				"realized cargo is unattributable, which is the defect this column closes", i, s.legIndex)
			continue
		}
		if got := trading.LegEngine(rows[i].Engine); got != s.want {
			t.Errorf("row %d (leg_index=%d): engine = %q, want %q", i, s.legIndex, got, s.want)
		}
	}

	// The falsifier the bead states: planner accuracy must be selectable WITHOUT filtering on
	// planned_unit_price. Asserted as a query, because that is the form the analysis takes.
	var solverCount int64
	if err := db.Model(&persistence.TourLegTelemetryModel{}).
		Where("tour_id = ? AND engine = ?", "ctr-historic", string(trading.LegEngineSolver)).
		Count(&solverCount).Error; err != nil {
		t.Fatalf("count solver legs: %v", err)
	}
	if solverCount != 3 {
		t.Errorf("`WHERE engine = 'solver'` selected %d legs, want 3 — the attribution query is the acceptance bar", solverCount)
	}

	// And no realized leg may be left unattributable.
	var unattributed int64
	if err := db.Model(&persistence.TourLegTelemetryModel{}).
		Where("realized_units > 0 AND (engine IS NULL OR engine = '')").
		Count(&unattributed).Error; err != nil {
		t.Fatalf("count unattributed legs: %v", err)
	}
	if unattributed != 0 {
		t.Errorf("%d legs with realized cargo have no engine", unattributed)
	}
}

// The backfill must not overwrite an engine a path already declared. It runs on the
// migration that adds the column, but a re-run after a partial failure must resume rather
// than reclassify — and a declared engine outranks anything derived from a leg index.
func TestBackfillTourLegEngine_LeavesDeclaredEngineAlone(t *testing.T) {
	db, err := NewTestConnection()
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}

	// A row whose declared engine deliberately disagrees with its leg-index class: if the
	// backfill derived unconditionally it would rewrite this to solver.
	declared := persistence.TourLegTelemetryModel{
		TourID: "ctr-declared", ShipSymbol: "H-NEW", Engine: string(trading.LegEngineLiquidation),
		LegIndex: 2, Waypoint: "X1-Z-2", Good: "G", RealizedUnits: 10, PlayerID: 5,
	}
	blank := persistence.TourLegTelemetryModel{
		TourID: "ctr-declared", ShipSymbol: "H-NEW", Engine: "",
		LegIndex: 2, Waypoint: "X1-Z-2", Good: "G", RealizedUnits: 10, PlayerID: 5,
	}
	if err := db.Create(&declared).Error; err != nil {
		t.Fatalf("seed declared row: %v", err)
	}
	if err := db.Create(&blank).Error; err != nil {
		t.Fatalf("seed blank row: %v", err)
	}

	if err := backfillTourLegEngine(db); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var got persistence.TourLegTelemetryModel
	if err := db.First(&got, declared.ID).Error; err != nil {
		t.Fatalf("read back declared row: %v", err)
	}
	if got.Engine != string(trading.LegEngineLiquidation) {
		t.Errorf("declared engine was overwritten: got %q, want %q — a path that names itself outranks "+
			"a class derived from its leg index", got.Engine, trading.LegEngineLiquidation)
	}

	var filled persistence.TourLegTelemetryModel
	if err := db.First(&filled, blank.ID).Error; err != nil {
		t.Fatalf("read back blank row: %v", err)
	}
	if filled.Engine != string(trading.LegEngineSolver) {
		t.Errorf("blank row at plan position was not filled: got %q, want %q", filled.Engine, trading.LegEngineSolver)
	}
}

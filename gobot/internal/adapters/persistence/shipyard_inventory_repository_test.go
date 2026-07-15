package persistence_test

// Integration tests (real GORM/sqlite, no mocks) for the shipyard-inventory
// store (sp-42ow): upsert semantics — a re-scan refreshes price/last_scanned
// with NO duplicate rows and delisted types disappear — and era scoping — a
// dead era's yards never leak into live reads.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func availability(waypoint, shipType string, price int, supply string) shipyard.ShipTypeAvailability {
	return shipyard.ShipTypeAvailability{
		SystemSymbol:   "X1-AA",
		WaypointSymbol: waypoint,
		ShipType:       shipType,
		PurchasePrice:  price,
		Supply:         supply,
	}
}

// A re-scan REPLACES the waypoint's row set: updated price and last_scanned,
// no duplicate (player, waypoint, ship_type) rows, and a type the yard stopped
// listing disappears.
func TestShipyardInventory_Rescan_UpsertsWithoutDuplicates(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()
	yard := "X1-AA-Y1"

	firstScan := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", yard, []shipyard.ShipTypeAvailability{
		availability(yard, "SHIP_HEAVY_FREIGHTER", 1_200_000, "MODERATE"),
		availability(yard, "SHIP_PROBE", 25_000, "HIGH"),
	}, firstScan))

	secondScan := firstScan.Add(30 * time.Minute)
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", yard, []shipyard.ShipTypeAvailability{
		availability(yard, "SHIP_HEAVY_FREIGHTER", 1_150_000, "HIGH"), // price + supply moved
		// SHIP_PROBE delisted between scans
	}, secondScan))

	rows, err := repo.ListByTypes(ctx, 1, []string{"SHIP_HEAVY_FREIGHTER", "SHIP_PROBE"})
	require.NoError(t, err)
	require.Len(t, rows, 1, "re-scan must not duplicate rows, and a delisted type must disappear")
	require.Equal(t, "SHIP_HEAVY_FREIGHTER", rows[0].ShipType)
	require.Equal(t, 1_150_000, rows[0].PurchasePrice, "re-scan must refresh the price")
	require.Equal(t, "HIGH", rows[0].Supply)
	require.WithinDuration(t, secondScan, rows[0].LastScanned, time.Second, "re-scan must refresh last_scanned")

	has, err := repo.HasAnyOfTypes(ctx, 1, []string{"SHIP_HEAVY_FREIGHTER"})
	require.NoError(t, err)
	require.True(t, has)
}

// Era scoping (the sp-vapw class of bug): a dead era's yard rows must never
// leak into live reads — not into ListByTypes, not into the HasAnyOfTypes
// milestone predicate (or the new era's first discovery would be suppressed by
// a ghost). A re-scan of the same waypoint purges the dead-era rows.
func TestShipyardInventory_DeadEraRows_InvisibleToLiveReads(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	closedAt := time.Now().Add(-24 * time.Hour)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)

	deadEra := 1
	require.NoError(t, db.Create(&persistence.ShipyardInventoryModel{
		PlayerID: 1, SystemSymbol: "X1-AA", WaypointSymbol: "X1-AA-Y1",
		ShipType: "SHIP_BULK_FREIGHTER", PurchasePrice: 2_000_000,
		LastScanned: time.Now(), EraID: &deadEra,
	}).Error)

	// A new era opens.
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()

	rows, err := repo.ListByTypes(ctx, 1, []string{"SHIP_BULK_FREIGHTER"})
	require.NoError(t, err)
	require.Empty(t, rows, "a dead era's yards must not leak into live reads")

	has, err := repo.HasAnyOfTypes(ctx, 1, []string{"SHIP_BULK_FREIGHTER"})
	require.NoError(t, err)
	require.False(t, has, "a dead-era ghost must not suppress the new era's first-discovery milestone")

	// A live re-scan of the same waypoint self-cleans the ghost and reads back.
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		availability("X1-AA-Y1", "SHIP_BULK_FREIGHTER", 1_900_000, "MODERATE"),
	}, time.Now()))
	rows, err = repo.ListByTypes(ctx, 1, []string{"SHIP_BULK_FREIGHTER"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1_900_000, rows[0].PurchasePrice)

	var total int64
	require.NoError(t, db.Model(&persistence.ShipyardInventoryModel{}).Count(&total).Error)
	require.Equal(t, int64(1), total, "the re-scan must have purged the dead-era row, not stacked on it")
}

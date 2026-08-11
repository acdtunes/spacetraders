package persistence_test

// Integration tests (real GORM/sqlite, no mocks) for the shipyard-inventory
// store: upsert semantics — a re-scan refreshes price/last_scanned
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

// Era scoping: a dead era's yard rows must never
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

// ListSavedYards backs the `shipyard yards --type` CLI query: filtered to the
// requested types, era-scoped, ordered by purchase_price ASCENDING (unlike
// ListByTypes, which orders by waypoint/type for deterministic ranking).
func TestShipyardInventory_ListSavedYards_FiltersByTypeOrderedByPriceAscending(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		availability("X1-AA-Y1", "SHIP_HEAVY_FREIGHTER", 1_300_000, "MODERATE"),
		availability("X1-AA-Y1", "SHIP_PROBE", 25_000, "HIGH"), // different type, must be excluded by the filter
	}, time.Now()))
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-BB", "X1-BB-Y1", []shipyard.ShipTypeAvailability{
		{SystemSymbol: "X1-BB", WaypointSymbol: "X1-BB-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 1_150_000, Supply: "HIGH"},
	}, time.Now()))

	rows, err := repo.ListSavedYards(ctx, 1, []string{"SHIP_HEAVY_FREIGHTER"})
	require.NoError(t, err)
	require.Len(t, rows, 2, "only the requested type must be returned")
	require.Equal(t, "X1-BB-Y1", rows[0].WaypointSymbol, "cheaper yard must sort first")
	require.Equal(t, 1_150_000, rows[0].PurchasePrice)
	require.Equal(t, "X1-AA-Y1", rows[1].WaypointSymbol)
	require.Equal(t, 1_300_000, rows[1].PurchasePrice)
}

// The CRITICAL acceptance requirement (Admiral): a yard whose only
// shipyard_inventory rows are from a CLOSED era must never appear in the
// `shipyard yards` result, even though a current-era yard for the same type
// exists — a stale closed-era row would misdirect a live "buy 2 heavies" order.
func TestShipyardInventory_ListSavedYards_ExcludesClosedEraRows(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	closedAt := time.Now().Add(-24 * time.Hour)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)
	closedEra := 1
	require.NoError(t, db.Create(&persistence.ShipyardInventoryModel{
		PlayerID: 1, SystemSymbol: "X1-DEAD", WaypointSymbol: "X1-DEAD-Y1",
		ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 900_000, // cheapest — must NOT win the price sort
		LastScanned: time.Now(), EraID: &closedEra,
	}).Error)

	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		availability("X1-AA-Y1", "SHIP_HEAVY_FREIGHTER", 1_300_000, "MODERATE"),
	}, time.Now()))

	rows, err := repo.ListSavedYards(ctx, 1, []string{"SHIP_HEAVY_FREIGHTER"})
	require.NoError(t, err)
	require.Len(t, rows, 1, "the closed-era yard must not appear alongside the current-era yard")
	require.Equal(t, "X1-AA-Y1", rows[0].WaypointSymbol, "only the current-era yard is present")
}

// No --type filter = every saved current-era ship type for the player, still
// price-ascending across types.
func TestShipyardInventory_ListSavedYards_NoTypeFilterReturnsAllSavedTypes(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		availability("X1-AA-Y1", "SHIP_HEAVY_FREIGHTER", 1_300_000, "MODERATE"),
		availability("X1-AA-Y1", "SHIP_PROBE", 25_000, "HIGH"),
	}, time.Now()))

	rows, err := repo.ListSavedYards(ctx, 1, nil)
	require.NoError(t, err)
	require.Len(t, rows, 2, "an empty type filter must return every saved type")
	require.Equal(t, "SHIP_PROBE", rows[0].ShipType, "cheapest row sorts first regardless of type")
	require.Equal(t, "SHIP_HEAVY_FREIGHTER", rows[1].ShipType)
}

// ScannedSystems returns the DISTINCT open-era systems the player has scanned: one entry per system regardless of how
// many yards/types it holds, and a dead-era scan does NOT count as scanned (so a
// universe reset re-backfills every shipyard this era).
func TestShipyardInventory_ScannedSystems_DistinctAndEraScoped(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	// A dead era whose scanned system must NOT read as scanned in the new era.
	closedAt := time.Now().Add(-24 * time.Hour)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)
	deadEra := 1
	require.NoError(t, db.Create(&persistence.ShipyardInventoryModel{
		PlayerID: 1, SystemSymbol: "X1-DEAD", WaypointSymbol: "X1-DEAD-Y1",
		ShipType: "SHIP_PROBE", PurchasePrice: 25_000, LastScanned: time.Now(), EraID: &deadEra,
	}).Error)

	// The open era.
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()

	// System X1-AA scanned with TWO types (must collapse to one distinct system);
	// system X1-BB scanned with one; a second player's scan must not bleed in.
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		availability("X1-AA-Y1", "SHIP_PROBE", 25_000, "HIGH"),
		availability("X1-AA-Y1", "SHIP_HEAVY_FREIGHTER", 1_200_000, "MODERATE"),
	}, time.Now()))
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-BB", "X1-BB-Y1", []shipyard.ShipTypeAvailability{
		{SystemSymbol: "X1-BB", WaypointSymbol: "X1-BB-Y1", ShipType: "SHIP_PROBE", PurchasePrice: 30_000, Supply: "HIGH"},
	}, time.Now()))
	require.NoError(t, repo.ReplaceScan(ctx, 2, "X1-CC", "X1-CC-Y1", []shipyard.ShipTypeAvailability{
		{SystemSymbol: "X1-CC", WaypointSymbol: "X1-CC-Y1", ShipType: "SHIP_PROBE", PurchasePrice: 30_000, Supply: "HIGH"},
	}, time.Now()))

	systems, err := repo.ScannedSystems(ctx, 1)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"X1-AA", "X1-BB"}, systems,
		"ScannedSystems must return each open-era system once (distinct), excluding dead-era and other-player scans")
}

// THE STORE-WIDE HALF OF THE READ-MODE DISCRIMINATOR. A shipyard quotes an ask and its supply
// together, under presence, and neither without one — so a row where they disagree is a row nobody
// can say how they read. The scanner refuses to write one; this catches one that arrived anyway,
// from a writer that never passed through that check.
func TestShipyardInventory_PriceSupplyMismatches_CatchesBothOffDiagonals(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()
	scannedAt := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)

	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		availability("X1-AA-Y1", "SHIP_HEAVY_FREIGHTER", 1_200_000, "MODERATE"), // priced under presence
		availability("X1-AA-Y1", "SHIP_PROBE", 0, ""),                           // catalogue only
	}, scannedAt))
	clean, err := repo.PriceSupplyMismatches(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, clean, "a store written the only way the API answers must report nothing")

	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-BB", "X1-BB-Y1", []shipyard.ShipTypeAvailability{
		availability("X1-BB-Y1", "SHIP_HEAVY_FREIGHTER", 1_900_000, ""), // an ask nobody could have read
		availability("X1-BB-Y1", "SHIP_PROBE", 0, "ABUNDANT"),           // a supply with no ask beside it
	}, scannedAt))

	found, err := repo.PriceSupplyMismatches(ctx, 1)
	require.NoError(t, err)
	require.Len(t, found, 2, "both off-diagonals must be named, and the honest rows left alone")
	for _, row := range found {
		require.Equal(t, "X1-BB-Y1", row.WaypointSymbol)
	}
}

package persistence_test

// Integration tests (real GORM/sqlite, no mocks) for CheapestPricedYard — the
// price half of the derived heavy reservation. The rules that matter to the
// economics: an unpriced listing must never be reported as the cheapest yard
// (it would collapse the reservation to zero while a real yard sat one row
// away), no known yard means the capability is CLOSED, and a dead era's yard
// never feeds a live buy signal.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func heavyAt(system, waypoint, shipType string, price int) shipyard.ShipTypeAvailability {
	return shipyard.ShipTypeAvailability{
		SystemSymbol:   system,
		WaypointSymbol: waypoint,
		ShipType:       shipType,
		PurchasePrice:  price,
		Supply:         "MODERATE",
	}
}

// No known yard sells a heavy ⇒ found=false ⇒ the capability is CLOSED and the
// reservation is zero.
func TestCheapestPricedYard_NoHeavyRows_CapabilityClosed(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()
	scanned := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	// A yard exists, but sells only probes.
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-AA", "X1-AA-Y1", "SHIP_PROBE", 23_500),
	}, scanned))

	_, found, err := repo.CheapestPricedYard(ctx, 1, shipyard.DefaultHeavyShipTypes)
	require.NoError(t, err)
	require.False(t, found, "no heavy-selling yard ⇒ capability closed")
}

// An unpriced listing (purchase_price 0) proves availability but can never feed
// a money guard. It must be EXCLUDED, and the real priced yard must win — the
// failure this guards is reporting a cheapest price of 0 and reserving nothing.
func TestCheapestPricedYard_ExcludesUnpricedRows(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()
	scanned := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-AA", "X1-AA-Y1", "SHIP_HEAVY_FREIGHTER", 0), // listed, no price
	}, scanned))
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-BB", "X1-BB-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-BB", "X1-BB-Y1", "SHIP_HEAVY_FREIGHTER", 1_300_000),
	}, scanned))

	row, found, err := repo.CheapestPricedYard(ctx, 1, shipyard.DefaultHeavyShipTypes)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "X1-BB-Y1", row.WaypointSymbol, "the unpriced yard must not win on price 0")
	require.Equal(t, 1_300_000, row.PurchasePrice)

	// Availability-only reads still see the unpriced row — the two questions differ.
	has, err := repo.HasAnyOfTypes(ctx, 1, shipyard.DefaultHeavyShipTypes)
	require.NoError(t, err)
	require.True(t, has)
}

// Only unpriced heavy rows exist ⇒ no usable price ⇒ found=false (reserve
// nothing rather than reserve zero-and-claim-open).
func TestCheapestPricedYard_OnlyUnpriced_NotFound(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-AA", "X1-AA-Y1", "SHIP_HEAVY_FREIGHTER", 0),
	}, time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)))

	_, found, err := repo.CheapestPricedYard(ctx, 1, shipyard.DefaultHeavyShipTypes)
	require.NoError(t, err)
	require.False(t, found)
}

// The cheapest priced yard wins across multiple yards, systems and heavy hull
// classes — this is what lets a newly-discovered cheaper yard immediately lower
// the reservation.
func TestCheapestPricedYard_CheapestWinsAcrossYards(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()
	scanned := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-AA", "X1-AA-Y1", "SHIP_HEAVY_FREIGHTER", 1_300_000),
	}, scanned))
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-BB", "X1-BB-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-BB", "X1-BB-Y1", "SHIP_BULK_FREIGHTER", 1_100_000), // cheapest, other heavy class
	}, scanned))
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-CC", "X1-CC-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-CC", "X1-CC-Y1", "SHIP_HEAVY_FREIGHTER", 1_250_000),
	}, scanned))

	row, found, err := repo.CheapestPricedYard(ctx, 1, shipyard.DefaultHeavyShipTypes)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "X1-BB-Y1", row.WaypointSymbol)
	require.Equal(t, "SHIP_BULK_FREIGHTER", row.ShipType, "both heavy classes are candidates")
	require.Equal(t, 1_100_000, row.PurchasePrice)
	require.Equal(t, "X1-BB", row.SystemSymbol)
}

// A dead era's yard must never feed a live buy signal: after the era closes and
// a new one opens, the old cheap yard is invisible.
func TestCheapestPricedYard_DeadEraYardExcluded(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	dead := &persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}
	require.NoError(t, db.Create(dead).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()
	scanned := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-AA", "X1-AA-Y1", "SHIP_HEAVY_FREIGHTER", 900_000),
	}, scanned))

	row, found, err := repo.CheapestPricedYard(ctx, 1, shipyard.DefaultHeavyShipTypes)
	require.NoError(t, err)
	require.True(t, found, "visible while its era is open")
	require.Equal(t, 900_000, row.PurchasePrice)

	// Close that era and open a new one — the reset the era scoping exists for.
	closedAt := scanned.Add(time.Hour)
	require.NoError(t, db.Model(&persistence.EraModel{}).Where("era_id = ?", dead.EraID).
		Update("closed_at", closedAt).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "rigel", AgentSymbol: "RIGEL", PlayerID: 1}).Error)

	_, found, err = repo.CheapestPricedYard(ctx, 1, shipyard.DefaultHeavyShipTypes)
	require.NoError(t, err)
	require.False(t, found, "a dead era's yard must never feed a live buy signal")
}

// An empty ship-type set returns not-found rather than scanning every type: a
// caller that lost its heavy-type set must not reserve against an unrelated hull.
func TestCheapestPricedYard_EmptyTypeSet_NotFound(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-AA", "X1-AA-Y1", "SHIP_PROBE", 23_500),
	}, time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)))

	_, found, err := repo.CheapestPricedYard(ctx, 1, nil)
	require.NoError(t, err)
	require.False(t, found)
}

// Two yards at the same ask resolve deterministically (waypoint tie-break), so
// the reservation does not oscillate between equal-priced yards tick to tick.
func TestCheapestPricedYard_TieBreakIsDeterministic(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()
	scanned := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-ZZ", "X1-ZZ-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-ZZ", "X1-ZZ-Y1", "SHIP_HEAVY_FREIGHTER", 1_000_000),
	}, scanned))
	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-AA", "X1-AA-Y1", "SHIP_HEAVY_FREIGHTER", 1_000_000),
	}, scanned))

	for i := 0; i < 5; i++ {
		row, found, err := repo.CheapestPricedYard(ctx, 1, shipyard.DefaultHeavyShipTypes)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "X1-AA-Y1", row.WaypointSymbol, "equal asks must resolve to the same yard every read")
	}
}

// Another player's cheaper yard must not lower this player's reservation.
func TestCheapestPricedYard_IsPlayerScoped(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewShipyardInventoryRepository(db)
	ctx := context.Background()
	scanned := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	require.NoError(t, repo.ReplaceScan(ctx, 1, "X1-AA", "X1-AA-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-AA", "X1-AA-Y1", "SHIP_HEAVY_FREIGHTER", 1_300_000),
	}, scanned))
	require.NoError(t, repo.ReplaceScan(ctx, 2, "X1-BB", "X1-BB-Y1", []shipyard.ShipTypeAvailability{
		heavyAt("X1-BB", "X1-BB-Y1", "SHIP_HEAVY_FREIGHTER", 400_000),
	}, scanned))

	row, found, err := repo.CheapestPricedYard(ctx, 1, shipyard.DefaultHeavyShipTypes)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 1_300_000, row.PurchasePrice, "another player's yard must not lower this reservation")
}

package grpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func newFuelMarketDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	for _, id := range []int{10, 11} {
		require.NoError(t, db.Create(&persistence.PlayerModel{
			ID: id, AgentSymbol: fmt.Sprintf("AGENT-%d", id), Token: "t", CreatedAt: time.Now(),
		}).Error)
	}
	return db
}

func addWaypoint(t *testing.T, db *gorm.DB, symbol, kind string, hasFuel int) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: symbol, SystemSymbol: "X1-AA", Type: kind, HasFuel: hasFuel,
	}).Error)
}

func addFuelListing(t *testing.T, db *gorm.DB, waypoint string, ask int) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.MarketData{
		PlayerID: 10, WaypointSymbol: waypoint, GoodSymbol: "FUEL",
		PurchasePrice: ask, SellPrice: ask - 1, TradeVolume: 100, LastUpdated: time.Now(),
	}).Error)
}

// purchase_price is the ASK — what a fill actually costs, and so what the guard must bound.
func TestFuelAskReadsTheLocalAsk(t *testing.T) {
	db := newFuelMarketDB(t)
	addWaypoint(t, db, "X1-AA-A1", "PLANET", 1)
	addFuelListing(t, db, "X1-AA-A1", 42)

	price, sells, err := (&hullFuelMarket{db: db}).FuelAsk(context.Background(), 10, "X1-AA-A1")

	require.NoError(t, err)
	require.True(t, sells)
	require.Equal(t, 42, price)
}

// A waypoint that sells no fuel is not a pricing failure; the repair cannot run there and
// must not fly the hull to find some.
func TestFuelAskReportsAWaypointThatSellsNoFuel(t *testing.T) {
	db := newFuelMarketDB(t)
	addWaypoint(t, db, "X1-AA-A2", "ASTEROID", 0)

	price, sells, err := (&hullFuelMarket{db: db}).FuelAsk(context.Background(), 10, "X1-AA-A2")

	require.NoError(t, err)
	require.False(t, sells)
	require.Zero(t, price)
}

// A FUEL_STATION sells fuel whatever the cached bit says.
func TestFuelAskTrustsTheFuelStationType(t *testing.T) {
	db := newFuelMarketDB(t)
	addWaypoint(t, db, "X1-AA-A3", "FUEL_STATION", 0)
	addFuelListing(t, db, "X1-AA-A3", 7)

	_, sells, err := (&hullFuelMarket{db: db}).FuelAsk(context.Background(), 10, "X1-AA-A3")

	require.NoError(t, err)
	require.True(t, sells)
}

// An unscanned waypoint prices off the worst ask the fleet has actually measured, which
// bounds the spend more conservatively than the local price would.
func TestFuelAskFallsBackToTheWorstMeasuredAsk(t *testing.T) {
	db := newFuelMarketDB(t)
	addWaypoint(t, db, "X1-AA-A4", "PLANET", 1)
	addFuelListing(t, db, "X1-AA-B1", 11)
	addFuelListing(t, db, "X1-AA-B2", 99)

	price, sells, err := (&hullFuelMarket{db: db}).FuelAsk(context.Background(), 10, "X1-AA-A4")

	require.NoError(t, err)
	require.True(t, sells)
	require.Equal(t, 99, price)
}

// No fuel price anywhere means the spend cannot be bounded at all, and the repairer reads
// a zero price as a refusal to spend (RULINGS #4).
func TestFuelAskReportsNoPriceAtAll(t *testing.T) {
	db := newFuelMarketDB(t)
	addWaypoint(t, db, "X1-AA-A5", "PLANET", 1)

	price, sells, err := (&hullFuelMarket{db: db}).FuelAsk(context.Background(), 10, "X1-AA-A5")

	require.NoError(t, err)
	require.True(t, sells)
	require.Zero(t, price)
}

// Another player's prices are not ours to spend against.
func TestFuelAskIsScopedPerPlayer(t *testing.T) {
	db := newFuelMarketDB(t)
	addWaypoint(t, db, "X1-AA-A6", "PLANET", 1)
	require.NoError(t, db.Create(&persistence.MarketData{
		PlayerID: 11, WaypointSymbol: "X1-AA-A6", GoodSymbol: "FUEL",
		PurchasePrice: 5, SellPrice: 4, TradeVolume: 100, LastUpdated: time.Now(),
	}).Error)

	price, _, err := (&hullFuelMarket{db: db}).FuelAsk(context.Background(), 10, "X1-AA-A6")

	require.NoError(t, err)
	require.Zero(t, price)
}

func TestTankSizeReadsTheStoredCapacity(t *testing.T) {
	db := newFuelMarketDB(t)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: 10, FuelCapacity: 600,
	}).Error)

	capacity, err := (&hullTankSize{db: db}).FuelCapacity(context.Background(), 10, "SHIP-1")

	require.NoError(t, err)
	require.Equal(t, 600, capacity)
}

// An absent row cannot bound the worst-case fill, and the repairer reads that error as a
// refusal to spend.
func TestTankSizeErrorsWithoutARow(t *testing.T) {
	db := newFuelMarketDB(t)

	_, err := (&hullTankSize{db: db}).FuelCapacity(context.Background(), 10, "SHIP-1")

	require.Error(t, err)
}

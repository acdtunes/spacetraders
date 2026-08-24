package parkedsensing_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// The bulk price snapshot the buy queue ranks counters with: priced probe rows only,
// this player only, the open era only. Everything it lets through is something the
// drain may spend against, so each exclusion below is a spend it cannot make.
func TestProbeAsks_PricedProbeRowsOfThisPlayerOnly(t *testing.T) {
	db := newShipPortsDB(t)
	scanned := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, db.Create(&[]persistence.ShipyardInventoryModel{
		{PlayerID: testPlayerID, SystemSymbol: "X1-AA1", WaypointSymbol: "X1-AA1-Y1",
			ShipType: "SHIP_PROBE", PurchasePrice: 25_000, LastScanned: scanned},
		{PlayerID: testPlayerID, SystemSymbol: "X1-BB2", WaypointSymbol: "X1-BB2-Y1",
			ShipType: "SHIP_PROBE", PurchasePrice: 249_000, LastScanned: scanned},
		// A CATALOGUE-ONLY reading: the yard sells probes, nobody has priced them.
		// Returned as a zero it would rank as the cheapest counter on the map.
		{PlayerID: testPlayerID, SystemSymbol: "X1-CC3", WaypointSymbol: "X1-CC3-Y1",
			ShipType: "SHIP_PROBE", PurchasePrice: 0, LastScanned: scanned},
		{PlayerID: testPlayerID, SystemSymbol: "X1-DD4", WaypointSymbol: "X1-DD4-Y1",
			ShipType: "SHIP_EXPLORER", PurchasePrice: 10, LastScanned: scanned},
		{PlayerID: testPlayerID + 1, SystemSymbol: "X1-EE5", WaypointSymbol: "X1-EE5-Y1",
			ShipType: "SHIP_PROBE", PurchasePrice: 1, LastScanned: scanned},
	}).Error)

	asks, err := newCatalogPort(db).ProbeAsks(context.Background(), testPlayerID)
	require.NoError(t, err)

	byYard := map[string]int64{}
	for _, ask := range asks {
		byYard[ask.Yard] = ask.Price
	}
	require.Equal(t, map[string]int64{"X1-AA1-Y1": 25_000, "X1-BB2-Y1": 249_000}, byYard,
		"only PRICED SHIP_PROBE rows belonging to this player; an unpriced row says the yard "+
			"sells probes, never what it charges, and another player's inventory is not ours")

	require.Len(t, asks, 2)
	for _, ask := range asks {
		require.Equal(t, ask.Yard[:6], ask.System,
			"the system travels with the ask so the caller's reach test never parses a symbol")
		require.WithinDuration(t, scanned, ask.ScannedAt, time.Second,
			"the reading stamp is what makes freshness decidable; without it every row looks current")
	}
}

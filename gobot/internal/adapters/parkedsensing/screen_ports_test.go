package parkedsensing_test

// Integration tests (real GORM/sqlite, no mocks) for the sensing engine's map and
// market reads.
//
// Three of the properties pinned here are ones the engine ABOVE cannot detect on
// its own — a wrong answer produces a working, silent, subtly-wrong fleet rather
// than an error:
//
//   - MarketPrices RENAMES its columns rather than passing them through: Bid comes
//     from sell_price and Ask from purchase_price, because the columns are named
//     from our side of the trade and GoodPrice from the market's. Wire them by
//     name and every quote inverts, the spread weighting flattens, and the
//     rotation simply stops preferring the markets worth watching. This mapping
//     was correct all along, and it is what detected sp-en5h7 — the fault was in
//     the SCANNER, which persisted both prices transposed.
//   - CatalogKnown must be MONOTONE once a system is swept. A flicker to false
//     NULLs the very stamp that proves the sweep, because the screen writes this
//     value back through its column list.
//   - ExistingSlots must genuinely decode whitelist_goods. An empty list restores
//     a per-screen API fetch AND drops remotely-discovered markets out of their
//     own placement plan.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// marketRow writes one persisted quote. The argument names are the PERSISTED
// column meanings — from OUR side of the trade — which is exactly the naming the
// rename in MarketPrices exists to survive. purchasePrice is the ask and must be
// the LARGER of the two; a fixture built the other way round is the sp-en5h7
// corruption, not a market.
func marketRow(waypoint, good string, purchasePrice, sellPrice, volume int) persistence.MarketData {
	return persistence.MarketData{
		WaypointSymbol: waypoint,
		GoodSymbol:     good,
		PurchasePrice:  purchasePrice,
		SellPrice:      sellPrice,
		TradeVolume:    volume,
		LastUpdated:    time.Now().UTC(),
		PlayerID:       testPlayerID,
	}
}

// A normal market charges MORE than it pays (purchase_price > sell_price), so a
// correctly-wired adapter reports Ask > Bid and RelativeSpread sees a POSITIVE
// spread. This is the assertion that fails the moment the columns are wired by
// name instead of by meaning.
func TestMarketPrices_ColumnsAreCrossed_NormalMarketReadsPositive(t *testing.T) {
	db := newShipPortsDB(t)
	// A realistic quote: we buy FOOD at 120, the market buys it back from us at 100.
	require.NoError(t, db.Create(&[]persistence.MarketData{
		marketRow("X1-AA1-M1", "FOOD", 120, 100, 60),
		marketRow("X1-AA1-M1", "MACHINERY", 900, 850, 20),
	}).Error)

	prices, err := adapterSensing.NewMarketGoodsPort(db).MarketPrices(context.Background(), testPlayerID, "X1-AA1-M1")
	require.NoError(t, err)
	require.Len(t, prices, 2)

	food := prices[0]
	require.Equal(t, "FOOD", food.Good)
	require.Equal(t, 100, food.Bid, "Bid is what the market PAYS us — market_data.sell_price")
	require.Equal(t, 120, food.Ask, "Ask is what the market CHARGES us — market_data.purchase_price")
	require.Greater(t, food.Ask, food.Bid, "a real market's ask exceeds its bid")

	spread, inverted := appSensing.RelativeSpread(prices, []string{"FOOD", "MACHINERY"})
	require.Zero(t, inverted, "a correctly-wired adapter produces no impossible quotes")
	require.Positive(t, spread,
		"the weighting sees a real spread; wired uncrossed this is 0 and every market ranks identically")
}

// The mirror image, stated as the failure it detects: had the columns been wired
// by name, THIS is what the engine would have observed.
func TestMarketPrices_UncrossedWouldFlattenTheWeighting(t *testing.T) {
	// The same market, read the wrong way round.
	inverted := []appSensing.GoodPrice{{Good: "FOOD", Bid: 120, Ask: 100}}

	spread, invertedCount := appSensing.RelativeSpread(inverted, []string{"FOOD"})

	require.Equal(t, 1, invertedCount, "every good is rejected as impossible")
	require.Zero(t, spread,
		"and the market observes a spread of ZERO — no error, no crash, just a rotation "+
			"that has stopped preferring anything")
}

// GoodsAt reports whether the cache holds ANY row for the waypoint — which is
// not the same question as "does this market trade anything". A barren market
// and a never-scanned one both have zero rows and are indistinguishable here;
// this test pins the distinction that IS real, populated vs empty. (The old name
// claimed the other one, matching a since-corrected claim on the interface.)
func TestGoodsAt_ReportsWhetherTheCacheHoldsRows(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.MarketData{
		marketRow("X1-AA1-M1", "FOOD", 120, 100, 60),
	}).Error)
	port := adapterSensing.NewMarketGoodsPort(db)

	goods, known, err := port.GoodsAt(context.Background(), testPlayerID, "X1-AA1-M1")
	require.NoError(t, err)
	require.True(t, known)
	require.Equal(t, []string{"FOOD"}, goods)

	// A waypoint with no rows reads as a GAP, and the screen fills it remotely
	// rather than recording a durable rejection it never justified. A genuinely
	// barren market lands here too — the cache cannot tell them apart — which is
	// why the fallback is a fetch and not a verdict.
	_, known, err = port.GoodsAt(context.Background(), testPlayerID, "X1-AA1-M2")
	require.NoError(t, err)
	require.False(t, known)
}

// The market cache is multi-player and a competitor's rows would poison every
// verdict derived from it.
func TestMarketReads_AreStrictlyPlayerScoped(t *testing.T) {
	db := newShipPortsDB(t)
	other := marketRow("X1-AA1-M1", "FOOD", 120, 100, 60)
	other.PlayerID = testPlayerID + 1
	require.NoError(t, db.Create(&other).Error)

	port := adapterSensing.NewMarketGoodsPort(db)
	_, known, err := port.GoodsAt(context.Background(), testPlayerID, "X1-AA1-M1")
	require.NoError(t, err)
	require.False(t, known, "another player's rows are not our knowledge")

	prices, err := port.MarketPrices(context.Background(), testPlayerID, "X1-AA1-M1")
	require.NoError(t, err)
	require.Empty(t, prices)
}

// --- the catalog ----------------------------------------------------------------

func waypointRow(symbol, system string, traits []string) persistence.WaypointModel {
	return typedWaypointRow(symbol, system, "PLANET", traits)
}

// typedWaypointRow writes a waypoint whose TYPE matters. Type is visible without
// charting — only traits are withheld — which is what lets the charting tour
// filter and order by it.
func typedWaypointRow(symbol, system, waypointType string, traits []string) persistence.WaypointModel {
	encoded, _ := json.Marshal(traits)
	return persistence.WaypointModel{
		WaypointSymbol: symbol,
		SystemSymbol:   system,
		Type:           waypointType,
		Traits:         string(encoded),
		SyncedAt:       time.Now().UTC().Format(time.RFC3339),
	}
}

func newCatalogPort(db *gorm.DB) *adapterSensing.WaypointCatalogPort {
	return adapterSensing.NewWaypointCatalogPort(persistence.NewGormWaypointRepository(db), db, testPlayerID)
}

// CatalogKnown answers TRUE on either proof, and neither proof can be un-learned.
//
// The waypoint-rows branch is the one that matters most in practice: it is what
// recognises a system the fleet swept long before this model existed, and it is
// what keeps the answer MONOTONE if the ledger's stamp is ever absent — which is
// load-bearing, because the screen writes this value BACK through a column list
// that includes the stamp. An adapter answering false for a swept system would
// erase its own evidence.
func TestCatalogKnown_TrueOnEitherProof(t *testing.T) {
	t.Run("stamped in the ledger, no waypoint rows yet", func(t *testing.T) {
		db := newShipPortsDB(t)
		stamped := time.Now().UTC()
		require.NoError(t, db.Create(&persistence.SensingSystemModel{
			PlayerID: testPlayerID, SystemSymbol: "X1-STAMPED",
			Verdict: appSensing.VerdictPending, CatalogSyncedAt: &stamped,
		}).Error)

		known, err := newCatalogPort(db).CatalogKnown(context.Background(), "X1-STAMPED")
		require.NoError(t, err)
		require.True(t, known)
	})

	t.Run("waypoint rows exist, ledger never stamped", func(t *testing.T) {
		db := newShipPortsDB(t)
		require.NoError(t, db.Create(&[]persistence.WaypointModel{
			waypointRow("X1-OLD-A1", "X1-OLD", []string{"MARKETPLACE"}),
		}).Error)
		// No sensing_systems row at all — the pre-existing-sweep case.
		known, err := newCatalogPort(db).CatalogKnown(context.Background(), "X1-OLD")
		require.NoError(t, err)
		require.True(t, known,
			"a system the fleet swept before this model existed is known, stamp or no stamp")
	})

	t.Run("a row exists but its stamp is NULL, and waypoints are present", func(t *testing.T) {
		db := newShipPortsDB(t)
		require.NoError(t, db.Create(&persistence.SensingSystemModel{
			PlayerID: testPlayerID, SystemSymbol: "X1-OLD", Verdict: appSensing.VerdictPending,
		}).Error)
		require.NoError(t, db.Create(&[]persistence.WaypointModel{
			waypointRow("X1-OLD-A1", "X1-OLD", nil),
		}).Error)

		known, err := newCatalogPort(db).CatalogKnown(context.Background(), "X1-OLD")
		require.NoError(t, err)
		require.True(t, known,
			"a NULL stamp must not flicker the answer to false — the screen would write that NULL back")
	})
}

// Neither proof: nobody has ever looked at this system. It must read FALSE, or
// the screen records a durable NO_WHITELIST on absent evidence and the system
// becomes a frontier propagation origin, carrying the mistake outward.
func TestCatalogKnown_FalseWhenNobodyHasLooked(t *testing.T) {
	db := newShipPortsDB(t)

	known, err := newCatalogPort(db).CatalogKnown(context.Background(), "X1-DARK")
	require.NoError(t, err)
	require.False(t, known)
}

// ListMarketWaypoints is CHARTED-only. An uncharted waypoint's traits are
// unknown, so including one would send the screen's remote gap fill at a waypoint
// the API cannot answer for — one wasted call per uncharted waypoint per screen.
func TestListMarketWaypoints_ExcludesUncharted(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		waypointRow("X1-AA1-M1", "X1-AA1", []string{"MARKETPLACE"}),
		waypointRow("X1-AA1-M2", "X1-AA1", []string{"MARKETPLACE", "UNCHARTED"}),
		waypointRow("X1-AA1-A1", "X1-AA1", []string{"BARREN"}),
	}).Error)

	markets, err := newCatalogPort(db).ListMarketWaypoints(context.Background(), "X1-AA1")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-AA1-M1"}, markets)
}

func TestListUnchartedCount_AndOrder(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		waypointRow("X1-AA1-C2", "X1-AA1", []string{"UNCHARTED"}),
		waypointRow("X1-AA1-C1", "X1-AA1", []string{"UNCHARTED"}),
		waypointRow("X1-AA1-M1", "X1-AA1", []string{"MARKETPLACE"}),
	}).Error)
	port := newCatalogPort(db)

	count, err := port.ListUnchartedCount(context.Background(), "X1-AA1")
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// Deterministic order is what the tour needs: a seed charts the first waypoint
	// it is handed and re-derives the list next tick, so an unstable order would
	// let it oscillate between two waypoints and never finish.
	order, err := port.UnchartedWaypoints(context.Background(), "X1-AA1")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-AA1-C1", "X1-AA1-C2"}, order)
}

// ListProbeYards is DB-only, priced-first, with an unpriced SHIPYARD fallback.
//
// The DB-only part is load-bearing for the buy queue: the drain treats "no yard
// here" as a free skip costing no attempt against its per-tick cap, explicitly
// because the answer came from local rows. An implementation that reached for the
// API would turn every unbuyable placement into a live call — hardest exactly
// when the API is already degraded.
// liveShapedSystem mirrors the waypoint mix of the systems actually being
// charted: mostly ASTEROID, one ORBITAL_STATION, and a handful of other bodies
// including the lone FUEL_STATION that is the live X1-AJ10 case.
//
// THE STATION IS NAMED LAST ON PURPOSE. Under the old flat alphabetical order
// X1-AA2-Z9 sorted behind every other waypoint here, so an ordering assertion
// against a fixture where the station happened to sort early would pass without
// the priority rule existing at all. Named this way, only a real shipyard-first
// ordering can put it in front.
func liveShapedSystem() []persistence.WaypointModel {
	uncharted := []string{"UNCHARTED"}
	return []persistence.WaypointModel{
		typedWaypointRow("X1-AA2-A1", "X1-AA2", "ASTEROID", uncharted),
		typedWaypointRow("X1-AA2-A2", "X1-AA2", "ASTEROID", uncharted),
		typedWaypointRow("X1-AA2-A3", "X1-AA2", "ASTEROID", uncharted),
		typedWaypointRow("X1-AA2-B1", "X1-AA2", "MOON", uncharted),
		typedWaypointRow("X1-AA2-C1", "X1-AA2", "PLANET", uncharted),
		typedWaypointRow("X1-AA2-D1", "X1-AA2", "GAS_GIANT", uncharted),
		typedWaypointRow("X1-AA2-F1", "X1-AA2", "FUEL_STATION", uncharted),
		typedWaypointRow("X1-AA2-Z9", "X1-AA2", "ORBITAL_STATION", uncharted),
		// Already charted: never charting work, whatever its type.
		typedWaypointRow("X1-AA2-M1", "X1-AA2", "MOON", []string{"MARKETPLACE"}),
	}
}

// THE REORDER, with the whole set still present.
//
// Both halves of this assertion matter and neither is sufficient alone. The
// ORDER is the speedup: the station comes first because a charted yard makes
// the system buyable, then the market types so a parked scanner can be placed
// on them and start producing trade data while the tour continues, then the
// rest. The COMPLETENESS is the safety: all eight uncharted waypoints are still
// handed to the seed, so the map still finishes.
func TestUnchartedWaypoints_OrdersByValueWithoutDroppingAnyWaypoint(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(liveShapedSystem()).Error)

	order, err := newCatalogPort(db).UnchartedWaypoints(context.Background(), "X1-AA2")
	require.NoError(t, err)

	require.Equal(t, []string{
		"X1-AA2-Z9", // ORBITAL_STATION — 523 of the 567 shipyards ever seen
		"X1-AA2-B1", // MOON        \
		"X1-AA2-C1", // PLANET       > market-bearing, alphabetical within the tier
		"X1-AA2-F1", // FUEL_STATION/  (1129 of 1129 carry a market)
		"X1-AA2-D1", // GAS_GIANT — 72 of 546, unproven
		"X1-AA2-A1", // ASTEROID  \
		"X1-AA2-A2", // ASTEROID   > 0 of 3297, so last — but still charted
		"X1-AA2-A3", // ASTEROID  /
	}, order,
		"the station must be visited FIRST despite sorting last alphabetically, and every asteroid must still be in the list")

	require.Len(t, order, 8, "the tour is still EXHAUSTIVE: reordering must never drop a waypoint from it")
}

// A system of nothing but asteroids is still a full tour. Barren is a sorting
// tier, not an exemption — this is the shape of X1-KC84 (51 asteroids) and five
// others, and every one of those waypoints is still charted.
func TestUnchartedWaypoints_AnAllAsteroidSystemIsStillFullyToured(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		typedWaypointRow("X1-AA3-A2", "X1-AA3", "ASTEROID", []string{"UNCHARTED"}),
		typedWaypointRow("X1-AA3-A1", "X1-AA3", "ASTEROID", []string{"UNCHARTED"}),
	}).Error)
	port := newCatalogPort(db)

	order, err := port.UnchartedWaypoints(context.Background(), "X1-AA3")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-AA3-A1", "X1-AA3-A2"}, order,
		"both asteroids are toured, in a deterministic order — the seed still has work here")

	count, err := port.ListUnchartedCount(context.Background(), "X1-AA3")
	require.NoError(t, err)
	require.Equal(t, 2, count, "and the completion signal still counts them")
}

// THE COHERENCE INVARIANT, asserted directly rather than inferred.
//
// ListUnchartedCount is the tour's COMPLETION SIGNAL and UnchartedWaypoints is
// its WORK LIST. The engine treats them as two views of one set: the screen
// stores the count in uncharted_count and verdictFor will not write a system off
// durably until it reads zero, while the tour ends only when the list is empty.
// If the count ever exceeded the list, the tour would finish and the count would
// never reach zero — the system pinned PENDING forever, seedlessTargets sending
// it probes endlessly, and since only IN_SCOPE/NO_WHITELIST systems propagate
// the frontier, expansion stalled there permanently.
//
// A pure reorder cannot break this — the two reads see identical rows and differ
// only in sequence — and that is precisely why it is worth pinning: it is the
// property that would silently die the moment anyone turned this ordering into a
// filter.
func TestUnchartedReads_CountAndWorkListCoverTheSameSet(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(liveShapedSystem()).Error)
	port := newCatalogPort(db)

	order, err := port.UnchartedWaypoints(context.Background(), "X1-AA2")
	require.NoError(t, err)
	count, err := port.ListUnchartedCount(context.Background(), "X1-AA2")
	require.NoError(t, err)

	require.Equal(t, len(order), count,
		"the completion signal must count exactly the waypoints a seed will be sent to, and nothing else")
	require.Equal(t, 8, count, "eight uncharted waypoints, and the tour visits all eight")
}

// The barren tier must match the type EXACTLY. ASTEROID_BASE and
// ENGINEERED_ASTEROID share its prefix and are 100% market-bearing — 345 of 345
// and 22 of 22 — so a prefix test would bury 367 guaranteed markets behind every
// barren rock in the system while still looking like a working ordering.
func TestUnchartedWaypoints_AsteroidPrefixedTypesRankWithTheMarkets(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		// Named so that alphabetical order would put the bare asteroid FIRST.
		typedWaypointRow("X1-AA4-A1", "X1-AA4", "ASTEROID", []string{"UNCHARTED"}),
		typedWaypointRow("X1-AA4-B1", "X1-AA4", "ASTEROID_BASE", []string{"UNCHARTED"}),
		typedWaypointRow("X1-AA4-E1", "X1-AA4", "ENGINEERED_ASTEROID", []string{"UNCHARTED"}),
	}).Error)

	order, err := newCatalogPort(db).UnchartedWaypoints(context.Background(), "X1-AA4")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-AA4-B1", "X1-AA4-E1", "X1-AA4-A1"}, order,
		"only the bare ASTEROID is barren; its two prefix-sharing cousins always carry a market and must be reached first")
}

func TestListProbeYards_PricedFirstThenTraitFallback(t *testing.T) {
	t.Run("priced inventory, cheapest first", func(t *testing.T) {
		db := newShipPortsDB(t)
		require.NoError(t, db.Create(&[]persistence.ShipyardInventoryModel{
			{PlayerID: testPlayerID, SystemSymbol: "X1-AA1", WaypointSymbol: "X1-AA1-Y2",
				ShipType: "SHIP_PROBE", PurchasePrice: 80_000, LastScanned: time.Now().UTC()},
			{PlayerID: testPlayerID, SystemSymbol: "X1-AA1", WaypointSymbol: "X1-AA1-Y1",
				ShipType: "SHIP_PROBE", PurchasePrice: 40_000, LastScanned: time.Now().UTC()},
			{PlayerID: testPlayerID, SystemSymbol: "X1-AA1", WaypointSymbol: "X1-AA1-Y3",
				ShipType: "SHIP_EXPLORER", PurchasePrice: 10, LastScanned: time.Now().UTC()},
		}).Error)

		yards, err := newCatalogPort(db).ListProbeYards(context.Background(), "X1-AA1")
		require.NoError(t, err)
		require.Equal(t, []string{"X1-AA1-Y1", "X1-AA1-Y2"}, yards,
			"probe-selling yards only, cheapest first")
	})

	t.Run("never-scanned shipyard is still a candidate", func(t *testing.T) {
		db := newShipPortsDB(t)
		require.NoError(t, db.Create(&[]persistence.WaypointModel{
			waypointRow("X1-BB2-Y1", "X1-BB2", []string{"SHIPYARD"}),
			waypointRow("X1-BB2-Y9", "X1-BB2", []string{"SHIPYARD", "UNCHARTED"}),
		}).Error)

		yards, err := newCatalogPort(db).ListProbeYards(context.Background(), "X1-BB2")
		require.NoError(t, err)
		require.Equal(t, []string{"X1-BB2-Y1"}, yards,
			"a yard nobody has priced still sells probes; an UNCHARTED one is not yet a yard")
	})

	t.Run("another player's inventory is not ours", func(t *testing.T) {
		db := newShipPortsDB(t)
		require.NoError(t, db.Create(&persistence.ShipyardInventoryModel{
			PlayerID: testPlayerID + 1, SystemSymbol: "X1-CC3", WaypointSymbol: "X1-CC3-Y1",
			ShipType: "SHIP_PROBE", PurchasePrice: 1, LastScanned: time.Now().UTC(),
		}).Error)

		yards, err := newCatalogPort(db).ListProbeYards(context.Background(), "X1-CC3")
		require.NoError(t, err)
		require.Empty(t, yards)
	})
}

// THE PER-WAYPOINT RULE, and the regression this whole change exists for.
//
// A single priced yard used to switch the SHIPYARD-trait fallback off for the
// WHOLE system, so every yard we had not yet priced went missing — not ranked
// last, absent. A yard nothing can see is a counter we can never buy at, because
// buying requires a hull already standing there. Measured live: 81 of 614 charted
// shipyards were invisible to both yard ports for this reason.
//
// Whether Y1 is priced is evidence about Y1. It says nothing about Y2.
func TestListProbeYards_UnpricedYardSurvivesAPricedNeighbour(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		waypointRow("X1-DD4-Y1", "X1-DD4", []string{"SHIPYARD"}),
		waypointRow("X1-DD4-Y2", "X1-DD4", []string{"SHIPYARD"}),
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipyardInventoryModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-DD4", WaypointSymbol: "X1-DD4-Y1",
		ShipType: "SHIP_PROBE", PurchasePrice: 40_000, LastScanned: time.Now().UTC(),
	}).Error)

	yards, err := newCatalogPort(db).ListProbeYards(context.Background(), "X1-DD4")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-DD4-Y1", "X1-DD4-Y2"}, yards,
		"both shipyards are candidates; the priced one leads because it is evidence, the other is a guess we still have to be able to see")
}

// A yard priced and found PROBE-LESS stays out, even though the fallback is now
// unconditional. That exclusion is correct and predates this change: it is the
// standing fact the buy queue's skipKnownProbeless already refuses on, and
// readmitting it would write wants that are scanned, learn nothing, and are
// refused for the memo's whole TTL.
func TestListProbeYards_PricedAndProbelessYardStaysExcluded(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		waypointRow("X1-EE5-Y1", "X1-EE5", []string{"SHIPYARD"}),
		waypointRow("X1-EE5-Y2", "X1-EE5", []string{"SHIPYARD"}),
	}).Error)
	require.NoError(t, db.Create(&[]persistence.ShipyardInventoryModel{
		{PlayerID: testPlayerID, SystemSymbol: "X1-EE5", WaypointSymbol: "X1-EE5-Y1",
			ShipType: "SHIP_PROBE", PurchasePrice: 40_000, LastScanned: time.Now().UTC()},
		// Read RECENTLY, and it carries no probe listing.
		{PlayerID: testPlayerID, SystemSymbol: "X1-EE5", WaypointSymbol: "X1-EE5-Y2",
			ShipType: "SHIP_EXPLORER", PurchasePrice: 10, LastScanned: time.Now().UTC()},
	}).Error)

	yards, err := newCatalogPort(db).ListProbeYards(context.Background(), "X1-EE5")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-EE5-Y1"}, yards,
		"a yard we priced and found probe-less is a standing fact, not a gap to fill")
}

// A STALE probe-less reading is reconsidered, because membership is decided by
// the shared probe-stock rule and that rule degrades a stale NONE to UNREAD.
//
// This is what proves the adapter CONSULTS readProbeStock rather than re-deriving
// "has a probe row" in SQL: a hand-rolled query would keep Y2 excluded forever and
// write off every counter that has since restocked.
func TestListProbeYards_StaleProbelessReadingIsReconsidered(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		waypointRow("X1-FF6-Y1", "X1-FF6", []string{"SHIPYARD"}),
		waypointRow("X1-FF6-Y2", "X1-FF6", []string{"SHIPYARD"}),
	}).Error)
	require.NoError(t, db.Create(&[]persistence.ShipyardInventoryModel{
		{PlayerID: testPlayerID, SystemSymbol: "X1-FF6", WaypointSymbol: "X1-FF6-Y1",
			ShipType: "SHIP_PROBE", PurchasePrice: 40_000, LastScanned: time.Now().UTC()},
		// Last read well beyond the memo TTL: no longer a fact worth acting on.
		{PlayerID: testPlayerID, SystemSymbol: "X1-FF6", WaypointSymbol: "X1-FF6-Y2",
			ShipType: "SHIP_EXPLORER", PurchasePrice: 10,
			LastScanned: time.Now().UTC().Add(-24 * time.Hour)},
	}).Error)

	yards, err := newCatalogPort(db).ListProbeYards(context.Background(), "X1-FF6")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-FF6-Y1", "X1-FF6-Y2"}, yards,
		"a stale probe-less reading degrades to never-priced, so a restocked counter is reconsidered")
}

// --- the ledger's goods column ---------------------------------------------------

// ExistingSlots must SUPPLY the recorded goods, not merely report that a slot
// exists.
//
// The slot row is the screen's cache for a market discovered REMOTELY: market_data
// stays empty until a probe actually parks there, so without the recorded list the
// screen re-fetches the same answer on every re-screen. Worse, an empty list drops
// the waypoint out of the hit set and takes its own slot back out of the plan — so
// the market would be found and dropped forever.
func TestExistingSlots_DecodesTheRecordedGoods(t *testing.T) {
	db := newShipPortsDB(t)
	port := adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db))
	ctx := context.Background()

	require.NoError(t, port.UpsertSlotMetadata(ctx, testPlayerID, appSensing.SlotRecord{
		Waypoint: "X1-AA1-M1", System: "X1-AA1",
		Kind: appSensing.SlotKindMarket, State: appSensing.SlotStateWanted,
		WhitelistGoods: []string{"ELECTRONICS", "FOOD"}, DepthCredits: 4_200,
	}))
	// A YARD slot legitimately records no goods — it is placed for the shipyard.
	require.NoError(t, port.UpsertSlotMetadata(ctx, testPlayerID, appSensing.SlotRecord{
		Waypoint: "X1-AA1-Y1", System: "X1-AA1",
		Kind: appSensing.SlotKindYard, State: appSensing.SlotStateWanted,
	}))

	slots, err := port.ExistingSlots(ctx, testPlayerID, "X1-AA1")
	require.NoError(t, err)

	byWaypoint := map[string]appSensing.ExistingSlot{}
	for _, slot := range slots {
		byWaypoint[slot.Waypoint] = slot
	}
	require.Equal(t, []string{"ELECTRONICS", "FOOD"}, byWaypoint["X1-AA1-M1"].WhitelistGoods,
		"the recorded goods come back intact — this is what suppresses the re-fetch")
	require.Equal(t, int64(4_200), byWaypoint["X1-AA1-M1"].DepthCredits)
	require.Empty(t, byWaypoint["X1-AA1-Y1"].WhitelistGoods, "a yard records no goods, and that is an answer")
}

// ParkedSlotViews carries the three columns the rotation paces on, which the
// state-only QueuedSlot projection does not. YardCadence is deliberately left
// zero: it is a knob, and the coordinator stamps it.
func TestParkedSlotViews_CarryTheRotationColumns(t *testing.T) {
	db := newShipPortsDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	port := adapterSensing.NewLedgerPort(repo)
	ctx := context.Background()

	require.NoError(t, port.UpsertSpareSlot(ctx, testPlayerID, appSensing.SlotRecord{
		Waypoint: "X1-AA1-M1", System: "X1-AA1",
		Kind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		WhitelistGoods: []string{"FOOD"}, AssignedShip: "PROBE-1",
	}))
	require.NoError(t, port.UpsertSlotMetadata(ctx, testPlayerID, appSensing.SlotRecord{
		Waypoint: "X1-AA1-M2", System: "X1-AA1",
		Kind: appSensing.SlotKindMarket, State: appSensing.SlotStateWanted,
	}))
	scannedAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, port.MarkScanned(ctx, testPlayerID, "X1-AA1-M1", appSensing.SlotKindMarket, scannedAt, 0.042))

	views, err := port.ParkedSlotViews(ctx, testPlayerID)
	require.NoError(t, err)
	require.Len(t, views, 1, "only PARKED placements are in the rotation")

	view := views[0]
	require.Equal(t, "X1-AA1-M1", view.Waypoint)
	require.Equal(t, []string{"FOOD"}, view.Whitelist, "the goods the slot exists to watch")
	require.InDelta(t, 0.042, view.SpreadEWMA, 1e-9, "the smoothed spread that weights it")
	require.WithinDuration(t, scannedAt, view.LastScan, time.Second, "the stamp that makes it due")
	require.Zero(t, view.YardCadence, "the cadence is a knob the coordinator stamps, not a column")
}

// screened_at is stamped on EVERY verdict write, PENDING included, because this
// call IS the screening.
//
// The column answers "when was this system last looked at", which is exactly the
// question an operator asks of a system stuck PENDING — is the sweep reaching it
// at all, or has it fallen off the batch? Left NULL beside a populated
// catalog_synced_at it read as though nothing had ever screened the system.
func TestUpsertSystem_StampsScreenedAtOnEveryVerdict(t *testing.T) {
	db := newShipPortsDB(t)
	port := adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db))
	ctx := context.Background()
	before := time.Now().UTC().Add(-time.Second)

	for _, tc := range []struct {
		verdict      string
		catalogKnown bool
	}{
		{appSensing.VerdictPending, false},
		{appSensing.VerdictInScope, true},
		{appSensing.VerdictNoWhitelist, true},
	} {
		t.Run(tc.verdict, func(t *testing.T) {
			system := "X1-SCREEN-" + tc.verdict
			require.NoError(t, port.UpsertSystem(ctx, testPlayerID, appSensing.SystemRecord{
				System: system, Verdict: tc.verdict, CatalogKnown: tc.catalogKnown,
			}))

			var row persistence.SensingSystemModel
			require.NoError(t, db.Where("player_id = ? AND system_symbol = ?", testPlayerID, system).
				First(&row).Error)

			require.NotNil(t, row.ScreenedAt, "%s must record WHEN it was screened", tc.verdict)
			require.True(t, row.ScreenedAt.After(before), "the stamp is this screen's, not a stale one")
			if tc.catalogKnown {
				require.NotNil(t, row.CatalogSyncedAt)
			} else {
				require.Nil(t, row.CatalogSyncedAt,
					"an unswept catalog stays NULL — that is the guard on the durable rejection")
			}
		})
	}
}

// The stamp MOVES on a re-screen, which is the path that actually matters and
// the one the subtests above do not exercise: they all insert a fresh system, so
// they would pass even if screened_at were missing from the upsert's
// on-conflict column list and the value only ever landed on INSERT.
//
// A frozen stamp is worse than a NULL one. NULL reads as "never screened" and is
// obviously missing; a stale timestamp reads as a real answer, and an operator
// checking whether the sweep is still reaching a stuck PENDING system would
// conclude it stopped hours ago when it is in fact screening every batch.
func TestUpsertSystem_ScreenedAtMovesOnReScreen(t *testing.T) {
	db := newShipPortsDB(t)
	port := adapterSensing.NewLedgerPort(persistence.NewSensingLedgerRepository(db))
	ctx := context.Background()
	const system = "X1-RESCREEN"

	require.NoError(t, port.UpsertSystem(ctx, testPlayerID, appSensing.SystemRecord{
		System: system, Verdict: appSensing.VerdictPending,
	}))
	var first persistence.SensingSystemModel
	require.NoError(t, db.Where("player_id = ? AND system_symbol = ?", testPlayerID, system).
		First(&first).Error)
	require.NotNil(t, first.ScreenedAt)

	// A later screen of the SAME system, now resolving to a verdict.
	time.Sleep(10 * time.Millisecond) // so the two stamps are distinguishable
	require.NoError(t, port.UpsertSystem(ctx, testPlayerID, appSensing.SystemRecord{
		System: system, Verdict: appSensing.VerdictInScope, CatalogKnown: true,
	}))

	var second persistence.SensingSystemModel
	require.NoError(t, db.Where("player_id = ? AND system_symbol = ?", testPlayerID, system).
		First(&second).Error)

	require.NotNil(t, second.ScreenedAt)
	require.True(t, second.ScreenedAt.After(*first.ScreenedAt),
		"the re-screen must advance screened_at, not leave the insert's value frozen")
	require.Equal(t, appSensing.VerdictInScope, second.Verdict, "and the verdict advances with it")
}

// gatedSystem is liveShapedSystem plus the one waypoint type that grows the MAP
// rather than the trade data: a JUMP_GATE.
//
// THE GATE IS NAMED LAST ON PURPOSE, and it is the whole point of the fixture.
// X1-AA4-ZZ9 sorts after every other waypoint here — after the asteroids, after
// the market bodies, and after the station. Under the old ordering the gate sat
// in the market tier and therefore came out FIFTH, behind B1/C1/F1. If its symbol
// happened to sort early the old and new orders would coincide and the assertion
// would pass without a gate tier existing at all.
func gatedSystem() []persistence.WaypointModel {
	uncharted := []string{"UNCHARTED"}
	return []persistence.WaypointModel{
		typedWaypointRow("X1-AA4-A1", "X1-AA4", "ASTEROID", uncharted),
		typedWaypointRow("X1-AA4-A2", "X1-AA4", "ASTEROID", uncharted),
		typedWaypointRow("X1-AA4-B1", "X1-AA4", "MOON", uncharted),
		typedWaypointRow("X1-AA4-C1", "X1-AA4", "PLANET", uncharted),
		typedWaypointRow("X1-AA4-D1", "X1-AA4", "GAS_GIANT", uncharted),
		typedWaypointRow("X1-AA4-F1", "X1-AA4", "FUEL_STATION", uncharted),
		typedWaypointRow("X1-AA4-Z9", "X1-AA4", "ORBITAL_STATION", uncharted),
		typedWaypointRow("X1-AA4-ZZ9", "X1-AA4", "JUMP_GATE", uncharted),
	}
}

// THE GATE IS CHARTED SECOND — after the shipyard, ahead of every market.
//
// A jump gate is the only waypoint type that grows the MAP: charting it reveals
// the system's gate adjacency, which frontier propagation turns into new PENDING
// rows, which is where the next ring of reachable systems comes from. Every other
// market-bearing type yields at best one market. Measured live, 51 systems under
// charting had an uncharted gate queued behind 1,787 other waypoints while the
// reachable-target ring stood at zero.
//
// The shipyard still goes first, and that is deliberate rather than incidental: a
// charted yard makes its system buyable, which funds local spares, which stage
// the very seeds that fly to the gates. Demoting it would starve the mechanism
// this ordering exists to accelerate.
func TestUnchartedWaypoints_ChartsTheJumpGateBeforeAnyMarket(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(gatedSystem()).Error)

	order, err := newCatalogPort(db).UnchartedWaypoints(context.Background(), "X1-AA4")
	require.NoError(t, err)

	require.Equal(t, []string{
		"X1-AA4-Z9",  // ORBITAL_STATION — the yard still comes first
		"X1-AA4-ZZ9", // JUMP_GATE — sorts LAST alphabetically, so only a tier can lift it here
		"X1-AA4-B1",  // MOON        \
		"X1-AA4-C1",  // PLANET       > market-bearing, alphabetical within the tier
		"X1-AA4-F1",  // FUEL_STATION/
		"X1-AA4-D1",  // GAS_GIANT — unproven
		"X1-AA4-A1",  // ASTEROID \  0 of 3297, last — but still charted
		"X1-AA4-A2",  // ASTEROID /
	}, order,
		"the jump gate must be charted straight after the shipyard: it is the only waypoint that adds new SYSTEMS")

	require.Len(t, order, 8, "the tour is still EXHAUSTIVE: a reorder must never drop a waypoint")
}

// The set is a property of the rows, not of the ordering. Pinned separately from
// the order above so a future tier change cannot quietly turn the reorder into a
// filter — the owner asked for everything charted, not only the interesting parts.
func TestUnchartedWaypoints_TheGateTierChangesOrderNotMembership(t *testing.T) {
	db := newShipPortsDB(t)
	rows := gatedSystem()
	require.NoError(t, db.Create(rows).Error)
	port := newCatalogPort(db)

	order, err := port.UnchartedWaypoints(context.Background(), "X1-AA4")
	require.NoError(t, err)

	want := make([]string, 0, len(rows))
	for _, row := range rows {
		want = append(want, row.WaypointSymbol)
	}
	require.ElementsMatch(t, want, order,
		"every uncharted waypoint must still be handed to the seed, gate tier or not")

	// The completion signal and the work list read the same rows with no
	// coordination between them, so the count must still equal the tour length —
	// that equality is what makes uncharted_count fall to zero exactly when the
	// seed runs out of stops.
	count, err := port.ListUnchartedCount(context.Background(), "X1-AA4")
	require.NoError(t, err)
	require.Equal(t, len(order), count,
		"ListUnchartedCount must still agree with the tour, or the tour never reads as finished")
}

// Determinism, asserted across two calls. A seed charts the head of this list and
// re-derives it next tick; if the order were unstable it could oscillate between
// two waypoints and never finish the system.
func TestUnchartedWaypoints_TheGatedOrderIsStableAcrossCalls(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(gatedSystem()).Error)
	port := newCatalogPort(db)

	first, err := port.UnchartedWaypoints(context.Background(), "X1-AA4")
	require.NoError(t, err)
	second, err := port.UnchartedWaypoints(context.Background(), "X1-AA4")
	require.NoError(t, err)

	require.Equal(t, first, second, "two derivations of the same system must agree exactly")
}

// --- the free shipyard-catalogue sweep -------------------------------------------
//
// The blind spot these pin: 76 waypoints carried a SHIPYARD trait, 23 appeared in
// shipyard_inventory, 44 had never been read at all, and NOT ONE of the reads that
// were taken happened without a hull standing on the waypoint for unrelated
// reasons. `shipTypes` needs no presence, so all 44 were free the whole time.

// OutstandingYards is the SET DIFFERENCE: every charted shipyard, minus the ones we
// already hold a reading for. The exclusion is what makes the pass self-quiescing —
// and it is also what stops an unpriced sweep from stomping a PRICED reading a
// parked hull recorded, because a priced reading is a catalogue we hold.
func TestOutstandingYards_ExcludesTheYardsWeHaveAlreadyRead(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		waypointRow("X1-QR78-AE4F", "X1-QR78", []string{"SHIPYARD", "MARKETPLACE"}),
		waypointRow("X1-QR78-FE8C", "X1-QR78", []string{"SHIPYARD"}),
		waypointRow("X1-QR78-READ", "X1-QR78", []string{"SHIPYARD"}),
		waypointRow("X1-QR78-M1", "X1-QR78", []string{"MARKETPLACE"}),
	}).Error)
	// One yard already carries a reading. It must never be enumerated again.
	require.NoError(t, db.Create(&persistence.ShipyardInventoryModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-QR78", WaypointSymbol: "X1-QR78-READ",
		ShipType: "SHIP_PROBE", PurchasePrice: 40_000, LastScanned: time.Now().UTC(),
	}).Error)

	outstanding, err := newCatalogPort(db).OutstandingYards(context.Background(), testPlayerID)
	require.NoError(t, err)

	symbols := make([]string, 0, len(outstanding))
	for _, yard := range outstanding {
		symbols = append(symbols, yard.Waypoint)
	}
	require.ElementsMatch(t, []string{"X1-QR78-AE4F", "X1-QR78-FE8C"}, symbols,
		"only the yards we hold no reading for; a plain market is not a yard and a read one is done")
}

// An UNCHARTED waypoint is not yet a shipyard. Its traits are a guess until somebody
// charts it, so reading it would spend a call on a waypoint the API cannot answer for
// — and would then record the answer as though it were evidence.
func TestOutstandingYards_ExcludesUncharted(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		waypointRow("X1-AA1-Y1", "X1-AA1", []string{"SHIPYARD"}),
		waypointRow("X1-AA1-Y9", "X1-AA1", []string{"SHIPYARD", "UNCHARTED"}),
	}).Error)

	outstanding, err := newCatalogPort(db).OutstandingYards(context.Background(), testPlayerID)
	require.NoError(t, err)
	require.Len(t, outstanding, 1)
	require.Equal(t, "X1-AA1-Y1", outstanding[0].Waypoint)
}

// FRONTIER RANK is "is this pass the ONLY route to the answer". A yard in a system we
// already watch will eventually have a hull parked in it, and that hull's scan reads
// the yard under its feet; a yard in a system we watch nothing in has no other route
// at all, so it outranks.
func TestOutstandingYards_RanksUnwatchedSystemsAhead(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		waypointRow("X1-WATCHED-Y1", "X1-WATCHED", []string{"SHIPYARD"}),
		waypointRow("X1-DARK-Y1", "X1-DARK", []string{"SHIPYARD"}),
	}).Error)
	require.NoError(t, db.Create(&persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-WATCHED-M1", SystemSymbol: "X1-WATCHED",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
	}).Error)

	outstanding, err := newCatalogPort(db).OutstandingYards(context.Background(), testPlayerID)
	require.NoError(t, err)

	rank := map[string]int{}
	for _, yard := range outstanding {
		rank[yard.Waypoint] = yard.Frontier
	}
	require.Greater(t, rank["X1-DARK-Y1"], rank["X1-WATCHED-Y1"],
		"a system we watch nothing in has no other route to its yards' catalogues")
}

// --- the catalogue-only reading -------------------------------------------------

// THE FLEET-KILLER THIS GUARD STOPS.
//
// A presence-less read persists every listed type at price 0, because the priced
// `ships` array only appears when a hull is at the counter. Read naively that says
// "this yard sells no probe", which classifies it probeStockNone, drops it out of
// ListProbeYards, and makes a counter we could have bought our next probe at
// invisible for six hours. Turning the free sweep on would then have SHRUNK the
// probe-yard universe it exists to grow.
//
// An unpriced reading is all the evidence there is, and it says the yard lists the
// hull.
func TestLastListingScan_CatalogueOnlyProbeRowCountsAsSellingProbes(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.ShipyardInventoryModel{
		{PlayerID: testPlayerID, SystemSymbol: "X1-AA1", WaypointSymbol: "X1-AA1-Y1",
			ShipType: "SHIP_PROBE", PurchasePrice: 0, LastScanned: time.Now().UTC()},
		{PlayerID: testPlayerID, SystemSymbol: "X1-AA1", WaypointSymbol: "X1-AA1-Y1",
			ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 0, LastScanned: time.Now().UTC()},
	}).Error)

	sellsProbe, _, known, err := newCatalogPort(db).LastListingScan(context.Background(), testPlayerID, "X1-AA1-Y1")
	require.NoError(t, err)
	require.True(t, known)
	require.True(t, sellsProbe,
		"a reading that priced NOTHING is a catalogue, and the catalogue lists SHIP_PROBE")

	// And the consequence that actually matters: the yard is still buyable-from.
	require.NoError(t, db.Create(&[]persistence.WaypointModel{
		waypointRow("X1-AA1-Y1", "X1-AA1", []string{"SHIPYARD"}),
	}).Error)
	yards, err := newCatalogPort(db).ListProbeYards(context.Background(), "X1-AA1")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-AA1-Y1"}, yards,
		"the free catalogue sweep must never remove a probe yard from the buy queue's reach")
}

// The loop-break the memo exists for is PRESERVED. A reading that priced something
// carried the `ships` array, so a probe row left at 0 in it genuinely is "listed but
// not purchasable" — and re-quoting such a counter is exactly what the memo stops.
func TestLastListingScan_PricedReadingStillRefusesAnUnpricedProbe(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&[]persistence.ShipyardInventoryModel{
		{PlayerID: testPlayerID, SystemSymbol: "X1-BB2", WaypointSymbol: "X1-BB2-Y1",
			ShipType: "SHIP_PROBE", PurchasePrice: 0, LastScanned: time.Now().UTC()},
		// Something in the same reading DID price, so this was a hull-present read.
		{PlayerID: testPlayerID, SystemSymbol: "X1-BB2", WaypointSymbol: "X1-BB2-Y1",
			ShipType: "SHIP_EXPLORER", PurchasePrice: 90_000, LastScanned: time.Now().UTC()},
	}).Error)

	sellsProbe, _, known, err := newCatalogPort(db).LastListingScan(context.Background(), testPlayerID, "X1-BB2-Y1")
	require.NoError(t, err)
	require.True(t, known)
	require.False(t, sellsProbe,
		"a reading that priced other hulls but not the probe is evidence the probe cannot be bought there")
}

// A catalogue-only reading that lists NO probe is the tightening this change buys:
// positive evidence the yard sells none, so the drain stops paying live quotes there.
func TestLastListingScan_CatalogueOnlyWithoutAProbeIsEvidenceOfNone(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&persistence.ShipyardInventoryModel{
		PlayerID: testPlayerID, SystemSymbol: "X1-CC3", WaypointSymbol: "X1-CC3-Y1",
		ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 0, LastScanned: time.Now().UTC(),
	}).Error)

	sellsProbe, _, known, err := newCatalogPort(db).LastListingScan(context.Background(), testPlayerID, "X1-CC3-Y1")
	require.NoError(t, err)
	require.True(t, known)
	require.False(t, sellsProbe, "the catalogue is complete and it does not list a probe")
}

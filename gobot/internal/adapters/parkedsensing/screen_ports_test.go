package parkedsensing_test

// Integration tests (real GORM/sqlite, no mocks) for the sensing engine's map and
// market reads.
//
// Three of the properties pinned here are ones the engine ABOVE cannot detect on
// its own — a wrong answer produces a working, silent, subtly-wrong fleet rather
// than an error:
//
//   - MarketPrices maps its columns CROSSED. Uncrossed, every quote inverts, the
//     spread weighting flattens, and the rotation simply stops preferring the
//     markets worth watching.
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
// crossed mapping exists to survive.
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
	encoded, _ := json.Marshal(traits)
	return persistence.WaypointModel{
		WaypointSymbol: symbol,
		SystemSymbol:   system,
		Type:           "PLANET",
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

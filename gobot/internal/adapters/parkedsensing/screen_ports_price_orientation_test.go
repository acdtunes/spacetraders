package parkedsensing_test

// End-to-end price ORIENTATION test: the SpaceTraders API's two prices, through
// the market scanner, into market_data, and back out through the sensing
// screen's MarketPrices port.
//
// This spans a seam nothing else in the suite spans, and that seam is why
// sp-en5h7 survived four era resets. Both halves were tested in isolation and
// both halves passed:
//
//   - screen_ports_test's TestMarketPrices_ColumnsAreCrossed_NormalMarketReadsPositive
//     hand-writes a row with purchase_price > sell_price and proves the READ
//     maps it correctly.
//   - api's client_market_mapping_test proves the API DECODE lands purchasePrice
//     in PurchasePrice.
//
// Nobody tested the WRITE between them, so the scanner could hand the domain
// constructor its two prices transposed and every isolated test stayed green
// while 100% of persisted rows were inverted. A test that starts at the API DTO
// and ends at the sensing port cannot be fooled that way.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appCommon "github.com/andrescamacho/spacetraders-go/internal/application/common"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	domainShared "github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// orientationAPI serves one market read. It is the only test double here: the
// repository, the domain constructor and the sensing port are all real, because
// the defect lived in the wiring BETWEEN them.
type orientationAPI struct {
	domainPorts.APIClient
	goods []domainPorts.TradeGoodData
}

func (a *orientationAPI) GetMarket(_ context.Context, _, waypointSymbol, _ string) (*domainPorts.MarketData, error) {
	return &domainPorts.MarketData{Symbol: waypointSymbol, TradeGoods: a.goods}, nil
}

// The three goods measured live at X1-DA89-DC6A on 2026-07-29, the read that
// confirmed the bead. Every one of them has the API quoting a HIGHER
// purchasePrice than sellPrice, because that gap is the market's rake — it is
// how a market makes money, and it is present at every real market.
func liveMeasuredGoods() []domainPorts.TradeGoodData {
	return []domainPorts.TradeGoodData{
		{Symbol: "FUEL", Supply: "ABUNDANT", Activity: "STRONG", PurchasePrice: 72, SellPrice: 68, TradeVolume: 180, TradeType: "EXCHANGE"},
		{Symbol: "IRON", Supply: "MODERATE", Activity: "WEAK", PurchasePrice: 324, SellPrice: 161, TradeVolume: 60, TradeType: "IMPORT"},
		{Symbol: "MACHINERY", Supply: "LIMITED", Activity: "GROWING", PurchasePrice: 6334, SellPrice: 3123, TradeVolume: 20, TradeType: "IMPORT"},
	}
}

// A scan must persist the API's purchasePrice in purchase_price. Stated as the
// invariant rather than as three numbers: purchase_price is what WE PAY, so it
// is the LARGER of the two at every market that charges more than it pays.
//
// Fails before the fix on the very first good: the scanner handed
// market.NewTradeGood its two prices in the wrong order, so purchase_price held
// 68 (the API's sellPrice) instead of 72.
func TestScanToSensing_APIPurchasePriceLandsInPurchasePriceColumn(t *testing.T) {
	const waypoint = "X1-DA89-DC6A"
	db := newShipPortsDB(t)

	scanner := appShip.NewMarketScanner(
		&orientationAPI{goods: liveMeasuredGoods()},
		persistence.NewMarketRepository(db),
		nil, nil,
	)
	ctx := appCommon.WithPlayerToken(context.Background(), "test-token")
	require.NoError(t, scanner.ScanAndSaveMarket(ctx, uint(testPlayerID), waypoint))

	var rows []persistence.MarketData
	require.NoError(t, db.Where("player_id = ? AND waypoint_symbol = ?", testPlayerID, waypoint).
		Find(&rows).Error)
	require.Len(t, rows, 3)

	byGood := map[string]persistence.MarketData{}
	for _, r := range rows {
		byGood[r.GoodSymbol] = r
	}
	for _, want := range liveMeasuredGoods() {
		got, ok := byGood[want.Symbol]
		require.True(t, ok, "%s was not persisted", want.Symbol)
		require.Equal(t, want.PurchasePrice, got.PurchasePrice,
			"%s: market_data.purchase_price must hold the API's purchasePrice (what WE PAY)", want.Symbol)
		require.Equal(t, want.SellPrice, got.SellPrice,
			"%s: market_data.sell_price must hold the API's sellPrice (what WE RECEIVE)", want.Symbol)
		require.Greater(t, got.PurchasePrice, got.SellPrice,
			"%s: a real market charges more than it pays", want.Symbol)
	}
}

// market_price_history is written from the SAME domain object as market_data, so
// the transposition reached it too — 265,709 rows, every era, with the entire
// series' spread recorded backwards. That makes anything derived from it measure
// the spread the wrong way round.
//
// It is a SECOND write path (a different repository and a different table), and
// it is append-only, so unlike the market_data cache it does not heal itself on
// the next scan. Pin its orientation independently rather than inferring it from
// the market_data assertion.
func TestScanToSensing_PriceHistoryRecordsSameOrientation(t *testing.T) {
	const waypoint = "X1-DA89-DC6A"
	db := newShipPortsDB(t)
	repo := persistence.NewMarketRepository(db)
	historyRepo := persistence.NewGormMarketPriceHistoryRepository(db)
	ctx := appCommon.WithPlayerToken(context.Background(), "test-token")

	// First scan seeds the cache; history is only written once there is a previous
	// observation to compare against.
	first := &orientationAPI{goods: liveMeasuredGoods()}
	require.NoError(t, appShip.NewMarketScanner(first, repo, nil, historyRepo).
		ScanAndSaveMarket(ctx, uint(testPlayerID), waypoint))

	// Second scan at moved prices — still a normal market, still ask above bid.
	//
	// Stamped as a PAIRED read (sp-ntgfj): back-to-back scans of one market are what
	// the fleet market-scan budget's freshness rule declines, and the "after" half of
	// a before/after pair is the one read that exemption exists for. The production
	// path that records a price move — the sampled post-trade impact scan — stamps
	// exactly this, so the fixture matches how the history is really collected.
	moved := []domainPorts.TradeGoodData{
		{Symbol: "MACHINERY", Supply: "SCARCE", Activity: "GROWING", PurchasePrice: 6500, SellPrice: 3200, TradeVolume: 20, TradeType: "IMPORT"},
	}
	require.NoError(t, appShip.NewMarketScanner(&orientationAPI{goods: moved}, repo, nil, historyRepo).
		ScanAndSaveMarket(domainShared.WithPairedScan(ctx), uint(testPlayerID), waypoint))

	var rows []persistence.MarketPriceHistoryModel
	require.NoError(t, db.Where("player_id = ? AND good_symbol = ?", testPlayerID, "MACHINERY").
		Find(&rows).Error)
	require.NotEmpty(t, rows, "a moved price must be recorded to history")
	for _, r := range rows {
		require.Equal(t, 6500, r.PurchasePrice,
			"market_price_history.purchase_price must hold the API's purchasePrice (what WE PAY)")
		require.Equal(t, 3200, r.SellPrice,
			"market_price_history.sell_price must hold the API's sellPrice (what WE RECEIVE)")
		require.Greater(t, r.PurchasePrice, r.SellPrice,
			"the historical series must record the spread the right way round")
	}
}

// The consequence the bead actually reported: the sensing screen refusing goods
// as impossible. On correctly-oriented data the ask/bid guard in RelativeSpread
// must not fire at all, and the rotation must observe a real spread instead of
// the zero that collapses every slot onto the same prior weight.
//
// This is the OUTER assertion — it does not name a column, only the behaviour a
// working sensing model has.
func TestScanToSensing_NoInvertedQuoteWarningOnLiveData(t *testing.T) {
	const waypoint = "X1-DA89-DC6A"
	db := newShipPortsDB(t)

	scanner := appShip.NewMarketScanner(
		&orientationAPI{goods: liveMeasuredGoods()},
		persistence.NewMarketRepository(db),
		nil, nil,
	)
	ctx := appCommon.WithPlayerToken(context.Background(), "test-token")
	require.NoError(t, scanner.ScanAndSaveMarket(ctx, uint(testPlayerID), waypoint))

	prices, err := adapterSensing.NewMarketGoodsPort(db).
		MarketPrices(context.Background(), testPlayerID, waypoint)
	require.NoError(t, err)
	require.Len(t, prices, 3)
	for _, p := range prices {
		require.Greater(t, p.Ask, p.Bid, "%s: ask must exceed bid on a scanned market", p.Good)
	}

	spread, inverted := appSensing.RelativeSpread(prices, []string{"FUEL", "IRON", "MACHINERY"})
	require.Zero(t, inverted,
		"a scan of a normal market must produce ZERO impossible quotes; every one of these "+
			"was being skipped, and the skip is what sp-en5h7 reported")
	require.Positive(t, spread,
		"the rotation must observe a real spread; skipped goods leave it at 0 and every "+
			"slot lands on the same cold-start prior")
}

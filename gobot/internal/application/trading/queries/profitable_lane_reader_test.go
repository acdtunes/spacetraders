package queries

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// --- fake market reader (narrow: the two reads the lane count consumes) -----------------------

type fakeLaneMarketReader struct {
	systems map[string][]string       // system -> waypoints
	markets map[string]*market.Market // waypoint -> cached market
	listErr map[string]error          // system -> FindAllMarketsInSystem error
	dataErr map[string]error          // waypoint -> GetMarketData error
}

func (f *fakeLaneMarketReader) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	if e := f.listErr[systemSymbol]; e != nil {
		return nil, e
	}
	return f.systems[systemSymbol], nil
}

func (f *fakeLaneMarketReader) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	if e := f.dataErr[waypointSymbol]; e != nil {
		return nil, e
	}
	return f.markets[waypointSymbol], nil
}

// good builds one TradeGood. Recall the market-perspective columns: purchasePrice is the market's
// BUY column (Bid — what a ship RECEIVES selling TO it); sellPrice is the market's SELL column (Ask —
// what a ship PAYS buying FROM it). A profitable lane BUYS at a low exporter Ask and SELLS at a high
// importer Bid: spread/unit = destBid − sourceAsk.
func good(t *testing.T, symbol string, bid, ask, volume int, tradeType market.TradeType) market.TradeGood {
	t.Helper()
	g, err := market.NewTradeGood(symbol, nil, nil, bid, ask, volume, tradeType)
	require.NoError(t, err)
	return *g
}

func mkt(t *testing.T, waypoint string, goods ...market.TradeGood) *market.Market {
	t.Helper()
	m, err := market.NewMarket(waypoint, goods, time.Now())
	require.NoError(t, err)
	return m
}

// profitablePair returns the exporter+importer goods for a floor-clearing lane in `symbol`: buy at
// the exporter (Ask=100), sell at the importer (Bid=2000) → spread 1900 >= MinBidMargin(1000). The
// exporter/importer roles are carried by TradeType + prices; the caller places each good in its
// waypoint's market.
func profitablePair(t *testing.T, symbol string) (market.TradeGood, market.TradeGood) {
	return good(t, symbol, 50, 100, 50, market.TradeTypeExport), // exporter: Ask 100 (cheap to buy)
		good(t, symbol, 2000, 3000, 50, market.TradeTypeImport) // importer: Bid 2000 (rich to sell)
}

// subFloorPair returns a sub-floor lane: spread 700 < 1000, so it must NOT be counted as profitable.
func subFloorPair(t *testing.T, symbol string) (market.TradeGood, market.TradeGood) {
	return good(t, symbol, 50, 100, 50, market.TradeTypeExport),
		good(t, symbol, 800, 900, 50, market.TradeTypeImport) // Bid 800 − Ask 100 = 700 < 1000
}

// Counts only the floor-clearing lanes, summed across the player's systems.
func TestCountProfitableLanes_CountsFloorClearingLanesAcrossSystems(t *testing.T) {
	// System AA: one profitable lane (FUEL) + one sub-floor lane (ICE).
	aExpFuel, aImpFuel := profitablePair(t, "FUEL")
	aExpIce, aImpIce := subFloorPair(t, "ICE")
	// System BB: one profitable lane (GOLD).
	bExpGold, bImpGold := profitablePair(t, "GOLD")

	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		systems: map[string][]string{
			"X1-AA": {"X1-AA-1", "X1-AA-2"},
			"X1-BB": {"X1-BB-1", "X1-BB-2"},
		},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", aExpFuel, aExpIce),
			"X1-AA-2": mkt(t, "X1-AA-2", aImpFuel, aImpIce),
			"X1-BB-1": mkt(t, "X1-BB-1", bExpGold),
			"X1-BB-2": mkt(t, "X1-BB-2", bImpGold),
		},
	})

	count, readable, err := reader.CountProfitableLanes(context.Background(), 1, []string{"X1-AA", "X1-BB"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 2, count, "one profitable lane per system (FUEL, GOLD); the sub-floor ICE lane is excluded")
}

// Sub-floor-only markets yield zero profitable lanes — readable (genuinely no demand), never a
// fail-closed miss.
func TestCountProfitableLanes_SubFloorLanesExcluded(t *testing.T) {
	exp, imp := subFloorPair(t, "ICE")
	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2"}},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", exp),
			"X1-AA-2": mkt(t, "X1-AA-2", imp),
		},
	})
	count, readable, err := reader.CountProfitableLanes(context.Background(), 1, []string{"X1-AA"})
	require.NoError(t, err)
	require.True(t, readable, "a readable market with no floor-clearing lane is a readable ZERO, not a failure")
	require.Equal(t, 0, count)
}

// RULINGS #4: a genuine market-list read failure fails the WHOLE count CLOSED — never a silent
// under-count feeding a spend.
func TestCountProfitableLanes_MarketListErrorFailsClosed(t *testing.T) {
	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		listErr: map[string]error{"X1-AA": errors.New("db down")},
	})
	count, readable, err := reader.CountProfitableLanes(context.Background(), 1, []string{"X1-AA"})
	require.Error(t, err)
	require.False(t, readable, "an unreadable market surface must fail closed")
	require.Zero(t, count)
}

// No cached markets is a readable zero (the bootstrap DATA phase hasn't scouted yet) — no demand, no
// buy, not a fail-closed.
func TestCountProfitableLanes_EmptyCacheReadableZero(t *testing.T) {
	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {}},
	})
	count, readable, err := reader.CountProfitableLanes(context.Background(), 1, []string{"X1-AA"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Zero(t, count)
}

// READ-ONLY: the lane count is a PARALLEL reader over the same pure ranking, never a call into the
// trade coordinator — it holds only a read-only market interface (no absorption ledger, no circuit
// state), so it cannot mutate the coordinator. This proves the concrete no-side-effect claim: the
// read is idempotent and never mutates the cached markets it consumes.
func TestCountProfitableLanes_ReadOnly_NoSideEffects(t *testing.T) {
	exp, imp := profitablePair(t, "FUEL")
	expMkt := mkt(t, "X1-AA-1", exp)
	impMkt := mkt(t, "X1-AA-2", imp)
	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2"}},
		markets: map[string]*market.Market{"X1-AA-1": expMkt, "X1-AA-2": impMkt},
	})
	before := marketSnapshot(expMkt) + "|" + marketSnapshot(impMkt)

	c1, _, _ := reader.CountProfitableLanes(context.Background(), 1, []string{"X1-AA"})
	c2, _, _ := reader.CountProfitableLanes(context.Background(), 1, []string{"X1-AA"})

	require.Equal(t, c1, c2, "the read is idempotent — no state accrues between calls")
	require.Equal(t, before, marketSnapshot(expMkt)+"|"+marketSnapshot(impMkt),
		"the cached markets must be byte-identical after the read (no coordinator/circuit side effect)")
}

func marketSnapshot(m *market.Market) string {
	var b strings.Builder
	for _, g := range m.TradeGoods() {
		fmt.Fprintf(&b, "%s:%d/%d/%d;", g.Symbol(), g.PurchasePrice(), g.SellPrice(), g.TradeVolume())
	}
	return b.String()
}

// A single unreadable market within a system is skipped (fail-open at the finest grain, mirroring the
// trade coordinator's collectSystemListings) — the readable markets still form their lane.
func TestCountProfitableLanes_SkipsUnreadableIndividualMarket(t *testing.T) {
	exp, imp := profitablePair(t, "FUEL")
	// A third waypoint whose GetMarketData errors is skipped; the FUEL lane across 1/2 still counts.
	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2", "X1-AA-3"}},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", exp),
			"X1-AA-2": mkt(t, "X1-AA-2", imp),
		},
		dataErr: map[string]error{"X1-AA-3": errors.New("stale/missing")},
	})
	count, readable, err := reader.CountProfitableLanes(context.Background(), 1, []string{"X1-AA"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 1, count)
}

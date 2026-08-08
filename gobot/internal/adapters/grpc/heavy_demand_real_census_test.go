package grpc

// The heavy-demand chain end-to-end over the REAL lane census. Every other test of this seam feeds
// UnservedLaneCount a canned int through a fake counter, so the MAGNITUDE the census produces —
// which is what sizes the trade pool, one hull per unserved lane — is invisible to them. This test
// drives the production wiring: cached markets -> ProfitableLaneReader -> UnservedLaneReader, so a
// future change to what the census counts has to restate its magnitude here rather than land
// silently. (sp-5pclx deleted the autosizer's HeavyDemandProvider that once sat on the end of this
// chain; the growth coordinator reads the same UnservedLaneReader, so the chain under test is the
// live one and the arithmetic below is unchanged.)

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	tradingQueries "github.com/andrescamacho/spacetraders-go/internal/application/trading/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// censusMarkets is the narrow market surface the lane census reads.
type censusMarkets struct {
	systems map[string][]string
	markets map[string]*market.Market
}

func (c *censusMarkets) FindAllMarketsInSystem(ctx context.Context, system string, playerID int) ([]string, error) {
	return c.systems[system], nil
}

func (c *censusMarkets) GetMarketData(ctx context.Context, waypoint string, playerID int) (*market.Market, error) {
	return c.markets[waypoint], nil
}

// censusGates proves every scanned system one gate hop from every other — the fixture's stand-in
// for a fully-charted neighbourhood, mirroring the stored walk's own "0 hops from itself".
type censusGates struct{}

func (censusGates) StoredHopDistances(ctx context.Context, from string, targets []string, maxJumps int) (map[string]int, error) {
	out := map[string]int{}
	for _, t := range targets {
		if t == from {
			out[t] = 0
			continue
		}
		out[t] = 1
	}
	return out, nil
}

// twoSystemTradingGrounds builds two systems, each with one exporter and two import sinks, both
// trading the same two goods. Every exporter listing pairs with every import sink: 2 sources x 4
// sinks x 2 goods = 16 lanes, half of them crossing a gate. Ranking per system instead — one lane
// per good per system — would report four.
func twoSystemTradingGrounds(t *testing.T) *censusMarkets {
	t.Helper()
	exporter := func(symbol string) market.TradeGood {
		g, err := market.NewTradeGood(symbol, nil, nil, 100, 50, 50, market.TradeTypeExport)
		require.NoError(t, err)
		return *g
	}
	importer := func(symbol string) market.TradeGood {
		g, err := market.NewTradeGood(symbol, nil, nil, 3000, 2000, 50, market.TradeTypeImport)
		require.NoError(t, err)
		return *g
	}
	at := func(waypoint string, goods ...market.TradeGood) *market.Market {
		m, err := market.NewMarket(waypoint, goods, time.Now())
		require.NoError(t, err)
		return m
	}

	return &censusMarkets{
		systems: map[string][]string{
			"X1-AA": {"X1-AA-1", "X1-AA-2", "X1-AA-3"},
			"X1-BB": {"X1-BB-1", "X1-BB-2", "X1-BB-3"},
		},
		markets: map[string]*market.Market{
			"X1-AA-1": at("X1-AA-1", exporter("FUEL"), exporter("IRON")),
			"X1-AA-2": at("X1-AA-2", importer("FUEL"), importer("IRON")),
			"X1-AA-3": at("X1-AA-3", importer("FUEL"), importer("IRON")),
			"X1-BB-1": at("X1-BB-1", exporter("FUEL"), exporter("IRON")),
			"X1-BB-2": at("X1-BB-2", importer("FUEL"), importer("IRON")),
			"X1-BB-3": at("X1-BB-3", importer("FUEL"), importer("IRON")),
		},
	}
}

// THE MAGNITUDE ASSERTION. Heavy demand is current heavies plus unserved lanes, so the census
// number IS the size of the trade pool the fleet's heavy buyer will drive toward.
func TestHeavyDemand_OverTheRealLaneCensus(t *testing.T) {
	shipRepo := &fakeHeavyShipRepo{all: []*navigation.Ship{
		tradeShipAt(t, "TR-1", 1, "X1-AA-1"),
		tradeShipAt(t, "TR-2", 1, "X1-BB-1"),
	}}
	census := tradingQueries.NewProfitableLaneReader(twoSystemTradingGrounds(t), censusGates{})
	unserved, readable, err := tradingQueries.NewUnservedLaneReader(shipRepo, census).
		UnservedLaneCount(context.Background(), 1)

	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 14, unserved, "16 census lanes − 2 trade hulls in the pool = 14 unserved")
	// …and the pure sizing math on top of it: one hull wanted per unserved lane the fleet can reach.
	require.Equal(t, 16, fleetCmd.HeavyDemandFor(2, unserved),
		"2 heavies + 14 unserved = 16 — the size of the trade pool this census asks for")
}

// The same wiring with no walkable gate graph counts only the lanes inside each system: the
// cross-system pairings the pooled census can SEE are not pairings it may COUNT until something
// proves a hull can fly them (RULINGS #4).
func TestHeavyDemand_UnprovenCrossingsDoNotSizeThePool(t *testing.T) {
	shipRepo := &fakeHeavyShipRepo{all: []*navigation.Ship{
		tradeShipAt(t, "TR-1", 1, "X1-AA-1"),
		tradeShipAt(t, "TR-2", 1, "X1-BB-1"),
	}}
	census := tradingQueries.NewProfitableLaneReader(twoSystemTradingGrounds(t), sameSystemOnlyGates{})
	unserved, readable, err := tradingQueries.NewUnservedLaneReader(shipRepo, census).
		UnservedLaneCount(context.Background(), 1)

	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 8, fleetCmd.HeavyDemandFor(2, unserved),
		"2 heavies + (8 within-system lanes − 2 in the pool) = 8; the 8 cross-system lanes are unproven")
}

// sameSystemOnlyGates proves nothing but a system's distance to itself.
type sameSystemOnlyGates struct{}

func (sameSystemOnlyGates) StoredHopDistances(ctx context.Context, from string, targets []string, maxJumps int) (map[string]int, error) {
	out := map[string]int{}
	for _, t := range targets {
		if t == from {
			out[t] = 0
		}
	}
	return out, nil
}

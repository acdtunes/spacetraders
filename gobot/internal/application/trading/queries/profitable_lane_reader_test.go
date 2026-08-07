package queries

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
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

// --- fake reachability (narrow: the stored gate-hop walk the census bounds itself by) ----------

// fakeLaneReach stands in for the stored gate graph. It mirrors the two properties the real
// StoredHopDistances contract rests on: a system is 0 hops from ITSELF, and a pair it cannot prove
// is ABSENT from the result rather than reported as far away.
type fakeLaneReach struct {
	crossHops int // hops between two DISTINCT systems; negative => no cross-system pair is proven
	err       error
	askedFor  int // the jump bound the census requested, recorded for the bound assertion
}

// reachAllWithin joins every distinct system to every other at `hops` gate crossings.
func reachAllWithin(hops int) *fakeLaneReach { return &fakeLaneReach{crossHops: hops} }

// reachNothingAcross proves no crossing at all — the stored graph has no walkable edge.
func reachNothingAcross() *fakeLaneReach { return &fakeLaneReach{crossHops: -1} }

func (f *fakeLaneReach) StoredHopDistances(ctx context.Context, from string, targets []string, maxJumps int) (map[string]int, error) {
	f.askedFor = maxJumps
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]int{}
	for _, t := range targets {
		switch {
		case t == from:
			out[t] = 0
		case f.crossHops >= 0 && f.crossHops <= maxJumps:
			out[t] = f.crossHops
		}
	}
	return out, nil
}

// countProfitable runs the census for its NUMBER alone — the count the wave consumes — so a test
// whose subject is that number is not made to unpack terms it does not assert. The terms have
// their own tests; this must never grow one.
func (r *ProfitableLaneReader) countProfitable(ctx context.Context, playerID int, systems []string) (int, bool, error) {
	census, readable, err := r.CountProfitableLanes(ctx, playerID, systems)
	return census.Profitable, readable, err
}

// good builds one TradeGood from a (bid, ask) pair. The columns are named from OUR side, so the
// mapping is a rename and not a swap: purchase_price is the ASK — what a ship PAYS buying FROM the
// market — and sell_price is the BID — what a ship RECEIVES selling TO it. A real market charges
// more than it pays, so ask > bid on every fixture here. A profitable lane BUYS at a low exporter
// Ask and SELLS at a high importer Bid: spread/unit = destBid − sourceAsk.
//
// This helper passed bid into the purchasePrice slot and ask into sellPrice until sp-en5h7, matching
// the transposed rows the market scanner was writing. Under the corrected readers those fixtures
// quote ask < bid, which GoodListing.IsCrossed refuses outright — so the inversion now shows up as a
// zero lane count rather than as prices that cancel out.
func good(t *testing.T, symbol string, bid, ask, volume int, tradeType market.TradeType) market.TradeGood {
	t.Helper()
	g, err := market.NewTradeGood(symbol, nil, nil, ask, bid, volume, tradeType)
	require.NoError(t, err)
	return *g
}

func mkt(t *testing.T, waypoint string, goods ...market.TradeGood) *market.Market {
	t.Helper()
	return mktObservedAt(t, waypoint, time.Now(), goods...)
}

// mktObservedAt dates a cached market, so a fixture can be as stale as the freshness table cares.
func mktObservedAt(t *testing.T, waypoint string, observed time.Time, goods ...market.TradeGood) *market.Market {
	t.Helper()
	m, err := market.NewMarket(waypoint, goods, observed)
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
	}, reachAllWithin(1))

	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA", "X1-BB"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 2, count, "one profitable lane per system (FUEL, GOLD); the sub-floor ICE lane is excluded")
}

// A lane's source and destination need not share a system. FUEL exports from two waypoints in AA
// and imports into two in BB: four cross-system lanes and NO within-system one (an exporter is
// never a sink, and the two importers quote against each other at a loss).
//
// The three answers this pins apart: 0 is the per-system scan that cannot see across a system
// boundary at all; 1 is pooling the listings and reusing trading.RankSpreads, which is a SELECTION
// primitive and keeps one lane per good; 4 is the census — how much profitable work exists.
func TestCountProfitableLanes_CountsEveryCrossSystemLane(t *testing.T) {
	reader := NewProfitableLaneReader(crossSystemFuelSurface(t), reachAllWithin(1))

	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA", "X1-BB"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 4, count,
		"both AA exporters pair with both BB importers: 4 distinct (good, source, dest) lanes clear the floor")
}

// crossSystemFuelSurface is two FUEL exporters in AA and two rich FUEL importers in BB — four
// cross-system lanes, every one of them deep enough that only reachability can veto it.
func crossSystemFuelSurface(t *testing.T) *fakeLaneMarketReader {
	t.Helper()
	return crossSystemFuelSurfaceOfDepth(t, 50)
}

// crossSystemFuelSurfaceOfDepth is that surface at a chosen market depth. A test whose subject is a
// REACH bound needs the deep variant: the round-trip gate charge grows with the hop count, and a
// fixture the fee can veto at the bound never consults the bound at all.
//
// shallowestCrossSystemSpread is the thinnest of the four lanes (AA-2's ask against BB-2's bid) —
// the one that decides whether the fee or the bound is doing the refusing.
const shallowestCrossSystemSpread = 1800 - 120

func crossSystemFuelSurfaceOfDepth(t *testing.T, depth int) *fakeLaneMarketReader {
	t.Helper()
	return &fakeLaneMarketReader{
		systems: map[string][]string{
			"X1-AA": {"X1-AA-1", "X1-AA-2"},
			"X1-BB": {"X1-BB-1", "X1-BB-2"},
		},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", good(t, "FUEL", 50, 100, depth, market.TradeTypeExport)),
			"X1-AA-2": mkt(t, "X1-AA-2", good(t, "FUEL", 60, 120, depth, market.TradeTypeExport)),
			"X1-BB-1": mkt(t, "X1-BB-1", good(t, "FUEL", 2000, 3000, depth, market.TradeTypeImport)),
			"X1-BB-2": mkt(t, "X1-BB-2", good(t, "FUEL", 1800, 2500, depth, market.TradeTypeImport)),
		},
	}
}

// RULINGS #4: a pairing whose crossing nothing can prove is NOT counted. The census pools listings
// across systems, so without this every far-flung market a probe once wandered past would pair with
// every local one and demand a hull per phantom lane. Same fixture as the cross-system census
// above, which counts 4 when the crossing is proven — so the 0 here is the reachability veto and
// not a broken fixture.
func TestCountProfitableLanes_UnprovenCrossingIsNotCounted(t *testing.T) {
	reach := reachNothingAcross()
	reader := NewProfitableLaneReader(crossSystemFuelSurface(t), reach)

	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA", "X1-BB"})
	require.NoError(t, err)
	require.True(t, readable, "an unprovable crossing is a readable ZERO, not an outage")
	require.Zero(t, count)
	require.Equal(t, laneSpanHops, reach.askedFor,
		"the census must ask only as far as one of the executor's lane pools spans")
	require.Less(t, laneSpanHops, gategraph.MaxJumpPath,
		"and that span is TIGHTER than the ferry bound — a lane a bought heavy could be sent past is not thereby flyable")
}

// RULINGS #4: the gate store failing is not evidence of reachability. It fails the WHOLE count
// closed, exactly as a market-list read failure does.
func TestCountProfitableLanes_ReachabilityReadFailureFailsClosed(t *testing.T) {
	reach := reachAllWithin(1)
	reach.err = errors.New("gate store down")
	reader := NewProfitableLaneReader(crossSystemFuelSurface(t), reach)

	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA", "X1-BB"})
	require.Error(t, err)
	require.False(t, readable)
	require.Zero(t, count)
}

// A reader built without a reachability source counts NOTHING and says so. The alternative —
// counting every pooled pairing — is the over-count the money path may not take.
func TestCountProfitableLanes_UnwiredReachabilityFailsClosed(t *testing.T) {
	reader := NewProfitableLaneReader(crossSystemFuelSurface(t), nil)

	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA", "X1-BB"})
	require.Error(t, err)
	require.False(t, readable)
	require.Zero(t, count)
}

// Within one system the count is a census too: every floor-clearing pair, not the single best pair
// per good. One exporter feeding two importers is two lanes a hull could fly, not one.
func TestCountProfitableLanes_CountsEveryLanePerGoodNotOnlyTheBest(t *testing.T) {
	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2", "X1-AA-3"}},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", good(t, "FUEL", 50, 100, 50, market.TradeTypeExport)),
			"X1-AA-2": mkt(t, "X1-AA-2", good(t, "FUEL", 2000, 3000, 50, market.TradeTypeImport)),
			"X1-AA-3": mkt(t, "X1-AA-3", good(t, "FUEL", 1500, 2200, 50, market.TradeTypeImport)),
		},
	}, reachAllWithin(1))

	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 2, count, "AA-1 feeds both AA-2 and AA-3 above the floor; ranking keeps only the deeper one")
}

// The counting unit is the (good, source, dest) CIRCUIT, and the good is half of it. One waypoint
// pair trading several goods above the floor is several lanes a hull could fly — a hub feeding a
// hub is the normal shape of that, not an edge case. Keyed on the waypoint pair alone this reports
// 1 and under-sizes the trade pool by every extra good.
func TestCountProfitableLanes_CountsEveryFloorClearingGoodOnOneWaypointPair(t *testing.T) {
	expFuel, impFuel := profitablePair(t, "FUEL")
	expIron, impIron := profitablePair(t, "IRON")

	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2"}},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", expFuel, expIron),
			"X1-AA-2": mkt(t, "X1-AA-2", impFuel, impIron),
		},
	}, reachAllWithin(1))

	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 2, count, "FUEL and IRON on X1-AA-1 → X1-AA-2 are two lanes, not one")
}

// The counting unit is the circuit, not the listing pair that produced it: a waypoint the market
// list names twice must not double its lanes.
func TestCountProfitableLanes_DedupesRepeatedWaypoint(t *testing.T) {
	exp, imp := profitablePair(t, "FUEL")
	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2", "X1-AA-2"}},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", exp),
			"X1-AA-2": mkt(t, "X1-AA-2", imp),
		},
	}, reachAllWithin(1))

	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 1, count)
}

// RULINGS #4, the OVER-count direction. A sink that can absorb nothing is not work at any spread:
// the trip moves min(source, dest) units, and zero units earn zero however rich the quote. Counting
// it demands a hull for a lane that can trade nothing. The deep sink beside it is the calibration —
// the fixture does produce a lane, so the 1 is a rejection and not an empty scan.
func TestCountProfitableLanes_ZeroAbsorptionSinkIsNotWork(t *testing.T) {
	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2", "X1-AA-3"}},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", good(t, "FUEL", 50, 100, 50, market.TradeTypeExport)),
			"X1-AA-2": mkt(t, "X1-AA-2", good(t, "FUEL", 2000, 3000, 0, market.TradeTypeImport)),
			"X1-AA-3": mkt(t, "X1-AA-3", good(t, "FUEL", 2000, 3000, 50, market.TradeTypeImport)),
		},
	}, reachAllWithin(1))

	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 1, count, "only the sink that can actually absorb a cargo is a lane")
}

// RULINGS #4: a crossing costs money, so a trip that earns less than the gates it pays is a LOSS
// and must not demand a hull. The two halves differ only in whether the sink is across a gate, so
// what is being pinned is the crossing charge and not the per-unit floor (MinBidMargin is
// untouched: both lanes clear it exactly).
func TestCountProfitableLanes_ThinLaneMustCoverItsCrossing(t *testing.T) {
	thinExporter := good(t, "FUEL", 50, 100, 1, market.TradeTypeExport)
	thinImporter := good(t, "FUEL", 1100, 1600, 1, market.TradeTypeImport) // spread 1000 == MinBidMargin
	require.Less(t, int64(1000*1), domainSensing.DefaultGateFeeCredits,
		"the fixture only tests the charge if one trip earns LESS than one crossing costs")

	t.Run("across a gate it is a loss", func(t *testing.T) {
		reader := NewProfitableLaneReader(&fakeLaneMarketReader{
			systems: map[string][]string{"X1-AA": {"X1-AA-1"}, "X1-BB": {"X1-BB-1"}},
			markets: map[string]*market.Market{
				"X1-AA-1": mkt(t, "X1-AA-1", thinExporter),
				"X1-BB-1": mkt(t, "X1-BB-1", thinImporter),
			},
		}, reachAllWithin(1))

		count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA", "X1-BB"})
		require.NoError(t, err)
		require.True(t, readable)
		require.Zero(t, count)
	})

	t.Run("inside one system it pays no gate and counts", func(t *testing.T) {
		reader := NewProfitableLaneReader(&fakeLaneMarketReader{
			systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2"}},
			markets: map[string]*market.Market{
				"X1-AA-1": mkt(t, "X1-AA-1", thinExporter),
				"X1-AA-2": mkt(t, "X1-AA-2", thinImporter),
			},
		}, reachAllWithin(1))

		count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA"})
		require.NoError(t, err)
		require.True(t, readable)
		require.Equal(t, 1, count)
	})
}

// The census drops rows past their activity's freshness cap, exactly as the coordinator's lane
// ranker does. Pooling is what makes this load-bearing: one system whose cache went cold months ago
// would otherwise cross-product its dead quotes with every fresh market in the fleet's grounds.
func TestCountProfitableLanes_StaleListingsFormNoLane(t *testing.T) {
	exp, imp := profitablePair(t, "FUEL")
	surface := func(bbObserved time.Time) *fakeLaneMarketReader {
		return &fakeLaneMarketReader{
			systems: map[string][]string{"X1-AA": {"X1-AA-1"}, "X1-BB": {"X1-BB-1"}},
			markets: map[string]*market.Market{
				"X1-AA-1": mkt(t, "X1-AA-1", exp),
				"X1-BB-1": mktObservedAt(t, "X1-BB-1", bbObserved, imp),
			},
		}
	}

	fresh := NewProfitableLaneReader(surface(time.Now()), reachAllWithin(1))
	count, _, err := fresh.countProfitable(context.Background(), 1, []string{"X1-AA", "X1-BB"})
	require.NoError(t, err)
	require.Equal(t, 1, count, "calibration: with a fresh BB the pairing IS a lane")

	stale := NewProfitableLaneReader(surface(time.Now().Add(-30*24*time.Hour)), reachAllWithin(1))
	count, readable, err := stale.countProfitable(context.Background(), 1, []string{"X1-AA", "X1-BB"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Zero(t, count, "a month-old quote is not a lane the fleet can be sized to")
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
	}, reachAllWithin(1))
	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA"})
	require.NoError(t, err)
	require.True(t, readable, "a readable market with no floor-clearing lane is a readable ZERO, not a failure")
	require.Equal(t, 0, count)
}

// RULINGS #4: a genuine market-list read failure fails the WHOLE count CLOSED — never a silent
// under-count feeding a spend.
func TestCountProfitableLanes_MarketListErrorFailsClosed(t *testing.T) {
	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		listErr: map[string]error{"X1-AA": errors.New("db down")},
	}, reachAllWithin(1))
	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA"})
	require.Error(t, err)
	require.False(t, readable, "an unreadable market surface must fail closed")
	require.Zero(t, count)
}

// No cached markets is a readable zero (the bootstrap DATA phase hasn't scouted yet) — no demand, no
// buy, not a fail-closed.
func TestCountProfitableLanes_EmptyCacheReadableZero(t *testing.T) {
	reader := NewProfitableLaneReader(&fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {}},
	}, reachAllWithin(1))
	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA"})
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
	}, reachAllWithin(1))
	before := marketSnapshot(expMkt) + "|" + marketSnapshot(impMkt)

	c1, _, _ := reader.countProfitable(context.Background(), 1, []string{"X1-AA"})
	c2, _, _ := reader.countProfitable(context.Background(), 1, []string{"X1-AA"})

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
	}, reachAllWithin(1))
	count, readable, err := reader.countProfitable(context.Background(), 1, []string{"X1-AA"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 1, count)
}

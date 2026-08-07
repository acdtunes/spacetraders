package queries

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// laneReachTable proves a hop distance only for the pairs it was given, so ONE fixture can hold
// both a proven crossing and an unprovable one. Absence is refusal, as it is in the stored walk.
type laneReachTable struct{ hops map[string]map[string]int }

func (f laneReachTable) StoredHopDistances(_ context.Context, from string, targets []string, maxJumps int) (map[string]int, error) {
	out := map[string]int{}
	for _, t := range targets {
		if t == from {
			out[t] = 0
			continue
		}
		if h, ok := f.hops[from][t]; ok && h <= maxJumps {
			out[t] = h
		}
	}
	return out, nil
}

// termsSurface is one market surface holding every outcome the census can reach, so the terms are
// counted against each other and not each in its own private world:
//
//	AA-1  FUEL export, the source of every FUEL lane below
//	AA-3  FUEL import  — the WITHIN-system lane, counted but not cross-system
//	BB-1  FUEL import  — counted across a proven gate
//	BB-3  FUEL import  — counted across a proven gate
//	CC-*  FUEL imports — rich sinks nothing can prove a route to
//	AA-2 → BB-2  IRON, thin enough that its round trip's gates take it under the floor
func termsSurface(t *testing.T) *fakeLaneMarketReader {
	t.Helper()
	return &fakeLaneMarketReader{
		systems: map[string][]string{
			"X1-AA": {"X1-AA-1", "X1-AA-2", "X1-AA-3"},
			"X1-BB": {"X1-BB-1", "X1-BB-2", "X1-BB-3"},
			"X1-CC": {"X1-CC-1", "X1-CC-2", "X1-CC-3", "X1-CC-4"},
		},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", good(t, "FUEL", 50, 100, termsVolume, market.TradeTypeExport)),
			"X1-AA-2": mkt(t, "X1-AA-2", good(t, "IRON", 50, 100, termsVolume, market.TradeTypeExport)),
			"X1-AA-3": mkt(t, "X1-AA-3", good(t, "FUEL", 2000, 3000, termsVolume, market.TradeTypeImport)),
			"X1-BB-1": mkt(t, "X1-BB-1", good(t, "FUEL", 2000, 3000, termsVolume, market.TradeTypeImport)),
			"X1-BB-2": mkt(t, "X1-BB-2", good(t, "IRON", 1200, 1700, termsVolume, market.TradeTypeImport)),
			"X1-BB-3": mkt(t, "X1-BB-3", good(t, "FUEL", 1900, 2900, termsVolume, market.TradeTypeImport)),
			"X1-CC-1": mkt(t, "X1-CC-1", good(t, "FUEL", 2000, 3000, termsVolume, market.TradeTypeImport)),
			"X1-CC-2": mkt(t, "X1-CC-2", good(t, "FUEL", 1900, 2900, termsVolume, market.TradeTypeImport)),
			"X1-CC-3": mkt(t, "X1-CC-3", good(t, "FUEL", 1800, 2800, termsVolume, market.TradeTypeImport)),
			"X1-CC-4": mkt(t, "X1-CC-4", good(t, "FUEL", 1700, 2700, termsVolume, market.TradeTypeImport)),
		},
	}
}

const termsVolume = 50

// aaReachesBBOnly proves the one gate the fixture has: AA and BB, one hop apart. CC is in the
// scanned systems and in nobody's distance map.
func aaReachesBBOnly() laneReachTable {
	return laneReachTable{hops: map[string]map[string]int{
		"X1-AA": {"X1-BB": 1},
		"X1-BB": {"X1-AA": 1},
	}}
}

func termsCensus(t *testing.T) LaneCensus {
	t.Helper()
	census, readable, err := NewProfitableLaneReader(termsSurface(t), aaReachesBBOnly()).
		CountProfitableLanes(context.Background(), 1, []string{"X1-AA", "X1-BB", "X1-CC"})
	require.NoError(t, err)
	require.True(t, readable)
	return census
}

// THE COUNT ALONE CANNOT BE READ. A thin surface and a fleet that can reach nothing print the same
// small number, which is the blindness the parts of growth_lane_surface exist to remove — so the
// census reports what each narrowing guard refused beside what survived.
func TestCountProfitableLanes_ReportsWhatEachGuardRefused(t *testing.T) {
	census := termsCensus(t)

	require.Equal(t, 3, census.Profitable, "AA-1 feeds AA-3 within its system and BB-1/BB-3 across the gate")
	require.Equal(t, 2, census.CrossSystem, "two of the three counted lanes end in another system")
	require.Equal(t, 4, census.RejectedUnreachable, "the four CC sinks are rich and unprovable")
	require.Equal(t, 1, census.RejectedJumpCost, "IRON clears the floor until its round trip pays for the gate")
}

// The fixture proves itself: IRON is refused for its GATES and nothing else, so the arithmetic that
// makes it a jump-cost casualty is stated rather than assumed. A fixture that was sub-floor all
// along would report the same 1 and mean something else entirely.
func TestCountProfitableLanes_TheJumpCostTermIsCalibrated(t *testing.T) {
	const spread = 1200 - 100
	headroom := int64((spread - trading.MinBidMargin) * termsVolume)

	require.Positive(t, headroom, "calibration: IRON clears the floor before any gate is charged")
	require.Less(t, headroom, 2*domainSensing.DefaultGateFeeCredits,
		"calibration: what it has above the floor cannot pay for the round trip's two crossings")
}

// ONE HULL PER LANE IS A CLAIM ABOUT DEPTH, AND THE COUNT DOES NOT MAKE IT. The consumer sizes the
// trade pool at one hull per unserved lane, so an operator reading `unserved` is reading hull-trips
// of work — a reading that only holds while the lanes are as deep as a hold. The two surfaces below
// are indistinguishable by COUNT and differ by eighty times in what a hull could actually move.
func TestCountProfitableLanes_ReportsWhatTheCountedLanesCanMove(t *testing.T) {
	require.Equal(t, 3*termsVolume, termsCensus(t).AbsorbableUnits,
		"each of the three counted lanes absorbs one market's trade volume")

	oneLaneOfDepth := func(volume int) LaneCensus {
		markets := &fakeLaneMarketReader{
			systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2"}},
			markets: map[string]*market.Market{
				"X1-AA-1": mkt(t, "X1-AA-1", good(t, "FUEL", 50, 100, volume, market.TradeTypeExport)),
				// Spread 1001 — a credit over the floor, which is all the count ever asks.
				"X1-AA-2": mkt(t, "X1-AA-2", good(t, "FUEL", 1101, 1600, volume, market.TradeTypeImport)),
			},
		}
		census, readable, err := NewProfitableLaneReader(markets, aaReachesBBOnly()).
			CountProfitableLanes(context.Background(), 1, []string{"X1-AA"})
		require.NoError(t, err)
		require.True(t, readable)
		return census
	}

	sliver, hold := oneLaneOfDepth(1), oneLaneOfDepth(80)
	require.Equal(t, 1, sliver.Profitable)
	require.Equal(t, sliver.Profitable, hold.Profitable,
		"calibration: the count cannot tell a one-unit lane from a hull-sized one — both ask for a heavy")
	require.Equal(t, 1, sliver.AbsorbableUnits, "a 1,001-credit trip, published as the single unit it moves")
	require.Equal(t, 80, hold.AbsorbableUnits)
}

// unreachableSinks is one exporter in X1-AA against four sinks in a system nothing can prove a route
// to. The sinks' BID is the only variable, so the same four locked-out pairs are either worth flying
// or were never work.
func unreachableSinks(t *testing.T, bid int) *fakeLaneMarketReader {
	t.Helper()
	surface := &fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {"X1-AA-1"}},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", good(t, "FUEL", 50, 100, termsVolume, market.TradeTypeExport)),
		},
	}
	for i := 1; i <= 4; i++ {
		wp := fmt.Sprintf("X1-CC-%d", i)
		surface.systems["X1-CC"] = append(surface.systems["X1-CC"], wp)
		surface.markets[wp] = mkt(t, wp, good(t, "FUEL", bid, bid+1000, termsVolume, market.TradeTypeImport))
	}
	return surface
}

// THE TERM MUST SUPPORT THE READING ITS HELP TEXT PRESCRIBES. An operator is told that a thin
// surface with rejected_unreachable large is a reachability failure and not a poor market — a
// reading the term can only carry if it counts pairs that WOULD have been work. Incremented for
// every routeless pair it prints the same large number whether the charting work it charters would
// unlock four rich lanes or four that were never worth flying.
func TestCountProfitableLanes_TheUnreachableTermCountsOnlyPairsThatWouldHaveBeenWork(t *testing.T) {
	census := func(bid int) LaneCensus {
		c, readable, err := NewProfitableLaneReader(unreachableSinks(t, bid), aaReachesBBOnly()).
			CountProfitableLanes(context.Background(), 1, []string{"X1-AA", "X1-CC"})
		require.NoError(t, err)
		require.True(t, readable)
		require.Zero(t, c.Profitable, "calibration: nothing proves a route to X1-CC, so none of these count")
		return c
	}

	require.Equal(t, 4, census(2000).RejectedUnreachable, "four lanes worth flying, locked out by the missing route")
	require.Zero(t, census(800).RejectedUnreachable,
		"the same four pairs at a sub-floor bid were never work, and charting a gate to them unlocks nothing")
}

// A SINK THAT ABSORBS NOTHING IS NOT A GATE-FEE CASUALTY EITHER. Zero units earn zero at any spread,
// so the crossing refused nothing — blamed on the gates it reports a fee pricing lanes out where
// there is no cargo to move. This is the half of the shared counterfactual that a spread-only test
// (ClearsFloor) silently drops: it is above the floor, and it is not work.
func TestCountProfitableLanes_AZeroAbsorptionSinkIsNotBlamedOnTheGates(t *testing.T) {
	exporter := good(t, "FUEL", 50, 100, termsVolume, market.TradeTypeExport)
	acrossOneGate := func(sink market.TradeGood) LaneCensus {
		census, readable, err := NewProfitableLaneReader(oneLaneAcross(t, exporter, sink), aaReachesBBOnly()).
			CountProfitableLanes(context.Background(), 1, []string{"X1-AA", "X1-BB"})
		require.NoError(t, err)
		require.True(t, readable)
		return census
	}

	dead := acrossOneGate(good(t, "FUEL", 9000, 12000, 0, market.TradeTypeImport))
	require.Zero(t, dead.Profitable, "a sink that absorbs nothing is not a lane however rich its quote")
	require.Zero(t, dead.RejectedJumpCost, "and it is not the crossing that refused it")
	require.Zero(t, dead.RejectedUnreachable, "the crossing itself WAS proven, both ways")

	live := acrossOneGate(good(t, "FUEL", 9000, 12000, termsVolume, market.TradeTypeImport))
	require.Equal(t, 1, live.Profitable,
		"calibration: the same quote over a sink that can absorb a cargo is a lane, so the gate is genuinely there")
}

// A lane nothing could have flown at any distance is NOT a jump-cost casualty. Attributing it to
// the gates would print a fee problem where the market is simply thin, and the two call for
// opposite responses — the reason to publish the term at all.
func TestCountProfitableLanes_ASubFloorLaneIsNotBlamedOnTheGates(t *testing.T) {
	exporter, importer := subFloorPair(t, "ICE")
	reader := NewProfitableLaneReader(oneLaneAcross(t, exporter, importer), aaReachesBBOnly())

	census, readable, err := reader.CountProfitableLanes(context.Background(), 1, []string{"X1-AA", "X1-BB"})
	require.NoError(t, err)
	require.True(t, readable)
	require.Zero(t, census.Profitable)
	require.Zero(t, census.RejectedJumpCost, "the spread was never above the floor; the crossing refused nothing")
	require.Zero(t, census.RejectedUnreachable, "the crossing WAS proven — this lane died on its prices")
}

// A blind census carries no terms. Publishing them from a reading that failed would explain a count
// nobody has, and a fail-closed path must say nothing rather than something stale (RULINGS #4).
func TestCountProfitableLanes_AFailedCensusReportsNoTerms(t *testing.T) {
	reach := reachAllWithin(1)
	reach.err = context.DeadlineExceeded
	reader := NewProfitableLaneReader(termsSurface(t), reach)

	census, readable, err := reader.CountProfitableLanes(context.Background(), 1, []string{"X1-AA", "X1-BB"})
	require.Error(t, err)
	require.False(t, readable)
	require.Equal(t, LaneCensus{}, census)
}

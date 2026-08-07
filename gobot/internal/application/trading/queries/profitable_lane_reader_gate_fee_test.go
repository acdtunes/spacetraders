package queries

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// oneLaneAcross builds a single-good surface: the exporter alone in X1-AA, the importer alone in
// X1-BB. There is exactly one lane in it, so a census over this surface answers 1 or 0 and nothing
// else can move the number.
func oneLaneAcross(t *testing.T, exporter, importer market.TradeGood) *fakeLaneMarketReader {
	t.Helper()
	return &fakeLaneMarketReader{
		systems: map[string][]string{"X1-AA": {"X1-AA-1"}, "X1-BB": {"X1-BB-1"}},
		markets: map[string]*market.Market{
			"X1-AA-1": mkt(t, "X1-AA-1", exporter),
			"X1-BB-1": mkt(t, "X1-BB-1", importer),
		},
	}
}

// RULINGS #4: MinBidMargin is the discipline the executor holds every visit to, and a crossing
// spends credits before the first unit is sold. Netting the fee against BREAK-EVEN only asks
// whether the trip out-earns its gates; the floor has to survive the crossing too, or the census
// counts a lane whose realised per-unit margin the executor will refuse, and sizes a hull purchase
// against it.
//
// The lane below is exactly the shape a break-even test misses: its trip out-earns one gate several
// times over and still has nothing left above the floor. The same lane inside one system pays no
// gate and IS work — that leg is the calibration, so the zero across the gate is the deduction and
// not a dead fixture.
func TestCountProfitableLanes_TheFloorMustSurviveTheCrossing(t *testing.T) {
	const volume = 50
	exporter := good(t, "FUEL", 50, 100, volume, market.TradeTypeExport)
	importer := good(t, "FUEL", 1100, 1600, volume, market.TradeTypeImport)

	spread := importer.SellPrice() - exporter.PurchasePrice()
	require.Equal(t, trading.MinBidMargin, spread, "calibration: the lane sits exactly on the floor")
	require.Greater(t, int64(spread*volume), domainSensing.DefaultGateFeeCredits,
		"calibration: the trip DOES out-earn one gate — what it cannot do is out-earn it and keep the floor")

	t.Run("across a gate the floor is gone", func(t *testing.T) {
		count, readable, err := countOverRealGateGraph(t, oneLaneAcross(t, exporter, importer),
			&laneGateStore{adjacency: gatedChain("X1-AA", "X1-BB")}, "X1-AA", "X1-BB")
		require.NoError(t, err)
		require.True(t, readable, "a lane priced out by its gate is a readable ZERO, not an outage")
		require.Zero(t, count, "what is left after the crossing is below the floor the executor enforces")
	})

	t.Run("inside one system there is nothing to deduct", func(t *testing.T) {
		markets := &fakeLaneMarketReader{
			systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2"}},
			markets: map[string]*market.Market{
				"X1-AA-1": mkt(t, "X1-AA-1", exporter),
				"X1-AA-2": mkt(t, "X1-AA-2", importer),
			},
		}
		count, readable, err := countOverRealGateGraph(t, markets,
			&laneGateStore{adjacency: map[string][]domainSystem.GateEdge{}}, "X1-AA")
		require.NoError(t, err)
		require.True(t, readable)
		require.Equal(t, 1, count, "calibration: the same prices with no gate to pay are a lane")
	})
}

// A MULTI-JUMP ROUTE PAYS ONE FEE PER GATE CROSSED. The two legs below share one market surface and
// differ only in how many gates the stored graph puts between its ends, so the hop arithmetic is the
// only thing that can separate them. Charging a flat crossing however far the lane runs counts the
// two-gate leg as work, and the hull bought for it flies a trip that loses money.
//
// The counted unit is the hull's round trip, so a lane N gates out is charged 2N crossings: the
// fixture has room for the one-gate circuit's two and not for the two-gate circuit's four.
func TestCountProfitableLanes_EveryGateCrossedIsPaidFor(t *testing.T) {
	const volume = 50
	exporter := good(t, "FUEL", 50, 100, volume, market.TradeTypeExport)
	importer := good(t, "FUEL", 1400, 1900, volume, market.TradeTypeImport)

	spread := importer.SellPrice() - exporter.PurchasePrice()
	headroom := int64((spread - trading.MinBidMargin) * volume)
	require.GreaterOrEqual(t, headroom, 2*domainSensing.DefaultGateFeeCredits,
		"calibration: above the floor this trip has room for exactly one gate, crossed both ways")
	require.Less(t, headroom, 4*domainSensing.DefaultGateFeeCredits,
		"calibration: it has no room for a second gate's two crossings, so the legs must disagree")

	one, readable, err := countOverRealGateGraph(t, oneLaneAcross(t, exporter, importer),
		&laneGateStore{adjacency: gatedChain("X1-AA", "X1-BB")}, "X1-AA", "X1-BB")
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 1, one, "one gate, crossed both ways, is a charge this lane can carry")

	two, readable, err := countOverRealGateGraph(t, oneLaneAcross(t, exporter, importer),
		&laneGateStore{adjacency: gatedChain("X1-AA", "X1-MID", "X1-BB")}, "X1-AA", "X1-BB")
	require.NoError(t, err)
	require.True(t, readable)
	require.Zero(t, two, "the same lane two gates away pays twice as much and cannot carry it")
}

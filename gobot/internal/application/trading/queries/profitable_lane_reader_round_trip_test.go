package queries

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// WHAT THE CENSUS COUNTS IS A CIRCUIT, NOT A LEG. Each counted lane asks for one hull, and that hull
// flies the lane over and over: out with a cargo, back to re-buy. The jump fee is a per-ORIGIN-gate
// charge, so the return crossing is billed too — the same round trip the circuit time model prices
// (crossSystemRoundTripHops). Charging the outbound leg alone counts a band of lanes whose real
// circuit is under the floor, and buys a heavy for each of them (RULINGS #4).
//
// The lane below is exactly that band: one crossing fits in what it has above the floor and two do
// not, so only the return can decide it. The in-system leg crosses nothing and IS work — that is the
// calibration, so the zero across the gate is the return charge and not a dead fixture.
func TestCountProfitableLanes_TheReturnCrossingIsPaidForToo(t *testing.T) {
	const volume = 50
	exporter := good(t, "FUEL", 50, 100, volume, market.TradeTypeExport)
	importer := good(t, "FUEL", 1300, 1800, volume, market.TradeTypeImport)

	spread := importer.SellPrice() - exporter.PurchasePrice()
	headroom := int64((spread - trading.MinBidMargin) * volume)
	require.GreaterOrEqual(t, headroom, domainSensing.DefaultGateFeeCredits,
		"calibration: the outbound crossing alone fits, so only the RETURN can decide this lane")
	require.Less(t, headroom, 2*domainSensing.DefaultGateFeeCredits,
		"calibration: the round trip does not fit")

	t.Run("one gate away the circuit crosses twice", func(t *testing.T) {
		count, readable, err := countOverRealGateGraph(t, oneLaneAcross(t, exporter, importer),
			&laneGateStore{adjacency: gatedChain("X1-AA", "X1-BB")}, "X1-AA", "X1-BB")
		require.NoError(t, err)
		require.True(t, readable, "a lane priced out by its return leg is a readable ZERO, not an outage")
		require.Zero(t, count, "the hull pays a crossing each way for every cargo it sells")
	})

	t.Run("inside one system there is no crossing either way", func(t *testing.T) {
		markets := &fakeLaneMarketReader{
			systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2"}},
			markets: map[string]*market.Market{
				"X1-AA-1": mkt(t, "X1-AA-1", exporter),
				"X1-AA-2": mkt(t, "X1-AA-2", importer),
			},
		}
		count, readable, err := countOverRealGateGraph(t, markets,
			&laneGateStore{adjacency: gatedChain("X1-AA")}, "X1-AA")
		require.NoError(t, err)
		require.True(t, readable)
		require.Equal(t, 1, count, "calibration: the same prices with no gate to pay are a lane")
	})
}

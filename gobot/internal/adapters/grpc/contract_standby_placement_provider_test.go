package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The coordinator's between-legs placement provider resolves the SAME home-system role lookup the
// scaler buys against (ONE selection), coord-dedups the central parks (distinct LOCATIONS), and returns
// the ≤6 FIXED delivery placement slots (ranked highest-demand first, capped at the knee). This exercises
// the whole adapter pipeline (geometry + market roles → ResolveRoles → dedup → TopDeliverySlots):
//   - co-located parks (a planet + its moon at one coordinate) collapse to the highest-demand
//     representative — G49(340) kept, G47(180) dropped;
//   - a distinct-location park stays — K83(260);
//   - the far-outlier importer (the J sink) is EXCLUDED (far-band importer, not a central park — served
//     live, no J depot); the far exporter (a source) is excluded (not an importer/park);
//   - the result is ORDERED highest-demand first — the fixed placement set.
func TestContractStandbyPlacementProvider_DedupedFixedPlacementSlots(t *testing.T) {
	waypoints := []*shared.Waypoint{
		wp("X1-SC-G49", 54, -33), // co-located pair — one planet
		wp("X1-SC-G47", 54, -33),
		wp("X1-SC-K83", 8, 104),       // distinct central location
		wp("X1-SC-SOURCE1", 330, 0),   // far exporter → far source (not a park)
		wp("X1-SC-JSINK70", 700, 100), // far-outlier importer → far sink, served live (NOT a park)
	}
	markets := map[string]*market.Market{
		"X1-SC-G49":     buildMarket(t, "X1-SC-G49", goodSpec{"FUEL", market.TradeTypeImport, 200}, goodSpec{"FOOD", market.TradeTypeImport, 140}),
		"X1-SC-G47":     buildMarket(t, "X1-SC-G47", goodSpec{"MACHINERY", market.TradeTypeImport, 180}),
		"X1-SC-K83":     buildMarket(t, "X1-SC-K83", goodSpec{"CLOTHING", market.TradeTypeImport, 260}),
		"X1-SC-SOURCE1": buildMarket(t, "X1-SC-SOURCE1", goodSpec{"IRON_ORE", market.TradeTypeExport, 40}),
		"X1-SC-JSINK70": buildMarket(t, "X1-SC-JSINK70", goodSpec{"ELECTRONICS", market.TradeTypeImport, 500}),
	}

	provider := &contractStandbyPlacementProvider{resolver: &contractScalerRoleResolver{
		home:      &fakeHomeReader{system: "X1-SC", readable: true},
		waypoints: &fakeWaypointLister{waypoints: waypoints},
		markets:   &fakeMarketReader{byWaypoint: markets},
	}}

	got, err := provider.StandbyPlacement(context.Background(), 1)
	require.NoError(t, err)

	// G49(340) > K83(260), demand-ranked; G47 deduped away; SOURCE1/JSINK70 excluded.
	require.Equal(t, []string{"X1-SC-G49", "X1-SC-K83"}, got)
}

// Fail-safe: an unresolvable/unscanned home system yields an empty slice (no error), so the
// coordinator's homing keeps its passed set rather than erroring — a positioning read, not a spend.
func TestContractStandbyPlacementProvider_UnreadableHomeYieldsEmpty(t *testing.T) {
	provider := &contractStandbyPlacementProvider{resolver: &contractScalerRoleResolver{
		home:      &fakeHomeReader{system: "", readable: false},
		waypoints: &fakeWaypointLister{},
		markets:   &fakeMarketReader{byWaypoint: map[string]*market.Market{}},
	}}

	got, err := provider.StandbyPlacement(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, got)
}

// wpMarket builds a charted MARKETPLACE waypoint (the durable trait), independent of whether its
// per-good market has been dock-scanned — the fixture for a central sink whose imports are not yet in
// market_data.
func wpMarket(symbol string, x, y float64) *shared.Waypoint {
	return &shared.Waypoint{Symbol: symbol, X: x, Y: y, Traits: []string{"MARKETPLACE"}}
}

// KEEP isCentralSink UNDER FIXED PLACEMENT: eight distinct inner-band charted
// MARKETPLACES, only four dock-scanned this pass. Classifying central parks by the DURABLE marketplace
// trait keeps the four UNSCANNED bands (E/F/D/C) as placement CANDIDATES — without it only the four
// scanned importers would be candidates and the op would cap at four slots. The fixed placement then
// selects the ≤6 knee across ALL distinct bands: the four scanned (demand-ranked) plus two of the
// unscanned (symbol-ranked among the zero-demand tail), so the two lowest-symbol unscanned bands make
// the cut while the surplus over the knee is dropped — never a pile onto the scanned subset.
func TestContractStandbyPlacementProvider_UnscannedBandsAreCandidates_CappedAtKnee(t *testing.T) {
	waypoints := []*shared.Waypoint{
		wpMarket("X1-UM5-A1", -22, 15),
		wpMarket("X1-UM5-K83", 8, 104),
		wpMarket("X1-UM5-G49", 54, -33),
		wpMarket("X1-UM5-H55", -33, -31),
		wpMarket("X1-UM5-E43", 50, 24),
		wpMarket("X1-UM5-F46", -74, 13),
		wpMarket("X1-UM5-D40", -73, -39),
		wpMarket("X1-UM5-C38", 149, -36),
	}
	markets := map[string]*market.Market{
		"X1-UM5-A1":  buildMarket(t, "X1-UM5-A1", goodSpec{"ELECTRONICS", market.TradeTypeImport, 146}),
		"X1-UM5-K83": buildMarket(t, "X1-UM5-K83", goodSpec{"CLOTHING", market.TradeTypeImport, 260}),
		"X1-UM5-G49": buildMarket(t, "X1-UM5-G49", goodSpec{"FUEL", market.TradeTypeImport, 340}),
		"X1-UM5-H55": buildMarket(t, "X1-UM5-H55", goodSpec{"FOOD", market.TradeTypeImport, 240}),
		// E43/F46/D40/C38: charted marketplaces, no dock-scan → GetMarketData returns nil (demand 0).
	}
	provider := &contractStandbyPlacementProvider{resolver: &contractScalerRoleResolver{
		home:      &fakeHomeReader{system: "X1-UM5", readable: true},
		waypoints: &fakeWaypointLister{waypoints: waypoints},
		markets:   &fakeMarketReader{byWaypoint: markets},
	}}

	got, err := provider.StandbyPlacement(context.Background(), 1)
	require.NoError(t, err)

	// The ≤6 knee across all distinct bands: 4 scanned (demand-ranked G49>K83>H55>A1) + the 2
	// lowest-symbol unscanned candidates (C38, D40); E43/F46 dropped by the knee.
	require.Equal(t, []string{"X1-UM5-G49", "X1-UM5-K83", "X1-UM5-H55", "X1-UM5-A1", "X1-UM5-C38", "X1-UM5-D40"}, got)
}

var _ appContract.StandbyPlacementProvider = (*contractStandbyPlacementProvider)(nil)

package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The coordinator's between-legs homing demand provider resolves the SAME home-system
// role lookup + import-volume demand the scaler buys against (ONE demand definition), then
// coord-dedups the central parks so idle hulls spread across distinct LOCATIONS. This
// exercises the whole adapter pipeline (geometry + market roles → ResolveRoles → dedup):
//   - co-located parks (a planet + its moon at one coordinate) collapse to the highest-
//     demand representative — G49(340) kept, G47(180) dropped;
//   - a distinct-location park stays — K83(260);
//   - the far-outlier importer (the J sink) is EXCLUDED despite a HIGH import volume: it is
//     a far-band importer, not a central park (no J depot — served live);
//   - the far exporter (a source) is excluded (not an importer/park).
func TestContractStandbyDemandProvider_DedupedCentralParkDemandOnly(t *testing.T) {
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

	provider := &contractStandbyDemandProvider{resolver: &contractScalerRoleResolver{
		home:      &fakeHomeReader{system: "X1-SC", readable: true},
		waypoints: &fakeWaypointLister{waypoints: waypoints},
		markets:   &fakeMarketReader{byWaypoint: markets},
	}}

	got, err := provider.StandbyDemand(context.Background(), 1)
	require.NoError(t, err)

	want := map[string]float64{"X1-SC-G49": 340, "X1-SC-K83": 260}
	require.Equal(t, want, got)
}

// Fail-safe: an unresolvable/unscanned home system yields an empty demand map (no error),
// so the coordinator's homing degrades to plain occupancy+nearest balancing rather than
// erroring — a positioning read, not a spend.
func TestContractStandbyDemandProvider_UnreadableHomeYieldsEmpty(t *testing.T) {
	provider := &contractStandbyDemandProvider{resolver: &contractScalerRoleResolver{
		home:      &fakeHomeReader{system: "", readable: false},
		waypoints: &fakeWaypointLister{},
		markets:   &fakeMarketReader{byWaypoint: map[string]*market.Market{}},
	}}

	got, err := provider.StandbyDemand(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, got)
}

var _ appContract.StandbyDemandProvider = (*contractStandbyDemandProvider)(nil)

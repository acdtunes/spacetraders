package grpc

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
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

// wpMarket builds a charted MARKETPLACE waypoint (the durable trait), independent of
// whether its per-good market has been dock-scanned — the fixture for a central sink
// whose imports are not yet in market_data.
func wpMarket(symbol string, x, y float64) *shared.Waypoint {
	return &shared.Waypoint{Symbol: symbol, X: x, Y: y, Traits: []string{"MARKETPLACE"}}
}

// ACCEPTANCE (sp-ojp32): the live pile-up — 8 idle delivery hulls clustered on ~4 central
// parks (A1×3, K83×2, G49, H55) while the E/F/D/C central bands sat EMPTY. The coordinator's
// standby_stations config is null, so homing falls to the ResolveStandbyForHoming auto-fill;
// that auto-fill resolved only the central parks whose IMPORT markets were currently dock-scanned
// (~4), so the occupancy-aware distribution spread 8 hulls across just 4 waypoints. Classifying
// central parks by the DURABLE marketplace trait brings every distinct central band into the
// standby set, so N idle hulls land on N DISTINCT central waypoints (one per band, no clustering).
func TestContractStandbyDemandProvider_UnscannedCentralBandsJoinTheSpread(t *testing.T) {
	// Eight distinct inner-band MARKETPLACE locations (the era-4 A/H/E/G/F/D/K/C bands). Only
	// four (A/K/G/H) have a dock-scanned import market this pass; the other four (E/F/D/C) are
	// charted marketplaces whose imports have not been scanned yet — the live "empty" bands.
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
		// E43/F46/D40/C38: charted marketplaces, no dock-scan → GetMarketData returns nil.
	}
	provider := &contractStandbyDemandProvider{resolver: &contractScalerRoleResolver{
		home:      &fakeHomeReader{system: "X1-UM5", readable: true},
		waypoints: &fakeWaypointLister{waypoints: waypoints},
		markets:   &fakeMarketReader{byWaypoint: markets},
	}}

	// The REAL auto-fill path the null-standby_stations coordinator falls to: an EMPTY operator
	// set is auto-driven by the role-resolved central parks (standby_demand.go).
	stations, demand := appContract.ResolveStandbyForHoming(context.Background(), nil, provider, 1, nil)
	require.Len(t, stations, 8, "auto-filled standby set must cover all 8 distinct central bands, not just the dock-scanned importers")

	// Eight idle hulls distribute across the resolved set. With the full 8-park set they land on
	// eight DISTINCT central waypoints; with the short 4-park set the same 8 hulls piled 2-per-park.
	waypointSet := make([]domainContract.StandbyWaypoint, 0, len(stations))
	for _, s := range stations {
		waypointSet = append(waypointSet, domainContract.StandbyWaypoint{Symbol: s, DemandWeight: demand[s]})
	}
	hulls := make([]domainContract.IdleHullToPlace, 0, 8)
	for i := 0; i < 8; i++ {
		hulls = append(hulls, domainContract.IdleHullToPlace{ShipSymbol: fmt.Sprintf("HULL-%d", i)})
	}
	placement := domainContract.DistributeIdleHullsAcrossStandby(hulls, waypointSet, nil)

	landed := map[string]bool{}
	for _, wp := range placement {
		landed[wp] = true
	}
	require.Len(t, landed, 8, "8 idle hulls must land on 8 DISTINCT central waypoints (one per band), not cluster onto the scanned subset")
}

var _ appContract.StandbyDemandProvider = (*contractStandbyDemandProvider)(nil)

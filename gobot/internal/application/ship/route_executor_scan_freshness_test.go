package ship_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-v34b behavior 1: the recent-scan freshness gate. The route executor scans a market
// on every arrival (RouteExecutor.scanMarketIfPresent → GetMarket). A trade hull arrives
// at a market it (or another hull) scanned seconds ago — the measured "same market
// re-scanned 4s apart" that made this the top API consumer. Under a trade coordinator's
// ScanPolicy, an arrival at a market scanned within MaxScanAge reuses the cache instead
// of re-calling GetMarket. The freshness-scout recovery path stamps NO policy, so its
// arrival scans are ungated (its recovery/decay dataset is untouched). These drive the
// REAL RouteExecutor.ExecuteRoute driving port and assert on the observable outcome: the
// live GetMarket call count.

// countingMarketAPI is the driven-port stub at the ONLY external boundary the arrival
// scan crosses: the live GetMarket. gets records the call count, which is the whole
// observable of the freshness gate — a skipped scan is a GetMarket that never fired.
type countingMarketAPI struct {
	domainPorts.APIClient
	gets int
}

func (c *countingMarketAPI) GetMarket(_ context.Context, _, waypointSymbol, _ string) (*domainPorts.MarketData, error) {
	c.gets++
	return &domainPorts.MarketData{
		Symbol: waypointSymbol,
		TradeGoods: []domainPorts.TradeGoodData{
			{Symbol: "IRON_ORE", Supply: "MODERATE", Activity: "WEAK", SellPrice: 200, PurchasePrice: 100, TradeVolume: 1000, TradeType: "EXPORT"},
		},
	}, nil
}

// freshnessFakeMarketRepo serves the cached market the gate reads to decide whether the
// arrival scan may be skipped. lastUpdated drives the age: zero returns nil (never
// scanned → must scan), a recent value is FRESH (reuse), an old value is STALE (re-scan).
// UpsertMarketData is a no-op sink for the scans that do proceed.
type freshnessFakeMarketRepo struct {
	scoutingQuery.MarketRepository
	waypoint    string
	lastUpdated time.Time
}

func (r *freshnessFakeMarketRepo) GetMarketData(_ context.Context, _ string, _ int) (*market.Market, error) {
	if r.lastUpdated.IsZero() {
		return nil, nil
	}
	supply, activity := "MODERATE", "WEAK"
	g, err := market.NewTradeGood("IRON_ORE", &supply, &activity, 100, 200, 1000, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(r.waypoint, []market.TradeGood{*g}, r.lastUpdated)
}

func (r *freshnessFakeMarketRepo) UpsertMarketData(_ context.Context, _ uint, _ string, _ []market.TradeGood, _ time.Time) error {
	return nil
}

// runArrivalScan drives one real single-leg ExecuteRoute to a marketplace and returns
// the number of live GetMarket calls the arrival scan made under the given policy.
func runArrivalScan(t *testing.T, cachedLastUpdated time.Time, policy *shared.ScanPolicy) int {
	t.Helper()
	const marketWaypoint = "X1-RTE-MKT"

	api := &countingMarketAPI{}
	marketRepo := &freshnessFakeMarketRepo{waypoint: marketWaypoint, lastUpdated: cachedLastUpdated}
	scanner := ship.NewMarketScanner(api, marketRepo, nil, nil)

	executor := ship.NewRouteExecutor(
		nil,
		&succeedingMediator{fuelCapacity: 400},
		nil,
		scanner,
		nil, // no shipyard scanner
		nil,
		nil,
		noopSubscriber{},
	)

	from := mustTestWaypoint(t, "X1-RTE-A", 0, 0)
	to := mustTestWaypoint(t, marketWaypoint, 10, 0)
	to.Traits = []string{"MARKETPLACE"}
	segment := domainNavigation.NewRouteSegment(from, to, 10, 10, 0, shared.FlightModeCruise, false)

	playerID := shared.MustNewPlayerID(2)
	route, err := domainNavigation.NewRoute("route-1", "SCOUT-1", 2, []*domainNavigation.RouteSegment{segment}, 400, false)
	require.NoError(t, err)
	shipEntity := newScoutShip(t, from, playerID)

	ctx := common.WithPlayerToken(context.Background(), "test-token")
	if policy != nil {
		ctx = shared.WithScanPolicy(ctx, *policy)
	}
	require.NoError(t, executor.ExecuteRoute(ctx, route, shipEntity, playerID))
	return api.gets
}

// THE RED case: arriving at a market scanned <N ago under a policy reuses the cache — no
// redundant live GetMarket. This is the top API consumer sp-v34b sheds.
func TestArrivalScan_FreshlyScannedMarket_SkipsGetMarket(t *testing.T) {
	gets := runArrivalScan(t, time.Now(), &shared.ScanPolicy{MaxScanAge: 90 * time.Second})
	require.Equal(t, 0, gets,
		"arriving at a market scanned within MaxScanAge must reuse the cache, not re-call GetMarket")
}

// A STALE cache (older than the gate) still scans on arrival: the trade needs
// fresh-enough prices. Proves the gate is not over-aggressive.
func TestArrivalScan_StaleMarket_ScansGetMarket(t *testing.T) {
	gets := runArrivalScan(t, time.Now().Add(-10*time.Minute), &shared.ScanPolicy{MaxScanAge: 90 * time.Second})
	require.Equal(t, 1, gets, "a stale cached market must still be re-scanned on arrival")
}

// A never-scanned market (no cached row) always scans — there is nothing to reuse.
func TestArrivalScan_NeverScannedMarket_ScansGetMarket(t *testing.T) {
	gets := runArrivalScan(t, time.Time{}, &shared.ScanPolicy{MaxScanAge: 90 * time.Second})
	require.Equal(t, 1, gets, "a never-scanned market must be scanned on arrival")
}

// Deploy-safety: with NO policy stamped (the freshness-scout recovery path and every
// pre-sp-v34b caller) the arrival scan is unconditional — byte-for-byte pre-sp-v34b —
// so the recovery/decay dataset the scout collects is untouched. And the mutation
// (MaxScanAge=0) proves the gate is load-bearing: turn it off and every arrival scans
// even at a freshly-cached market.
func TestArrivalScan_UngatedPaths_AlwaysScan(t *testing.T) {
	require.Equal(t, 1, runArrivalScan(t, time.Now(), nil),
		"no policy (scout recovery path / pre-sp-v34b) must scan unconditionally on arrival")
	require.Equal(t, 1, runArrivalScan(t, time.Now(), &shared.ScanPolicy{MaxScanAge: 0}),
		"mutation: MaxScanAge=0 disables the gate — a fresh market still scans (gate is load-bearing)")
}

package commands

// The scout fleet is the fleet's freshness mechanism, so these tests pin exactly
// how far the dedup window may reach: it removes a SECOND observation of a market
// another hull just recorded, and nothing else. A market outside the window, a
// market never scanned, and an unreadable cache all still scan.

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// dedupMarketStore serves the cached market the window is judged against. It is
// deliberately ADVERSARIAL: when readErr is set it STILL returns a perfectly
// fresh market, so a gate that ignores the error skips a scan it owed.
type dedupMarketStore struct {
	waypoint    string
	lastUpdated time.Time
	readErr     error
	upserts     int
}

func (f *dedupMarketStore) GetMarketData(context.Context, string, int) (*market.Market, error) {
	if f.lastUpdated.IsZero() && f.readErr == nil {
		return nil, nil
	}
	supply, activity := "MODERATE", "WEAK"
	g, err := market.NewTradeGood("IRON_ORE", &supply, &activity, 100, 200, 1000, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	stamp := f.lastUpdated
	if stamp.IsZero() {
		stamp = time.Now()
	}
	m, err := market.NewMarket(f.waypoint, []market.TradeGood{*g}, stamp)
	if err != nil {
		return nil, err
	}
	return m, f.readErr
}

func (f *dedupMarketStore) UpsertMarketData(context.Context, uint, string, []market.TradeGood, time.Time) error {
	f.upserts++
	return nil
}

func (f *dedupMarketStore) ListMarketsInSystem(context.Context, uint, string, int) ([]market.Market, error) {
	return nil, nil
}

// countingScoutAPI records the live GetMarket calls — the whole observable of
// the window, since a suppressed scan is a GetMarket that never fired.
type countingScoutAPI struct {
	domainPorts.APIClient
	marketGets int
}

func (a *countingScoutAPI) GetMarket(_ context.Context, _, waypointSymbol, _ string) (*domainPorts.MarketData, error) {
	a.marketGets++
	return &domainPorts.MarketData{Symbol: waypointSymbol}, nil
}

func (a *countingScoutAPI) GetShipyard(_ context.Context, _, _, _ string) (*domainPorts.ShipyardData, error) {
	return &domainPorts.ShipyardData{}, nil
}

// ctxCapturingMediator records the context the tour hands to navigation — the
// same context the route executor's arrival scan reads its window from.
type ctxCapturingMediator struct {
	captured context.Context
}

func (m *ctxCapturingMediator) Send(ctx context.Context, _ mediator.Request) (mediator.Response, error) {
	if m.captured == nil {
		m.captured = ctx
	}
	return &shipNav.NavigateRouteResponse{Status: "completed"}, nil
}

func (m *ctxCapturingMediator) Register(reflect.Type, mediator.RequestHandler) error { return nil }

func (m *ctxCapturingMediator) RegisterMiddleware(mediator.Middleware) {}

// runStationaryScout drives one real stationary tour visit at scoutedYard
// against a cache of the given age and returns the live GetMarket count.
func runStationaryScout(t *testing.T, store *dedupMarketStore, window time.Duration) (*countingScoutAPI, *dedupMarketStore) {
	t.Helper()
	api := &countingScoutAPI{}
	marketScanner := ship.NewMarketScanner(api, store, nil, nil)
	shipyardScanner := ship.NewShipyardScanner(
		api, newFakeInventoryStore(),
		&fakeWaypointTraits{waypoints: map[string]*shared.Waypoint{}},
		nil, domainShipyard.NewHeavyShipTypeSet(nil), 0,
	)
	h := NewScoutTourHandler(&fakeTourShipRepo{ship: scoutAt(t, scoutedYard)}, nil, marketScanner, shipyardScanner, &shared.MockClock{CurrentTime: time.Now()})
	h.SetScanDedupWindow(window)

	_, err := h.Handle(tourCtx(), &ScoutTourCommand{
		PlayerID:           shared.MustNewPlayerID(1),
		ShipSymbol:         "PROBE-1",
		Markets:            []string{scoutedYard},
		Iterations:         1,
		StartJitterMaxSecs: 1,
	})
	require.NoError(t, err)
	return api, store
}

// Two scouts on overlapping routes reaching one market a minute apart both paid
// for the same observation. The second must reuse the first's row.
func TestScoutTour_MarketScannedInsideWindow_SkipsLiveRead(t *testing.T) {
	store := &dedupMarketStore{waypoint: scoutedYard, lastUpdated: time.Now().Add(-30 * time.Second)}

	api, store := runStationaryScout(t, store, 75*time.Second)

	require.Zero(t, api.marketGets, "a market refreshed inside the window must not be re-read")
	require.Zero(t, store.upserts, "a suppressed scan must write nothing")
}

// The window only removes redundant observations. A market older than it is the
// scout fleet's actual job and must always be re-read.
func TestScoutTour_MarketBeyondWindow_StillScans(t *testing.T) {
	store := &dedupMarketStore{waypoint: scoutedYard, lastUpdated: time.Now().Add(-10 * time.Minute)}

	api, _ := runStationaryScout(t, store, 75*time.Second)

	require.Equal(t, 1, api.marketGets, "a market older than the window must still be scanned")
}

// A market with no cached row at all has nothing to reuse.
func TestScoutTour_NeverScannedMarket_StillScans(t *testing.T) {
	store := &dedupMarketStore{waypoint: scoutedYard}

	api, _ := runStationaryScout(t, store, 75*time.Second)

	require.Equal(t, 1, api.marketGets, "a never-scanned market must always be scanned")
}

// An unreadable cache is not evidence of freshness. The store returns a fresh
// market alongside the error, so a gate that trusts the value over the error
// wrongly suppresses the scan.
func TestScoutTour_UnreadableCache_StillScans(t *testing.T) {
	store := &dedupMarketStore{waypoint: scoutedYard, lastUpdated: time.Now(), readErr: errors.New("db down")}

	api, _ := runStationaryScout(t, store, 75*time.Second)

	require.Equal(t, 1, api.marketGets, "an unreadable cache must not grant a skip")
}

// SUPERSEDED BY sp-ntgfj. With no window wired the scout path used to scan on
// every visit however fresh the cache; it is now paced by the fleet's one
// market-scan budget like every other reader, so a just-scanned market is served
// from store. An unset caller-level window no longer means an ungated scan —
// there is no ungated market scan left in the daemon.
func TestScoutTour_NoWindowConfigured_IsStillPacedByTheFleetBudget(t *testing.T) {
	fresh := &dedupMarketStore{waypoint: scoutedYard, lastUpdated: time.Now()}
	api, _ := runStationaryScout(t, fresh, 0)
	require.Equal(t, 0, api.marketGets,
		"an unset window cannot opt the scout out of the fleet market-scan budget")

	stale := &dedupMarketStore{waypoint: scoutedYard, lastUpdated: time.Now().Add(-30 * time.Minute)}
	staleAPI, _ := runStationaryScout(t, stale, 0)
	require.Equal(t, 1, staleAPI.marketGets,
		"and a market past its interval is still scouted, so the budget is pacing rather than blocking")
}

// The multi-market tour does not scan in the handler at all — it navigates and
// the route executor scans on arrival, reading the window off the context. So
// the window only reaches that path if the tour stamps it before dispatching.
func TestScoutTour_StampsDedupWindowOntoContext(t *testing.T) {
	api := &countingScoutAPI{}
	spy := &ctxCapturingMediator{}
	h := NewScoutTourHandler(
		&fakeTourShipRepo{ship: scoutAt(t, scoutedYard)}, spy,
		ship.NewMarketScanner(api, &dedupMarketStore{waypoint: scoutedYard}, nil, nil),
		nil, &shared.MockClock{CurrentTime: time.Now()},
	)
	h.SetScanDedupWindow(75 * time.Second)

	_, _ = h.Handle(tourCtx(), &ScoutTourCommand{
		PlayerID:           shared.MustNewPlayerID(1),
		ShipSymbol:         "PROBE-1",
		Markets:            []string{scoutedYard, "X1-TEST-M2"},
		Iterations:         1,
		StartJitterMaxSecs: 1,
	})

	require.NotNil(t, spy.captured, "the tour must dispatch navigation for a multi-market route")
	policy, ok := shared.ScanPolicyFromContext(spy.captured)
	require.True(t, ok, "the arrival scan reads its window off the context - an unstamped tour leaves it ungated")
	require.Equal(t, 75*time.Second, policy.MaxScanAge)
}

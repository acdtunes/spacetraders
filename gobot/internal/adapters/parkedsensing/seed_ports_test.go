package parkedsensing_test

// Unit tests for the charting-seed adapter. Two things here are behaviour rather
// than plumbing, and both are the difference between a tour that finishes and
// one that loops: the benign already-charted verdict must read as SUCCESS, and a
// refresh must not report success until the waypoint has actually been written
// back to the cache.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

type fakeChartAPI struct {
	chartErr    error
	detail      *ports.WaypointDetail
	waypointErr error

	// pages is the paginated waypoint list, indexed by page number. listErrOn
	// breaks one page so a torn sweep can be exercised.
	pages     map[int][]domainSystem.WaypointAPIData
	listErrOn int

	chartCalls, waypointCalls int
	listedPages               []int
}

func (f *fakeChartAPI) ListWaypoints(_ context.Context, _, _ string, page, limit int) (*domainSystem.WaypointsListResponse, error) {
	f.listedPages = append(f.listedPages, page)
	if f.listErrOn == page {
		return nil, errors.New("waypoint list unavailable")
	}
	data := f.pages[page]
	return &domainSystem.WaypointsListResponse{
		Data: data,
		Meta: domainSystem.PaginationMeta{Page: page, Limit: limit},
	}, nil
}

// apiWaypoint builds one row of the API's waypoint list, whose traits and
// orbitals arrive as loose maps rather than typed values.
func apiWaypoint(symbol, wpType string, x, y float64, traits ...string) domainSystem.WaypointAPIData {
	rows := make([]map[string]interface{}, 0, len(traits))
	for _, trait := range traits {
		rows = append(rows, map[string]interface{}{"symbol": trait})
	}
	return domainSystem.WaypointAPIData{Symbol: symbol, Type: wpType, X: x, Y: y, Traits: rows}
}

func (f *fakeChartAPI) CreateChart(_ context.Context, _, _ string) error {
	f.chartCalls++
	return f.chartErr
}

func (f *fakeChartAPI) GetWaypoint(_ context.Context, _, waypoint, _ string) (*ports.WaypointDetail, error) {
	f.waypointCalls++
	if f.waypointErr != nil {
		// Adversarial: a marketplace alongside the error, so a caller that leaks
		// it spends a scan on a waypoint it never actually read.
		return &ports.WaypointDetail{Symbol: waypoint, Traits: []string{"MARKETPLACE"}}, f.waypointErr
	}
	return f.detail, nil
}

type fakeWaypointCache struct {
	err     error
	written []*ports.WaypointDetail
}

func (f *fakeWaypointCache) UpsertFromDetail(_ context.Context, detail *ports.WaypointDetail) error {
	f.written = append(f.written, detail)
	return f.err
}

type fakeSeedScanner struct {
	scanned []string
	err     error
}

func (f *fakeSeedScanner) ScanAndSaveMarket(_ context.Context, _ uint, waypoint string) error {
	f.scanned = append(f.scanned, waypoint)
	return f.err
}

type stubPlayerRepo struct{ err error }

func (s *stubPlayerRepo) FindByID(_ context.Context, id shared.PlayerID) (*player.Player, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &player.Player{ID: id, Token: "tok"}, nil
}

func newSeedPort(api *fakeChartAPI, cache *fakeWaypointCache, scanner *fakeSeedScanner) *adapterSensing.SeedCommandPort {
	return adapterSensing.NewSeedCommandPort(nil, api, &stubPlayerRepo{}, cache, scanner, nil)
}

func TestSeedCommandPort_Chart_TreatsAnAlreadyChartedWaypointAsSuccess(t *testing.T) {
	// 4230 means the waypoint is public — which is the entire outcome the call
	// was after. Surfacing it would stall a tour on a frontier another agent
	// reached first, and there is nothing the engine could do about it.
	for _, message := range []string{
		"failed to chart waypoint: API error 400 (code 4230)",
		"failed to chart waypoint: waypoint already charted",
	} {
		api := &fakeChartAPI{chartErr: errors.New(message)}
		port := newSeedPort(api, &fakeWaypointCache{}, &fakeSeedScanner{})

		require.NoError(t, port.Chart(context.Background(), 1, "PROBE-7"), message)
		require.Equal(t, 1, api.chartCalls)
	}
}

func TestSeedCommandPort_Chart_SurfacesEveryOtherFailure(t *testing.T) {
	api := &fakeChartAPI{chartErr: errors.New("API error 400 (code 4236): ship is not in orbit")}
	port := newSeedPort(api, &fakeWaypointCache{}, &fakeSeedScanner{})

	require.Error(t, port.Chart(context.Background(), 1, "PROBE-7"))
}

func TestSeedCommandPort_RefreshWaypoint_PersistsBeforeReportingSuccess(t *testing.T) {
	api := &fakeChartAPI{detail: &ports.WaypointDetail{
		Symbol: "X1-BB-A1", Type: "PLANET", X: 3, Y: 9,
		Traits: []string{"MARKETPLACE", "SHIPYARD"},
	}}
	cache := &fakeWaypointCache{}
	port := newSeedPort(api, cache, &fakeSeedScanner{})

	isMarket, err := port.RefreshWaypoint(context.Background(), 1, "X1-BB", "X1-BB-A1")
	require.NoError(t, err)
	require.True(t, isMarket)
	require.Len(t, cache.written, 1)
	require.Equal(t, "X1-BB-A1", cache.written[0].Symbol)
	require.Equal(t, []string{"MARKETPLACE", "SHIPYARD"}, cache.written[0].Traits)
}

func TestSeedCommandPort_RefreshWaypoint_FailsWhenTheCacheWriteFails(t *testing.T) {
	// A refresh whose write did not land leaves the waypoint UNCHARTED in the
	// cache. Reporting success would have the engine record a market for a
	// waypoint the tour is about to chart all over again.
	api := &fakeChartAPI{detail: &ports.WaypointDetail{
		Symbol: "X1-BB-A1", Traits: []string{"MARKETPLACE"},
	}}
	cache := &fakeWaypointCache{err: errors.New("cache write failed")}
	port := newSeedPort(api, cache, &fakeSeedScanner{})

	isMarket, err := port.RefreshWaypoint(context.Background(), 1, "X1-BB", "X1-BB-A1")
	require.Error(t, err)
	require.False(t, isMarket)
}

func TestSeedCommandPort_RefreshWaypoint_ReportsANonMarketWithoutError(t *testing.T) {
	api := &fakeChartAPI{detail: &ports.WaypointDetail{
		Symbol: "X1-BB-B2", Type: "ASTEROID", Traits: []string{"BARREN"},
	}}
	cache := &fakeWaypointCache{}
	port := newSeedPort(api, cache, &fakeSeedScanner{})

	isMarket, err := port.RefreshWaypoint(context.Background(), 1, "X1-BB", "X1-BB-B2")
	require.NoError(t, err)
	require.False(t, isMarket)
	require.Len(t, cache.written, 1, "a waypoint with nothing on it is still charted and still written back")
}

func TestSeedCommandPort_RefreshWaypoint_WritesNothingWhenTheReadFails(t *testing.T) {
	api := &fakeChartAPI{waypointErr: errors.New("API unavailable")}
	cache := &fakeWaypointCache{}
	port := newSeedPort(api, cache, &fakeSeedScanner{})

	isMarket, err := port.RefreshWaypoint(context.Background(), 1, "X1-BB", "X1-BB-A1")
	require.Error(t, err)
	require.False(t, isMarket, "an unread waypoint is not a market")
	require.Empty(t, cache.written)
}

func TestSeedCommandPort_SyncWaypoints_WalksEveryPageAndPersistsEachRow(t *testing.T) {
	// A full page means there may be another; a short one ends the sweep. Getting
	// this wrong in the stopping direction leaves a HALF-swept catalog, which the
	// charting tour reads as a system that is nearly finished.
	full := make([]domainSystem.WaypointAPIData, 20)
	for i := range full {
		full[i] = apiWaypoint(fmt.Sprintf("X1-BB-P%d", i), "PLANET", float64(i), 0)
	}
	api := &fakeChartAPI{pages: map[int][]domainSystem.WaypointAPIData{
		1: full,
		2: {apiWaypoint("X1-BB-Z1", "MOON", 5, 6, "MARKETPLACE", "SHIPYARD")},
	}}
	cache := &fakeWaypointCache{}
	port := newSeedPort(api, cache, &fakeSeedScanner{})

	require.NoError(t, port.SyncWaypoints(context.Background(), 1, "X1-BB"))
	require.Equal(t, []int{1, 2}, api.listedPages)
	require.Len(t, cache.written, 21)

	last := cache.written[20]
	require.Equal(t, "X1-BB-Z1", last.Symbol)
	require.Equal(t, "MOON", last.Type)
	require.Equal(t, float64(5), last.X)
	require.Equal(t, []string{"MARKETPLACE", "SHIPYARD"}, last.Traits,
		"the API's loose trait objects must be flattened to the symbols the cache stores")
}

func TestSeedCommandPort_SyncWaypoints_StopsOnAShortFirstPage(t *testing.T) {
	api := &fakeChartAPI{pages: map[int][]domainSystem.WaypointAPIData{
		1: {apiWaypoint("X1-BB-A1", "PLANET", 1, 1)},
	}}
	port := newSeedPort(api, &fakeWaypointCache{}, &fakeSeedScanner{})

	require.NoError(t, port.SyncWaypoints(context.Background(), 1, "X1-BB"))
	require.Equal(t, []int{1}, api.listedPages, "a short page is the end of the list")
}

func TestSeedCommandPort_SyncWaypoints_FailsOnATornSweep(t *testing.T) {
	// The caller stamps the catalog as synced only when this returns cleanly, so
	// a sweep that lost a page must NOT look like a completed one.
	full := make([]domainSystem.WaypointAPIData, 20)
	for i := range full {
		full[i] = apiWaypoint(fmt.Sprintf("X1-BB-P%d", i), "PLANET", float64(i), 0)
	}
	api := &fakeChartAPI{
		pages:     map[int][]domainSystem.WaypointAPIData{1: full},
		listErrOn: 2,
	}
	port := newSeedPort(api, &fakeWaypointCache{}, &fakeSeedScanner{})

	require.Error(t, port.SyncWaypoints(context.Background(), 1, "X1-BB"))
}

func TestSeedCommandPort_SyncWaypoints_FailsWhenARowCannotBePersisted(t *testing.T) {
	api := &fakeChartAPI{pages: map[int][]domainSystem.WaypointAPIData{
		1: {apiWaypoint("X1-BB-A1", "PLANET", 1, 1)},
	}}
	cache := &fakeWaypointCache{err: errors.New("cache write failed")}
	port := newSeedPort(api, cache, &fakeSeedScanner{})

	require.Error(t, port.SyncWaypoints(context.Background(), 1, "X1-BB"))
}

func TestSeedCommandPort_ReadMarketAt_GoesThroughTheSharedScanner(t *testing.T) {
	scanner := &fakeSeedScanner{}
	port := newSeedPort(&fakeChartAPI{}, &fakeWaypointCache{}, scanner)

	require.NoError(t, port.ReadMarketAt(context.Background(), 1, "X1-BB-A1"))
	require.Equal(t, []string{"X1-BB-A1"}, scanner.scanned)
}

package expansion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// fakeUniverseLister serves the universe roster page-by-page and counts how many API
// pages were pulled — the frugality assertion below reads that count.
type fakeUniverseLister struct {
	pages map[int][]system.SystemAPIData
	total int
	calls int
	err   error
}

func (f *fakeUniverseLister) ListSystems(_ context.Context, _ string, page, limit int) (*system.SystemsListResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &system.SystemsListResponse{
		Data: f.pages[page],
		Meta: system.PaginationMeta{Total: f.total, Page: page, Limit: limit},
	}, nil
}

// fakeUniversePlayerRepo supplies the auth token the crawl needs (mirrors how the graph
// builder reads player.Token before a paginated pull).
type fakeUniversePlayerRepo struct{ token string }

func (f *fakeUniversePlayerRepo) FindByID(_ context.Context, id shared.PlayerID) (*player.Player, error) {
	return &player.Player{ID: id, Token: f.token}, nil
}
func (f *fakeUniversePlayerRepo) FindByAgentSymbol(context.Context, string) (*player.Player, error) {
	return nil, nil
}
func (f *fakeUniversePlayerRepo) ListAll(context.Context) ([]*player.Player, error) { return nil, nil }
func (f *fakeUniversePlayerRepo) Add(context.Context, *player.Player) error         { return nil }

// TestUniverseSystemsCache_CrawlsAllPagesOnceThenServesFromCache pins the frugality
// bound: the universe roster is near-static within an era and large (many pages), so the
// cache pays the full paginated crawl ONCE and serves every subsequent AllSystems from
// memory for the TTL — a per-tick refetch would burn the API budget for nothing. Past the
// TTL it refetches (near-static, not frozen). The lister's call count is the observable
// proof of the frugality.
func TestUniverseSystemsCache_CrawlsAllPagesOnceThenServesFromCache(t *testing.T) {
	lister := &fakeUniverseLister{
		total: 3,
		pages: map[int][]system.SystemAPIData{
			1: {{Symbol: "X1-A", Type: "BLUE_STAR", X: 1, Y: 1}, {Symbol: "X1-B", Type: "RED_STAR", X: 2, Y: 2}},
			2: {{Symbol: "X1-C", Type: "BLACK_HOLE", X: 3, Y: 3}},
		},
	}
	clock := &shared.MockClock{CurrentTime: time.Now()}
	cache := NewUniverseSystemsCache(lister, &fakeUniversePlayerRepo{token: "tok"}, clock, time.Hour)
	cache.pageLimit = 2 // small pages so the 3-system fixture spans two real pages

	got, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, got, 3, "the crawl paged through the whole universe into one roster")
	crawlCalls := lister.calls
	require.GreaterOrEqual(t, crawlCalls, 2, "more than one page was pulled")

	got2, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, got2, 3)
	require.Equal(t, crawlCalls, lister.calls, "within the TTL the roster is served from cache, NOT refetched")

	clock.CurrentTime = clock.CurrentTime.Add(2 * time.Hour)
	_, err = cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	require.Greater(t, lister.calls, crawlCalls, "past the TTL the roster refetches (near-static, not frozen)")
}

// TestUniverseSystemsCache_ServesStaleRosterWhenRefetchFails pins the fail-safe: once a
// roster is cached, a later refetch error serves the last-good roster rather than losing
// the whole off-gate signal on a transient API blip (systems are near-static, so stale is
// safe). With no cache yet, the error surfaces.
func TestUniverseSystemsCache_ServesStaleRosterWhenRefetchFails(t *testing.T) {
	lister := &fakeUniverseLister{
		total: 1,
		pages: map[int][]system.SystemAPIData{1: {{Symbol: "X1-A", Type: "BLUE_STAR"}}},
	}
	clock := &shared.MockClock{CurrentTime: time.Now()}
	cache := NewUniverseSystemsCache(lister, &fakeUniversePlayerRepo{token: "tok"}, clock, time.Hour)

	// Cold cache + error → surface the error (nothing to fall back on).
	lister.err = errors.New("systems endpoint down")
	_, err := cache.AllSystems(context.Background(), 1)
	require.Error(t, err, "a cold-cache fetch failure surfaces the error")

	// Warm the cache, then fail a post-TTL refetch → last-good roster is served.
	lister.err = nil
	_, err = cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	clock.CurrentTime = clock.CurrentTime.Add(2 * time.Hour)
	lister.err = errors.New("systems endpoint down again")
	got, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err, "a warm-cache refetch failure serves stale rather than erroring")
	require.Len(t, got, 1, "the last-good roster is served")
}

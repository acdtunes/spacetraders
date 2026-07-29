package expansion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// fakeUniverseLister serves the universe roster page-by-page and counts how many API
// pages were pulled — the frugality assertion below reads that count.
//
// IT IS MUTEX-GUARDED AND CONTEXT-AWARE, and both are load-bearing rather than decoration. The
// crawl now runs on a DETACHED goroutine, so every counter it touches is written off the test's
// own goroutine and would race without the lock. And it fails a call whose context is already
// done, with the same wording the production log carried ("context cancelled"), because "the
// crawl dies with the caller's tick" is the exact regression these tests exist to catch — a fake
// that ignored ctx could not fail when the fix is removed.
type fakeUniverseLister struct {
	mu        sync.Mutex
	pages     map[int][]system.SystemAPIData
	total     int
	calls     int
	pageCalls map[int]int
	err       error
	gate      chan struct{}
}

func (f *fakeUniverseLister) ListSystems(ctx context.Context, _ string, page, limit int) (*system.SystemsListResponse, error) {
	f.mu.Lock()
	f.calls++
	if f.pageCalls == nil {
		f.pageCalls = make(map[int]int)
	}
	f.pageCalls[page]++
	pages, total, err, gate := f.pages, f.total, f.err, f.gate
	f.mu.Unlock()

	// A HELD page stands in for where the live crawl actually spends its minutes: parked on the
	// shared 2.00 req/s limiter waiting for its turn. Cancelling the caller's context while a page
	// is held is the whole regression fixture, so the wait watches ctx.Done exactly as the real
	// client does rather than sleeping blind.
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, fmt.Errorf("failed to list systems: context cancelled: %w", ctx.Err())
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("failed to list systems: context cancelled: %w", ctxErr)
	}
	if err != nil {
		return nil, err
	}
	return &system.SystemsListResponse{
		Data: pages[page],
		Meta: system.PaginationMeta{Total: total, Page: page, Limit: limit},
	}, nil
}

func (f *fakeUniverseLister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeUniverseLister) pageCount(page int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pageCalls[page]
}

func (f *fakeUniverseLister) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeUniverseLister) setPages(pages map[int][]system.SystemAPIData, total int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages, f.total = pages, total
}

// hold parks every crawl on the page it is currently pulling until the returned release is
// called. It is what makes the asynchronous assertions deterministic: with a crawl pinned
// mid-flight, what the next AllSystems call reports is decided by the state already recorded and
// never by a second crawl racing in behind it.
func (f *fakeUniverseLister) hold() func() {
	gate := make(chan struct{})
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()

	var once sync.Once
	return func() {
		f.mu.Lock()
		f.gate = nil
		f.mu.Unlock()
		once.Do(func() { close(gate) })
	}
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

// awaitWarm blocks until no detached crawl is in flight.
//
// It reads the cache's own latch under the cache's own mutex rather than sleeping on the
// goroutine. That is not just tidier — the recording block holds c.mu across BOTH the latch clear
// and the c.clock.Now() stamp, so taking the same lock is what guarantees a test that then
// advances the mock clock is not racing the goroutine that reads it.
func awaitWarm(t *testing.T, cache *UniverseSystemsCache) {
	t.Helper()
	require.Eventually(t, func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return !cache.warming
	}, 5*time.Second, time.Millisecond, "the detached warm settled")
}

func universeCache(lister *fakeUniverseLister, clock shared.Clock) *UniverseSystemsCache {
	return NewUniverseSystemsCache(lister, &fakeUniversePlayerRepo{token: "tok"}, clock, time.Hour)
}

// TestUniverseSystemsCache_CrawlsAllPagesOnceThenServesFromCache pins the frugality
// bound: the universe roster is near-static within an era and large (many pages), so the
// cache pays the full paginated crawl ONCE and serves every subsequent AllSystems from
// memory for the TTL — a per-tick refetch would burn the API budget for nothing. Past the
// TTL it refetches (near-static, not frozen). The lister's call count is the observable
// proof of the frugality.
//
// It also pins the cold contract that the whole detached shape rests on: the call that starts a
// warm returns AT ONCE, with an empty roster and NO error. A cold cache is not a failure, it is a
// fleet whose universe is still loading, and reporting it as one is what filled the production log
// with a per-tick "off-gate target selection failed".
func TestUniverseSystemsCache_CrawlsAllPagesOnceThenServesFromCache(t *testing.T) {
	lister := &fakeUniverseLister{
		total: 3,
		pages: map[int][]system.SystemAPIData{
			1: {{Symbol: "X1-A", Type: "BLUE_STAR", X: 1, Y: 1}, {Symbol: "X1-B", Type: "RED_STAR", X: 2, Y: 2}},
			2: {{Symbol: "X1-C", Type: "BLACK_HOLE", X: 3, Y: 3}},
		},
	}
	clock := &shared.MockClock{CurrentTime: time.Now()}
	cache := universeCache(lister, clock)
	cache.pageLimit = 2 // small pages so the 3-system fixture spans two real pages

	cold, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err, "a cold cache is not a failure — the roster is merely still loading")
	require.Empty(t, cold, "the cold call returns at once rather than crawling on the caller's tick")

	awaitWarm(t, cache)

	got, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, got, 3, "the crawl paged through the whole universe into one roster")
	crawlCalls := lister.callCount()
	require.GreaterOrEqual(t, crawlCalls, 2, "more than one page was pulled")

	got2, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, got2, 3)
	require.Equal(t, crawlCalls, lister.callCount(), "within the TTL the roster is served from cache, NOT refetched")

	clock.CurrentTime = clock.CurrentTime.Add(2 * time.Hour)
	stale, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, stale, 3, "the last-good roster keeps serving while the refetch runs behind it")
	awaitWarm(t, cache)
	require.Greater(t, lister.callCount(), crawlCalls, "past the TTL the roster refetches (near-static, not frozen)")
}

// TestUniverseSystemsCache_CancelledCallerContextStillWarmsTheRoster is THE regression.
//
// The crawl used to run inline on the caller's context. Its only caller is the off-gate target
// selector, which runs inside a 30-second sensing tick, so the crawl could never finish: measured
// live it reached page 318 and died on "context canceled", leaving the cache cold, so the next
// tick started from page 1 and died in the same place. The roster was therefore structurally
// unwarmable and the fleet had nowhere to send an explorer it had already bought.
//
// So this asserts the fix directly rather than by proxy: the caller's context is cancelled while
// the crawl is still parked on its first page, and the roster warms to completion anyway. Put the
// tick's context back into the goroutine and this test fails on the very next line.
func TestUniverseSystemsCache_CancelledCallerContextStillWarmsTheRoster(t *testing.T) {
	lister := &fakeUniverseLister{
		total: 3,
		pages: map[int][]system.SystemAPIData{
			1: {{Symbol: "X1-A", Type: "BLUE_STAR", X: 1, Y: 1}, {Symbol: "X1-B", Type: "RED_STAR", X: 2, Y: 2}},
			2: {{Symbol: "X1-C", Type: "BLACK_HOLE", X: 3, Y: 3}},
		},
	}
	release := lister.hold() // the crawl parks mid-flight, as the live one does for minutes
	defer release()

	cache := universeCache(lister, &shared.MockClock{CurrentTime: time.Now()})
	cache.pageLimit = 2

	tick, endTick := context.WithCancel(context.Background())
	cold, err := cache.AllSystems(tick, 1)
	require.NoError(t, err)
	require.Empty(t, cold)

	endTick() // the 30-second sensing tick ends with the crawl barely started

	release()
	awaitWarm(t, cache)

	roster, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err, "the tick's death is not the crawl's death")
	require.Len(t, roster, 3, "the roster warmed to completion on the crawl's OWN context, outliving the caller that started it")
}

// TestUniverseSystemsCache_WarmsOnceUnderConcurrentColdCallers pins the single-flight latch.
// Callers arrive on a 30-second tick and a full crawl takes minutes, so without the latch every
// tick would pile ANOTHER full-universe crawl on top of the one already running and the storm
// would eat the request budget the live fleet trades on. Twenty concurrent cold callers must buy
// exactly ONE crawl between them.
func TestUniverseSystemsCache_WarmsOnceUnderConcurrentColdCallers(t *testing.T) {
	lister := &fakeUniverseLister{
		total: 3,
		pages: map[int][]system.SystemAPIData{
			1: {{Symbol: "X1-A", Type: "BLUE_STAR", X: 1, Y: 1}, {Symbol: "X1-B", Type: "RED_STAR", X: 2, Y: 2}},
			2: {{Symbol: "X1-C", Type: "BLACK_HOLE", X: 3, Y: 3}},
		},
	}
	release := lister.hold()
	defer release()

	cache := universeCache(lister, &shared.MockClock{CurrentTime: time.Now()})
	cache.pageLimit = 2

	// Start one crawl and wait until it is provably in flight, so the crowd below arrives into a
	// genuinely warming cache rather than racing the first caller.
	_, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return lister.pageCount(1) == 1 }, 5*time.Second, time.Millisecond,
		"the first caller's crawl reached page 1")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cache.AllSystems(context.Background(), 1)
		}()
	}
	wg.Wait()

	require.Never(t, func() bool { return lister.pageCount(1) > 1 }, 200*time.Millisecond, 5*time.Millisecond,
		"twenty cold callers bought ONE crawl between them — a second page-1 pull is a second full-universe crawl")

	release()
	awaitWarm(t, cache)
	roster, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, roster, 3)
	require.Equal(t, 1, lister.pageCount(1), "and the finished crawl pulled page 1 exactly once")
}

// TestUniverseSystemsCache_ColdCrawlFailureSurfacesOnceThenGoesQuiet pins the reporting contract
// for a cold outage. The crawl is DETACHED, so the goroutine that discovers a failure is not the
// call that can report it: the failure is parked for the next caller. It must surface — a silent
// one presents as a fleet that quietly never explores — but exactly once per failed attempt, not
// once per 30-second tick, or the signal drowns in its own noise.
func TestUniverseSystemsCache_ColdCrawlFailureSurfacesOnceThenGoesQuiet(t *testing.T) {
	lister := &fakeUniverseLister{
		total: 1,
		pages: map[int][]system.SystemAPIData{1: {{Symbol: "X1-A", Type: "BLUE_STAR"}}},
	}
	lister.setErr(errors.New("systems endpoint down"))
	cache := universeCache(lister, &shared.MockClock{CurrentTime: time.Now()})

	cold, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err, "the call that LAUNCHES a warm has no verdict to report yet")
	require.Empty(t, cold)

	awaitWarm(t, cache) // the detached crawl has now failed and parked its error

	// Park every further crawl, so what the next two calls report is decided by that parked error
	// alone and never by a second failure racing in behind them.
	release := lister.hold()
	defer release()

	_, err = cache.AllSystems(context.Background(), 1)
	require.Error(t, err, "the cold failure surfaces to the next caller")
	require.ErrorContains(t, err, "systems endpoint down")

	_, err = cache.AllSystems(context.Background(), 1)
	require.NoError(t, err, "and then goes quiet: one report per failed attempt, not one per tick")
}

// TestUniverseSystemsCache_ServesStaleRosterWhenRefetchFails pins the fail-safe: once a
// roster is cached, a later refetch error serves the last-good roster rather than losing
// the whole off-gate signal on a transient API blip (systems are near-static, so stale is
// safe), and it keeps serving it after the doomed refetch has actually landed.
func TestUniverseSystemsCache_ServesStaleRosterWhenRefetchFails(t *testing.T) {
	lister := &fakeUniverseLister{
		total: 1,
		pages: map[int][]system.SystemAPIData{1: {{Symbol: "X1-A", Type: "BLUE_STAR"}}},
	}
	clock := &shared.MockClock{CurrentTime: time.Now()}
	cache := universeCache(lister, clock)

	_, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	awaitWarm(t, cache)
	got, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, got, 1, "the roster is warm")

	clock.CurrentTime = clock.CurrentTime.Add(2 * time.Hour)
	lister.setErr(errors.New("systems endpoint down again"))

	got, err = cache.AllSystems(context.Background(), 1)
	require.NoError(t, err, "a warm-cache refetch failure serves stale rather than erroring")
	require.Len(t, got, 1, "the last-good roster is served")

	awaitWarm(t, cache) // the doomed refetch has now failed
	got, err = cache.AllSystems(context.Background(), 1)
	require.NoError(t, err, "and a loaded cache still never reports it — losing the roster over a blip loses the whole off-gate signal")
	require.Len(t, got, 1)
}

// TestUniverseSystemsCache_RefusesToLatchAnEmptyRoster pins the empty-200 refusal. GET /systems on
// a live account always returns systems, so a crawl that came back empty is an intermittent blank,
// not news that the universe emptied — and latching it would freeze off-gate selection for a whole
// TTL with nothing in the log to say why. It is treated as a failed warm instead, which is both
// what lets the next tick recover and what lets the rest of the cache state flatly that an empty
// roster means "not warm yet" and nothing else.
func TestUniverseSystemsCache_RefusesToLatchAnEmptyRoster(t *testing.T) {
	lister := &fakeUniverseLister{total: 0, pages: map[int][]system.SystemAPIData{}}
	cache := universeCache(lister, &shared.MockClock{CurrentTime: time.Now()})

	_, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	awaitWarm(t, cache)

	release := lister.hold()
	_, err = cache.AllSystems(context.Background(), 1)
	require.ErrorContains(t, err, "returned no systems at all",
		"an empty crawl is reported as a failed warm, not served as an empty universe")
	release()
	awaitWarm(t, cache)

	// The endpoint recovers. Nothing was latched, so the very next attempt warms properly instead
	// of the cache sitting on a blank roster until the TTL expires.
	lister.setPages(map[int][]system.SystemAPIData{1: {{Symbol: "X1-A", Type: "BLUE_STAR"}}}, 1)
	_, _ = cache.AllSystems(context.Background(), 1) // launches a fresh crawl; drains the parked error
	awaitWarm(t, cache)

	roster, err := cache.AllSystems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, roster, 1, "the refused empty roster left the cache free to warm on the next attempt")
}

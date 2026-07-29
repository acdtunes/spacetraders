package expansion

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

const (
	// defaultUniverseTTL is how long a crawled universe roster stays authoritative before a
	// refetch. Systems are near-static within an era (new ones do not spawn mid-era), so a
	// long TTL keeps the roster fresh enough while the crawl (potentially hundreds of pages)
	// runs at most once per window per process. Deliberately long — the roster is background
	// reference data for target selection, not a live trading signal.
	defaultUniverseTTL = 6 * time.Hour

	// universePageLimit is the page size for the /systems crawl (the API's max, matching the
	// graph builder's waypoint crawl).
	universePageLimit = 20

	// universeMaxPages bounds the crawl so a misbehaving Meta.Total can never loop forever.
	universeMaxPages = 2000
)

// UniverseLister is the narrow slice of the SpaceTraders api client the universe cache
// reads: one paginated page of the GET /systems universe roster. The concrete
// *api.SpaceTradersClient satisfies it; tests fake it to count API pulls.
type UniverseLister interface {
	ListSystems(ctx context.Context, token string, page, limit int) (*system.SystemsListResponse, error)
}

// UniverseSystemsCache crawls the whole universe system list ONCE (all pages) and serves it
// from memory for a long TTL. The roster is near-static within an era and large
// (thousands of systems, many pages), so a per-tick refetch would burn the API budget for
// nothing. The first AllSystems after construction or TTL expiry pays the full paginated
// crawl; every call within the TTL is a pure in-memory read. Once a roster is cached, a
// later refetch failure serves the last-good roster rather than losing the whole off-gate
// signal on a transient blip (systems are near-static, so stale is safe). The frugality
// bound is ONE full crawl per TTL per process.
type UniverseSystemsCache struct {
	lister     UniverseLister
	playerRepo player.PlayerRepository
	clock      shared.Clock
	ttl        time.Duration

	pageLimit int

	mu       sync.Mutex
	cached   []system.SystemAPIData
	cachedAt time.Time
	loaded   bool
}

// NewUniverseSystemsCache wires the cache. A non-positive ttl falls back to the documented
// default; a nil clock uses the real clock.
func NewUniverseSystemsCache(lister UniverseLister, playerRepo player.PlayerRepository, clock shared.Clock, ttl time.Duration) *UniverseSystemsCache {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	if ttl <= 0 {
		ttl = defaultUniverseTTL
	}
	return &UniverseSystemsCache{
		lister:     lister,
		playerRepo: playerRepo,
		clock:      clock,
		ttl:        ttl,
		pageLimit:  universePageLimit,
	}
}

// AllSystems returns the whole universe roster, warming it in the BACKGROUND and serving from
// cache. It never crawls on the caller's context, and that is the whole point of this shape.
//
// THE CRAWL CANNOT FIT IN A TICK. This roster is hundreds of pages of GET /systems drawn against
// a 2.00 req/s account budget shared with the live fleet — minutes of wall clock. Its only caller
// is the off-gate target selector, which runs inside a 30-second sensing tick. Crawling on the
// tick's context therefore could not ever finish: measured live, it reached page 318 and died on
// "context canceled", leaving the cache cold, so the NEXT tick started from page 1 and died too.
// A cold cache has no last-good roster to fall back on, so the selector returned an error every
// time and off-gate target selection was structurally impossible — the fleet could have bought an
// explorer and would still have had nowhere to send it.
//
// So the crawl is detached: the first call starts ONE background warm on a context of its own and
// returns an empty roster immediately. An empty roster is not an error and must not be reported as
// one — it means "no target yet", which is the honest state of a fleet whose universe is still
// loading, and it lets the caller emit its zero-demand signal on schedule instead of logging a
// failure every tick. A later tick finds the roster warm and pays nothing but a map read.
//
// A refetch failure serves the last-good roster (systems are near-static within an era). A cold
// failure is surfaced ONCE to the next caller and then cleared, so a genuine outage is visible in
// the log without becoming per-tick noise.
func (c *UniverseSystemsCache) AllSystems(ctx context.Context, playerID int) ([]system.SystemAPIData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.loaded && c.clock.Now().Sub(c.cachedAt) < c.ttl {
		return c.cached, nil
	}

	c.startWarmLocked(playerID)

	if c.loaded {
		return c.cached, nil // stale but usable while the refresh runs; systems are near-static
	}
	if err := c.lastErr; err != nil {
		c.lastErr = nil // report a cold failure once, then go quiet until the next attempt fails
		return nil, err
	}
	return nil, nil // cold and warming: no roster yet, and that is not a failure
}

// startWarmLocked launches the background crawl unless one is already running. The caller holds
// c.mu.
//
// IT DOES NOT INHERIT THE CALLER'S CONTEXT. That is the fix: a tick-scoped context is cancelled
// long before the crawl completes, and inheriting it is precisely what made the cache
// permanently cold. The warm gets its own budget instead — generous enough for hundreds of
// paginated pages behind the shared rate limiter, bounded so a wedged crawl cannot hold the
// warming flag forever and block every future attempt.
func (c *UniverseSystemsCache) startWarmLocked(playerID int) {
	if c.warming {
		return
	}
	c.warming = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), universeWarmTimeout)
		defer cancel()

		systems, err := c.crawl(ctx, playerID)

		c.mu.Lock()
		defer c.mu.Unlock()
		c.warming = false
		if err != nil {
			c.lastErr = err
			return // a loaded cache keeps serving its last-good roster
		}
		c.cached = systems
		c.cachedAt = c.clock.Now()
		c.loaded = true
		c.lastErr = nil
	}()
}

// crawl pulls every page of the universe system list into one roster.
func (c *UniverseSystemsCache) crawl(ctx context.Context, playerID int) ([]system.SystemAPIData, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player id for universe crawl: %w", err)
	}
	p, err := c.playerRepo.FindByID(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("failed to read player token for universe crawl: %w", err)
	}
	if p == nil {
		return nil, fmt.Errorf("no player %d for universe crawl", playerID)
	}

	all := make([]system.SystemAPIData, 0, 256)
	for page := 1; page <= universeMaxPages; page++ {
		resp, err := c.lister.ListSystems(ctx, p.Token, page, c.pageLimit)
		if err != nil {
			return nil, fmt.Errorf("failed to crawl systems page %d: %w", page, err)
		}
		if len(resp.Data) == 0 {
			break
		}
		all = append(all, resp.Data...)
		if c.crawledLastPage(resp, page) {
			break
		}
	}
	return all, nil
}

// crawledLastPage reports whether the crawl has reached the final page (either a short page
// or the computed total-page bound), mirroring the graph builder's waypoint pagination.
func (c *UniverseSystemsCache) crawledLastPage(resp *system.SystemsListResponse, page int) bool {
	if len(resp.Data) < c.pageLimit {
		return true
	}
	totalPages := resp.Meta.Total / c.pageLimit
	if resp.Meta.Total%c.pageLimit > 0 {
		totalPages++
	}
	return page >= totalPages
}

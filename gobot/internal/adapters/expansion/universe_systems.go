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

// AllSystems returns the whole universe roster, crawling it once and serving from cache
// within the TTL. On a refetch failure it serves the last-good roster if one exists,
// otherwise returns the error (a cold cache has nothing to fall back on).
func (c *UniverseSystemsCache) AllSystems(ctx context.Context, playerID int) ([]system.SystemAPIData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.loaded && c.clock.Now().Sub(c.cachedAt) < c.ttl {
		return c.cached, nil
	}

	systems, err := c.crawl(ctx, playerID)
	if err != nil {
		if c.loaded {
			return c.cached, nil // serve last-good roster; systems are near-static
		}
		return nil, err
	}

	c.cached = systems
	c.cachedAt = c.clock.Now()
	c.loaded = true
	return c.cached, nil
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

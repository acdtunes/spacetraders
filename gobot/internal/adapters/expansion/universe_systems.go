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

	// universeWarmTimeout bounds ONE detached roster warm.
	//
	// IT IS NOT WHAT PACES THE CRAWL — the shared 2.00 req/s account limiter is. Cutting a crawl
	// short does not lower the request rate, it only throws away the pages already paid for, so
	// there is nothing to buy by setting this tight. Its ONLY job is to stop a WEDGED crawl (a
	// request that never returns and never cancels) from holding the warming latch and blocking
	// every future attempt for the life of the process — the exact permanent-coldness failure this
	// whole shape exists to end, re-introduced by a different door.
	//
	// So it is set generously above any legitimate crawl — the universeMaxPages bound drawn at a
	// pessimistic fraction of a budget shared with the live fleet — and still an order of magnitude
	// under defaultUniverseTTL, so a wedged warm self-clears and retries with hours to spare inside
	// the very roster window it was filling.
	universeWarmTimeout = 30 * time.Minute
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

	// warming is the single-flight latch over the detached crawl: set the moment a warm is
	// launched, cleared only when that goroutine has recorded its outcome. It is load-bearing, not
	// tidy. Callers arrive on a 30-second sensing tick and a crawl takes minutes, so without it
	// every tick would launch ANOTHER full-universe crawl on top of the one already running and the
	// pile-up would eat the shared request budget the live fleet trades on. With it, a cold cache
	// costs exactly one crawl at a time no matter how many callers pile up behind it.
	warming bool

	// lastErr is the outcome of the most recent FAILED crawl, held only until one caller reads it.
	// It exists because the crawl is DETACHED: the goroutine that discovers a failure is not the
	// call that can report it, so the failure has to be parked somewhere for the next caller to
	// pick up. Read-and-clear rather than sticky, so a genuine outage is logged once per failed
	// attempt instead of once per tick.
	lastErr error
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
// the log without becoming per-tick noise. A cold cache retries on the next tick — but the warming
// latch means "retry" is at most one crawl in flight, never one crawl per tick, so a persistent
// outage cannot turn this into a request storm against the fleet's own budget.
//
// AN EMPTY ROSTER HAS EXACTLY ONE MEANING HERE: not warm yet. A successful crawl that came back
// empty is refused as a failed warm rather than latched (see startWarmLocked), so a caller reading
// zero systems never has to wonder which of the two it is looking at.
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
// c.mu, and the launch itself does not block: it starts a goroutine and returns, so the tick
// carries on at once.
//
// IT DOES NOT INHERIT THE CALLER'S CONTEXT. That is the fix: a tick-scoped context is cancelled
// long before the crawl completes, and inheriting it is precisely what made the cache permanently
// cold. The warm gets its own budget instead — see universeWarmTimeout.
//
// THE LOCK HAND-OFF IS THE SUBTLE PART, so it is spelled out. The goroutine re-acquires the SAME
// c.mu the launching caller is still holding. That cannot deadlock, because the launch is not a
// join: startWarmLocked returns immediately, AllSystems then returns and releases c.mu on its
// deferred unlock, and the goroutine simply waits its turn — it has a whole paginated crawl to run
// before it wants the lock at all. c.crawl deliberately runs OUTSIDE the lock and touches only
// construction-time state (lister, playerRepo, pageLimit), so minutes of API paging never hold a
// tick's read of the roster hostage.
//
// THE LATCH IS CLEARED FIRST, before anything that can return. Every exit path from the recording
// block therefore clears it: the error return, the success fall-through, and any future branch
// someone adds between them. A latch left set is not a lost roster, it is a PERMANENTLY cold cache
// — the original bug wearing a different mask — so it gets the safest statement position rather
// than the most readable one.
func (c *UniverseSystemsCache) startWarmLocked(playerID int) {
	if c.warming {
		return // one crawl at a time; the callers piling up behind it get the cache as it stands
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
		// AN EMPTY ROSTER IS NEVER A RESULT. GET /systems on a live account always returns
		// systems, so an empty crawl is an intermittent empty-200, not news that the universe
		// emptied — and latching it would freeze off-gate selection for a whole 6-hour TTL with
		// nothing in the log to say why. Treated as a failed warm instead, so the next tick simply
		// tries again. This is the same refusal the gate graph makes for an empty gate read
		// (sp-dmxy5), and it is what lets the rest of this file state flatly that an empty roster
		// means "not warm yet" and nothing else.
		if len(systems) == 0 {
			c.lastErr = fmt.Errorf("universe crawl returned no systems at all; treating as a failed warm rather than latching an empty roster for the TTL")
			return
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

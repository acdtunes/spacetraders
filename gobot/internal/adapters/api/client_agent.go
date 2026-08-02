package api

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// SetAgentCacheTTL overrides how long GetAgent may serve a cached agent before
// re-reading it live. ttl<=0 selects defaultAgentCacheTTL. Wired at
// daemon boot from DaemonConfig.AgentCacheTTLSeconds via setter injection, so the
// many NewSpaceTradersClient call sites stay untouched. Thread-safe: the cache
// mutex guards the knob because SetAgentCacheTTL can race concurrent GetAgent
// reads. Shortening the TTL is always safe; the credit-DECREASING-call
// invalidation is what protects money guards, not the TTL.
func (c *SpaceTradersClient) SetAgentCacheTTL(ttl time.Duration) {
	c.agentCacheMu.Lock()
	c.agentCacheTTL = ttl
	c.agentCacheMu.Unlock()
}

// agentFlight is one in-flight live /my/agent read, shared by every GetAgent
// caller that arrives while it is running. done is closed exactly once, by the
// leader, after agent/err are set — so a waiter that observes the close is
// guaranteed to see both (the channel close is the happens-before edge).
type agentFlight struct {
	done  chan struct{}
	agent *player.AgentData
	err   error
}

// await blocks until the shared flight lands and returns its result, or gives up
// early if the WAITER's own context dies (a caller never inherits the leader's
// deadline). The flight's error is returned verbatim to every waiter: a failed
// read must surface as an error at each call site so the money guards fail
// closed, never as a retained earlier value.
func (f *agentFlight) await(ctx context.Context) (*player.AgentData, error) {
	select {
	case <-f.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	observed := *f.agent // copy: never hand out the shared pointer
	return &observed, nil
}

// GetAgent retrieves agent information, served from a short-TTL cache to cut
// redundant live reads, with concurrent misses coalesced into a single request.
//
// Concurrency: a caller that finds no usable cache entry either STARTS the live
// read or JOINS the one already running for the same token. N concurrent cold
// readers therefore cost exactly ONE API call, and each receives a value observed
// at that single instant — the duplication is removed, the liveness is not. This
// holds on the failure path too, which is where it matters most: without it, N
// money guards meeting a rate-limited API each climb their own retry ladder and
// pile more load onto the exact condition that broke the read. Every waiter gets
// the shared error; none is ever handed a retained value in its place.
//
// Safety (money-critical): the fetch runs WITHOUT the cache lock, so a spend can
// invalidate mid-flight. agentCacheEpoch closes that hole — the epoch is sampled
// before the request and re-checked before the store, and a store whose epoch
// moved is DROPPED. So an invalidation that races a fetch always wins, and after
// any spend the cache is EMPTY: the next money-guard read cannot be answered with
// a pre-spend (stale-HIGH) balance, it re-reads live. Holding the lock across the
// fetch would enforce the same invariant, at the cost of head-of-line blocking
// every reader behind one network round trip.
//
// A defensive copy is returned so a caller mutating the AgentData can never
// poison the shared cache. The token is part of the cache key so a client reused
// across agents/tokens (era rotation) never serves one agent's balance to another.
func (c *SpaceTradersClient) GetAgent(ctx context.Context, token string) (*player.AgentData, error) {
	c.agentCacheMu.Lock()

	if c.agentCache != nil &&
		c.agentCacheToken == token &&
		c.clock.Now().Sub(c.agentCachedAt) < c.resolvedAgentCacheTTLLocked() {
		cached := *c.agentCache // copy: never hand out the cached pointer
		c.agentCacheMu.Unlock()
		return &cached, nil
	}

	// A read for this token is already in flight — share it rather than issue a
	// duplicate. Matched on token so an era rotation never joins a flight
	// authenticating a different agent.
	if inFlight := c.agentFlight; inFlight != nil && c.agentFlightToken == token {
		joined := c.onAgentFlightJoin
		c.agentCacheMu.Unlock()
		if joined != nil {
			joined()
		}
		return inFlight.await(ctx)
	}

	flight := &agentFlight{done: make(chan struct{})}
	c.agentFlight = flight
	c.agentFlightToken = token
	epoch := c.agentCacheEpoch
	c.agentCacheMu.Unlock()

	agent, err := c.fetchAgentLive(ctx, token)

	c.agentCacheMu.Lock()
	// Store only if no spend invalidated while this read was in flight. If one
	// did, the value in hand may pre-date it (stale-HIGH), so the cache stays
	// empty and the next money guard re-reads live.
	if err == nil && c.agentCacheEpoch == epoch {
		c.agentCache = agent
		c.agentCachedAt = c.clock.Now()
		c.agentCacheToken = token
	}
	c.agentFlight = nil
	c.agentFlightToken = ""
	c.agentCacheMu.Unlock()

	// Publish to the waiters last: the close is what makes agent/err visible.
	flight.agent = agent
	flight.err = err
	close(flight.done)

	if err != nil {
		return nil, err
	}
	fresh := *agent
	return &fresh, nil
}

// resolvedAgentCacheTTLLocked reports the effective agent-cache TTL. The caller
// MUST hold agentCacheMu. A zero/negative configured value selects the built-in
// default, mirroring ShipRepository.resolvedCASRetries.
func (c *SpaceTradersClient) resolvedAgentCacheTTLLocked() time.Duration {
	if c.agentCacheTTL <= 0 {
		return defaultAgentCacheTTL
	}
	return c.agentCacheTTL
}

// invalidateAgentCache clears the cached agent. It is called AFTER every
// successful credit-DECREASING API call (purchase/refuel/ship-buy/jump/module
// install) so a subsequent money-guard read re-fetches the true post-spend
// balance instead of a stale-HIGH cached one — the over-spend safety invariant.
// Also invalidated on obvious income (sell) as a cheap bonus so an
// affordable buy becomes visible sooner. Cheap and idempotent: a redundant
// invalidation only costs one extra live read.
//
// Bumping agentCacheEpoch is what makes the invariant hold against a read that is
// ALREADY in flight: that read sampled the old epoch and will refuse to store its
// (now possibly pre-spend) value. Clearing the fields alone would not — the
// in-flight store would land after this call and resurrect a stale-HIGH balance.
func (c *SpaceTradersClient) invalidateAgentCache() {
	c.agentCacheMu.Lock()
	c.agentCache = nil
	c.agentCacheToken = ""
	c.agentCacheEpoch++
	c.agentCacheMu.Unlock()
}

// fetchAgentLive performs the raw GET /my/agent read (no caching). Callers go
// through GetAgent; this is split out so GetAgent can hold the cache mutex across
// the fetch without embedding the HTTP shape in the locking logic.
func (c *SpaceTradersClient) fetchAgentLive(ctx context.Context, token string) (*player.AgentData, error) {
	path := "/my/agent"

	var response struct {
		Data struct {
			AccountID       string `json:"accountId"`
			Symbol          string `json:"symbol"`
			Headquarters    string `json:"headquarters"`
			Credits         int    `json:"credits"`
			StartingFaction string `json:"startingFaction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get agent: %w", err)
	}

	return &player.AgentData{
		AccountID:       response.Data.AccountID,
		Symbol:          response.Data.Symbol,
		Headquarters:    response.Data.Headquarters,
		Credits:         response.Data.Credits,
		StartingFaction: response.Data.StartingFaction,
	}, nil
}

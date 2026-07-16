package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests pin the shared-agent cache (sp-oszc): GetAgent serves a short-TTL
// cached agent to cut the #2 API consumer, WITH a hard safety invariant — every
// credit-DECREASING client call invalidates the cache so a money guard can never
// read a pre-spend (stale-HIGH) balance after we have spent (the over-spend
// hazard). The tests exercise the REAL client against a stateful httptest server
// (real HTTP, real JSON, real caching + invalidation) — not mocks.

// agentCacheFakeServer is a minimal stateful SpaceTraders stand-in: it holds a
// mutable credit balance, decrements it on every spend endpoint (purchase/
// refuel/ship-buy/jump/module-install), increments it on sell, and — critically
// for the call-reduction assertions — COUNTS how many live GET /my/agent reads
// the client actually issues. A cache hit issues no GET, so agentGets is the
// direct observable for "how many live Get Agent calls did we make".
type agentCacheFakeServer struct {
	mu          sync.Mutex
	credits     int
	agentGets   int
	purchase500 bool // when true, POST .../purchase returns 500 (a FAILED spend)
}

const (
	agentCacheSpendPrice  = 100 // every spend endpoint decrements credits by this
	agentCacheSellRevenue = 50  // sell increments credits by this
)

func (s *agentCacheFakeServer) getAgentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentGets
}

func (s *agentCacheFakeServer) currentCredits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credits
}

func (s *agentCacheFakeServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		path := r.URL.Path
		switch {
		case path == "/my/agent":
			s.agentGets++
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"data":{"accountId":"A","symbol":"AGENT","headquarters":"X1-HQ-A1","credits":%d,"startingFaction":"COSMIC"}}`, s.credits)
		case strings.HasSuffix(path, "/purchase"):
			if s.purchase500 {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"message":"boom","code":500}}`)
				return
			}
			s.credits -= agentCacheSpendPrice
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"data":{"agent":{"credits":%d},"transaction":{"totalPrice":%d,"units":10}}}`, s.credits, agentCacheSpendPrice)
		case strings.HasSuffix(path, "/refuel"):
			s.credits -= agentCacheSpendPrice
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"data":{"agent":{"credits":%d},"fuel":{"current":100,"capacity":100},"transaction":{"units":10,"totalPrice":%d}}}`, s.credits, agentCacheSpendPrice)
		case strings.HasSuffix(path, "/sell"):
			s.credits += agentCacheSellRevenue
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"data":{"agent":{"credits":%d},"transaction":{"totalPrice":%d,"units":10}}}`, s.credits, agentCacheSellRevenue)
		case strings.HasSuffix(path, "/jump"):
			s.credits -= agentCacheSpendPrice
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"data":{"nav":{"systemSymbol":"X1-S","waypointSymbol":"X1-S-A"},"cooldown":{"remainingSeconds":0},"transaction":{"totalPrice":%d}}}`, agentCacheSpendPrice)
		case strings.Contains(path, "/modules/"):
			s.credits -= agentCacheSpendPrice
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"data":{"agent":{"credits":%d},"modules":[],"cargo":{"capacity":40,"units":0},"transaction":{"totalPrice":%d}}}`, s.credits, agentCacheSpendPrice)
		case path == "/my/ships": // PurchaseShip
			s.credits -= agentCacheSpendPrice
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"data":{"agent":{"accountId":"A","symbol":"AGENT","headquarters":"X1-HQ-A1","credits":%d,"startingFaction":"COSMIC"},"ship":{"symbol":"SHIP-2","nav":{"systemSymbol":"X1-S","waypointSymbol":"X1-S-A","status":"DOCKED","flightMode":"CRUISE"},"fuel":{"current":100,"capacity":100},"cargo":{"capacity":40,"units":0,"inventory":[]},"engine":{"symbol":"ENGINE_ION_DRIVE_I","speed":10}},"transaction":{"waypointSymbol":"X1-S-A","shipSymbol":"SHIP-2","shipType":"SHIP_PROBE","price":%d,"agentSymbol":"AGENT","timestamp":"2030-01-01T00:00:00Z"}}}`, s.credits, agentCacheSpendPrice)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":{"message":"no route %s"}}`, path)
		}
	}
}

// startAgentCacheServer starts the fake server with an initial balance and wires
// a client to it. clock may be nil (RealClock). Returns the client and server.
func startAgentCacheServer(t *testing.T, initialCredits int, clock shared.Clock) (*SpaceTradersClient, *agentCacheFakeServer) {
	t.Helper()
	fake := &agentCacheFakeServer{credits: initialCredits}
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, clock)
	return client, fake
}

// --- Behavior 1: call reduction (the point of the cache) ---

func TestGetAgentServesFromCacheWithinTTL(t *testing.T) {
	client, fake := startAgentCacheServer(t, 1000, nil)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		agent, err := client.GetAgent(ctx, "token")
		require.NoError(t, err)
		require.Equal(t, 1000, agent.Credits)
	}

	require.Equal(t, 1, fake.getAgentCount(),
		"5 rapid reads within the TTL must collapse to exactly ONE live Get Agent")
}

// --- Behavior 2: TTL expiry re-fetches ---

func TestGetAgentReFetchesAfterTTLExpiry(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now().UTC()}
	client, fake := startAgentCacheServer(t, 1000, clock)
	ctx := context.Background()

	_, err := client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, 1, fake.getAgentCount())

	// Still within the default TTL -> served from cache, no new live read.
	clock.Advance(defaultAgentCacheTTL - time.Second)
	_, err = client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, 1, fake.getAgentCount(), "read inside TTL must not re-fetch")

	// Cross the TTL boundary -> the next read must re-fetch live.
	clock.Advance(2 * time.Second)
	_, err = client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, 2, fake.getAgentCount(), "read past TTL must re-fetch live")
}

// --- Behavior 3 (THE safety behavior): every credit-DECREASING call invalidates
// the cache, so the NEXT read cannot return a pre-spend stale-HIGH balance. ---

func TestCreditDecreasingCallInvalidatesAgentCache(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		spend func(t *testing.T, c *SpaceTradersClient)
	}{
		{"PurchaseCargo", func(t *testing.T, c *SpaceTradersClient) {
			_, err := c.PurchaseCargo(ctx, "SHIP-1", "IRON_ORE", 10, "token")
			require.NoError(t, err)
		}},
		{"RefuelShip", func(t *testing.T, c *SpaceTradersClient) {
			_, err := c.RefuelShip(ctx, "SHIP-1", "token", nil)
			require.NoError(t, err)
		}},
		{"PurchaseShip", func(t *testing.T, c *SpaceTradersClient) {
			_, err := c.PurchaseShip(ctx, "SHIP_PROBE", "X1-S-A", "token")
			require.NoError(t, err)
		}},
		{"JumpShip", func(t *testing.T, c *SpaceTradersClient) {
			_, err := c.JumpShip(ctx, "SHIP-1", "X1-S-A", "token")
			require.NoError(t, err)
		}},
		{"InstallShipModule", func(t *testing.T, c *SpaceTradersClient) {
			_, err := c.InstallShipModule(ctx, "SHIP-1", "MODULE_CARGO_HOLD_I", "token")
			require.NoError(t, err)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, fake := startAgentCacheServer(t, 1000, nil)

			// Warm the cache: a money guard reads 1000 and caches it.
			agent, err := client.GetAgent(ctx, "token")
			require.NoError(t, err)
			require.Equal(t, 1000, agent.Credits)
			require.Equal(t, 1, fake.getAgentCount())

			// Spend credits.
			tc.spend(t, client)
			require.Equal(t, 1000-agentCacheSpendPrice, fake.currentCredits(),
				"the server balance must have dropped by the spend")

			// The NEXT read MUST reflect the reduced balance — the spend
			// invalidated the cache, forcing a fresh live read.
			after, err := client.GetAgent(ctx, "token")
			require.NoError(t, err)
			require.Equal(t, 1000-agentCacheSpendPrice, after.Credits,
				"%s must invalidate the cache: the post-spend read must NOT return the stale pre-spend 1000", tc.name)
			require.Equal(t, 2, fake.getAgentCount(),
				"%s must force a fresh live Get Agent after the spend", tc.name)
		})
	}
}

// --- Behavior 4a: concurrent cold readers collapse to exactly one live fetch. ---

func TestGetAgentConcurrentColdReadsSingleFetch(t *testing.T) {
	client, fake := startAgentCacheServer(t, 1000, nil)
	ctx := context.Background()

	const readers = 50
	var wg sync.WaitGroup
	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			agent, err := client.GetAgent(ctx, "token")
			require.NoError(t, err)
			require.Equal(t, 1000, agent.Credits)
		}()
	}
	wg.Wait()

	require.Equal(t, 1, fake.getAgentCount(),
		"%d concurrent cold reads must produce exactly ONE live Get Agent", readers)
}

// --- Behavior 4b: many concurrent readers interleaved with spend-invalidations
// stay race-clean, and the final read reflects the true post-spend balance. ---

func TestGetAgentConcurrentReadsWithInvalidationRaceClean(t *testing.T) {
	client, fake := startAgentCacheServer(t, 100000, nil)
	ctx := context.Background()

	const readers = 30
	const spends = 10
	var wg sync.WaitGroup

	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, err := client.GetAgent(ctx, "token")
				require.NoError(t, err)
			}
		}()
	}
	wg.Add(spends)
	for i := 0; i < spends; i++ {
		go func() {
			defer wg.Done()
			_, err := client.PurchaseCargo(ctx, "SHIP-1", "IRON_ORE", 10, "token")
			require.NoError(t, err)
		}()
	}
	wg.Wait()

	// After all spends settle, a fresh read must equal the true server balance.
	final, err := client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, fake.currentCredits(), final.Credits,
		"the final post-spend read must reflect the true reduced balance, not a stale cache")
}

// --- Behavior 7: a FAILED spend must NOT invalidate the cache (nothing was
// spent, so the cached balance is still accurate — no needless re-fetch). ---

func TestFailedPurchaseDoesNotInvalidateAgentCache(t *testing.T) {
	client, fake := startAgentCacheServer(t, 1000, nil)
	fake.purchase500 = true
	ctx := context.Background()

	agent, err := client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, 1000, agent.Credits)
	require.Equal(t, 1, fake.getAgentCount())

	_, err = client.PurchaseCargo(ctx, "SHIP-1", "IRON_ORE", 10, "token")
	require.Error(t, err, "the purchase must fail (server 500)")
	require.Equal(t, 1000, fake.currentCredits(), "a failed purchase spends nothing")

	after, err := client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, 1000, after.Credits)
	require.Equal(t, 1, fake.getAgentCount(),
		"a FAILED spend must leave the cache intact (still 1 live read total)")
}

// --- Behavior 6 (cross-agent safety): a read with a DIFFERENT token must never
// be answered from another token's cached balance. ---

func TestGetAgentDifferentTokenBypassesCache(t *testing.T) {
	client, fake := startAgentCacheServer(t, 1000, nil)
	ctx := context.Background()

	_, err := client.GetAgent(ctx, "token-A")
	require.NoError(t, err)
	require.Equal(t, 1, fake.getAgentCount())

	_, err = client.GetAgent(ctx, "token-B")
	require.NoError(t, err)
	require.Equal(t, 2, fake.getAgentCount(),
		"a different token must bypass the cache and read live (never serve another agent's balance)")
}

// --- The TTL knob is live/config-tunable via SetAgentCacheTTL. ---

func TestSetAgentCacheTTLShortensCacheLifetime(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now().UTC()}
	client, fake := startAgentCacheServer(t, 1000, clock)
	client.SetAgentCacheTTL(5 * time.Second)
	ctx := context.Background()

	_, err := client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, 1, fake.getAgentCount())

	clock.Advance(4 * time.Second) // within the 5s knob -> cached
	_, err = client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, 1, fake.getAgentCount())

	clock.Advance(2 * time.Second) // now past 5s -> re-fetch
	_, err = client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, 2, fake.getAgentCount())
}

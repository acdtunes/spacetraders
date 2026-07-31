package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// These tests pin GetAgent's COALESCING behaviour: concurrent readers that miss
// the cache share ONE live /my/agent read instead of each issuing their own.
//
// Coalescing is not caching, and the distinction is the whole point. Every caller
// still receives an observation from the shared read's instant — the liveness
// LiveTreasury and the other money guards depend on is untouched (RULINGS #4).
// What is removed is duplication. Two properties carry the safety weight:
//
//   - a FAILED shared read must reach every waiter as an error, never as a
//     retained earlier value, so each caller's guard still fails closed; and
//   - a spend that invalidates while a read is in flight must still leave the
//     cache EMPTY, so no guard is answered with a pre-spend (stale-HIGH) balance.
//
// The old implementation held the cache mutex across the fetch, which coalesced
// successes as a side effect but serialized every cache HIT behind an in-flight
// miss and gave each waiter its OWN retry ladder on the failure path.

// coalesceFakeServer is a /my/agent stand-in that can be HELD OPEN: the first
// request parks in the handler until the test releases it, so waiters provably
// pile up behind one in-flight read rather than a sleep pretending they did.
//
// It records the balance each request observed AT ENTRY, before any concurrent
// spend, so a test can tell a value that pre-dates a spend from one that follows
// it — the stale-HIGH read the money guard must never be handed.
type coalesceFakeServer struct {
	mu        sync.Mutex
	credits   int
	agentGets int
	failAgent bool

	hold     bool
	entered  chan struct{} // one token per /my/agent request entering the handler
	release  chan struct{} // closed to let held requests finish
	observed []int         // per-request balance sampled at handler entry
}

func newCoalesceFakeServer(t *testing.T, credits int) (*SpaceTradersClient, *coalesceFakeServer) {
	t.Helper()
	fake := &coalesceFakeServer{
		credits: credits,
		entered: make(chan struct{}, 64),
		release: make(chan struct{}),
	}
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	// maxRetries=0: a 5xx surfaces as one error instead of climbing the retry
	// ladder, so a failure-path assertion counts flights and not attempts.
	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	return client, fake
}

func (s *coalesceFakeServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/my/agent" {
			s.mu.Lock()
			s.agentGets++
			atEntry := s.credits
			s.observed = append(s.observed, atEntry)
			hold, fail := s.hold, s.failAgent
			s.mu.Unlock()

			if hold {
				s.entered <- struct{}{}
				<-s.release
			}
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"message":"agent read failed","code":500}}`)
				return
			}
			// Renders the balance seen at ENTRY: a read that overlapped a spend
			// legitimately carries the pre-spend figure.
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"data":{"accountId":"A","symbol":"AGENT","headquarters":"X1-HQ-A1","credits":%d,"startingFaction":"COSMIC"}}`, atEntry)
			return
		}

		if r.Method == http.MethodPost && len(r.URL.Path) > len("/purchase") &&
			r.URL.Path[len(r.URL.Path)-len("/purchase"):] == "/purchase" {
			s.mu.Lock()
			s.credits -= coalesceSpendPrice
			now := s.credits
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"data":{"agent":{"credits":%d},"transaction":{"totalPrice":%d,"units":10}}}`, now, coalesceSpendPrice)
			return
		}

		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"message":"no route %s"}}`, r.URL.Path)
	}
}

const coalesceSpendPrice = 100

func (s *coalesceFakeServer) gets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentGets
}

func (s *coalesceFakeServer) currentCredits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credits
}

// holdNextReads parks every /my/agent request in the handler until releaseReads.
func (s *coalesceFakeServer) holdNextReads() {
	s.mu.Lock()
	s.hold = true
	s.mu.Unlock()
}

func (s *coalesceFakeServer) releaseReads() {
	s.mu.Lock()
	s.hold = false
	s.mu.Unlock()
	close(s.release)
}

// waitForEntry blocks until a /my/agent request has entered the handler, proving
// a read is genuinely in flight before the test starts piling waiters onto it.
func (s *coalesceFakeServer) waitForEntry(t *testing.T) {
	t.Helper()
	select {
	case <-s.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no /my/agent request reached the server")
	}
}

// launchWaiters starts n GetAgent calls that will JOIN the in-flight read, and
// returns once all n have provably joined — the onAgentFlightJoin seam makes
// that deterministic, so a slow scheduler can never turn a real pass into a
// spurious second API call. Results arrive on the returned channel.
type agentResult struct {
	agent *player.AgentData
	err   error
}

func launchWaiters(t *testing.T, client *SpaceTradersClient, n int, token string) <-chan agentResult {
	t.Helper()

	var joined sync.WaitGroup
	joined.Add(n)
	client.agentCacheMu.Lock()
	client.onAgentFlightJoin = joined.Done
	client.agentCacheMu.Unlock()

	results := make(chan agentResult, n)
	for i := 0; i < n; i++ {
		go func() {
			agent, err := client.GetAgent(context.Background(), token)
			results <- agentResult{agent: agent, err: err}
		}()
	}

	done := make(chan struct{})
	go func() { joined.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("waiters never joined the in-flight read")
	}
	return results
}

// --- Acceptance 1 + 2: N concurrent readers -> ONE API call, each receiving a
// value observed within that read's window. ---

func TestGetAgentConcurrentMissesShareOneLiveRead(t *testing.T) {
	client, fake := newCoalesceFakeServer(t, 1000)
	fake.holdNextReads()

	const waiters = 25
	leader := make(chan agentResult, 1)
	go func() {
		agent, err := client.GetAgent(context.Background(), "token")
		leader <- agentResult{agent: agent, err: err}
	}()
	fake.waitForEntry(t)

	results := launchWaiters(t, client, waiters, "token")
	fake.releaseReads()

	lead := <-leader
	require.NoError(t, lead.err)
	require.Equal(t, 1000, lead.agent.Credits)

	for i := 0; i < waiters; i++ {
		got := <-results
		require.NoError(t, got.err, "a waiter must not fail when the shared read succeeded")
		require.Equal(t, 1000, got.agent.Credits,
			"every waiter must receive the value the shared read observed")
	}

	require.Equal(t, 1, fake.gets(),
		"%d concurrent readers must produce exactly ONE live Get Agent, not %d", waiters+1, fake.gets())
}

// Acceptance 2, sharpened: the value a waiter receives must come from the shared
// read's own instant — not from a cache entry predating the window. A read taken
// while the balance was 1000 must hand back 1000 even though the server has since
// moved on; the observation is of THAT moment, which is what a money guard sizes
// against.
func TestGetAgentWaitersReceiveTheSharedReadsObservation(t *testing.T) {
	client, fake := newCoalesceFakeServer(t, 1000)
	fake.holdNextReads()

	leaderDone := make(chan agentResult, 1)
	go func() {
		agent, err := client.GetAgent(context.Background(), "token")
		leaderDone <- agentResult{agent: agent, err: err}
	}()
	fake.waitForEntry(t)

	results := launchWaiters(t, client, 5, "token")
	fake.releaseReads()

	<-leaderDone
	for i := 0; i < 5; i++ {
		got := <-results
		require.NoError(t, got.err)
		require.Equal(t, 1000, got.agent.Credits,
			"the waiter must see the balance the shared read observed, not a later or earlier one")
	}

	fake.mu.Lock()
	observed := append([]int(nil), fake.observed...)
	fake.mu.Unlock()
	require.Equal(t, []int{1000}, observed,
		"exactly one observation should have been taken, at the shared read's instant")
}

// --- Acceptance 3 (THE money-guard behaviour): a failing shared read reaches
// EVERY waiter as an error. Never a retained value. ---
//
// This is the property that silently weakens the guard if it regresses: a waiter
// handed a stale balance instead of the error would size a buy against credits it
// could not actually read, which is precisely what RULINGS #4 forbids and what
// readEnvelope's fail-closed path exists to prevent.
func TestGetAgentFailedSharedReadPropagatesErrorToEveryWaiter(t *testing.T) {
	client, fake := newCoalesceFakeServer(t, 1000)
	ctx := context.Background()

	// Establish a good value first, so a regression that retains the last known
	// balance has something plausible to hand back instead of the error.
	warm, err := client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, 1000, warm.Credits)

	// A spend invalidates the cache (the real trigger for a cold read) and moves
	// the balance, so a retained 1000 is now demonstrably wrong AND stale-HIGH.
	_, err = client.PurchaseCargo(ctx, "SHIP-1", "IRON_ORE", 10, "token")
	require.NoError(t, err)
	require.Equal(t, 1000-coalesceSpendPrice, fake.currentCredits())

	// Now every live read fails.
	fake.mu.Lock()
	fake.failAgent = true
	fake.mu.Unlock()
	fake.holdNextReads()

	const waiters = 20
	leaderDone := make(chan agentResult, 1)
	go func() {
		agent, gerr := client.GetAgent(ctx, "token")
		leaderDone <- agentResult{agent: agent, err: gerr}
	}()
	fake.waitForEntry(t)

	results := launchWaiters(t, client, waiters, "token")
	fake.releaseReads()

	lead := <-leaderDone
	require.Error(t, lead.err, "the read that owned the failing call must surface the error")
	require.Nil(t, lead.agent)

	for i := 0; i < waiters; i++ {
		got := <-results
		require.Error(t, got.err,
			"a failed shared read MUST reach every waiter as an error so its money guard fails closed")
		require.Nil(t, got.agent,
			"a waiter must never be handed a value when the shared read failed — no retained balance")
	}

	// And the failure must not have poisoned the cache with anything either.
	client.agentCacheMu.Lock()
	cached := client.agentCache
	client.agentCacheMu.Unlock()
	require.Nil(t, cached, "a failed read must leave the cache empty, never a retained value")

	require.Equal(t, 2, fake.gets(),
		"the failing read must be shared: one warm-up read plus ONE failing read, not one per waiter")
}

// A failure must not be sticky either: once the API recovers, the next read
// returns the true post-spend balance rather than continuing to serve the error
// or resurrecting the pre-spend figure.
func TestGetAgentRecoversAfterSharedFailureWithPostSpendBalance(t *testing.T) {
	client, fake := newCoalesceFakeServer(t, 1000)
	ctx := context.Background()

	_, err := client.PurchaseCargo(ctx, "SHIP-1", "IRON_ORE", 10, "token")
	require.NoError(t, err)

	fake.mu.Lock()
	fake.failAgent = true
	fake.mu.Unlock()

	_, err = client.GetAgent(ctx, "token")
	require.Error(t, err)

	fake.mu.Lock()
	fake.failAgent = false
	fake.mu.Unlock()

	recovered, err := client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, 1000-coalesceSpendPrice, recovered.Credits,
		"after recovery the read must show the true post-spend balance")
}

// --- The over-spend invariant, now that the fetch runs WITHOUT the cache lock:
// a spend that commits while a read is in flight must leave the cache EMPTY. ---
//
// The in-flight read observed the PRE-spend balance. If it were allowed to store
// that value, the cache would serve a stale-HIGH balance for the whole TTL and a
// money guard could size a buy against credits already spent. The epoch check is
// what drops that store; this test is its falsifier.
func TestSpendDuringInFlightReadLeavesCacheEmpty(t *testing.T) {
	client, fake := newCoalesceFakeServer(t, 1000)
	ctx := context.Background()
	fake.holdNextReads()

	leaderDone := make(chan agentResult, 1)
	go func() {
		agent, err := client.GetAgent(ctx, "token")
		leaderDone <- agentResult{agent: agent, err: err}
	}()
	fake.waitForEntry(t) // the read is parked in the handler, holding a 1000 balance

	// Spend WHILE that read is in flight: the balance drops and the cache is
	// invalidated, both strictly after the in-flight read sampled 1000.
	_, err := client.PurchaseCargo(ctx, "SHIP-1", "IRON_ORE", 10, "token")
	require.NoError(t, err)
	require.Equal(t, 1000-coalesceSpendPrice, fake.currentCredits())

	fake.releaseReads()
	lead := <-leaderDone
	require.NoError(t, lead.err)
	require.Equal(t, 1000, lead.agent.Credits,
		"the in-flight read legitimately observed the pre-spend balance")

	// THE INVARIANT: that pre-spend value must NOT have been cached.
	client.agentCacheMu.Lock()
	cached := client.agentCache
	client.agentCacheMu.Unlock()
	require.Nil(t, cached,
		"a spend that raced an in-flight read must leave the cache EMPTY — a stored pre-spend balance is the over-spend hazard")

	before := fake.gets()
	after, err := client.GetAgent(ctx, "token")
	require.NoError(t, err)
	require.Equal(t, 1000-coalesceSpendPrice, after.Credits,
		"the next money-guard read must see the post-spend balance, never the raced pre-spend one")
	require.Equal(t, before+1, fake.gets(),
		"the next read must go live because the raced store was dropped")
}

// --- Head-of-line: a cache HIT must not queue behind an in-flight miss. ---
//
// The old lock-across-the-fetch made every reader wait on a slow read, and a
// /my/agent call that hits the 429 ladder can stall for tens of seconds — with 46
// trade workers on one client that stalled every money guard at once.
func TestGetAgentCacheHitDoesNotBlockBehindInFlightRead(t *testing.T) {
	client, fake := newCoalesceFakeServer(t, 1000)
	ctx := context.Background()

	warm, err := client.GetAgent(ctx, "token") // fills the cache for "token"
	require.NoError(t, err)
	require.Equal(t, 1000, warm.Credits)

	// Park a read for a DIFFERENT token (never served from another agent's cache,
	// so it must go live) and leave it in flight.
	fake.holdNextReads()
	go func() { _, _ = client.GetAgent(ctx, "other-token") }()
	fake.waitForEntry(t)

	// The cached "token" read must complete while that one is still parked.
	hit := make(chan agentResult, 1)
	go func() {
		agent, herr := client.GetAgent(ctx, "token")
		hit <- agentResult{agent: agent, err: herr}
	}()

	select {
	case got := <-hit:
		require.NoError(t, got.err)
		require.Equal(t, 1000, got.agent.Credits)
	case <-time.After(3 * time.Second):
		t.Fatal("a cache hit blocked behind an in-flight live read (head-of-line blocking)")
	}

	fake.releaseReads()
}

// A waiter must honour its OWN context rather than inheriting the shared read's
// fate: a caller whose deadline expires gives up promptly instead of hanging on a
// stalled flight.
func TestGetAgentWaiterHonoursItsOwnContext(t *testing.T) {
	client, fake := newCoalesceFakeServer(t, 1000)
	fake.holdNextReads()

	go func() { _, _ = client.GetAgent(context.Background(), "token") }()
	fake.waitForEntry(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := client.GetAgent(ctx, "token")
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "a waiter with a dead context must return its own error, not hang")
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("a waiter ignored its own cancelled context")
	}

	fake.releaseReads()
}

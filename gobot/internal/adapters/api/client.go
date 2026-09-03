package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	baseURL            = "https://api.spacetraders.io/v2"
	defaultTimeout     = 30 * time.Second
	defaultMaxRetries  = 10               // Handles persistent 429s
	defaultBackoffBase = 2 * time.Second  // More aggressive backoff strategy
	maxBackoffDuration = 30 * time.Second // Cap exponential backoff to prevent extreme waits

	// RateLimitPerSecond is the sustained request-rate ceiling this client
	// enforces against SpaceTraders. Exported so the budget tracker can
	// compute utilization-vs-ceiling against the same number the limiter
	// actually uses, instead of a second hardcoded copy that could drift.
	RateLimitPerSecond = 2.0

	// RateLimitBurst is the token-bucket burst the limiter actually uses
	// (SpaceTraders allows ~30 req/60s burst). Exported as the SINGLE source of
	// truth for the burst, the twin of RateLimitPerSecond: the config.yaml
	// api.rate_limit.burst knob is NOT plumbed to this client (display-only in
	// `config show`); wiring it through to make burst live-tunable is a
	// deferred decision.
	RateLimitBurst = 30

	// apiPageLimitMax is the largest page size SpaceTraders accepts (openapi.json: limit maximum 20).
	apiPageLimitMax = 20

	// FleetPageLimit exports apiPageLimitMax for callers pricing a fleet enumeration: the
	// divisor turning a hull count into the API cost of one sweep, so no caller keeps its own.
	FleetPageLimit = apiPageLimitMax

	// defaultFleetIsolationAbortStreak: consecutive per-hull probe failures that end
	// an isolation sweep and fail the read CLOSED — the line between "one poisoned
	// record" and "the API is down". Tunable (RULINGS #5).
	defaultFleetIsolationAbortStreak = 3

	// defaultFleetIsolationProbeRetries caps ONE probe's server-error ladder, far
	// below maxRetries: the page read already spent the long one, so minutes of
	// backoff have not cleared it. The fault is INTERMITTENT per hull, making a cheap
	// give-up right — the hull is present-but-unknown for this pass and re-probed on
	// the next, where it will likely read. 429s exempt (serverErrorRetryCap).
	defaultFleetIsolationProbeRetries = 1

	// defaultFleetIsolationProbeBudget caps the per-hull probes ONE enumeration may
	// spend across ALL of its refused pages. The abort streak bounds a SINGLE page's
	// sweep; unbounded in total, a fleet carrying a poisoned record on every page
	// narrows every page, which IS paging at limit=1 — the cost paging at the API
	// maximum exists to avoid. Past the budget a refused page is skipped and reported
	// whole, for one call. Two spans: narrowing buys only the bad hull's identity, and
	// a fleet serving several is one no decision acts on anyway. Tunable (RULINGS #5).
	defaultFleetIsolationProbeBudget = 2 * apiPageLimitMax

	errCodeAgentHasContract = 4511
	errCodeShipMustBeDocked = 4214
	errCodeShipNotDocked    = 4244

	// defaultAgentCacheTTL is how long GetAgent may serve a cached agent before
	// re-reading it live. Agent data (credits/HQ/ship-count) changes rarely, so a
	// short TTL cuts redundant reads. It is a floor on staleness, NOT a
	// safety mechanism: the SAFETY invariant is that every credit-DECREASING call
	// invalidates the cache immediately (see invalidateAgentCache), so a money
	// guard can never read a pre-spend (stale-HIGH) balance after we have spent.
	// Overridable at boot via SetAgentCacheTTL (config: agent_cache_ttl_seconds).
	defaultAgentCacheTTL = 15 * time.Second
)

// APIMetricsRecorder defines the interface for recording API metrics
type APIMetricsRecorder interface {
	RecordAPIRequest(method string, endpoint string, statusCode int, duration float64)
	RecordAPIRetry(method string, endpoint string, reason string)
	RecordRateLimitWait(method string, endpoint string, duration float64)
	SetRateLimiterTokens(tokens float64)
	// RecordRateLimitHeaders reports what the SERVER said about its own limit on one
	// response (x-ratelimit-*). Absent or unparsable numeric fields arrive as
	// metrics.RateLimitHeaderAbsent; sawHeaders says whether ANY x-ratelimit-* header
	// arrived, which is what distinguishes a garbled server from a silent one.
	RecordRateLimitHeaders(kind string, perSecond, burst, remaining, resetSeconds float64, sawHeaders bool)
	// SetRateLimiterTarget reports the rate the limiter is CURRENTLY running at —
	// the governor's target, or RateLimitPerSecond while a 429 hold is in force.
	SetRateLimiterTarget(rps float64)
	RecordRateGovernorTrip(endpoint string)
}

// SpaceTradersClient implements the APIClient interface
type SpaceTradersClient struct {
	httpClient       *http.Client
	rateLimiter      *rate.Limiter
	baseURL          string
	maxRetries       int
	backoffBase      time.Duration
	clock            shared.Clock
	metricsCollector APIMetricsRecorder
	budgetTracker    *metrics.APIBudgetTracker

	fleetIsolationAbortStreak int // 0 selects the default; written once at boot
	fleetIsolationProbeBudget int // 0 selects the default; written once at boot

	// limiterPressure is the always-on smoothed rate-limiter-wait signal
	// (see LimiterPressure). It is updated at the same site that records the
	// wait histogram, but unconditionally — pressure is a control signal the
	// sensing coordinator sheds scanning on, not telemetry, so it must exist
	// even with metrics disabled.
	limiterPressure *LimiterPressure

	// Agent cache. All GetAgent callers share this one client instance, so
	// caching here transparently cuts the redundant live reads for every money
	// guard and monitor at once. agentCacheMu guards every field below.
	// agentCache==nil means empty/invalidated.
	//
	// The mutex is NOT held across the live fetch (it once was). Holding it there
	// made every cache HIT queue behind an in-flight miss, and a /my/agent read
	// that hits the 429 retry ladder can stall for tens of seconds — head-of-line
	// blocking every money guard on the box. agentCacheEpoch replaces the lock as
	// the over-spend safety proof; see GetAgent and invalidateAgentCache.
	agentCacheMu    sync.Mutex
	agentCache      *player.AgentData
	agentCachedAt   time.Time
	agentCacheToken string        // token that produced agentCache (cross-agent guard)
	agentCacheTTL   time.Duration // 0 => defaultAgentCacheTTL

	// agentCacheEpoch counts invalidations. A live fetch samples it before the
	// request and re-checks it before storing: any spend that invalidated while
	// the fetch was in flight bumps the epoch, and the store is DROPPED. That
	// holds the invariant — after any spend the cache is EMPTY, so no money guard
	// can be answered with a pre-spend (stale-HIGH) balance — without serializing
	// readers behind a lock held across the network.
	agentCacheEpoch uint64

	// agentFlight is the single in-flight live /my/agent read that concurrent
	// GetAgent callers share (singleflight), and agentFlightToken the token it
	// authenticates. Callers arriving during a flight WAIT on it instead of
	// issuing their own request, so N concurrent cold readers cost ONE API call —
	// on the failure path too, where N independent retry ladders against a
	// rate-limited API is the worst thing the fleet can do. nil => no flight.
	agentFlight      *agentFlight
	agentFlightToken string

	// onAgentFlightJoin, when non-nil, is invoked by a caller that has joined an
	// in-flight read, immediately before it blocks on it. Test-only seam, always
	// nil in production: it lets a test prove N waiters really did share ONE
	// flight by waiting for them deterministically, instead of racing a sleep and
	// calling the resulting flake a pass. Read under agentCacheMu.
	onAgentFlightJoin func()

	// scheduler is the priority-aware rate-limit scheduler (sp-ratelimit-prio,
	// ARMED Admiral 2026-07-17): every token acquisition goes through it.
	// Trade-critical calls (Buy/Sell Cargo, and calls explicitly marked via
	// WithPriority) acquire the next contended token ahead of deferrable status
	// polls under saturation. This changes NOTHING about how many calls are made
	// or the 2 req/s ceiling, burst, or refill — every token still comes from the
	// same limiter — it only reorders contended waiters. Bounded aging inside the
	// scheduler guarantees no low-priority call is starved. Constructed once at
	// client creation; read on the hot path of every request.
	scheduler *priorityScheduler

	// governor owns the limiter's LIMIT: it raises it to the configured target and
	// retreats to RateLimitPerSecond on a 429 (see rate_governor.go). Unset knob =>
	// target RateLimitPerSecond, so the limiter never moves.
	governor *rateGovernor
}

// NewSpaceTradersClient creates a new SpaceTraders API client with default settings
// Rate limit: 2 requests per second with burst of 30
// Retry: max 10 attempts with 2s exponential backoff + jitter (capped at 30s)
//
// ST_API_BASE_URL, when set and non-empty, overrides the production base URL.
// This is the digital-twin seam: the twin harness spawns the daemon as a
// process, so an env var is the only way to point it at the fake server.
// Production never sets it and keeps hitting the real API, so the constructed
// client is byte-identical to the pre-seam one. Already documented as a
// supported knob in .env.example and docs/CONFIGURATION.md.
func NewSpaceTradersClient() *SpaceTradersClient {
	base := baseURL
	if v := os.Getenv("ST_API_BASE_URL"); v != "" {
		base = v
	}
	return NewSpaceTradersClientWithConfig(
		base,
		defaultMaxRetries,
		defaultBackoffBase,
		nil, // Use RealClock by default
	)
}

// NewSpaceTradersClientWithConfig creates a new SpaceTraders API client with custom configuration
// If clock is nil, uses RealClock for production
// Automatically uses the global API metrics collector if one is set
func NewSpaceTradersClientWithConfig(
	baseURL string,
	maxRetries int,
	backoffBase time.Duration,
	clock shared.Clock,
) *SpaceTradersClient {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	rateLimiter := rate.NewLimiter(rate.Limit(RateLimitPerSecond), RateLimitBurst)
	client := &SpaceTradersClient{
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		rateLimiter:      rateLimiter,
		baseURL:          baseURL,
		maxRetries:       maxRetries,
		backoffBase:      backoffBase,
		clock:            clock,
		metricsCollector: nil,
		limiterPressure:  NewLimiterPressure(defaultLimiterPressureHalfLife),
	}
	client.scheduler = newPriorityScheduler(rateLimiter.Wait, clock, defaultPriorityAgingWindow)
	client.governor = newRateGovernor(rateLimiter)

	return client
}

// SetMetricsCollector sets the metrics collector for the client
// This allows metrics to be enabled after client construction
func (c *SpaceTradersClient) SetMetricsCollector(collector APIMetricsRecorder) {
	c.metricsCollector = collector
}

// getMetricsCollector returns the metrics collector for this client.
// If no local collector is set, it checks for the global API collector.
func (c *SpaceTradersClient) getMetricsCollector() APIMetricsRecorder {
	if c.metricsCollector != nil {
		return c.metricsCollector
	}
	// Check the concrete pointer before boxing it into the interface: a
	// typed-nil *APIMetricsCollector passes callers' nil checks and then
	// crashes on first use (metrics disabled = nil global collector).
	if collector := metrics.GetGlobalAPICollector(); collector != nil {
		return collector
	}
	return nil
}

// SetBudgetTracker sets the API request-budget tracker for this client. This
// allows the tracker to be enabled after client construction, mirroring
// SetMetricsCollector.
func (c *SpaceTradersClient) SetBudgetTracker(tracker *metrics.APIBudgetTracker) {
	c.budgetTracker = tracker
}

// getBudgetTracker returns the budget tracker for this client. If no local
// tracker is set, it falls back to the global tracker. May return nil;
// APIBudgetTracker.Record tolerates a nil receiver, so callers never need an
// extra nil-check.
func (c *SpaceTradersClient) getBudgetTracker() *metrics.APIBudgetTracker {
	if c.budgetTracker != nil {
		return c.budgetTracker
	}
	return metrics.GetGlobalAPIBudgetTracker()
}

// GetRateLimiterTokens returns the current number of available tokens in the rate limiter
// This is useful for monitoring rate limiter saturation (max 30 tokens)
func (c *SpaceTradersClient) GetRateLimiterTokens() float64 {
	return c.rateLimiter.Tokens()
}

// LimiterPressure exposes the client's smoothed rate-limiter-wait tracker —
// the scouting.PressureReader the sensing coordinator consumes. May be nil on
// a hand-built client; LimiterPressure methods are nil-safe and read as no
// pressure.
func (c *SpaceTradersClient) LimiterPressure() *LimiterPressure {
	return c.limiterPressure
}

// SetLimiterPressureHalfLife overrides the pressure tracker's smoothing
// half-life. Wired at daemon boot from
// DaemonConfig.LimiterPressureHalfLifeSeconds via setter injection, so the
// many NewSpaceTradersClient call sites stay untouched (the SetAgentCacheTTL
// idiom, RULINGS #5). halfLife<=0 selects the built-in default. The tracker is
// retuned IN PLACE, so consumers already holding it never go stale.
func (c *SpaceTradersClient) SetLimiterPressureHalfLife(halfLife time.Duration) {
	c.limiterPressure.SetHalfLife(halfLife)
}

// SetFleetIsolationAbortStreak overrides how many consecutive per-hull probe
// failures end an isolation sweep (boot-time setter injection, RULINGS #5; <=0
// selects the default). It tunes the isolation, it does not arm it.
func (c *SpaceTradersClient) SetFleetIsolationAbortStreak(streak int) {
	c.fleetIsolationAbortStreak = streak
}

func (c *SpaceTradersClient) isolationAbortStreak() int {
	if c.fleetIsolationAbortStreak > 0 {
		return c.fleetIsolationAbortStreak
	}
	return defaultFleetIsolationAbortStreak
}

// SetFleetIsolationProbeBudget bounds the per-hull probes ONE fleet enumeration may
// spend narrowing refused pages (boot-time setter injection, RULINGS #5; <=0 selects
// the default). It prices the isolation, it does not disable it: below the budget a
// refused page is skipped and reported whole rather than narrowed.
func (c *SpaceTradersClient) SetFleetIsolationProbeBudget(budget int) {
	c.fleetIsolationProbeBudget = budget
}

func (c *SpaceTradersClient) isolationProbeBudget() int {
	if c.fleetIsolationProbeBudget > 0 {
		return c.fleetIsolationProbeBudget
	}
	return defaultFleetIsolationProbeBudget
}

// acquireRateToken acquires exactly ONE token from the shared rate limiter before
// an API attempt, ordered by the call's priority (endpoint classification,
// overridable via WithPriority). Every token still comes from the SAME limiter,
// so the rate/burst/refill are unchanged — only the order of contended waiters
// differs (sp-ratelimit-prio, ARMED Admiral 2026-07-17).
func (c *SpaceTradersClient) acquireRateToken(ctx context.Context, endpoint string) error {
	return c.scheduler.wait(ctx, priorityForRequest(ctx, endpoint))
}

// request makes an HTTP request with rate limiting and exponential backoff retries
func (c *SpaceTradersClient) request(ctx context.Context, method, path, token string, body interface{}, result interface{}) error {
	return c.requestWithRetryCap(ctx, method, path, token, body, result, nil)
}

// requestWithRetryCap is request() with a per-call server-error ceiling; nil is request().
func (c *SpaceTradersClient) requestWithRetryCap(ctx context.Context, method, path, token string, body interface{}, result interface{}, serverErrorRetryCap *int) error {
	call := apiRequest{method: method, path: path, token: token, body: body, serverErrorRetryCap: serverErrorRetryCap}
	return c.doWithRetry(ctx, call, func(statusCode int, respBody []byte) error {
		if statusCode < 200 || statusCode >= 300 {
			// Typed so callers can classify a permanent 4xx apart from a transient failure via
			// errors.As (negative-cache a 400'd jump gate). APIError.Error() preserves
			// the exact "API error (status %d): %s" string, so every existing message/JSON
			// string-parser (cooldown, insufficient-credits, dock/orbit classifiers) is unaffected.
			return &domainPorts.APIError{StatusCode: statusCode, Body: string(respBody)}
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to unmarshal response: %w", err)
		}
		return nil
	})
}

// requestWithErrorParsing is like request() but unmarshals JSON BEFORE checking status codes
// This allows callers to inspect error details (like error code 4511) even when status is 4xx
func (c *SpaceTradersClient) requestWithErrorParsing(ctx context.Context, method, path, token string, body interface{}, result interface{}) error {
	return c.doWithRetry(ctx, apiRequest{method: method, path: path, token: token, body: body}, func(statusCode int, respBody []byte) error {
		if result != nil {
			if err := json.Unmarshal(respBody, result); err != nil {
				return fmt.Errorf("failed to unmarshal response: %w", err)
			}
		}
		if statusCode >= 200 && statusCode < 300 {
			return nil
		}
		return fmt.Errorf("API error (status %d): %s", statusCode, string(respBody))
	})
}

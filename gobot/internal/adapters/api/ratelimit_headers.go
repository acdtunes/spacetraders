package api

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
)

// rateLimitHeaderPrefix is the lowercase prefix every server rate-limit header shares.
const rateLimitHeaderPrefix = "x-ratelimit"

// rateLimitHeaderAbsent marks a header the server did not send, or one that would not
// parse. Shared with the collector so the two ends cannot drift.
const rateLimitHeaderAbsent = metrics.RateLimitHeaderAbsent

// rateLimitLogInterval is the sampling period for the header log line: one line per
// process per period, sampled by TIME so the cadence is predictable regardless of how
// many calls the fleet makes.
const rateLimitLogInterval = 60 * time.Second

// rateLimitLogLast is the wall-clock nanosecond stamp of the last header log line,
// process-wide. Purely a log sampler — nothing reads it to make a decision.
var rateLimitLogLast atomic.Int64

// rateLimitObservation is what one response's x-ratelimit-* headers said. Every numeric
// field is rateLimitHeaderAbsent when its header was missing or unparsable; sawHeaders
// answers the separate question of whether ANY x-ratelimit-* header arrived at all.
type rateLimitObservation struct {
	kind         string
	perSecond    float64
	burst        float64
	remaining    float64
	resetSeconds float64
	sawHeaders   bool
}

func parseRateLimitHeaders(header http.Header, now time.Time) rateLimitObservation {
	return rateLimitObservation{
		kind:         header.Get("X-Ratelimit-Type"),
		perSecond:    parseRateLimitNumber(header.Get("X-Ratelimit-Limit-Per-Second")),
		burst:        parseRateLimitNumber(header.Get("X-Ratelimit-Limit-Burst")),
		remaining:    parseRateLimitNumber(header.Get("X-Ratelimit-Remaining")),
		resetSeconds: parseRateLimitReset(header.Get("X-Ratelimit-Reset"), now),
		sawHeaders:   hasRateLimitHeader(header),
	}
}

// hasRateLimitHeader is the presence fact the observed-total counter reports, on the same
// prefix scan the sampled log renders: a renamed or garbage x-ratelimit-* header is still a
// server that is talking, which is the one thing present="no" must never claim.
func hasRateLimitHeader(header http.Header) bool {
	for name := range header {
		if isRateLimitHeaderName(name) {
			return true
		}
	}
	return false
}

func isRateLimitHeaderName(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), rateLimitHeaderPrefix)
}

func parseRateLimitNumber(value string) float64 {
	if value == "" {
		return rateLimitHeaderAbsent
	}
	parsed, err := strconv.ParseFloat(value, 64)
	// ParseFloat accepts NaN and ±Inf, either of which poisons every rate()/avg_over_time()
	// on the gauge for good; no rate-limit field is meaningfully negative either.
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return rateLimitHeaderAbsent
	}
	return parsed
}

// parseRateLimitReset accepts both shapes the field is documented to take: a seconds
// count, or an RFC3339 stamp of the moment the window resets.
func parseRateLimitReset(value string, now time.Time) float64 {
	if value == "" {
		return rateLimitHeaderAbsent
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return rateLimitHeaderAbsent // Same poison as above; a past stamp's negative span is real, so it stays.
		}
		return seconds
	}
	if stamp, err := time.Parse(time.RFC3339, value); err == nil {
		return stamp.Sub(now).Seconds()
	}
	return rateLimitHeaderAbsent
}

// observeRateLimitHeaders reports one response's rate-limit headers to metrics and, at
// most once per rateLimitLogInterval, to the log. It reads the headers and nothing else:
// the client's own limiter, burst and retry ladder are untouched by what the server says.
func (c *SpaceTradersClient) observeRateLimitHeaders(header http.Header, statusCode int) {
	observation := parseRateLimitHeaders(header, c.clock.Now())
	if collector := c.getMetricsCollector(); collector != nil {
		collector.RecordRateLimitHeaders(
			observation.kind,
			observation.perSecond,
			observation.burst,
			observation.remaining,
			observation.resetSeconds,
			observation.sawHeaders,
		)
	}
	logRateLimitHeaders(header, statusCode, time.Now())
}

func logRateLimitHeaders(header http.Header, statusCode int, now time.Time) {
	fields := rateLimitHeaderFields(header)
	if len(fields) == 0 {
		return
	}
	last := rateLimitLogLast.Load()
	if last != 0 && now.Sub(time.Unix(0, last)) < rateLimitLogInterval {
		return
	}
	if !rateLimitLogLast.CompareAndSwap(last, now.UnixNano()) {
		return // Another goroutine claimed this period's line.
	}
	log.Printf("INFO: API rate-limit headers status=%d %s", statusCode, strings.Join(fields, " "))
}

// rateLimitHeaderFields renders only the x-ratelimit-* headers, sorted for a stable line.
// The allowlist by prefix is deliberate: no other response header — and never a request
// header such as Authorization — can reach the log through here.
func rateLimitHeaderFields(header http.Header) []string {
	fields := make([]string, 0, len(header))
	for name, values := range header {
		if !isRateLimitHeaderName(name) {
			continue
		}
		fields = append(fields, fmt.Sprintf("%s=%q", strings.ToLower(name), strings.Join(values, ",")))
	}
	sort.Strings(fields)
	return fields
}

package api

import (
	"log"
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// RateGovernorMaxReqPerSec is the ceiling the target knob is clamped to: the 2.0
	// req/s SpaceTraders documents plus the 30-per-60s burst refill on top. Above it
	// we would be asking the account for throughput nothing advertises.
	RateGovernorMaxReqPerSec = 2.5

	// DefaultRateGovernorCooldownMinutes is how long a trip holds the limiter at
	// RateLimitPerSecond when the cooldown knob is unset.
	DefaultRateGovernorCooldownMinutes = 30

	// rateGovernorReleaseCheckInterval throttles the release check the request path
	// drives; the hold is measured in minutes, so once a second is ample.
	rateGovernorReleaseCheckInterval = time.Second
)

// rateGovernor raises the client's shared rate limiter above RateLimitPerSecond when an
// operator sets the knob, and drops it back on the first 429 for a cooldown.
//
// It fails closed in every direction: an absent, malformed or below-floor target leaves
// the limiter at RateLimitPerSecond, the clamp bounds it at RateGovernorMaxReqPerSec, and
// a nil governor is inert. SetLimit on the limiter the priority scheduler already waits on
// is the ONLY actuator — the burst, the scheduler and the retry ladder are untouched.
type rateGovernor struct {
	limiter *rate.Limiter

	mu                sync.Mutex
	targetRate        float64
	governorCooldown  time.Duration
	governorTrippedAt time.Time
	governorTrips     int
	lastReleaseCheck  time.Time
}

func newRateGovernor(limiter *rate.Limiter) *rateGovernor {
	return &rateGovernor{
		limiter:          limiter,
		targetRate:       RateLimitPerSecond,
		governorCooldown: time.Duration(DefaultRateGovernorCooldownMinutes) * time.Minute,
	}
}

// clampTargetRate reads the knob fail-closed: unset, negative, NaN or below the floor all
// mean the floor; above the ceiling means the ceiling.
func clampTargetRate(rps float64) float64 {
	if math.IsNaN(rps) || rps < RateLimitPerSecond {
		return RateLimitPerSecond
	}
	if rps > RateGovernorMaxReqPerSec {
		return RateGovernorMaxReqPerSec
	}
	return rps
}

// SetTargetRate sets the sustained rate the limiter runs at (boot-time setter injection,
// RULINGS #5; 0/unset selects RateLimitPerSecond, i.e. today's behaviour unchanged).
func (c *SpaceTradersClient) SetTargetRate(rps float64) {
	c.governor.setTarget(rps, c.getMetricsCollector())
}

// SetGovernorCooldown sets how long a 429 holds the limiter at RateLimitPerSecond;
// <=0 selects DefaultRateGovernorCooldownMinutes.
func (c *SpaceTradersClient) SetGovernorCooldown(cooldown time.Duration) {
	c.governor.setCooldown(cooldown)
}

func (g *rateGovernor) setTarget(rps float64, collector APIMetricsRecorder) {
	if g == nil {
		return
	}
	target := clampTargetRate(rps)
	if rps > RateGovernorMaxReqPerSec {
		// Once, at boot: the knob is set exactly once at the composition root.
		log.Printf("WARNING: API rate governor: target above the ceiling, clamped requested_req_per_sec=%.2f target_req_per_sec=%.2f",
			rps, target)
	}

	g.mu.Lock()
	g.targetRate = target
	effective := target
	if g.governorTrippedAt.IsZero() {
		g.limiter.SetLimit(rate.Limit(target))
	} else {
		effective = RateLimitPerSecond // A trip in force outranks a new target.
	}
	g.mu.Unlock()

	// Silent at the floor: an unarmed daemon must log exactly what it logged before.
	if target > RateLimitPerSecond {
		log.Printf("INFO: API rate governor: target set target_req_per_sec=%.2f", target)
	}
	reportEffectiveRate(collector, effective)
}

func (g *rateGovernor) setCooldown(cooldown time.Duration) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if cooldown <= 0 {
		cooldown = time.Duration(DefaultRateGovernorCooldownMinutes) * time.Minute
	}
	g.governorCooldown = cooldown
}

// trip is the retreat: the first 429 drops the limiter to RateLimitPerSecond and starts
// (or restarts) the hold. A governor still at the floor is inert, so an unarmed daemon
// neither logs nor counts.
func (g *rateGovernor) trip(now time.Time, endpoint string, collector APIMetricsRecorder) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.targetRate <= RateLimitPerSecond {
		g.mu.Unlock()
		return
	}
	g.governorTrippedAt = now // Restarts the clock, so 429s inside a hold extend it.
	g.governorTrips++
	trips, target, cooldown := g.governorTrips, g.targetRate, g.governorCooldown
	g.limiter.SetLimit(rate.Limit(RateLimitPerSecond))
	g.mu.Unlock()

	log.Printf("WARNING: API rate governor: 429 received, holding at 2.0 req/s target=%.2f cooldown_minutes=%.0f trips_total=%d endpoint=%s",
		target, cooldown.Minutes(), trips, endpoint)
	if collector != nil {
		collector.RecordRateGovernorTrip(endpoint)
	}
	reportEffectiveRate(collector, RateLimitPerSecond)
}

// maybeRelease restores the target once a hold has expired, and re-reports the limiter's
// live limit on the same throttled tick whether or not a hold was in force — the daemon
// installs the metrics collector long after it sets the knob, so the gauge only ever holds
// the armed target if the request path heals it. The request path drives the release too —
// no goroutine, no timer — because a client that is not calling the API does not need the
// higher rate restored.
func (g *rateGovernor) maybeRelease(now time.Time, collector APIMetricsRecorder) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if now.Sub(g.lastReleaseCheck) < rateGovernorReleaseCheckInterval {
		g.mu.Unlock()
		return
	}
	g.lastReleaseCheck = now
	released := false
	if !g.governorTrippedAt.IsZero() && now.Sub(g.governorTrippedAt) >= g.governorCooldown {
		g.governorTrippedAt = time.Time{}
		g.limiter.SetLimit(rate.Limit(g.targetRate))
		released = true
	}
	effective := float64(g.limiter.Limit())
	g.mu.Unlock()

	if released {
		log.Printf("INFO: API rate governor: cooldown over, target restored target_req_per_sec=%.2f", effective)
	}
	reportEffectiveRate(collector, effective)
}

func reportEffectiveRate(collector APIMetricsRecorder, rps float64) {
	if collector != nil {
		collector.SetRateLimiterTarget(rps)
	}
}

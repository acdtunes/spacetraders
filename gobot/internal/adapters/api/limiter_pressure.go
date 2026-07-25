package api

import (
	"math"
	"sync"
	"time"
)

// defaultLimiterPressureHalfLife is the smoothing half-life of the client's
// built-in pressure tracker. It matches the sensing coordinator's tick order of
// magnitude so the smoothed wait reflects roughly the last tick's contention:
// long enough that one slow call cannot flip the rotation, short enough that
// sustained queueing registers within a couple of ticks.
const defaultLimiterPressureHalfLife = 30 * time.Second

// LimiterPressure is a concurrency-safe, time-decayed EWMA of rate-limiter
// wait durations — the API-pressure signal the sensing coordinator sheds
// scanning against. Utilisation is useless for this (it reads ~100% whenever
// work is queued); wait time measures actual contention: near zero with
// headroom, climbing when calls queue.
//
// Each observation is blended with weight 1−0.5^(Δt/halfLife) over the time
// elapsed since the previous one, and reads decay the same way, so with no
// traffic the signal halves every halfLife and drifts toward zero. An
// observation stamped at or before the previous one contributes nothing (the
// elapsed weight is zero) — harmless at real call rates, where consecutive
// waits are always milliseconds apart.
type LimiterPressure struct {
	mu       sync.Mutex
	halfLife time.Duration
	ewma     float64 // seconds
	lastAt   time.Time
}

// NewLimiterPressure creates a pressure tracker with the given half-life.
// A non-positive halfLife selects defaultLimiterPressureHalfLife.
func NewLimiterPressure(halfLife time.Duration) *LimiterPressure {
	if halfLife <= 0 {
		halfLife = defaultLimiterPressureHalfLife
	}
	return &LimiterPressure{halfLife: halfLife}
}

// SetHalfLife retunes the smoothing half-life in place, preserving the tracker
// pointer so consumers wired through the client accessor never go stale. A
// non-positive value selects defaultLimiterPressureHalfLife, mirroring the
// constructor. Nil-safe.
func (p *LimiterPressure) SetHalfLife(halfLife time.Duration) {
	if p == nil {
		return
	}
	if halfLife <= 0 {
		halfLife = defaultLimiterPressureHalfLife
	}
	p.mu.Lock()
	p.halfLife = halfLife
	p.mu.Unlock()
}

// Observe blends one rate-limiter wait into the EWMA. Nil-safe: a zero-value
// client without a tracker simply records nothing.
func (p *LimiterPressure) Observe(wait time.Duration, at time.Time) {
	if p == nil {
		return
	}
	if wait < 0 {
		wait = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	carry := p.carryWeightLocked(at)
	p.ewma = carry*p.ewma + (1-carry)*wait.Seconds()
	if at.After(p.lastAt) {
		p.lastAt = at
	}
}

// Current returns the smoothed wait as of at, decaying toward 0 with no
// traffic. Nil-safe: a missing tracker reads as no pressure. Read-only — a
// read never advances the decay clock, so reads are pure.
func (p *LimiterPressure) Current(at time.Time) time.Duration {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Duration(p.carryWeightLocked(at) * p.ewma * float64(time.Second))
}

// carryWeightLocked is the fraction of the stored EWMA that survives from
// lastAt to at: 0.5^(Δt/halfLife), clamped so a backwards timestamp neither
// inflates nor deflates the signal. On the very first observation lastAt is the
// zero time, the elapsed span is enormous, and the carry underflows to 0 — the
// first wait simply sets the level. The caller must hold p.mu.
func (p *LimiterPressure) carryWeightLocked(at time.Time) float64 {
	elapsed := at.Sub(p.lastAt)
	if elapsed <= 0 {
		return 1
	}
	return math.Pow(0.5, float64(elapsed)/float64(p.halfLife))
}

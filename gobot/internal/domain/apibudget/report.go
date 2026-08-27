// Package apibudget computes the fleet's API request-budget utilization: how
// much of the shared SpaceTraders rate-limiter ceiling is being consumed, by
// which purpose (poll/transact/retry), and by which hull. The shared API req/s
// budget — mostly consumed by per-ship status-poll cadence — is the binding
// fleet-scale wall this package makes measurable.
//
// Everything here is pure: ComputeReport takes an event slice and a point in
// time and derives the report. Collecting events off the live request path and
// surfacing the report over the CLI/gRPC boundary are adapter concerns that
// live outside this package (internal/adapters/metrics, internal/adapters/api).
package apibudget

import (
	"sort"
	"time"
)

// Purpose classifies why an API request was made. Every attempt against the
// SpaceTraders API funnels through exactly one of these three buckets: a retry
// (any attempt after the first, regardless of HTTP method) takes priority over
// the method-based split, so retry share is tracked as its own consumer of
// budget distinct from the poll/transact mix.
type Purpose string

const (
	PurposePoll     Purpose = "poll"
	PurposeTransact Purpose = "transact"
	PurposeRetry    Purpose = "retry"
)

// Source classifies which fleet activity drove an API request. Where Purpose
// splits requests by HTTP shape (poll/transact/retry), Source splits them by
// the work being done, so the sensing budget can be derived as the residual
// the other consumers leave under the ceiling.
//
// The taxonomy is deliberately coarse and quota-free: sensing is the only
// source with unbounded appetite, while trading, contracts, navigation and
// bootstrap are intrinsically bounded by fleet size and tour structure.
// Bounded sources need right-of-way, not rations.
type Source string

const (
	SourceScanning   Source = "scanning"
	SourceCharting   Source = "charting"
	SourceTrading    Source = "trading"
	SourceContract   Source = "contract"
	SourceNavigation Source = "navigation"
	SourceBootstrap  Source = "bootstrap"

	// SourceUnspecified is the zero value: an attempt made on a call path that
	// carries no source tag. Residual arithmetic counts it as non-sensing, so
	// an untagged caller can only ever shrink the sensing budget, never
	// inflate it.
	SourceUnspecified Source = ""
)

// Event is one observed HTTP attempt against the SpaceTraders API.
type Event struct {
	// Hull is the ship symbol this request concerned, or "" if the request was
	// not ship-scoped (e.g. GET /my/agent, GET /systems/*).
	Hull    string
	Purpose Purpose
	// Source is the fleet activity that drove the request, or
	// SourceUnspecified on a call path that carries no tag.
	Source      Source
	Timestamp   time.Time
	RateLimited bool // true if this attempt received a 429
	// RateLimitWait is how long this attempt queued for a token; 0 when unmeasured.
	RateLimitWait time.Duration
}

// HullStats is one hull's request volume within a report window.
type HullStats struct {
	Hull             string  `json:"hull"`
	RequestsInWindow int     `json:"requests_in_window"`
	ReqPerSec        float64 `json:"req_per_sec"`
}

// Report is the computed API-budget snapshot for a window of the given
// duration ending at the time ComputeReport was called with.
type Report struct {
	WindowSeconds        float64             `json:"window_seconds"`
	TotalRequests        int                 `json:"total_requests"`
	GlobalReqPerSec      float64             `json:"global_req_per_sec"`
	CeilingReqPerSec     float64             `json:"ceiling_req_per_sec"`
	UtilizationPct       float64             `json:"utilization_pct"`
	HeadroomReqPerSec    float64             `json:"headroom_req_per_sec"`
	RateLimited429       int                 `json:"rate_limited_429_count"`
	RateLimited429PerMin float64             `json:"rate_limited_429_per_min"`
	PurposeCounts        map[Purpose]int     `json:"purpose_counts"`
	PurposeSharePct      map[Purpose]float64 `json:"purpose_share_pct"`
	// MeanRateLimitWaitSeconds is the mean wait per REQUEST: the delay requests actually
	// meet, not one a clock would average through the idle gaps between bursts.
	MeanRateLimitWaitSeconds float64 `json:"mean_rate_limit_wait_seconds"`
	// HullsToCeiling is the derived scaling number: how many hulls like the
	// currently-observed average could run before saturating the ceiling. 0
	// when no hull-scoped traffic was observed (avoids reporting a
	// meaningless +Inf).
	HullsToCeiling float64     `json:"hulls_to_ceiling"`
	PerHull        []HullStats `json:"per_hull"` // sorted desc by RequestsInWindow
}

// DualReport pairs a narrow "current" window with a rolling 5-minute window
// so callers get both an instantaneous and a smoothed rate.
type DualReport struct {
	Current   Report `json:"current"`
	Rolling5m Report `json:"rolling_5m"`
}

// currentWindow is the narrow window used for DualReport.Current — short
// enough to reflect "right now" while still smoothing over single-request
// noise.
const currentWindow = 10 * time.Second

const rolling5mWindow = 5 * time.Minute

// ComputeDualReport computes both the current and rolling-5m windows from the
// same event slice.
//
// observedSince is when the observer STARTED COLLECTING, and passing it is the whole of sp-fr19d.
// A rate is a count divided by a span, and the only defensible span is the one actually observed:
// dividing by a 5-minute window an in-memory observer has only been alive 23 seconds for reports
// 7.7% while the account sits at 100%, because 46 real requests are spread across 300 seconds of
// which 277 never happened. That was not a rounding error — it is a 13x understatement, it lasts for
// a full window after every process start, and it is permanent under a restart loop.
//
// It is the caller's start time rather than the oldest retained EVENT deliberately. The oldest event
// looks like the same quantity and is not: after an idle stretch it is recent, so a single request
// two seconds ago would divide by two seconds and report 25% off one call. Idle time is real
// observed time and must stay in the denominator; only unobserved time may leave it.
//
// A zero observedSince means "start unknown" and yields the FULL window — the pre-existing
// behaviour — so a caller that cannot say when it started is never handed a fabricated denominator.
func ComputeDualReport(events []Event, now time.Time, ceilingReqPerSec float64, observedSince time.Time) DualReport {
	return DualReport{
		Current:   ComputeReport(events, now, observedWindow(currentWindow, now, observedSince), ceilingReqPerSec),
		Rolling5m: ComputeReport(events, now, observedWindow(rolling5mWindow, now, observedSince), ceilingReqPerSec),
	}
}

// observedWindow narrows a nominal window to the span actually observed, and never widens it.
//
// THE NARROWING ONLY EVER RAISES THE REPORTED RATE. A smaller denominator over the same events is a
// higher req/s and so a higher UtilizationPct, which makes every consumer of this figure STRICTER —
// the api_util money guard refuses growth sooner, never later (RULINGS #4). There is no input for
// which this returns a window wider than the nominal one, so no traffic can be diluted into looking
// affordable.
//
// Every degenerate input falls back to the nominal window rather than to a small one: an unknown
// start, a clock that went backwards, and a span already past the window all return `window`. The
// failure direction matters — inventing a SMALL denominator here would spike utilization toward
// infinity and wedge the fleet, so the fallbacks deliberately err toward the old, permissive
// behaviour rather than toward a spurious block.
func observedWindow(window time.Duration, now, observedSince time.Time) time.Duration {
	if observedSince.IsZero() {
		return window
	}
	elapsed := now.Sub(observedSince)
	if elapsed <= 0 || elapsed >= window {
		return window
	}
	return elapsed
}

// ComputeReport prunes events older than `window` relative to `now` and
// derives the utilization, purpose-split, 429, and per-hull breakdown.
// ceilingReqPerSec is the configured rate-limiter ceiling (sustained
// requests/sec) that all fleet scaling is bounded by.
func ComputeReport(events []Event, now time.Time, window time.Duration, ceilingReqPerSec float64) Report {
	report := Report{
		WindowSeconds:    window.Seconds(),
		CeilingReqPerSec: ceilingReqPerSec,
		PurposeCounts:    make(map[Purpose]int),
		PurposeSharePct:  make(map[Purpose]float64),
	}

	cutoff := now.Add(-window)
	hullCounts := make(map[string]int)
	var waitTotal time.Duration

	for _, e := range events {
		if e.Timestamp.Before(cutoff) || e.Timestamp.After(now) {
			continue
		}
		report.TotalRequests++
		report.PurposeCounts[e.Purpose]++
		if e.RateLimited {
			report.RateLimited429++
		}
		if e.RateLimitWait > 0 {
			waitTotal += e.RateLimitWait
		}
		if e.Hull != "" {
			hullCounts[e.Hull]++
		}
	}

	windowSeconds := window.Seconds()
	if windowSeconds > 0 {
		report.GlobalReqPerSec = float64(report.TotalRequests) / windowSeconds
		report.RateLimited429PerMin = float64(report.RateLimited429) / windowSeconds * 60
	}

	if ceilingReqPerSec > 0 {
		report.UtilizationPct = report.GlobalReqPerSec / ceilingReqPerSec * 100
		report.HeadroomReqPerSec = ceilingReqPerSec - report.GlobalReqPerSec
	}

	if report.TotalRequests > 0 {
		for purpose, count := range report.PurposeCounts {
			report.PurposeSharePct[purpose] = float64(count) / float64(report.TotalRequests) * 100
		}
		report.MeanRateLimitWaitSeconds = waitTotal.Seconds() / float64(report.TotalRequests)
	}

	report.PerHull, report.HullsToCeiling = perHullBreakdown(hullCounts, windowSeconds, ceilingReqPerSec)

	return report
}

// perHullBreakdown derives the per-hull request stats (sorted busiest-first,
// ties broken by hull for deterministic output) and the derived
// hulls-to-ceiling scaling figure: how many hulls at the observed average
// per-hull rate fit under the ceiling. hullsToCeiling stays 0 when no
// hull-scoped traffic was observed or the ceiling is unset (avoids reporting
// a meaningless +Inf).
func perHullBreakdown(hullCounts map[string]int, windowSeconds, ceilingReqPerSec float64) (perHull []HullStats, hullsToCeiling float64) {
	perHull = make([]HullStats, 0, len(hullCounts))
	var hullReqPerSecSum float64
	for hull, count := range hullCounts {
		var reqPerSec float64
		if windowSeconds > 0 {
			reqPerSec = float64(count) / windowSeconds
		}
		hullReqPerSecSum += reqPerSec
		perHull = append(perHull, HullStats{
			Hull:             hull,
			RequestsInWindow: count,
			ReqPerSec:        reqPerSec,
		})
	}
	sort.SliceStable(perHull, func(i, j int) bool {
		if perHull[i].RequestsInWindow != perHull[j].RequestsInWindow {
			return perHull[i].RequestsInWindow > perHull[j].RequestsInWindow
		}
		return perHull[i].Hull < perHull[j].Hull
	})

	if len(hullCounts) > 0 && ceilingReqPerSec > 0 {
		avgPerHullRate := hullReqPerSecSum / float64(len(hullCounts))
		if avgPerHullRate > 0 {
			hullsToCeiling = ceilingReqPerSec / avgPerHullRate
		}
	}
	return perHull, hullsToCeiling
}

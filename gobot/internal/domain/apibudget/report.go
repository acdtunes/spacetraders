// Package apibudget computes the fleet's API request-budget utilization: how
// much of the shared SpaceTraders rate-limiter ceiling is being consumed, by
// which purpose (poll/transact/retry), and by which hull. The architect's 20x
// scaling directive (sp-pwwe) identified the shared API req/s budget — mostly
// consumed by per-ship status-poll cadence — as the binding fleet-scale wall,
// and it was unmeasured (sp-51ti). This package makes it measurable.
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
// the method-based split, since the bead calls for retry share as its own
// consumer of budget distinct from the poll/transact mix.
type Purpose string

const (
	PurposePoll     Purpose = "poll"
	PurposeTransact Purpose = "transact"
	PurposeRetry    Purpose = "retry"
)

// Event is one observed HTTP attempt against the SpaceTraders API.
type Event struct {
	// Hull is the ship symbol this request concerned, or "" if the request was
	// not ship-scoped (e.g. GET /my/agent, GET /systems/*).
	Hull        string
	Purpose     Purpose
	Timestamp   time.Time
	RateLimited bool // true if this attempt received a 429
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
	// HullsToCeiling is the architect's derived scaling number: how many hulls
	// like the currently-observed average could run before saturating the
	// ceiling. 0 when no hull-scoped traffic was observed (avoids reporting a
	// meaningless +Inf).
	HullsToCeiling float64     `json:"hulls_to_ceiling"`
	PerHull        []HullStats `json:"per_hull"` // sorted desc by RequestsInWindow
}

// DualReport pairs a narrow "current" window with the bead-mandated rolling
// 5-minute window so callers get both an instantaneous and a smoothed rate.
type DualReport struct {
	Current   Report `json:"current"`
	Rolling5m Report `json:"rolling_5m"`
}

// currentWindow is the narrow window used for DualReport.Current — short
// enough to reflect "right now" while still smoothing over single-request
// noise.
const currentWindow = 10 * time.Second

// rolling5mWindow is the window the bead names explicitly for the rolling
// req/s figure.
const rolling5mWindow = 5 * time.Minute

// ComputeDualReport computes both the current and rolling-5m windows from the
// same event slice.
func ComputeDualReport(events []Event, now time.Time, ceilingReqPerSec float64) DualReport {
	return DualReport{
		Current:   ComputeReport(events, now, currentWindow, ceilingReqPerSec),
		Rolling5m: ComputeReport(events, now, rolling5mWindow, ceilingReqPerSec),
	}
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

	for _, e := range events {
		if e.Timestamp.Before(cutoff) || e.Timestamp.After(now) {
			continue
		}
		report.TotalRequests++
		report.PurposeCounts[e.Purpose]++
		if e.RateLimited {
			report.RateLimited429++
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

package grpc

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// TourAPISaturationReader turns the shared budget tracker's rolling-5m window into the tour
// solver's saturation reading, so selection sees both resources a plan spends. Same window as
// the autosizer's API-util guard, so the two cannot disagree about how loaded the fleet is.
//
// IT PRICES THE QUEUE, NOT THE THROUGHPUT (trading.SaturationPermille).
//
// THE TRACKER IS RESOLVED AT READ TIME, NOT CAPTURED AT WIRING TIME: a captured nil reads
// exactly like a fleet with headroom, so a wiring order nothing enforces would break this
// invisibly and permanently. Resolving per read makes the ordering irrelevant and any nil
// transient. FAILS OPEN — no spend rides on it, and an unreadable budget ranks on credits/hour.
type TourAPISaturationReader struct {
	resolve func() apiBudgetReporter
}

// SaturationPermille returns how hard the request budget is binding, in permille. 0 means no
// opinion: no resolver, no tracker this tick, no ceiling, a thin window, or nobody queued.
func (r *TourAPISaturationReader) SaturationPermille(ctx context.Context) int {
	if r == nil || r.resolve == nil {
		return 0
	}
	reporter := r.resolve()
	if reporter == nil {
		return 0
	}
	rolling := reporter.Report().Rolling5m
	// Derived per read, so a re-rated limiter retunes the hinge rather than staling it.
	params := trading.APISaturationParamsForLimiter(rolling.CeilingReqPerSec, api.RateLimitBurst)
	permille, ok := trading.SaturationPermille(rolling.MeanRateLimitWaitSeconds, rolling.TotalRequests, params)
	if !ok {
		return 0
	}
	return permille
}

// NewTourAPISaturationReader wires the estimator, resolving the tracker per read.
func NewTourAPISaturationReader() *TourAPISaturationReader {
	return &TourAPISaturationReader{resolve: globalAPIBudgetReporter}
}

package grpc

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// TourAPISaturationReader turns the shared budget tracker's rolling-5m window into the tour
// solver's saturation reading, so selection sees both resources a plan spends. Same window as
// the autosizer's API-util guard, so the two cannot disagree about how loaded the fleet is.
//
// THE TRACKER IS RESOLVED AT READ TIME, NOT CAPTURED AT WIRING TIME: a captured nil reads
// exactly like a fleet with headroom, so a wiring order nothing enforces would break this
// invisibly and permanently. Resolving per read makes the ordering irrelevant and any nil
// transient. FAILS OPEN, unlike the guard sharing its window — no spend rides on it, and an
// unreadable budget ranks tours on credits per hour.
type TourAPISaturationReader struct {
	resolve func() apiBudgetReporter
	params  trading.APISaturationParams
}

// SaturationPermille returns how hard the request budget is binding, in permille. 0 means no
// opinion: no resolver, no tracker this tick, no configured ceiling, a thin window, or
// genuine headroom.
func (r *TourAPISaturationReader) SaturationPermille(ctx context.Context) int {
	if r == nil || r.resolve == nil {
		return 0
	}
	reporter := r.resolve()
	if reporter == nil {
		return 0
	}
	rolling := reporter.Report().Rolling5m
	if rolling.CeilingReqPerSec <= 0 {
		// No ceiling means utilization is a fraction of nothing, not measured headroom.
		return 0
	}
	permille, ok := trading.SaturationPermille(rolling.UtilizationPct, rolling.TotalRequests, r.params)
	if !ok {
		return 0
	}
	return permille
}

// NewTourAPISaturationReader wires the estimator, resolving the tracker per read.
func NewTourAPISaturationReader() *TourAPISaturationReader {
	return &TourAPISaturationReader{
		resolve: globalAPIBudgetReporter,
		params:  trading.DefaultAPISaturationParams(),
	}
}

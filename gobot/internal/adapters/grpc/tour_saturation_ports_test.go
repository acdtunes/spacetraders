package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// window builds a rolling-5m report with the given contention evidence.
func window(requests int, meanWait time.Duration, reqPerSec float64) apibudget.DualReport {
	return apibudget.DualReport{Rolling5m: apibudget.Report{
		WindowSeconds:            300,
		CeilingReqPerSec:         api.RateLimitPerSecond,
		GlobalReqPerSec:          reqPerSec,
		UtilizationPct:           reqPerSec / api.RateLimitPerSecond * 100,
		TotalRequests:            requests,
		MeanRateLimitWaitSeconds: meanWait.Seconds(),
	}}
}

func readerOver(report apibudget.DualReport) *TourAPISaturationReader {
	return &TourAPISaturationReader{resolve: staticReporter(&fakeBudgetReporter{report: report})}
}

// THE DEFECT sp-rdev0 NAMES. Throughput past the limiter is censored at the ceiling and
// averaged across the idle gaps between bursts, so two windows in which every request
// queued for the same nine and a half seconds read 100% and 53% utilized. The reading the
// solver gets must be the SAME in both, because the contention a marginal request meets is
// the same in both.
func TestTourAPISaturationReader_PricesTheQueueRegardlessOfWindowThroughput(t *testing.T) {
	pinned := readerOver(window(600, 9500*time.Millisecond, 2.00))
	bursty := readerOver(window(318, 9500*time.Millisecond, 1.06))

	atCeiling := pinned.SaturationPermille(context.Background())
	halfTheCeiling := bursty.SaturationPermille(context.Background())

	require.Greater(t, atCeiling, 0, "a fleet queueing 9.5s per request is not unconstrained")
	require.Equal(t, atCeiling, halfTheCeiling,
		"the same queue must price the same; a window that served half the ceiling in bursts is not half-idle")
}

// The load-bearing half: a fleet with genuine headroom still reads nothing, so both
// consumers stay inert when a request displaces nothing.
func TestTourAPISaturationReader_ReadsNothingOnAFleetWithGenuineHeadroom(t *testing.T) {
	for _, idle := range []apibudget.DualReport{
		window(300, 0, 1.00),
		window(320, 20*time.Millisecond, 1.07),
		window(440, 10*time.Millisecond, 1.47),
	} {
		require.Zero(t, readerOver(idle).SaturationPermille(context.Background()),
			"a window nobody queued in must price the request budget at nothing")
	}
}

func TestTourAPISaturationReader_FailsOpenWithoutALiveBudgetSurface(t *testing.T) {
	var unwired *TourAPISaturationReader
	require.Zero(t, unwired.SaturationPermille(context.Background()))

	require.Zero(t, (&TourAPISaturationReader{}).SaturationPermille(context.Background()),
		"no resolver means no opinion")

	require.Zero(t, (&TourAPISaturationReader{resolve: func() apiBudgetReporter { return nil }}).
		SaturationPermille(context.Background()),
		"a tracker that does not exist yet means no opinion")

	unconfigured := readerOver(apibudget.DualReport{Rolling5m: apibudget.Report{
		CeilingReqPerSec: 0, TotalRequests: 10_000, MeanRateLimitWaitSeconds: 20,
	}})
	require.Zero(t, unconfigured.SaturationPermille(context.Background()),
		"without a ceiling there is no bucket to derive the hinge from")

	thin := readerOver(window(10, 20*time.Second, 2.0))
	require.Zero(t, thin.SaturationPermille(context.Background()),
		"a window too thin to price a fleet-wide resource off yields no opinion")
}

// The hinge must stay pinned to the limiter the fleet actually queues on. If the rate or
// burst move, this fails rather than leaving the estimator describing a bucket that no
// longer exists.
func TestTourAPISaturationReader_DerivesItsHingeFromTheLiveLimiter(t *testing.T) {
	p := trading.APISaturationParamsForLimiter(api.RateLimitPerSecond, api.RateLimitBurst)

	require.InDelta(t, 1/api.RateLimitPerSecond, p.QueueFloorSeconds, 1e-9,
		"the floor is one token service period")
	require.InDelta(t, api.RateLimitBurst/api.RateLimitPerSecond, p.QueueCeilingSeconds, 1e-9,
		"the ceiling is a full burst drain")

	full := readerOver(window(600, time.Duration(p.QueueCeilingSeconds*float64(time.Second)), 2.0))
	require.Equal(t, trading.APISaturationPermilleMax, full.SaturationPermille(context.Background()))
}

// The resolver runs on every read, so a tracker wired after this port was built is still
// found — the sp-a75fz property, held here too.
func TestTourAPISaturationReader_ResolvesTheTrackerOnEveryRead(t *testing.T) {
	t.Cleanup(func() { metrics.SetGlobalAPIBudgetTracker(nil) })
	metrics.SetGlobalAPIBudgetTracker(nil)

	reader := NewTourAPISaturationReader()
	require.Zero(t, reader.SaturationPermille(context.Background()))

	clock := &shared.MockClock{CurrentTime: shared.NewRealClock().Now()}
	queued := metrics.NewAPIBudgetTracker(api.RateLimitPerSecond, clock)
	for i := 0; i < 600; i++ {
		queued.Record("SHIP-1", apibudget.PurposeTransact, apibudget.SourceTrading, false, 9500*time.Millisecond)
	}
	metrics.SetGlobalAPIBudgetTracker(queued)

	require.Greater(t, reader.SaturationPermille(context.Background()), 0,
		"a port built before the tracker must still price the queue the live tracker measures")
}

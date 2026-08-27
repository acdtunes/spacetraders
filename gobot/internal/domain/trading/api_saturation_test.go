package trading

import (
	"math"
	"testing"
)

// limiterParams is the armed hinge: the live limiter's 2 req/s ceiling and 30-token burst.
func limiterParams() APISaturationParams { return APISaturationParamsForLimiter(2.0, 30) }

// requests returns a request count comfortably above the params' evidence floor.
func requests(p APISaturationParams) int { return p.MinRequests * 10 }

func TestAPISaturationParamsForLimiterDerivesTheHingeFromTheLimiterItself(t *testing.T) {
	p := limiterParams()
	if p.QueueFloorSeconds != 0.5 {
		t.Fatalf("queue floor: got %v, want one token period 0.5s", p.QueueFloorSeconds)
	}
	if p.QueueCeilingSeconds != 15 {
		t.Fatalf("queue ceiling: got %v, want a full burst drain 15s", p.QueueCeilingSeconds)
	}
	if p.MinRequests <= 0 {
		t.Fatalf("min requests: got %d, want an evidence floor", p.MinRequests)
	}
}

func TestAPISaturationParamsForLimiterDescribesNothingWithoutALimiter(t *testing.T) {
	// A ceiling or burst that does not exist cannot be divided by; the zero-value params
	// that come back are the ones SaturationPermille refuses to estimate from.
	for _, c := range []struct {
		ceiling float64
		burst   int
	}{{0, 30}, {-2, 30}, {2, 0}, {2, -1}, {math.NaN(), 30}} {
		if got := APISaturationParamsForLimiter(c.ceiling, c.burst); got != (APISaturationParams{}) {
			t.Fatalf("ceiling %v burst %d: got %+v, want the zero value", c.ceiling, c.burst, got)
		}
	}
}

func TestSaturationPermilleStaysSilentWhileTheLimiterImposesNoQueue(t *testing.T) {
	// Complementary slackness: a request that waits less than one service period queued
	// behind nobody, so the constraint it would compete for is unbound and priced at zero.
	p := limiterParams()
	for _, wait := range []float64{0, 0.01, 0.25, 0.49, p.QueueFloorSeconds} {
		if got, ok := SaturationPermille(wait, requests(p), p); ok || got != 0 {
			t.Fatalf("wait %.2fs: got (%d, %v), want (0, false)", wait, got, ok)
		}
	}
}

func TestSaturationPermilleReadsNothingOnAGenuinelyIdleFleetProfile(t *testing.T) {
	// THE LOAD-BEARING PROPERTY. Both consumers scale a charge by this reading and both
	// must be inert when a request displaces nothing. These are measured windows from a
	// fleet running with real headroom: hundreds of requests served, no queue behind any
	// of them. A statistic that spoke here would over-charge a fleet that owes nothing.
	p := limiterParams()
	for _, profile := range []struct {
		name     string
		wait     float64
		requests int
	}{
		{"half the ceiling, no queue", 0.00, 300},
		{"half the ceiling, a millisecond of jitter", 0.02, 320},
		{"three quarters of the ceiling, no queue", 0.01, 440},
		{"a full window at two thirds of the ceiling", 0.00, 400},
	} {
		if got, ok := SaturationPermille(profile.wait, profile.requests, p); ok || got != 0 {
			t.Fatalf("%s: got (%d, %v), want (0, false)", profile.name, got, ok)
		}
	}
}

func TestSaturationPermilleRisesFromOneServicePeriodToAFullBurstDrain(t *testing.T) {
	// The reading is the proportion of the bucket's committed reservoir, so weight shifts
	// continuously between "queued behind nobody" and "every token already spoken for".
	p := limiterParams()
	span := p.QueueCeilingSeconds - p.QueueFloorSeconds
	cases := []struct {
		wait float64
		want int
	}{
		{p.QueueFloorSeconds + span*0.25, 250},
		{p.QueueFloorSeconds + span*0.50, 500},
		{p.QueueFloorSeconds + span*0.75, 750},
		{p.QueueCeilingSeconds, 1000},
	}
	for _, c := range cases {
		got, ok := SaturationPermille(c.wait, requests(p), p)
		if !ok || got != c.want {
			t.Fatalf("wait %.2fs: got (%d, %v), want (%d, true)", c.wait, got, ok, c.want)
		}
	}
}

func TestSaturationPermilleNeverFallsAsContentionRises(t *testing.T) {
	// Monotone in the evidence: a deeper queue can only ever price the budget higher.
	p := limiterParams()
	previous := 0
	for wait := 0.0; wait <= 40; wait += 0.05 {
		got, ok := SaturationPermille(wait, requests(p), p)
		if !ok && got != 0 {
			t.Fatalf("wait %.2fs: a withheld reading must be 0, got %d", wait, got)
		}
		if got < previous {
			t.Fatalf("wait %.2fs: reading fell from %d to %d", wait, previous, got)
		}
		if got > APISaturationPermilleMax {
			t.Fatalf("wait %.2fs: reading %d escaped the scale", wait, got)
		}
		previous = got
	}
	if previous != APISaturationPermilleMax {
		t.Fatalf("a 40s queue must read fully bound, got %d", previous)
	}
}

func TestSaturationPermilleClampsAQueuePastAFullBurstDrain(t *testing.T) {
	// Past a full burst drain the budget is entirely committed, not worth more than
	// everything else on the board.
	p := limiterParams()
	got, ok := SaturationPermille(25.7, requests(p), p)
	if !ok || got != APISaturationPermilleMax {
		t.Fatalf("got (%d, %v), want (%d, true)", got, ok, APISaturationPermilleMax)
	}
}

func TestSaturationPermilleIsSilentOnThinOrDegenerateEvidence(t *testing.T) {
	// THE FAIL-OPEN SURFACE. A thin window, a nonsensical wait, or a params shape that
	// describes nothing all yield no opinion, and selection ranks on credits/hour.
	p := limiterParams()
	if got, ok := SaturationPermille(20, p.MinRequests-1, p); ok || got != 0 {
		t.Fatalf("thin window: got (%d, %v), want (0, false)", got, ok)
	}
	for _, wait := range []float64{-5, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got, ok := SaturationPermille(wait, requests(p), p); ok || got != 0 {
			t.Fatalf("wait %v: got (%d, %v), want (0, false)", wait, got, ok)
		}
	}
	for _, bad := range []APISaturationParams{
		{},
		{MinRequests: 10, QueueFloorSeconds: 0.5, QueueCeilingSeconds: 0.5},
		{MinRequests: 10, QueueFloorSeconds: 15, QueueCeilingSeconds: 0.5},
		{MinRequests: 10, QueueFloorSeconds: -1, QueueCeilingSeconds: 15},
		{MinRequests: 0, QueueFloorSeconds: 0.5, QueueCeilingSeconds: 15},
	} {
		if got, ok := SaturationPermille(20, 10_000, bad); ok || got != 0 {
			t.Fatalf("params %+v: got (%d, %v), want (0, false)", bad, got, ok)
		}
	}
}

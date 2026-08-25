package trading

import (
	"testing"
	"time"
)

// samplesAt builds n identical samples all recorded ageBefore the reference instant.
func samplesAt(now time.Time, ageBefore time.Duration, waitSeconds, n int) []JumpTollSample {
	out := make([]JumpTollSample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, JumpTollSample{
			WaitSeconds:     waitSeconds,
			CooldownSeconds: waitSeconds * 4 / 5,
			RecordedAt:      now.Add(-ageBefore),
		})
	}
	return out
}

func TestEstimatePerHopTollTracksTheSamplesItIsGiven(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	p := DefaultJumpTollParams()

	got, ok := EstimatePerHopTollSeconds(samplesAt(now, 10*time.Minute, 1200, p.MinSamples), now, p)

	if !ok {
		t.Fatalf("a full window of samples must produce an estimate")
	}
	if got != 1200 {
		t.Fatalf("estimate = %d, want 1200 (the level every sample sits at)", got)
	}
}

func TestEstimatePerHopTollDecaysAnOldRegimeOut(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	p := DefaultJumpTollParams()

	// An old, contention-inflated regime that is four half-lives back, against a fresh one
	// at half the level and the same sample count. Recency must dominate: the fat old
	// samples are worth 1/16 of the fresh ones by weight.
	var samples []JumpTollSample
	samples = append(samples, samplesAt(now, 4*p.HalfLife, 1700, 40)...)
	samples = append(samples, samplesAt(now, 5*time.Minute, 800, 40)...)

	got, ok := EstimatePerHopTollSeconds(samples, now, p)
	if !ok {
		t.Fatalf("80 samples must clear the floor")
	}
	if got >= 1000 {
		t.Fatalf("estimate = %d; the decayed old regime still dominates (want well under the 1250 midpoint)", got)
	}
	if got < 800 {
		t.Fatalf("estimate = %d; must not fall below the freshest measured level (800)", got)
	}
}

func TestEstimatePerHopTollWithholdsAnOverrideBelowTheSampleFloor(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	p := DefaultJumpTollParams()

	if _, ok := EstimatePerHopTollSeconds(nil, now, p); ok {
		t.Fatalf("no samples must yield no override — the fail-open pin")
	}
	if _, ok := EstimatePerHopTollSeconds(samplesAt(now, time.Minute, 1200, p.MinSamples-1), now, p); ok {
		t.Fatalf("%d samples is below the floor of %d — must yield no override", p.MinSamples-1, p.MinSamples)
	}
	if _, ok := EstimatePerHopTollSeconds(samplesAt(now, time.Minute, 1200, p.MinSamples), now, p); !ok {
		t.Fatalf("exactly the floor of %d samples must yield an override", p.MinSamples)
	}
}

func TestEstimatePerHopTollIsMedianRobustInsideABucket(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	p := DefaultJumpTollParams()

	// One bucket, mostly 900s hops plus a handful of admissible-but-fat tail readings.
	// A mean would be dragged past 1100; the bucket median must stay at the mode.
	var samples []JumpTollSample
	samples = append(samples, samplesAt(now, 10*time.Minute, 900, 30)...)
	samples = append(samples, samplesAt(now, 10*time.Minute, 3500, 8)...)

	got, ok := EstimatePerHopTollSeconds(samples, now, p)
	if !ok {
		t.Fatalf("38 samples must clear the floor")
	}
	if got != 900 {
		t.Fatalf("estimate = %d, want 900 — the tail readings must not move a median", got)
	}
}

func TestEstimatePerHopTollDropsSamplesOutsideTheAdmissibleBand(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	p := DefaultJumpTollParams()

	// A cooldown ride after a restart, and an instant no-op: neither is a hop's economic
	// cost. Dropping them must also drop them from the sample COUNT, so a window made only
	// of inadmissible readings withholds the override rather than estimating off nothing.
	var samples []JumpTollSample
	samples = append(samples, samplesAt(now, time.Minute, p.MaxSampleSeconds+1, 40)...)
	samples = append(samples, samplesAt(now, time.Minute, p.MinSampleSeconds-1, 40)...)
	if _, ok := EstimatePerHopTollSeconds(samples, now, p); ok {
		t.Fatalf("every sample was out of band — must yield no override")
	}

	// Same for age: a sample older than the window is not evidence about now.
	stale := samplesAt(now, p.Window+time.Hour, 1200, 60)
	if _, ok := EstimatePerHopTollSeconds(stale, now, p); ok {
		t.Fatalf("every sample was outside the lookback window — must yield no override")
	}
}

func TestEstimatePerHopTollClampsToTheSolversTermBounds(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	p := DefaultJumpTollParams()

	high, ok := EstimatePerHopTollSeconds(samplesAt(now, time.Minute, 3400, 40), now, p)
	if !ok || float64(high) != CrossingTermMaxSeconds {
		t.Fatalf("high estimate = %d (ok=%v), want the term ceiling %.0f", high, ok, CrossingTermMaxSeconds)
	}
	low, ok := EstimatePerHopTollSeconds(samplesAt(now, time.Minute, 90, 40), now, p)
	if !ok || float64(low) != CrossingTermMinSeconds {
		t.Fatalf("low estimate = %d (ok=%v), want the term floor %.0f", low, ok, CrossingTermMinSeconds)
	}
}

func TestDefaultJumpTollParamsAreCoherent(t *testing.T) {
	p := DefaultJumpTollParams()
	if p.Window <= 0 || p.Bucket <= 0 || p.HalfLife <= 0 {
		t.Fatalf("window/bucket/half-life must all be positive: %+v", p)
	}
	if p.Bucket > p.Window {
		t.Fatalf("a bucket wider than the window collapses the decay: %+v", p)
	}
	if p.MinSamples <= 0 {
		t.Fatalf("a non-positive sample floor would let one reading reprice the fleet: %+v", p)
	}
	if p.MinSampleSeconds <= 0 || p.MaxSampleSeconds <= p.MinSampleSeconds {
		t.Fatalf("the admissible band must be a real interval: %+v", p)
	}
	// A degenerate params value must not silently estimate off nothing.
	if _, ok := EstimatePerHopTollSeconds(samplesAt(time.Now(), time.Minute, 1200, 100), time.Now(), JumpTollParams{}); ok {
		t.Fatalf("zero-value params must withhold the override, not invent a window")
	}
}

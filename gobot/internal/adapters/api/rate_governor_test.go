package api

// The governor buys sustained throughput above the 2.0 req/s the client has always
// capped itself at, and pays for it with an immediate retreat on the first 429. These
// tests pin both halves: the raise only happens when an operator asks for it, and the
// retreat happens without one.

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	prevWriter, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&out)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	return &out
}

// governorServer answers every request with whatever status the returned pointer holds,
// so one test can flip a live client between 429 and 200 between requests.
func governorServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	status := new(int)
	*status = http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(*status)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(server.Close)
	return server, status
}

// maxRetries 0 makes one request cost exactly one attempt, so each test step maps to a
// single governor observation instead of a ladder of them.
func newGovernorTestClient(t *testing.T, serverURL string) (*SpaceTradersClient, *shared.MockClock, *recordingMetrics) {
	t.Helper()
	clock := &shared.MockClock{CurrentTime: time.Unix(1_700_000_000, 0).UTC()}
	client := NewSpaceTradersClientWithConfig(serverURL, 0, time.Millisecond, clock)
	recorder := &recordingMetrics{}
	client.SetMetricsCollector(recorder)
	return client, clock, recorder
}

func callOnce(t *testing.T, client *SpaceTradersClient) {
	t.Helper()
	_ = client.request(context.Background(), http.MethodGet, "/my/agent", "test-token", nil, nil)
}

func limitOf(t *testing.T, client *SpaceTradersClient) float64 {
	t.Helper()
	return float64(client.rateLimiter.Limit())
}

// THE KNOB IS THE ONLY THING THAT RAISES THE LIMITER, and it raises the one the
// scheduler already wraps — a second limiter would leave the fleet at 2.0 forever.
func TestSetTargetRateRaisesTheLiveLimiter(t *testing.T) {
	out := captureLog(t)
	server, _ := governorServer(t)
	client, _, _ := newGovernorTestClient(t, server.URL)

	client.SetTargetRate(2.2)

	if got := limitOf(t, client); got != 2.2 {
		t.Errorf("limiter limit = %v, want 2.2", got)
	}
	if !strings.Contains(out.String(), "API rate governor: target set") {
		t.Errorf("no target-set line logged: %q", out.String())
	}
	if !strings.Contains(out.String(), "target_req_per_sec=2.20") {
		t.Errorf("the target-set line omits the target: %q", out.String())
	}
}

// THE FIRST 429 IS THE WHOLE SAFETY ARGUMENT: the experiment retreats to the documented
// 2.0 on the first sign the account cannot sustain the higher rate.
func TestA429DropsTheLimiterBackToTwoAndCountsTheTrip(t *testing.T) {
	out := captureLog(t)
	server, status := governorServer(t)
	client, _, recorder := newGovernorTestClient(t, server.URL)
	client.SetTargetRate(2.2)
	client.SetGovernorCooldown(30 * time.Minute)

	*status = http.StatusTooManyRequests
	callOnce(t, client)

	if got := limitOf(t, client); got != RateLimitPerSecond {
		t.Errorf("limiter limit after a 429 = %v, want %v", got, RateLimitPerSecond)
	}
	logged := out.String()
	if !strings.Contains(logged, "WARNING: API rate governor: 429 received, holding at 2.0 req/s") {
		t.Errorf("no trip warning logged: %q", logged)
	}
	if !strings.Contains(logged, "trips_total=1") {
		t.Errorf("the trip warning omits the trip count: %q", logged)
	}
	wantEndpoint := "endpoint=" + apiEndpointClassifier.classify("/my/agent")
	if !strings.Contains(logged, wantEndpoint) {
		t.Errorf("the trip warning omits %q: %q", wantEndpoint, logged)
	}
	if len(recorder.governorTrips) != 1 {
		t.Errorf("recorded %d governor trips, want 1", len(recorder.governorTrips))
	}
	if len(recorder.limiterTargets) == 0 || recorder.limiterTargets[len(recorder.limiterTargets)-1] != RateLimitPerSecond {
		t.Errorf("the effective-rate gauge was not moved to %v: %v", RateLimitPerSecond, recorder.limiterTargets)
	}
}

// THE HOLD IS A COOLDOWN, NOT A KILL SWITCH — a governor that never releases turns one
// transient 429 into a permanent 20% throughput loss nobody would notice.
func TestCooldownExpiryRestoresTheTargetOnTheNextRequest(t *testing.T) {
	out := captureLog(t)
	server, status := governorServer(t)
	client, clock, _ := newGovernorTestClient(t, server.URL)
	client.SetTargetRate(2.2)
	client.SetGovernorCooldown(30 * time.Minute)

	*status = http.StatusTooManyRequests
	callOnce(t, client)
	*status = http.StatusOK
	clock.Advance(31 * time.Minute)
	callOnce(t, client)

	if got := limitOf(t, client); got != 2.2 {
		t.Errorf("limiter limit after the cooldown = %v, want 2.2", got)
	}
	if !strings.Contains(out.String(), "API rate governor: cooldown over, target restored") {
		t.Errorf("no release line logged: %q", out.String())
	}
}

// A SECOND 429 INSIDE THE COOLDOWN RESTARTS THE HOLD. Counting from the first one instead
// would let a fleet that is 429ing continuously climb back to the higher rate on schedule.
func TestASecond429DuringTheCooldownRestartsTheHold(t *testing.T) {
	server, status := governorServer(t)
	client, clock, recorder := newGovernorTestClient(t, server.URL)
	client.SetTargetRate(2.2)
	client.SetGovernorCooldown(30 * time.Minute)

	*status = http.StatusTooManyRequests
	callOnce(t, client)
	clock.Advance(20 * time.Minute)
	callOnce(t, client) // second 429, 20 minutes into the first hold

	*status = http.StatusOK
	clock.Advance(15 * time.Minute) // 35 min after the FIRST trip, 15 after the second
	callOnce(t, client)
	if got := limitOf(t, client); got != RateLimitPerSecond {
		t.Errorf("limiter limit 15 min into the restarted hold = %v, want %v", got, RateLimitPerSecond)
	}

	clock.Advance(16 * time.Minute)
	callOnce(t, client)
	if got := limitOf(t, client); got != 2.2 {
		t.Errorf("limiter limit after the restarted hold expired = %v, want 2.2", got)
	}
	if len(recorder.governorTrips) != 2 {
		t.Errorf("recorded %d governor trips, want 2", len(recorder.governorTrips))
	}
}

// THE CLAMP IS THE CEILING ON THE EXPERIMENT: 2.5 is the documented per-second rate plus
// the burst refill, and a fat-fingered knob must not be able to exceed it.
func TestTargetRateIsClampedAtBothEnds(t *testing.T) {
	tests := []struct {
		name      string
		set       float64
		want      float64
		wantWarn  bool
		wantQuiet bool
	}{
		{name: "above the ceiling clamps to 2.5", set: 3.0, want: RateGovernorMaxReqPerSec, wantWarn: true},
		{name: "below the floor reads as 2.0", set: 1.0, want: RateLimitPerSecond, wantQuiet: true},
		{name: "zero reads as 2.0", set: 0, want: RateLimitPerSecond, wantQuiet: true},
		{name: "negative reads as 2.0", set: -5, want: RateLimitPerSecond, wantQuiet: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := captureLog(t)
			server, _ := governorServer(t)
			client, _, _ := newGovernorTestClient(t, server.URL)

			client.SetTargetRate(tc.set)

			if got := limitOf(t, client); got != tc.want {
				t.Errorf("limiter limit for target %v = %v, want %v", tc.set, got, tc.want)
			}
			if tc.wantWarn && !strings.Contains(out.String(), "WARNING: API rate governor: target above the ceiling") {
				t.Errorf("target %v was clamped silently: %q", tc.set, out.String())
			}
			if tc.wantQuiet && strings.Contains(out.String(), "API rate governor") {
				t.Errorf("target %v logged a governor line despite changing nothing: %q", tc.set, out.String())
			}
		})
	}
}

// BYTE-IDENTITY WITH THE KNOB ABSENT. Nobody has armed this yet, so a daemon that does not
// set the knob must behave exactly as it did before the governor existed — same 2.0 limit,
// same silence, even across a 429.
func TestWithoutTheKnobTheClientIsUnchangedAtTwoRequestsPerSecond(t *testing.T) {
	out := captureLog(t)
	server, status := governorServer(t)
	client, _, recorder := newGovernorTestClient(t, server.URL)

	callOnce(t, client)
	*status = http.StatusTooManyRequests
	callOnce(t, client)

	if got := limitOf(t, client); got != RateLimitPerSecond {
		t.Errorf("limiter limit with no knob set = %v, want exactly %v", got, RateLimitPerSecond)
	}
	if client.rateLimiter.Burst() != RateLimitBurst {
		t.Errorf("limiter burst = %d, want %d", client.rateLimiter.Burst(), RateLimitBurst)
	}
	if strings.Contains(out.String(), "API rate governor") {
		t.Errorf("the governor spoke without being armed: %q", out.String())
	}
	if len(recorder.governorTrips) != 0 {
		t.Errorf("recorded %d governor trips with no knob set, want 0", len(recorder.governorTrips))
	}
}

// THE GAUGE IS THE OPERATOR'S ONLY READOUT for "is the raise live", and the daemon installs
// the metrics collector hundreds of lines AFTER it sets the knob — so a gauge written only at
// set/trip/release would export 0 for the life of an armed daemon that never sees a 429.
func TestTheEffectiveRateGaugeHealsWhenTheCollectorArrivesAfterTheKnob(t *testing.T) {
	server, _ := governorServer(t)
	clock := &shared.MockClock{CurrentTime: time.Unix(1_700_000_000, 0).UTC()}
	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, clock)

	client.SetTargetRate(2.2) // Collector still nil, exactly as at the composition root.
	recorder := &recordingMetrics{}
	client.SetMetricsCollector(recorder)
	callOnce(t, client)

	if got := limitOf(t, client); got != 2.2 {
		t.Fatalf("limiter limit = %v, want 2.2", got)
	}
	if len(recorder.limiterTargets) == 0 || recorder.limiterTargets[len(recorder.limiterTargets)-1] != 2.2 {
		t.Errorf("the effective-rate gauge never reported the armed target: %v", recorder.limiterTargets)
	}
}

// The scheduler is constructed over rateLimiter.Wait, so SetLimit is the only actuator that
// can move the rate every call actually pays; a governor that swapped the limiter would move
// a limiter nothing waits on.
func TestTheGovernorActuatesTheSameLimiterTheSchedulerWaitsOn(t *testing.T) {
	server, _ := governorServer(t)
	client, _, _ := newGovernorTestClient(t, server.URL)
	before := client.rateLimiter

	client.SetTargetRate(2.4)

	if client.rateLimiter != before {
		t.Fatal("SetTargetRate replaced the limiter instead of retuning it in place")
	}
	if client.rateLimiter.Limit() != rate.Limit(2.4) {
		t.Errorf("limiter limit = %v, want 2.4", client.rateLimiter.Limit())
	}
}

// The cooldown knob is an operator number, so 0 must mean "the built-in default", never
// "release immediately" — the fail-closed reading of an unset value.
func TestUnsetCooldownSelectsTheDefaultHold(t *testing.T) {
	server, status := governorServer(t)
	client, clock, _ := newGovernorTestClient(t, server.URL)
	client.SetTargetRate(2.2)
	client.SetGovernorCooldown(0)

	*status = http.StatusTooManyRequests
	callOnce(t, client)
	*status = http.StatusOK
	clock.Advance(time.Duration(DefaultRateGovernorCooldownMinutes)*time.Minute - time.Minute)
	callOnce(t, client)

	if got := limitOf(t, client); got != RateLimitPerSecond {
		t.Errorf("limiter released a minute early with an unset cooldown: limit = %v", got)
	}
}

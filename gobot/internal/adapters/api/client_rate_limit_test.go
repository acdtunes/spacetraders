package api

import (
	"os"
	"testing"

	"golang.org/x/time/rate"
)

// The ST_API_RATE_LIMIT_PER_SEC / ST_API_RATE_LIMIT_BURST seams let the
// digital-twin test stacks raise the client's request-rate ceiling so a
// synchronous buy's ~11 twin HTTP calls stop serialising behind the 2 req/sec
// prod limiter (~5.5s/hauler of rateLimiter.Wait). Production leaves BOTH env
// vars unset and MUST stay byte-identical to the pre-seam limiter
// (rate.Limit(2.0), burst 30) — mirrors ST_CLOCK_DRIFT_BUFFER_MS (st-drm.8).

// restoreRateLimitEnv snapshots both seam vars and restores them after the test
// so cases can freely set/unset each without leaking into other tests.
func restoreRateLimitEnv(t *testing.T) {
	t.Helper()
	perSec, hadPerSec := os.LookupEnv("ST_API_RATE_LIMIT_PER_SEC")
	burst, hadBurst := os.LookupEnv("ST_API_RATE_LIMIT_BURST")
	t.Cleanup(func() {
		reset := func(key, val string, had bool) {
			if had {
				os.Setenv(key, val)
			} else {
				os.Unsetenv(key)
			}
		}
		reset("ST_API_RATE_LIMIT_PER_SEC", perSec, hadPerSec)
		reset("ST_API_RATE_LIMIT_BURST", burst, hadBurst)
	})
}

func setOrUnset(key, val string) {
	if val == "" {
		os.Unsetenv(key)
		return
	}
	os.Setenv(key, val)
}

func TestResolveRateLimit(t *testing.T) {
	restoreRateLimitEnv(t)

	tests := []struct {
		name      string
		perSecEnv string
		burstEnv  string
		wantRate  rate.Limit
		wantBurst int
	}{
		{
			name:      "unset -> prod defaults (byte-identical to pre-seam 2.0/30)",
			perSecEnv: "",
			burstEnv:  "",
			wantRate:  rate.Limit(2.0),
			wantBurst: 30,
		},
		{
			name:      "valid values -> parsed (the test-stack 100/200)",
			perSecEnv: "100",
			burstEnv:  "200",
			wantRate:  rate.Limit(100),
			wantBurst: 200,
		},
		{
			name:      "fractional rate parses",
			perSecEnv: "2.5",
			burstEnv:  "5",
			wantRate:  rate.Limit(2.5),
			wantBurst: 5,
		},
		{
			name:      "garbage -> defaults",
			perSecEnv: "garbage",
			burstEnv:  "notanumber",
			wantRate:  rate.Limit(2.0),
			wantBurst: 30,
		},
		{
			name:      "zero -> defaults (a 0/sec limiter would deadlock every request)",
			perSecEnv: "0",
			burstEnv:  "0",
			wantRate:  rate.Limit(2.0),
			wantBurst: 30,
		},
		{
			name:      "negative -> defaults",
			perSecEnv: "-5",
			burstEnv:  "-1",
			wantRate:  rate.Limit(2.0),
			wantBurst: 30,
		},
		{
			name:      "one var set, the other unset -> independent fallback",
			perSecEnv: "50",
			burstEnv:  "",
			wantRate:  rate.Limit(50),
			wantBurst: 30,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setOrUnset("ST_API_RATE_LIMIT_PER_SEC", tc.perSecEnv)
			setOrUnset("ST_API_RATE_LIMIT_BURST", tc.burstEnv)

			gotRate, gotBurst := resolveRateLimit()
			if gotRate != tc.wantRate {
				t.Fatalf("rate = %v, want %v", gotRate, tc.wantRate)
			}
			if gotBurst != tc.wantBurst {
				t.Fatalf("burst = %d, want %d", gotBurst, tc.wantBurst)
			}
		})
	}
}

// The constructor must apply the resolved seam to the live limiter (observable
// via the limiter's own Limit()/Burst() accessors), so the wiring — not just the
// resolver — is proven. Prod (env unset) stays at 2.0/30.
func TestNewClientAppliesRateLimitSeam(t *testing.T) {
	restoreRateLimitEnv(t)

	os.Unsetenv("ST_API_RATE_LIMIT_PER_SEC")
	os.Unsetenv("ST_API_RATE_LIMIT_BURST")
	prod := NewSpaceTradersClientWithConfig(baseURL, defaultMaxRetries, defaultBackoffBase, nil)
	if prod.rateLimiter.Limit() != rate.Limit(2.0) {
		t.Fatalf("prod (env unset) rate = %v, want 2.0", prod.rateLimiter.Limit())
	}
	if prod.rateLimiter.Burst() != 30 {
		t.Fatalf("prod (env unset) burst = %d, want 30", prod.rateLimiter.Burst())
	}

	os.Setenv("ST_API_RATE_LIMIT_PER_SEC", "100")
	os.Setenv("ST_API_RATE_LIMIT_BURST", "200")
	fast := NewSpaceTradersClientWithConfig(baseURL, defaultMaxRetries, defaultBackoffBase, nil)
	if fast.rateLimiter.Limit() != rate.Limit(100) {
		t.Fatalf("test stack rate = %v, want 100", fast.rateLimiter.Limit())
	}
	if fast.rateLimiter.Burst() != 200 {
		t.Fatalf("test stack burst = %d, want 200", fast.rateLimiter.Burst())
	}
}

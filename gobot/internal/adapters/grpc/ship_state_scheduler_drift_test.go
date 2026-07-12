// internal/adapters/grpc/ship_state_scheduler_drift_test.go
package grpc

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The ST_CLOCK_DRIFT_BUFFER_MS seam (st-drm.8) lets the digital-twin test stacks shrink the
// arrival/cooldown clock-drift clamp from its 1s prod default so compressed twin arrivals resolve
// fast. Production leaves the env unset and MUST stay byte-identical to the pre-seam behaviour.
func TestResolveClockDriftBuffer(t *testing.T) {
	orig, had := os.LookupEnv("ST_CLOCK_DRIFT_BUFFER_MS")
	t.Cleanup(func() {
		if had {
			os.Setenv("ST_CLOCK_DRIFT_BUFFER_MS", orig)
		} else {
			os.Unsetenv("ST_CLOCK_DRIFT_BUFFER_MS")
		}
	})

	// unset -> the 1s default (prod byte-identical)
	os.Unsetenv("ST_CLOCK_DRIFT_BUFFER_MS")
	require.Equal(t, ClockDriftBuffer, resolveClockDriftBuffer())
	require.Equal(t, 1000*time.Millisecond, resolveClockDriftBuffer())

	// a positive integer -> that many milliseconds (test stacks set 50)
	os.Setenv("ST_CLOCK_DRIFT_BUFFER_MS", "50")
	require.Equal(t, 50*time.Millisecond, resolveClockDriftBuffer())

	// garbage -> default
	os.Setenv("ST_CLOCK_DRIFT_BUFFER_MS", "garbage")
	require.Equal(t, ClockDriftBuffer, resolveClockDriftBuffer())

	// non-positive -> default (sane floor: values below 1ms fall back rather than arming a ~0 delay)
	os.Setenv("ST_CLOCK_DRIFT_BUFFER_MS", "0")
	require.Equal(t, ClockDriftBuffer, resolveClockDriftBuffer())
	os.Setenv("ST_CLOCK_DRIFT_BUFFER_MS", "-5")
	require.Equal(t, ClockDriftBuffer, resolveClockDriftBuffer())

	// the 1ms floor itself is honoured
	os.Setenv("ST_CLOCK_DRIFT_BUFFER_MS", "1")
	require.Equal(t, 1*time.Millisecond, resolveClockDriftBuffer())
}

// NewShipStateScheduler resolves the seam once at construction (mirroring how the API client
// reads ST_API_BASE_URL once at NewSpaceTradersClient) and stores it on the scheduler.
func TestNewShipStateSchedulerAppliesDriftBufferSeam(t *testing.T) {
	orig, had := os.LookupEnv("ST_CLOCK_DRIFT_BUFFER_MS")
	t.Cleanup(func() {
		if had {
			os.Setenv("ST_CLOCK_DRIFT_BUFFER_MS", orig)
		} else {
			os.Unsetenv("ST_CLOCK_DRIFT_BUFFER_MS")
		}
	})

	os.Unsetenv("ST_CLOCK_DRIFT_BUFFER_MS")
	prod := NewShipStateScheduler(nil, &shared.RealClock{}, nil)
	require.Equal(t, ClockDriftBuffer, prod.driftBuffer, "prod (env unset) keeps the 1s default")

	os.Setenv("ST_CLOCK_DRIFT_BUFFER_MS", "50")
	test := NewShipStateScheduler(nil, &shared.RealClock{}, nil)
	require.Equal(t, 50*time.Millisecond, test.driftBuffer, "test stack picks up the 50ms clamp")
}

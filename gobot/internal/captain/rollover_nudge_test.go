package watchkeeper

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func rolloverHoursPtr(n int) *int { return &n }

// TestRolloverNudgeFiresForSessionPastAgeThreshold is the core sp-0zx9 case: a
// live gc-managed session whose own reported age has crossed
// RolloverNudgeHours gets a "rollover due" nudge.
func TestRolloverNudgeFiresForSessionPastAgeThreshold(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.RolloverNudgeHours = rolloverHoursPtr(24)
	sup.lastSession = time.Now() // heartbeat/surveyor cadence not due — isolate the rollover path
	now := time.Now()
	gw.sessions = []SessionInfo{
		{Alias: "shipwright", State: "active", CreatedAt: now.Add(-25 * time.Hour)},
	}

	_, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)

	require.Equal(t, 1, nudgesTo(gw, "shipwright"))
	require.Contains(t, gw.nudges[0][1], "rollover")
}

func TestRolloverNudgeSkipsSessionUnderAgeThreshold(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.RolloverNudgeHours = rolloverHoursPtr(24)
	sup.lastSession = time.Now()
	now := time.Now()
	gw.sessions = []SessionInfo{
		{Alias: "shipwright", State: "active", CreatedAt: now.Add(-1 * time.Hour)},
	}

	_, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)

	require.Equal(t, 0, nudgesTo(gw, "shipwright"))
}

func TestRolloverNudgeSkipsDeadSession(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.RolloverNudgeHours = rolloverHoursPtr(24)
	sup.lastSession = time.Now()
	now := time.Now()
	gw.sessions = []SessionInfo{
		{Alias: "shipwright", State: "dead", CreatedAt: now.Add(-48 * time.Hour)},
	}

	_, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)

	require.Equal(t, 0, nudgesTo(gw, "shipwright"), "a dead session cannot act on a nudge")
}

func TestRolloverNudgeThrottlesRepeatNudgeWithinInterval(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.RolloverNudgeHours = rolloverHoursPtr(24)
	sup.lastSession = time.Now()
	t0 := time.Now()
	gw.sessions = []SessionInfo{
		{Alias: "shipwright", State: "active", CreatedAt: t0.Add(-25 * time.Hour)},
	}

	_, err := sup.Tick(context.Background(), t0)
	require.NoError(t, err)
	_, err = sup.Tick(context.Background(), t0.Add(time.Minute))
	require.NoError(t, err)

	require.Equal(t, 1, nudgesTo(gw, "shipwright"), "repeat ticks within the throttle window must not re-nudge")
}

func TestRolloverNudgeRefiresAfterThrottleIntervalElapses(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.RolloverNudgeHours = rolloverHoursPtr(24)
	sup.lastSession = time.Now()
	t0 := time.Now()
	gw.sessions = []SessionInfo{
		{Alias: "shipwright", State: "active", CreatedAt: t0.Add(-25 * time.Hour)},
	}

	_, err := sup.Tick(context.Background(), t0)
	require.NoError(t, err)
	// Still alive and still old 25h later — past the 24h re-nudge throttle.
	_, err = sup.Tick(context.Background(), t0.Add(25*time.Hour))
	require.NoError(t, err)

	require.Equal(t, 2, nudgesTo(gw, "shipwright"), "past the throttle window, an unchanged overdue session is re-nudged")
}

func TestRolloverNudgeDisabledWhenHoursZero(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.RolloverNudgeHours = rolloverHoursPtr(0)
	sup.lastSession = time.Now()
	now := time.Now()
	gw.sessions = []SessionInfo{
		{Alias: "shipwright", State: "active", CreatedAt: now.Add(-999 * time.Hour)},
	}

	_, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)

	require.Equal(t, 0, nudgesTo(gw, "shipwright"))
}

func TestRolloverNudgeSilentWhenDisabledSwitchSet(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.cfg.RolloverNudgeHours = rolloverHoursPtr(24)
	now := time.Now()
	gw.sessions = []SessionInfo{
		{Alias: "shipwright", State: "active", CreatedAt: now.Add(-999 * time.Hour)},
	}
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "DISABLED"), []byte("halt"), 0o644))

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.False(t, ran)

	require.Equal(t, 0, nudgesTo(gw, "shipwright"))
}

// TestRolloverNudgeFiresImmediatelyOnFreshSupervisorWhenAlreadyOverdue is a
// deliberate divergence from TestFreshSupervisorDoesNotNudgeSurveyorImmediately
// (surveyor_nudge_test.go): the surveyor cadence is a fixed-agent timer that a
// brand-new process arms one interval out from construction, so it must NOT
// fire immediately. The rollover nudge is grounded in a session's own external
// age (its CreatedAt from `gc session list`), not an internal software
// counter — a session that is already past the threshold IS genuinely
// overdue right now, restart or not, so it must fire on the very first tick.
func TestRolloverNudgeFiresImmediatelyOnFreshSupervisorWhenAlreadyOverdue(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.RolloverNudgeHours = rolloverHoursPtr(24)
	sup.lastSession = time.Now() // heartbeat cadence not due — isolate the rollover path
	now := time.Now()
	gw.sessions = []SessionInfo{
		{Alias: "shipwright", State: "active", CreatedAt: now.Add(-48 * time.Hour)},
	}

	_, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)

	require.Equal(t, 1, nudgesTo(gw, "shipwright"),
		"a session already past the age threshold at process start is genuinely overdue and must nudge immediately, unlike the surveyor's fixed-agent cadence")
}

func TestRolloverNudgeHandlesListSessionsErrorGracefully(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.RolloverNudgeHours = rolloverHoursPtr(24)
	sup.lastSession = time.Now()
	gw.sessionsErr = fmt.Errorf("gc session list: boom")

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err, "a list-sessions failure must not fail the whole tick")
	require.False(t, ran)
	require.Empty(t, gw.nudges)
}

// TestRolloverNudgeIgnoresSessionWithZeroCreatedAt guards the same bug class
// as the durable-scheduling-state fix in wake_test.go: a zero time.Time makes
// now.Sub(zero) enormous, which would make every session with an unparsed or
// missing CreatedAt look infinitely old and nudge on the very first tick.
func TestRolloverNudgeIgnoresSessionWithZeroCreatedAt(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.RolloverNudgeHours = rolloverHoursPtr(24)
	sup.lastSession = time.Now()
	gw.sessions = []SessionInfo{
		{Alias: "shipwright", State: "active"}, // CreatedAt left at zero value
	}

	_, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)

	require.Equal(t, 0, nudgesTo(gw, "shipwright"), "a session with no reported creation time must not be treated as infinitely old")
}

func TestRolloverNudgeHandlesMultipleSessionsIndependently(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.RolloverNudgeHours = rolloverHoursPtr(24)
	sup.lastSession = time.Now()
	now := time.Now()
	gw.sessions = []SessionInfo{
		{Alias: "shipwright", State: "active", CreatedAt: now.Add(-25 * time.Hour)},
		{Alias: "trade-analyst", State: "active", CreatedAt: now.Add(-1 * time.Hour)},
	}

	_, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)

	require.Equal(t, 1, nudgesTo(gw, "shipwright"))
	require.Equal(t, 0, nudgesTo(gw, "trade-analyst"))
}

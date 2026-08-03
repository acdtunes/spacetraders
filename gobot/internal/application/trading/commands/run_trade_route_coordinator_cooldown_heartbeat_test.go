package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- sp-39hjn COMPLEMENT: a long jump-cooldown wait is not a silent log gap ---
//
// waitForJumpCooldown logs ONCE at the start of the wait, then — before — slept the whole
// ETA-scaled budget in a single blocking call, emitting nothing until the cooldown ended. On
// a far sp-tp5c3 tour a cooldown budget can exceed the sp-m3122 watchdog's 12-min (720s)
// stall threshold, so LatestLogTimestamps (the newest container-log line, which the
// log-derived liveness uses) goes stale and any log-derived consumer reads the parked,
// legitimately-waiting hull as hung. The fix chunks a long wait and emits a periodic
// heartbeat between the chunks so the tour's liveness signal keeps advancing.

// (B8) A cooldown wait longer than the heartbeat interval emits periodic heartbeats — the
// log-derived liveness signal keeps advancing instead of sitting silent past the watchdog's
// stall threshold. travelFakeClock makes each chunk's sleep instant, so the whole wait runs
// in the test without real time passing.
func TestWaitForJumpCooldown_LongWait_EmitsPeriodicHeartbeat(t *testing.T) {
	logger := &tradeCaptureLogger{}
	h := &RunTradeRouteCoordinatorHandler{clock: &travelFakeClock{}}

	// A far-tour cooldown well past the heartbeat interval: budget ~= 1875s, chunked into
	// cooldownHeartbeatInterval-sized pieces -> several heartbeats between the chunks.
	err := h.waitForJumpCooldown(tradeCtx(logger), 1500)

	require.NoError(t, err)
	heartbeats := 0
	for _, m := range logger.messages {
		if strings.Contains(m, "heartbeat") {
			heartbeats++
		}
	}
	require.GreaterOrEqual(t, heartbeats, 2, "a long cooldown wait must emit periodic liveness heartbeats, not sit silent")
}

// (B8 guard) An ordinary short cooldown wait (within one heartbeat interval) stays a single
// silent sleep — no heartbeat noise, byte-identical to the previous single-sleep behavior.
func TestWaitForJumpCooldown_ShortWait_NoHeartbeat(t *testing.T) {
	logger := &tradeCaptureLogger{}
	h := &RunTradeRouteCoordinatorHandler{clock: &travelFakeClock{}}

	err := h.waitForJumpCooldown(tradeCtx(logger), 60) // budget 75s << interval

	require.NoError(t, err)
	for _, m := range logger.messages {
		require.NotContains(t, m, "heartbeat", "a short cooldown wait must not emit heartbeats")
	}
}

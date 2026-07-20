package watchkeeper

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- sp-cu1ou: the hourly cap must not FREEZE the heartbeat cadence anchor ---
//
// recordWake is the only writer of last_session (the cadence anchor), and it
// runs only INSIDE bridgeWake. The hourly-cap check returns from Tick BEFORE
// bridgeWake, so a heartbeat wake that is due but suppressed by the cap never
// advances the anchor. A frozen anchor keeps effectiveNextWake in the past, so
// the gate reports "cadence due" on every 30s poll and the captain is delivered
// a wake at the cap-clearance rate (~60/cap minutes) instead of once per
// heartbeat interval — the over-wake this bead fixes. The fix advances (and
// persists) the anchor on a cap-suppressed heartbeat, but ONLY while the
// delivery channel is healthy, so a genuine delivery outage still lets the
// never-wake ceiling fire (see TestCapSuppressedWakeDuringDeliveryOutage...).

// setupSaturatedOverdue builds a bridge supervisor whose hourly cap is already
// full and whose heartbeat is long overdue, so the very next Tick's wake gate
// fires on cadence and is suppressed by the cap.
func setupSaturatedOverdue(t *testing.T, now time.Time) (*Supervisor, *captainStores, *fakeGateway) {
	t.Helper()
	sup, s, gw := newBridgeSupervisor(t)
	sup.cfg.MaxSessionsPerHour = 3
	sup.cfg.HeartbeatMinutes = 60
	sup.cfg.MaxWakeIntervalMinutes = 180
	for i := 0; i < 3; i++ { // saturate the hourly cap with real, recent sessions
		sup.sessionStarts = append(sup.sessionStarts, now.Add(-time.Duration(i+1)*time.Minute))
	}
	sup.lastSession = now.Add(-90 * time.Minute) // heartbeat (60m) long overdue
	return sup, s, gw
}

// TestCapSuppressedHeartbeatAdvancesCadenceAnchor is the failing-first cadence
// proof: a heartbeat wake suppressed by the hourly cap must still advance and
// persist the cadence anchor, so it does not re-fire every poll.
func TestCapSuppressedHeartbeatAdvancesCadenceAnchor(t *testing.T) {
	now := time.Now()
	sup, s, gw := setupSaturatedOverdue(t, now)

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.False(t, ran, "the hourly cap suppresses delivery this tick")
	require.Empty(t, gw.mails, "a capped tick never delivers a wake")
	require.Empty(t, gw.nudges)

	// THE FIX: the cadence anchor advanced to now, so the next poll is not due.
	require.WithinDuration(t, now, sup.lastSession, time.Second,
		"a cap-suppressed heartbeat must advance the cadence anchor, not leave it frozen in the past")

	// RULINGS #2: the advance must survive a restart, so a reopened supervisor
	// does not immediately re-treat the heartbeat as overdue.
	reloaded, err := loadSupervisorState(s.dir + "/state/" + supervisorStateFile)
	require.NoError(t, err)
	require.WithinDuration(t, now, reloaded.LastSession, time.Second,
		"the advanced cadence anchor must be persisted (RULINGS #2)")
}

// TestCapSuppressedHeartbeatDoesNotReFireOncePerPoll is the behavioral proof of
// the over-wake: after a cap-suppressed heartbeat, the anchor is serviced, so
// even the moment the cap frees the captain is not woken again until a full
// heartbeat interval has elapsed — not immediately, and not every poll.
func TestCapSuppressedHeartbeatDoesNotReFireOncePerPoll(t *testing.T) {
	now := time.Now()
	sup, _, gw := setupSaturatedOverdue(t, now)

	// The overdue heartbeat fires the gate but is suppressed by the full cap.
	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.False(t, ran)

	// The cap frees completely (the recent sessions age out of the window).
	sup.sessionStarts = nil

	// One poll later, with the cap wide open: the captain must NOT be woken,
	// because the heartbeat was serviced on the capped tick and is not due for
	// another ~60m. On the frozen-anchor bug the anchor is still 90m in the past,
	// so this delivers a spurious heartbeat immediately.
	ran, err = sup.Tick(context.Background(), now.Add(30*time.Second))
	require.NoError(t, err)
	require.False(t, ran, "a serviced heartbeat must not re-fire the instant the cap frees")
	require.Empty(t, gw.nudges, "no spurious heartbeat after the cap frees")

	// It DOES fire again once a real heartbeat interval has elapsed (cadence intact).
	ran, err = sup.Tick(context.Background(), now.Add(61*time.Minute))
	require.NoError(t, err)
	require.True(t, ran, "the heartbeat resumes on its normal cadence one interval later")
	require.Len(t, gw.nudges, 1)
	require.Contains(t, gw.nudges[0][1], "heartbeat")
}

// TestCapSuppressedWakeDuringDeliveryOutageDoesNotAdvanceAnchor is the
// never-suppress-a-wake ceiling proof: the cadence-anchor advance is gated to a
// HEALTHY delivery channel. During a delivery outage the anchor must stay
// frozen so the never-wake ceiling (base+MaxWakeIntervalMinutes) remains a hard
// deadline — advancing it here would let a dead channel push the guaranteed
// wake out indefinitely, the exact suppression the ceiling exists to prevent.
func TestCapSuppressedWakeDuringDeliveryOutageDoesNotAdvanceAnchor(t *testing.T) {
	now := time.Now()
	sup, _, gw := setupSaturatedOverdue(t, now)

	// A standing delivery outage whose backoff window has already elapsed, so
	// deliveryThrottled lets this tick through to the cap check.
	frozenAnchor := sup.lastSession
	sup.deliveryFailures = 2
	sup.firstDeliveryFailure = now.Add(-30 * time.Minute)
	sup.lastDeliveryAttempt = now.Add(-30 * time.Minute)

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.False(t, ran)
	require.Empty(t, gw.nudges)

	require.Equal(t, frozenAnchor, sup.lastSession,
		"during a delivery outage the anchor must NOT advance, so the never-wake ceiling still fires")

	// The frozen anchor keeps the ceiling a fixed deadline: the gate is still due
	// now (well within base+180) and stays due, so the wake is delayed, never
	// suppressed past the ceiling.
	next := effectiveNextWake(wakeGateInput{
		LastSession:            sup.lastSession,
		HeartbeatMinutes:       sup.cfg.HeartbeatMinutes,
		MaxWakeIntervalMinutes: sup.cfg.MaxWakeIntervalMinutes,
	})
	require.False(t, now.Before(next), "the ceiling deadline stays anchored to the frozen last_session")
}

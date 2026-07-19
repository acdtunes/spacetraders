package watchkeeper

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type captainStores struct {
	store    captain.EventStore
	playerID int
	dir      string
}

func TestTickRespectsHourlyCap(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	now := time.Now()
	for i := 0; i < 6; i++ {
		sup.sessionStarts = append(sup.sessionStarts, now.Add(-time.Duration(i)*time.Minute))
	}
	recordEvent(t, s, captain.EventShipIdle)

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.False(t, ran, "cap reached: events queue, no session")
	require.Empty(t, gw.nudges, "capped tick emits no wake signals")
	require.Empty(t, gw.mails)
}

// --- sp-sk68 wake model: Tick-level gate behavior ---
//
// Tick's wake GATE (when to wake) is independent from delivery: routine
// events like workflow.finished must not force a full captain session, while
// bridgeWake still receives the full unprocessed batch once a wake IS
// decided.

func TestTickDefersRoutineEventsWithoutWaking(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now() // heartbeat cadence nowhere near due
	recordEvent(t, s, captain.EventWorkflowFinished)

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran, "a deferred-only event (workflow.finished) must not force an immediate wake")
	require.Empty(t, gw.mails)
	require.Empty(t, gw.nudges)

	left, err := s.store.FindUnprocessed(context.Background(), s.playerID, 10)
	require.NoError(t, err)
	require.Len(t, left, 1, "the deferred event stays unprocessed, riding whichever wake fires next")
}

// sp-soh9 FIX B: when the wake gate keeps firing into a full hourly cap, the
// "session cap reached" line must NOT spam once per tick. It is rate-limited
// to one line per cap engagement window, so three consecutive capped ticks
// emit it once — while keeping the informative content intact.
func TestCapReachedLogIsRateLimitedAcrossConsecutiveTicks(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	now := time.Now()
	for i := 0; i < 6; i++ {
		sup.sessionStarts = append(sup.sessionStarts, now.Add(-time.Duration(i)*time.Minute))
	}
	// Overdue heartbeat: the gate wakes on every tick, and because the cap
	// blocks bridgeWake, last_session never advances — so it stays overdue and
	// keeps re-waking into the cap on every tick.
	sup.lastSession = now.Add(-2 * time.Hour)

	out := captureOutput(t, func() {
		for k := 0; k < 3; k++ {
			ran, err := sup.Tick(context.Background(), now.Add(time.Duration(k)*30*time.Second))
			require.NoError(t, err)
			require.False(t, ran, "the cap suppresses the wake on every tick")
		}
	})

	require.Equal(t, 1, strings.Count(out, "session cap reached"),
		"the cap-reached line must be emitted once per engagement, not once per tick")
	require.Empty(t, gw.mails, "a capped tick never delivers")
	require.Empty(t, gw.nudges)
}

func TestTickHourlyCapSuppressesEvenAnInterruptEvent(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	now := time.Now()
	for i := 0; i < 6; i++ {
		sup.sessionStarts = append(sup.sessionStarts, now.Add(-time.Duration(i)*time.Minute))
	}
	recordEvent(t, s, captain.EventWorkflowFailed) // interrupt type: the gate alone would wake

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.False(t, ran, "hourly cap must still suppress a wake even when an interrupt event is queued")
	require.Empty(t, gw.nudges)
	require.Empty(t, gw.mails)

	left, err := s.store.FindUnprocessed(context.Background(), s.playerID, 10)
	require.NoError(t, err)
	require.Len(t, left, 1, "the interrupt event stays queued, to be delivered once the cap allows a session")
}

// --- sp-ftgq: the hourly cap must track NEW sessions only ---
//
// Only firstWake (delivery of a never-before-mailed event batch) charges the
// hourly cap, via recordNewSession. renudge (re-poking the captain about an
// event already mailed) and the empty-heartbeat nudge still call recordWake
// for their cadence/backoff bookkeeping, but recordWake does not append to
// sessionStarts — so a backlog of unacked events re-nudging, or a quiet fleet
// heartbeating, can never by itself exhaust the cap and starve a genuinely
// new event.

func TestNewEventWakeNotStarvedByRenudgeBacklog(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.cfg.MaxSessionsPerHour = 2 // tight enough that renudges alone would exhaust it if they wrongly counted
	recordEvent(t, s, captain.EventWorkflowFailed)

	t0 := time.Now()
	sup.lastSession = t0
	ran, err := sup.Tick(context.Background(), t0)
	require.NoError(t, err)
	require.True(t, ran, "first wake for the new event")
	require.Len(t, gw.mails, 1)

	// Two re-nudge cycles of the SAME still-unacked event, each past the ack
	// timeout — renudges alone must never fill the 2-session cap.
	_, err = sup.Tick(context.Background(), t0.Add(11*time.Minute))
	require.NoError(t, err)
	_, err = sup.Tick(context.Background(), t0.Add(22*time.Minute))
	require.NoError(t, err)
	require.Len(t, gw.nudges, 3, "initial wake + two re-nudges must all actually fire")

	// A genuinely new, distinct event arrives. It must be delivered, not
	// starved by the re-nudge traffic above.
	recordEvent(t, s, captain.EventContainerCrashLoop)
	ran, err = sup.Tick(context.Background(), t0.Add(23*time.Minute))
	require.NoError(t, err)
	require.True(t, ran, "a genuinely new event must wake the captain even though re-nudges ran twice")
	require.Len(t, gw.mails, 2, "the new event is mailed, not silently dropped")
}

func TestRenudgeCyclesDoNotConsumeHourlyCap(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	recordEvent(t, s, captain.EventWorkflowFailed)

	t0 := time.Now()
	sup.lastSession = t0
	_, err := sup.Tick(context.Background(), t0)
	require.NoError(t, err)
	require.Len(t, sup.sessionStarts, 1, "the first wake charges exactly one cap slot")

	for i := 1; i <= 10; i++ {
		_, err = sup.Tick(context.Background(), t0.Add(time.Duration(i)*11*time.Minute))
		require.NoError(t, err)
		// Checked after every cycle, not just at the end: sessionsInLastHour
		// prunes entries older than an hour, so a naive post-loop check (110
		// minutes elapsed) would trivially "pass" once the original charge
		// ages out on its own. The real invariant is that a re-nudge never
		// ADDS to the count, at any point along the way.
		require.LessOrEqual(t, len(sup.sessionStarts), 1,
			"a re-nudge cycle must never grow the hourly-cap count beyond the original new-session charge")
	}
	require.Greater(t, len(gw.nudges), 1, "re-nudges still actually fire")
}

func TestHeartbeatsDoNotConsumeHourlyCap(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.HeartbeatMinutes = 1

	t0 := time.Now()
	sup.lastSession = t0.Add(-2 * time.Hour) // heartbeat overdue immediately

	for i := 0; i < 7; i++ {
		ran, err := sup.Tick(context.Background(), t0.Add(time.Duration(i)*2*time.Minute))
		require.NoError(t, err)
		require.True(t, ran, "heartbeat %d must fire: no events, cap untouched by heartbeats", i)
	}
	require.Len(t, gw.nudges, 7, "all seven heartbeats delivered")
	require.Empty(t, gw.mails)
	require.Empty(t, sup.sessionStarts, "heartbeats must never charge the hourly session cap")
}

func TestHourlyCapStillBoundsGenuinelyNewSessions(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.cfg.MaxSessionsPerHour = 2
	t0 := time.Now()
	sup.lastSession = t0

	recordEvent(t, s, captain.EventWorkflowFailed)
	ran, err := sup.Tick(context.Background(), t0)
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, gw.mails, 1)

	recordEvent(t, s, captain.EventContainerCrashLoop)
	ran, err = sup.Tick(context.Background(), t0.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ran, "second genuinely new event still within the cap")
	require.Len(t, gw.mails, 2)

	recordEvent(t, s, captain.EventWorkflowFailed) // a third distinct new event
	ran, err = sup.Tick(context.Background(), t0.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, ran, "the cap must still block a third genuinely new session within the hour")
	require.Len(t, gw.mails, 2, "the third event is not delivered while capped")

	left, err := s.store.FindUnprocessed(context.Background(), s.playerID, 10)
	require.NoError(t, err)
	require.Len(t, left, 3, "all three events remain queued: two delivered-but-unacked, one capped")
}

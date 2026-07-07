package captainsup

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
// The bug: Tick woke on ANY unprocessed event, so routine events like
// workflow.finished triggered a full captain session. The fix changes only
// the wake GATE (when to wake), never delivery: bridgeWake still receives
// the full unprocessed batch once a wake is decided.

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

// sp-soh9 FIX B: when the wake gate keeps firing into a full hourly cap (as it
// did every 30s during the one-shot-alarm regression), the "session cap
// reached" line must NOT spam once per tick. It is rate-limited to one line per
// cap engagement window, so three consecutive capped ticks emit it once — while
// keeping the informative content intact.
func TestCapReachedLogIsRateLimitedAcrossConsecutiveTicks(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	now := time.Now()
	for i := 0; i < 6; i++ {
		sup.sessionStarts = append(sup.sessionStarts, now.Add(-time.Duration(i)*time.Minute))
	}
	// Overdue heartbeat: the gate wakes on every tick, and because the cap
	// blocks bridgeWake, last_session never advances — so it stays overdue and
	// keeps re-waking into the cap, exactly the live spam loop.
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

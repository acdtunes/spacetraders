package captainsup

import (
	"context"
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

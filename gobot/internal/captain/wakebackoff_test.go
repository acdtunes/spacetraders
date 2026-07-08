package watchkeeper

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// flakyGateway records every delivery attempt and, while fail is set, errors
// on SendMail/Nudge exactly the way a broken gc/bd substrate did during the
// sp-sk68 incident. SessionAlive is healthy so ensureCaptainAlive is a clean
// no-op and never pollutes the wake-attempt counts.
type flakyGateway struct {
	fail       bool
	mailCalls  int
	nudgeCalls int
	mails      [][]string
	nudges     [][]string
}

func (g *flakyGateway) SendMail(_ context.Context, to, subject, body string) error {
	g.mailCalls++
	if g.fail {
		return errors.New("gc failed: bd-router: cannot find the real bd binary on PATH (set BD_REAL)")
	}
	g.mails = append(g.mails, []string{to, subject, body})
	return nil
}

func (g *flakyGateway) Nudge(_ context.Context, alias, text string) error {
	g.nudgeCalls++
	if g.fail {
		return errors.New("gc failed: bd-router: cannot find the real bd binary on PATH (set BD_REAL)")
	}
	g.nudges = append(g.nudges, []string{alias, text})
	return nil
}

func (g *flakyGateway) SessionAlive(_ context.Context, _ string) (bool, error) { return true, nil }

// ListSessions returns no sessions, so nudgeRolloverOnAge (sp-0zx9) is also a
// clean no-op here and never pollutes the wake-attempt counts this fake
// exists to measure.
func (g *flakyGateway) ListSessions(_ context.Context) ([]SessionInfo, error) { return nil, nil }

// attempts counts total gateway delivery calls. While failing, each bridgeWake
// attempt makes exactly one call (the first Send errors and returns), so this
// equals the number of delivery attempts.
func (g *flakyGateway) attempts() int { return g.mailCalls + g.nudgeCalls }

// D1 part (1): a failed wake delivery records NO progress — last_session must
// not advance (a failed wake is not a session), the renudge ladder must not
// arm, the hourly cap must not be charged — and the outage must be logged with
// one distinct, grep-able line per failed attempt.
func TestFailedWakeDeliveryRecordsNoProgressAndLogsLoudly(t *testing.T) {
	sup, _, _ := newBridgeSupervisor(t)
	gw := &flakyGateway{fail: true}
	sup.gw = gw
	frozen := time.Now().Add(-2 * time.Hour) // heartbeat overdue
	sup.lastSession = frozen

	out := captureOutput(t, func() {
		_, _ = sup.Tick(context.Background(), time.Now())
	})

	require.Equal(t, 1, gw.attempts(), "exactly one delivery attempt was made")
	require.Equal(t, frozen, sup.lastSession, "a failed wake must NOT advance last_session")
	require.Empty(t, sup.renudges, "a failed wake must NOT arm the renudge ladder")
	require.Empty(t, sup.sessionStarts, "a failed wake must NOT be charged to the hourly cap")
	require.Equal(t, 1, sup.deliveryFailures, "the consecutive-failure counter advances")
	require.Contains(t, out, "WAKE DELIVERY FAILING (1 consecutive",
		"the outage must be grep-able and distinct from generic tick errors")
}

// D1 part (2): with a persistent outage and cadence overdue every tick, the
// attempt count grows sub-linearly. Delivery is retried on an exponential
// backoff of poll*2^n capped at 15m (poll=30s here), so attempts land at
// offsets 0, 60, 180, 420, 900, 1800 — not on all 61 ticks.
func TestWakeDeliveryExponentialBackoffSchedule(t *testing.T) {
	sup, _, _ := newBridgeSupervisor(t)
	gw := &flakyGateway{fail: true}
	sup.gw = gw
	t0 := time.Now()
	sup.lastSession = t0.Add(-2 * time.Hour) // cadence due at every tick

	var attemptOffsets []int
	_ = captureOutput(t, func() {
		for k := 0; k <= 60; k++ { // t0 .. t0+1800s, 30s apart
			before := gw.attempts()
			_, _ = sup.Tick(context.Background(), t0.Add(time.Duration(k)*30*time.Second))
			if gw.attempts() > before {
				attemptOffsets = append(attemptOffsets, k*30)
			}
		}
	})

	require.Equal(t, []int{0, 60, 180, 420, 900, 1800}, attemptOffsets,
		"delivery attempts back off as poll*2^n capped at 15m (gaps 60,120,240,480,900,900)")
}

// D1 part (3): the backoff must never regress interrupt delivery. A brand-new
// interrupt-class event (one not present at the last attempt) bypasses the
// backoff window and is delivered on the very next tick.
func TestNewInterruptEventBypassesWakeBackoff(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	gw := &flakyGateway{fail: true}
	sup.gw = gw
	t0 := time.Now()
	sup.lastSession = t0.Add(-2 * time.Hour)

	// Two failed heartbeat attempts (t0, t0+60) put us deep in backoff: after
	// the 2nd failure the next scheduled slot is t0+180.
	_ = captureOutput(t, func() {
		_, _ = sup.Tick(context.Background(), t0)
		_, _ = sup.Tick(context.Background(), t0.Add(60*time.Second))
	})
	require.Equal(t, 2, gw.attempts())
	require.Equal(t, 2, sup.deliveryFailures)

	// Mid-backoff at t0+90s (well before the t0+180 slot) a new interrupt
	// arrives. It must be attempted immediately despite the open backoff window.
	recordEvent(t, s, captain.EventWorkflowFailed)
	_ = captureOutput(t, func() {
		_, _ = sup.Tick(context.Background(), t0.Add(90*time.Second))
	})
	require.Equal(t, 3, gw.attempts(),
		"a new interrupt event bypasses the backoff and is delivered immediately")

	// But the SAME interrupt, still unacked, does not re-bypass on the next
	// tick — it is no longer new, so the backoff throttle re-applies.
	_ = captureOutput(t, func() {
		_, _ = sup.Tick(context.Background(), t0.Add(120*time.Second))
	})
	require.Equal(t, 3, gw.attempts(),
		"an unchanged interrupt does not defeat the backoff every tick")
}

// D1 part (4): once the channel recovers, a successful wake records progress
// (last_session advances, hourly cap charged) and zeroes the failure counter,
// so the next overdue cadence attempts immediately with no residual backoff.
func TestWakeDeliveryRecoveryResetsBackoffAndRecordsWake(t *testing.T) {
	sup, _, _ := newBridgeSupervisor(t)
	gw := &flakyGateway{fail: true}
	sup.gw = gw
	t0 := time.Now()
	sup.lastSession = t0.Add(-2 * time.Hour)

	_ = captureOutput(t, func() {
		_, _ = sup.Tick(context.Background(), t0)                      // fail #1
		_, _ = sup.Tick(context.Background(), t0.Add(60*time.Second))  // fail #2
		_, _ = sup.Tick(context.Background(), t0.Add(180*time.Second)) // fail #3
	})
	require.Equal(t, 3, sup.deliveryFailures)
	require.Empty(t, sup.sessionStarts)

	// Channel recovers. The next scheduled slot after fail #3 (t0+180) is
	// t0+180+backoff(3)=t0+420.
	gw.fail = false
	recoverAt := t0.Add(420 * time.Second)
	_ = captureOutput(t, func() {
		ran, err := sup.Tick(context.Background(), recoverAt)
		require.NoError(t, err)
		require.True(t, ran)
	})
	require.Equal(t, recoverAt, sup.lastSession, "a successful wake advances last_session")
	require.Equal(t, 0, sup.deliveryFailures, "recovery zeroes the failure counter")
	require.Len(t, sup.sessionStarts, 1, "the successful wake is charged to the hourly cap")

	// Immediately overdue again: with the counter reset there is no residual
	// backoff, so the next cadence wake attempts on the very next tick.
	nudgesBefore := gw.nudgeCalls
	sup.lastSession = recoverAt.Add(-2 * time.Hour)
	_ = captureOutput(t, func() {
		ran, _ := sup.Tick(context.Background(), recoverAt.Add(time.Second))
		require.True(t, ran)
	})
	require.Equal(t, nudgesBefore+1, gw.nudgeCalls,
		"failure counter reset: the next overdue cadence attempts immediately")
}

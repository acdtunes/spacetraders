package captainsup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func metaReviewDaysPtr(n int) *int { return &n }

func nudgesTo(gw *fakeGateway, alias string) int {
	n := 0
	for _, nu := range gw.nudges {
		if nu[0] == alias {
			n++
		}
	}
	return n
}

// TestSurveyorNudgeDueAlertsAdmiralWhenSurveyorDead is the surveyor half of the
// sp-qv71 ruling: a survey is due but the standing surveyor session is dead, so
// the watchkeeper ALERTS the Admiral (mail + grep-able log) for a manual
// relaunch and NEVER spawns. It must not mail/nudge the dead surveyor, and a
// second poll within the throttle window must not re-alert.
func TestSurveyorNudgeDueAlertsAdmiralWhenSurveyorDead(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.MetaReviewDays = metaReviewDaysPtr(7)
	sup.lastSession = time.Now()                               // heartbeat not due — isolate the surveyor path
	sup.lastSurveyorNudge = time.Now().Add(-8 * 24 * time.Hour) // survey cadence elapsed
	gw.alive = map[string]bool{"captain": true, surveyorAgent: false}

	t0 := time.Now()
	out := captureOutput(t, func() {
		_, err := sup.Tick(context.Background(), t0)
		require.NoError(t, err)
		_, err = sup.Tick(context.Background(), t0.Add(time.Minute)) // next poll, still dead
		require.NoError(t, err)
	})

	require.Equal(t, 1, mailsTo(gw, "human"),
		"a dead surveyor at cadence alerts the Admiral once, not every poll")
	require.Equal(t, 0, mailsTo(gw, surveyorAgent), "no survey-due mail to a dead surveyor")
	require.Equal(t, 0, nudgesTo(gw, surveyorAgent), "no nudge to a dead surveyor")
	require.Contains(t, out, "STANDING SESSION DOWN", "a dead surveyor emits a grep-able local log line")
}

// TestSurveyorNudgeReachesSurveyorOnceRelaunched proves the dead branch does
// NOT advance the cadence: it stays due through the outage, so the very first
// tick after a human relaunches the surveyor delivers the survey-due mail+nudge
// to the now-live session.
func TestSurveyorNudgeReachesSurveyorOnceRelaunched(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.MetaReviewDays = metaReviewDaysPtr(7)
	sup.lastSession = time.Now()
	sup.lastSurveyorNudge = time.Now().Add(-8 * 24 * time.Hour) // cadence elapsed
	gw.alive = map[string]bool{"captain": true, surveyorAgent: false}

	t0 := time.Now()
	_ = captureOutput(t, func() {
		_, err := sup.Tick(context.Background(), t0) // dead → alert, cadence NOT advanced
		require.NoError(t, err)
	})
	require.Equal(t, 0, mailsTo(gw, surveyorAgent))

	gw.alive[surveyorAgent] = true // human relaunches the surveyor
	_, err := sup.Tick(context.Background(), t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, mailsTo(gw, surveyorAgent),
		"cadence stays due through the outage so the relaunched surveyor still gets its survey-due mail")
	require.Equal(t, 1, nudgesTo(gw, surveyorAgent))
}

func TestSurveyorNudgeMailsLiveSurveyorWithoutAlert(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.MetaReviewDays = metaReviewDaysPtr(7)
	sup.lastSurveyorNudge = time.Now().Add(-8 * 24 * time.Hour) // cadence elapsed
	gw.alive = map[string]bool{"captain": true, surveyorAgent: true}

	_, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)

	require.Equal(t, 1, mailsTo(gw, surveyorAgent))
	require.Equal(t, 1, nudgesTo(gw, surveyorAgent))
	require.Equal(t, 0, mailsTo(gw, "human"), "a live surveyor needs no Admiral alert")
}

func TestSurveyorNudgeNotDueDoesNothing(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.MetaReviewDays = metaReviewDaysPtr(7)
	now := time.Now()
	sup.lastSurveyorNudge = now

	_, err := sup.Tick(context.Background(), now.Add(time.Hour))
	require.NoError(t, err)

	require.Equal(t, 0, mailsTo(gw, surveyorAgent))
	require.Equal(t, 0, nudgesTo(gw, surveyorAgent))
}

func TestSurveyorNudgeSecondTickSameDayDoesNothing(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.MetaReviewDays = metaReviewDaysPtr(7)
	sup.lastSurveyorNudge = time.Now().Add(-8 * 24 * time.Hour) // cadence elapsed for the first tick
	now := time.Now()

	_, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	_, err = sup.Tick(context.Background(), now.Add(time.Minute))
	require.NoError(t, err)

	require.Equal(t, 1, mailsTo(gw, surveyorAgent))
	require.Equal(t, 1, nudgesTo(gw, surveyorAgent))
}

func TestSurveyorNudgeDisabledWhenMetaReviewDaysZero(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.MetaReviewDays = metaReviewDaysPtr(0)

	_, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)

	require.Equal(t, 0, mailsTo(gw, surveyorAgent))
	require.Equal(t, 0, nudgesTo(gw, surveyorAgent))
}

func TestSurveyorNudgeSilentWhenDisabledSwitchSet(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.cfg.MetaReviewDays = metaReviewDaysPtr(7)
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "DISABLED"), []byte("halt"), 0o644))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran)

	require.Equal(t, 0, mailsTo(gw, surveyorAgent))
	require.Equal(t, 0, nudgesTo(gw, surveyorAgent))
}

// TestFreshSupervisorDoesNotNudgeSurveyorImmediately guards the second half
// of the reported bug: a brand-new process (meta_review_days configured, no
// prior state on disk) must arm the survey cadence one full interval out,
// not treat it as already due at construction.
func TestFreshSupervisorDoesNotNudgeSurveyorImmediately(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.MetaReviewDays = metaReviewDaysPtr(7)

	_, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)

	require.Equal(t, 0, mailsTo(gw, surveyorAgent), "fresh process start must not treat survey cadence as immediately due")
	require.Equal(t, 0, nudgesTo(gw, surveyorAgent))
}

// TestSurveyorNudgeStateSurvivesRestart mirrors
// TestSupervisorRestartRoundTripsHeartbeatStateAndDoesNotRefire (wake_test.go)
// for the survey cadence: a restart right after a nudge fired must not
// re-fire just because the new process's in-memory clock reset to zero.
func TestSurveyorNudgeStateSurvivesRestart(t *testing.T) {
	db, playerID, store := setupDB(t)
	dir := t.TempDir()

	sup1, gw1 := reopenBridgeSupervisor(t, db, playerID, store, dir)
	sup1.cfg.MetaReviewDays = metaReviewDaysPtr(7)
	sup1.lastSurveyorNudge = time.Now().Add(-8 * 24 * time.Hour)
	_, err := sup1.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, mailsTo(gw1, surveyorAgent))

	sup2, gw2 := reopenBridgeSupervisor(t, db, playerID, store, dir)
	sup2.cfg.MetaReviewDays = metaReviewDaysPtr(7)
	_, err = sup2.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.Equal(t, 0, mailsTo(gw2, surveyorAgent), "restart must not re-fire a survey nudge just sent before the restart")
}

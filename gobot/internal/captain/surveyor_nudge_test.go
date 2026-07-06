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

func spawnedAgent(gw *fakeGateway, agent string) bool {
	for _, sp := range gw.spawned {
		if sp[0] == agent {
			return true
		}
	}
	return false
}

func TestSurveyorNudgeDueSpawnsWhenDeadAndSignalsOnce(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.MetaReviewDays = metaReviewDaysPtr(7)
	sup.lastSession = time.Now().Add(-2 * time.Hour)           // heartbeat due, so Tick reports ran=true
	sup.lastSurveyorNudge = time.Now().Add(-8 * 24 * time.Hour) // cadence elapsed
	gw.alive = map[string]bool{"captain": true}

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)

	require.True(t, spawnedAgent(gw, surveyorAgent))
	require.Equal(t, 1, mailsTo(gw, surveyorAgent))
	require.Equal(t, 1, nudgesTo(gw, surveyorAgent))
}

func TestSurveyorNudgeDoesNotSpawnWhenAlive(t *testing.T) {
	sup, _, gw := newBridgeSupervisor(t)
	sup.cfg.MetaReviewDays = metaReviewDaysPtr(7)
	sup.lastSurveyorNudge = time.Now().Add(-8 * 24 * time.Hour) // cadence elapsed
	gw.alive = map[string]bool{"captain": true, surveyorAgent: true}

	_, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)

	require.False(t, spawnedAgent(gw, surveyorAgent))
	require.Equal(t, 1, mailsTo(gw, surveyorAgent))
	require.Equal(t, 1, nudgesTo(gw, surveyorAgent))
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
	require.False(t, spawnedAgent(gw, surveyorAgent))
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
	require.False(t, spawnedAgent(gw, surveyorAgent))
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

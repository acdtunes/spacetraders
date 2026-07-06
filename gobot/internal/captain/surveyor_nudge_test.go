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

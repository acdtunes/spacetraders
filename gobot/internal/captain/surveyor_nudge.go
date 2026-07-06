package captainsup

import (
	"context"
	"fmt"
	"time"
)

const surveyorAgent = "surveyor"

func (s *Supervisor) metaReviewDays() int {
	if s.cfg.MetaReviewDays == nil {
		return 0
	}
	return *s.cfg.MetaReviewDays
}

func (s *Supervisor) nudgeSurveyorOnCadence(ctx context.Context, now time.Time) {
	if s.metaReviewDays() <= 0 {
		return
	}
	if !s.surveyorNudgeDue(now) {
		return
	}
	alive, err := s.gw.SessionAlive(ctx, surveyorAgent)
	if err == nil && !alive {
		if spawnErr := s.gw.SpawnSession(ctx, surveyorAgent, surveyorAgent); spawnErr != nil {
			_ = s.gw.SendMail(ctx, s.cfg.AdmiralAlias, "surveyor respawn failed", spawnErr.Error())
			return
		}
	}
	body := composeSurveyorMail(s.metaReviewDays(), s.lastSurveyorNudge, now)
	s.lastSurveyorNudge = now
	s.saveState()
	if mailErr := s.gw.SendMail(ctx, surveyorAgent, "survey due", body); mailErr != nil {
		return
	}
	_ = s.gw.Nudge(ctx, surveyorAgent, "survey due — check mail")
}

func (s *Supervisor) surveyorNudgeDue(now time.Time) bool {
	if s.lastSurveyorNudge.IsZero() {
		return true
	}
	return now.Sub(s.lastSurveyorNudge) >= time.Duration(s.metaReviewDays())*24*time.Hour
}

func composeSurveyorMail(cadenceDays int, last, now time.Time) string {
	window := fmt.Sprintf("%dd cadence", cadenceDays)
	if !last.IsZero() {
		window = fmt.Sprintf("%dd cadence; %s since last survey", cadenceDays, now.Sub(last).Round(time.Hour))
	}
	return "Run your full survey ritual: mail check, captain report telemetry, decision/consult/friction sampling, session health, template-vs-practice drift; then file evidence beads to the shipwright queue and one digest mail to the Admiral. " + window
}

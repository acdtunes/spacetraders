package watchkeeper

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
		// City policy: no auto-spawn. A dead surveyor is alerted to the Admiral
		// for manual relaunch, never respawned. Deliberately do NOT advance
		// lastSurveyorNudge here: the cadence stays due so the survey-due mail +
		// nudge below reach the surveyor on the first tick after a human brings
		// it back. alertSessionDown throttles the alert so a surveyor that stays
		// dead across polls does not spam the Admiral.
		s.alertSessionDown(ctx, now, surveyorAgent, "surveyor session down",
			"A survey is due but the standing surveyor session is not alive. City policy "+
				"is no auto-spawn: relaunch it manually (acd run / acd prime "+surveyorAgent+").")
		return
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

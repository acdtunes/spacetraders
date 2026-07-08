package watchkeeper

import (
	"context"
	"fmt"
	"time"
)

// rolloverRenudgeInterval throttles the repeat "rollover due" nudge for the
// SAME session (sp-0zx9): a long-lived session stays past the age threshold
// on every 30s poll, so without a per-alias cooldown it would be re-nudged on
// every tick. Mirrors sessionDownAlertInterval's rationale (respawn.go): one
// nudge per this window keeps an overdue session visible without spamming it.
const rolloverRenudgeInterval = 24 * time.Hour

func (s *Supervisor) rolloverNudgeHours() int {
	if s.cfg.RolloverNudgeHours == nil {
		return 0
	}
	return *s.cfg.RolloverNudgeHours
}

// nudgeRolloverOnAge nudges every live gc-managed session whose own reported
// age (CreatedAt from `gc session list`) has crossed RolloverNudgeHours,
// prompting it to hand off and restart with a fresh context (sp-0zx9).
//
// This deliberately diverges from nudgeSurveyorOnCadence (surveyor_nudge.go),
// which is grounded in a single persisted timer (lastSurveyorNudge) for one
// fixed agent, armed one interval out from construction so a fresh process
// never fires immediately. Here the due-check is grounded in EACH session's
// own external age, not an internal software counter: a session that is
// already past the threshold when the watchkeeper starts (or restarts) is
// genuinely overdue right now, so it fires on the very first tick. The
// per-alias rolloverNudged map (in-memory only, mirroring
// sessionDownAlerted/alertSessionDown in respawn.go) throttles only REPEAT
// nudges to the same still-overdue session, not the first one.
func (s *Supervisor) nudgeRolloverOnAge(ctx context.Context, now time.Time) {
	hours := s.rolloverNudgeHours()
	if hours <= 0 {
		return
	}
	sessions, err := s.gw.ListSessions(ctx)
	if err != nil {
		fmt.Printf("watchkeeper: list-sessions failed (skipping rollover check): %v\n", err)
		return
	}
	threshold := time.Duration(hours) * time.Hour
	for _, sess := range sessions {
		if !isAliveState(sess.State) {
			continue
		}
		if sess.CreatedAt.IsZero() {
			// No reported creation time: never treat this as infinitely old.
			continue
		}
		age := now.Sub(sess.CreatedAt)
		if age < threshold {
			continue
		}
		if !s.rolloverNudgeDue(sess.Alias, now) {
			continue
		}
		s.markRolloverNudged(sess.Alias, now)
		if err := s.gw.Nudge(ctx, sess.Alias, composeRolloverNudge(sess.Alias, age)); err != nil {
			fmt.Printf("watchkeeper: rollover nudge to %q failed: %v\n", sess.Alias, err)
		}
	}
}

// rolloverNudgeDue reports whether session alias is due for a (repeat)
// rollover nudge: either it has never been nudged, or the last nudge is
// further back than rolloverRenudgeInterval.
func (s *Supervisor) rolloverNudgeDue(alias string, now time.Time) bool {
	last, ok := s.rolloverNudged[alias]
	if !ok {
		return true
	}
	return now.Sub(last) >= rolloverRenudgeInterval
}

func (s *Supervisor) markRolloverNudged(alias string, now time.Time) {
	if s.rolloverNudged == nil {
		s.rolloverNudged = map[string]time.Time{}
	}
	s.rolloverNudged[alias] = now
}

// isAliveState reports whether a gc-reported session state counts as "alive"
// for watchkeeper purposes. Mirrors the literal check already used in
// CityGateway.SessionAlive/ListSessions (gc.go).
func isAliveState(state string) bool {
	return state == "active" || state == "running"
}

func composeRolloverNudge(_ string, age time.Duration) string {
	return fmt.Sprintf(
		"rollover due — this session is %s old; hand off and restart with a fresh context to avoid running into the weekly quota blind (see captain tokens).",
		age.Round(time.Minute))
}

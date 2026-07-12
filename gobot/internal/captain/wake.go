package watchkeeper

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// cityGateway is the driven port the bridge wake path uses; *CityGateway
// satisfies it. Kept narrow so tests can substitute a recording fake.
type cityGateway interface {
	SendMail(ctx context.Context, to, subject, body string) error
	Nudge(ctx context.Context, alias, text string) error
	SessionAlive(ctx context.Context, alias string) (bool, error)
}

// SetCity wires the city adapters for bridge mode without changing the
// constructor signature legacy tests depend on.
func (s *Supervisor) SetCity(gw *CityGateway, bc *BeadsClient) {
	s.gw = gw
	s.bc = bc
}

// nudgeCooldown is the minimum spacing between successive non-interrupt wake
// nudges. During deploy churn many events queue across consecutive poll ticks;
// without this gate firstWake fired a fresh mail+nudge on EVERY tick that saw
// any newly-arrived event and flooded — and ultimately stalled — the captain
// session (sp-o8wi). Events arriving inside the window are NOT dropped: they
// stay unmailed and ride the next allowed firstWake as one accumulated batch.
// A never-mailed interrupt-class event bypasses the cooldown entirely — an
// interrupt is never delayed. The clock (s.lastNudge) is persisted, so a
// watchkeeper restart mid-window does not reset it and re-storm on boot.
const nudgeCooldown = 3 * time.Minute

// bridgeWake replaces the legacy prompt+runner session with visible city
// signals: one event mail + nudge, re-nudges for unacked events, and an
// Admiral escalation when the captain stays unresponsive.
func (s *Supervisor) bridgeWake(ctx context.Context, now time.Time, events []*captain.Event, policy WakePolicy) (bool, error) {
	if s.renudges == nil {
		s.renudges = map[int64]int{}
	}
	if s.escalated == nil {
		s.escalated = map[int64]bool{}
	}
	s.pruneWakeState(events)

	agent := s.cfg.CaptainAgent
	if len(events) == 0 {
		if err := s.gw.Nudge(ctx, agent, "heartbeat — no events"); err != nil {
			return true, err
		}
		s.recordWake(now)
		return true, nil
	}

	if s.hasUnmailedEvents(events) {
		// Coalesce a storm of newly-arrived events into at most one non-interrupt
		// firstWake per nudgeCooldown window. Deferring leaves the events unmailed
		// (no renudges entry) so they accumulate and the next allowed firstWake
		// carries the whole batch; a never-mailed interrupt bypasses the gate.
		if !s.firstWakeDue(now, events, policy) {
			return false, nil
		}
		return s.firstWake(ctx, now, agent, events)
	}

	ackTimeout := time.Duration(s.cfg.AckTimeoutMinutes) * time.Minute
	if now.Sub(s.lastSession) < ackTimeout {
		return false, nil
	}
	return s.renudge(ctx, now, events)
}

// firstWakeDue reports whether a batch that contains never-mailed events may
// deliver its wake mail+nudge now, or must defer to coalesce. A never-mailed
// interrupt-class event always fires immediately (interrupts are never
// delayed). Otherwise the batch fires only once nudgeCooldown has elapsed since
// the last firstWake nudge; until then the events stay unmailed and accumulate,
// so the eventual firstWake carries the whole batch as one nudge (sp-o8wi).
func (s *Supervisor) firstWakeDue(now time.Time, events []*captain.Event, policy WakePolicy) bool {
	if s.hasUnmailedInterrupt(events, policy) {
		return true
	}
	return now.Sub(s.lastNudge) >= nudgeCooldown
}

func (s *Supervisor) firstWake(ctx context.Context, now time.Time, agent string, events []*captain.Event) (bool, error) {
	subject, body := composeWakeMail(s.cfg.PlayerID, events, now)
	// Prepend the sp-g2w6 fleet+financial briefing (fail-open: an empty block on
	// any read failure or when disabled — never blocks the wake). composeBriefing
	// runs BEFORE recordWake stamps lastSession, so its "since last wake" deltas
	// still measure the gap to the PREVIOUS wake.
	if brief := s.composeBriefing(ctx, now); brief != "" {
		body = brief + "\n" + body
	}
	if err := s.gw.SendMail(ctx, agent, subject, body); err != nil {
		return true, err
	}
	nudge := fmt.Sprintf("wake: %d events + heartbeat — check mail", len(events))
	if err := s.gw.Nudge(ctx, agent, nudge); err != nil {
		return true, err
	}
	for _, e := range events {
		if _, ok := s.renudges[e.ID]; !ok {
			s.renudges[e.ID] = 0
		}
	}
	// Stamp the coalescing clock BEFORE recordNewSession so its saveState
	// persists it: a restart inside the next window must not reset the cooldown
	// and re-storm (sp-o8wi).
	s.lastNudge = now
	s.recordNewSession(now)
	return true, nil
}

func (s *Supervisor) renudge(ctx context.Context, now time.Time, events []*captain.Event) (bool, error) {
	needNudge := false
	for _, e := range events {
		if s.escalated[e.ID] {
			continue
		}
		s.renudges[e.ID]++
		if s.renudges[e.ID] > s.cfg.EscalateAfterRenudges {
			if err := s.escalate(ctx, e); err != nil {
				return true, err
			}
			continue
		}
		needNudge = true
	}
	if needNudge {
		nudge := fmt.Sprintf("wake: %d events still unacked — check mail", len(events))
		if err := s.gw.Nudge(ctx, s.cfg.CaptainAgent, nudge); err != nil {
			return true, err
		}
	}
	s.recordWake(now)
	return true, nil
}

func (s *Supervisor) escalate(ctx context.Context, e *captain.Event) error {
	body := fmt.Sprintf("event %d (%s) unacked after %d re-nudges", e.ID, e.Type, s.cfg.EscalateAfterRenudges)
	if err := s.gw.SendMail(ctx, s.cfg.AdmiralAlias, "captain unresponsive", body); err != nil {
		return err
	}
	s.escalated[e.ID] = true
	return nil
}

// pruneWakeState drops bookkeeping for events the captain has since acked, so
// re-arming after an ack starts from a clean slate.
func (s *Supervisor) pruneWakeState(events []*captain.Event) {
	live := make(map[int64]bool, len(events))
	for _, e := range events {
		live[e.ID] = true
	}
	for id := range s.renudges {
		if !live[id] {
			delete(s.renudges, id)
		}
	}
	for id := range s.escalated {
		if !live[id] {
			delete(s.escalated, id)
		}
	}
}

func (s *Supervisor) hasUnmailedEvents(events []*captain.Event) bool {
	for _, e := range events {
		if _, ok := s.renudges[e.ID]; !ok {
			return true
		}
	}
	return false
}

// recordWake resets the heartbeat/backoff clocks and persists scheduling
// state, exactly as a legacy session start did. It is called on every
// successful wake delivery — firstWake, renudge, and the empty-heartbeat
// nudge alike — so it also clears the delivery-failure backoff (sp-sk68 D1):
// any delivered wake proves the channel is healthy again, regardless of
// whether it carried a new event.
//
// recordWake deliberately does NOT charge the hourly session cap. Only a
// genuinely new event batch does that — see recordNewSession (sp-ftgq): a
// re-nudge of an already-mailed event or a no-op heartbeat is not a new
// captain session, and must not compete with genuinely new events for the
// cap that exists to bound runaway NEW-session creation. Before this split,
// every wake delivery charged the cap, so a backlog of unacked events being
// re-nudged (or a quiet fleet's heartbeats) could exhaust it on its own and
// starve a brand-new event of ever waking the captain.
func (s *Supervisor) recordWake(now time.Time) {
	s.lastSession = now
	s.deliveryFailures = 0
	s.firstDeliveryFailure = time.Time{}
	s.lastDeliveryAttempt = time.Time{}
	s.lastAttemptInterrupts = nil
	s.lastAttemptCreditsAbove = false
	s.lastAttemptCreditsBelow = false
	s.saveState()
}

// recordNewSession charges one hourly session-cap slot in addition to
// everything recordWake does. Call this ONLY for the delivery of a
// genuinely new (never-before-mailed) event batch — i.e. firstWake — so the
// cap tracks what it is meant to bound: new captain sessions, not the
// re-nudge/heartbeat traffic that keeps an already-notified captain honest
// (sp-ftgq).
func (s *Supervisor) recordNewSession(now time.Time) {
	s.sessionStarts = append(s.sessionStarts, now)
	s.recordWake(now)
}

// deliveryThrottled reports whether this tick's wake attempt should be skipped
// because a prior delivery failed and the exponential backoff window has not
// yet elapsed (sp-sk68 D1). It never throttles when there have been zero
// consecutive failures (the healthy path), and it always lets a brand-new
// interrupt-class event through so interrupt delivery is never regressed.
func (s *Supervisor) deliveryThrottled(now time.Time, events []*captain.Event, policy WakePolicy) bool {
	if s.deliveryFailures == 0 {
		return false
	}
	if s.hasNewInterrupt(events, policy) {
		return false
	}
	// A CreditsAbove/Below bound newly satisfied since the last attempt is a
	// genuine edge-triggered crossing (not a standing level), so it bypasses the
	// backoff without letting a permanently-satisfied threshold hammer a dead
	// channel every tick (sp-sk68 D4).
	if s.creditsGateNewlyCrossed(policy) {
		return false
	}
	delay := backoffDelay(s.pollInterval(), s.deliveryFailures)
	// An interrupt-class event that has never been successfully mailed inherits
	// the same deliveryFailures counter that failed heartbeat nudges accumulate;
	// without a cap its one bypassed attempt would be followed by up to the full
	// 15m heartbeat backoff before the next retry (sp-sk68 D1 follow-up). Keep an
	// undelivered interrupt prompt at 2x poll; pure-heartbeat and
	// already-delivered (renudge-only) batches keep the full ceiling.
	if s.hasUnmailedInterrupt(events, policy) {
		if capped := 2 * s.pollInterval(); delay > capped {
			delay = capped
		}
	}
	return now.Before(s.lastDeliveryAttempt.Add(delay))
}

// hasNewInterrupt reports whether the batch contains an interrupt-class event
// (classified under the current policy) whose id was not present at the last
// delivery attempt. Such an event must bypass the backoff. Comparing ids is
// policy-independent, so a mid-outage interrupt-type redeclaration cannot mask
// a genuinely new event.
func (s *Supervisor) hasNewInterrupt(events []*captain.Event, policy WakePolicy) bool {
	interrupts, _ := partitionEvents(events, policy.InterruptTypes)
	for _, e := range interrupts {
		if !s.lastAttemptInterrupts[e.ID] {
			return true
		}
	}
	return false
}

// hasUnmailedInterrupt reports whether the batch contains an interrupt-class
// event that has not yet been successfully mailed. s.renudges gains an entry
// for an event only after firstWake's mail+nudge both succeed, so an interrupt
// with no renudges entry has never reached the captain and must not sit out the
// full delivery backoff (sp-sk68 D1 follow-up).
func (s *Supervisor) hasUnmailedInterrupt(events []*captain.Event, policy WakePolicy) bool {
	interrupts, _ := partitionEvents(events, policy.InterruptTypes)
	for _, e := range interrupts {
		if _, mailed := s.renudges[e.ID]; !mailed {
			return true
		}
	}
	return false
}

// creditsGateNewlyCrossed reports whether a CreditsAbove/Below wake condition is
// satisfied now but was NOT at the last delivery attempt — a true edge-triggered
// crossing. A still-satisfied (level) bound returns false so a standing
// threshold cannot defeat the delivery backoff on every tick (sp-sk68 D4). The
// snapshot is refreshed on every actual attempt (rememberAttempt), so whenever
// deliveryFailures>0 it reflects the credit state at the most recent attempt.
func (s *Supervisor) creditsGateNewlyCrossed(policy WakePolicy) bool {
	aboveNow := policy.CreditsAbove != nil && s.lastCredits >= *policy.CreditsAbove
	belowNow := policy.CreditsBelow != nil && s.lastCredits <= *policy.CreditsBelow
	return (aboveNow && !s.lastAttemptCreditsAbove) || (belowNow && !s.lastAttemptCreditsBelow)
}

// armCreditsEdge re-arms the PRIMARY wake gate's credits edge-state each tick,
// BEFORE the gate is evaluated (sp-l6pz). It clears creditsAboveFired /
// creditsBelowFired whenever credits are OUTSIDE the corresponding bound (or the
// bound is undeclared), so the next crossing INTO the satisfied region fires
// exactly one wake. It never SETS a flag — a still-satisfied bound keeps its
// fired flag, so the gate stays quiet; setting is markCreditsFired's job, on
// delivery. The re-arm is in-memory only (no per-tick disk write): if credits
// exit and the process restarts before the next delivered wake, the first
// post-restart tick re-derives the cleared state from live credits before the
// gate runs, and the next delivered wake persists it. Deliberately independent
// of the ATTEMPT-relative lastAttemptCredits{Above,Below} snapshot, which paces
// the sp-sk68 D4 delivery-backoff bypass and is untouched here.
func (s *Supervisor) armCreditsEdge(policy WakePolicy) {
	if policy.CreditsAbove == nil || s.lastCredits < *policy.CreditsAbove {
		s.creditsAboveFired = false
	}
	if policy.CreditsBelow == nil || s.lastCredits > *policy.CreditsBelow {
		s.creditsBelowFired = false
	}
}

// markCreditsFired records that this tick's delivered wake has serviced any
// currently-satisfied CreditsAbove/Below bound, so the edge-triggered gate stays
// quiet until credits exit and re-cross it (sp-l6pz). Call ONLY after a
// successful delivery. It persists the edge-state (but only when a flag actually
// transitions, so an ordinary heartbeat wake in the neutral zone adds no
// redundant write) so a restart does not re-fire a still-satisfied bound
// (RULINGS #2). The complementary re-arm on exit is armCreditsEdge.
func (s *Supervisor) markCreditsFired(policy WakePolicy) {
	changed := false
	if policy.CreditsAbove != nil && s.lastCredits >= *policy.CreditsAbove && !s.creditsAboveFired {
		s.creditsAboveFired = true
		changed = true
	}
	if policy.CreditsBelow != nil && s.lastCredits <= *policy.CreditsBelow && !s.creditsBelowFired {
		s.creditsBelowFired = true
		changed = true
	}
	if changed {
		s.saveState()
	}
}

// rememberAttempt snapshots the interrupt-event ids present at this delivery
// attempt, so the NEXT tick can tell a genuinely new interrupt (which bypasses
// the backoff) from the same unchanged interrupt still failing to deliver.
func (s *Supervisor) rememberAttempt(events []*captain.Event, policy WakePolicy) {
	interrupts, _ := partitionEvents(events, policy.InterruptTypes)
	set := make(map[int64]bool, len(interrupts))
	for _, e := range interrupts {
		set[e.ID] = true
	}
	s.lastAttemptInterrupts = set
	s.lastAttemptCreditsAbove = policy.CreditsAbove != nil && s.lastCredits >= *policy.CreditsAbove
	s.lastAttemptCreditsBelow = policy.CreditsBelow != nil && s.lastCredits <= *policy.CreditsBelow
}

// noteDeliveryFailure records one failed wake delivery: it advances the
// consecutive-failure counter, stamps the attempt/first-failure times that
// drive the backoff, and emits ONE distinct, grep-able line per failed attempt
// so a persistent outage is unmistakable and separable from generic tick
// errors (sp-sk68 D1). It deliberately does NOT touch last_session — a failed
// wake is not a session.
func (s *Supervisor) noteDeliveryFailure(now time.Time, err error) {
	if s.deliveryFailures == 0 {
		s.firstDeliveryFailure = now
	}
	s.deliveryFailures++
	s.lastDeliveryAttempt = now
	fmt.Printf("watchkeeper: WAKE DELIVERY FAILING (%d consecutive since %s): %v\n",
		s.deliveryFailures, s.firstDeliveryFailure.Format(time.RFC3339), err)
}

func (s *Supervisor) pollInterval() time.Duration {
	return time.Duration(s.cfg.PollIntervalSeconds) * time.Second
}

// backoffDelay is the wait before the next delivery retry: pollInterval*2^n
// capped at 15 minutes, where n is the count of consecutive failures so far.
func backoffDelay(poll time.Duration, failures int) time.Duration {
	const ceiling = 15 * time.Minute
	if poll <= 0 {
		poll = 30 * time.Second
	}
	d := poll
	for i := 0; i < failures; i++ {
		d *= 2
		if d >= ceiling {
			return ceiling
		}
	}
	if d > ceiling {
		return ceiling
	}
	return d
}

func composeWakeMail(playerID int, events []*captain.Event, now time.Time) (subject, body string) {
	subject = fmt.Sprintf("wake: %d events", len(events))
	var b strings.Builder
	ids := make([]string, 0, len(events))
	for _, e := range events {
		age := now.Sub(e.CreatedAt).Round(time.Minute)
		fmt.Fprintf(&b, "%d\t%s\t%s\t%s\n", e.ID, e.Type, e.Ship, age)
		// A fired one-shot watch (sp-oyer) rides this mail as a wake.watch
		// event; annotate it with which watch tripped and whether it matched or
		// deadline-fired, so the tag is visible instead of buried in the payload.
		if e.Type == captain.EventWakeWatch {
			if desc := describeWatchFire(e.Payload); desc != "" {
				fmt.Fprintf(&b, "\t↳ %s\n", desc)
			}
		}
		// A firing Prometheus alert (sp-y0f6) rides this mail as a
		// prometheus.alert_firing event; annotate it with the alertname and
		// summary so the wake mail explains WHY without a Grafana round-trip.
		if e.Type == captain.EventPrometheusAlertFiring {
			if desc := describePrometheusAlert(e.Payload); desc != "" {
				fmt.Fprintf(&b, "\t↳ %s\n", desc)
			}
		}
		ids = append(ids, strconv.FormatInt(e.ID, 10))
	}
	fmt.Fprintf(&b, "\nack: spacetraders captain events ack --player-id %d --ids %s\n",
		playerID, strings.Join(ids, ","))
	return subject, b.String()
}

// describePrometheusAlert renders a prometheus.alert_firing event's payload
// (sp-y0f6: alertname/summary/severity, see detectPrometheusAlerts) as a
// one-line annotation for the wake mail. Returns "" on an empty or
// unparseable payload, mirroring describeWatchFire's own fail-silent shape —
// a malformed payload must not break mail composition.
func describePrometheusAlert(payload string) string {
	var p struct {
		AlertName string `json:"alertname"`
		Summary   string `json:"summary"`
		Severity  string `json:"severity"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return ""
	}
	if p.AlertName == "" {
		return ""
	}
	if p.Summary == "" {
		return p.AlertName
	}
	return fmt.Sprintf("%s: %s", p.AlertName, p.Summary)
}
